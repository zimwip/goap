package engine

import (
	"context"
	"fmt"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/journal"
	"github.com/zimwip/goap/engine/methodology"
)

// The oversight floor (EU AI Act Art. 14): with Engine.PersonReviewsModelOutput, what a model produced is accepted by a
// person, whatever the methodology says. An automated action (llm, script, tool, builtin) may still reject a change
// impact, or accept one no model produced (a deployment a script recorded); it may not accept an impact a model produced,
// nor may an llm action accept anything. A human action, whose verdict is the person's, is not constrained.

// automatedKey marks the context of the node operations of an automated action run.
type automatedKey struct{}

// withAutomated marks the node operations of an action run that no person answers: the executor's result.
func withAutomated(ctx context.Context, a methodology.Action) context.Context {
	if a.Kind == methodology.KindHuman {
		return ctx
	}
	return context.WithValue(ctx, automatedKey{}, a.Kind)
}

// automatedKind is the kind of the automated action whose operations run in ctx ("" for a person's).
func automatedKind(ctx context.Context) string {
	k, _ := ctx.Value(automatedKey{}).(string)
	return k
}

// checkOversight refuses the acceptance of impact by an automated action when the floor is on and a model is behind
// the verdict or the impact.
func (e *Engine) checkOversight(ctx context.Context, c domain.Change, impact domain.ChangeImpactID) error {
	if !e.PersonReviewsModelOutput {
		return nil
	}
	kind := automatedKind(ctx)
	if kind == "" {
		return nil
	}
	if kind == methodology.KindLLM {
		return fmt.Errorf("a model may not accept %s: a person reviews it (the oversight floor of the platform): %w", impact, authz.ErrForbidden)
	}
	var cn *domain.ChangeImpact
	for i := range c.Nodes {
		if c.Nodes[i].ID == impact {
			cn = &c.Nodes[i]
		}
	}
	if cn == nil || cn.Execution == "" {
		return nil
	}
	byModel, err := e.calledModel(ctx, c.ID, cn.Execution)
	if err != nil {
		return err
	}
	if byModel {
		return fmt.Errorf("%s was produced by a model: a person reviews it, not an automated action (the oversight floor of the platform): %w", impact, authz.ErrForbidden)
	}
	return nil
}

// calledModel reports whether the action run execution of a change called a model: an exchange in the log, or model
// calls or the llm kind on its journal record.
func (e *Engine) calledModel(ctx context.Context, change domain.ChangeID, execution string) (bool, error) {
	entries, _, err := e.Graph.ChangeLog(ctx, domain.LogFilter{Change: change, Execution: execution,
		Types: []string{journal.StreamModel + ".", journal.StreamJournal + "."}})
	if err != nil {
		return false, err
	}
	for _, en := range entries {
		if en.Stream() == journal.StreamModel {
			return true, nil
		}
	}
	records, err := journal.Decode(entries)
	if err != nil {
		return false, err
	}
	for _, r := range records {
		if len(r.ModelCalls) > 0 || r.ActionKind == methodology.KindLLM {
			return true, nil
		}
	}
	return false, nil
}
