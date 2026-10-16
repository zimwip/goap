package domain

import (
	"reflect"
	"testing"
)

func TestDiffShapes(t *testing.T) {
	base := NodeShape{Key: "REQ-1", Type: "Requirement", State: "draft", Owner: "ORG-A",
		Properties: map[string]any{"title": "Use PSP", "n": float64(1), "tags": []any{"a"}},
		Links:      []LinkShape{{Type: "satisfies", To: "n1", ToKey: "NEED-1"}}}

	if d := DiffShapes(base, base); len(d) != 0 {
		t.Fatalf("identical shapes: %+v", d)
	}
	// a number as another Go type, a link order, an empty and a nil property map are not differences
	same := base
	same.Properties = map[string]any{"title": "Use PSP", "n": 1, "tags": []any{"a"}}
	same.Links = []LinkShape{{Type: "satisfies", To: "n1", ToKey: "NEED-1", Properties: map[string]any{}}}
	if d := DiffShapes(base, same); len(d) != 0 {
		t.Fatalf("equivalent shapes: %+v", d)
	}

	right := NodeShape{Key: "REQ-1", Type: "Requirement", State: "approved", Owner: "ORG-B",
		Properties: map[string]any{"title": "Use Stripe", "n": float64(1), "extra": "x"},
		Links: []LinkShape{
			{Type: "satisfies", To: "n1", ToKey: "NEED-1", Properties: map[string]any{"w": 2}},
			{Type: "refines", To: "n2", ToKey: "NEED-2"},
		}}
	got := DiffShapes(base, right)
	want := []FieldChange{
		{Kind: FieldState, Op: OpChanged, Old: "draft", New: "approved"},
		{Kind: FieldOwner, Op: OpChanged, Old: "ORG-A", New: "ORG-B"},
		{Kind: FieldProperty, Name: "extra", Op: OpAdded, New: "x"},
		{Kind: FieldProperty, Name: "tags", Op: OpRemoved, Old: []any{"a"}},
		{Kind: FieldProperty, Name: "title", Op: OpChanged, Old: "Use PSP", New: "Use Stripe"},
		{Kind: FieldLink, Name: "refines", Op: OpAdded, Target: "NEED-2"},
		{Kind: FieldLink, Name: "satisfies", Op: OpChanged, Target: "NEED-1", New: map[string]any{"w": 2}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("diff:\n got %+v\nwant %+v", got, want)
	}

	// the other way round: what was added is removed
	back := DiffShapes(right, base)
	if len(back) != len(want) || back[2].Op != OpRemoved || back[3].Op != OpAdded || back[5].Op != OpRemoved {
		t.Fatalf("reverse diff: %+v", back)
	}

	// a state or an owner set from nothing, a link removed
	got = DiffShapes(NodeShape{Links: base.Links}, NodeShape{State: "draft", Owner: "ORG-A"})
	if len(got) != 3 || got[0].Op != OpAdded || got[1].Op != OpAdded || got[2].Op != OpRemoved || got[2].Target != "NEED-1" {
		t.Fatalf("set from nothing: %+v", got)
	}
}

func TestFlowDiffCounts(t *testing.T) {
	d := FlowDiff{Impacts: []ImpactDiff{{Category: DiffAdded}, {Category: DiffAdded}, {Category: DiffModified}}}
	if c := d.DiffCounts(); c[DiffAdded] != 2 || c[DiffRemoved] != 0 || c[DiffModified] != 1 {
		t.Fatalf("counts: %v", c)
	}
}
