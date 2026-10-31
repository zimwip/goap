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
	base := Node{ID: "n1", Version: 3, Key: "K", Type: "alm@Requirement", State: "draft", Responsible: unitURL("u1"), Properties: map[string]any{"title": strings.Repeat("x", 500)}}
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

func TestAnEntryCarriesTheEventsOfOneOperation(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var events []ImpactEvent
	for i, id := range []ChangeImpactID{"i1", "i2", "i3"} {
		r := Review{Status: ReviewAccepted, By: "bob", Comment: "ok " + string(id), At: at, ReviewID: "REV-1"}
		events = append(events, ImpactEvent{ID: PartID("e1", i), Change: "c1", Impact: id, Op: ImpactReviewed, By: "bob", At: at, Causation: "r1", Review: &r})
	}
	l, err := ImpactBatchEntry("e1", events)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(l.Payload), "REV-1") != 1 {
		t.Errorf("what the events share is written once: %s", l.Payload)
	}
	l.Seq = 4
	got, err := ImpactEventsOf(l)
	if err != nil {
		t.Fatal(err)
	}
	for i := range events {
		events[i].Seq = 4
	}
	if !reflect.DeepEqual(got, events) {
		t.Fatalf("read back\n%+v\nwant\n%+v", got, events)
	}
	if _, err := ImpactBatchEntry("e2", []ImpactEvent{events[0], {Change: "c1", Impact: "i4", Op: ImpactCommitted}}); err == nil {
		t.Fatal("events of two operations share an entry")
	}
	one, err := ImpactEventsOf(LogEntry{ID: "e3", Change: "c1", Type: "impact.withdrawn", Subject: "i9", Payload: []byte(`{"op":"withdrawn"}`)})
	if err != nil || len(one) != 1 || one[0].Impact != "i9" {
		t.Fatalf("an entry of one event: %+v, %v", one, err)
	}
}

// An event installing a draft leaves out what its draft says (its post, a creation's key and type) and writes what
// differs, empty or none included: the log rebuilds it as it was.
func TestImpactPayloadLeavesOutWhatTheDraftSays(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	draft := func(key, typ string) *Draft {
		return &Draft{Node: "n1", Key: key, Type: typ, Properties: map[string]any{"title": "T"}}
	}
	ref := func(id NodeID, v Version) *NodeRef { return &NodeRef{ID: id, Version: v} }
	created := func(post *NodeRef, stKey, stType string) ImpactEvent {
		st := ChangeImpact{ID: "i1", Key: stKey, Type: stType, Intent: IntentCreated, Review: ReviewProposed}
		return ImpactEvent{ID: "e1", Change: "c1", Impact: "i1", Op: ImpactCreated, By: "alice", At: at, State: &st, Post: post, Draft: draft("K", "alm@Requirement")}
	}
	base := &BaseCopy{}
	checkedOut := func(post *NodeRef) ImpactEvent {
		return ImpactEvent{ID: "e2", Change: "c1", Impact: "i1", Op: ImpactCheckedOut, At: at, Post: post, FromBase: base,
			Draft: &Draft{Base: ref("n2", 3)}}
	}
	for name, c := range map[string]struct {
		e    ImpactEvent
		left []string
	}{
		"created":                 {created(ref("n1", 0), "K", "alm@Requirement"), []string{`"post"`, `"state":{"key"`}},
		"created, other key":      {created(ref("n1", 0), "", "alm@Other"), nil},
		"created, no post":        {created(nil, "K", "alm@Requirement"), nil},
		"created, a version post": {created(ref("n1", 2), "K", "alm@Requirement"), nil},
		"checked out":             {checkedOut(ref("n2", 0)), []string{`"post"`, `"key"`, `"node"`}},
		"checked out, no post":    {checkedOut(nil), nil},
	} {
		l, err := ImpactEntry(c.e)
		if err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(string(l.Payload), `"alm@Requirement"`); n > 1 {
			t.Errorf("%s: the payload holds the type %d times: %s", name, n, l.Payload)
		}
		for _, k := range c.left {
			if strings.Contains(string(l.Payload), k) {
				t.Errorf("%s: the payload holds %s: %s", name, k, l.Payload)
			}
		}
		got, err := ImpactEventOf(l)
		if err != nil {
			t.Fatal(err)
		}
		want := c.e
		if want.Draft != nil && want.FromBase == nil {
			d := *want.Draft
			d.Change, d.Impact = want.Change, want.Impact
			want.Draft = &d
		}
		if want.FromBase != nil {
			got.Draft.Node = "" // ImpactEventOf names the node of a copy before it is hydrated
			d := *want.Draft
			d.Change, d.Impact = want.Change, want.Impact
			want.Draft = &d
		}
		if !reflect.DeepEqual(got, want) {
			g, _ := json.Marshal(got)
			w, _ := json.Marshal(want)
			t.Errorf("%s: read back\n%s\nwant\n%s\npayload %s", name, g, w, l.Payload)
		}
	}
}
