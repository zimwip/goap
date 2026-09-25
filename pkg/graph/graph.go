package graph

import (
	"context"
	"fmt"
	"maps"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/zimwip/goap/pkg/domain"
)

// Graph exposes the domain and change axes.
type Graph struct {
	repo  Repo
	now   func() time.Time
	newID func() string
	// Authorizer, when set, is asked before every lifecycle transition.
	Authorizer TransitionAuthorizer
	types      sync.Map // baseline id → *typeIndex
}

// New returns a Graph backed by repo.
func New(repo Repo) *Graph {
	return &Graph{repo: repo, now: func() time.Time { return time.Now().UTC() }, newID: func() string { return uuid.NewString() }}
}

// ---- Domain axis --------------------------------------------------------

// NewNode describes a node to create.
type NewNode struct {
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
	n := domain.Node{ID: domain.NodeID(g.newID()), Version: 1, Branch: domain.MainBranch, Reason: domain.ReasonCreate, Key: in.Key, Type: in.Type, Properties: in.Properties, CreatedAt: g.now(), State: in.State}
	if n.Key == "" {
		n.Key = string(n.ID)
	}
	err := g.repo.InTx(ctx, func(tx Tx) error { return tx.PutNode(ctx, n) })
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

// NodeByKey returns the latest version of the node with the given key.
func (g *Graph) NodeByKey(ctx context.Context, key string) (n domain.Node, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { n, err = tx.NodeByKey(ctx, key); return err })
	return
}

// Link creates a link between two node versions outside of any change (import).
func (g *Graph) Link(ctx context.Context, typ string, from, to domain.NodeRef, props map[string]any) (domain.Link, error) {
	l := domain.Link{ID: domain.LinkID(g.newID()), Type: typ, From: from, To: to, Properties: props}
	err := g.repo.InTx(ctx, func(tx Tx) error {
		if _, err := tx.Node(ctx, from); err != nil {
			return err
		}
		if _, err := tx.Node(ctx, to); err != nil {
			return err
		}
		return tx.PutLink(ctx, l)
	})
	return l, err
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

// CreateBaseline snapshots the given node versions (Version 0 = latest).
// Deleted versions are skipped.
func (g *Graph) CreateBaseline(ctx context.Context, name string, nodes []domain.NodeRef) (domain.Baseline, error) {
	b := domain.Baseline{ID: domain.BaselineID(g.newID()), Name: name, Nodes: map[domain.NodeID]domain.Version{}, CreatedAt: g.now()}
	err := g.repo.InTx(ctx, func(tx Tx) error {
		for _, r := range nodes {
			n, err := tx.Node(ctx, r)
			if err != nil {
				return err
			}
			if n.Deleted {
				continue
			}
			b.Nodes[n.ID] = n.Version
		}
		return tx.PutBaseline(ctx, b)
	})
	return b, err
}

// CreateBaselineFromLatest snapshots the latest version of every live node.
func (g *Graph) CreateBaselineFromLatest(ctx context.Context, name string) (domain.Baseline, error) {
	var refs []domain.NodeRef
	err := g.repo.InTx(ctx, func(tx Tx) error {
		nodes, err := tx.LatestNodes(ctx)
		for _, n := range nodes {
			refs = append(refs, n.Ref())
		}
		return err
	})
	if err != nil {
		return domain.Baseline{}, err
	}
	return g.CreateBaseline(ctx, name, refs)
}

// Baseline returns a baseline.
func (g *Graph) Baseline(ctx context.Context, id domain.BaselineID) (b domain.Baseline, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { b, err = tx.Baseline(ctx, id); return err })
	return
}

// Baselines lists baselines by creation date.
func (g *Graph) Baselines(ctx context.Context) (bs []domain.Baseline, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { bs, err = tx.Baselines(ctx); return err })
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
	BaselineID  domain.BaselineID
	// Branch the change applies to (default main); it must be open.
	Branch string
	Data   map[string]any
}

// CreateChange opens a change on a reference baseline.
func (g *Graph) CreateChange(ctx context.Context, in NewChange) (domain.ChangeSet, error) {
	c := domain.ChangeSet{
		ID: domain.ChangeID(g.newID()), Title: in.Title, Intent: in.Intent, Methodology: in.Methodology,
		Status: domain.ChangeDraft, BaselineID: in.BaselineID, Branch: domain.BranchOf(in.Branch), Data: in.Data, CreatedAt: g.now(),
	}
	err := g.repo.InTx(ctx, func(tx Tx) error {
		if _, err := tx.Baseline(ctx, in.BaselineID); err != nil {
			return err
		}
		b, err := branchOf(ctx, tx, c.Branch)
		if err != nil {
			return err
		}
		if b.Status != domain.BranchOpen {
			return fmt.Errorf("branch %s is %s: %w", b.Name, b.Status, ErrConflict)
		}
		return tx.PutChange(ctx, c)
	})
	return c, err
}

// Change returns a change with its items.
func (g *Graph) Change(ctx context.Context, id domain.ChangeID) (c domain.ChangeSet, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { c, err = tx.Change(ctx, id); return err })
	return
}

// Changes lists changes.
func (g *Graph) Changes(ctx context.Context) (cs []domain.ChangeSet, err error) {
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
func (g *Graph) UpdateChange(ctx context.Context, id domain.ChangeID, p ChangePatch) (c domain.ChangeSet, err error) {
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

// AddItems appends items to the blackboard of a change. Item ids are assigned
// when empty. Impact targets and proposal bases must exist in the reference
// baseline.
func (g *Graph) AddItems(ctx context.Context, id domain.ChangeID, items []domain.ChangeItem) ([]domain.ChangeItem, error) {
	out := make([]domain.ChangeItem, 0, len(items))
	err := g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		if c.Status == domain.ChangeApplied || c.Status == domain.ChangeAbandoned {
			return fmt.Errorf("change %s is %s: %w", id, c.Status, ErrConflict)
		}
		b, err := tx.Baseline(ctx, c.BaselineID)
		if err != nil {
			return err
		}
		known := map[domain.ItemID]bool{}
		for _, it := range c.Items {
			known[it.ID] = true
		}
		for _, it := range items {
			if it.ID == "" {
				it.ID = domain.ItemID(g.newID())
			}
			if it.Status == "" {
				it.Status = domain.ItemProposed
			}
			it.CreatedAt = g.now()
			if err := it.Validate(); err != nil {
				return fmt.Errorf("item %s: %v: %w", it.ID, err, ErrInvalid)
			}
			for _, r := range refsOf(it) {
				if !b.Contains(r) {
					return fmt.Errorf("item %s references %s which is not in baseline %s: %w", it.ID, r, b.ID, ErrInvalid)
				}
			}
			for _, e := range endpointsOf(it) {
				if e.Item != "" && !known[e.Item] {
					return fmt.Errorf("item %s references unknown item %s: %w", it.ID, e.Item, ErrInvalid)
				}
			}
			if it.Decision != nil && !known[it.Decision.Item] {
				return fmt.Errorf("decision %s targets unknown item %s: %w", it.ID, it.Decision.Item, ErrInvalid)
			}
			if err := tx.PutItem(ctx, id, it); err != nil {
				return err
			}
			known[it.ID] = true
			out = append(out, it)
		}
		// lifecycle: replay the whole change (early feedback, Apply is authoritative)
		if full, err := tx.Change(ctx, id); err != nil {
			return err
		} else if _, err := g.walk(ctx, tx, full, false); err != nil {
			return err
		}
		for _, ref := range modifies(out) {
			if err := tx.PutAttachment(ctx, id, ref); err != nil {
				return err
			}
		}
		if c.Status == domain.ChangeDraft {
			c.Status = domain.ChangeActive
			return tx.PutChange(ctx, c)
		}
		return nil
	})
	return out, err
}

func refsOf(it domain.ChangeItem) []domain.NodeRef {
	c := domain.ChangeSet{Items: []domain.ChangeItem{it}}
	return c.ReferencedNodes()
}

func endpointsOf(it domain.ChangeItem) []domain.Endpoint {
	if it.Proposal == nil || it.Proposal.Link == nil {
		return nil
	}
	return []domain.Endpoint{it.Proposal.Link.From, it.Proposal.Link.To}
}

// Blackboard returns the change and a hydrated view of every node it
// references, ready for condition evaluation.
func (g *Graph) Blackboard(ctx context.Context, id domain.ChangeID) (bb domain.Blackboard, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
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
	}
	return false
}

// ChangeNodes lists the nodes a change is attached to (the version it starts from).
func (g *Graph) ChangeNodes(ctx context.Context, id domain.ChangeID) (refs []domain.NodeRef, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		if _, err := tx.Change(ctx, id); err != nil {
			return err
		}
		refs, err = tx.Attachments(ctx, id)
		return err
	})
	return
}

// NodeChanges lists the changes a node is attached to, oldest first.
func (g *Graph) NodeChanges(ctx context.Context, node domain.NodeID) (out []domain.ChangeSet, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		ids, err := tx.NodeAttachments(ctx, node)
		if err != nil {
			return err
		}
		for _, id := range ids {
			c, err := tx.Change(ctx, id)
			if err != nil {
				return err
			}
			c.Items = nil
			out = append(out, c)
		}
		return nil
	})
	return
}
