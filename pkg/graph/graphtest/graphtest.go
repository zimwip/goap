// Package graphtest lands the fixtures of tests through the operations every change uses (ADR 0076): a node is created
// in a change of its own, accepted, checked in, moved to its state and applied.
package graphtest

import (
	"context"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
)

// Node is a node a fixture lands: Namespace (default domain.DefaultNamespace), Key (required), Type, the Properties of
// its first version, its outgoing Links, the lifecycle State it is moved to and the unit owning it (its key).
type Node struct {
	Namespace  string
	Key        string
	Type       string
	Properties map[string]any
	State      string
	Owner      string
	Links      []graph.LinkWrite
}

// open opens a change of its own on the head of main of a namespace.
func open(ctx context.Context, g *graph.Graph, ns, title string) (domain.Change, error) {
	head, err := g.BranchHead(ctx, ns, domain.MainBranch)
	if err != nil {
		return domain.Change{}, err
	}
	return g.CreateChange(ctx, graph.NewChange{Namespace: ns, Title: title, Intent: title, BaselineID: head.ID, OwnBranch: true})
}

// Import lands a node on main through a change of its own and returns its latest version. It names no required
// parent: a fixture adds structure links with Links, or afterwards (Link).
func Import(ctx context.Context, g *graph.Graph, in Node) (domain.Node, error) {
	ns := domain.NamespaceOf(in.Namespace)
	c, err := open(ctx, g, ns, "Import "+in.Key)
	if err != nil {
		return domain.Node{}, err
	}
	cn, err := g.CreateNode(ctx, c.ID, graph.NodeCreate{Key: in.Key, Type: in.Type, Properties: in.Properties, Owner: in.Owner, Rationale: "Import " + in.Key, Links: in.Links})
	if err != nil {
		return domain.Node{}, err
	}
	if err := AcceptAndCheckin(ctx, g, c.ID, cn.ID); err != nil {
		return domain.Node{}, err
	}
	if in.State != "" {
		n, err := g.Node(ctx, *cn.Post)
		if err != nil {
			return domain.Node{}, err
		}
		if n.State != in.State {
			if _, err := g.TransitionNode(ctx, c.ID, graph.NodeTransition{NodeCheckout: graph.NodeCheckout{Impact: cn.ID}, To: in.State}); err != nil {
				return domain.Node{}, err
			}
		}
	}
	if _, err := g.Apply(ctx, c.ID, ""); err != nil {
		return domain.Node{}, err
	}
	return g.NodeByKey(ctx, ns, in.Key)
}

// Edit lands an edit of the latest version of a node on main through a change of its own: checked out, its
// properties merged, the links added, accepted, checked in, applied. It returns the new version.
func Edit(ctx context.Context, g *graph.Graph, node domain.NodeID, props map[string]any, links ...graph.LinkWrite) (domain.Node, error) {
	cur, err := g.Node(ctx, domain.NodeRef{ID: node})
	if err != nil {
		return cur, err
	}
	c, err := open(ctx, g, domain.NamespaceOf(cur.Namespace), "Edit "+cur.Key)
	if err != nil {
		return cur, err
	}
	cn, err := g.CheckoutNode(ctx, c.ID, graph.NodeCheckout{Node: node, Rationale: "Edit " + cur.Key})
	if err != nil {
		return cur, err
	}
	if len(props) > 0 {
		if _, err := g.UpdateNode(ctx, c.ID, cn.ID, graph.NodeUpdate{Properties: props}); err != nil {
			return cur, err
		}
	}
	for _, l := range links {
		if _, err := g.CreateLink(ctx, c.ID, cn.ID, l, "", ""); err != nil {
			return cur, err
		}
	}
	if err := AcceptAndCheckin(ctx, g, c.ID, cn.ID); err != nil {
		return cur, err
	}
	if _, err := g.Apply(ctx, c.ID, ""); err != nil {
		return cur, err
	}
	return g.Node(ctx, *cn.Post)
}

// Link lands an outgoing link of the latest version of a node on main (Edit): the source gets a new version.
func Link(ctx context.Context, g *graph.Graph, typ string, from, to domain.NodeRef) (domain.Node, error) {
	return Edit(ctx, g, from.ID, nil, graph.LinkWrite{Type: typ, To: to})
}

// AcceptAndCheckin accepts a change impact of the main flow when it is proposed, and checks its working version in.
func AcceptAndCheckin(ctx context.Context, g *graph.Graph, change domain.ChangeID, impact domain.ChangeImpactID) error {
	impacts, err := g.ListChangeImpacts(ctx, change)
	if err != nil {
		return err
	}
	for _, cn := range impacts {
		if cn.ID != impact {
			continue
		}
		if cn.Review == domain.ReviewProposed {
			if cn, err = g.ReviewNodeOn(ctx, change, domain.MainFlow, "", impact, domain.ReviewAccepted, "tester", "ok"); err != nil {
				return err
			}
		}
		if cn.Post == nil {
			return nil
		}
		n, err := g.Node(ctx, *cn.Post)
		if err != nil || !n.CheckedOut {
			return err
		}
		_, err = g.CheckinNode(ctx, change, impact, domain.MainFlow, "")
		return err
	}
	return nil
}

// Project lands a project under the root project of the graph (ADR 0039: a project is created with its parent).
func Project(ctx context.Context, g *graph.Graph, key, name string) (domain.Node, error) {
	return under(ctx, g, domain.StructureProject, key, name)
}

// Unit lands a unit of the organisation under its root unit (ADR 0054: a unit is created with its parent).
func Unit(ctx context.Context, g *graph.Graph, key, name string) (domain.Node, error) {
	return under(ctx, g, domain.StructureOrganisation, key, name)
}

func under(ctx context.Context, g *graph.Graph, kind, key, name string) (domain.Node, error) {
	if err := g.Bootstrap(ctx); err != nil {
		return domain.Node{}, err
	}
	st := g.Structure(kind)
	root, err := g.NodeByKey(ctx, st.Namespace, st.Root)
	if err != nil {
		return domain.Node{}, err
	}
	return Import(ctx, g, Node{Namespace: st.Namespace, Key: key, Type: st.Type, Properties: map[string]any{"name": name},
		Links: []graph.LinkWrite{{Type: st.Parent, To: root.Ref()}}})
}
