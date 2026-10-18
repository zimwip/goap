package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	assistantv1 "github.com/zimwip/goap/contracts/gen/goap/assistant/v1"
	"github.com/zimwip/goap/contracts/gen/goap/assistant/v1/assistantv1connect"
	convv1 "github.com/zimwip/goap/contracts/gen/goap/conversations/v1"
	"github.com/zimwip/goap/contracts/gen/goap/conversations/v1/conversationsv1connect"
	graphv1 "github.com/zimwip/goap/contracts/gen/goap/graph/v1"
	"github.com/zimwip/goap/contracts/gen/goap/graph/v1/graphv1connect"
)

// TestAssistantOnFakeModel: with no API key the assistant runs on the fake echo model, which still calls its tools:
// it lists the agents the person may run and proposes to start one.
func TestAssistantOnFakeModel(t *testing.T) {
	_, ts := smokeApp(t)
	ctx := context.Background()
	cv := conversationsv1connect.NewConversationServiceClient(ts.Client(), ts.URL)
	as := assistantv1connect.NewAssistantServiceClient(ts.Client(), ts.URL)
	c, err := cv.CreateConversation(ctx, connect.NewRequest(&convv1.CreateConversationRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	id := c.Msg.Conversation.Id
	ask := func(project, text string) *convv1.Message {
		t.Helper()
		req := &assistantv1.SendRequest{ConversationId: id, Text: text}
		if project != "" {
			req.Context = &assistantv1.PageContext{App: &assistantv1.AppContext{Project: project}}
		}
		if _, err := as.Send(ctx, connect.NewRequest(req)); err != nil {
			t.Fatal(err)
		}
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			g, err := cv.GetConversation(ctx, connect.NewRequest(&convv1.GetConversationRequest{Id: id}))
			if err != nil {
				t.Fatal(err)
			}
			last := g.Msg.Messages[len(g.Msg.Messages)-1]
			if last.Role == "assistant" && last.Status != "pending" {
				return last
			}
		}
		t.Fatal("no answer")
		return nil
	}

	// a fresh install: the root project names no methodology, and the assistant says so
	if m := ask("", "which agents can I run?"); !strings.Contains(m.Text, "names no methodology") {
		t.Fatalf("fresh install: %q", m.Text)
	}

	g := graphv1connect.NewGraphServiceClient(ts.Client(), ts.URL)
	if _, err := g.LoadDemoData(ctx, connect.NewRequest(&graphv1.LoadDemoDataRequest{})); err != nil {
		t.Fatal(err)
	}
	m := ask("PROJ-DEMO", "which agents can I run?")
	if !strings.Contains(m.Text, "list_agents returned") || !strings.Contains(m.Text, "sdlc") {
		t.Fatalf("demo project: %q", m.Text)
	}
	agent := ""
	for _, line := range strings.Split(m.Text, "\n") {
		if f := strings.Split(strings.TrimPrefix(line, "- "), " · "); strings.HasPrefix(line, "- ") && len(f) > 1 && f[0] == "sdlc" {
			agent = f[1]
			break
		}
	}
	if agent == "" {
		t.Fatalf("no sdlc agent listed: %q", m.Text)
	}
	// starting it searches the open changes, then proposes the agent on a new change, which the person accepts
	m = ask("PROJ-DEMO", "start the "+agent+" agent")
	index := -1
	for i, a := range m.Actions {
		if a.AsMap()["type"] == "start_agent" && a.AsMap()["status"] == "proposed" {
			index = i
		}
	}
	if index < 0 {
		t.Fatalf("start: %q %v", m.Text, m.Actions)
	}
	r, err := as.ConfirmAction(ctx, connect.NewRequest(&assistantv1.ConfirmActionRequest{ConversationId: id, MessageId: m.Id,
		ActionIndex: int32(index), Decision: "accept", Project: "PROJ-DEMO"}))
	if err != nil {
		t.Fatal(err)
	}
	if a := r.Msg.Message.Actions[index].AsMap(); a["status"] != "started" {
		t.Fatalf("confirm: %v", a)
	}
}
