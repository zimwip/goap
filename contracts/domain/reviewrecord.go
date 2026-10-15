package domain

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// TypeReview is the kind of the items that hold a review of a change (ADR 0080, built in since ADR 0105): every verdict
// on a change impact is made by a submitted review, so the graph writes the record of one with the verdicts it decides.
// Each item is a version of the record of its data.key; pkg/review is the use case that builds one up before it is
// submitted.
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

// ReviewEntry is the review of one change impact in a review record: its own comment and outcome.
type ReviewEntry struct {
	Impact  string `json:"impact"`
	Comment string `json:"comment,omitempty"`
	Outcome string `json:"outcome,omitempty"`
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

// ReviewRecord is the data of a review item (the whole record, a version never restates by omission): key, flow (the
// flow the entries are reviewed on, "" the main flow), comment (the global comment), status, entries, by (the author),
// submittedAt (once submitted).
type ReviewRecord struct {
	Key         string
	Flow        string
	Comment     string
	Status      string
	Entries     []ReviewEntry
	By          string
	SubmittedAt time.Time
}

// Data is the data of the item holding the record.
func (r ReviewRecord) Data() map[string]any {
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

// ReviewRecordOf reads the record of the data of a review item.
func ReviewRecordOf(d map[string]any) ReviewRecord {
	r := ReviewRecord{Key: reviewString(d, "key"), Flow: reviewString(d, "flow"), Comment: reviewString(d, "comment"),
		Status: reviewString(d, "status"), Entries: ReviewEntriesOf(d), By: reviewString(d, "by")}
	if s := reviewString(d, "submittedAt"); s != "" {
		r.SubmittedAt, _ = time.Parse(time.RFC3339Nano, s)
	}
	return r
}

func reviewString(d map[string]any, k string) string {
	s, _ := d[k].(string)
	return strings.TrimSpace(s)
}

// ReviewEntriesOf reads the entries of the data of a review item.
func ReviewEntriesOf(d map[string]any) []ReviewEntry {
	var out []ReviewEntry
	add := func(m map[string]any) {
		out = append(out, ReviewEntry{Impact: reviewString(m, "impact"), Comment: reviewString(m, "comment"), Outcome: reviewString(m, "outcome")})
	}
	switch v := d["entries"].(type) {
	case []ReviewEntry:
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

// ValidReviewStatus reports whether s is a status of a review record.
func ValidReviewStatus(s string) bool {
	return s == ReviewStatusOpen || s == ReviewStatusSubmitted || s == ReviewStatusDiscarded
}

// validateReview checks the data of a review item: what any version satisfies, and what a submitted one adds.
func validateReview(it ChangeItem) error {
	d := it.Data
	if reviewString(d, "key") == "" {
		return fmt.Errorf("review item requires data.key")
	}
	status := reviewString(d, "status")
	if !ValidReviewStatus(status) {
		return fmt.Errorf("review status must be one of %s, %s, %s", ReviewStatusOpen, ReviewStatusSubmitted, ReviewStatusDiscarded)
	}
	entries := ReviewEntriesOf(d)
	seen := map[string]bool{}
	for _, e := range entries {
		if e.Impact == "" {
			return fmt.Errorf("review entry requires its impact")
		}
		if seen[e.Impact] {
			return fmt.Errorf("review names impact %s twice", e.Impact)
		}
		seen[e.Impact] = true
		if e.Outcome != "" && !e.Decided() {
			return fmt.Errorf("review entry outcome must be %s or %s", ReviewOutcomeAccept, ReviewOutcomeReject)
		}
	}
	if status == ReviewStatusSubmitted {
		if len(entries) == 0 {
			return fmt.Errorf("a submitted review has at least one entry")
		}
		for _, e := range entries {
			if !e.Decided() {
				return fmt.Errorf("a submitted review decides every entry: impact %s has no outcome", e.Impact)
			}
		}
		if reviewString(d, "submittedAt") == "" {
			return fmt.Errorf("a submitted review requires data.submittedAt")
		}
	}
	return nil
}
