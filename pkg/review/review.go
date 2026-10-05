// Package review is the review object of a change (ADR 0080): a reviewer builds a review up, with a global comment and
// one entry per change impact (its own comment and outcome), then submits it; the submission applies every entry as an
// ordinary review of the impact, all of them or none. The review is an item of the change of the kind this package
// registers (Register), each version of the item the whole record of its data.key, as the register of pkg/risk; Reviews
// folds the items, Service (service.go) is the use case over a Port. It is a use case: pkg/graph and pkg/domain know
// nothing of it, the graph keeps the mechanism (domain.ReviewBatch).
//
// Data of a review item: key, flow (the flow branch the entries are reviewed on, "" the main flow), comment (the global
// comment), status (open, then submitted or discarded: both final), entries ([{impact, comment, outcome}]), by (the
// author), submittedAt (RFC 3339, once submitted).
package review

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
)

// KindReview is the kind of the items that hold a review of a change.
const KindReview domain.ItemKind = "review"

// Statuses of a review: it is open while the reviewer builds it, then submitted or discarded, which are final.
const (
	Open      = "open"
	Submitted = "submitted"
	Discarded = "discarded"
)

// Outcomes of an entry: the verdict on its change impact. Empty while the reviewer has not decided.
const (
	Accept = "accept"
	Reject = "reject"
)

// The refusals of the operations; the services map them as the graph's own (internal/rpcerr).
var (
	ErrInvalid   = errors.New("invalid review")
	ErrConflict  = errors.New("review conflict")
	ErrNotFound  = errors.New("review not found")
	ErrForbidden = authz.ErrForbidden
)

// Register makes the review item kind known to the graph (domain.RegisterItemKind). The services that write or read
// such items call it at start, and so does a test that builds them; it may be called any number of times.
func Register() { domain.RegisterItemKind(KindReview, validate) }

// Entry is the review of one change impact: its own comment and outcome.
type Entry struct {
	Impact  string `json:"impact"`
	Comment string `json:"comment,omitempty"`
	Outcome string `json:"outcome,omitempty"`
}

// Decided reports whether the entry has an outcome.
func (e Entry) Decided() bool { return e.Outcome == Accept || e.Outcome == Reject }

// Review is the current version of a review of the change.
type Review struct {
	Key         string    `json:"key"`
	Flow        string    `json:"flow,omitempty"`
	Comment     string    `json:"comment,omitempty"`
	Status      string    `json:"status"`
	Entries     []Entry   `json:"entries"`
	By          string    `json:"by,omitempty"`
	SubmittedAt time.Time `json:"submittedAt,omitempty"`
	// Item is the item holding this version, Versions how many versions the key has.
	Item     domain.ItemID `json:"item"`
	Versions int           `json:"versions"`
}

// Open reports whether the reviewer may still change the review.
func (r Review) Open() bool { return r.Status == Open }

// Ready reports whether the review may be submitted: open, at least one entry, an outcome on every one.
func (r Review) Ready() bool {
	if !r.Open() || len(r.Entries) == 0 {
		return false
	}
	return slices.IndexFunc(r.Entries, func(e Entry) bool { return !e.Decided() }) < 0
}

// Entry returns the entry of a change impact.
func (r Review) Entry(impact string) (Entry, bool) {
	i := slices.IndexFunc(r.Entries, func(e Entry) bool { return e.Impact == impact })
	if i < 0 {
		return Entry{}, false
	}
	return r.Entries[i], true
}

// EffectiveComment is the comment the review of an impact keeps when the review is submitted: the entry's own, then
// the global one, so that each impact's review keeps both. It is empty only when both are.
func EffectiveComment(global, entry string) string {
	global, entry = strings.TrimSpace(global), strings.TrimSpace(entry)
	switch {
	case entry == "":
		return global
	case global == "":
		return entry
	}
	return entry + "\n\nReview: " + global
}

func dataString(d map[string]any, k string) string {
	s, _ := d[k].(string)
	return strings.TrimSpace(s)
}

// entriesOf reads the entries of the data of an item.
func entriesOf(d map[string]any) []Entry {
	var out []Entry
	add := func(m map[string]any) {
		out = append(out, Entry{Impact: dataString(m, "impact"), Comment: strings.TrimSpace(dataString(m, "comment")), Outcome: dataString(m, "outcome")})
	}
	switch v := d["entries"].(type) {
	case []Entry:
		out = slices.Clone(v)
	case []map[string]any:
		for _, m := range v {
			add(m)
		}
	case []any:
		for _, x := range v {
			if m, ok := x.(map[string]any); ok {
				add(m)
			}
		}
	}
	return out
}

// dataOf is the data of an item holding the review (the whole record).
func dataOf(r Review) map[string]any {
	entries := make([]any, 0, len(r.Entries))
	for _, e := range r.Entries {
		m := map[string]any{"impact": e.Impact}
		if e.Comment != "" {
			m["comment"] = e.Comment
		}
		if e.Outcome != "" {
			m["outcome"] = e.Outcome
		}
		entries = append(entries, m)
	}
	d := map[string]any{"key": r.Key, "status": r.Status, "entries": entries}
	if r.Flow != "" {
		d["flow"] = r.Flow
	}
	if r.Comment != "" {
		d["comment"] = r.Comment
	}
	if r.By != "" {
		d["by"] = r.By
	}
	if !r.SubmittedAt.IsZero() {
		d["submittedAt"] = r.SubmittedAt.UTC().Format(time.RFC3339Nano)
	}
	return d
}

func validStatus(s string) bool { return s == Open || s == Submitted || s == Discarded }

// validate checks the data of a review item: what any version satisfies, and what a submitted one adds.
func validate(it domain.ChangeItem) error {
	d := it.Data
	if dataString(d, "key") == "" {
		return fmt.Errorf("review item requires data.key")
	}
	status := dataString(d, "status")
	if !validStatus(status) {
		return fmt.Errorf("review status must be one of %s, %s, %s", Open, Submitted, Discarded)
	}
	entries := entriesOf(d)
	seen := map[string]bool{}
	for _, e := range entries {
		if e.Impact == "" {
			return fmt.Errorf("review entry requires its impact")
		}
		if seen[e.Impact] {
			return fmt.Errorf("review names impact %s twice", e.Impact)
		}
		seen[e.Impact] = true
		if e.Outcome != "" && e.Outcome != Accept && e.Outcome != Reject {
			return fmt.Errorf("review entry outcome must be %s or %s", Accept, Reject)
		}
	}
	if status == Submitted {
		if len(entries) == 0 {
			return fmt.Errorf("a submitted review has at least one entry")
		}
		for _, e := range entries {
			if !e.Decided() {
				return fmt.Errorf("a submitted review decides every entry: impact %s has no outcome", e.Impact)
			}
		}
		if dataString(d, "submittedAt") == "" {
			return fmt.Errorf("a submitted review requires data.submittedAt")
		}
	}
	return nil
}

// Reviews folds the items of a change into its reviews: the last version of each key, in the order the keys appeared.
// A review that is submitted or discarded is final: a later version of its key is ignored. A rejected or superseded item
// is not a version. It is pure.
func Reviews(items []domain.ChangeItem) []Review {
	out := []Review{}
	at := map[string]int{}
	for _, it := range items {
		if it.Kind != KindReview || it.Status == domain.ItemRejected || it.Status == domain.ItemSuperseded {
			continue
		}
		key := dataString(it.Data, "key")
		status := dataString(it.Data, "status")
		if key == "" || !validStatus(status) {
			continue
		}
		r := Review{Key: key, Flow: it.Flow, Comment: dataString(it.Data, "comment"), Status: status, Entries: entriesOf(it.Data),
			By: dataString(it.Data, "by"), Item: it.ID, Versions: 1}
		if r.By == "" {
			r.By = it.ProducedBy
		}
		if s := dataString(it.Data, "submittedAt"); s != "" {
			r.SubmittedAt, _ = time.Parse(time.RFC3339Nano, s)
		}
		if i, ok := at[key]; ok {
			if !out[i].Open() {
				continue
			}
			r.Versions = out[i].Versions + 1
			out[i] = r
			continue
		}
		at[key] = len(out)
		out = append(out, r)
	}
	return out
}

// Find returns the review of a key.
func Find(items []domain.ChangeItem, key string) (Review, bool) {
	for _, r := range Reviews(items) {
		if r.Key == key {
			return r, true
		}
	}
	return Review{}, false
}

// chain lists the flows from flow up to the main flow ("" included, last).
func chain(c domain.Change, flow string) []string {
	out := []string{flow}
	for hops := 0; flow != "" && hops < 64; hops++ {
		f, ok := c.Flow(flow)
		if !ok {
			break
		}
		flow = f.Parent
		out = append(out, flow)
	}
	return out
}

// statusOn is the review a change impact has for a flow: the last review made on that flow, else the one of the
// impact (the main flow's).
func statusOn(cn domain.ChangeImpact, flow string) domain.NodeReview {
	if flow != "" {
		for i := len(cn.Reviews) - 1; i >= 0; i-- {
			if r := cn.Reviews[i]; r.Flow == flow && !r.Superseded {
				return r.Status
			}
		}
	}
	return cn.Review
}

// Awaiting returns the change impacts a review of a flow may take: those the flow sees (not superseded, declared on the
// flow or its parents) whose review is proposed and that no other open review of the same flow holds. Pass the reviews
// of the change (Reviews); except is the key of a review whose own entries do not count (the one being edited).
func Awaiting(c domain.Change, flow string, reviews []Review, except string) []domain.ChangeImpact {
	flows := chain(c, flow)
	held := map[string]bool{}
	for _, r := range reviews {
		if r.Open() && r.Flow == flow && r.Key != except {
			for _, e := range r.Entries {
				held[e.Impact] = true
			}
		}
	}
	var out []domain.ChangeImpact
	for _, cn := range c.Nodes {
		if cn.Superseded || !slices.Contains(flows, cn.Flow) || statusOn(cn, flow) != domain.ReviewProposed || held[string(cn.ID)] {
			continue
		}
		out = append(out, cn)
	}
	return out
}
