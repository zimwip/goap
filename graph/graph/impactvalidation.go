package graph

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/zimwip/goap/contracts/domain"
)

// The validation of a change impact (domain.ImpactValidation) is derived, never stored and no event: what the review gate
// of an acceptance (checkAccepted) would say of the draft as it stands. The views of a change carry it (ChangeChecked,
// BlackboardIn) so a person sees the failure on the impact and not only after accepting it. An acceptance still runs
// the check itself: this is a preview, not a gate.
//
// A change's validations are computed per flow and kept by the position of the impact log they were computed at (the
// same position that validates the draft cache): a view of an unchanged change runs no validator.

type validationKey struct {
	change domain.ChangeID
	flow   string
}

type validationEntry struct {
	seq int64
	out map[domain.ChangeImpactID]*domain.ImpactValidation
}

// validationMessage is the readable text of a refusal: the error without the sentinel the graph wraps it with.
func validationMessage(err error) string {
	msg := err.Error()
	for _, s := range []error{ErrInvalid, ErrConflict} {
		msg = strings.TrimSuffix(msg, ": "+s.Error())
	}
	return msg
}

// impactValidations checks the drafts the flow sees, by change impact. A refusal of the gate (ErrInvalid, and the
// origins gate, ErrConflict) is a result; any other error is no verdict: the impact is left out and nothing is cached.
func (g *Graph) impactValidations(ctx context.Context, tx Tx, c domain.Change, flow string) (map[domain.ChangeImpactID]*domain.ImpactValidation, error) {
	switch c.Status {
	case domain.ChangeApplied, domain.ChangeAbandoned, domain.ChangeCommitted:
		return nil, nil
	}
	if flow != "" {
		if _, ok := c.Flow(flow); !ok || c.FlowStatusOf(flow) != domain.FlowOpen {
			return nil, nil
		}
	}
	st, err := g.draftState(ctx, tx, c.ID)
	if err != nil {
		return nil, err
	}
	key := validationKey{c.ID, flow}
	if out, ok := g.validations.Get(key); ok && out.seq == st.seq {
		return out.out, nil
	}
	seen, err := g.newFlowNodes(tx, c, flow).nodes(ctx)
	if err != nil {
		return nil, err
	}
	drafts, err := g.seenDrafts(ctx, tx, c, flow)
	if err != nil {
		return nil, err
	}
	ix, err := g.typesAt(ctx, tx, c.ForkCommit)
	if err != nil {
		return nil, err
	}
	out := map[domain.ChangeImpactID]*domain.ImpactValidation{}
	complete := true
	for _, cn := range seen.All() {
		d, ok := drafts[cn.ID]
		if cn.Superseded || !cn.Drafted() || !ok {
			continue
		}
		modified, err := g.draftModifies(ctx, tx, &d)
		if err != nil {
			slog.Debug("impact validation skipped", "change", c.ID, "impact", cn.ID, "err", err)
			complete = false
			continue
		}
		err = g.checkDraftOf(ctx, tx, c, flow, ix, seen, cn, &d)
		switch {
		case err == nil:
			out[cn.ID] = &domain.ImpactValidation{Valid: true, Modified: modified}
		case errors.Is(err, ErrInvalid) || errors.Is(err, ErrConflict):
			out[cn.ID] = &domain.ImpactValidation{Message: validationMessage(err), Modified: modified}
		default:
			slog.Debug("impact validation skipped", "change", c.ID, "impact", cn.ID, "err", err)
			complete = false
		}
	}
	if complete {
		g.validations.Put(key, validationEntry{seq: st.seq, out: out})
	}
	return out, nil
}

// attachValidation sets Validation on the impacts of the list (a copy the caller owns) that the flow validates; pick, when
// set, narrows the impacts to look at. It never fails: a view without a validation is still a view.
func (g *Graph) attachValidation(ctx context.Context, tx Tx, c domain.Change, flow string, impacts []domain.ChangeImpact, pick func(domain.ChangeImpact) bool) {
	vs, err := g.impactValidations(ctx, tx, c, flow)
	if err != nil {
		slog.Debug("impact validations skipped", "change", c.ID, "flow", flow, "err", err)
		return
	}
	for i, cn := range impacts {
		if pick != nil && !pick(cn) {
			continue
		}
		impacts[i].Validation = vs[cn.ID]
	}
}
