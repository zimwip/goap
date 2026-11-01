package graph

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/review"
)

// reviewWorld is a change proposing the three nodes of the fixture: three impacts awaiting review.
func reviewWorld(t *testing.T, repo Repo) (fixture, domain.Change, []domain.ChangeImpact, review.Service) {
	t.Helper()
	ctx := context.Background()
	f := newFixture(t, repo)
	c, err := f.g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "review", ForkCommit: f.base.ID})
	if err != nil {
		t.Fatal(err)
	}
	var nodes []domain.ChangeImpact
	for _, n := range []domain.Node{f.need, f.req, f.test} {
		pre := n.Ref()
		nodes = append(nodes, domain.ChangeImpact{Intent: domain.IntentModified, Pre: &pre, Rationale: "r", ProducedBy: "bob"})
	}
	got, err := f.g.ProposeImpact(ctx, c.ID, nodes)
	if err != nil {
		t.Fatal(err)
	}
	// each impact modifies its node: only a modification is accepted (ADR 0102)
	for i := range got {
		if got[i], err = f.g.edit(ctx, c.ID, got[i].ID, edit{Properties: map[string]any{"title": "reviewed " + got[i].Key}}); err != nil {
			t.Fatal(err)
		}
	}
	return f, c, got, review.Service{Port: f.g}
}

func reviewsOf(t *testing.T, g *Graph, id domain.ChangeID) []review.Review {
	t.Helper()
	c, err := g.Change(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return review.Reviews(c.Items)
}

func reviewedEvents(t *testing.T, g *Graph, id domain.ChangeID) []domain.ImpactEvent {
	t.Helper()
	var out []domain.ImpactEvent
	err := g.repo.InTx(context.Background(), func(tx Tx) error {
		events, err := impactEvents(context.Background(), tx, id)
		for _, e := range events {
			if e.Op == domain.ImpactReviewed {
				out = append(out, e)
			}
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// The batch reviews several impacts in one transaction, each with its own comment and outcome, every review carrying
// the id of the batch (ADR 0080); it writes one entry, the review opened and submitted, and the view derives the verdicts
// from it (ADR 0105 addendum).
func TestReviewBatch(t *testing.T) { forEachRepo(t, testReviewBatch) }

func testReviewBatch(t *testing.T, repo Repo) {
	ctx := context.Background()
	f, c, imps, _ := reviewWorld(t, repo)
	g := f.g
	before := len(logT(t, g, c.ID))
	out, err := g.ImpactNodeReviewBatch(ctx, c.ID, domain.ReviewBatch{ID: "REV-1", By: "alice", Verdicts: []domain.ImpactVerdict{
		{Impact: imps[0].ID, Status: domain.ReviewAccepted, Comment: "fine"},
		{Impact: imps[1].ID, Status: domain.ReviewRejected, Comment: "no"},
	}})
	if err != nil || len(out) != 2 {
		t.Fatalf("batch: %v %v", out, err)
	}
	if l := logT(t, g, c.ID); len(l) != before+1 || l[len(l)-1].Type != domain.LogFact+"."+string(domain.TypeReview) {
		t.Fatalf("a submission is one entry: %d -> %d", before, len(l))
	}
	ch, _ := g.Change(ctx, c.ID)
	if ch.Nodes[0].Review != domain.ReviewAccepted || ch.Nodes[1].Review != domain.ReviewRejected || ch.Nodes[2].Review != domain.ReviewProposed {
		t.Fatalf("outcomes: %v %v %v", ch.Nodes[0].Review, ch.Nodes[1].Review, ch.Nodes[2].Review)
	}
	if r := ch.Nodes[0].Reviews[0]; r.ReviewID != "REV-1" || r.By != "alice" || r.Comment != "fine" {
		t.Fatalf("review: %+v", r)
	}
	evs := reviewedEvents(t, g, c.ID)
	if len(evs) != 2 || evs[0].Review.ReviewID != "REV-1" || evs[1].Review.ReviewID != "REV-1" {
		t.Fatalf("events: %+v", evs)
	}
	if rs := review.Reviews(ch.Items); len(rs) != 1 || rs[0].Status != review.Submitted {
		t.Fatalf("item: %+v", rs)
	}
	// the submission of the review is the cause of each verdict it decides, at its position (ADR 0105 addendum)
	causes := causesOf(t, g, c.ID)
	if len(causes) != 2 {
		t.Fatalf("causation: %+v", causes)
	}
	for _, cause := range causes {
		if cause.cause.ID != string(rs0(t, g, c.ID, "REV-1")) || cause.cause.Seq != cause.seq {
			t.Fatalf("causation: %+v", causes)
		}
	}
	// a verdict on its own is a review of one entry, submitted at once
	if _, err := g.accept(ctx, c.ID, imps[2].ID, "carol", "ok"); err != nil {
		t.Fatal(err)
	}
	evs = reviewedEvents(t, g, c.ID)
	ch, _ = g.Change(ctx, c.ID)
	lone, ok := review.Find(ch.Items, evs[2].Review.ReviewID)
	if !ok || lone.Status != review.Submitted || lone.By != "carol" || len(lone.Entries) != 1 || lone.Entries[0].Impact != string(imps[2].ID) || lone.Entries[0].Comment != "ok" || lone.Entries[0].Outcome != review.Accept {
		t.Fatalf("a lone verdict is a submitted review of one entry: %+v (%v)", lone, ok)
	}
	if causes := causesOf(t, g, c.ID); len(causes) != 3 || causes[2].cause.ID != string(lone.Item) {
		t.Fatalf("the lone verdict is caused by its review: %+v", causes)
	}
}

// causedVerdict is the position of a verdict and the entry it names as its cause.
type causedVerdict struct {
	seq   int64
	cause domain.LogEntry
}

// logT is the log of a change.
func logT(t *testing.T, g *Graph, id domain.ChangeID) []domain.LogEntry {
	t.Helper()
	entries, _, err := g.ChangeLog(context.Background(), domain.LogFilter{Change: id})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

// causesOf lists the decided verdicts of a change (derived from its log) with their cause, which must be a fact.review
// entry.
func causesOf(t *testing.T, g *Graph, id domain.ChangeID) []causedVerdict {
	t.Helper()
	byID := map[string]domain.LogEntry{}
	for _, e := range logT(t, g, id) {
		byID[e.ID] = e
	}
	var out []causedVerdict
	for _, e := range reviewedEvents(t, g, id) {
		if e.Causation == "" {
			continue
		}
		cause, ok := byID[e.Causation]
		if !ok || cause.Type != domain.LogFact+"."+string(domain.TypeReview) {
			t.Fatalf("verdict %d names cause %q, not a review: %+v", e.Seq, e.Causation, cause)
		}
		out = append(out, causedVerdict{seq: int64(e.Seq), cause: cause})
	}
	return out
}

// rs0 is the item holding the last version of a review.
func rs0(t *testing.T, g *Graph, id domain.ChangeID, key string) domain.ItemID {
	t.Helper()
	c, err := g.Change(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	r, ok := review.Find(c.Items, key)
	if !ok {
		t.Fatalf("no review %s", key)
	}
	return r.Item
}

// One refused verdict refuses the whole batch: nothing is written, the error names the entry (ADR 0080).
func TestReviewBatchIsAtomic(t *testing.T) { forEachRepo(t, testReviewBatchIsAtomic) }

func testReviewBatchIsAtomic(t *testing.T, repo Repo) {
	ctx := context.Background()
	f, c, imps, _ := reviewWorld(t, repo)
	g := f.g
	if _, err := g.accept(ctx, c.ID, imps[1].ID, "alice", "already"); err != nil {
		t.Fatal(err)
	}
	logLen := func() int {
		entries, _, err := g.ChangeLog(ctx, domain.LogFilter{Change: c.ID})
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}
	before := logLen()
	_, err := g.ImpactNodeReviewBatch(ctx, c.ID, domain.ReviewBatch{ID: "REV-2", By: "bob", Verdicts: []domain.ImpactVerdict{
		{Impact: imps[0].ID, Status: domain.ReviewAccepted, Comment: "ok"},
		{Impact: imps[1].ID, Status: domain.ReviewAccepted, Comment: "twice"},
	}})
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "verdict 2 (REQ-1)") {
		t.Fatalf("the failing entry is named: %v", err)
	}
	ch, _ := g.Change(ctx, c.ID)
	if ch.Nodes[0].Review != domain.ReviewProposed || len(ch.Nodes[0].Reviews) != 0 || logLen() != before || len(review.Reviews(ch.Items)) != 1 { // the review of the earlier lone verdict
		t.Fatalf("a refused batch writes nothing: %+v, log %d -> %d", ch.Nodes[0], before, logLen())
	}
	// the review policy refuses per impact too, whatever the position
	g.ReviewPolicy = refuseReviewer("mallory")
	if _, err := g.ImpactNodeReviewBatch(ctx, c.ID, domain.ReviewBatch{By: "mallory", Verdicts: []domain.ImpactVerdict{{Impact: imps[0].ID, Status: domain.ReviewAccepted, Comment: "x"}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("policy: %v", err)
	}
	for _, bad := range []domain.ReviewBatch{
		{By: "a"}, // no verdict
		{By: "a", Verdicts: []domain.ImpactVerdict{{Impact: imps[0].ID, Status: domain.ReviewProposed, Comment: "x"}}},
		{By: "a", Verdicts: []domain.ImpactVerdict{{Impact: imps[0].ID, Status: domain.ReviewAccepted}}},
		{By: "a", Verdicts: []domain.ImpactVerdict{{Impact: imps[0].ID, Status: domain.ReviewAccepted, Comment: "x"}, {Impact: imps[0].ID, Status: domain.ReviewRejected, Comment: "y"}}},
	} {
		if _, err := g.ImpactNodeReviewBatch(ctx, c.ID, bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
}

// The operations of the use case: open, add and remove impacts, edit entries, submit, final once submitted.
func TestReviewObject(t *testing.T) { forEachRepo(t, testReviewObject) }

func testReviewObject(t *testing.T, repo Repo) {
	ctx := context.Background()
	f, c, imps, svc := reviewWorld(t, repo)
	g := f.g
	alice, admin := review.Actor{Subject: "alice"}, review.Actor{Subject: "root", Admin: true}
	id := func(i int) string { return string(imps[i].ID) }
	str := func(s string) *string { return &s }

	r, err := svc.Open(ctx, c.ID, "", " global ", alice)
	if err != nil || r.Status != review.Open || r.By != "alice" || r.Comment != "global" || len(r.Entries) != 0 {
		t.Fatalf("open: %+v %v", r, err)
	}
	r, err = svc.Update(ctx, c.ID, r.Key, review.Edit{Add: []string{id(0), id(1)}, Entries: []review.EntryEdit{{Impact: id(0), Comment: str("a"), Outcome: str("accept")}}}, alice)
	if err != nil || len(r.Entries) != 2 || r.Entries[0].Outcome != review.Accept || r.Entries[0].Comment != "a" || r.Entries[1].Outcome != "" {
		t.Fatalf("add: %+v %v", r, err)
	}
	// an impact is in one open review per flow
	other, err := svc.Open(ctx, c.ID, "", "", alice)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update(ctx, c.ID, other.Key, review.Edit{Add: []string{id(1)}}, alice); !errors.Is(err, review.ErrConflict) || !strings.Contains(err.Error(), r.Key) {
		t.Fatalf("an impact in two open reviews: %v", err)
	}
	if _, err := svc.Update(ctx, c.ID, other.Key, review.Edit{Add: []string{"nope"}}, alice); !errors.Is(err, review.ErrNotFound) {
		t.Fatalf("unknown impact: %v", err)
	}
	// what awaits review leaves the list once taken, and comes back when removed
	ch, _ := g.Change(ctx, c.ID)
	if aw := review.Awaiting(ch, "", review.Reviews(ch.Items), ""); len(aw) != 1 || aw[0].ID != imps[2].ID {
		t.Fatalf("awaiting: %+v", aw)
	}
	r, err = svc.Update(ctx, c.ID, r.Key, review.Edit{Remove: []string{id(1)}}, alice)
	if err != nil || len(r.Entries) != 1 {
		t.Fatalf("remove: %+v %v", r, err)
	}
	if _, err := svc.Update(ctx, c.ID, other.Key, review.Edit{Add: []string{id(1)}}, alice); err != nil {
		t.Fatalf("the impact is free again: %v", err)
	}
	// authorization: the author or an administrator
	if _, err := svc.Update(ctx, c.ID, r.Key, review.Edit{Comment: str("hijack")}, review.Actor{Subject: "mallory"}); !errors.Is(err, review.ErrForbidden) {
		t.Fatalf("someone else: %v", err)
	}
	if _, err := svc.Submit(ctx, c.ID, r.Key, review.Actor{Subject: "mallory"}); !errors.Is(err, review.ErrForbidden) {
		t.Fatalf("someone else submits: %v", err)
	}
	if _, err := svc.Update(ctx, c.ID, r.Key, review.Edit{Add: []string{id(2)}, Entries: []review.EntryEdit{{Impact: id(2), Outcome: str("reject")}}}, admin); err != nil {
		t.Fatalf("an administrator: %v", err)
	}
	// every entry decided, a comment on each (its own or the global one)
	if _, err := svc.Submit(ctx, c.ID, "REV-none", alice); !errors.Is(err, review.ErrNotFound) {
		t.Fatalf("unknown review: %v", err)
	}
	r, _ = review.Find(mustChange(t, g, c.ID).Items, r.Key)
	if r.Ready() != true {
		t.Fatalf("ready: %+v", r)
	}
	r, err = svc.Update(ctx, c.ID, r.Key, review.Edit{Entries: []review.EntryEdit{{Impact: id(2), Outcome: str("")}}}, alice)
	if err != nil || r.Ready() {
		t.Fatalf("an undecided entry: %+v %v", r, err)
	}
	if _, err := svc.Submit(ctx, c.ID, r.Key, alice); !errors.Is(err, review.ErrInvalid) {
		t.Fatalf("submit needs an outcome everywhere: %v", err)
	}
	if _, err := svc.Update(ctx, c.ID, r.Key, review.Edit{Entries: []review.EntryEdit{{Impact: id(2), Outcome: str("reject"), Comment: str("not yet")}}}, alice); err != nil {
		t.Fatal(err)
	}
	// the review's own failure leaves it open: REQ-1 is accepted by someone else meanwhile
	if _, err := g.accept(ctx, c.ID, imps[2].ID, "carol", "meanwhile"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Submit(ctx, c.ID, r.Key, alice); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "verdict 2") {
		t.Fatalf("an impact no longer proposed: %v", err)
	}
	if got, _ := review.Find(mustChange(t, g, c.ID).Items, r.Key); !got.Open() || len(reviewedEvents(t, g, c.ID)) != 1 {
		t.Fatalf("the review stays open, nothing was reviewed: %+v", got)
	}
	r, err = svc.Update(ctx, c.ID, r.Key, review.Edit{Remove: []string{id(2)}}, alice)
	if err != nil {
		t.Fatal(err)
	}
	r, err = svc.Submit(ctx, c.ID, r.Key, alice)
	if err != nil || r.Status != review.Submitted || r.SubmittedAt.IsZero() || r.Item == "" {
		t.Fatalf("submit: %+v %v", r, err)
	}
	ch = mustChange(t, g, c.ID)
	if ch.Nodes[0].Review != domain.ReviewAccepted || ch.Nodes[0].Reviews[0].ReviewID != r.Key || ch.Nodes[0].Reviews[0].By != "alice" {
		t.Fatalf("the impact is reviewed in the review: %+v", ch.Nodes[0])
	}
	// the comment of the review keeps both
	if got := ch.Nodes[0].Reviews[0].Comment; got != "a\n\nReview: global" {
		t.Fatalf("comment = %q", got)
	}
	// final
	if _, err := svc.Update(ctx, c.ID, r.Key, review.Edit{Comment: str("late")}, alice); !errors.Is(err, review.ErrConflict) {
		t.Fatalf("a submitted review is final: %v", err)
	}
	if _, err := svc.Discard(ctx, c.ID, r.Key, alice); !errors.Is(err, review.ErrConflict) {
		t.Fatalf("a submitted review is not discarded: %v", err)
	}
	if _, err := svc.Submit(ctx, c.ID, r.Key, alice); !errors.Is(err, review.ErrConflict) {
		t.Fatalf("submitted twice: %v", err)
	}
	d, err := svc.Discard(ctx, c.ID, other.Key, alice)
	if err != nil || d.Status != review.Discarded {
		t.Fatalf("discard: %+v %v", d, err)
	}
	if _, err := svc.Update(ctx, c.ID, other.Key, review.Edit{Comment: str("x")}, alice); !errors.Is(err, review.ErrConflict) {
		t.Fatalf("a discarded review is final: %v", err)
	}
	// a discarded review frees its impacts
	if aw := review.Awaiting(mustChange(t, g, c.ID), "", review.Reviews(mustChange(t, g, c.ID).Items), ""); len(aw) != 1 || aw[0].ID != imps[1].ID {
		t.Fatalf("the impact of the discarded review awaits again: %+v", aw)
	}
}

func mustChange(t *testing.T, g *Graph, id domain.ChangeID) domain.Change {
	t.Helper()
	c, err := g.Change(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A review takes the impacts of its flow: a review opened while an option is active is on the option, and the review
// of the main flow named explicitly is not taken by it.
func TestReviewOnTheMainFlowWhileAnOptionIsActive(t *testing.T) {
	forEachRepo(t, func(t *testing.T, repo Repo) {
		ctx := context.Background()
		f, c, imps, svc := reviewWorld(t, repo)
		who := review.Actor{Subject: "alice"}
		r, err := svc.Open(ctx, c.ID, domain.MainFlow, "", who)
		if err != nil || r.Flow != "" {
			t.Fatalf("open on main: %+v %v", r, err)
		}
		if _, err := f.g.OpenOption(ctx, c.ID, OpenOptionRequest{Name: "opt", Hypothesis: "h", Activate: true, By: "alice"}); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Update(ctx, c.ID, r.Key, review.Edit{Add: []string{string(imps[0].ID)}, Entries: []review.EntryEdit{{Impact: string(imps[0].ID), Outcome: ptr("accept"), Comment: ptr("c")}}}, who); err != nil {
			t.Fatalf("the review stays on its flow: %v", err)
		}
		if got, err := svc.Submit(ctx, c.ID, r.Key, who); err != nil || got.Flow != "" || got.Status != review.Submitted {
			t.Fatalf("submit on main while an option is active: %+v %v", got, err)
		}
		if mustChange(t, f.g, c.ID).Nodes[0].Review != domain.ReviewAccepted {
			t.Fatal("main flow review")
		}
		on, err := svc.Open(ctx, c.ID, "", "", who)
		if err != nil || on.Flow == "" {
			t.Fatalf("a review with no flow is on the active option: %+v %v", on, err)
		}
	})
}

func ptr[T any](v T) *T { return &v }

// A batch settles the origins of a successor first, whatever the order of its verdicts: the successor is listed
// before the impact deciding the sources it derives from and the batch still stands (ADR 0077 gate, ADR 0080, 0102).
func TestReviewBatchSettlesOriginsFirst(t *testing.T) {
	forEachRepo(t, testReviewBatchSettlesOriginsFirst)
}

func testReviewBatchSettlesOriginsFirst(t *testing.T, repo Repo) {
	d := newDocs(t, repo)
	ctx, g := d.ctx, d.g
	f, a, b := d.node(t, "F", "Folder"), d.node(t, "A", "Item"), d.node(t, "B", "Item")
	d.link(t, "contains", f, a)
	d.link(t, "contains", f, b)
	c := d.change(t)
	res := must[Restructured](t)(g.ImpactNodeMerge(ctx, c.ID, MergeInput{Sources: []NodeName{{Key: "A"}, {Key: "B"}}, Into: NodeCreate{Key: "C", Type: "docs@Item", Rationale: "merge"}}))
	vs := []domain.ImpactVerdict{{Impact: res.Successors[0].ID, Status: domain.ReviewAccepted, Comment: "c"}}
	for _, s := range res.Parents { // the merge is decided by the impact of the parent (ADR 0102)
		vs = append(vs, domain.ImpactVerdict{Impact: s.ID, Status: domain.ReviewAccepted, Comment: "ok"})
	}
	if _, err := g.ImpactNodeReviewBatch(ctx, c.ID, domain.ReviewBatch{ID: "R", By: "alice", Verdicts: vs}); err != nil {
		t.Fatalf("the origins are settled first: %v", err)
	}
	// without the parent in the batch, the gate still refuses it and nothing is written
	c2 := d.change(t)
	res2 := must[Restructured](t)(g.ImpactNodeMerge(ctx, c2.ID, MergeInput{Sources: []NodeName{{Key: "A"}, {Key: "B"}}, Into: NodeCreate{Key: "C2", Type: "docs@Item", Rationale: "merge"}}))
	_, err := g.ImpactNodeReviewBatch(ctx, c2.ID, domain.ReviewBatch{By: "alice", Verdicts: []domain.ImpactVerdict{{Impact: res2.Successors[0].ID, Status: domain.ReviewAccepted, Comment: "c"}}})
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "derives from") {
		t.Fatalf("the origins gate: %v", err)
	}
	if got := mustChange(t, g, c2.ID); got.Nodes[0].Review != domain.ReviewProposed {
		t.Fatalf("nothing written: %+v", got.Nodes)
	}
}
