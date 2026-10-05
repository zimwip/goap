package graphsvc_test

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"

	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/graph/graphtest"
	"github.com/zimwip/goap/pkg/review"
)

// The review object over the RPCs (ADR 0080): open, update, submit, discard; the author or an administrator only; the
// reviewer is the principal of the request; a refusal of the submit leaves the review open.
func TestReviewRPCs(t *testing.T) {
	review.Register()
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	authorizer, err := authz.NewCasbin(nil)
	if err != nil {
		t.Fatal(err)
	}
	floor, err := authz.NewCasbinWith(authz.FloorPolicies)
	if err != nil {
		t.Fatal(err)
	}
	h := &graphsvc.Handler{Graph: g, Authz: authorizer, Floor: floor}
	g.Caller = graphsvc.Caller
	req1, err := graphtest.Import(ctx, g, graphtest.Node{Key: "REQ-1", Type: "Requirement", Properties: map[string]any{"title": "one"}})
	if err != nil {
		t.Fatal(err)
	}
	need, err := graphtest.Import(ctx, g, graphtest.Node{Key: "NEED-1", Type: "Need", Properties: map[string]any{"title": "n"}})
	if err != nil {
		t.Fatal(err)
	}
	base, _ := g.BranchHead(ctx, "", domain.MainBranch)
	c, err := g.CreateChange(ctx, graph.NewChange{Title: "t", BaselineID: base.ID, OwnBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	var imps []string
	for _, n := range []domain.Node{req1, need} {
		pre := n.Ref()
		out, err := g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: "r"}})
		if err != nil {
			t.Fatal(err)
		}
		imps = append(imps, string(out[0].ID))
	}
	as := func(subject, roles string, r interface{ Header() http.Header }) {
		r.Header().Set(identity.HeaderSubject, subject)
		r.Header().Set(identity.HeaderOrg, "acme")
		r.Header().Set(identity.HeaderRoles, roles)
	}
	open := connect.NewRequest(&graphv1.ReviewOpenRequest{ChangeId: string(c.ID), Comment: "global"})
	as("alice", "contributor", open)
	o, err := h.ReviewOpen(ctx, open)
	if err != nil || o.Msg.Review.Status != "open" || o.Msg.Review.By != "alice" {
		t.Fatalf("open: %v %v", o, err)
	}
	key := o.Msg.Review.Key
	upd := func(subject, roles string, m *graphv1.ReviewUpdateRequest) (*graphv1.ReviewRecord, error) {
		m.ChangeId, m.Key = string(c.ID), key
		r := connect.NewRequest(m)
		as(subject, roles, r)
		out, err := h.ReviewUpdate(ctx, r)
		if err != nil {
			return nil, err
		}
		return out.Msg.Review, nil
	}
	rec, err := upd("alice", "contributor", &graphv1.ReviewUpdateRequest{Add: imps, Entries: []*graphv1.ReviewEntryEdit{
		{ChangeImpactId: imps[0], SetOutcome: true, Outcome: "accept", SetComment: true, Comment: "good"},
		{ChangeImpactId: imps[1], SetOutcome: true, Outcome: "reject"}}})
	if err != nil || len(rec.Entries) != 2 || rec.Entries[0].Outcome != "accept" || rec.Entries[0].Comment != "good" {
		t.Fatalf("update: %v %v", rec, err)
	}
	if _, err := upd("mallory", "contributor", &graphv1.ReviewUpdateRequest{SetComment: true, Comment: "x"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("someone else: %v", err)
	}
	if _, err := upd("root", "admin", &graphv1.ReviewUpdateRequest{SetComment: true, Comment: "global (edited)"}); err != nil {
		t.Fatalf("an administrator: %v", err)
	}
	if _, err := upd("alice", "contributor", &graphv1.ReviewUpdateRequest{Add: []string{"nope"}}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown impact: %v", err)
	}
	// a review made elsewhere meanwhile: the submit is refused whole, the review stays open, the error names the entry
	if _, err := g.ImpactNodeReviewOn(ctx, c.ID, "", "", domain.ChangeImpactID(imps[1]), domain.ReviewAccepted, "carol", "meanwhile"); err != nil {
		t.Fatal(err)
	}
	sub := connect.NewRequest(&graphv1.ReviewSubmitRequest{ChangeId: string(c.ID), Key: key})
	as("alice", "contributor", sub)
	if _, err := h.ReviewSubmit(ctx, sub); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("a refused submit: %v", err)
	}
	if ch, _ := g.Change(ctx, c.ID); ch.Nodes[0].Review != domain.ReviewProposed {
		t.Fatalf("all or none: %+v", ch.Nodes[0])
	}
	if _, err := upd("alice", "contributor", &graphv1.ReviewUpdateRequest{Remove: []string{imps[1]}}); err != nil {
		t.Fatal(err)
	}
	sub = connect.NewRequest(&graphv1.ReviewSubmitRequest{ChangeId: string(c.ID), Key: key})
	as("alice", "contributor", sub)
	out, err := h.ReviewSubmit(ctx, sub)
	if err != nil || out.Msg.Review.Status != "submitted" || out.Msg.Review.SubmittedAt == nil {
		t.Fatalf("submit: %v %v", out, err)
	}
	ch, _ := g.Change(ctx, c.ID)
	if r := ch.Nodes[0].Reviews[0]; ch.Nodes[0].Review != domain.ReviewAccepted || r.ReviewID != key || r.By != "alice" {
		t.Fatalf("the impact is reviewed by the submitter in the review: %+v", ch.Nodes[0])
	}
	if got := pbconv.ChangeImpactToPB(ch.Nodes[0]).Reviews[0].ReviewId; got != key {
		t.Fatalf("review id on the wire: %q", got)
	}
	if _, err := upd("alice", "contributor", &graphv1.ReviewUpdateRequest{SetComment: true, Comment: "late"}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("final: %v", err)
	}
	dis := connect.NewRequest(&graphv1.ReviewDiscardRequest{ChangeId: string(c.ID), Key: key})
	as("alice", "contributor", dis)
	if _, err := h.ReviewDiscard(ctx, dis); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("a submitted review is not discarded: %v", err)
	}
	// the batch RPC under it reviews as the principal of the request, and anonymous callers are refused upstream
	bt := connect.NewRequest(&graphv1.ImpactNodeReviewBatchRequest{ChangeId: string(c.ID), ReviewId: "ad-hoc",
		Verdicts: []*graphv1.ImpactVerdict{{ChangeImpactId: imps[1], Accept: false, Comment: "no"}}})
	as("bob", "contributor", bt)
	if _, err := h.ImpactNodeReviewBatch(ctx, bt); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("batch on a reviewed impact: %v", err)
	}
}
