package domain

import (
	"reflect"
	"testing"
)

func TestMergeDrafts(t *testing.T) {
	to := func(id NodeID, v Version) NodeRef { return NodeRef{ID: id, Version: v} }
	base := Draft{Key: "K", State: "draft", Owner: "U1",
		Properties: map[string]any{"title": "t", "size": float64(1), "gone": "x", "both": "b"},
		Links:      []DraftLink{{ID: "l1", Type: "uses", To: to("A", 1)}, {ID: "l2", Type: "uses", To: to("B", 1)}, {ID: "l3", Type: "uses", To: to("C", 1)}}}
	ours := base.Clone()
	ours.Properties["title"] = "ours" // ours only
	ours.Properties["both"] = "o"     // both, differently: conflict
	ours.State = "review"             // both, differently: conflict
	ours.Links[1].To = to("B", 2)     // both, differently: conflict
	ours.Links = append(ours.Links, DraftLink{ID: "l4", Type: "uses", To: to("D", 1)})
	theirs := base.Clone()
	theirs.Properties["size"] = 2 // theirs only (an int: compared as JSON)
	delete(theirs.Properties, "gone")
	theirs.Properties["both"] = "t"
	theirs.Properties["new"] = "n"
	theirs.State = "done"
	theirs.Owner = "U2"
	theirs.Links = []DraftLink{{ID: "x1", Type: "uses", To: to("A", 3)}, {ID: "x2", Type: "uses", To: to("B", 3)}, {ID: "x5", Type: "uses", To: to("E", 1)}} // C removed, A moved, E added

	got, conflicts := MergeDrafts(base, ours, theirs)
	if want := []string{ConflictLink("uses", "B"), ConflictProp("both"), ConflictState}; !reflect.DeepEqual(conflicts, want) {
		t.Fatalf("conflicts = %v, want %v", conflicts, want)
	}
	wantProps := map[string]any{"title": "ours", "size": 2, "both": "o", "new": "n"}
	if !reflect.DeepEqual(got.Properties, wantProps) {
		t.Fatalf("props = %v", got.Properties)
	}
	if got.State != "review" || got.Owner != "U2" {
		t.Fatalf("state %q owner %q", got.State, got.Owner)
	}
	wantLinks := []DraftLink{{ID: "l1", Type: "uses", To: to("A", 3)}, {ID: "l2", Type: "uses", To: to("B", 2)}, {ID: "l4", Type: "uses", To: to("D", 1)}, {Type: "uses", To: to("E", 1)}}
	if !reflect.DeepEqual(got.Links, wantLinks) {
		t.Fatalf("links = %+v", got.Links)
	}
	if _, c := MergeDrafts(base, base, base); len(c) != 0 {
		t.Fatalf("no change, no conflict: %v", c)
	}
}

func TestPendingConflicts(t *testing.T) {
	ev := func(op ImpactOp, patch map[string]any) ImpactEvent {
		return ImpactEvent{Impact: "i", Op: op, Patch: patch}
	}
	rebased := ev(ImpactTransitioned, map[string]any{"rebased": map[string]any{"change": "p", "seq": 3},
		"conflicts": []any{ConflictProp("a"), ConflictProp("b"), ConflictOwner, ConflictState, ConflictLink("uses", "N"), ConflictNode}})
	log := []ImpactEvent{
		rebased,
		ev(ImpactUpdated, map[string]any{"props": map[string]any{"a": 1}}),
		ev(ImpactUpdated, map[string]any{"ownerId": "U"}),
		ev(ImpactTransitioned, map[string]any{"state": map[string]any{"from": "x", "to": "y"}}),
		ev(ImpactUpdated, map[string]any{"removeLink": map[string]any{"id": "l", "type": "uses", "toId": "N"}}),
		{Impact: "other", Op: ImpactUpdated, Patch: map[string]any{"resolved": true}},
	}
	if got := PendingConflicts(log, "i"); !reflect.DeepEqual(got, []string{ConflictProp("b")}) {
		t.Fatalf("pending = %v", got)
	}
	if got := PendingConflicts(append(log, ev(ImpactUpdated, map[string]any{"resolved": true})), "i"); len(got) != 0 {
		t.Fatalf("resolved = %v", got)
	}
	if got := PendingConflicts(append(log, rebased), "i"); len(got) != 6 {
		t.Fatalf("a new rebase names its conflicts again: %v", got)
	}
}
