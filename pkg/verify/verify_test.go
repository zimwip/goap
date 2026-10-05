package verify

import (
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

func item(id string, execution, by string, data map[string]any) domain.ChangeItem {
	return domain.ChangeItem{ID: domain.ItemID(id), Kind: KindVerification, Execution: execution, ProducedBy: by, Data: data}
}

func produced(id, execution, by string, independent bool, impacts ...string) domain.ChangeItem {
	return item(id, execution, by, map[string]any{KeyState: Produced, KeyAction: by, KeyOracle: "human", KeyIndependent: independent, KeyImpacts: impacts})
}

func judged(id, state, impact, by string) domain.ChangeItem {
	return item(id, "e2", by, map[string]any{KeyState: state, KeyAction: by, KeyImpact: impact, KeyBy: by})
}

func TestValidate(t *testing.T) {
	for name, tc := range map[string]struct {
		data map[string]any
		msg  string
	}{
		"state":  {map[string]any{KeyState: "done", KeyAction: "a"}, "unknown state"},
		"action": {map[string]any{KeyState: Produced}, "data.action"},
		"impact": {map[string]any{KeyState: Accepted, KeyAction: "a"}, "data.impact"},
		"ok":     {map[string]any{KeyState: Rejected, KeyAction: "a", KeyImpact: "i1"}, ""},
	} {
		err := validate(item("1", "e", "a", tc.data))
		if (tc.msg == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.msg)) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The fold is pure: one subject per effect, moved by the later entries; a produced entry again starts over.
func TestSubjects(t *testing.T) {
	items := []domain.ChangeItem{
		produced("1", "e1", "write", true, "i1", "i2"),
		judged("2", Verified, "i1", "check"),
		judged("3", Accepted, "i1", "check"),
		judged("4", Rejected, "i2", "check"),
		{ID: "x", Kind: domain.KindArtifact},
	}
	got := Subjects(items)
	if len(got) != 2 || got[0].Impact != "i1" || got[0].State != Accepted || got[0].By != "check" || got[0].Producer != "write" || !got[0].Independent ||
		got[1].State != Rejected || got[1].Open() || got[0].Open() {
		t.Fatalf("%+v", got)
	}
	again := Subjects(append(items, produced("5", "e3", "write", true, "i2")))
	if len(again) != 2 || again[1].State != Produced || !again[1].Open() || again[1].Execution != "e3" {
		t.Fatalf("a rerun starts over: %+v", again)
	}
	if s := Subjects(items[:1]); len(s) != 2 || !s[0].Open() {
		t.Fatalf("produced only: %+v", s)
	}
	if s := Subjects([]domain.ChangeItem{produced("1", "e1", "a", true)}); len(s) != 1 || s[0].Impact != "" {
		t.Fatalf("a run with no impact: %+v", s)
	}
	if s := Subjects([]domain.ChangeItem{judged("1", Accepted, "i9", "b")}); len(s) != 0 {
		t.Fatalf("a verdict with no produced entry: %+v", s)
	}
}

// The verifier is not the producer when the effect declared an independent verification, and only then.
func TestPolicy(t *testing.T) {
	imp := domain.ChangeImpact{ID: "i1", Key: "REQ-1", ProducedBy: "write", Execution: "e1"}
	req := func(reviewer string, items ...domain.ChangeItem) domain.ReviewRequest {
		return domain.ReviewRequest{Impact: imp, Reviewer: reviewer, Status: domain.ReviewAccepted, Items: items}
	}
	p := Policy{}
	if err := p.Review(req("write", produced("1", "e1", "write", true, "i1"))); err == nil || !strings.Contains(err.Error(), "may not verify") {
		t.Fatalf("producer == reviewer: %v", err)
	}
	if err := p.Review(req("check", produced("1", "e1", "write", true, "i1"))); err != nil {
		t.Fatalf("another reviewer: %v", err)
	}
	if err := p.Review(req("write", produced("1", "e1", "write", false, "i1"))); err != nil {
		t.Fatalf("not independent: %v", err)
	}
	if err := p.Review(req("write")); err != nil {
		t.Fatalf("no verification declared: %v", err)
	}
	// the producer is the one who wrote the effect, even when another declared the impact
	if err := p.Review(req("rewrite", produced("1", "e2", "rewrite", true, "i1"))); err == nil {
		t.Fatalf("the writer may not verify")
	}
	// a model alias recorded on the produced entry
	m := produced("1", "e1", "write", true, "i1")
	m.Data[KeyModel] = "gpt-a"
	if err := p.Review(req("gpt-a", m)); err == nil {
		t.Fatal("the model alias of the producer")
	}
}

// accepted_with_reserve stands on a derogation: the entry names it, and the subject keeps it until the next verdict.
func TestAcceptedWithReserveNamesItsDerogation(t *testing.T) {
	Register()
	it := judged("2", AcceptedWithReserve, "i1", "carol")
	if err := it.Validate(); err == nil || !strings.Contains(err.Error(), "data.derogation") {
		t.Fatalf("a reserve without a derogation: %v", err)
	}
	it.Data[KeyDerogation] = "DRG-1"
	if err := it.Validate(); err != nil {
		t.Fatal(err)
	}
	items := []domain.ChangeItem{produced("1", "e1", "bob", true, "i1"), it}
	if s := Subjects(items); len(s) != 1 || s[0].State != AcceptedWithReserve || s[0].Derogation != "DRG-1" {
		t.Fatalf("reserve: %+v", s)
	}
	// produced again (the derogation expired): starts over, with no derogation
	items = append(items, produced("3", "e1", "bob", true, "i1"))
	if s := Subjects(items); len(s) != 1 || s[0].State != Produced || s[0].Derogation != "" {
		t.Fatalf("starts over: %+v", s)
	}
}
