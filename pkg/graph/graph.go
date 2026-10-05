package graph

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/zimwip/goap/pkg/algo"
	"github.com/zimwip/goap/pkg/domain"
)

// TypeCatalog is what the graph needs of the type catalogue (implemented by pkg/typecat.Catalog). The types are
// qualified references ("alm@Requirement"); an unknown type has no lifecycle, validator or search declaration.
type TypeCatalog interface {
	Lifecycle(typ string) *domain.Lifecycle
	Validators(typ string) []algo.Bound
	// Attributes are the attributes of a node type, to check the values of its nodes.
	AttributeChecks(typ string) []domain.AttributeCheck
	Search(typ string) []domain.SearchProperty
	// CheckNode is the existence rule of a node of namespace ns, CheckLink of a link between two node types.
	CheckNode(ns, typ string) error
	CheckLink(typ, from, to string) error
	// Structure is the hierarchy a domain tags (ADR 0054: the organisation, the project); IsA reports a type or a
	// subtype of base.
	Structure(kind string) (domain.Structure, bool)
	IsA(typ, base string) bool
	// Structures is both structures with the types belonging to each.
	Structures() domain.Structures
	// Requires are the links a node of a type must carry (`requires:` on a node type, ADR 0065).
	Requires(typ string) []domain.RequiredLink
}

// Graph exposes the domain and change axes.
type Graph struct {
	repo  Repo
	now   func() time.Time
	newID func() string
	// Authorizer, when set, is asked before every lifecycle transition.
	Authorizer TransitionAuthorizer
	// Types returns the type catalogue in force (ADR 0012 §2, pkg/typecat): the graph judges the nodes by it and
	// refuses the ones whose type or link type it does not resolve. Unset: an untyped graph (tests, tools).
	Types func() TypeCatalog
	// Caller names the principal behind an operation, recorded on the events of the change impacts (ADR 0029).
	// Set by the services from the authenticated principal; unset, the events carry no caller.
	Caller func(ctx context.Context) string
	// Validators are the NodeValidator plugins checked once per Apply (ADR 0048), keyed by the node types they
	// declare interest in via Types(). Unset: no plugin validators run (tests, tools).
	Validators []NodeValidator
	// ActivityGoalsMet, when set, is asked at Apply time for a change whose ActivityRef names a Process/Step/
	// Method/MethodStep (architecture plan "Activity concept"): true once that activity's own goal condition
	// holds against bb, given its compiled methodology (which pkg/graph has no access to - resolving it is the
	// caller's job, e.g. internal/registrysvc, wired from main). bb is built from this Apply's own cposts (ADR
	// 0024), not a fresh read, since it must see the change's own pending writes before they are visible outside
	// this transaction. It replaces the node-type lifecycle's Editable floor as the landing gate for that change,
	// maturity of content and state being the activity's call, not a fixed per-type flag; unset, or a change with
	// no ActivityRef, falls back to the Editable floor unchanged.
	ActivityGoalsMet func(ctx context.Context, activityRef string, bb domain.Blackboard) (bool, error)

	// Lifecycles resolves the lifecycle of the changes of a methodology and the world state their guards read (ADR
	// 0058). Unset: no change follows a lifecycle (tests, tools).
	Lifecycles ChangeLifecycles

	// ChangeAuthorizer, when set, is asked before every transition of the lifecycle of a change (ADR 0058), like
	// Authorizer is for the nodes.
	ChangeAuthorizer ChangeTransitionAuthorizer

	// MaterializeEvery is how many baselines of a chain pass between two that are materialised (their state stored,
	// ADR 0056); the others are computed from the log. 0: DefaultMaterializeEvery.
	MaterializeEvery int

	// booted is set once the roots of the structures are known to exist (Bootstrap).
	booted atomic.Bool
	// states caches the states computed for baselines kept as a header only.
	states stateCache
}

// New returns a Graph backed by repo. Every transaction goes through the guard of the graph (guardRepo, ADR 0054),
// whatever the storage.
func New(repo Repo) *Graph {
	g := &Graph{now: func() time.Time { return time.Now().UTC() }, newID: func() string { return uuid.NewString() }}
	g.repo = &guardRepo{Repo: repo, g: g}
	return g
}

// ---- Domain axis --------------------------------------------------------

// NewNode describes a node to create.
type NewNode struct {
	// Namespace of the node (default: domain.DefaultNamespace).
	Namespace  string
	Key        string
	Type       string
	Properties map[string]any
	// State is the lifecycle state of the first version (import; changes use create_node).
	State string
	// Owner is the key of the organisational unit owning the node (ADR 0054); empty: the root unit.
	Owner string
}

// CreateNode creates version 1 of a node as a change of its own (ADR 0049): every write is change-shaped, even
// an import, so it gets the same impact log, NodeValidator and lifecycle-initial-state handling an ordinary
// change gets. checkDirect's existence check is now checkNode's own job inside Commit (ix.checkNode). Unlike
// Commit, it does not enforce checkRequiredParent/checkParentInvariant (ADR 0040): those were always a
// Commit-specific producer guarantee, never made of a raw node write, and a caller importing a single node at
// a time (e.g. a demo seed landing a unit LinkOrphanUnits parents afterward) relies on that staying true.
func (g *Graph) CreateNode(ctx context.Context, in NewNode) (domain.Node, error) {
	if in.Type == "" {
		return domain.Node{}, fmt.Errorf("node type required: %w", ErrInvalid)
	}
	ns := domain.NamespaceOf(in.Namespace)
	key := in.Key
	if key == "" {
		key = g.newID()
	}
	if _, err := g.commitEdits(ctx, Commit{Namespace: ns, Title: "Import " + in.Type + " " + key, Intent: "Import " + in.Type + " " + key,
		By: "graph.import", Edits: []NodeEdit{{Key: key, Type: in.Type, Props: in.Properties, State: in.State, Owner: in.Owner, Rationale: "Import " + key}}}, false); err != nil {
		return domain.Node{}, err
	}
	return g.NodeByKey(ctx, ns, key)
}

// UpdateNode creates a new version of a node on main as a change of its own (ADR 0049). base must be the
// latest version on main; props replaces the current properties entirely (a property not repeated here is
// cleared), the same full-replace contract this had before it went through Commit (whose own NodeEdit.Props
// merges): the ones base already has and props does not repeat are set to nil to reproduce that.
func (g *Graph) UpdateNode(ctx context.Context, base domain.NodeRef, props map[string]any) (domain.Node, error) {
	cur, err := g.Node(ctx, domain.NodeRef{ID: base.ID})
	if err != nil {
		return domain.Node{}, err
	}
	merged := maps.Clone(props)
	if merged == nil {
		merged = map[string]any{}
	}
	for k := range cur.Properties {
		if _, ok := props[k]; !ok {
			merged[k] = nil
		}
	}
	ref := base
	if _, err := g.commitEdits(ctx, Commit{Namespace: cur.Namespace, Title: "Import " + cur.Key, Intent: "Import " + cur.Key, By: "graph.import",
		Edits: []NodeEdit{{Pre: &ref, Props: merged, Rationale: "Import " + cur.Key}}}, false); err != nil {
		return domain.Node{}, err
	}
	return g.Node(ctx, domain.NodeRef{ID: base.ID})
}

// Node returns a node version (latest when ref.Version == 0).
func (g *Graph) Node(ctx context.Context, ref domain.NodeRef) (n domain.Node, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { n, err = tx.Node(ctx, ref); return err })
	return
}

// NodeByKey returns the latest version of the node with the given key in a namespace.
func (g *Graph) NodeByKey(ctx context.Context, namespace, key string) (n domain.Node, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { n, err = tx.NodeByKey(ctx, namespace, key); return err })
	return
}

// NodesOfType returns the latest nodes of a type in a namespace, live on main (existence checks, e.g. ADR
// 0040's first-user bootstrap). Deliberately not baseline-scoped: EnsureUser writes with CreateNode/Link
// (import, outside of any change), which is visible on main immediately but does not itself advance a
// baseline — a baseline-scoped read (NodesIn) would miss a User created this way until an unrelated Commit
// happened to recompute one, silently defeating the first-admin bootstrap for every user after the first.
func (g *Graph) NodesOfType(ctx context.Context, namespace, typ string) (ns []domain.Node, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		all, err := tx.LatestNodes(ctx, namespace, domain.MainBranch)
		if err != nil {
			return err
		}
		for _, n := range all {
			if n.Type == typ && !n.Deleted {
				ns = append(ns, n)
			}
		}
		return nil
	})
	return
}

// OutLinksOf returns the outgoing links of a node version, live on main (not scoped to a baseline snapshot:
// an import write, e.g. EnsureUser's member_of, is visible on main immediately, before any baseline is next
// recomputed from it).
func (g *Graph) OutLinksOf(ctx context.Context, ref domain.NodeRef) (ls []domain.Link, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { ls, err = tx.OutLinks(ctx, ref); return err })
	return
}

// Link creates a link between two node versions, attributed to an open change (ADR 0049), without versioning
// either endpoint: the one kind of graph mutation that intentionally stays outside the Commit/Apply pipeline,
// because giving the source node a new version here would turn its own existing outgoing links into suspect
// links (SuspectLinks) — independently wrong, not merely inconvenient. id must name a change still accepting
// writes (changeOpen): there is no implicit change for a link, unlike CreateNode/UpdateNode, because picking
// one silently would defeat the point of a link that is deliberately not part of a reviewable change impact.
func (g *Graph) Link(ctx context.Context, id domain.ChangeID, typ string, from, to domain.NodeRef, props map[string]any) (domain.Link, error) {
	l := domain.Link{ID: domain.LinkID(g.newID()), Type: typ, From: from, To: to, Properties: props, ChangeID: id}
	err := g.repo.InTx(ctx, func(tx Tx) error {
		if _, err := changeOpen(ctx, tx, id); err != nil {
			return err
		}
		f, err := tx.Node(ctx, from)
		if err != nil {
			return err
		}
		t, err := tx.Node(ctx, to)
		if err != nil {
			return err
		}
		if g.Types != nil {
			if err := (&typeIndex{cat: g.catalog()}).checkLink(typ, f.Type, t.Type); err != nil {
				return err
			}
		}
		return tx.PutLink(ctx, l)
	})
	return l, err
}

// catalog returns the type catalogue in force.
func (g *Graph) catalog() TypeCatalog { return g.Types() }

// View hydrates a node version with its neighbourhood.
func (g *Graph) View(ctx context.Context, ref domain.NodeRef) (v domain.NodeView, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { v, err = view(ctx, tx, ref); return err })
	return
}

func view(ctx context.Context, tx Tx, ref domain.NodeRef) (domain.NodeView, error) {
	n, err := tx.Node(ctx, ref)
	if err != nil {
		return domain.NodeView{}, err
	}
	latest, err := tx.Node(ctx, domain.NodeRef{ID: ref.ID})
	if errors.Is(err, ErrNotFound) {
		latest, err = n, nil // a node the change created, not on main yet
	}
	if err != nil {
		return domain.NodeView{}, err
	}
	out, err := tx.OutLinks(ctx, n.Ref())
	if err != nil {
		return domain.NodeView{}, err
	}
	in, err := tx.InLinks(ctx, n.Ref())
	if err != nil {
		return domain.NodeView{}, err
	}
	return domain.NodeView{Node: n, Latest: latest.Version, Out: out, In: in}, nil
}

// Baseline returns a baseline.
func (g *Graph) Baseline(ctx context.Context, id domain.BaselineID) (b domain.Baseline, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { b, err = tx.Baseline(ctx, id); return err })
	return
}

// Baselines lists the baselines of a namespace, by creation date.
func (g *Graph) Baselines(ctx context.Context, namespace string) (bs []domain.Baseline, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { bs, err = tx.Baselines(ctx, namespace); return err })
	return
}

// BaselineGraph returns the nodes and links of a baseline.
func (g *Graph) BaselineGraph(ctx context.Context, id domain.BaselineID) (nodes []domain.Node, links []domain.Link, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { nodes, links, err = baselineGraphTx(ctx, tx, id); return err })
	return
}

func baselineGraphTx(ctx context.Context, tx Tx, id domain.BaselineID) ([]domain.Node, []domain.Link, error) {
	b, err := tx.Baseline(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	nodes, err := tx.NodesIn(ctx, id, "")
	if err != nil {
		return nil, nil, err
	}
	links, err := linksWithin(ctx, tx, b, nodes)
	return nodes, links, err
}

// linksWithin returns the outgoing links of the nodes whose target is in b.
func linksWithin(ctx context.Context, tx Tx, b domain.Baseline, nodes []domain.Node) ([]domain.Link, error) {
	var links []domain.Link
	for _, n := range nodes {
		out, err := tx.OutLinks(ctx, n.Ref())
		if err != nil {
			return nil, err
		}
		for _, l := range out {
			if b.Contains(l.To) {
				links = append(links, l)
			}
		}
	}
	return links, nil
}

// SuspectLinks returns the links whose source is in the baseline but whose
// target node is in the baseline at another version: the target changed
// since the link was asserted.
func (g *Graph) SuspectLinks(ctx context.Context, id domain.BaselineID) (out []domain.Link, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		b, err := tx.Baseline(ctx, id)
		if err != nil {
			return err
		}
		for nid, v := range b.Nodes {
			links, err := tx.OutLinks(ctx, domain.NodeRef{ID: nid, Version: v})
			if err != nil {
				return err
			}
			for _, l := range links {
				if tv, ok := b.Nodes[l.To.ID]; ok && tv != l.To.Version {
					out = append(out, l)
				}
			}
		}
		return nil
	})
	return
}

// ---- Change axis --------------------------------------------------------

// NewChange describes a change to open.
type NewChange struct {
	Title       string
	Intent      string
	Methodology string
	// Namespace the change acts on (default: domain.DefaultNamespace).
	Namespace  string
	BaselineID domain.BaselineID
	// Branch the change applies to (default main); it must be open. With
	// OwnBranch it is the branch the change is finally merged into.
	Branch string
	// OwnBranch gives the change a branch of its own, named after it and forked
	// from BaselineID: its versions live there until the change is merged.
	OwnBranch bool
	// BranchIntent says why the own branch exists relative to its parent (derive/revise/refine, same vocabulary
	// as an Option's); only meaningful with OwnBranch or ParentID. "" (the default) is domain.IntentDerive.
	BranchIntent domain.OptionIntent
	// ParentID makes the change a sub-change of another one (see prepareSubChange).
	ParentID domain.ChangeID
	// OwnerOrg is the key of the OrgUnit responsible for the change (empty: the default organisation).
	OwnerOrg string
	// ProjectID is the key of the ProjectUnit this change's nodes belong to (ADR 0039; empty: the root
	// project). A sub-change inherits it from its parent when unset, and must stay within the parent's
	// project when set. Selecting one before acting is a UX-level gate (ADR 0039), not enforced here.
	ProjectID string
	// ActivityRef scopes the change to one Activity (architecture plan "Activity concept"): "Request -> Create
	// Change -> Define scope -> Execute". Empty: no activity-relative gating beyond a node type's own lifecycle.
	ActivityRef string
	Data        map[string]any
}

// CreateChange opens a change on a reference baseline. A sub-change
// (ParentID) belongs to the namespace of its parent, forks its own branch from
// the branch of the parent (which must have one) and is merged into it.
func (g *Graph) CreateChange(ctx context.Context, in NewChange) (domain.Change, error) {
	if err := g.Bootstrap(ctx); err != nil {
		return domain.Change{}, fmt.Errorf("bootstrap: %w", err)
	}
	// a change whose methodology names a lifecycle starts in its initial state (ADR 0058); the registry is asked
	// before the transaction, it reads the graph
	var lifecycle, initial string
	if g.Lifecycles != nil && in.Methodology != "" {
		lc, err := g.Lifecycles.Lifecycle(ctx, in.Methodology)
		if err != nil {
			return domain.Change{}, err
		}
		if lc != nil {
			lifecycle, initial = lc.Name, lc.Initial
		}
	}
	var c domain.Change
	err := g.repo.InTx(ctx, func(tx Tx) error {
		c = domain.Change{
			ID: domain.ChangeID(g.newID()), Title: in.Title, Intent: in.Intent, Methodology: in.Methodology, Namespace: domain.NamespaceOf(in.Namespace),
			Status: domain.ChangeDraft, BaselineID: in.BaselineID, Branch: domain.BranchOf(in.Branch), Data: in.Data, CreatedAt: g.now(),
			ParentID: in.ParentID, OwnerOrg: in.OwnerOrg, ProjectID: in.ProjectID, ActivityRef: in.ActivityRef,
		}
		c.Lifecycle, c.State = lifecycle, initial
		if err := g.prepareSubChange(ctx, tx, &c, &in); err != nil {
			return err
		}
		// a change is held by a unit and acts in a project (ADR 0054), both resolved here and never left empty: a
		// sub-change inherits its parent's (prepareSubChange), a change naming none is held by the root unit and acts
		// in the default project. Requiring a caller to pick a project is a UX-level gate (the project selector, ADR
		// 0039); that the change has a real one is the graph's.
		if err := g.scopeChange(ctx, tx, &c); err != nil {
			return err
		}
		// a change naming no starting state starts from the head of its branch: the state the last change left, or
		// the empty state while the namespace has none (ADR 0056)
		if c.BaselineID == "" {
			head, err := branchHead(ctx, tx, c.Namespace, c.Branch)
			if err != nil {
				return err
			}
			c.BaselineID = head.ID
		}
		fork, err := tx.Baseline(ctx, c.BaselineID)
		if err != nil {
			return err
		}
		if fork.ID != "" && fork.Namespace != c.Namespace {
			return fmt.Errorf("change acts on namespace %s but its reference baseline %s is of namespace %s: %w",
				c.Namespace, c.BaselineID, fork.Namespace, ErrInvalid)
		}
		b, err := branchOf(ctx, tx, c.Namespace, c.Branch)
		if err != nil {
			return err
		}
		if b.Status != domain.BranchOpen {
			return fmt.Errorf("branch %s is %s: %w", b.Name, b.Status, ErrConflict)
		}
		if in.OwnBranch {
			if !domain.ValidOptionIntent(in.BranchIntent) {
				return fmt.Errorf("unknown branch intent %q: %w", in.BranchIntent, ErrInvalid)
			}
			intent := in.BranchIntent
			if intent == "" {
				intent = domain.IntentDerive
			}
			own := domain.Branch{Name: changeBranchName(c.ID), Namespace: c.Namespace, Parent: b.Name, ForkBaseline: fork.ID, Head: fork.ID,
				Origin: domain.ChangeBranchOrigin(c.ID), Intent: intent, Status: domain.BranchOpen, CreatedAt: g.now()}
			if err := tx.PutBranch(ctx, own); err != nil {
				return err
			}
			c.Branch = own.Name
		}
		return tx.PutChange(ctx, c)
	})
	return c, err
}

// Change returns a change with its items.
func (g *Graph) Change(ctx context.Context, id domain.ChangeID) (c domain.Change, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		if c, err = tx.Change(ctx, id); err != nil {
			return err
		}
		return nil
	})
	return
}

// Changes lists changes.
func (g *Graph) Changes(ctx context.Context) (cs []domain.Change, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { cs, err = tx.Changes(ctx); return err })
	return
}

// ChangesFilter narrows ListChanges; the zero value matches every change.
type ChangesFilter struct {
	Namespace string
	OwnerOrg  string
	// Status: draft, active, committed, applied, abandoned (empty: every status).
	Status []domain.ChangeStatus
}

// Match reports whether a change satisfies the filter.
func (f ChangesFilter) Match(c domain.Change) bool {
	if f.Namespace != "" && c.Namespace != f.Namespace {
		return false
	}
	if f.OwnerOrg != "" && c.OwnerOrg != f.OwnerOrg {
		return false
	}
	if len(f.Status) > 0 && !slices.Contains(f.Status, c.Status) {
		return false
	}
	return true
}

// ListChanges lists the changes matching a filter, in no particular order (the caller sorts and caps).
func (g *Graph) ListChanges(ctx context.Context, f ChangesFilter) ([]domain.Change, error) {
	cs, err := g.Changes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Change, 0, len(cs))
	for _, c := range cs {
		if f.Match(c) {
			out = append(out, c)
		}
	}
	return out, nil
}

// ChangePatch updates mutable fields of a change header. Nil fields are kept. Title and Intent are
// the current definition of the change; goap-change/reformulate is the only caller that sets them,
// after superseding the previous definition as an "intent" item so the history is kept.
type ChangePatch struct {
	Title  *string
	Intent *string
	Goal   *string
	Status *domain.ChangeStatus
	Data   map[string]any // merged
}

// UpdateChange patches a change header.
func (g *Graph) UpdateChange(ctx context.Context, id domain.ChangeID, p ChangePatch) (c domain.Change, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err = tx.Change(ctx, id)
		if err != nil {
			return err
		}
		if p.Title != nil {
			c.Title = *p.Title
		}
		if p.Intent != nil {
			c.Intent = *p.Intent
		}
		if p.Goal != nil {
			c.Goal = *p.Goal
		}
		if p.Status != nil && *p.Status != c.Status {
			if !validStatusMove(c.Status, *p.Status) {
				return fmt.Errorf("change %s cannot go from %s to %s: %w", id, c.Status, *p.Status, ErrConflict)
			}
			c.Status = *p.Status
			if c.Status == domain.ChangeAbandoned {
				if err := g.abandonSubChanges(ctx, tx, c.ID); err != nil {
					return err
				}
				if own, ok, err := ownBranch(ctx, tx, c); err != nil {
					return err
				} else if ok {
					own.Status = domain.BranchAbandoned
					if err := tx.PutBranch(ctx, own); err != nil {
						return err
					}
				}
			}
		}
		if len(p.Data) > 0 {
			if c.Data == nil {
				c.Data = map[string]any{}
			}
			maps.Copy(c.Data, p.Data)
		}
		return tx.PutChange(ctx, c)
	})
	return
}

// AddItems appends facts to the blackboard of a change (artifacts, decisions): the nodes
// a change acts on are its change impacts (AddNodes, ADR 0024). Item ids are assigned when empty.
func (g *Graph) AddItems(ctx context.Context, id domain.ChangeID, items []domain.ChangeItem) ([]domain.ChangeItem, error) {
	out := make([]domain.ChangeItem, 0, len(items))
	err := g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		if c.Status == domain.ChangeApplied || c.Status == domain.ChangeAbandoned || c.Status == domain.ChangeCommitted {
			return fmt.Errorf("change %s is %s: %w", id, c.Status, ErrConflict)
		}
		known := map[domain.ItemID]bool{}
		for _, it := range c.Items {
			known[it.ID] = true
		}
		items = slices.Clone(items)
		for i := range items {
			if items[i].Kind != domain.KindFlow {
				items[i].Flow = c.ResolveFlow(items[i].Flow) // no flow: the active option (ADR 0032 §6)
			}
		}
		batchFlow := ""
		if len(items) > 0 {
			batchFlow = items[0].Flow
		}
		for _, it := range items {
			if it.Kind == domain.KindFlow {
				return fmt.Errorf("flow events are recorded by OpenFlow, AdoptFlow and DiscardFlow: %w", ErrInvalid)
			}
			if it.Kind == domain.KindTransition {
				return fmt.Errorf("transitions are recorded by TransitionChange: %w", ErrInvalid)
			}
			if it.Kind == domain.KindDecisionPoint {
				return fmt.Errorf("decision points are recorded by OpenDecision, RuleDecision, AnswerQuestion and RatifyDecision: %w", ErrInvalid)
			}
			if it.Flow != batchFlow {
				return fmt.Errorf("the items of a batch belong to one flow: %w", ErrInvalid)
			}
		}
		if batchFlow != "" && c.FlowStatusOf(batchFlow) != domain.FlowOpen {
			return fmt.Errorf("flow %s is not open: %w", batchFlow, ErrConflict)
		}
		for i := range items {
			if items[i].ID == "" {
				items[i].ID = domain.ItemID(g.newID())
			}
		}
		for _, it := range items {
			if it.Status == "" {
				it.Status = domain.ItemProposed
			}
			it.CreatedAt = g.now()
			if err := it.Validate(); err != nil {
				return fmt.Errorf("item %s: %v: %w", it.ID, err, ErrInvalid)
			}
			if it.Decision != nil && !known[it.Decision.Item] {
				return fmt.Errorf("decision %s targets unknown item %s: %w", it.ID, it.Decision.Item, ErrInvalid)
			}
			if err := putItem(ctx, tx, id, it); err != nil {
				return err
			}
			known[it.ID] = true
			out = append(out, it)
		}
		if c.Status == domain.ChangeDraft {
			c.Status = domain.ChangeActive
			return tx.PutChange(ctx, c)
		}
		return nil
	})
	return out, err
}

// Blackboard returns the change and a hydrated view of every node it
// references, ready for condition evaluation.
func (g *Graph) Blackboard(ctx context.Context, id domain.ChangeID) (domain.Blackboard, error) {
	return g.BlackboardIn(ctx, id, "")
}

// BlackboardIn is the blackboard as seen by the process running on a flow
// branch ("" = the main flow, see domain.Change.View).
func (g *Graph) BlackboardIn(ctx context.Context, id domain.ChangeID, flow string) (bb domain.Blackboard, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		flow := c.ResolveFlow(flow)                          // no flow: the active option (ADR 0032 §6)
		nodes, err := g.newFlowNodes(tx, c, flow).nodes(ctx) // the change impacts as the flow sees them (ADR 0025)
		if err != nil {
			return err
		}
		// what the view of a flow does not carry: the options and the decision points of the change (ADR 0009)
		now := g.now()
		options, active, points := c.Options(), c.ActiveOption(), c.DecisionPointsAt(now)
		c = c.View(flow)
		c.Nodes = nodes
		bb = domain.Blackboard{Change: c, Nodes: map[domain.NodeRef]domain.NodeView{}, Neighbors: map[domain.NodeRef]domain.Node{},
			Options: options, ActiveOption: active, DecisionPoints: points, At: now}
		ix, err := g.typesAt(ctx, tx, c.BaselineID)
		if err != nil {
			return err
		}
		for _, r := range c.ReferencedNodes() {
			v, err := view(ctx, tx, r)
			if err != nil {
				return err
			}
			if lc := ix.lifecycleOf(v.Type); lc != nil && v.State != "" {
				v.Frozen = !lc.Editable(v.State)
			}
			bb.Nodes[r] = v
			for _, l := range v.Out {
				if err := neighbor(ctx, tx, bb.Neighbors, l.To); err != nil {
					return err
				}
			}
			for _, l := range v.In {
				if err := neighbor(ctx, tx, bb.Neighbors, l.From); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return
}

func neighbor(ctx context.Context, tx Tx, into map[domain.NodeRef]domain.Node, r domain.NodeRef) error {
	if _, ok := into[r]; ok {
		return nil
	}
	n, err := tx.Node(ctx, r)
	if err != nil {
		return err
	}
	into[r] = n
	return nil
}

// validStatusMove is the change status machine: draft → active → committed → applied (committing and integrating are
// done by CommitChange, IntegrateChange and Apply) or abandoned; applied and abandoned are final.
func validStatusMove(from, to domain.ChangeStatus) bool {
	switch from {
	case domain.ChangeDraft:
		return to == domain.ChangeActive || to == domain.ChangeAbandoned
	case domain.ChangeActive:
		return to == domain.ChangeDraft || to == domain.ChangeAbandoned
	case domain.ChangeCommitted:
		return to == domain.ChangeAbandoned
	}
	return false
}

// ChangeImpacts lists the versions a change starts from: the pre version of each of its change
// nodes (stored, and derived from its items).
func (g *Graph) ChangeImpacts(ctx context.Context, id domain.ChangeID) (refs []domain.NodeRef, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		for _, cn := range c.Nodes {
			if cn.Pre != nil {
				refs = append(refs, *cn.Pre)
			}
		}
		return nil
	})
	return
}

// openChangeHolders maps each node to the unapplied changes acting on it: through
// stored change impacts or through the change impacts derived from their items.
func (g *Graph) openChangeHolders(ctx context.Context, tx Tx) (map[domain.NodeID][]domain.ChangeID, error) {
	ids, err := tx.OpenChangeIDs(ctx)
	if err != nil {
		return nil, err
	}
	out := map[domain.NodeID][]domain.ChangeID{}
	for _, id := range ids {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return nil, err
		}
		for _, cn := range c.Nodes {
			for _, ref := range []*domain.NodeRef{cn.Pre, cn.Post} {
				if ref != nil && !slices.Contains(out[ref.ID], id) {
					out[ref.ID] = append(out[ref.ID], id)
				}
			}
		}
	}
	return out, nil
}

// NodeChanges lists the changes acting on a node, oldest first.
func (g *Graph) NodeChanges(ctx context.Context, node domain.NodeID) (out []domain.Change, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		stored, err := tx.NodeChangeImpacts(ctx, node)
		if err != nil {
			return err
		}
		open, err := g.openChangeHolders(ctx, tx)
		if err != nil {
			return err
		}
		for _, id := range slices.Concat(stored, open[node]) {
			if slices.ContainsFunc(out, func(c domain.Change) bool { return c.ID == id }) {
				continue
			}
			c, err := tx.Change(ctx, id)
			if err != nil {
				return err
			}
			c.Items, c.Nodes = nil, nil
			out = append(out, c)
		}
		slices.SortStableFunc(out, func(a, b domain.Change) int { return a.CreatedAt.Compare(b.CreatedAt) })
		return nil
	})
	return
}
