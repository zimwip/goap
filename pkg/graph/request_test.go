package graph

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

// Requests (ADR 0098): created open, triaged by their first link (taking the project of the change), linked to root
// changes of their project only, delivered when every linked change is applied, closed explicitly; their log records
// everything; a change records its links in its own log.
func TestRequests(t *testing.T) { forEachRepo(t, testRequests) }

func testRequests(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newMoveWorld(t, repo)
	g := w.g
	g.Caller = func(context.Context) string { return "alice" }

	if _, err := g.CreateRequest(ctx, NewRequest{Title: " "}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no title: %v", err)
	}
	if _, err := g.CreateRequest(ctx, NewRequest{Title: "t", Origin: domain.RequestOrigin{Kind: "fax"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unknown origin: %v", err)
	}
	if _, err := g.CreateRequest(ctx, NewRequest{Title: "t", ProjectID: "PROJ-NOPE"}); err == nil {
		t.Fatal("an unknown project")
	}
	r := must[domain.Request](t)(g.CreateRequest(ctx, NewRequest{Title: "Faster checkout", Text: "the checkout is slow", Origin: domain.RequestOrigin{Kind: domain.OriginConversation, Ref: "conv/1"}}))
	if r.Status != domain.RequestOpen || r.Requester != "alice" || r.ProjectID != "" || r.Origin.Kind != domain.OriginConversation {
		t.Fatalf("request: %+v", r)
	}

	// a first link triages the request: it takes the project of the change
	c := w.open(t, "PROJ-A")
	r = must[domain.Request](t)(g.LinkRequest(ctx, r.ID, c.ID, domain.LinkOrigin))
	if r.Status != domain.RequestTriaged || r.ProjectID != "PROJ-A" || len(r.Links) != 1 || r.Links[0].Role != domain.LinkOrigin || r.Links[0].ChangeStatus != domain.ChangeDraft {
		t.Fatalf("linked: %+v", r)
	}
	if _, err := g.LinkRequest(ctx, r.ID, c.ID, domain.LinkCovers); !errors.Is(err, ErrConflict) {
		t.Fatalf("linked twice: %v", err)
	}
	if _, err := g.LinkRequest(ctx, r.ID, c.ID, "blocks"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unknown role: %v", err)
	}
	// only root changes of its project
	other := w.open(t, "PROJ-B")
	if _, err := g.LinkRequest(ctx, r.ID, other.ID, domain.LinkCovers); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a change of another project: %v", err)
	}
	sub := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "s", ParentID: c.ID}))
	if _, err := g.LinkRequest(ctx, r.ID, sub.ID, domain.LinkCovers); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a sub-change: %v", err)
	}
	// one change answers several requests, one request several changes
	r2 := must[domain.Request](t)(g.CreateRequest(ctx, NewRequest{Title: "Same need", ProjectID: "PROJ-A"}))
	must[domain.Request](t)(g.LinkRequest(ctx, r2.ID, c.ID, domain.LinkCovers))
	c2 := w.open(t, "PROJ-A")
	must[domain.Request](t)(g.LinkRequest(ctx, r.ID, c2.ID, domain.LinkCovers))
	if got := must[[]domain.Request](t)(g.Requests(ctx, domain.RequestFilter{Change: c.ID})); len(got) != 2 {
		t.Fatalf("the requests of a change: %+v", got)
	}
	if got := must[[]domain.Change](t)(g.ListChanges(ctx, ChangesFilter{Request: r.ID})); len(got) != 2 {
		t.Fatalf("the changes of a request: %+v", got)
	}
	// the change records its links
	log, _, err := g.ChangeLog(ctx, domain.LogFilter{Change: c.ID, Types: []string{LogRequestLinked}})
	if err != nil || len(log) != 2 || log[0].Subject != string(r.ID) {
		t.Fatalf("change log: %+v %v", log, err)
	}

	// a move of a change takes the requests linked to it alone; one linked to a change staying behind holds it back
	if _, err := g.MoveChange(ctx, c.ID, "PROJ-B"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a request also linked to a change that stays: %v", err)
	}
	must[domain.Request](t)(g.UnlinkRequest(ctx, r.ID, c2.ID))
	must[domain.Change](t)(g.MoveChange(ctx, c.ID, "PROJ-B"))
	for _, id := range []domain.RequestID{r.ID, r2.ID} {
		if got := must[domain.Request](t)(g.Request(ctx, id)); got.ProjectID != "PROJ-B" {
			t.Fatalf("request %s follows its change: %s", id, got.ProjectID)
		}
	}

	// delivered once every linked change is applied; then closed explicitly
	gone := domain.ChangeAbandoned
	must[domain.Change](t)(g.UpdateChange(ctx, sub.ID, ChangePatch{Status: &gone}))
	must[domain.Baseline](t)(g.Apply(ctx, c.ID, ""))
	got := must[domain.Request](t)(g.Request(ctx, r.ID))
	if got.Effective() != domain.RequestDelivered || got.Status != domain.RequestTriaged {
		t.Fatalf("delivered: %s / %s", got.Effective(), got.Status)
	}
	if list := must[[]domain.Request](t)(g.Requests(ctx, domain.RequestFilter{Statuses: []domain.RequestStatus{domain.RequestDelivered}})); len(list) != 2 {
		t.Fatalf("delivered requests: %+v", list)
	}
	if _, err := g.SetRequestStatus(ctx, r.ID, domain.RequestTriaged, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("not a final status: %v", err)
	}
	closed := must[domain.Request](t)(g.SetRequestStatus(ctx, r.ID, domain.RequestClosed, "works"))
	if closed.Status != domain.RequestClosed {
		t.Fatalf("closed: %+v", closed)
	}
	for name, err := range map[string]error{
		"close again": func() error { _, err := g.SetRequestStatus(ctx, r.ID, domain.RequestWithdrawn, ""); return err }(),
		"rename":      func() error { _, err := g.UpdateRequest(ctx, r.ID, "x"); return err }(),
		"link":        func() error { _, err := g.LinkRequest(ctx, r.ID, other.ID, domain.LinkCovers); return err }(),
	} {
		if !errors.Is(err, ErrConflict) {
			t.Errorf("%s a closed request: %v", name, err)
		}
	}
	entries := must[[]domain.RequestEntry](t)(g.RequestLog(ctx, r.ID))
	var kinds []string
	for _, e := range entries {
		kinds = append(kinds, e.Type)
	}
	want := []string{domain.RequestCreated, domain.RequestLinked, domain.RequestLinked, domain.RequestUnlinked, domain.RequestMoved, domain.RequestStatusSet}
	if len(kinds) != len(want) {
		t.Fatalf("log: %v", kinds)
	}
	for i := range want {
		if kinds[i] != want[i] || entries[i].By != "alice" {
			t.Fatalf("log: %v", kinds)
		}
	}

	// a purged change leaves its requests, unlinked and open again
	r3 := must[domain.Request](t)(g.CreateRequest(ctx, NewRequest{Title: "Third"}))
	tmp := w.open(t, "PROJ-A")
	must[domain.Request](t)(g.LinkRequest(ctx, r3.ID, tmp.ID, domain.LinkOrigin))
	must[domain.Change](t)(g.PurgeChange(ctx, tmp.ID))
	r3 = must[domain.Request](t)(g.Request(ctx, r3.ID))
	if r3.Status != domain.RequestOpen || len(r3.Links) != 0 || r3.ProjectID != "PROJ-A" {
		t.Fatalf("after a purge: %+v", r3)
	}
	last := must[[]domain.RequestEntry](t)(g.RequestLog(ctx, r3.ID))
	var p map[string]any
	_ = json.Unmarshal(last[len(last)-1].Payload, &p)
	if last[len(last)-1].Type != domain.RequestUnlinked || p["reason"] != "purged" {
		t.Fatalf("purge logged: %+v", last)
	}
	if got := must[[]domain.Request](t)(g.Requests(ctx, domain.RequestFilter{Requester: "alice", Projects: []string{"PROJ-A"}})); len(got) != 1 || got[0].ID != r3.ID {
		t.Fatalf("by requester and project: %+v", got)
	}
}
