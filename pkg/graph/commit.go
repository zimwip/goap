package graph

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/zimwip/goap/pkg/domain"
)

// This file is the producer-facing way of making a change of node edits
// (ADR 0024): the producer says which nodes it creates or modifies and why; the
// change, its change impacts, the versions on the change branch, the reviews and
// the apply follow. Services that project their own definitions into the graph
// (registry, seeds) use it.

// NodeEdit is one node created or modified by a Commit.
type NodeEdit struct {
	// Key and Type name a created node (Pre is nil).
	Key, Type string
	// Pre is the version a modified node starts from (in the reference baseline).
	Pre *domain.NodeRef
	// Props are merged over the current properties (a nil value clears one).
	Props map[string]any
	// State moves the node to a lifecycle state, after its other edits (see NodeWrite.State).
	State string
	// Retire deletes the node (see NodeWrite.Retire).
	Retire bool
	// Owner transfers the node to another organisational unit (see NodeWrite.Owner); empty: unchanged, or the unit
	// holding the commit for a created node.
	Owner string
	// Rationale says why; the title of the commit when empty.
	Rationale   string
	Links       []LinkEdit
	RemoveLinks []domain.LinkID
}

// LinkEdit is an outgoing link added by an edit: to an existing version (To) or
// to a node created by another edit of the commit (ToKey). Nodes created by the commit may link each other in
// any direction, cycles included: a link to a node not written yet is added, in the same change, once it is.
type LinkEdit struct {
	Type  string
	To    *domain.NodeRef
	ToKey string
	Props map[string]any
}

// Commit describes a change made of node edits, applied as one unit.
type Commit struct {
	Namespace, Title, Intent, Methodology string
	Data                                  map[string]any
	Baseline                              domain.BaselineID
	// Branch the commit lands on (default main).
	Branch string
	// By is who accepts the edits: the producer.
	By           string
	BaselineName string
	Edits        []NodeEdit
	// OwnerOrg is the unit holding the commit and ProjectID the project it acts in (ADR 0054); empty: the root unit,
	// the default project.
	OwnerOrg, ProjectID string
}

// CommitResult is the outcome of a Commit.
type CommitResult struct {
	Change   domain.ChangeID
	Baseline domain.Baseline
}

// Commit runs the edits as one change: it opens the change on a branch of its
// own, declares a change impact per edit, writes their versions, accepts them
// (the rationale is the comment) and applies the change. When the branch
// cannot be merged without conflict (another change moved a node meanwhile) the
// change is abandoned and ErrConflict returned: the producer reads again and
// rebuilds its edits. A created node of a structure or a User must name its required
// parent (checkRequiredParent, ADR 0040) — the producer-level guarantee
// CreateNode/UpdateNode do not make (commitEdits' parenting param), since they
// stand in for the single-node writes the engine itself makes while landing an
// ordinary change's own impacts (AddNodes/WriteNode), which never enforced it either.
func (g *Graph) Commit(ctx context.Context, in Commit) (CommitResult, error) {
	return g.commitEdits(ctx, in, true)
}

func (g *Graph) commitEdits(ctx context.Context, in Commit, parenting bool) (res CommitResult, err error) {
	if len(in.Edits) == 0 {
		return res, fmt.Errorf("a commit needs at least one edit: %w", ErrInvalid)
	}
	if parenting {
		for _, e := range in.Edits {
			if err := g.checkRequiredParent(e); err != nil {
				return res, err
			}
		}
	}
	c, err := g.CreateChange(ctx, NewChange{Namespace: in.Namespace, Title: in.Title, Intent: in.Intent, Methodology: in.Methodology,
		BaselineID: in.Baseline, Branch: in.Branch, Data: in.Data, OwnBranch: true, OwnerOrg: in.OwnerOrg, ProjectID: in.ProjectID})
	if err != nil {
		return res, err
	}
	res.Change = c.ID
	defer func() {
		if err == nil {
			return
		}
		abandoned := domain.ChangeAbandoned
		if _, aerr := g.UpdateChange(ctx, c.ID, ChangePatch{Status: &abandoned}); aerr != nil {
			err = errors.Join(err, aerr)
		}
	}()

	nodes := make([]domain.ChangeImpact, len(in.Edits))
	for i, e := range in.Edits {
		why := e.Rationale
		if why == "" {
			why = in.Title
		}
		nodes[i] = domain.ChangeImpact{Intent: domain.IntentModified, Pre: e.Pre, Rationale: why, ProducedBy: in.By}
		if e.Pre == nil {
			nodes[i].Intent, nodes[i].Key, nodes[i].Type = domain.IntentCreated, e.Key, e.Type
		}
	}
	added, err := g.AddNodes(ctx, c.ID, nodes)
	if err != nil {
		return res, err
	}
	order, err := g.commitOrder(ctx, in.Edits)
	if err != nil {
		return res, err
	}
	written := map[string]domain.NodeRef{} // key of a created node → its version
	type late struct {
		from string
		l    LinkEdit
	}
	var later []late // links to a node of the commit not written yet (a cycle): added once every node is
	created := map[string]bool{}
	for _, e := range in.Edits {
		if e.Pre == nil {
			created[e.Key] = true
		}
	}
	for _, i := range order {
		e := in.Edits[i]
		w := NodeWrite{Properties: e.Props, State: e.State, RemoveLinks: e.RemoveLinks, Retire: e.Retire, Owner: e.Owner}
		for _, l := range e.Links {
			to := l.To
			if l.ToKey != "" {
				ref, ok := written[l.ToKey]
				if !ok {
					if created[l.ToKey] {
						later = append(later, late{e.Key, l})
						continue
					}
					// not a node of this commit: a node the graph already holds, whose version stays
					n, err := g.NodeByKeyOn(ctx, in.Namespace, in.Branch, l.ToKey)
					if errors.Is(err, ErrNotFound) {
						return res, fmt.Errorf("link of %s to %q: no such node, neither stored nor created by this commit: %w", nodeName(e), l.ToKey, ErrInvalid)
					} else if err != nil {
						return res, err
					}
					ref = n.Ref()
				}
				to = &ref
			}
			if to == nil {
				return res, fmt.Errorf("link of %s needs a target: %w", nodeName(e), ErrInvalid)
			}
			if e.Pre != nil {
				// a link is part of its source version: the one the version already carries is not added again
				if has, err := g.linksTo(ctx, *e.Pre, l.Type, to.ID); err != nil {
					return res, err
				} else if has {
					continue
				}
			}
			w.AddLinks = append(w.AddLinks, LinkWrite{Type: l.Type, To: *to, Properties: l.Props})
		}
		cn, err := g.WriteNode(ctx, c.ID, added[i].ID, w)
		if err != nil {
			return res, fmt.Errorf("%s: %w", nodeName(e), err)
		}
		if parenting && cn.Post != nil && !e.Retire {
			if err := g.checkParentInvariant(ctx, *cn.Post); err != nil {
				return res, fmt.Errorf("%s: %w", nodeName(e), err)
			}
		}
		if e.Pre == nil {
			written[e.Key] = *cn.Post
		}
	}
	for _, x := range later {
		if _, err := g.Link(ctx, c.ID, x.l.Type, written[x.from], written[x.l.ToKey], x.l.Props); err != nil {
			return res, fmt.Errorf("link %s from %s to %s: %w", x.l.Type, x.from, x.l.ToKey, err)
		}
	}
	for i := range in.Edits {
		if _, err := g.ReviewNode(ctx, c.ID, added[i].ID, domain.ReviewAccepted, in.By, nodes[i].Rationale); err != nil {
			return res, err
		}
	}
	if res.Baseline, err = g.Apply(ctx, c.ID, in.BaselineName); err != nil {
		return res, err
	}
	if done, err := g.Change(ctx, c.ID); err != nil {
		return res, err
	} else if done.Status == domain.ChangeCommitted {
		return res, fmt.Errorf("commit %q conflicts with a concurrent change: %w", in.Title, ErrConflict)
	}
	return res, nil
}

// linksTo reports whether a version already has an outgoing link of the type to the node.
func (g *Graph) linksTo(ctx context.Context, from domain.NodeRef, typ string, to domain.NodeID) (has bool, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		out, err := tx.OutLinks(ctx, from)
		for _, l := range out {
			has = has || l.Type == typ && l.To.ID == to
		}
		return err
	})
	return
}

func nodeName(e NodeEdit) string {
	if e.Pre != nil {
		return e.Pre.String()
	}
	return e.Key
}

// commitOrder orders the edits so that what a link points to is written before
// the node that links to it: an edit comes after the created node it links to by
// key, after the modified node it links to, and (for a modified node) after the
// modified nodes its current version links to. Cycles among modified nodes keep
// the given order; a cycle among created nodes is written in the given order, the links that close it added after the nodes.
func (g *Graph) commitOrder(ctx context.Context, edits []NodeEdit) ([]int, error) {
	byKey := map[string]int{}
	byNode := map[domain.NodeID]int{}
	for i, e := range edits {
		if e.Pre == nil {
			if _, dup := byKey[e.Key]; dup {
				return nil, fmt.Errorf("node %q is created twice: %w", e.Key, ErrInvalid)
			}
			byKey[e.Key] = i
		} else {
			byNode[e.Pre.ID] = i
		}
	}
	type dep struct {
		on int
	}
	deps := make([][]dep, len(edits))
	err := g.repo.InTx(ctx, func(tx Tx) error {
		for i, e := range edits {
			for _, l := range e.Links {
				if l.ToKey != "" {
					if j, ok := byKey[l.ToKey]; ok {
						deps[i] = append(deps[i], dep{j})
					}
				} else if l.To != nil {
					if j, ok := byNode[l.To.ID]; ok && j != i {
						deps[i] = append(deps[i], dep{j})
					}
				}
			}
			if e.Pre == nil {
				continue
			}
			out, err := tx.OutLinks(ctx, *e.Pre)
			if err != nil {
				return err
			}
			for _, l := range out {
				if j, ok := byNode[l.To.ID]; ok && j != i {
					deps[i] = append(deps[i], dep{j})
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var order []int
	state := make([]int, len(edits)) // 0 new, 1 visiting, 2 done
	var visit func(i int) error
	visit = func(i int) error {
		switch state[i] {
		case 2:
			return nil
		case 1:
			return nil // a cycle: keep the given order, the link that closes it is added after the nodes
		}
		state[i] = 1
		for _, d := range deps[i] {
			if err := visit(d.on); err != nil {
				return err
			}
		}
		state[i] = 2
		order = append(order, i)
		return nil
	}
	for i := range edits {
		if err := visit(i); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// CreateObject creates a node of a qualified type (ADR 0012) through one change applied on main, in the namespace of
// its type (namespace, when given, must be it). The type catalogue judges it like any node. It fails with ErrConflict
// when the key is taken in the namespace and ErrInvalid without a key or with an unqualified type.
func (g *Graph) CreateObject(ctx context.Context, methodology, namespace, typ, key string, props map[string]any) (domain.Node, domain.Baseline, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return domain.Node{}, domain.Baseline{}, fmt.Errorf("object key is required: %w", ErrInvalid)
	}
	ref, err := domain.ParseTypeRef(typ)
	if err != nil || !ref.Qualified() {
		return domain.Node{}, domain.Baseline{}, fmt.Errorf("object type %q must be <namespace>@<type>: %w", typ, ErrInvalid)
	}
	if namespace == "" {
		namespace = ref.Namespace
	}
	if _, err := g.NodeByKey(ctx, namespace, key); err == nil {
		return domain.Node{}, domain.Baseline{}, fmt.Errorf("key %q is already used: %w", key, ErrConflict)
	} else if !errors.Is(err, ErrNotFound) {
		return domain.Node{}, domain.Baseline{}, err
	}
	head, err := g.BranchHead(ctx, namespace, domain.MainBranch)
	if err != nil {
		return domain.Node{}, domain.Baseline{}, err
	}
	out, err := g.Commit(ctx, Commit{Namespace: namespace, Title: "Create " + key, Intent: "Create " + typ + " " + key,
		Baseline: head.ID, Methodology: methodology, By: "graph.create_object",
		Edits: []NodeEdit{{Key: key, Type: typ, Props: props, Rationale: "Create " + typ + " " + key}}})
	if err != nil {
		return domain.Node{}, domain.Baseline{}, err
	}
	n, err := g.NodeByKey(ctx, namespace, key)
	return n, out.Baseline, err
}
