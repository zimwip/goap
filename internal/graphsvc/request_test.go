package graphsvc_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
)

// Requests over RPC (ADR 0098): anyone signed in asks; an untriaged request is seen by its requester and the triagers
// only; once linked to a change it takes its project, whose members see it, link and reject it; the requester closes it.
func TestRequestsThroughTheService(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	authorizer, err := authz.NewCasbin(nil)
	if err != nil {
		t.Fatal(err)
	}
	h := &graphsvc.Handler{Graph: g, Authz: authorizer}
	head := func(r connect.AnyRequest, subject, roles string) {
		r.Header().Set(identity.HeaderSubject, subject)
		r.Header().Set(identity.HeaderOrg, "acme")
		r.Header().Set(identity.HeaderRoles, roles)
	}

	create := connect.NewRequest(&graphv1.CreateRequestRequest{Title: "Faster checkout", Text: "slow", OriginKind: domain.OriginManual})
	head(create, "alice", "")
	out, err := h.CreateRequest(ctx, create)
	if err != nil {
		t.Fatal(err)
	}
	id := out.Msg.Request.Id
	if out.Msg.Request.Requester != "alice" || out.Msg.Request.Status != string(domain.RequestOpen) {
		t.Fatalf("created: %+v", out.Msg.Request)
	}
	get := func(subject, roles string) error {
		r := connect.NewRequest(&graphv1.GetRequestRequest{RequestId: id})
		head(r, subject, roles)
		_, err := h.GetRequest(ctx, r)
		return err
	}
	list := func(subject, roles string) int {
		r := connect.NewRequest(&graphv1.ListRequestsRequest{})
		head(r, subject, roles)
		res, err := h.ListRequests(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		return len(res.Msg.Requests)
	}
	// untriaged: the requester and the triagers
	if err := get("alice", ""); err != nil {
		t.Fatalf("the requester: %v", err)
	}
	if err := get("triager", "triage"); err != nil {
		t.Fatalf("a triager: %v", err)
	}
	if err := get("bob", "developer"); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("a member of a project, the request untriaged: %v", err)
	}
	if list("bob", "developer") != 0 || list("alice", "") != 1 {
		t.Fatal("the list holds what the caller may view")
	}
	// a triager links it to a change: it takes the project, whose members see it
	base, err := g.BranchHead(ctx, "", domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	c, err := g.CreateChange(ctx, graph.NewChange{ProjectID: "PROJ-ROOT", Title: "c", BaselineID: base.ID})
	if err != nil {
		t.Fatal(err)
	}
	link := connect.NewRequest(&graphv1.LinkRequestRequest{RequestId: id, ChangeId: string(c.ID), Role: string(domain.LinkOrigin)})
	head(link, "bob", "developer")
	if _, err := h.LinkRequest(ctx, link); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("a member links an untriaged request: %v", err)
	}
	head(link, "triager", "triage")
	linked, err := h.LinkRequest(ctx, link)
	if err != nil || linked.Msg.Request.ProjectId != "PROJ-ROOT" || linked.Msg.Request.Status != string(domain.RequestTriaged) || len(linked.Msg.Request.Links) != 1 {
		t.Fatalf("linked: %+v %v", linked, err)
	}
	if err := get("bob", "developer"); err != nil {
		t.Fatalf("a member of the project, the request triaged: %v", err)
	}
	// closing is the requester's (or a triager's)
	closeAs := func(subject, roles string) error {
		r := connect.NewRequest(&graphv1.SetRequestStatusRequest{RequestId: id, Status: string(domain.RequestClosed), Comment: "done"})
		head(r, subject, roles)
		_, err := h.SetRequestStatus(ctx, r)
		return err
	}
	if err := closeAs("bob", "developer"); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a member closes: %v", err)
	}
	if err := closeAs("alice", ""); err != nil {
		t.Fatalf("the requester closes: %v", err)
	}
	logReq := connect.NewRequest(&graphv1.ListRequestLogRequest{RequestId: id})
	head(logReq, "alice", "")
	entries, err := h.ListRequestLog(ctx, logReq)
	if err != nil || len(entries.Msg.Entries) != 3 {
		t.Fatalf("log: %+v %v", entries, err)
	}
}
