package graph

import (
	"context"
	"encoding/json"

	"github.com/zimwip/goap/contracts/domain"
)

// maxLinkDepth is how many deltas deep a link set may be before a version writes its links whole (ADR 0141): the
// bound of the chain a read of the links of a version walks.
const maxLinkDepth = 16

// putLinks writes the outgoing links of a version written in the transaction (ADR 0141 and its addendum). When one of
// the versions like (earlier versions of the node: the one it was checked out from first) holds the same links, the
// version shares its set and writes no row. Else it writes a new set, the next in order after the one the first of like
// holds (else the previous version): a delta (the links it adds, a row per link it drops) over the set baseSet picks
// when that delta has fewer rows than its links, else its links whole. A link it keeps keeps its id.
func (g *Graph) putLinks(ctx context.Context, tx Tx, n domain.Node, links []domain.Link, like ...domain.NodeRef) error {
	from := n.Ref()
	var prev domain.NodeRef // the version the new set follows
	for _, ref := range like {
		if ref.ID != n.ID || ref.Version < 1 || ref.Version >= n.Version {
			continue
		}
		if prev.ID == "" {
			prev = ref
		}
		held, err := tx.OutLinks(ctx, ref)
		if err != nil {
			return err
		}
		if a, d := diffLinks(held, links); len(a) == 0 && len(d) == 0 {
			set, err := tx.LinkSetOf(ctx, ref)
			if err != nil {
				return err
			}
			return tx.PutLinkSet(ctx, from, LinkSet{Of: set.Of, Depth: set.Depth, Seq: set.Seq})
		}
	}
	if prev.ID == "" && n.Version > 1 {
		prev = domain.NodeRef{ID: n.ID, Version: n.Version - 1}
	}
	seq := 1
	var (
		delta       bool
		base        LinkSet
		adds, drops []domain.Link
	)
	if prev.ID != "" {
		set, err := tx.LinkSetOf(ctx, prev)
		if err != nil {
			return err
		}
		seq = set.Seq + 1
		if b, ok, err := baseSet(ctx, tx, prev, set, seq); err != nil {
			return err
		} else if ok && len(links) > 0 {
			held, err := tx.OutLinks(ctx, domain.NodeRef{ID: n.ID, Version: b.Of})
			if err != nil {
				return err
			}
			if a, d := diffLinks(held, links); len(a)+len(d) < len(links) {
				base, adds, drops, delta = b, a, d, true
			}
		}
	}
	if !delta {
		if err := tx.PutLinkSet(ctx, from, LinkSet{Seq: seq}); err != nil {
			return err
		}
		return tx.PutLinks(ctx, stamped(links, from, n.ChangeID))
	}
	if err := tx.PutLinkSet(ctx, from, LinkSet{Base: base.Of, Depth: base.Depth + 1, Seq: seq}); err != nil {
		return err
	}
	if err := tx.PutLinks(ctx, stamped(adds, from, n.ChangeID)); err != nil {
		return err
	}
	for _, l := range drops {
		l.ChangeID = n.ChangeID
		if err := tx.DropLink(ctx, from, domain.LinkID(g.newID()), l); err != nil {
			return err
		}
	}
	return nil
}

// stamped are links leaving a version written by a change: copies, the caller's untouched.
func stamped(links []domain.Link, from domain.NodeRef, change domain.ChangeID) []domain.Link {
	out := make([]domain.Link, len(links))
	for i, l := range links {
		l.From, l.ChangeID = from, change
		out[i] = l
	}
	return out
}

// baseSet is the set the set of order seq is a delta over (putLinks): the one of order seq with its lowest bit cleared,
// found on the chain of the set prev holds (that set, then the set each is a delta over); none when that order is 0 (a
// power of two writes its links whole) or the chain does not reach it.
//
// These are skip deltas: the chain of the set of order k is as deep as the bits set in k, less one, and over K sets a
// delta holds the changes of about log2(K)/2 sets. A delta over the set prev holds would make a node whose links grow
// write them whole every maxLinkDepth sets: rows in the size of its links at every few sets.
func baseSet(ctx context.Context, tx Tx, prev domain.NodeRef, set LinkSet, seq int) (LinkSet, bool, error) {
	want := seq & (seq - 1)
	if want == 0 {
		return LinkSet{}, false, nil
	}
	var err error
	if set.Of != prev.Version { // prev shares the set of an earlier version: how that one holds it
		if set, err = tx.LinkSetOf(ctx, domain.NodeRef{ID: prev.ID, Version: set.Of}); err != nil {
			return set, false, err
		}
	}
	for set.Seq > want && set.Base != 0 {
		if set, err = tx.LinkSetOf(ctx, domain.NodeRef{ID: prev.ID, Version: set.Base}); err != nil {
			return set, false, err
		}
	}
	return set, set.Seq == want && set.Depth < maxLinkDepth, nil
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
	return l.Type + "\x00" + l.Points() + "\x00" + string(p)
}

// linkTarget is how a row of the link table names the target of a link (ADR 0154): to_id and to_version for a node of
// this graph, null with to_url for a target in another source.
func linkTarget(l domain.Link) (to, version any, url string) {
	if l.External() {
		return nil, nil, l.Target.String()
	}
	return string(l.To.ID), int(l.To.Version), ""
}
