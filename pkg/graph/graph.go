package graph

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
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
	Search(typ string) []domain.SearchProperty
	// CheckNode is the existence rule of a node of namespace ns, CheckLink of a link between two node types.
	CheckNode(ns, typ string) error
	CheckLink(typ, from, to string) error
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
}

// New returns a Graph backed by repo.
func New(repo Repo) *Graph {
	return &Graph{repo: repo, now: func() time.Time { return time.Now().UTC() }, newID: func() string { return uuid.NewString() }}
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
}

// CreateNode creates version 1 of a node outside of any change (import).
func (g *Graph) CreateNode(ctx context.Context, in NewNode) (domain.Node, error) {
	if in.Type == "" {
		return domain.Node{}, fmt.Errorf("node type required: %w", ErrInvalid)
	}
	n := domain.Node{ID: domain.NodeID(g.newID()), Version: 1, Branch: domain.MainBranch, Reason: domain.ReasonCreate, Namespace: domain.NamespaceOf(in.Namespace), Key: in.Key, Type: in.Type, Properties: in.Properties, CreatedAt: g.now(), State: in.State}
	if n.Key == "" {
		n.Key = string(n.ID)
	}
	err := g.repo.InTx(ctx, func(tx Tx) error {
		if err := g.checkDirect(n.Namespace, n.Type); err != nil {
			return err
		}
		return tx.PutNode(ctx, n)
	})
	return n, err
}

// UpdateNode creates a new version of a node on main outside of any change
// (import). base must be the latest version on main.
func (g *Graph) UpdateNode(ctx context.Context, base domain.NodeRef, props map[string]any) (domain.Node, error) {
	var n domain.Node
	err := g.repo.InTx(ctx, func(tx Tx) error {
		latest, err := tx.Node(ctx, domain.NodeRef{ID: base.ID})
		if err != nil {
			return err
		}
		if latest.Version != base.Version {
			return fmt.Errorf("node %s is at v%d: %w", base, latest.Version, ErrConflict)
		}
		v, err := nextVersion(ctx, tx, base.ID)
		if err != nil {
			return err
		}
		n = latest
		n.Version, n.Branch, n.Parents, n.Reason = v, domain.MainBranch, []domain.Version{latest.Version}, domain.ReasonRevise
		n.Properties = props
		n.ChangeID = ""
		n.CreatedAt = g.now()
		return tx.PutNode(ctx, n)
	})
	return n, err
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

// Link creates a link between two node versions outside of any change (import).
func (g *Graph) Link(ctx context.Context, typ string, from, to domain.NodeRef, props map[string]any) (domain.Link, error) {
	l := domain.Link{ID: domain.LinkID(g.newID()), Type: typ, From: from, To: to, Properties: props}
	err := g.repo.InTx(ctx, func(tx Tx) error {
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

// checkDirect applies the existence rule to a direct write; nothing is checked without a catalogue.
func (g *Graph) checkDirect(ns, typ string) error {
	if g.Types == nil {
		return nil
	}
	return (&typeIndex{cat: g.catalog()}).checkNode(ns, typ)
}

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

// CreateBaseline snapshots the given node versions (Version 0 = latest), all
// of which must belong to namespace. Deleted versions are skipped.
func (g *Graph) CreateBaseline(ctx context.Context, namespace, name string, nodes []domain.NodeRef) (domain.Baseline, error) {
	namespace = domain.NamespaceOf(namespace)
	b := domain.Baseline{ID: domain.BaselineID(g.newID()), Name: name, Namespace: namespace, Nodes: map[domain.NodeID]domain.Version{}, CreatedAt: g.now()}
	err := g.repo.InTx(ctx, func(tx Tx) error {
		for _, r := range nodes {
			n, err := tx.Node(ctx, r)
			if err != nil {
				return err
			}
			if n.Deleted {
				continue
			}
			if n.Namespace != namespace {
				return fmt.Errorf("node %s is of namespace %s, baseline %s is of namespace %s: %w", n.Key, n.Namespace, name, namespace, ErrInvalid)
			}
			b.Nodes[n.ID] = n.Version
		}
		return tx.PutBaseline(ctx, b)
	})
	return b, err
}

// CreateBaselineFromLatest snapshots the latest version of every live node of namespace.
func (g *Graph) CreateBaselineFromLatest(ctx context.Context, namespace, name string) (domain.Baseline, error) {
	var refs []domain.NodeRef
	err := g.repo.InTx(ctx, func(tx Tx) error {
		nodes, err := tx.LatestNodes(ctx, namespace, domain.MainBranch)
		for _, n := range nodes {
			refs = append(refs, n.Ref())
		}
		return err
	})
	if err != nil {
		return domain.Baseline{}, err
	}
	return g.CreateBaseline(ctx, namespace, name, refs)
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
	err = g.repo.InTx(ctx, func(tx Tx) error {
		b, err := tx.Baseline(ctx, id)
		if err != nil {
			return err
		}
		nodes, err = tx.NodesIn(ctx, id, "")
		if err != nil {
			return err
		}
		for _, n := range nodes {
			out, err := tx.OutLinks(ctx, n.Ref())
			if err != nil {
				return err
			}
			for _, l := range out {
				if b.Contains(l.To) {
					links = append(links, l)
				}
			}
		}
		return nil
	})
	return
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
	// ParentID makes the change a sub-change of another one (see prepareSubChange).
	ParentID domain.ChangeID
	// OwnerOrg is the key of the OrgUnit responsible for the change (empty: the default organisation).
	OwnerOrg string
	Data     map[string]any
}

// CreateChange opens a change on a reference baseline. A sub-change
// (ParentID) belongs to the namespace of its parent, forks its own branch from
// the branch of the parent (which must have one) and is merged into it.
func (g *Graph) CreateChange(ctx context.Context, in NewChange) (domain.Change, error) {
	var c domain.Change
	err := g.repo.InTx(ctx, func(tx Tx) error {
		c = domain.Change{
			ID: domain.ChangeID(g.newID()), Title: in.Title, Intent: in.Intent, Methodology: in.Methodology, Namespace: domain.NamespaceOf(in.Namespace),
			Status: domain.ChangeDraft, BaselineID: in.BaselineID, Branch: domain.BranchOf(in.Branch), Data: in.Data, CreatedAt: g.now(),
			ParentID: in.ParentID, OwnerOrg: in.OwnerOrg,
		}
		if err := g.prepareSubChange(ctx, tx, &c, &in); err != nil {
			return err
		}
		if in.OwnerOrg != "" {
			if err := checkOwnerOrg(ctx, tx, in.OwnerOrg); err != nil {
				return err
			}
		}
		fork, err := tx.Baseline(ctx, c.BaselineID)
		if err != nil {
			return err
		}
		if fork.Namespace != c.Namespace {
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
			own := domain.Branch{Name: changeBranchName(c.ID), Namespace: c.Namespace, Parent: b.Name, ForkBaseline: fork.ID, Head: fork.ID,
				Origin: domain.ChangeBranchOrigin(c.ID), Status: domain.BranchOpen, CreatedAt: g.now()}
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

// ChangePatch updates mutable fields of a change header. Nil fields are kept.
type ChangePatch struct {
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
		if c.Status == domain.ChangeApplied || c.Status == domain.ChangeAbandoned || c.Status == domain.ChangeMergePending {
			return fmt.Errorf("change %s is %s: %w", id, c.Status, ErrConflict)
		}
		known := map[domain.ItemID]bool{}
		for _, it := range c.Items {
			known[it.ID] = true
		}
		items = slices.Clone(items)
		batchFlow := ""
		if len(items) > 0 {
			batchFlow = items[0].Flow
		}
		for _, it := range items {
			if it.Kind == domain.KindFlow {
				return fmt.Errorf("flow events are recorded by OpenFlow, AdoptFlow and DiscardFlow: %w", ErrInvalid)
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
		nodes, err := g.newFlowNodes(tx, c, flow).nodes(ctx) // the change impacts as the flow sees them (ADR 0025)
		if err != nil {
			return err
		}
		c = c.View(flow)
		c.Nodes = nodes
		bb = domain.Blackboard{Change: c, Nodes: map[domain.NodeRef]domain.NodeView{}, Neighbors: map[domain.NodeRef]domain.Node{}}
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

// validStatusMove is the change status machine: draft → active → applied
// (applying is done by Apply) or abandoned; applied and abandoned are final.
func validStatusMove(from, to domain.ChangeStatus) bool {
	switch from {
	case domain.ChangeDraft:
		return to == domain.ChangeActive || to == domain.ChangeAbandoned
	case domain.ChangeActive:
		return to == domain.ChangeDraft || to == domain.ChangeAbandoned
	case domain.ChangeMergePending:
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
