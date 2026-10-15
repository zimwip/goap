package assistantsvc

import (
	"fmt"
	"strings"
	"testing"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/platform/convsvc"
)

func TestOldMessagesAreFoldedIntoTheSummary(t *testing.T) {
	e := newEnv(t, `{"message":"ok"}`, "Durable facts:\n- the person works on CHG-7\nLanguage: English", `{"message":"still ok"}`)
	big := strings.Repeat("w", 3000)
	for i := 0; i < 20; i++ {
		role, p := convsvc.RoleUser, e.user
		if i%2 == 1 {
			role, p = convsvc.RoleAssistant, Principal
		}
		if _, err := e.convs.Append(authz.With(e.ctx, p), p, e.conv.ID, role, convsvc.Content{Text: fmt.Sprintf("%02d%s", i, big)}); err != nil {
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
	sum, err := e.convs.Summary(e.ctx, Principal, e.conv.ID)
	if err != nil || !strings.Contains(sum.Text, "CHG-7") || sum.UpTo != 20-window {
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
