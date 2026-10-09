package engine

import (
	"context"
	"fmt"

	"github.com/zimwip/goap/pkg/changeapi"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/engine/blackboard"
)

// GuardianName is the name of the guardian of the changes governed by a methodology (ADR 0098), the one a change
// names (Change.Guardian) and the graph resolves: the engine, asked in process or over the GuardianService.
const GuardianName = "methodology"

// Guardian is the guardian of the changes governed by a methodology (changeapi.Guardian, ADR 0098): the methodology
// defines the lifecycle of a change and its rules, the change follows them through its guardian. It freezes the
// impacts started in a state the change has left (MayEdit) and lands a change only in a final state of its lifecycle
// (MayCommit); Next adds the rules of the registry (the goal of the Activity a change is scoped to, the sub_activity
// cascade of its sub-changes, the projects a change may move between); nil: none.
type Guardian struct {
	Engine *Engine
	Next   changeapi.Guardian
}

var _ changeapi.Guardian = Guardian{}

// lifecycle reads what the guardian needs of a change: its lifecycle (nil: it follows none) and its execution view.
func (gd Guardian) lifecycle(ctx context.Context, c domain.Change) (*domain.Lifecycle, blackboard.View, error) {
	lc, err := gd.Engine.lifecycleOf(ctx, c)
	if err != nil || lc == nil {
		return nil, blackboard.View{}, err
	}
	objs, err := gd.Engine.Graph.Objects(ctx, c.ID, domain.ObjectFilter{Types: []string{blackboard.TypeState, blackboard.TypeTransition}})
	if err != nil {
		return nil, blackboard.View{}, err
	}
	return lc, blackboard.View{Change: c, Objects: objs}, nil
}

// MayEdit implements changeapi.Guardian: an impact started in a state the change has left is frozen (ADR 0058 §4);
// going back to that state unfreezes it.
func (gd Guardian) MayEdit(ctx context.Context, c domain.Change, impact domain.ChangeImpactID) error {
	lc, v, err := gd.lifecycle(ctx, c)
	if err != nil {
		return err
	}
	if lc != nil {
		moves := v.Moves()
		ph, err := gd.Engine.phases(ctx, c.ID, moves)
		if err != nil {
			return err
		}
		cur := stateOf(v, lc)
		if p, ok := ph[impact]; ok && p.State != cur.State {
			return fmt.Errorf("change impact %s was written in state %s, which the change has left (now %s): go back to it first: %w",
				impact, p.State, cur.State, changeapi.ErrConflict)
		}
	}
	if gd.Next != nil {
		return gd.Next.MayEdit(ctx, c, impact)
	}
	return nil
}

// MayCommit implements changeapi.Guardian: a change following a lifecycle lands in a final state of it (ADR 0058 §4).
func (gd Guardian) MayCommit(ctx context.Context, c domain.Change, bb domain.Blackboard) (decided, ok bool, err error) {
	lc, v, err := gd.lifecycle(ctx, c)
	if err != nil {
		return false, false, err
	}
	if lc != nil {
		cur := stateOf(v, lc)
		if s, found := lc.State(cur.State); !found || !s.Final {
			return false, false, fmt.Errorf("change %s is in state %s of lifecycle %s: it can be applied once it is in a final state: %w",
				c.ID, cur.State, lc.Name, changeapi.ErrConflict)
		}
	}
	if gd.Next != nil {
		return gd.Next.MayCommit(ctx, c, bb)
	}
	return false, false, nil
}

// MayCreateChild implements changeapi.Guardian.
func (gd Guardian) MayCreateChild(ctx context.Context, parent, child domain.Change) error {
	if gd.Next != nil {
		return gd.Next.MayCreateChild(ctx, parent, child)
	}
	return nil
}

// MayMove implements changeapi.Guardian.
func (gd Guardian) MayMove(ctx context.Context, family []domain.Change, to string) error {
	if gd.Next != nil {
		return gd.Next.MayMove(ctx, family, to)
	}
	return nil
}
