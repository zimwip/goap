package risk

import (
	"testing"
	"time"

	"github.com/zimwip/goap/pkg/domain"
)

func derogation(extra map[string]any) domain.ChangeItem {
	d := map[string]any{"key": "DRG-1", "rule": "tests pass", "target": "REQ-1", "reason": "deadline", "signatory": "alice",
		"expires": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
	for k, v := range extra {
		if v == nil {
			delete(d, k)
		} else {
			d[k] = v
		}
	}
	return domain.ChangeItem{Kind: KindDerogation, Data: d}
}

// No reason, rule, target, signatory or expiry, no derogation; an open one cannot expire in the past (ADR 0075 §2).
func TestDerogationValidation(t *testing.T) {
	Register()
	if err := derogation(nil).Validate(); err != nil {
		t.Fatalf("valid: %v", err)
	}
	for _, f := range []string{"key", "rule", "target", "reason", "signatory", "expires"} {
		if err := derogation(map[string]any{f: nil}).Validate(); err == nil {
			t.Errorf("a derogation without %s is accepted", f)
		}
	}
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	for name, extra := range map[string]map[string]any{
		"past": {"expires": past}, "unparsable": {"expires": "soon"}, "status": {"status": "paused"}} {
		if err := derogation(extra).Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// a closing version may carry the expiry that passed; a date is the end of that day
	if err := derogation(map[string]any{"expires": past, "status": "closed"}).Validate(); err != nil {
		t.Errorf("closing version: %v", err)
	}
	if err := derogation(map[string]any{"expires": time.Now().Add(48 * time.Hour).Format(time.DateOnly)}).Validate(); err != nil {
		t.Errorf("a date: %v", err)
	}
	if p, ok := domain.ItemPermissionOf(KindDerogation); !ok || p.Permission != PermissionSign || p.SubjectField != "signatory" {
		t.Errorf("permission: %+v %v", p, ok)
	}
}

func TestDerogationsFoldExpiredAndCovering(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
	item := func(id string, data map[string]any) domain.ChangeItem {
		return domain.ChangeItem{ID: domain.ItemID(id), Kind: KindDerogation, Data: data, ProducedBy: "p"}
	}
	base := func(key, target, exp string) map[string]any {
		return map[string]any{"key": key, "rule": "r", "target": target, "reason": "why", "signatory": "alice", "expires": exp}
	}
	c := domain.Change{Items: []domain.ChangeItem{
		item("1", base("DRG-1", "REQ-1", "2030-01-01T11:00:00Z")),     // ran out
		item("2", base("DRG-2", "REQ-2", "2030-01-01T13:00:00Z")),     // in force
		item("3", base("DRG-3", "REQ-3", "2030-01-02")),               // in force (a date)
		item("4", base("DRG-3", "REQ-3", "2030-01-02")),               // new version
		item("5", map[string]any{"key": "DRG-2", "status": "closed"}), // closes DRG-2, keeps the rest
	}}
	ds := Derogations(c)
	if len(ds) != 3 || ds[2].Versions != 2 || ds[1].Status != DerogationClosed || ds[0].Status != DerogationOpen {
		t.Fatalf("fold: %+v", ds)
	}
	ex := Expired(c, now)
	if len(ex) != 1 || ex[0].Key != "DRG-1" {
		t.Fatalf("expired: %+v", ex)
	}
	if got := Expired(c, now.Add(-2*time.Hour)); len(got) != 0 {
		t.Fatalf("not yet: %+v", got)
	}
	if d, ok := Covering(c, now, "REQ-3"); !ok || d.Key != "DRG-3" {
		t.Fatalf("covering: %+v %v", d, ok)
	}
	for _, target := range []string{"REQ-1", "REQ-2", "REQ-9"} { // ran out, closed, none
		if _, ok := Covering(c, now, target); ok {
			t.Errorf("%s is covered", target)
		}
	}
	// the closing version a platform writes is valid
	Register()
	closing := domain.ChangeItem{Kind: KindDerogation, Data: ex[0].ClosingData("expired")}
	if err := closing.Validate(); err != nil {
		t.Fatalf("closing: %v", err)
	}
}
