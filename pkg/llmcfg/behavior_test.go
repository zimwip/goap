package llmcfg

import (
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

func TestBehaviorRoundTripAndValidation(t *testing.T) {
	b := Behavior{Name: "terse", Instruction: "Be brief.", Enabled: true, Position: PositionPrepend, Order: 3, Aliases: []string{"fast"}, Models: []string{"a/b"}, Sources: []string{"engine"}, Kinds: []string{KindComplete}, AppliesToJSON: true}
	got, err := BehaviorFromProps(b.Props())
	if err != nil || got.Name != b.Name || got.Order != 3 || got.Position != PositionPrepend || !got.AppliesToJSON || len(got.Aliases) != 1 || got.Models[0] != "a/b" {
		t.Fatalf("%+v %v", got, err)
	}
	bad := map[string]Behavior{
		"name":        {Name: "Bad Name", Instruction: "x"},
		"empty":       {Name: "x", Instruction: "  "},
		"too long":    {Name: "x", Instruction: strings.Repeat("a", MaxInstruction+1)},
		"position":    {Name: "x", Instruction: "x", Position: "middle"},
		"alias":       {Name: "x", Instruction: "x", Aliases: []string{"A B"}},
		"model":       {Name: "x", Instruction: "x", Models: []string{"nomodel"}},
		"source":      {Name: "x", Instruction: "x", Sources: []string{"nobody"}},
		"embed kinds": {Name: "x", Instruction: "x", Kinds: []string{"embed"}},
	}
	for what, v := range bad {
		if v.Validate() == nil {
			t.Errorf("%s: accepted", what)
		}
	}
}

func TestSnapshotSplitsBehaviors(t *testing.T) {
	on := Behavior{Name: "on", Instruction: "ON", Enabled: true, Order: 2}
	off := Behavior{Name: "off", Instruction: "OFF"}
	retired := Behavior{Name: "gone", Instruction: "GONE", Enabled: true}
	broken := Behavior{Name: "broken", Enabled: true}
	gone := node(BehaviorKey("gone"), NodeTypeBehavior, retired.Props())
	gone.State = StateRetired
	s := BuildSnapshot("b", []domain.Node{
		node(BehaviorKey("on"), NodeTypeBehavior, on.Props()), node(BehaviorKey("off"), NodeTypeBehavior, off.Props()), gone,
		node(BehaviorKey("broken"), NodeTypeBehavior, broken.Props()),
	}, nil)
	if len(s.Behaviors) != 1 || s.Behaviors[0].Name != "on" || len(s.Off) != 1 || s.Off[0].Name != "off" {
		t.Fatalf("%+v %+v", s.Behaviors, s.Off)
	}
	if len(s.Problems) != 1 || !strings.Contains(s.Problems[0], "LLB:broken") {
		t.Fatalf("problems: %v", s.Problems)
	}
}

func TestApply(t *testing.T) {
	s := &Snapshot{Behaviors: []Behavior{
		{Name: "b", Instruction: "B", Enabled: true, Order: 1},
		{Name: "a", Instruction: "A", Enabled: true, Order: 1, Position: PositionPrepend},
		{Name: "z", Instruction: "Z", Enabled: true, Order: 0, Sources: []string{"helper"}},
	}}
	call := CallInfo{Alias: "default", Provider: "p", Model: "m", Source: "engine", Kind: KindComplete}
	if got := s.Apply("S", call); got.System != "A\n\nS\n\nB" || strings.Join(got.Names, ",") != "a,b" {
		t.Fatalf("%+v", got)
	}
	if got := s.Apply("", call); got.System != "A\n\nB" {
		t.Fatalf("no system text: %q", got.System)
	}
	call.Kind = "embed"
	if got := s.Apply("S", call); got.System != "S" || len(got.Names) != 0 {
		t.Fatalf("embed: %+v", got)
	}
	var none *Snapshot
	if got := none.Apply("S", CallInfo{Kind: KindComplete}); got.System != "S" {
		t.Fatalf("%+v", got)
	}
}

func TestTerseBehaviorIsValidAndOn(t *testing.T) {
	b := TerseBehavior()
	if err := b.Validate(); err != nil || !b.Enabled {
		t.Fatalf("%v %+v", err, b)
	}
}
