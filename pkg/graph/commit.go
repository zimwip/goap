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
	// Rationale says why; the title of the commit when empty.
	Rationale   string
	Links       []LinkEdit
	RemoveLinks []domain.LinkID
}

// LinkEdit is an outgoing link added by an edit: to an existing version (To) or
// to a node created by another edit of the commit (ToKey).
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
// rebuilds its edits. A created OrgUnit/ProjectUnit/User must name its required
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
			if err := checkRequiredParent(e); err != nil {
				return res, err
			}
		}
	}
	if in.Baseline == "" {
		// a namespace Commit touches for the first time has no baseline yet to reference (ADR 0049: every
		// write, even the first one of a namespace, goes through a change) — give it an empty one, exactly
		// what a caller doing this by hand (graphsvc.applyOn) already did.
		branch := in.Branch
		if branch == "" {
			branch = domain.MainBranch
		}
		head, err := g.BranchHead(ctx, in.Namespace, branch)
		if errors.Is(err, ErrNotFound) {
			head, err = g.CreateBaseline(ctx, in.Namespace, "Initial baseline", nil)
		}
		if err != nil {
			return res, err
		}
		in.Baseline = head.ID
	}
	c, err := g.CreateChange(ctx, NewChange{Namespace: in.Namespace, Title: in.Title, Intent: in.Intent, Methodology: in.Methodology,
		BaselineID: in.Baseline, Branch: in.Branch, Data: in.Data, OwnBranch: true})
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
	for _, i := range order {
		e := in.Edits[i]
		w := NodeWrite{Properties: e.Props, State: e.State, RemoveLinks: e.RemoveLinks, Retire: e.Retire}
		for _, l := range e.Links {
			to := l.To
			if l.ToKey != "" {
				ref, ok := written[l.ToKey]
				if !ok {
					return res, fmt.Errorf("link of %s to %q: no such node created by this commit: %w", nodeName(e), l.ToKey, ErrInvalid)
				}
				to = &ref
			}
			if to == nil {
				return res, fmt.Errorf("link of %s needs a target: %w", nodeName(e), ErrInvalid)
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
	} else if done.Status == domain.ChangeMergePending {
		return res, fmt.Errorf("commit %q conflicts with a concurrent change: %w", in.Title, ErrConflict)
	}
	return res, nil
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
// the given order; a cycle among created nodes cannot be written.
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
		on     int
		strict bool // a link by key: the target must exist first
	}
	deps := make([][]dep, len(edits))
	err := g.repo.InTx(ctx, func(tx Tx) error {
		for i, e := range edits {
			for _, l := range e.Links {
				if l.ToKey != "" {
					if j, ok := byKey[l.ToKey]; ok {
						deps[i] = append(deps[i], dep{j, true})
					}
				} else if l.To != nil {
					if j, ok := byNode[l.To.ID]; ok && j != i {
						deps[i] = append(deps[i], dep{j, false})
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
					deps[i] = append(deps[i], dep{j, false})
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
			return nil // a cycle: keep going, the strict ones are checked below
		}
		state[i] = 1
		for _, d := range deps[i] {
			if state[d.on] == 1 && d.strict {
				return fmt.Errorf("the links of %s form a cycle between created nodes: %w", nodeName(edits[i]), ErrInvalid)
			}
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
		Baseline: head.ID, Methodology: methodology, By: "graph.create_object", BaselineName: domain.MainBranch,
		Edits: []NodeEdit{{Key: key, Type: typ, Props: props, Rationale: "Create " + typ + " " + key}}})
	if err != nil {
		return domain.Node{}, domain.Baseline{}, err
	}
	n, err := g.NodeByKey(ctx, namespace, key)
	return n, out.Baseline, err
}
