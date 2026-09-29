package engine

import (
	"fmt"
	"strings"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/dsl"
)

// ItemInput is the external (LLM / human) representation of an item of the
// change: a fact (artifact, decision) or, with kind "changeImpact", an operation
// on a change impact. Items of the same batch are designated by "#<ref>" and
// existing items by "@<itemId>".
type ItemInput struct {
	Ref string `json:"ref,omitempty"`
	// Kind is artifact or decision; "changeImpact" carries an operation on a change impact (ChangeImpact:
	// declare, write or review, ADR 0024), applied in order with the ones of the batch.
	Kind        string         `json:"kind"`
	Type        string         `json:"type,omitempty"`
	Decision    *DecisionInput `json:"decision,omitempty"`
	Data        map[string]any `json:"data,omitempty"`
	DerivedFrom []string       `json:"derivedFrom,omitempty"`
	// ChangeImpact is the operation of a "changeImpact" item.
	ChangeImpact *dsl.NodeOp `json:"changeImpact,omitempty"`
	// DecisionPoint is the operation of a "decisionPoint" item (ADR 0009 §4).
	DecisionPoint *DecisionOp `json:"decisionPoint,omitempty"`
}

// DecisionInput is the external representation of a decision.
type DecisionInput struct {
	Item    string `json:"item"`
	Accept  bool   `json:"accept"`
	Comment string `json:"comment,omitempty"`
}

// resolver turns ItemInputs into domain items.
type resolver struct {
	items map[domain.ItemID]bool
	local map[string]domain.ItemID
	newID func() string
}

func newResolver(change domain.Change, newID func() string) *resolver {
	r := &resolver{items: map[domain.ItemID]bool{}, local: map[string]domain.ItemID{}, newID: newID}
	for _, it := range change.Items {
		r.items[it.ID] = true
	}
	return r
}

func (r *resolver) item(s string) (domain.ItemID, error) {
	switch {
	case strings.HasPrefix(s, "#"):
		if id, ok := r.local[s[1:]]; ok {
			return id, nil
		}
		return "", fmt.Errorf("unknown local item %q", s)
	case strings.HasPrefix(s, "@"):
		if id := domain.ItemID(s[1:]); r.items[id] {
			return id, nil
		}
		return "", fmt.Errorf("unknown item %q", s)
	}
	if r.items[domain.ItemID(s)] {
		return domain.ItemID(s), nil
	}
	if id, ok := r.local[s]; ok {
		return id, nil
	}
	return "", fmt.Errorf("unknown item %q", s)
}

// resolve converts a batch. Item ids are assigned here so that "#ref"
// references inside the batch can be resolved.
func (r *resolver) resolve(in []ItemInput, producedBy string) ([]domain.ChangeItem, error) {
	for i, it := range in {
		id := domain.ItemID(r.newID())
		if it.Ref != "" {
			r.local[it.Ref] = id
		}
		r.local[fmt.Sprintf("%d", i)] = id
	}
	out := make([]domain.ChangeItem, 0, len(in))
	for i, it := range in {
		item := domain.ChangeItem{ID: r.local[fmt.Sprintf("%d", i)], Kind: domain.ItemKind(it.Kind), Type: it.Type, Data: it.Data, ProducedBy: producedBy}
		for _, d := range it.DerivedFrom {
			id, err := r.item(d)
			if err != nil {
				return nil, fmt.Errorf("item %d: %w", i, err)
			}
			item.DerivedFrom = append(item.DerivedFrom, id)
		}
		if d := it.Decision; d != nil {
			id, err := r.item(d.Item)
			if err != nil {
				return nil, fmt.Errorf("item %d: %w", i, err)
			}
			item.Decision = &domain.Decision{Item: id, Accept: d.Accept, Comment: d.Comment}
		}
		if err := item.Validate(); err != nil {
			return nil, fmt.Errorf("item %d: %w", i, err)
		}
		out = append(out, item)
	}
	return out, nil
}
