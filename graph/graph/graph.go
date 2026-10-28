package graph

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/zimwip/goap/contracts/algo"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/events"
	"github.com/zimwip/goap/contracts/lnk"
	"github.com/zimwip/goap/lib/cache"
	"github.com/zimwip/goap/lib/outbox"
	"github.com/zimwip/goap/lib/platform"
	"github.com/zimwip/goap/lib/typecat"
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
	// LinkAttributeValidators are the property validators plugged on the attributes of a link type (each bound to its
	// attribute), LinkTypeValidators its link validators, both in call order.
	LinkAttributeValidators(typ string) []algo.Bound
	LinkTypeValidators(typ string) []algo.Bound
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
	// Visibility is how far a version of a node of the type is seen in a state (ADR 0161 §4: the state's, else the
	// type's, else project).
	Visibility(typ, state string) string
	// Composes reports a composition link type (`compose: true`): its target is a part of its source, so its source is
	// the parent of its target (ADR 0077, merge and split work from the parent side).
	Composes(linkType string) bool
	// ItemAttributeChecks are the attributes a type of change item declares for the data of its items, and whether a
	// domain declares the type (ADR 0128); ItemTypes lists the declared types.
	ItemAttributeChecks(itemType string) ([]domain.AttributeCheck, bool)
	ItemTypes() []*typecat.ItemType
}

// Graph exposes the domain and change axes.
type Graph struct {
	repo Repo
	// base is the repository as given to New, which keeps the outbox (ADR 0136); relay delivers it, set by Observe
	base  Repo
	relay *outbox.Relay
	now   func() time.Time
	newID func() string
	// Authorizer, when set, is asked before every lifecycle transition.
	Authorizer TransitionAuthorizer
	// Resolvers are the resolvers of the kinds of source (ADR 0154: lnk.KindGraph for the graphs with a location,
	// lnk.KindFiles...), read once, at the first use of Lnk. Unset: the graph resolves its own namespaces only.
	Resolvers map[string]lnk.Resolver
	// ProjectSources is the source mapping in force on a project, by its key (ADR 0154 §8: the name a URL gives a
	// source to the declared source holding it there, inherited from the parent projects), plugged by the compositions
	// from the organisation they read (graphsvc.ProjectSources). Unset: every project reads the sources as named.
	ProjectSources func(ctx context.Context, project string) (map[string]string, error)
	// ReachOf, when set, is what the caller of a read of the current graph reads (ADR 0161 §5): the view reader keeps
	// the nodes of its reach only, and the links between them. Unset: every read sees everything (tests, tools).
	ReachOf ReachOf
	// OwnedWrites, when set, makes a change check out only the nodes its project (or one below it) owns, in the
	// namespaces it names (ADR 0161 §3). Unset: a change writes the nodes of any project.
	OwnedWrites OwnedWrites
	// Directory, when set, is where the structures (the units of the organisation, the projects) are read: they live
	// in another graph, the platform graph (ADR 0154 §9), and this one holds none (Bootstrap creates no root). The
	// owners and projects a transaction writes, the scope of a change and the walks up a structure ask it. Unset: they
	// are nodes of this graph, read in the transaction.
	Directory StructureDirectory
	// OnBranchMoved, when set, is told once a transaction that wrote a branch of a namespace committed (a reader of
	// this graph keeping a head, the organisation's directory, reads it again; ADR 0145).
	OnBranchMoved func(namespace string)
	lnk           lnkState
	// Types returns the type catalogue in force (ADR 0012 §2, pkg/typecat): the graph judges the nodes by it and
	// refuses the ones whose type or link type it does not resolve. Unset: an untyped graph (tests, tools).
	Types func() TypeCatalog
	// Caller names the principal behind an operation, recorded on the events of the change impacts (ADR 0029).
	// Set by the services from the authenticated principal; unset, the events carry no caller.
	Caller func(ctx context.Context) string
	// Origin names the graph in the ids of its events (ADR 0136), set before Observe: two graphs on one bus (the
	// platform graph and a content graph, ADR 0154 §9) each need their own, or the bus takes the events of one for
	// repeats of the other's. Empty: OutboxOrigin.
	Origin string
	// EndUser names the person an operation is done for, carried by the events of the changes (ADR 0114). Set by the
	// services from the authenticated principal; unset, the events name none.
	EndUser func(ctx context.Context) string
	// Validators are the NodeValidator plugins checked once per Apply (ADR 0048), keyed by the node types they
	// declare interest in via Types(). Unset: no plugin validators run (tests, tools).
	Validators []NodeValidator
	// SubChangeValidator, when set, is asked by CreateChange about a sub-change before it is stored, with its parent
	// as stored and the change as it is about to be (Data, Methodology, ...): an error refuses it (wrap ErrInvalid).
	// Like the guardian of a commit (ChangeLifecycles.Commit) it runs in the transaction it judges (ADR 0140) and may read
	// the graph with the context it is given.
	SubChangeValidator func(ctx context.Context, parent, child domain.Change) error

	// Facets are the providers of the facets of a blackboard (domain.Blackboard.Facets) beyond the built-in ones (options,
	// active option, decision points, builtinFacets), by name: a use case adds what its conditions and actions observe
	// of a change without the graph knowing it. A provider is called inside the transaction that reads the blackboard,
	// with the change as stored: it reads nothing else.
	Facets map[string]BlackboardFacet
	// LogFacets are the facets of a blackboard folded from the log of the change (the history of ADR 0109, folded by
	// pkg/journal), by name: the entries of their types are read with the change, the fold runs outside the
	// transaction (it may ask another service, as the registry for the methodologies).
	LogFacets map[string]LogFacet

	// DecisionPolicy is the rule of the decision points of the changes (ADR 0067): when a ruling settles a point, when
	// only a person may rule it. Unset: domain.DefaultDecisionPolicy, a ruling of anyone settles the point. The graph
	// keeps the mechanism (open, rule, answer, ratify); pkg/decision is the policy the services plug.
	DecisionPolicy domain.DecisionPolicy

	// ReviewPolicy is the rule of who may review a change impact (ADR 0075): reviewTx asks it before it writes any verdict and
	// an error refuses the review. Unset: anyone allowed to review may. pkg/verify is the policy the services plug.
	ReviewPolicy domain.ReviewPolicy

	// ItemAuthorizer, when set, is asked before AddItems stores an item whose kind asks a permission of its writer
	// (domain.RequireItemPermission, ADR 0075). It runs outside any transaction, with the change as stored. Unset: no
	// item asks anything of its writer.
	ItemAuthorizer ItemAuthorizer
	// SeatAuthorizer, when set, is asked before a person rules a decision point of a board in a seat (ADR 0132 §3): the
	// caller must hold the role of the seat on the project of the change. Nil: any seat is taken at its word.
	SeatAuthorizer SeatAuthorizer

	// ItemPolicy, when set, is asked about every item AddItems is about to store, with the change as stored (ADR 0075 §3):
	// a use-case rule of what may be written on a change (the oracle a verification names, the lifetime of a
	// derogation). An error refuses the write. Unset: nothing is asked. The graph knows no kind: pkg/criticality is the
	// policy the services plug.
	ItemPolicy ItemPolicy

	// Lifecycles resolves the lifecycle of the changes of a methodology and the world state their guards read (ADR
	// 0058). Unset: no change follows a lifecycle (tests, tools).
	Lifecycles ChangeLifecycles

	// ChangeAuthorizer, when set, is asked before every transition of the lifecycle of a change (ADR 0058), like
	// Authorizer is for the nodes.
	ChangeAuthorizer ChangeTransitionAuthorizer
	// OperationAuthorizer, when set, is asked before every operation of the catalogue (pkg/operations, ADR 0103) a
	// caller performs on a change: who may check out, edit, withdraw, review, commit or merge, by the roles of its
	// methodology. Nil: every operation is allowed.
	OperationAuthorizer OperationAuthorizer
	// ProjectMoveGate, when set, is asked before a change moves to another project (ADR 0091): who may move it and
	// whether its methodology applies to both projects. Nil: no check beyond the graph's own rules.
	ProjectMoveGate ProjectMoveGate
	// MethodologyGate, when set, is asked before a change that follows no methodology takes one (SetChangeMethodology):
	// whether the methodology applies to its project.
	MethodologyGate MethodologyGate

	// booted is set once the roots of the structures are known to exist (Bootstrap).
	booted atomic.Bool
	// Caches bounds what the graph keeps in memory between calls (caches.go): a zero field takes DefaultCaches'.
	Caches Caches
	// headers caches the headers of baselines (ADR 0137, 0140).
	headers *cache.Cache[domain.BaselineID, domain.Baseline]
	// heads are the heads of the lasting branches read by the views (heads.go), moved by the commits of this graph and
	// the baseline events of the others; selections the versions of nodes at them, keyIDs the nodes of keys
	heads      keptHeads
	selections *cache.Cache[string, keptSel]
	keyIDs     *cache.Cache[string, domain.NodeID]
	// platformMoves counts the commits of this graph that wrote a branch of the platform namespace (Graph.Sources)
	platformMoves atomic.Uint64
	// foundRefs are the references to units and projects the guard found in this graph (guardTx.inStructure)
	foundRefs *cache.Cache[string, bool]
	// versions caches the outgoing links of versions.
	versions versionCache
	// draftStates caches the views of the changes folded from their logs.
	draftStates draftCache
	// validations caches the validation of the impacts of a change on a flow, by the log position it was computed at.
	validations *cache.Cache[validationKey, validationEntry]
	// adminNS keeps, for the catalogue in force, whether a namespace holds an adminOnly type (AdminOnlyIn)
	adminNS struct {
		mu sync.Mutex
		of TypeCatalog
		in map[string]bool
	}
}

// New returns a Graph backed by repo. Every transaction goes through the guard of the graph (guardRepo, ADR 0054),
// whatever the storage.
func New(repo Repo) *Graph {
	g := &Graph{base: repo, now: func() time.Time { return time.Now().UTC() }, newID: func() string { return uuid.NewString() }}
	g.repo = &ambientRepo{Repo: &guardRepo{Repo: repo, g: g}, g: g}
	g.newCaches()
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

// BaselineHeader returns a baseline without its state (Nodes nil): what names it, never what it holds.
func (g *Graph) BaselineHeader(ctx context.Context, id domain.BaselineID) (b domain.Baseline, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { b, err = headerOf(ctx, tx, id); return err }) // "": the empty state
	b.Nodes = nil
	return
}

// BranchHeadHeader returns the head of a branch without its state (Nodes nil).
func (g *Graph) BranchHeadHeader(ctx context.Context, namespace, name string) (b domain.Baseline, err error) {
	err = g.repo.InTx(platform.ReadOnly(ctx), func(tx Tx) error { b, err = branchHeadHeader(ctx, tx, namespace, name); return err })
	return
}

// BaselineHeaders lists the baselines of a namespace, by creation date, without their states: what names them, never
// what they hold (a listing that does not grow with the graph, ADR 0140).
func (g *Graph) BaselineHeaders(ctx context.Context, namespace string) (bs []domain.Baseline, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { bs, err = tx.BaselineHeaders(ctx, namespace); return err })
	for i := range bs {
		bs[i].Nodes, bs[i].Namespace = nil, domain.NamespaceOf(namespace)
	}
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

// linksWithin returns the outgoing links of the nodes whose target is in b, read in one query.
func linksWithin(ctx context.Context, tx Tx, b domain.Baseline, nodes []domain.Node) ([]domain.Link, error) {
	refs := make([]domain.NodeRef, len(nodes))
	for i, n := range nodes {
		refs[i] = n.Ref()
	}
	if len(refs) == 0 {
		return nil, nil
	}
	out, err := tx.LinksFrom(ctx, refs)
	if err != nil {
		return nil, err
	}
	links := out[:0]
	for _, l := range out {
		if b.Contains(l.To) {
			links = append(links, l)
		}
	}
	return links, nil
}

// SuspectLinks returns the links whose source is in the baseline but whose
// target node is in the baseline at another version: the target changed
// since the link was asserted; or whose target of the namespace the baseline
// no longer holds: it was deleted (ADR 0172).
func (g *Graph) SuspectLinks(ctx context.Context, id domain.BaselineID) (out []domain.Link, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		b, err := tx.Baseline(ctx, id)
		if err != nil {
			return err
		}
		if len(b.Nodes) == 0 {
			return nil
		}
		links, err := tx.LinksFrom(ctx, refsOf(b.Nodes))
		if err != nil {
			return err
		}
		var gone []domain.NodeRef
		for _, l := range links {
			if tv, ok := b.Nodes[l.To.ID]; ok && tv != l.To.Version {
				out = append(out, l)
			} else if !ok && !l.External() {
				gone = append(gone, l.To)
			}
		}
		if len(gone) == 0 {
			return nil
		}
		// a target of the namespace the baseline does not hold was deleted (ADR 0172): the link is suspect too; one of
		// another namespace is not the baseline's to judge
		targets, err := tx.NodesAt(ctx, gone, "")
		if err != nil {
			return err
		}
		deleted := map[domain.NodeID]bool{}
		for _, n := range targets {
			if domain.NamespaceOf(n.Namespace) == domain.NamespaceOf(b.Namespace) {
				deleted[n.ID] = true
			}
		}
		for _, l := range links {
			if _, ok := b.Nodes[l.To.ID]; !ok && deleted[l.To.ID] {
				out = append(out, l)
			}
		}
		return nil
	})
	return
}

// ---- Change axis --------------------------------------------------------

// CreateChange opens a change on a reference baseline. A sub-change
// (ParentID) belongs to the namespace of its parent (which must have a branch of
// its own), sees its parent's drafts and commits in its parent's log (ADR 0081).
func (g *Graph) CreateChange(ctx context.Context, in NewChange) (c domain.Change, err error) {
	if err := g.Bootstrap(ctx); err != nil {
		return domain.Change{}, fmt.Errorf("bootstrap: %w", err)
	}
	// one transaction (ADR 0140): the validator of a sub-change judges the parent the sub-change is stored under
	err = g.inOne(ctx, func(ctx context.Context) error {
		c, err = g.createChange(ctx, in)
		return err
	})
	return c, err
}

func (g *Graph) createChange(ctx context.Context, in NewChange) (domain.Change, error) {
	// the unit and the project are references to nodes: logical URLs (ADR 0154 §9), a bare key naming the node of its
	// structure
	owner, err := g.StructureRef(domain.StructureOrganisation, in.OwnerOrg)
	if err != nil {
		return domain.Change{}, fmt.Errorf("owner of the change: %w", err)
	}
	project, err := g.StructureRef(domain.StructureProject, in.ProjectID)
	if err != nil {
		return domain.Change{}, fmt.Errorf("project of the change: %w", err)
	}
	in.OwnerOrg, in.ProjectID = owner.String(), project.String()
	// a change whose methodology names a lifecycle starts in its initial state (ADR 0058); the registry, asked in the
	// transaction of the creation, may read the graph
	var lifecycle, initial, goal string
	goal = in.Goal
	// the version of the methodology the change follows (ADR 0113): a sub-change stays on its parent's, a new change
	// takes the latest published one; it keeps it while it runs
	version := in.MethodologyVersion
	if version == "" && in.Methodology != "" && in.ParentID != "" {
		if parent, err := g.Change(ctx, in.ParentID); err == nil && parent.Methodology == in.Methodology {
			version = parent.MethodologyVersion
		}
	}
	if g.Lifecycles != nil && in.Methodology != "" {
		v, err := g.Lifecycles.MethodologyVersion(ctx, domain.MethodologyRef(in.Methodology, version))
		if err != nil {
			return domain.Change{}, err
		}
		version = v
	}
	ref := domain.MethodologyRef(in.Methodology, version)
	if g.Lifecycles != nil && in.Methodology != "" {
		if goal == "" {
			dg, err := g.Lifecycles.DefaultGoal(ctx, ref)
			if err != nil {
				return domain.Change{}, err
			}
			goal = dg
		}
		lc, err := g.Lifecycles.Lifecycle(ctx, ref)
		if err != nil {
			return domain.Change{}, err
		}
		if lc != nil {
			lifecycle, initial = lc.Name, lc.Initial
		}
	}
	// the guardian chain (ADR 0106) is computed once, here, and saved on the change
	guardians, err := g.guardianChain(ctx, in, version)
	if err != nil {
		return domain.Change{}, err
	}
	// the validator of sub-changes, in the transaction of the creation (it may read the graph)
	if g.SubChangeValidator != nil && in.ParentID != "" {
		parent, err := g.Change(ctx, in.ParentID)
		if err != nil {
			return domain.Change{}, err
		}
		child := domain.Change{Title: in.Title, Intent: in.Intent, Methodology: in.Methodology, MethodologyVersion: version, Namespace: domain.NamespaceOf(in.Namespace),
			ParentID: in.ParentID, OwnerOrg: owner, ProjectID: project, Data: in.Data}
		if err := g.SubChangeValidator(ctx, parent, child); err != nil {
			return domain.Change{}, err
		}
	}
	var c domain.Change
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c = domain.Change{
			ID: domain.ChangeID(g.newID()), Title: in.Title, Intent: in.Intent, Methodology: in.Methodology, MethodologyVersion: version, Namespace: domain.NamespaceOf(in.Namespace),
			Status: domain.ChangeDraft, BaselineID: in.BaselineID, Branch: domain.BranchOf(in.Branch), Data: in.Data, CreatedAt: g.now(),
			ParentID: in.ParentID, OwnerOrg: owner, ProjectID: project,
		}
		c.Lifecycle, c.State, c.Goal, c.Guardians = lifecycle, initial, goal, guardians
		// no one knows the id of the change before the transaction commits: its own lock needs no statement (a
		// sub-change still takes its parent's tree)
		if k, ok := tx.(interface{ newChange(domain.ChangeID) }); ok {
			k.newChange(c.ID)
		}
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
		if c.BaselineID == "" && c.ParentID == "" {
			head, err := branchHeadHeader(ctx, tx, c.Namespace, c.Branch)
			if err != nil {
				return err
			}
			c.BaselineID = head.ID
		}
		fork, err := headerOf(ctx, tx, c.BaselineID)
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
			// the branch of a change holds no baseline (ADR 0140): its fork, a baseline of a lasting branch, or the point
			// of the branch of its parent change it is forked at (ADR 0103)
			own := domain.Branch{Ephemeral: true, Name: changeBranchName(c.ID), Namespace: c.Namespace, Parent: b.Name, ForkBaseline: fork.ID,
				Origin: domain.ChangeBranchOrigin(c.ID), Intent: intent, Status: domain.BranchOpen, CreatedAt: g.now()}
			if b.Ephemeral {
				if own.ForkBaseline, own.ForkSeq, err = forkPoint(ctx, tx, b); err != nil {
					return err
				}
			}
			if err := tx.PutBranch(ctx, own); err != nil {
				return err
			}
			c.Branch = own.Name
		}
		if err := tx.PutChange(ctx, c); err != nil {
			return err
		}
		g.raise(ctx, tx, events.ChangeCreated, c, nil)
		return nil
	})
	return c, err
}

// HasChange reports a change this graph holds (its header only; graphsvc.Router asks it to route a call, ADR 0154 §9).
func (g *Graph) HasChange(ctx context.Context, id domain.ChangeID) (found bool, err error) {
	err = g.repo.InTx(platform.ReadOnly(ctx), func(tx Tx) error {
		_, err := tx.ChangeHead(ctx, id)
		found = err == nil
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	})
	return
}

// Change returns a change with its items.
func (g *Graph) Change(ctx context.Context, id domain.ChangeID) (c domain.Change, err error) {
	err = g.repo.InTx(platform.ReadOnly(ctx), func(tx Tx) error {
		if c, err = tx.Change(ctx, id); err != nil {
			return err
		}
		return nil
	})
	return
}

// ChangeHead reads the head of a change: neither its impacts nor its items (Tx.ChangeHead), what a check of who may act
// on it reads (its unit, project, methodology) without folding its log. Under WithHeadMemo, a head read once is not
// read again.
func (g *Graph) ChangeHead(ctx context.Context, id domain.ChangeID) (c domain.Change, err error) {
	if c, ok := memoHead(ctx, id); ok {
		return c, nil
	}
	memo, _ := ctx.Value(headMemoKey{}).(*headMemo)
	err = g.repo.InTx(platform.ReadOnly(ctx), func(tx Tx) error {
		c, err = tx.ChangeHead(ctx, id)
		return err
	})
	if err == nil && memo != nil {
		memo.mu.Lock()
		memo.heads[id] = c
		memo.mu.Unlock()
	}
	return
}

type headMemoKey struct{}

// headMemo keeps the heads of the changes one request read (WithHeadMemo).
type headMemo struct {
	mu    sync.Mutex
	heads map[domain.ChangeID]domain.Change
}

// memoHead is the head of a change the request read already (WithHeadMemo).
func memoHead(ctx context.Context, id domain.ChangeID) (domain.Change, bool) {
	memo, _ := ctx.Value(headMemoKey{}).(*headMemo)
	if memo == nil {
		return domain.Change{}, false
	}
	memo.mu.Lock()
	defer memo.mu.Unlock()
	c, ok := memo.heads[id]
	return c, ok
}

// WithHeadMemo returns a context under which ChangeHead reads the head of a change once: the checks made before an
// operation of one request (who may read the change, who may perform the operation) ask for the same head, and the
// operation reads the change again in its own transaction. Meant for the life of one request, never across writes the
// checks must see.
func WithHeadMemo(ctx context.Context) context.Context {
	if _, ok := ctx.Value(headMemoKey{}).(*headMemo); ok {
		return ctx
	}
	return context.WithValue(ctx, headMemoKey{}, &headMemo{heads: map[domain.ChangeID]domain.Change{}})
}

// ChangeChecked is Change with the validation of each impact carrying a draft (ImpactValidation): what the change
// impacts look like to a person who reviews them. Each impact is checked on the flow that declared it.
func (g *Graph) ChangeChecked(ctx context.Context, id domain.ChangeID) (c domain.Change, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		if c, err = tx.Change(ctx, id); err != nil {
			return err
		}
		flows := map[string]bool{}
		for _, cn := range c.Nodes {
			flows[cn.Flow] = true
		}
		nodes := slices.Clone(c.Nodes)
		for flow := range flows {
			g.attachValidation(ctx, tx, c, flow, nodes, func(cn domain.ChangeImpact) bool { return cn.Flow == flow })
		}
		c.Nodes = nodes
		return nil
	})
	return
}

// Changes lists changes.
func (g *Graph) Changes(ctx context.Context) (cs []domain.Change, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { cs, err = tx.Changes(ctx); return err })
	return
}

// scopeFilter names the unit and the project of a filter by their URLs, as the changes store them (ADR 0154 §9): a
// filter may name them by key.
func (g *Graph) scopeFilter(f ChangesFilter) ChangesFilter {
	if u, err := g.StructureRef(domain.StructureOrganisation, f.OwnerOrg); err == nil && !u.IsZero() {
		f.OwnerOrg = u.String()
	}
	if u, err := g.StructureRef(domain.StructureProject, f.Project); err == nil && !u.IsZero() {
		f.Project = u.String()
	}
	return f
}

// ListChanges lists the changes matching a filter, in no particular order (the caller sorts and caps).
func (g *Graph) ListChanges(ctx context.Context, f ChangesFilter) (out []domain.Change, err error) {
	f = g.scopeFilter(f)
	// the filter reads the heads; only the changes it keeps are read whole, unless the heads are asked for
	err = g.repo.InTx(ctx, func(tx Tx) error {
		heads, err := tx.ChangeHeads(ctx, f)
		if err != nil {
			return err
		}
		for _, h := range heads {
			if !f.Match(h) {
				continue
			}
			if f.HeadersOnly {
				out = append(out, h)
				continue
			}
			c, err := tx.Change(ctx, h.ID)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return nil
	})
	return out, err
}

// UpdateChange patches a change header.
func (g *Graph) UpdateChange(ctx context.Context, id domain.ChangeID, p ChangePatch) (c domain.Change, err error) {
	err = g.changeTx(ctx, id, func(tx Tx) error {
		c, err = tx.Change(ctx, id)
		if err != nil {
			return err
		}
		before := c
		before.Data = maps.Clone(c.Data)
		edit := domain.HeaderEdit{Fields: map[string]domain.HeaderValue{}}
		if p.Title != nil && *p.Title != c.Title {
			edit.Fields["title"] = domain.HeaderValue{From: c.Title, To: *p.Title}
			c.Title = *p.Title
		}
		if p.Intent != nil && *p.Intent != c.Intent {
			edit.Fields["intent"] = domain.HeaderValue{From: c.Intent, To: *p.Intent}
			c.Intent = *p.Intent
		}
		for f := range edit.Fields {
			for _, fr := range c.InForceFreezes() {
				if fr.FrozenField(f) {
					return fmt.Errorf("the %s of change %s is frozen since it left a state of its lifecycle (now %s): %w", f, id, c.State, ErrConflict)
				}
			}
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
		if err := tx.PutChange(ctx, c); err != nil {
			return err
		}
		if fields := domain.EditedFields(before, c); fields != nil {
			g.raise(ctx, tx, events.ChangeEdited, c, func(ev *domain.ChangeEvent) { ev.Fields = fields })
		}
		return nil
	})
	return
}

// AddItems appends facts to the blackboard of a change (artifacts, decisions): the nodes
// a change acts on are its change impacts (ProposeImpact, ADR 0024). Item ids are assigned when empty.
func (g *Graph) AddItems(ctx context.Context, id domain.ChangeID, items []domain.ChangeItem) ([]domain.ChangeItem, error) {
	if err := g.authorizeItems(ctx, id, items); err != nil {
		return nil, err
	}
	out := make([]domain.ChangeItem, 0, len(items))
	err := g.changeTx(ctx, id, func(tx Tx) error {
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
			if items[i].Type != domain.TypeFlow {
				items[i].Flow = c.ResolveFlow(items[i].Flow) // no flow: the active option (ADR 0032 §6)
			}
		}
		batchFlow := ""
		if len(items) > 0 {
			batchFlow = items[0].Flow
		}
		for _, it := range items {
			if it.Type == domain.TypeFlow {
				return fmt.Errorf("flow events are recorded by OpenFlow, AdoptFlow and DiscardFlow: %w", ErrInvalid)
			}
			if it.Type == domain.TypeTransition {
				return fmt.Errorf("transitions are recorded by TransitionChange: %w", ErrInvalid)
			}
			if it.Type == domain.TypeDecisionPoint {
				return fmt.Errorf("decision points are recorded by OpenDecision, RuleDecision, AnswerQuestion and RatifyDecision: %w", ErrInvalid)
			}
			if it.Type == domain.TypeReview && domain.ReviewRecordOf(it.Data).Status == domain.ReviewStatusSubmitted {
				return fmt.Errorf("a review is submitted with its verdicts, by ImpactNodeReviewBatch (ADR 0105): %w", ErrInvalid)
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
			if err := g.checkItem(it); err != nil {
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
			if err := tx.PutChange(ctx, c); err != nil {
				return err
			}
		}
		g.raiseItems(ctx, tx, c, out)
		return nil
	})
	return out, err
}

// ItemAuthorizer judges the write of an item whose kind asks a permission of its writer: the permission it asks, the
// change it is written on and the item. An error refuses it.
type ItemAuthorizer func(ctx context.Context, c domain.Change, it domain.ChangeItem, p domain.ItemPermission) error

// SeatAuthorizer judges whether the caller of ctx may rule a decision point of the change in the seat of role.
type SeatAuthorizer func(ctx context.Context, c domain.Change, role string) error

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
		p, ok := domain.ItemPermissionOf(it.Type)
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
	var logs map[string][]domain.LogEntry
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		if logs, err = g.logEntries(ctx, tx, id); err != nil {
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
		if facets[domain.FacetDependencies], err = dependenciesOf(ctx, tx, c); err != nil { // ADR 0108
			return err
		}
		full := c // the drafts are read with the flows of the change, the view of a flow has none
		c = c.View(flow)
		c.Nodes = nodes.Slice()
		g.attachValidation(ctx, tx, full, flow, c.Nodes, nil)
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
		var near []domain.NodeRef
		for _, r := range c.ReferencedNodes() {
			v, err := viewIn(ctx, tx, reader, r)
			if err != nil {
				return err
			}
			if lc := ix.lifecycleOf(v.Type); lc != nil && v.State != "" {
				v.NotCommittable = !lc.Committable(v.State)
			}
			bb.Nodes[r] = v
			for _, l := range v.Out {
				if !l.External() {
					near = append(near, l.To)
				}
			}
			for _, l := range v.In {
				near = append(near, l.From)
			}
		}
		// the neighbours in one batch, not one read each
		bb.Neighbors, err = reader.nodes(ctx, tx, near)
		return err
	})
	if err != nil {
		return
	}
	// the facets folded from the log (the history, ADR 0109), outside the transaction
	return g.withLogFacets(ctx, bb, logs)
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
		for _, cn := range c.Impacts().All() {
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
		for _, cn := range c.Impacts().All() {
			for _, ref := range []*domain.NodeRef{cn.Pre, cn.Post} {
				if ref != nil && !slices.Contains(out[ref.ID], id) {
					out[ref.ID] = append(out[ref.ID], id)
				}
			}
		}
	}
	return out, nil
}

// NodeChanges lists a page of the changes acting on a node (ADR 0140): the last limit (0: 50, at most 500) whose cursor
// is below before (0: up to the latest), oldest first, headers only, and the before of the next older page (0: none).
func (g *Graph) NodeChanges(ctx context.Context, node domain.NodeID, before int64, limit int) (out []domain.Change, next int64, err error) {
	limit = pageLimit(limit)
	err = g.repo.InTx(ctx, func(tx Tx) error {
		page, err := tx.NodeChangePage(ctx, node, before, limit+1)
		if err != nil {
			return err
		}
		if len(page) > limit {
			page, next = page[:limit], page[limit-1].Seq
		}
		ids := make([]domain.ChangeID, len(page))
		for i, p := range page {
			ids[i] = p.Change
		}
		heads, err := changeHeads(ctx, tx, ids)
		if err != nil {
			return err
		}
		for i := len(page) - 1; i >= 0; i-- {
			c, ok := heads[page[i].Change]
			if !ok {
				return fmt.Errorf("change %s: %w", page[i].Change, ErrNotFound)
			}
			out = append(out, c)
		}
		return nil
	})
	return
}

// pageLimit is the size of a page of a history: 50 by default, at most 500.
func pageLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	return min(limit, 500)
}

// BaselineHistory is a page of the history of a branch (ADR 0140).
type BaselineHistory struct {
	// Baselines are the baselines of the branch of the page, oldest first.
	Baselines []domain.Baseline
	// Related are what draws the page: the baselines the changes of the page left on their own branches, those of the
	// open branches forked from it (on the newest page), and the baselines their edges reach outside the page.
	Related []domain.Baseline
	// Next is the order to ask the older page with (before); 0: none.
	Next int64
}

// BaselineHistory returns the page of the history of a branch committed before the order before (0: the newest), at most
// limit baselines of the branch (default 100, at most MaxBrowseLimit): headers only, never the whole namespace.
func (g *Graph) BaselineHistory(ctx context.Context, namespace, branch string, before int64, limit int) (h BaselineHistory, err error) {
	if limit <= 0 || limit > MaxBrowseLimit {
		limit = 100
	}
	branch = domain.BranchOf(branch)
	err = g.repo.InTx(ctx, func(tx Tx) error {
		page, err := tx.BaselinePage(ctx, namespace, branch, before, limit+1)
		if err != nil {
			return err
		}
		if len(page) > limit {
			page = page[:limit]
			h.Next = page[limit-1].Seq
		}
		slices.Reverse(page)
		h.Baselines = page
		seen := map[domain.BaselineID]bool{}
		var changes []domain.ChangeID
		for _, b := range page {
			seen[b.ID] = true
			if b.ChangeID != "" {
				changes = append(changes, b.ChangeID)
			}
		}
		add := func(bs []domain.Baseline) {
			for _, b := range bs {
				if !seen[b.ID] {
					seen[b.ID] = true
					h.Related = append(h.Related, b)
				}
			}
		}
		slices.Sort(changes)
		related, err := tx.ChangeBaselineHeaders(ctx, namespace, slices.Compact(changes))
		if err != nil {
			return err
		}
		add(related)
		if before == 0 {
			brs, err := tx.Branches(ctx, namespace)
			if err != nil {
				return err
			}
			for _, br := range brs {
				if br.Status != domain.BranchOpen || br.Name == branch || domain.BranchOf(br.Parent) != branch {
					continue
				}
				bs, err := tx.BranchBaselineHeaders(ctx, namespace, br.Name)
				if err != nil {
					return err
				}
				add(bs)
			}
		}
		var reach []domain.BaselineID
		for _, list := range [][]domain.Baseline{h.Baselines, h.Related} {
			for _, b := range list {
				for _, id := range []domain.BaselineID{b.ParentID, b.MergedFrom, b.RebasedFrom} {
					if id != "" && !seen[id] {
						reach = append(reach, id)
					}
				}
			}
		}
		slices.Sort(reach)
		ghosts, err := tx.BaselineHeadersOf(ctx, namespace, slices.Compact(reach))
		add(ghosts)
		return err
	})
	for _, list := range [][]domain.Baseline{h.Baselines, h.Related} {
		for i := range list {
			list[i].Nodes, list[i].Namespace = nil, domain.NamespaceOf(namespace)
		}
	}
	return h, err
}

// BaselineHeadersOf returns the headers of baselines of a namespace by id, oldest first.
func (g *Graph) BaselineHeadersOf(ctx context.Context, namespace string, ids []domain.BaselineID) (bs []domain.Baseline, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { bs, err = tx.BaselineHeadersOf(ctx, namespace, ids); return err })
	for i := range bs {
		bs[i].Nodes, bs[i].Namespace = nil, domain.NamespaceOf(namespace)
	}
	return
}

// BranchHeads returns the branches of a namespace (of these statuses, when given) with the header of each head and the number of baselines of each
// branch: what a list of the branches shows, without listing their baselines (ADR 0140).
func (g *Graph) BranchHeads(ctx context.Context, namespace string, status ...string) (bs []domain.Branch, heads []domain.Baseline, counts map[string]int, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		if bs, err = tx.Branches(ctx, namespace); err != nil {
			return err
		}
		if len(status) > 0 {
			bs = slices.DeleteFunc(bs, func(b domain.Branch) bool { return !slices.Contains(status, b.Status) })
		}
		var ids []domain.BaselineID
		for _, b := range bs {
			if b.Head != "" {
				ids = append(ids, b.Head)
			}
		}
		if heads, err = tx.BaselineHeadersOf(ctx, namespace, ids); err != nil {
			return err
		}
		counts, err = tx.BaselineCounts(ctx, namespace)
		return err
	})
	for i := range heads {
		heads[i].Nodes, heads[i].Namespace = nil, domain.NamespaceOf(namespace)
	}
	return
}
