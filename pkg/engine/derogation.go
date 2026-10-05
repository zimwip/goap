package engine

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/risk"
	"github.com/zimwip/goap/pkg/verify"
)

// ExpireDerogations applies the expiry of the derogations of a change (ADR 0075 §2): for each one open and run out at
// now (risk.Expired) it writes a closing version, sends the change impacts it covered back to proposed (a review event,
// graph.ReopenImpacts) and starts their verification over (a produced entry), so what was accepted on a gap is reviewed
// again. It is a Change operation like any other, done by the platform (the principal system:derogation) when a
// scheduler trigger fires the builtin derogation.expire; the graph reads no clock, now is the caller's. It returns the
// keys of the derogations it closed.
func ExpireDerogations(ctx context.Context, g GraphPort, id domain.ChangeID, now time.Time) ([]string, error) {
	ctx = authz.With(ctx, authz.System("derogation"))
	bb, err := g.BlackboardIn(ctx, id, "")
	if err != nil {
		return nil, err
	}
	expired := risk.Expired(bb.Change, now)
	if len(expired) == 0 {
		return nil, nil
	}
	var facts []domain.ChangeItem
	for _, it := range bb.Change.Items {
		if it.Kind == verify.KindVerification && bb.Change.Active(it.ID) {
			facts = append(facts, it)
		}
	}
	subjects := verify.Subjects(facts)
	var closed []string
	for _, d := range expired {
		if _, err := g.AddItems(ctx, id, []domain.ChangeItem{{ID: domain.ItemID(uuid.NewString()), Kind: risk.KindDerogation, Type: "derogation",
			Status: domain.ItemProposed, Data: d.ClosingData("expired"), ProducedBy: "derogation.expire"}}); err != nil {
			return closed, fmt.Errorf("close derogation %s: %w", d.Key, err)
		}
		closed = append(closed, d.Key)
		// what it covered: the effects accepted with a reserve on it, and the change impacts it targets
		var impacts []domain.ChangeImpactID
		var reset []verify.Subject
		for _, s := range subjects {
			if s.State == verify.AcceptedWithReserve && s.Derogation == d.Key && s.Impact != "" {
				impacts = append(impacts, domain.ChangeImpactID(s.Impact))
				reset = append(reset, s)
			}
		}
		for _, cn := range bb.Change.Nodes {
			if (string(cn.ID) == d.Target || cn.Key == d.Target) && !slices.Contains(impacts, cn.ID) {
				impacts = append(impacts, cn.ID)
			}
		}
		if len(impacts) == 0 {
			continue
		}
		if _, err := g.ReopenImpacts(ctx, id, impacts, fmt.Sprintf("derogation %s expired (%s): to be reviewed again", d.Key, d.Expires.Format(time.RFC3339))); err != nil {
			return closed, fmt.Errorf("reopen the effects of derogation %s: %w", d.Key, err)
		}
		for _, s := range reset {
			data := map[string]any{verify.KeyState: verify.Produced, verify.KeyAction: s.Action, verify.KeyImpacts: []string{s.Impact},
				verify.KeyOracle: s.Oracle, verify.KeyIndependent: s.Independent}
			if s.Model != "" {
				data[verify.KeyModel] = s.Model
			}
			// the producer stays the original one: the independence of the next verification does not move
			it := domain.ChangeItem{ID: domain.ItemID(uuid.NewString()), Kind: verify.KindVerification, Type: verify.Produced, Data: data,
				ProducedBy: s.Producer, Execution: s.Execution}
			if _, err := g.AddItems(ctx, id, []domain.ChangeItem{it}); err != nil {
				return closed, fmt.Errorf("restart the verification of %s: %w", s.Impact, err)
			}
		}
	}
	return closed, nil
}

// expireAllDerogations is the builtin derogation.expire: it applies the expiry to every live change that holds an
// open derogation.
func expireAllDerogations(ctx context.Context, ac ActionContext) (ActionResult, error) {
	changes, err := ac.Graph.ListChanges(ctx, graph.ChangesFilter{Status: []domain.ChangeStatus{domain.ChangeActive}})
	if err != nil {
		return ActionResult{}, err
	}
	now := time.Now().UTC()
	var closed []string
	for _, c := range changes {
		if len(risk.Expired(c, now)) == 0 {
			continue
		}
		keys, err := ExpireDerogations(ctx, ac.Graph, c.ID, now)
		for _, k := range keys {
			closed = append(closed, string(c.ID)+"/"+k)
		}
		if err != nil {
			return ActionResult{Output: fmt.Sprintf("%d derogation(s) closed", len(closed))}, err
		}
	}
	return ActionResult{Output: fmt.Sprintf("%d derogation(s) closed", len(closed)), VarsSet: map[string]any{"swept": true}}, nil
}
