package graph

import (
	"context"
	"strings"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

// A commit is one event of the log naming the nodes it commits (ADR 0144 addendum): the versions its change wrote are
// left to the version store and read back from it, so are the ones the change's merge into main takes. Each verdict of
// a review is an entry of its own (an acceptance is of one impact), the review submitted one fact.
func TestCommitIsOneEvent(t *testing.T) { forEachRepo(t, testCommitIsOneEvent) }

func testCommitIsOneEvent(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "one event", ForkCommit: f.base.ID, OwnBranch: true}))
	req := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Node: f.req.ID, Rationale: "v2"}))
	must[domain.ChangeImpact](t)(g.ImpactNodeUpdate(ctx, c.ID, req.ID, NodeUpdate{Properties: map[string]any{"title": "Use PSP v2"}}))
	design := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "D-1", Type: "Design", Rationale: "new", Properties: map[string]any{"title": "PSP"}}))
	if _, err := g.ImpactNodeReviewBatch(ctx, c.ID, domain.ReviewBatch{By: "bob", Verdicts: []domain.ImpactVerdict{
		{Impact: req.ID, Status: domain.ReviewAccepted, Comment: "ok"}, {Impact: design.ID, Status: domain.ReviewAccepted, Comment: "ok"}}}); err != nil {
		t.Fatal(err)
	}
	must[domain.Commit](t)(g.CommitChange(ctx, c.ID, ""))
	must[domain.Change](t)(g.IntegrateChange(ctx, c.ID, nil))

	var raw []domain.LogEntry
	if err := repo.InTx(ctx, func(tx Tx) (err error) {
		raw, err = tx.Log(ctx, domain.LogFilter{Change: c.ID, Types: []string{domain.LogImpact + "."}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	commits, verdicts := 0, 0
	for _, l := range raw {
		switch l.Type {
		case domain.LogImpact + "." + string(domain.ImpactCommitted):
			commits++
			p := string(l.Payload)
			if strings.Contains(p, "impactId") || strings.Contains(p, `"version"`) || !strings.Contains(p, `"nodes"`) {
				t.Errorf("a commit names its nodes only: %s", p)
			}
		case domain.LogImpact + "." + string(domain.ImpactReviewed):
			verdicts++
			if strings.Contains(string(l.Payload), `"impacts"`) {
				t.Errorf("a verdict is an entry of its own: %s", l.Payload)
			}
		}
	}
	if verdicts != 2 {
		t.Fatalf("one entry per verdict: %d", verdicts)
	}
	if commits != 2 {
		t.Fatalf("the commit and the merge into main are one event each: %d", commits)
	}
	// read back, each node has its version and its impact
	entries, _, err := g.ChangeLog(ctx, domain.LogFilter{Change: c.ID, Types: []string{domain.LogImpact + "." + string(domain.ImpactCommitted)}})
	if err != nil {
		t.Fatal(err)
	}
	got := must[domain.Change](t)(g.Change(ctx, c.ID))
	for _, l := range entries {
		events, err := domain.ImpactEventsOf(l)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range events {
			i := got.Impacts().Index(e.Impact)
			if !e.Hydrated() || i < 0 {
				t.Fatalf("a node of a commit read back: %+v", e)
			}
			if cn := got.Impacts().At(i); cn.Committed == nil || *cn.Committed != *e.Committed {
				t.Fatalf("a node of a commit read back: %+v (impact %+v)", e, cn)
			}
		}
	}
}
