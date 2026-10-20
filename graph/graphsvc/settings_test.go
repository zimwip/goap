package graphsvc_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	graphv1 "github.com/zimwip/goap/contracts/gen/goap/graph/v1"
	"github.com/zimwip/goap/contracts/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/graph/graphsvc"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/identity"
)

// The settings dialog stages its edits through the server (ADR 0122): one call per edit or batch of edits, the pending
// state of the caller in every answer, Save and Discard in one call each.
func TestStagedSettings(t *testing.T) {
	ctx := context.Background()
	g := typedGraph(t)
	if err := g.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	h := &graphsvc.Handler{Projects: graphsvc.EveryProject, Authz: authz.AllowAll{}, Graph: g}
	path, handler := graphv1connect.NewGraphServiceHandler(h, connect.WithInterceptors(h.PersonalScope()))
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cl := graphv1connect.NewGraphServiceClient(srv.Client(), srv.URL)
	as := func(subject string, r connect.AnyRequest) {
		identity.SetHeaders(authz.Principal{Subject: subject}, r.Header())
	}

	const ns, typ = "platform", "platform@LlmProvider"
	edit := func(key string, props map[string]any) *graphv1.SettingEdit {
		s, err := structpb.NewStruct(props)
		if err != nil {
			t.Fatal(err)
		}
		return &graphv1.SettingEdit{Namespace: ns, Type: typ, Key: key, Props: s}
	}
	retire := func(key string) *graphv1.SettingEdit {
		return &graphv1.SettingEdit{Namespace: ns, Type: typ, Key: key, Retire: true}
	}
	stage := func(subject string, edits ...*graphv1.SettingEdit) *graphv1.PendingSettingsResponse {
		t.Helper()
		r := connect.NewRequest(&graphv1.StageSettingsRequest{Edits: edits})
		as(subject, r)
		out, err := cl.StageSettings(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		return out.Msg
	}
	pendingOf := func(subject string) *graphv1.PendingSettingsResponse {
		t.Helper()
		r := connect.NewRequest(&graphv1.GetPendingSettingsRequest{})
		as(subject, r)
		out, err := cl.GetPendingSettings(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		return out.Msg
	}
	staged := func(p *graphv1.PendingSettingsResponse, key string) *graphv1.StagedSetting {
		for _, c := range p.Changes {
			for _, n := range c.Nodes {
				if n.Key == key {
					return n
				}
			}
		}
		return nil
	}
	onMain := func(key string) domain.Node {
		t.Helper()
		n, err := g.NodeByKeyOn(ctx, ns, domain.MainBranch, key)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	// a creation then an edit of the same node, in one call: one change, one created node with both edits
	p := stage("alice", edit("LLP:p1", map[string]any{"name": "p1", "kind": "openai", "protocol": "openai"}), edit("LLP:p1", map[string]any{"enabled": false}))
	if len(p.Changes) != 1 || len(p.Changes[0].Nodes) != 1 {
		t.Fatalf("pending = %v", p)
	}
	if s := staged(p, "LLP:p1"); !s.Created || !s.Written || s.Props.AsMap()["enabled"] != false || s.Props.AsMap()["name"] != "p1" {
		t.Fatalf("staged = %v", s)
	}
	// nobody else sees it
	if p := pendingOf("bob"); len(p.Changes) != 0 {
		t.Fatalf("bob sees %v", p)
	}
	if p := pendingOf("alice"); staged(p, "LLP:p1") == nil {
		t.Fatalf("alice reads back %v", p)
	}

	save := connect.NewRequest(&graphv1.SaveSettingsRequest{})
	as("alice", save)
	if out, err := cl.SaveSettings(ctx, save); err != nil || len(out.Msg.Changes) != 0 {
		t.Fatalf("save = %v, %v", out, err)
	}
	if n := onMain("LLP:p1"); n.Properties["enabled"] != false || n.State != "active" {
		t.Fatalf("on main = %+v", n)
	}

	// a retirement, then an edit restores the node
	p = stage("alice", retire("LLP:p1"))
	if s := staged(p, "LLP:p1"); s == nil || !s.Retire || s.Created {
		t.Fatalf("retired = %v", s)
	}
	p = stage("alice", edit("LLP:p1", map[string]any{"enabled": true}))
	if s := staged(p, "LLP:p1"); s == nil || s.Retire || s.Props.AsMap()["enabled"] != true {
		t.Fatalf("restored = %v", s)
	}
	// a node created then retired is given up, the rest stays
	p = stage("alice", edit("LLP:p2", map[string]any{"name": "p2"}), retire("LLP:p2"))
	if staged(p, "LLP:p2") != nil || staged(p, "LLP:p1") == nil {
		t.Fatalf("pending = %v", p)
	}

	// discard: nothing pending, main untouched
	discard := connect.NewRequest(&graphv1.DiscardSettingsRequest{})
	as("alice", discard)
	if out, err := cl.DiscardSettings(ctx, discard); err != nil || len(out.Msg.Changes) != 0 {
		t.Fatalf("discard = %v, %v", out, err)
	}
	if n := onMain("LLP:p1"); n.Properties["enabled"] != false || n.State == access.StateRetired {
		t.Fatalf("on main after discard = %+v", n)
	}

	// an edit put back as it was is not saved: the change goes, commit nothing
	stage("alice", edit("LLP:p1", map[string]any{"enabled": true}))
	stage("alice", edit("LLP:p1", map[string]any{"enabled": false}))
	before, _ := g.BranchHead(ctx, ns, domain.MainBranch)
	save = connect.NewRequest(&graphv1.SaveSettingsRequest{})
	as("alice", save)
	if out, err := cl.SaveSettings(ctx, save); err != nil || len(out.Msg.Changes) != 0 {
		t.Fatalf("save of nothing = %v, %v", out, err)
	}
	if after, _ := g.BranchHead(ctx, ns, domain.MainBranch); after.ID != before.ID {
		t.Fatalf("main moved: %s -> %s", before.ID, after.ID)
	}

	// an anonymous caller has no settings
	anon := connect.NewRequest(&graphv1.GetPendingSettingsRequest{})
	if _, err := cl.GetPendingSettings(ctx, anon); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("anonymous: %v", err)
	}
}
