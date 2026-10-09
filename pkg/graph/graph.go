package graph

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/zimwip/goap/pkg/algo"
	"github.com/zimwip/goap/pkg/changeapi"
	"github.com/zimwip/goap/pkg/domain"
)

// TypeCatalog is what the graph needs of the type catalogue (implemented by pkg/typecat.Catalog). The types are
// qualified references ("alm@Requirement"); an unknown type has no lifecycle, validator or search declaration.
type TypeCatalog interface {
	Lifecycle(typ string) *domain.Lifecycle
	Validators(typ string) []algo.Bound
	// Attributes are the attributes of a node type, to check the values of its nodes.
	AttributeChecks(typ string) []domain.AttributeCheck
	// LinkAttributeChecks are the attributes of a link type, to check the properties of its links.
	LinkAttributeChecks(typ string) []domain.AttributeCheck
	// AttributeNames are the names of the attributes of a node type, ancestors included, and whether the type is open
	// (`additionalProperties: true`, or unknown to the catalogue): the properties of a node of a closed type are
	// attributes only.
	AttributeNames(typ string) (names []string, open bool)
	Search(typ string) []domain.SearchProperty
	// CheckNode is the existence rule of a node of namespace ns, CheckLink of a link between two node types.
	CheckNode(ns, typ string) error
	CheckLink(typ, from, to string) error
	// Structure is the hierarchy a domain tags (ADR 0054: the organisation, the project); IsA reports a type or a
	// subtype of base.
	Structure(kind string) (domain.Structure, bool)
	IsA(typ, base string) bool
	// Structures is the structures declared with the types belonging to each.
	Structures() domain.Structures
	// Requires are the links a node of a type must carry (`requires:` on a node type, ADR 0065).
	Requires(typ string) []domain.RequiredLink
	// AdminOnly reports a node type written by platform administrators only (`adminOnly:` on a node type, ADR 0068).
	AdminOnly(typ string) bool
	// Composes reports a composition link type (`compose: true`): its target is a part of its source, so its source is
	// the parent of its target (ADR 0077, merge and split work from the parent side).
	Composes(linkType string) bool
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
	// Guardians are the guardians a change may name (Change.Guardian, ADR 0098), by name: what it asks before it lands,
	// takes a sub-change or moves. A change naming a guardian missing here is refused those operations.
	Guardians map[string]Guardian
	// DefaultGuardian is the guardian a root change created without one names (empty: none); a sub-change takes its
	// parent's.
	DefaultGuardian string

	// Facets are the providers of the facets of a blackboard (domain.Blackboard.Facets) beyond the built-in ones (options,
	// active option, decision points, builtinFacets), by name: a use case adds what its conditions and actions observe
	// of a change without the graph knowing it. A provider is called inside the transaction that reads the blackboard,
	// with the change as stored: it reads nothing else.
	Facets map[string]BlackboardFacet

	// DecisionPolicy is the rule of the decision points of the changes (ADR 0067): when a ruling settles a point, when
	// only a person may rule it. Unset: domain.DefaultDecisionPolicy, a ruling of anyone settles the point. The graph
	// keeps the mechanism (open, rule, answer, ratify); pkg/decision is the policy the services plug.
	DecisionPolicy domain.DecisionPolicy

	// ReviewPolicy is the rule of who may review a change impact (ADR 0075): ImpactNodeReviewOn asks it before it writes and an
	// error refuses the review. Unset: anyone allowed to review may. pkg/verify is the policy the services plug.
	ReviewPolicy domain.ReviewPolicy

	// ItemAuthorizer, when set, is asked before AddItems stores an item whose kind asks a permission of its writer
	// (domain.RequireItemPermission, ADR 0075). It runs outside any transaction, with the change as stored. Unset: no
	// item asks anything of its writer.
	ItemAuthorizer ItemAuthorizer

	// ItemPolicy, when set, is asked about every item AddItems is about to store, with the change as stored (ADR 0075 §3):
	// a use-case rule of what may be written on a change (the oracle a verification names, the lifetime of a
	// derogation). An error refuses the write. Unset: nothing is asked. The graph knows no kind: pkg/criticality is the
	// policy the services plug.
	ItemPolicy ItemPolicy

	// Defaults gives a new change the main goal of its methodology (ADR 0096). Unset: a change starts with the goal it
	// names.
	Defaults ChangeDefaults

	// ProjectMoveGate, when set, authorizes a move of a change to another project (ADR 0091): who may move it and
	// whether its methodology applies to both projects. Nil: no check beyond the graph's own rules.
	ProjectMoveGate ProjectMoveGate

	// MaterializeEvery is how many baselines of a chain pass between two that are materialised (their state stored,
	// ADR 0056); the others are computed from the log. 0: DefaultMaterializeEvery.
	MaterializeEvery int

	// booted is set once the roots of the structures are known to exist (Bootstrap).
	booted atomic.Bool
	// states caches the states computed for baselines kept as a header only.
	states stateCache

	// DraftCacheSize is how many changes keep their folded drafts in memory (ADR 0079 §2). 0: DefaultDraftCacheSize.
	DraftCacheSize int
	// draftStates caches the drafts folded from the impact logs.
	draftStates draftCache
}

// New returns a Graph backed by repo. Every transaction goes through the guard of the graph (guardRepo, ADR 0054),
// whatever the storage.
func New(repo Repo) *Graph {
	g := &Graph{now: func() time.Time { return time.Now().UTC() }, newID: func() string { return uuid.NewString() }}
	g.repo = &guardRepo{Repo: repo, g: g}
	return g
}

// ---- Domain axis --------------------------------------------------------

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
// 0040's first-user bootstrap): what the last change applied on main wrote, read from the versions themselves.
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

// OutLinksOf returns the outgoing links of a node version.
func (g *Graph) OutLinksOf(ctx context.Context, ref domain.NodeRef) (ls []domain.Link, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { ls, err = tx.OutLinks(ctx, ref); return err })
	return
}

// LinkByID reads one link.
func (g *Graph) LinkByID(ctx context.Context, id domain.LinkID) (l domain.Link, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { l, err = tx.Link(ctx, id); return err })
	return
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

// NewChange is changeapi.NewChange (ADR 0098: the contract of the change, shared with the engine).
type NewChange = changeapi.NewChange

// CreateChange opens a change on a reference baseline. A sub-change
// (ParentID) belongs to the namespace of its parent (which must have a branch of
// its own), sees its parent's drafts and lands in its parent's log (ADR 0081).
func (g *Graph) CreateChange(ctx context.Context, in NewChange) (domain.Change, error) {
	if err := g.Bootstrap(ctx); err != nil {
		return domain.Change{}, fmt.Errorf("bootstrap: %w", err)
	}
	// a change starts with the main goal of its methodology (ADR 0096); the registry is asked before the transaction,
	// it reads the graph
	goal := in.Goal
	if g.Defaults != nil && in.Methodology != "" && goal == "" {
		dg, err := g.Defaults.DefaultGoal(ctx, in.Methodology)
		if err != nil {
			return domain.Change{}, err
		}
		goal = dg
	}
	// the guardian of the parent is asked about a sub-change before the transaction too, it reads the graph (ADR 0098)
	guardian := in.Guardian
	if in.ParentID != "" {
		parent, err := g.Change(ctx, in.ParentID)
		if err != nil {
			return domain.Change{}, err
		}
		gd, err := g.guardianOf(parent)
		if err != nil {
			return domain.Change{}, err
		}
		if guardian == "" {
			guardian = parent.Guardian
		}
		if gd != nil {
			child := domain.Change{Title: in.Title, Intent: in.Intent, Methodology: in.Methodology, Namespace: domain.NamespaceOf(in.Namespace),
				ParentID: in.ParentID, OwnerOrg: in.OwnerOrg, ProjectID: in.ProjectID, Data: in.Data, Guardian: guardian}
			if err := gd.MayCreateChild(ctx, parent, child); err != nil {
				return domain.Change{}, err
			}
		}
	} else if guardian == "" {
		guardian = g.DefaultGuardian
	}
	var c domain.Change
	err := g.repo.InTx(ctx, func(tx Tx) error {
		c = domain.Change{
			ID: domain.ChangeID(g.newID()), Title: in.Title, Intent: in.Intent, Methodology: in.Methodology, Namespace: domain.NamespaceOf(in.Namespace),
			Status: domain.ChangeDraft, BaselineID: in.BaselineID, Branch: domain.BranchOf(in.Branch), Data: in.Data, CreatedAt: g.now(),
			ParentID: in.ParentID, OwnerOrg: in.OwnerOrg, ProjectID: in.ProjectID,
		}
		c.Goal, c.Guardian = goal, guardian
		if err := g.prepareSubChange(ctx, tx, &c, &in); err != nil {
			return err
		}
		// a change is held by a unit and acts in a project (ADR 0054, 0091), never left empty: a sub-change inherits
		// its parent's (prepareSubChange), a change naming no unit is held by the root unit, and a change naming no
		// project is refused.
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

// ChangesFilter is changeapi.ChangesFilter (ADR 0098: the contract of the change, shared with the engine).
type ChangesFilter = changeapi.ChangesFilter

// ListChanges lists the changes matching a filter, in no particular order (the caller sorts and caps).
func (g *Graph) ListChanges(ctx context.Context, f ChangesFilter) ([]domain.Change, error) {
	cs, err := g.Changes(ctx)
	if err != nil {
		return nil, err
	}
	var linked map[domain.ChangeID]bool
	if f.Request != "" {
		r, err := g.Request(ctx, f.Request)
		if err != nil {
			return nil, err
		}
		linked = map[domain.ChangeID]bool{}
		for _, l := range r.Links {
			linked[l.Change] = true
		}
	}
	out := make([]domain.Change, 0, len(cs))
	for _, c := range cs {
		if f.Match(c) && (linked == nil || linked[c.ID]) {
			out = append(out, c)
		}
	}
	return out, nil
}

// ChangePatch is changeapi.ChangePatch (ADR 0098: the contract of the change, shared with the engine).
type ChangePatch = changeapi.ChangePatch

// UpdateChange patches a change header.
func (g *Graph) UpdateChange(ctx context.Context, id domain.ChangeID, p ChangePatch) (c domain.Change, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err = tx.Change(ctx, id)
		if err != nil {
			return err
		}
		edit := domain.HeaderEdit{Fields: map[string]domain.HeaderValue{}}
		if p.Title != nil && *p.Title != c.Title {
			edit.Fields["title"] = domain.HeaderValue{From: c.Title, To: *p.Title}
			c.Title = *p.Title
		}
		if p.Intent != nil && *p.Intent != c.Intent {
			edit.Fields["intent"] = domain.HeaderValue{From: c.Intent, To: *p.Intent}
			c.Intent = *p.Intent
		}
		if p.Goal != nil && *p.Goal != c.Goal {
			edit.Fields["goal"] = domain.HeaderValue{From: c.Goal, To: *p.Goal}
			c.Goal = *p.Goal
		}
		if p.Status != nil && *p.Status != c.Status {
			if !validStatusMove(c.Status, *p.Status) {
				return fmt.Errorf("change %s cannot go from %s to %s: %w", id, c.Status, *p.Status, ErrConflict)
			}
			edit.Fields["status"] = domain.HeaderValue{From: string(c.Status), To: string(*p.Status)}
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
			for k, v := range p.Data {
				if old, ok := c.Data[k]; !ok || !reflect.DeepEqual(old, v) {
					edit.Fields["data."+k] = domain.HeaderValue{From: old, To: v}
				}
			}
			maps.Copy(c.Data, p.Data)
		}
		if len(edit.Fields) > 0 {
			e, err := domain.HeaderEntry(g.newID(), c.ID, g.caller(ctx), g.now(), edit)
			if err != nil {
				return err
			}
			if _, err := tx.AppendLog(ctx, e); err != nil {
				return err
			}
		}
		return tx.PutChange(ctx, c)
	})
	return
}

// AddItems appends facts to the blackboard of a change (artifacts, decisions): the nodes
// a change acts on are its change impacts (ProposeImpact, ADR 0024). Item ids are assigned when empty.
func (g *Graph) AddItems(ctx context.Context, id domain.ChangeID, items []domain.ChangeItem) ([]domain.ChangeItem, error) {
	if err := g.authorizeItems(ctx, id, items); err != nil {
		return nil, err
	}
	var out []domain.ChangeItem
	err := g.repo.InTx(ctx, func(tx Tx) error {
		var err error
		out, err = g.addItemsTx(ctx, tx, id, items)
		return err
	})
	return out, err
}

// addItemsTx is AddItems inside a transaction, its items authorized (authorizeItems).
func (g *Graph) addItemsTx(ctx context.Context, tx Tx, id domain.ChangeID, items []domain.ChangeItem) ([]domain.ChangeItem, error) {
	out := make([]domain.ChangeItem, 0, len(items))
	c, err := tx.Change(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.Status == domain.ChangeApplied || c.Status == domain.ChangeAbandoned || c.Status == domain.ChangeCommitted {
		return nil, fmt.Errorf("change %s is %s: %w", id, c.Status, ErrConflict)
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
			return nil, fmt.Errorf("flow events are recorded by OpenFlow, AdoptFlow and DiscardFlow: %w", ErrInvalid)
		}
		if it.Kind == domain.KindDecisionPoint {
			return nil, fmt.Errorf("decision points are recorded by OpenDecision, RuleDecision, AnswerQuestion and RatifyDecision: %w", ErrInvalid)
		}
		if it.Flow != batchFlow {
			return nil, fmt.Errorf("the items of a batch belong to one flow: %w", ErrInvalid)
		}
	}
	if batchFlow != "" && c.FlowStatusOf(batchFlow) != domain.FlowOpen {
		return nil, fmt.Errorf("flow %s is not open: %w", batchFlow, ErrConflict)
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
			return nil, fmt.Errorf("item %s: %v: %w", it.ID, err, ErrInvalid)
		}
		if it.Decision != nil && !known[it.Decision.Item] {
			return nil, fmt.Errorf("decision %s targets unknown item %s: %w", it.ID, it.Decision.Item, ErrInvalid)
		}
		if err := putItem(ctx, tx, id, it); err != nil {
			return nil, err
		}
		known[it.ID] = true
		out = append(out, it)
	}
	if c.Status == domain.ChangeDraft {
		c.Status = domain.ChangeActive
		return out, tx.PutChange(ctx, c)
	}
	return out, nil
}

// ItemAuthorizer judges the write of an item whose kind asks a permission of its writer: the permission it asks, the
// change it is written on and the item. An error refuses it.
type ItemAuthorizer func(ctx context.Context, c domain.Change, it domain.ChangeItem, p domain.ItemPermission) error

// ItemPolicy judges any item about to be written on a change; an error refuses it.
type ItemPolicy func(ctx context.Context, c domain.Change, it domain.ChangeItem) error

// authorizeItems asks ItemAuthorizer about the items of kinds that ask a permission, and ItemPolicy about every item,
// before any transaction.
func (g *Graph) authorizeItems(ctx context.Context, id domain.ChangeID, items []domain.ChangeItem) error {
	if g.ItemAuthorizer == nil && g.ItemPolicy == nil {
		return nil
	}
	var c *domain.Change
	change := func() (domain.Change, error) {
		if c == nil {
			got, err := g.Change(ctx, id)
			if err != nil {
				return got, err
			}
			c = &got
		}
		return *c, nil
	}
	for _, it := range items {
		if g.ItemPolicy != nil {
			ch, err := change()
			if err != nil {
				return err
			}
			if err := g.ItemPolicy(ctx, ch, it); err != nil {
				return fmt.Errorf("%w: %w", err, ErrInvalid)
			}
		}
		p, ok := domain.ItemPermissionOf(it.Kind)
		if !ok || g.ItemAuthorizer == nil {
			continue
		}
		ch, err := change()
		if err != nil {
			return err
		}
		if err := g.ItemAuthorizer(ctx, ch, it, p); err != nil {
			return err
		}
	}
	return nil
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
		facets := g.facets(c, now)
		// the change objects the flow sees (ADR 0098): the change-scoped ones and the workspace-scoped ones of the flow
		workspaces := []string{""}
		if flow != "" {
			workspaces = append(workspaces, flow)
		}
		objs, err := tx.ChangeObjects(ctx, id, domain.ObjectFilter{Workspaces: workspaces})
		if err != nil {
			return err
		}
		if facets == nil {
			facets = map[string]any{}
		}
		facets[domain.FacetObjects] = objs
		full := c // the drafts are read with the flows of the change, the view of a flow has none
		c = c.View(flow)
		c.Nodes = nodes
		bb = domain.Blackboard{Change: c, Nodes: map[domain.NodeRef]domain.NodeView{}, Neighbors: map[domain.NodeRef]domain.Node{},
			Facets: facets, At: now}
		ix, err := g.typesAt(ctx, tx, c.BaselineID)
		if err != nil {
			return err
		}
		reader, err := g.newDraftReader(ctx, tx, full, flow, nodes)
		if err != nil {
			return err
		}
		// a draft reference is hydrated from the draft the flow sees (ADR 0079), any other from the stored version
		for _, r := range c.ReferencedNodes() {
			v, err := viewIn(ctx, tx, reader, r)
			if err != nil {
				return err
			}
			if lc := ix.lifecycleOf(v.Type); lc != nil && v.State != "" {
				v.NotLandable = !lc.Landable(v.State)
			}
			bb.Nodes[r] = v
			for _, l := range v.Out {
				if err := neighbor(ctx, tx, reader, bb.Neighbors, l.To); err != nil {
					return err
				}
			}
			for _, l := range v.In {
				if err := neighbor(ctx, tx, reader, bb.Neighbors, l.From); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return
}

func neighbor(ctx context.Context, tx Tx, reader *draftReader, into map[domain.NodeRef]domain.Node, r domain.NodeRef) error {
	if _, ok := into[r]; ok {
		return nil
	}
	n, err := reader.node(ctx, tx, r)
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
