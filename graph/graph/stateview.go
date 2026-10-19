package graph

import (
	"context"
	"maps"
	"slices"

	"github.com/zimwip/goap/contracts/domain"
)

// stateView is the state a baseline leaves as an operation builds on it (ADR 0137): the versions it reads are read
// node by node from the baseline (versionsAt: the chain its state is stored over, ADR 0140), what it changes is kept apart (the delta the next baseline is written with), and the whole state is read only
// for a question about every node (all).
type stateView struct {
	ctx context.Context
	tx  Tx
	// versions reads some nodes of the state (nil: every node): a baseline (versionsAt) or the branch of a change
	// (branchVersions)
	versions func(ids []domain.NodeID) (map[domain.NodeID]domain.Version, error)
	read     map[domain.NodeID]domain.Version // versions read from the baseline
	absent   map[domain.NodeID]bool           // nodes read that the baseline does not hold
	changed  map[domain.NodeID]domain.Version // what the operation changed: a version, 0 for a node it removed
}

func newStateView(ctx context.Context, tx Tx, at domain.BaselineID) *stateView {
	return newView(ctx, tx, func(ids []domain.NodeID) (map[domain.NodeID]domain.Version, error) {
		if ids == nil {
			b, err := tx.Baseline(ctx, at)
			return b.Nodes, err
		}
		return versionsAt(ctx, tx, at, ids)
	})
}

// newBranchView is the state of the branch of a change (ADR 0140): its fork with what landed on it.
func newBranchView(ctx context.Context, tx Tx, b domain.Branch) *stateView {
	return newView(ctx, tx, func(ids []domain.NodeID) (map[domain.NodeID]domain.Version, error) {
		return branchVersions(ctx, tx, b, ids)
	})
}

func newView(ctx context.Context, tx Tx, versions func([]domain.NodeID) (map[domain.NodeID]domain.Version, error)) *stateView {
	return &stateView{ctx: ctx, tx: tx, versions: versions, read: map[domain.NodeID]domain.Version{}, absent: map[domain.NodeID]bool{},
		changed: map[domain.NodeID]domain.Version{}}
}

// load reads the versions of the nodes not read yet, in one read.
func (s *stateView) load(ids ...domain.NodeID) error {
	var missing []domain.NodeID
	for _, id := range ids {
		if _, ok := s.read[id]; ok || s.absent[id] {
			continue
		}
		if _, ok := s.changed[id]; ok {
			continue
		}
		missing = append(missing, id)
	}
	if len(missing) == 0 {
		return nil
	}
	slices.Sort(missing)
	missing = slices.Compact(missing)
	got, err := s.versions(missing)
	if err != nil {
		return err
	}
	for _, id := range missing {
		if v, ok := got[id]; ok {
			s.read[id] = v
		} else {
			s.absent[id] = true
		}
	}
	return nil
}

// get returns the version of a node in the state, and whether the state holds it.
func (s *stateView) get(id domain.NodeID) (domain.Version, bool, error) {
	if v, ok := s.changed[id]; ok {
		return v, v != 0, nil
	}
	if err := s.load(id); err != nil {
		return 0, false, err
	}
	v, ok := s.read[id]
	return v, ok, nil
}

func (s *stateView) set(id domain.NodeID, v domain.Version) { s.changed[id] = v }

func (s *stateView) remove(id domain.NodeID) { s.changed[id] = 0 }

// delta is what the operation changed: a version, 0 for a node it removed.
func (s *stateView) delta() map[domain.NodeID]domain.Version { return maps.Clone(s.changed) }

// all reads the whole state with the changes put in: for a question about every node of the state.
func (s *stateView) all() (map[domain.NodeID]domain.Version, error) {
	out, err := s.versions(nil)
	if err != nil {
		return nil, err
	}
	out = maps.Clone(out)
	if out == nil {
		out = map[domain.NodeID]domain.Version{}
	}
	applyMoves(out, s.changed)
	return out, nil
}

// versionsAt reads the versions of some nodes in a baseline (guardTx.BaselineVersions; a transaction without the guard
// reads the whole state).
func versionsAt(ctx context.Context, tx Tx, at domain.BaselineID, ids []domain.NodeID) (map[domain.NodeID]domain.Version, error) {
	if at == "" || len(ids) == 0 {
		return map[domain.NodeID]domain.Version{}, nil
	}
	if gt, ok := unwrapGuard(tx); ok {
		return gt.BaselineVersions(ctx, at, ids)
	}
	b, err := tx.Baseline(ctx, at)
	if err != nil {
		return nil, err
	}
	out := map[domain.NodeID]domain.Version{}
	for _, id := range ids {
		if v, ok := b.Nodes[id]; ok {
			out[id] = v
		}
	}
	return out, nil
}

// branchHeadHeader returns the head baseline of a branch without its state (ADR 0137); the empty state (no change
// landed yet) has the empty id.
func branchHeadHeader(ctx context.Context, tx Tx, namespace, name string) (domain.Baseline, error) {
	ns, n := domain.NamespaceOf(namespace), domain.BranchOf(name)
	b, err := tx.Branch(ctx, ns, n)
	if err == nil && b.Ephemeral {
		return domain.Baseline{}, errEphemeral(b)
	}
	if err == nil && b.Head != "" {
		return tx.BaselineHeader(ctx, b.Head)
	}
	head, err := branchHead(ctx, tx, namespace, name)
	head.Nodes = nil
	return head, err
}

// headerOf reads a baseline without its state; the empty id is the empty state.
func headerOf(ctx context.Context, tx Tx, id domain.BaselineID) (domain.Baseline, error) {
	if id == "" {
		return domain.Baseline{}, nil
	}
	return tx.BaselineHeader(ctx, id)
}
