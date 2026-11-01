package domain

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// TypeReview is the kind of the items that hold a review of a change (ADR 0080, built in since ADR 0105): every verdict
// on a change impact is made by a submitted review. Each item is a delta of the review of its data.key (ReviewDelta,
// ADR 0105 addendum): the gesture of the reviewer (opened, an impact added or removed, a comment, an outcome, the
// submission), folded into the whole review by ReviewFold. The verdicts are not stored: the item that submits the
// review decides them, and the view of the change derives them from the fold (ReviewState.Verdicts). pkg/review is the
// use case that builds one up before it is submitted.
const TypeReview ItemType = "review"

// Statuses of a review record: open while the reviewer builds it, then submitted or discarded, which are final.
const (
	ReviewStatusOpen      = "open"
	ReviewStatusSubmitted = "submitted"
	ReviewStatusDiscarded = "discarded"
)

// Outcomes of a review entry: the verdict on its change impact. Empty while the reviewer has not decided.
const (
	ReviewOutcomeAccept = "accept"
	ReviewOutcomeReject = "reject"
)

// ReviewEntry is the review of one change impact in a review record: its own comment and outcome. At is when the
// outcome was set (the time of the delta that set it: the time of the verdict), By who decided it when it is not the
// submitter (a merge carrying the verdict of a sub-change).
type ReviewEntry struct {
	Impact  string    `json:"impact"`
	Comment string    `json:"comment,omitempty"`
	Outcome string    `json:"outcome,omitempty"`
	By      string    `json:"by,omitempty"`
	At      time.Time `json:"at,omitzero"`
}

// Decided reports whether the entry has an outcome.
func (e ReviewEntry) Decided() bool {
	return e.Outcome == ReviewOutcomeAccept || e.Outcome == ReviewOutcomeReject
}

// ReviewOutcome is the outcome of a verdict.
func ReviewOutcome(s NodeReview) string {
	if s == ReviewAccepted {
		return ReviewOutcomeAccept
	}
	return ReviewOutcomeReject
}

// ReviewEffectiveComment is the comment the verdict on an impact keeps when its review is submitted: the entry's own,
// then the global one, so that each impact's review keeps both. It is empty only when both are.
func ReviewEffectiveComment(global, entry string) string {
	global, entry = strings.TrimSpace(global), strings.TrimSpace(entry)
	switch {
	case entry == "":
		return global
	case global == "":
		return entry
	}
	return entry + "\n\nReview: " + global
}

// ReviewRecord is a review as its deltas fold it: key, flow (the flow the entries are reviewed on, "" the main flow),
// comment (the global comment), status, entries, by (the author), submittedAt (once submitted).
type ReviewRecord struct {
	Key         string
	Flow        string
	Comment     string
	Status      string
	Entries     []ReviewEntry
	By          string
	SubmittedAt time.Time
}

// ReviewDelta is the data of a review item: what one gesture changes. Key names the review; Status, when set, opens it
// (the first delta), submits or discards it; Comment, when set, replaces the global comment; By is the author (the
// first delta); Remove drops entries; Entries add entries or set their comment and outcome (a field left nil is kept).
// A delta may open and submit at once (a review the graph or a single verdict submits).
type ReviewDelta struct {
	Key     string
	Status  string
	Comment *string
	By      string
	Remove  []string
	Entries []ReviewEntryEdit
}

// ReviewEntryEdit adds the entry of an impact, or sets its comment and outcome ("" takes the decision back), and who
// decided it when it is not the submitter.
type ReviewEntryEdit struct {
	Impact  string
	Comment *string
	Outcome *string
	By      string
}

// Data is the data of the item holding the delta: only what it sets.
func (d ReviewDelta) Data() map[string]any {
	out := map[string]any{"key": d.Key}
	if d.Status != "" {
		out["status"] = d.Status
	}
	if d.Comment != nil {
		out["comment"] = *d.Comment
	}
	if d.By != "" {
		out["by"] = d.By
	}
	if len(d.Remove) > 0 {
		rm := make([]any, len(d.Remove))
		for i, s := range d.Remove {
			rm[i] = s
		}
		out["remove"] = rm
	}
	if len(d.Entries) > 0 {
		ms := make([]map[string]any, len(d.Entries))
		for i, e := range d.Entries {
			m := map[string]any{"impact": e.Impact}
			if e.Comment != nil {
				m["comment"] = *e.Comment
			}
			if e.Outcome != nil {
				m["outcome"] = *e.Outcome
			}
			if e.By != "" {
				m["by"] = e.By
			}
			ms[i] = m
		}
		if defaults := entryDefaults(ms); len(defaults) > 0 {
			out[reviewDefaultsKey] = defaults
		}
		entries := make([]any, len(ms))
		for i, m := range ms {
			if len(m) == 1 {
				entries[i] = m["impact"] // its impact alone: the defaults say the rest
			} else {
				entries[i] = m
			}
		}
		out["entries"] = entries
	}
	return out
}

// reviewDefaultsKey holds what most entries of a delta set alike (ADR 0180): each entry writes what differs from it,
// null for a field it leaves unset, often its impact alone.
const reviewDefaultsKey = "defaults"

// entryFields are the fields of an entry a delta's defaults may hold.
var entryFields = []string{"outcome", "comment", "by"}

// entryDefaults takes out of the entries the value of each field most of them hold (two at least), and returns them.
func entryDefaults(ms []map[string]any) map[string]any {
	if len(ms) < 2 {
		return nil
	}
	defaults := map[string]any{}
	for _, f := range entryFields {
		count := map[string]int{}
		best, n := "", 0
		for _, m := range ms {
			if v, ok := m[f].(string); ok {
				count[v]++
				if c := count[v]; c > n {
					best, n = v, c
				}
			}
		}
		if n < 2 {
			continue
		}
		defaults[f] = best
		for _, m := range ms {
			switch v, ok := m[f]; {
			case ok && v == best:
				delete(m, f)
			case !ok:
				m[f] = nil // unset: not the default
			}
		}
	}
	return defaults
}

// ReviewDeltaOf reads the delta of the data of a review item.
func ReviewDeltaOf(d map[string]any) ReviewDelta {
	out := ReviewDelta{Key: reviewString(d, "key"), Status: reviewString(d, "status"), By: reviewString(d, "by"),
		Comment: reviewOptional(d, "comment")}
	for _, x := range reviewList(d["remove"]) {
		if s, ok := x.(string); ok {
			out.Remove = append(out.Remove, strings.TrimSpace(s))
		}
	}
	defaults, _ := d[reviewDefaultsKey].(map[string]any)
	add := func(m map[string]any) {
		if len(defaults) > 0 {
			full := maps.Clone(m)
			for f, v := range defaults {
				if _, ok := full[f]; !ok {
					full[f] = v
				}
			}
			m = full
		}
		out.Entries = append(out.Entries, ReviewEntryEdit{Impact: reviewString(m, "impact"), Comment: reviewOptional(m, "comment"),
			Outcome: reviewOptional(m, "outcome"), By: reviewString(m, "by")})
	}
	switch v := d["entries"].(type) {
	case []map[string]any:
		for _, m := range v {
			add(m)
		}
	default:
		for _, x := range reviewList(v) {
			switch e := x.(type) {
			case map[string]any:
				add(e)
			case string:
				add(map[string]any{"impact": e})
			}
		}
	}
	return out
}

func reviewList(v any) []any {
	switch l := v.(type) {
	case []any:
		return l
	case []string:
		out := make([]any, len(l))
		for i, s := range l {
			out[i] = s
		}
		return out
	}
	return nil
}

func reviewString(d map[string]any, k string) string {
	s, _ := d[k].(string)
	return strings.TrimSpace(s)
}

func reviewOptional(d map[string]any, k string) *string {
	v, ok := d[k]
	if !ok || v == nil {
		return nil // unset (null: an entry not taking its delta's default, ADR 0180)
	}
	s, _ := v.(string)
	s = strings.TrimSpace(s)
	return &s
}

// ValidReviewStatus reports whether s is a status of a review record.
func ValidReviewStatus(s string) bool {
	return s == ReviewStatusOpen || s == ReviewStatusSubmitted || s == ReviewStatusDiscarded
}

// validateReview checks the data of a review item: a delta on its own (what it means for the review is the fold's).
func validateReview(it ChangeItem) error {
	d := ReviewDeltaOf(it.Data)
	if d.Key == "" {
		return fmt.Errorf("review item requires data.key")
	}
	if d.Status != "" && !ValidReviewStatus(d.Status) {
		return fmt.Errorf("review status must be one of %s, %s, %s", ReviewStatusOpen, ReviewStatusSubmitted, ReviewStatusDiscarded)
	}
	seen := map[string]bool{}
	for _, e := range d.Entries {
		if e.Impact == "" {
			return fmt.Errorf("review entry requires its impact")
		}
		if seen[e.Impact] {
			return fmt.Errorf("review names impact %s twice", e.Impact)
		}
		seen[e.Impact] = true
		if e.Outcome != nil && *e.Outcome != "" && *e.Outcome != ReviewOutcomeAccept && *e.Outcome != ReviewOutcomeReject {
			return fmt.Errorf("review entry outcome must be %s or %s", ReviewOutcomeAccept, ReviewOutcomeReject)
		}
	}
	return nil
}

// ReviewState is a review as the deltas of its key fold it, with the item of its last delta, the number of its deltas,
// and, once submitted, who submitted it and from which action run.
type ReviewState struct {
	ReviewRecord
	Item        ItemID
	Versions    int
	SubmittedBy string
	Execution   string
}

// Open reports whether the review may still change.
func (s ReviewState) Open() bool { return s.Status == ReviewStatusOpen }

// Entry returns the entry of an impact.
func (s ReviewState) Entry(impact string) (ReviewEntry, bool) {
	i := slices.IndexFunc(s.Entries, func(e ReviewEntry) bool { return e.Impact == impact })
	if i < 0 {
		return ReviewEntry{}, false
	}
	return s.Entries[i], true
}

// ReviewFold folds the review items of a change, in the order of its log, into its reviews. Its values are never
// modified, only replaced: a copy (Clone) shares them.
type ReviewFold struct {
	at   map[string]int
	list []ReviewState
}

// Clone is a fold the caller may apply to without changing this one.
func (f *ReviewFold) Clone() *ReviewFold {
	if f == nil {
		return &ReviewFold{}
	}
	at := make(map[string]int, len(f.at))
	for k, v := range f.at {
		at[k] = v
	}
	return &ReviewFold{at: at, list: slices.Clip(f.list)}
}

// Reviews are the reviews, in the order their keys appeared.
func (f *ReviewFold) Reviews() []ReviewState {
	if f == nil {
		return nil
	}
	return f.list
}

// Get is the review of a key.
func (f *ReviewFold) Get(key string) (ReviewState, bool) {
	if f == nil {
		return ReviewState{}, false
	}
	i, ok := f.at[key]
	if !ok {
		return ReviewState{}, false
	}
	return f.list[i], true
}

// Apply folds a review item. A rejected or superseded item is not a delta; the first delta of a key opens it (status
// open, or submitted at once), a delta of a final review or of a key never opened is ignored. It reports the review
// after the item and whether the item submitted it.
func (f *ReviewFold) Apply(it ChangeItem) (s ReviewState, submitted bool) {
	if it.Type != TypeReview || it.Status == ItemRejected || it.Status == ItemSuperseded {
		return s, false
	}
	d := ReviewDeltaOf(it.Data)
	if d.Key == "" || (d.Status != "" && !ValidReviewStatus(d.Status)) {
		return s, false
	}
	if f.at == nil {
		f.at = map[string]int{}
	}
	i, known := f.at[d.Key]
	if known {
		s = f.list[i]
		if !s.Open() {
			return s, false
		}
	} else {
		if d.Status != ReviewStatusOpen && d.Status != ReviewStatusSubmitted {
			return s, false
		}
		s = ReviewState{ReviewRecord: ReviewRecord{Key: d.Key, Flow: it.Flow, By: d.By}}
		if s.By == "" {
			s.By = it.ProducedBy
		}
	}
	if d.Comment != nil {
		s.Comment = *d.Comment
	}
	s.Entries = slices.Clone(s.Entries)
	for _, rm := range d.Remove {
		s.Entries = slices.DeleteFunc(s.Entries, func(e ReviewEntry) bool { return e.Impact == rm })
	}
	for _, e := range d.Entries {
		j := slices.IndexFunc(s.Entries, func(x ReviewEntry) bool { return x.Impact == e.Impact })
		if j < 0 {
			s.Entries = append(s.Entries, ReviewEntry{Impact: e.Impact})
			j = len(s.Entries) - 1
		}
		if e.Comment != nil {
			s.Entries[j].Comment = *e.Comment
		}
		if e.Outcome != nil {
			s.Entries[j].Outcome, s.Entries[j].At = *e.Outcome, it.CreatedAt
		}
		if e.By != "" {
			s.Entries[j].By = e.By
		}
	}
	if d.Status != "" {
		s.Status = d.Status
	}
	if s.Status == ReviewStatusSubmitted {
		s.SubmittedAt, s.SubmittedBy, s.Execution = it.CreatedAt, it.ProducedBy, it.Execution
	}
	s.Item = it.ID
	s.Versions++
	if known {
		f.list = slices.Clone(f.list)
		f.list[i] = s
	} else {
		f.at[d.Key] = len(f.list)
		f.list = append(slices.Clip(f.list), s)
	}
	return s, s.Status == ReviewStatusSubmitted
}

// Verdicts are the reviewed events a submitted review decides, derived (never stored, ADR 0105 addendum): one per
// entry, at the position of the item that submitted it (seq), caused by it, each at the time its outcome was set. it is
// the item that submitted the review.
func (s ReviewState) Verdicts(change ChangeID, it ChangeItem, seq int64) []ImpactEvent {
	if s.Status != ReviewStatusSubmitted {
		return nil
	}
	out := make([]ImpactEvent, 0, len(s.Entries))
	for _, e := range s.Entries {
		if !e.Decided() {
			continue
		}
		status := ReviewAccepted
		if e.Outcome == ReviewOutcomeReject {
			status = ReviewRejected
		}
		by := e.By
		if by == "" {
			by = s.SubmittedBy
		}
		if by == "" {
			by = s.By
		}
		at := e.At
		if at.IsZero() {
			at = it.CreatedAt
		}
		r := Review{Status: status, By: by, Comment: ReviewEffectiveComment(s.Comment, e.Comment), At: at, Flow: s.Flow,
			Execution: s.Execution, ReviewID: s.Key}
		out = append(out, ImpactEvent{ID: string(it.ID) + "/" + e.Impact, Change: change, Seq: int(seq), Impact: ChangeImpactID(e.Impact),
			Op: ImpactReviewed, Flow: s.Flow, Execution: s.Execution, By: by, At: at, Causation: string(it.ID), Review: &r})
	}
	return out
}

// ReviewsOf folds the review items of a list (the items of a change, in their order).
func ReviewsOf(items []ChangeItem) *ReviewFold {
	f := &ReviewFold{}
	for _, it := range items {
		f.Apply(it)
	}
	return f
}
