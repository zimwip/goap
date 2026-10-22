package graph

import (
	"context"
	"slices"

	"github.com/zimwip/goap/contracts/domain"
)

// The batched reads of the read paths: what a read meets several of (the neighbours of a node, the posts of the
// impacts of a change, the changes of a history) is read in one statement, never one per element, so the round trips of
// a read do not grow with the links of a hub or the length of a history.

// nodesAt reads stored node versions by reference in one statement (Tx.NodesAt). A reference the store does not hold
// is read again alone, so it fails as Tx.Node does.
func nodesAt(ctx context.Context, tx Tx, refs []domain.NodeRef) (map[domain.NodeRef]domain.Node, error) {
	out := make(map[domain.NodeRef]domain.Node, len(refs))
	if len(refs) == 0 {
		return out, nil
	}
	want := slices.Clone(refs)
	slices.SortFunc(want, compareRefs)
	want = slices.Compact(want)
	nodes, err := tx.NodesAt(ctx, want, "")
	if err != nil {
		return nil, err
	}
	for _, n := range nodes {
		out[n.Ref()] = n
	}
	for _, r := range want {
		if _, ok := out[r]; !ok {
			n, err := tx.Node(ctx, r)
			if err != nil {
				return nil, err
			}
			out[r] = n
		}
	}
	return out, nil
}

func compareRefs(a, b domain.NodeRef) int {
	if a.ID != b.ID {
		if a.ID < b.ID {
			return -1
		}
		return 1
	}
	return int(a.Version) - int(b.Version)
}

// nodes reads several nodes as the reader reads one (node): the drafts it holds from them, the stored versions in one
// statement.
func (r *draftReader) nodes(ctx context.Context, tx Tx, refs []domain.NodeRef) (map[domain.NodeRef]domain.Node, error) {
	out := make(map[domain.NodeRef]domain.Node, len(refs))
	var stored []domain.NodeRef
	for _, ref := range refs {
		if _, ok := out[ref]; ok {
			continue
		}
		if ref.IsDraft() {
			if d, ok := r.get(ref.ID); ok {
				out[ref] = r.draftNode(d)
				continue
			}
			// a draft reference the reader holds no draft of reads the stored node, one by one as node does
			n, err := tx.Node(ctx, ref)
			if err != nil {
				return nil, err
			}
			out[ref] = n
			continue
		}
		stored = append(stored, ref)
	}
	read, err := nodesAt(ctx, tx, stored)
	if err != nil {
		return nil, err
	}
	for ref, n := range read {
		out[ref] = n
	}
	return out, nil
}

// changeHeads reads the heads of some changes in one statement (ChangeHeads by IDs); a change the store does not
// hold is absent.
func changeHeads(ctx context.Context, tx Tx, ids []domain.ChangeID) (map[domain.ChangeID]domain.Change, error) {
	out := make(map[domain.ChangeID]domain.Change, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	want := slices.Clone(ids)
	slices.Sort(want)
	heads, err := tx.ChangeHeads(ctx, ChangesFilter{IDs: slices.Compact(want)})
	if err != nil {
		return nil, err
	}
	for _, c := range heads {
		out[c.ID] = c
	}
	return out, nil
}

// storedPosts reads the stored versions the impacts of a change lead to (their posts, a draft left out) in one
// statement, the rejected ones left out, and when accepted only those accepted.
func storedPosts(ctx context.Context, tx Tx, impacts []domain.ChangeImpact, accepted bool) (map[domain.NodeRef]domain.Node, error) {
	var refs []domain.NodeRef
	for _, cn := range impacts {
		if cn.Post == nil || cn.Post.IsDraft() || cn.Review == domain.ReviewRejected || (accepted && cn.Review != domain.ReviewAccepted) {
			continue
		}
		refs = append(refs, *cn.Post)
	}
	return nodesAt(ctx, tx, refs)
}
