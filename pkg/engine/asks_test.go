package engine

import (
	"context"
	"testing"
	"time"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
)

func TestChangeAsksAddressTheRoleAndTellWhoMayAnswer(t *testing.T) {
	e := rolesEngine(t)
	dev := authz.With(context.Background(), authz.Principal{Subject: "dev", Roles: []string{"developer"}})
	rm := authz.With(context.Background(), authz.Principal{Subject: "rm", Roles: []string{"release_manager"}})

	p, err := e.Start(dev, StartRequest{Methodology: "roled", Goal: "ship", Intent: "ship it", ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	p, _ = e.Run(dev, p.ID)
	if p.ChangeID == "" || p.Pending == nil || p.Pending.Kind != TaskApproval {
		t.Fatalf("the release must wait for a release manager on a change: %s %+v", p.ChangeID, p.Pending)
	}

	asks, err := e.ChangeAsks(dev, p.ChangeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(asks) != 1 || asks[0].Kind != TaskApproval || asks[0].ProcessID != p.ID || asks[0].Action != "release" ||
		len(asks[0].Roles) != 1 || asks[0].Roles[0] != "release_manager" || asks[0].Owner != "" {
		t.Fatalf("one approval addressed to the release manager: %+v", asks)
	}
	if asks[0].MayAnswer {
		t.Fatal("a developer may not answer the release manager's approval")
	}
	if asks, err = e.ChangeAsks(rm, p.ChangeID); err != nil || len(asks) != 1 || !asks[0].MayAnswer {
		t.Fatalf("the release manager answers it: %+v %v", asks, err)
	}

	if _, err := e.Approve(rm, p.ID, true, "go"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(rm, p.ID); err != nil {
		t.Fatal(err)
	}
	if asks, err = e.ChangeAsks(dev, p.ChangeID); err != nil || len(asks) != 0 {
		t.Fatalf("nothing is asked once approved: %+v %v", asks, err)
	}
}

func TestDecisionAsks(t *testing.T) {
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	points := []domain.DecisionPoint{
		{ID: "d1", Question: "Which option?", Status: domain.PointBlocked, OpenedAt: at,
			Questions: []domain.Question{{ID: "q1", Point: "d1", Text: "What is the budget?", Status: domain.QuestionOpen, AskedAt: at.Add(time.Hour)},
				{ID: "q0", Point: "d1", Text: "Answered", Status: "answered"}}},
		{ID: "d2", Question: "Go or no go?", Status: domain.PointEscalated, Escalation: "too many rounds", OpenedAt: at},
		{ID: "d3", Question: "Agents rule it", Status: domain.PointOpen, OpenedAt: at},
		{ID: "d4", Question: "Done", Status: domain.PointDecided, OpenedAt: at},
	}
	asks := decisionAsks(points)
	if len(asks) != 2 {
		t.Fatalf("a question and an escalated point: %+v", asks)
	}
	if a := asks[0]; a.Kind != AskQuestion || a.Question != "q1" || a.Point != "d1" || a.Title != "What is the budget?" || !a.Since.Equal(at.Add(time.Hour)) {
		t.Fatalf("question: %+v", a)
	}
	if a := asks[1]; a.Kind != AskDecision || a.Point != "d2" || a.Detail != "too many rounds" {
		t.Fatalf("decision: %+v", a)
	}
}
