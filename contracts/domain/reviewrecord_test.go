package domain

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestReviewDeltaDefaults: what most entries of a delta set alike is written once, as its defaults, an entry its impact
// alone or what differs, null for what it leaves unset; read back, the entries are as given (ADR 0180).
func TestReviewDeltaDefaults(t *testing.T) {
	s := func(v string) *string { return &v }
	in := ReviewDelta{Key: "R1", Status: ReviewStatusSubmitted, Entries: []ReviewEntryEdit{
		{Impact: "i1", Outcome: s("accept"), Comment: s("ok")},
		{Impact: "i2", Outcome: s("accept"), Comment: s("ok")},
		{Impact: "i3", Outcome: s("reject"), Comment: s("no")},
		{Impact: "i4", Comment: s("ok")},
	}}
	d := in.Data()
	raw, _ := json.Marshal(d)
	if got := string(raw); got != `{"defaults":{"comment":"ok","outcome":"accept"},"entries":["i1","i2",{"comment":"no","impact":"i3","outcome":"reject"},{"impact":"i4","outcome":null}],"key":"R1","status":"submitted"}` {
		t.Fatalf("delta written: %s", got)
	}
	var back map[string]any
	_ = json.Unmarshal(raw, &back)
	if got := ReviewDeltaOf(back); !reflect.DeepEqual(got, in) {
		t.Fatalf("delta read back: %+v", got)
	}
	one := ReviewDelta{Key: "R2", Entries: []ReviewEntryEdit{{Impact: "i1", Outcome: s("accept")}}}
	if _, ok := one.Data()["defaults"]; ok {
		t.Fatal("one entry has no defaults")
	}
}
