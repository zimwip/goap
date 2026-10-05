package pbconv

import (
	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/review"
)

// ReviewToPB converts a review of the review object (ADR 0080).
func ReviewToPB(r review.Review) *graphv1.ReviewRecord {
	out := &graphv1.ReviewRecord{Key: r.Key, Flow: r.Flow, Comment: r.Comment, Status: r.Status, By: r.By, Item: string(r.Item), Versions: int32(r.Versions)}
	if !r.SubmittedAt.IsZero() {
		out.SubmittedAt = Time(r.SubmittedAt)
	}
	for _, e := range r.Entries {
		out.Entries = append(out.Entries, &graphv1.ReviewEntry{ChangeImpactId: e.Impact, Comment: e.Comment, Outcome: e.Outcome})
	}
	return out
}

// ReviewFromPB is the reverse of ReviewToPB.
func ReviewFromPB(m *graphv1.ReviewRecord) review.Review {
	if m == nil {
		return review.Review{}
	}
	out := review.Review{Key: m.Key, Flow: m.Flow, Comment: m.Comment, Status: m.Status, By: m.By, Item: domain.ItemID(m.Item), Versions: int(m.Versions), Entries: []review.Entry{}}
	if m.SubmittedAt != nil {
		out.SubmittedAt = FromTime(m.SubmittedAt)
	}
	for _, e := range m.Entries {
		out.Entries = append(out.Entries, review.Entry{Impact: e.ChangeImpactId, Comment: e.Comment, Outcome: e.Outcome})
	}
	return out
}

// ReviewEditFromPB reads the edit of an open review.
func ReviewEditFromPB(m *graphv1.ReviewUpdateRequest) review.Edit {
	e := review.Edit{Add: m.Add, Remove: m.Remove}
	if m.SetComment {
		c := m.Comment
		e.Comment = &c
	}
	for _, x := range m.Entries {
		ee := review.EntryEdit{Impact: x.ChangeImpactId}
		if x.SetComment {
			c := x.Comment
			ee.Comment = &c
		}
		if x.SetOutcome {
			o := x.Outcome
			ee.Outcome = &o
		}
		e.Entries = append(e.Entries, ee)
	}
	return e
}

// ReviewEditToPB is the reverse of ReviewEditFromPB (the request of an edit).
func ReviewEditToPB(change, key string, e review.Edit) *graphv1.ReviewUpdateRequest {
	m := &graphv1.ReviewUpdateRequest{ChangeId: change, Key: key, Add: e.Add, Remove: e.Remove}
	if e.Comment != nil {
		m.SetComment, m.Comment = true, *e.Comment
	}
	for _, x := range e.Entries {
		ee := &graphv1.ReviewEntryEdit{ChangeImpactId: x.Impact}
		if x.Comment != nil {
			ee.SetComment, ee.Comment = true, *x.Comment
		}
		if x.Outcome != nil {
			ee.SetOutcome, ee.Outcome = true, *x.Outcome
		}
		m.Entries = append(m.Entries, ee)
	}
	return m
}
