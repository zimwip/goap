package graphsvc_test

import (
	"context"
	"encoding/json"
	"testing"

	"connectrpc.com/connect"

	graphv1 "github.com/zimwip/goap/gen/goap/graph/v1"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
)

func TestLifecycleIsEnforcedByTheService(t *testing.T) {
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	authorizer, err := authz.NewCasbin(nil)
	if err != nil {
		t.Fatal(err)
	}
	g.Authorizer = graphsvc.TransitionAuthorizer(authorizer)
	h := &graphsvc.Handler{Graph: g, Authz: authorizer}

	lc := domain.Lifecycle{Initial: "released",
		States: []domain.LifecycleState{{Name: "draft", Editable: true}, {Name: "released"}},
		Transitions: []domain.Transition{
			{Name: "reopen", From: "released", To: "draft"},
			{Name: "release", From: "draft", To: "released", Permission: "requirement:release"},
		}}
	raw, _ := json.Marshal(lc)
	var lcMap map[string]any
	_ = json.Unmarshal(raw, &lcMap)
	nt, err := g.CreateNode(ctx, graph.NewNode{Key: "D:x/nodetype/Req", Type: graph.NodeTypeNode, Properties: map[string]any{"name": "Req", "lifecycle": lcMap}})
	if err != nil {
		t.Fatal(err)
	}
	req, err := g.CreateNode(ctx, graph.NewNode{Key: "REQ-1", Type: "Req", Properties: map[string]any{"title": "a"}, State: "released"})
	if err != nil {
		t.Fatal(err)
	}
	note, _ := g.CreateNode(ctx, graph.NewNode{Key: "N-1", Type: "Note"})
	base, err := g.CreateBaseline(ctx, "B", []domain.NodeRef{nt.Ref(), req.Ref(), note.Ref()})
	if err != nil {
		t.Fatal(err)
	}
	// the bypass is closed for lifecycle types, open for the others
	if _, err := h.CreateNode(ctx, connect.NewRequest(&graphv1.CreateNodeRequest{Key: "REQ-2", Type: "Req"})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("CreateNode on a lifecycle type: %v", err)
	}
	if _, err := h.UpdateNode(ctx, connect.NewRequest(&graphv1.UpdateNodeRequest{Base: pbconv.RefToPB(req.Ref()), Props: pbconv.Struct(map[string]any{"title": "b"})})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("UpdateNode on a lifecycle type: %v", err)
	}
	if _, err := h.CreateLink(ctx, connect.NewRequest(&graphv1.CreateLinkRequest{Type: "x", From: pbconv.RefToPB(req.Ref()), To: pbconv.RefToPB(note.Ref())})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("CreateLink from a lifecycle node: %v", err)
	}
	if _, err := h.CreateNode(ctx, connect.NewRequest(&graphv1.CreateNodeRequest{Key: "N-2", Type: "Note"})); err != nil {
		t.Errorf("a type without lifecycle stays writable: %v", err)
	}

	// reopen, edit, release: "release" needs requirement:release
	c, err := g.CreateChange(ctx, graph.NewChange{Title: "t", BaselineID: base.ID})
	if err != nil {
		t.Fatal(err)
	}
	ref := req.Ref()
	withRoles := func(hdr interface{ Set(k, v string) }, roles string) {
		hdr.Set(identity.HeaderSubject, "u")
		hdr.Set(identity.HeaderOrg, "acme")
		hdr.Set(identity.HeaderRoles, roles)
	}
	ar := connect.NewRequest(&graphv1.AddChangeImpactsRequest{ChangeId: string(c.ID),
		Nodes: []*graphv1.ChangeImpact{{Intent: "modified", Pre: pbconv.RefToPB(ref), Rationale: "release"}}})
	withRoles(ar.Header(), "contributor")
	added, err := h.AddChangeImpacts(ctx, ar)
	if err != nil {
		t.Fatalf("anyone may propose the release: %v", err)
	}
	cnID := added.Msg.Nodes[0].Id
	write := func(props map[string]any, state string) {
		t.Helper()
		wr := connect.NewRequest(&graphv1.WriteChangeImpactRequest{ChangeId: string(c.ID), ChangeImpactId: cnID, Props: pbconv.Struct(props), State: state})
		withRoles(wr.Header(), "contributor")
		if _, err := h.WriteChangeImpact(ctx, wr); err != nil {
			t.Fatalf("a contributor may reopen, edit and propose the release: %v", err)
		}
	}
	write(nil, "draft")
	write(map[string]any{"title": "b"}, "")
	write(nil, "released")
	rv := connect.NewRequest(&graphv1.ReviewChangeImpactRequest{ChangeId: string(c.ID), ChangeImpactId: cnID, Accept: true, Comment: "ok"})
	withRoles(rv.Header(), "contributor")
	if _, err := h.ReviewChangeImpact(ctx, rv); err != nil {
		t.Fatal(err)
	}
	// the transition is authorized for whoever applies the change
	r := connect.NewRequest(&graphv1.ApplyChangeRequest{ChangeId: string(c.ID)})
	withRoles(r.Header(), "contributor")
	if _, err := h.ApplyChange(ctx, r); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a contributor lacks requirement:release: %v", err)
	}
	r = connect.NewRequest(&graphv1.ApplyChangeRequest{ChangeId: string(c.ID)})
	withRoles(r.Header(), "admin")
	if _, err := h.ApplyChange(ctx, r); err != nil {
		t.Fatalf("admin may release: %v", err)
	}
	if n, err := g.NodeByKey(ctx, "", "REQ-1"); err != nil || n.State != "released" || n.Version != 4 || n.Properties["title"] != "b" {
		t.Fatalf("REQ-1: %+v %v", n, err)
	}
}
