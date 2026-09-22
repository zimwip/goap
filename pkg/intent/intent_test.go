package intent

import (
	"context"
	"strings"
	"testing"
)

var goals = []GoalInfo{
	{Name: "assess_impact", Description: "Évaluer l'impact d'un changement et produire un rapport", Examples: []string{"quel est l'impact", "qu'est-ce que ça casse"}},
	{Name: "prepare_change", Description: "Préparer la modification du référentiel et la faire valider", Examples: []string{"mettre à jour les exigences"}},
}

func TestResolveDirect(t *testing.T) {
	s := &Session{Turns: []Turn{{Role: "user", Text: "Le PSP change d'API : qu'est-ce que ça casse ?"}}}
	res, err := Resolver{Ranker: Lexical{}}.Resolve(context.Background(), s, goals)
	if err != nil {
		t.Fatal(err)
	}
	if res.Goal != "assess_impact" {
		t.Fatalf("got %+v", res)
	}
}

func TestResolveClarifyThenChoose(t *testing.T) {
	s := &Session{Turns: []Turn{{Role: "user", Text: "Le fournisseur de paiement change"}}}
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
