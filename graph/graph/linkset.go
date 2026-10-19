package graph

import (
	"context"
	"encoding/json"

	"github.com/zimwip/goap/contracts/domain"
)

// maxLinkDepth is how many deltas deep a link set may be before a version writes its links whole (ADR 0141): the
// bound of the chain a read of the links of a version walks.
const maxLinkDepth = 16

// putLinks writes the outgoing links of a version written in the transaction (ADR 0141). When one of the versions like
// (earlier versions of the node: the one it was checked out from first) holds the same links, the version shares its
// set and writes no row; else, when the first of them is close enough, the version writes a delta over its set (the
// links it adds, a row per link it drops) when that is at most half its links and the set is not too deep; else it
// writes its links whole. A link it keeps keeps its id.
func (g *Graph) putLinks(ctx context.Context, tx Tx, n domain.Node, links []domain.Link, like ...domain.NodeRef) error {
	from := n.Ref()
	var (
		delta       bool
		base        LinkSet
		adds, drops []domain.Link
	)
	for _, ref := range like {
		if ref.ID != n.ID || ref.Version < 1 || ref.Version >= n.Version {
			continue
		}
		held, err := tx.OutLinks(ctx, ref)
		if err != nil {
			return err
		}
		set, err := tx.LinkSetOf(ctx, ref)
		if err != nil {
			return err
		}
		a, d := diffLinks(held, links)
		if len(a) == 0 && len(d) == 0 {
			return tx.PutLinkSet(ctx, from, LinkSet{Of: set.Of, Depth: set.Depth})
		}
		if !delta && base.Of == 0 {
			base, adds, drops = set, a, d
			delta = set.Depth < maxLinkDepth && 2*(len(a)+len(d)) <= len(links)
		}
	}
	if !delta {
		for _, l := range links {
			l.From, l.ChangeID = from, n.ChangeID
			if err := tx.PutLink(ctx, l); err != nil {
				return err
			}
		}
		return nil
	}
	if err := tx.PutLinkSet(ctx, from, LinkSet{Base: base.Of, Depth: base.Depth + 1}); err != nil {
		return err
	}
	for _, l := range adds {
		l.From, l.ChangeID = from, n.ChangeID
		if err := tx.PutLink(ctx, l); err != nil {
			return err
		}
	}
	for _, l := range drops {
		l.ChangeID = n.ChangeID
		if err := tx.DropLink(ctx, from, domain.LinkID(g.newID()), l); err != nil {
			return err
		}
	}
	return nil
}

// diffLinks compares the links a version holds with the ones a new version holds, by type, target and properties
// (the ids of a draft's links are its own): the links to add, and the held ones to drop.
func diffLinks(held, want []domain.Link) (adds, drops []domain.Link) {
	unmatched := map[string][]domain.Link{}
	for _, l := range held {
		k := linkKey(l)
		unmatched[k] = append(unmatched[k], l)
	}
	for _, l := range want {
		k := linkKey(l)
		if ls := unmatched[k]; len(ls) > 0 {
			unmatched[k] = ls[1:]
			continue
		}
		adds = append(adds, l)
	}
	for _, l := range held {
		k := linkKey(l)
		if ls := unmatched[k]; len(ls) > 0 && ls[0].ID == l.ID {
			unmatched[k] = ls[1:]
			drops = append(drops, l)
		}
	}
	return adds, drops
}

// linkKey is what makes two links the same link of two versions of a node.
func linkKey(l domain.Link) string {
	p, _ := json.Marshal(l.Properties) // maps marshal with sorted keys
	if len(l.Properties) == 0 {
		p = nil
	}
	return l.Type + "\x00" + l.To.String() + "\x00" + string(p)
}
