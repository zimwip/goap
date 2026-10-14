package brief

import (
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/assess"
	"github.com/zimwip/goap/pkg/domain"
)

// The discussion of a topic joins the independent runs that rate it (ADR 0112 §4): two ratings and no comment are
// already a thread the next rater reads.
func TestBriefGivesTheDiscussionOfARatedTopic(t *testing.T) {
	rating := func(id string, score int, reply string) domain.ChangeItem {
		return domain.ChangeItem{ID: domain.ItemID(id), Kind: assess.KindAssessment, Status: domain.ItemAccepted,
			Data: map[string]any{"key": "description", "score": score, "reply": reply}}
	}
	c := domain.Change{ID: "C1", Title: "Note", Intent: "write a note", Items: []domain.ChangeItem{rating("a", 2, "say which release"), rating("b", 4, "clear now")}}
	got := Of(domain.Blackboard{Change: c}, nil)
	if !strings.Contains(got, "THREAD description 2 messages") || !strings.Contains(got, "say which release") {
		t.Fatalf("brief:\n%s", got)
	}
	c.Items = c.Items[:1]
	if got := Of(domain.Blackboard{Change: c}, nil); strings.Contains(got, "THREAD") {
		t.Fatalf("one rating is no discussion yet:\n%s", got)
	}
}
