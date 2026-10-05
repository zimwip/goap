package review

import (
	"testing"
	"time"

	"github.com/zimwip/goap/pkg/domain"
)

func item(id string, d map[string]any, mod ...func(*domain.ChangeItem)) domain.ChangeItem {
	it := domain.ChangeItem{ID: domain.ItemID(id), Kind: KindReview, Status: domain.ItemProposed, Data: d, ProducedBy: "alice"}
	for _, m := range mod {
		m(&it)
	}
	return it
}

func entries(es ...map[string]any) []any {
	out := make([]any, len(es))
	for i, e := range es {
		out[i] = e
	}
	return out
}

// Each version of an item is the whole record of its key: the review is the last one, in the order the keys appeared.
func TestReviewsFoldKeepsTheLastVersionOfEachKey(t *testing.T) {
	items := []domain.ChangeItem{
		item("1", map[string]any{"key": "R1", "status": Open, "comment": "first", "entries": entries()}),
		item("2", map[string]any{"key": "R2", "status": Open}),
		item("3", map[string]any{"key": "R1", "status": Open, "comment": "second", "entries": entries(map[string]any{"impact": "i1", "outcome": "accept", "comment": "c"})}),
		{ID: "x", Kind: domain.KindArtifact},
	}
	got := Reviews(items)
	if len(got) != 2 || got[0].Key != "R1" || got[1].Key != "R2" {
		t.Fatalf("order: %+v", got)
	}
	r := got[0]
	if r.Comment != "second" || r.Versions != 2 || r.Item != "3" || r.By != "alice" || len(r.Entries) != 1 || r.Entries[0] != (Entry{Impact: "i1", Comment: "c", Outcome: Accept}) {
		t.Fatalf("R1: %+v", r)
	}
}

// A submitted or discarded review is final: later versions of its key are ignored; so are rejected items.
func TestReviewsFinalStatusIsImmutable(t *testing.T) {
	sub := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).Format(time.RFC3339Nano)
	items := []domain.ChangeItem{
		item("1", map[string]any{"key": "R1", "status": Open}),
		item("2", map[string]any{"key": "R1", "status": Submitted, "submittedAt": sub, "entries": entries(map[string]any{"impact": "i", "outcome": "reject"})}),
		item("3", map[string]any{"key": "R1", "status": Open, "comment": "sneaky"}),
		item("4", map[string]any{"key": "R2", "status": Discarded}),
		item("5", map[string]any{"key": "R2", "status": Open}),
		item("6", map[string]any{"key": "R3", "status": Open}, func(i *domain.ChangeItem) { i.Status = domain.ItemRejected }),
	}
	got := Reviews(items)
	if len(got) != 2 || got[0].Status != Submitted || got[0].Comment != "" || !got[0].SubmittedAt.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) || got[1].Status != Discarded {
		t.Fatalf("%+v", got)
	}
	if got[0].Open() || got[0].Ready() {
		t.Fatal("a submitted review is neither open nor ready")
	}
}

func TestReviewReadyNeedsAnOutcomeOnEveryEntry(t *testing.T) {
	r := Review{Status: Open}
	if r.Ready() {
		t.Fatal("no entry")
	}
	r.Entries = []Entry{{Impact: "a", Outcome: Accept}, {Impact: "b"}}
	if r.Ready() {
		t.Fatal("an entry has no outcome")
	}
	r.Entries[1].Outcome = Reject
	if !r.Ready() {
		t.Fatal("every entry is decided")
	}
}

func TestEffectiveCommentKeepsBoth(t *testing.T) {
	for _, c := range []struct{ global, entry, want string }{
		{"", "", ""}, {"g", "", "g"}, {"", "e", "e"}, {" g ", " e ", "e\n\nReview: g"},
	} {
		if got := EffectiveComment(c.global, c.entry); got != c.want {
			t.Errorf("%q + %q = %q, want %q", c.global, c.entry, got, c.want)
		}
	}
}

func TestValidate(t *testing.T) {
	Register()
	ok := func(d map[string]any) error { return item("1", d).Validate() }
	e := func(imp, out string) map[string]any { return map[string]any{"impact": imp, "outcome": out} }
	for name, c := range map[string]struct {
		d  map[string]any
		ok bool
	}{
		"open, empty":            {map[string]any{"key": "R", "status": Open}, true},
		"no key":                 {map[string]any{"status": Open}, false},
		"unknown status":         {map[string]any{"key": "R", "status": "done"}, false},
		"undecided while open":   {map[string]any{"key": "R", "status": Open, "entries": entries(e("a", ""))}, true},
		"bad outcome":            {map[string]any{"key": "R", "status": Open, "entries": entries(e("a", "maybe"))}, false},
		"twice":                  {map[string]any{"key": "R", "status": Open, "entries": entries(e("a", ""), e("a", ""))}, false},
		"submitted no entry":     {map[string]any{"key": "R", "status": Submitted, "submittedAt": "x"}, false},
		"submitted undecided":    {map[string]any{"key": "R", "status": Submitted, "submittedAt": "x", "entries": entries(e("a", ""))}, false},
		"submitted no date":      {map[string]any{"key": "R", "status": Submitted, "entries": entries(e("a", "accept"))}, false},
		"submitted":              {map[string]any{"key": "R", "status": Submitted, "submittedAt": "x", "entries": entries(e("a", "accept"))}, true},
		"discarded with entries": {map[string]any{"key": "R", "status": Discarded, "entries": entries(e("a", ""))}, true},
	} {
		if err := ok(c.d); (err == nil) != c.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Awaiting lists the impacts the flow sees whose review is proposed and that no other open review of the flow holds.
func TestAwaiting(t *testing.T) {
	c := domain.Change{Nodes: []domain.ChangeImpact{
		{ID: "a", Review: domain.ReviewProposed},
		{ID: "b", Review: domain.ReviewAccepted},
		{ID: "c", Review: domain.ReviewProposed},
		{ID: "d", Review: domain.ReviewProposed, Superseded: true},
		{ID: "e", Review: domain.ReviewProposed, Flow: "f1"},
	}}
	ids := func(l []domain.ChangeImpact) (out []string) {
		for _, cn := range l {
			out = append(out, string(cn.ID))
		}
		return
	}
	rs := []Review{{Key: "R1", Status: Open, Entries: []Entry{{Impact: "c"}}}, {Key: "R2", Status: Submitted, Entries: []Entry{{Impact: "a"}}}}
	// the main flow does not see the impact of flow f1; c is held by the open review R1
	if got := ids(Awaiting(c, "", rs, "")); len(got) != 1 || got[0] != "a" {
		t.Fatalf("awaiting = %v", got)
	}
	if got := ids(Awaiting(c, "", rs, "R1")); len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Fatalf("the review being edited does not hold its own: %v", got)
	}
}
