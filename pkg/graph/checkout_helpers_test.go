package graph

import (
	"context"
	"errors"
	"fmt"

	"github.com/zimwip/goap/pkg/domain"
)

// The fixtures of the tests write through the operations every change uses (ADR 0076).

// newNode is a node a fixture lands: Namespace (default domain.DefaultNamespace), Key (default: an id), Type, the
// Properties of its first version, the lifecycle State it is moved to and the unit owning it.
type newNode struct {
	Namespace  string
	Key        string
	Type       string
	Properties map[string]any
	State      string
	Owner      string
	// Links are the outgoing links of its first version.
	Links []LinkWrite
}

// importNode lands a node on main through a change of its own: ImpactNodeCreate, moved to its State, accepted,
// applied. It names no required parent: fixtures add structure links afterwards (importLink).
func importNode(ctx context.Context, g *Graph, in newNode) (domain.Node, error) {
	ns := domain.NamespaceOf(in.Namespace)
	if in.Key == "" {
		in.Key = g.newID()
	}
	head, err := g.BranchHead(ctx, ns, domain.MainBranch)
	if err != nil {
		return domain.Node{}, err
	}
	c, err := g.CreateChange(ctx, NewChange{Namespace: ns, Title: "Import " + in.Key, Intent: "Import " + in.Key, BaselineID: head.ID, OwnBranch: true})
	if err != nil {
		return domain.Node{}, err
	}
	cn, err := g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: in.Key, Type: in.Type, Properties: in.Properties, Owner: in.Owner, Rationale: "Import " + in.Key, Links: in.Links})
	if err != nil {
		return domain.Node{}, err
	}
	if in.State != "" {
		n, err := g.Node(ctx, *cn.Post)
		if err != nil {
			return domain.Node{}, err
		}
		if n.State != in.State {
			if _, err := g.ImpactNodeTransition(ctx, c.ID, NodeTransition{NodeCheckout: NodeCheckout{Impact: cn.ID}, To: in.State}); err != nil {
				return domain.Node{}, err
			}
		}
	}
	if err := g.acceptImpact(ctx, c.ID, cn.ID, ""); err != nil {
		return domain.Node{}, err
	}
	if _, err := g.Apply(ctx, c.ID, ""); err != nil {
		return domain.Node{}, err
	}
	return g.NodeByKey(ctx, ns, in.Key)
}

// importLink adds an outgoing link to the latest version of a node on main through a change of its own: the node is
// checked out, linked, accepted and the change applied. The source gets a new version (outgoing links
// belong to the source version, ADR 0003); the link of that version is returned.
func importLink(ctx context.Context, g *Graph, typ string, from, to domain.NodeRef, props map[string]any) (domain.Link, error) {
	src, err := g.Node(ctx, domain.NodeRef{ID: from.ID})
	if err != nil {
		return domain.Link{}, err
	}
	ns := domain.NamespaceOf(src.Namespace)
	head, err := g.BranchHead(ctx, ns, domain.MainBranch)
	if err != nil {
		return domain.Link{}, err
	}
	c, err := g.CreateChange(ctx, NewChange{Namespace: ns, Title: "Link " + src.Key, Intent: "Link " + src.Key, BaselineID: head.ID, OwnBranch: true})
	if err != nil {
		return domain.Link{}, err
	}
	cn, err := g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Node: from.ID, Rationale: "Link " + src.Key})
	if err != nil {
		return domain.Link{}, err
	}
	l, err := g.ImpactLinkCreate(ctx, c.ID, cn.ID, LinkWrite{Type: typ, To: to, Properties: props}, "", "")
	if err != nil {
		return l, err
	}
	if err := g.acceptImpact(ctx, c.ID, cn.ID, ""); err != nil {
		return l, err
	}
	if _, err := g.Apply(ctx, c.ID, ""); err != nil {
		return l, err
	}
	return l, nil
}

// importProps sets properties of the latest version of a node on main through a change of its own (checkout, update,
// accepted, applied) and returns the new version.
func importProps(ctx context.Context, g *Graph, ref domain.NodeRef, props map[string]any) (domain.Node, error) {
	src, err := g.Node(ctx, domain.NodeRef{ID: ref.ID})
	if err != nil {
		return src, err
	}
	ns := domain.NamespaceOf(src.Namespace)
	head, err := g.BranchHead(ctx, ns, domain.MainBranch)
	if err != nil {
		return src, err
	}
	c, err := g.CreateChange(ctx, NewChange{Namespace: ns, Title: "Edit " + src.Key, Intent: "Edit " + src.Key, BaselineID: head.ID, OwnBranch: true})
	if err != nil {
		return src, err
	}
	cn, err := g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Node: ref.ID, Rationale: "Edit " + src.Key})
	if err != nil {
		return src, err
	}
	if _, err := g.ImpactNodeUpdate(ctx, c.ID, cn.ID, NodeUpdate{Properties: props}); err != nil {
		return src, err
	}
	if err := g.acceptImpact(ctx, c.ID, cn.ID, ""); err != nil {
		return src, err
	}
	if _, err := g.Apply(ctx, c.ID, ""); err != nil {
		return src, err
	}
	return g.Node(ctx, *cn.Post)
}

// acceptImpact accepts a change impact on a flow when it is proposed: accepting freezes its working version.
func (g *Graph) acceptImpact(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, flow string) error {
	seen, err := g.seenImpact(ctx, id, impact, flow)
	if err != nil {
		return err
	}
	if seen.Review == domain.ReviewProposed {
		if _, err := g.ImpactNodeReviewOn(ctx, id, flow, "", impact, domain.ReviewAccepted, "tester", "ok"); err != nil {
			return err
		}
	}
	return nil
}

// checkedOut reports whether a change impact has a working version of its own on a flow.
func (g *Graph) checkedOut(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, flow string) (out bool, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		w, err := g.workOn(ctx, tx, id, flow)
		if err != nil {
			return err
		}
		if err := w.selectImpact(impact); err != nil {
			return err
		}
		h, err := w.head(ctx, tx)
		out = h != nil && IsWorking(w.c, flow, *h)
		return err
	})
	return
}

// seenImpact is a change impact as a flow sees it.
func (g *Graph) seenImpact(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, flow string) (cn domain.ChangeImpact, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		cn, err = g.newFlowNodes(tx, c, c.ResolveFlow(flow)).find(ctx, impact)
		return err
	})
	return
}

// edit is an edit of a change impact in a test: its properties, owner and links are edited on its working version
// (checked out when it is not); a State is a transition (in place on a working version).
type edit struct {
	Properties      map[string]any
	State           string
	AddLinks        []LinkWrite
	RemoveLinks     []domain.LinkID
	Flow, Execution string
	Owner           string
}

// edit applies an edit to a change impact and returns it as the flow sees it.
func (g *Graph) edit(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, e edit) (domain.ChangeImpact, error) {
	if len(e.Properties) > 0 || e.Owner != "" || len(e.AddLinks) > 0 || len(e.RemoveLinks) > 0 {
		out, err := g.checkedOut(ctx, id, impact, e.Flow)
		if err != nil {
			return domain.ChangeImpact{}, err
		}
		if !out {
			if _, err := g.ImpactNodeCheckout(ctx, id, NodeCheckout{Impact: impact, Flow: e.Flow, Execution: e.Execution}); err != nil {
				return domain.ChangeImpact{}, err
			}
		}
		if len(e.Properties) > 0 || e.Owner != "" {
			if _, err := g.ImpactNodeUpdate(ctx, id, impact, NodeUpdate{Properties: e.Properties, Owner: e.Owner, Flow: e.Flow, Execution: e.Execution}); err != nil {
				return domain.ChangeImpact{}, err
			}
		}
		for _, l := range e.AddLinks {
			if _, err := g.ImpactLinkCreate(ctx, id, impact, l, e.Flow, e.Execution); err != nil {
				return domain.ChangeImpact{}, err
			}
		}
		for _, link := range e.RemoveLinks {
			if err := g.removeLinkOf(ctx, id, impact, link, e.Flow, e.Execution); err != nil {
				return domain.ChangeImpact{}, err
			}
		}
	}
	if e.State != "" {
		if _, err := g.ImpactNodeTransition(ctx, id, NodeTransition{NodeCheckout: NodeCheckout{Impact: impact, Flow: e.Flow, Execution: e.Execution}, To: e.State}); err != nil {
			return domain.ChangeImpact{}, err
		}
	}
	return g.seenImpact(ctx, id, impact, e.Flow)
}

// removeLinkOf removes a link from the working version of a change impact: the link itself, or the copy the checkout
// made of a link of the version it follows.
func (g *Graph) removeLinkOf(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, link domain.LinkID, flow, execution string) error {
	err := g.ImpactLinkDelete(ctx, id, link, flow, execution)
	if err == nil || !errors.Is(err, ErrConflict) {
		return err
	}
	seen, serr := g.seenImpact(ctx, id, impact, flow)
	if serr != nil {
		return serr
	}
	if seen.Post == nil {
		return err
	}
	var copied domain.LinkID
	if terr := g.repo.InTx(ctx, func(tx Tx) error {
		l, err := tx.Link(ctx, link)
		if err != nil {
			return err
		}
		out, err := tx.OutLinks(ctx, *seen.Post)
		for _, c := range out {
			if c.Type == l.Type && c.To.ID == l.To.ID {
				copied = c.ID
			}
		}
		return err
	}); terr != nil {
		return terr
	}
	if copied == "" {
		return fmt.Errorf("link %s: no copy on %s: %w", link, seen.Post, err)
	}
	return g.ImpactLinkDelete(ctx, id, copied, flow, execution)
}

// acceptAll accepts every proposed change impact of the main flow of a change (freezing their working versions).
func (g *Graph) acceptAll(ctx context.Context, id domain.ChangeID) error {
	c, err := g.Change(ctx, id)
	if err != nil {
		return err
	}
	for _, cn := range c.Nodes {
		if cn.Flow != "" || cn.Superseded || cn.Review == domain.ReviewRejected {
			continue
		}
		if err := g.acceptImpact(ctx, id, cn.ID, domain.MainFlow); err != nil {
			return err
		}
	}
	return nil
}

// accept accepts a change impact (freezing its working version): what a test applies next lands it.
func (g *Graph) accept(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, by, comment string) (domain.ChangeImpact, error) {
	return g.acceptOn(ctx, id, "", "", impact, by, comment)
}

// acceptOn accepts a change impact on a flow, freezing its working version there.
func (g *Graph) acceptOn(ctx context.Context, id domain.ChangeID, flow, execution string, impact domain.ChangeImpactID, by, comment string) (domain.ChangeImpact, error) {
	if _, err := g.ImpactNodeReviewOn(ctx, id, flow, execution, impact, domain.ReviewAccepted, by, comment); err != nil {
		return domain.ChangeImpact{}, err
	}
	return g.seenImpact(ctx, id, impact, flow)
}

// proposeOrCreate declares impacts in a test: a node of the baseline is proposed (ProposeImpact), a new node is created
// (ImpactNodeCreate: it has no proposal, ADR 0077), in order. A created impact comes back with its working version.
func (g *Graph) proposeOrCreate(ctx context.Context, id domain.ChangeID, nodes []domain.ChangeImpact) ([]domain.ChangeImpact, error) {
	out := make([]domain.ChangeImpact, 0, len(nodes))
	for _, cn := range nodes {
		if cn.Intent == domain.IntentCreated {
			made, err := g.ImpactNodeCreate(ctx, id, NodeCreate{Key: cn.Key, Type: cn.Type, Rationale: cn.Rationale, Flow: cn.Flow, Execution: cn.Execution, ProducedBy: cn.ProducedBy, DerivedFrom: cn.DerivedFrom})
			if err != nil {
				return out, err
			}
			out = append(out, made)
			continue
		}
		added, err := g.ProposeImpact(ctx, id, []domain.ChangeImpact{cn})
		if err != nil {
			return out, err
		}
		out = append(out, added...)
	}
	return out, nil
}
