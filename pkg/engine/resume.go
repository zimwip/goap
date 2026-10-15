package engine

import (
	"context"
	"slices"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/journal"
	"github.com/zimwip/goap/pkg/methodology"
)

// A step replayed after a recovery never writes twice (ADR 0123). With shared locks the engine records a step on the
// process before running it (checkpoint: its execution id, no end); a run that finds such a step left by a replica
// that stopped asks the change what that execution wrote (its entries carry the execution id) before going on:
//
//   - its action record: the step ended, only the process missed it; it is closed as it is, and the planner looks
//     at the change as it is now;
//   - other entries only (items, impacts, model calls): it was cut halfway; it is not run again but failed, so that
//     the planner takes another way or a person decides (MaxFailures);
//   - nothing: it wrote nothing; it is dropped and the planner runs it again.

// checkpoint records the steps about to run on the process, when the replicas share it.
func (e *Engine) checkpoint(ctx context.Context, p *Process) error {
	if e.Locks == nil {
		return nil
	}
	return e.Store.Put(ctx, p)
}

// What an interrupted step wrote before its replica stopped.
const (
	stepWroteNothing = iota
	stepWrotePart
	stepEnded
)

// settleInterrupted closes the steps a stopped replica left in flight on p (see above). It changes p only.
func (e *Engine) settleInterrupted(ctx context.Context, p *Process, m *methodology.Compiled) error {
	if p.Status != StatusRunning || p.Pending != nil {
		return nil
	}
	drop := map[int]bool{}
	for i := range p.Steps {
		s := &p.Steps[i]
		if s.Execution == "" || !s.EndedAt.IsZero() || s.Error != "" {
			continue
		}
		state, err := e.wrote(ctx, p, s.Execution)
		if err != nil {
			return err
		}
		switch state {
		case stepWroteNothing:
			drop[i] = true
			e.log().Info("an interrupted step wrote nothing: it runs again", "process", p.ID, "action", s.Action)
		case stepEnded:
			s.EndedAt = e.clock()
			s.Output = "recovered: the step had ended when its engine stopped"
			e.log().Info("an interrupted step had ended", "process", p.ID, "action", s.Action)
		default:
			s.EndedAt = e.clock()
			s.Error = "interrupted after writing part of its work: its engine stopped; not run again"
			e.log().Warn("an interrupted step wrote part of its work: failed", "process", p.ID, "action", s.Action)
			kind, ref := "", ""
			if a, ok := m.Action(s.Action); ok {
				kind, ref = a.Kind, a.Step
			}
			e.journal(ctx, p, actionRecord(p, i, kind, ref, s.Execution))
			if err := e.recordFailure(p, s.Action); err != nil {
				return err
			}
		}
	}
	if len(drop) > 0 {
		i := 0
		p.Steps = slices.DeleteFunc(p.Steps, func(Step) bool { i++; return drop[i-1] })
	}
	return nil
}

// wrote tells what the execution of a step wrote on the change of p.
func (e *Engine) wrote(ctx context.Context, p *Process, execution string) (int, error) {
	if p.ChangeID == "" {
		return stepWroteNothing, nil
	}
	entries, _, err := e.Graph.ChangeLog(ctx, domain.LogFilter{Change: p.ChangeID, Execution: execution, Limit: 100})
	if err != nil {
		return 0, err
	}
	if len(entries) == 0 {
		return stepWroteNothing, nil
	}
	for _, en := range entries {
		if en.Type == journal.StreamJournal+"."+journal.KindAction {
			return stepEnded, nil
		}
	}
	return stepWrotePart, nil
}
