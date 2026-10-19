package domain

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestImpactPayloadLeavesTheEnvelopeToTheColumns(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	st := ChangeImpact{ID: "i1", Key: "K", Type: "alm@Requirement", Intent: IntentModified, Review: ReviewProposed}
	e := ImpactEvent{ID: "e1", Change: "c1", Impact: "i1", Op: ImpactProposed, Flow: "f1", Execution: "x1", By: "alice", At: at, Causation: "r1", State: &st}
	l, err := ImpactEntry(e)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"id"`, `"changeId"`, `"impactId"`, `"seq"`, `"flow"`, `"execution"`, `"by"`, `"at"`, `"causation"`} {
		if strings.Contains(string(l.Payload), k) {
			t.Errorf("payload holds %s: %s", k, l.Payload)
		}
	}
	l.Seq = 9
	got, err := ImpactEventOf(l)
	if err != nil {
		t.Fatal(err)
	}
	e.Seq = 9
	if !reflect.DeepEqual(got, e) {
		t.Fatalf("read back\n%+v\nwant\n%+v", got, e)
	}
}

func TestCheckoutOfAVersionStoresItsBase(t *testing.T) {
	base := Node{ID: "n1", Version: 3, Key: "K", Type: "alm@Requirement", State: "draft", Owner: "u1", Properties: map[string]any{"title": strings.Repeat("x", 500)}}
	links := []Link{{ID: "l1", From: base.Ref(), To: NodeRef{ID: "n2", Version: 1}, Type: "alm@refines"}, {ID: "l2", From: base.Ref(), To: NodeRef{ID: "n3", Version: 2}, Type: "alm@refines"}}
	cp := &BaseCopy{Drafted: []NodeRef{{ID: "n3", Version: 2}}}
	e := ImpactEvent{ID: "e1", Change: "c1", Impact: "i1", Op: ImpactCheckedOut, Execution: "x1", FromBase: cp}
	d := CopyDraft(e, base, links)
	ref := d.Ref()
	e.Draft, e.Post = &d, &ref
	l, err := ImpactEntry(e)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Payload) > 200 || strings.Contains(string(l.Payload), "title") {
		t.Fatalf("the payload copies the version: %s", l.Payload)
	}
	got, err := ImpactEventOf(l)
	if err != nil {
		t.Fatal(err)
	}
	if got.Hydrated() {
		t.Fatal("a copy read from the log is not hydrated before its version is read")
	}
	h := CopyDraft(got, base, links)
	if !reflect.DeepEqual(h, d) {
		raw, _ := json.Marshal(h)
		want, _ := json.Marshal(d)
		t.Fatalf("hydrated\n%s\nwant\n%s", raw, want)
	}
	if h.Links[1].To != DraftRef("n3") || h.Links[0].ID != BaseLinkID("e1", "l1") {
		t.Fatalf("links: %+v", h.Links)
	}
}
