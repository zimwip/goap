package intent

import (
	"context"
	"strings"
	"testing"
)

var goals = []GoalInfo{
	{Name: "assess_impact", Description: "Assess the impact of a change and produce a report", Examples: []string{"what is the impact", "what does this break"}},
	{Name: "prepare_change", Description: "Prepare the update of the repository and get it validated", Examples: []string{"update the requirements"}},
}

func TestResolveDirect(t *testing.T) {
	s := &Session{Turns: []Turn{{Role: "user", Text: "The PSP is changing its API: what does this break?"}}}
	res, err := Resolver{Ranker: Lexical{}}.Resolve(context.Background(), s, goals)
	if err != nil {
		t.Fatal(err)
	}
	if res.Goal != "assess_impact" {
		t.Fatalf("got %+v", res)
	}
}

func TestResolveClarifyThenChoose(t *testing.T) {
	s := &Session{Turns: []Turn{{Role: "user", Text: "The payment provider is changing"}}}
	r := Resolver{Ranker: Lexical{}}
	res, err := r.Resolve(context.Background(), s, goals)
	if err != nil {
		t.Fatal(err)
	}
	if res.Resolved() || !strings.Contains(res.Question, "1)") {
		t.Fatalf("expected a clarification, got %+v", res)
	}
	s.Turns = append(s.Turns, Turn{Role: "user", Text: "2"})
	res, _ = r.Resolve(context.Background(), s, goals)
	if res.Goal != "prepare_change" {
		t.Fatalf("expected prepare_change, got %+v", res)
	}
}

func TestResolveGivesUpAfterMaxTurns(t *testing.T) {
	s := &Session{}
	r := Resolver{Ranker: Lexical{}, MaxTurns: 1}
	for _, txt := range []string{"bonjour", "impact changement rapport modification"} {
		s.Turns = append(s.Turns, Turn{Role: "user", Text: txt})
		if res, _ := r.Resolve(context.Background(), s, goals); res.Resolved() {
			return
		}
	}
	t.Fatal("expected a best-effort resolution after max turns")
}
