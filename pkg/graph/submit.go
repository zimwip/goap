package graph

import (
	"context"

	"github.com/zimwip/goap/pkg/domain"
)

// Batch is what Submit writes on a change in one transaction (ADR 0098): impact operations, items and change objects.
// They are applied in that order (a change object keyed by an impact may name one the batch creates).
type Batch struct {
	Creates   []NodeCreate
	Checkouts []NodeCheckout
	Updates   []ImpactUpdate
	Links     []ImpactLink
	Items     []domain.ChangeItem
	Objects   []domain.ObjectWrite
}

// ImpactUpdate is an ImpactNodeUpdate of a batch.
type ImpactUpdate struct {
	Impact domain.ChangeImpactID
	NodeUpdate
}

// ImpactLink is an ImpactLinkCreate of a batch.
type ImpactLink struct {
	Impact          domain.ChangeImpactID
	Link            LinkWrite
	Flow, Execution string
}

// BatchResult is what a batch wrote, in the order of the batch: the impacts of its creates, checkouts and updates,
// its links, its items and its change objects.
type BatchResult struct {
	Impacts []domain.ChangeImpact
	Links   []domain.Link
	Items   []domain.ChangeItem
	Objects []domain.ChangeObject
}

// IsEmpty reports a batch that writes nothing.
func (b Batch) IsEmpty() bool {
	return len(b.Creates)+len(b.Checkouts)+len(b.Updates)+len(b.Links)+len(b.Items)+len(b.Objects) == 0
}

// Submit applies a batch to a change in one transaction, all or none (ADR 0098): an action's outputs are written
// together, and a run replayed after a failure finds nothing half written.
func (g *Graph) Submit(ctx context.Context, id domain.ChangeID, b Batch) (res BatchResult, err error) {
	if b.IsEmpty() {
		return res, invalidf("nothing to submit")
	}
	for _, in := range b.Creates {
		if in.Key == "" || in.Type == "" {
			return res, invalidf("a node needs a key and a type")
		}
	}
	for _, u := range b.Updates {
		if len(u.Properties) == 0 && u.Owner == "" {
			return res, invalidf("nothing to update")
		}
	}
	if len(b.Items) > 0 {
		if err := g.authorizeItems(ctx, id, b.Items); err != nil {
			return res, err
		}
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		res = BatchResult{}
		for _, in := range b.Creates {
			cn, err := g.createTx(ctx, tx, id, in, nil)
			if err != nil {
				return err
			}
			res.Impacts = append(res.Impacts, cn)
		}
		for _, in := range b.Checkouts {
			w, err := g.resolve(ctx, tx, id, in.target())
			if err != nil {
				return err
			}
			ref, err := g.checkoutTx(ctx, tx, w, in.Execution, false)
			if err != nil {
				return err
			}
			res.Impacts = append(res.Impacts, w.seenAs(&ref))
		}
		for _, u := range b.Updates {
			cn, err := g.updateTx(ctx, tx, id, u.Impact, u.NodeUpdate)
			if err != nil {
				return err
			}
			res.Impacts = append(res.Impacts, cn)
		}
		for _, l := range b.Links {
			link, err := g.linkCreateTx(ctx, tx, id, l.Impact, l.Link, l.Flow, l.Execution)
			if err != nil {
				return err
			}
			res.Links = append(res.Links, link)
		}
		if len(b.Items) > 0 {
			items, err := g.addItemsTx(ctx, tx, id, b.Items)
			if err != nil {
				return err
			}
			res.Items = items
		}
		if len(b.Objects) > 0 {
			objs, err := g.putObjectsTx(ctx, tx, id, b.Objects)
			if err != nil {
				return err
			}
			res.Objects = objs
		}
		return nil
	})
	return
}
