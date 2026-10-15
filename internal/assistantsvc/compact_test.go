package assistantsvc

import (
	"fmt"
	"strings"
	"testing"

	"github.com/zimwip/goap/internal/convsvc"
	"github.com/zimwip/goap/pkg/authz"
)

func TestOldMessagesAreFoldedIntoTheSummary(t *testing.T) {
	e := newEnv(t, `{"message":"ok"}`, "Durable facts:\n- the person works on CHG-7\nLanguage: English", `{"message":"still ok"}`)
	big := strings.Repeat("w", 3000)
	for i := 0; i < 20; i++ {
		role, p := convsvc.RoleUser, e.user
		if i%2 == 1 {
			role, p = convsvc.RoleAssistant, Principal
		}
		c := convsvc.Content{Text: fmt.Sprintf("%02d%s", i, big)}
		if i == 1 {
			c.Actions = []convsvc.Action{{"type": ActionStartAgent, "status": StatusStarted,
				"args": map[string]any{"methodology": "sdlc", "agent": "analyst"}, "result": map[string]any{"processId": "PRC-9", "changeId": "CHG-7"}},
				{"type": "select_project", "args": map[string]any{"project": "PROJ-B"}}}
		}
		if _, err := e.convs.Append(authz.With(e.ctx, p), p, e.conv.ID, role, c); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := e.send("latest", Context{}); err != nil {
		t.Fatal(err)
	}
	// the turn, then the summary of what fell out of its window
	if len(e.model.got) != 2 {
		t.Fatalf("%d model calls", len(e.model.got))
	}
	sumReq := e.model.got[1]
	window := len(e.model.got[0].Messages) - 1 // the history, without the new message
	if sumReq.System != summarySystem || !strings.Contains(sumReq.Messages[0].Content, "[1 user] 00www") ||
		strings.Contains(sumReq.Messages[0].Content, fmt.Sprintf("[%d ", 20-window+1)) {
		t.Fatalf("summary request %.300q (window %d)", sumReq.Messages[0].Content, window)
	}
	// the facts of the actions are recorded by code, kept before the model's text
	facts := "Recorded facts:\n- [2] the person accepted the proposal to start agent analyst of sdlc: it was started (process PRC-9 on change CHG-7)\n" +
		`- [2] select_project project="PROJ-B"` + "\n\n"
	if !strings.Contains(sumReq.Messages[0].Content, facts) {
		t.Fatalf("summary request %.600q", sumReq.Messages[0].Content)
	}
	sum, err := e.convs.Summary(e.ctx, Principal, e.conv.ID)
	if err != nil || !strings.HasPrefix(sum.Text, facts+"Durable facts:\n- the person works on CHG-7") || sum.UpTo != 20-window {
		t.Fatalf("summary %+v %v (window %d)", sum, err, window)
	}
	// the next turn gets it with its rules; two more messages out of the window are not enough to write it again
	if _, _, err := e.send("and now?", Context{}); err != nil {
		t.Fatal(err)
	}
	if len(e.model.got) != 3 || !strings.Contains(e.model.got[2].System, "Summary of the earlier part of this conversation") ||
		!strings.Contains(e.model.got[2].System, "the person works on CHG-7") {
		t.Fatalf("%d calls, system %q", len(e.model.got), e.model.got[len(e.model.got)-1].System[len(e.model.got[len(e.model.got)-1].System)-400:])
	}
}

func TestAShortConversationHasNoSummary(t *testing.T) {
	e := newEnv(t, `{"message":"ok"}`)
	if _, _, err := e.send("hello", Context{}); err != nil {
		t.Fatal(err)
	}
	if len(e.model.got) != 1 || strings.Contains(e.model.got[0].System, "Summary of the earlier part") {
		t.Fatalf("%d calls", len(e.model.got))
	}
}

func TestRecordedFactsAreKept(t *testing.T) {
	stored := joinSummary([]string{"- [2] a", "- [3] b"}, "Durable facts:\n- x\n\nConversation so far:\nnothing")
	facts, text := splitSummary(stored)
	if strings.Join(facts, "|") != "- [2] a|- [3] b" || !strings.HasPrefix(text, "Durable facts:") || !strings.HasSuffix(text, "nothing") {
		t.Fatalf("%q / %q", facts, text)
	}
	if facts, text := splitSummary("an older summary"); facts != nil || text != "an older summary" {
		t.Fatal("a summary without recorded facts")
	}
	// repeats go, then the oldest past the budget
	got := keepFacts([]string{"- [1] old", "- [2] b", "- [2] b", "- [3] c"}, 18)
	if strings.Join(got, "|") != "- [2] b|- [3] c" {
		t.Fatalf("%q", got)
	}
	if long := joinSummary([]string{"- [1] a"}, strings.Repeat("z", convsvc.MaxSummaryBytes)); len(long) > convsvc.MaxSummaryBytes {
		t.Fatalf("%d bytes", len(long))
	}
}
