package enginesvc

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	enginev1 "github.com/zimwip/goap/gen/goap/engine/v1"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/graph/graphtest"
	"github.com/zimwip/goap/pkg/methodology"
)

const pointsYAML = `
name: pts
version: 1.0.0
namespace: alm
goal: ship
conditions:
  - {name: drafted, expr: 'artifacts.exists(a, a.type == "draft")'}
  - {name: shipped, expr: 'artifacts.exists(a, a.type == "ship")'}
actions:
  - {name: write, kind: human, effects: {drafted: true}}
  - {name: send, kind: human, pre: {drafted: true}, effects: {shipped: true}}
processes:
  - name: ship
    steps:
      - {name: draft, action: write, description: Write the draft}
      - {name: send, action: send}
`

// denySome refuses the "start" of the methodology named deny.
type denySome struct{ deny string }

func (d denySome) Authorize(_ context.Context, r authz.Request) (bool, error) {
	return !(r.Action == "start" && r.Resource.Name == d.deny), nil
}

func pointsHandler(t *testing.T, a authz.Authorizer) (*Handler, *graph.Graph) {
	t.Helper()
	m, err := methodology.Parse([]byte(pointsYAML))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	g := graph.New(graph.NewMemory())
	ctx := context.Background()
	for _, k := range []string{"PROJ-A", "PROJ-B"} {
		if _, err := graphtest.Project(ctx, g, k, k); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := graphtest.Unit(ctx, g, access.PersonalUnit("alice"), "alice"); err != nil {
		t.Fatal(err)
	}
	e := &engine.Engine{Graph: g, Methodologies: engine.StaticMethodologies{"pts": c}, Store: engine.NewMemoryStore(),
		Executors: map[string]engine.Executor{methodology.KindHuman: engine.HumanExecutor{}}}
	return &Handler{Engine: e, Authz: a}, g
}

func TestListStartingPoints(t *testing.T) {
	ctx := context.Background()
	h, g := pointsHandler(t, denySome{deny: "nope"})
	ch, err := g.CreateChange(ctx, graph.NewChange{Title: "t", Methodology: "pts", Goal: "ship", ProjectID: "PROJ-B"})
	if err != nil {
		t.Fatal(err)
	}
	call := func(ctx context.Context, id string) (*enginev1.ListStartingPointsResponse, error) {
		req := connect.NewRequest(&enginev1.ListStartingPointsRequest{ChangeId: id})
		identity.SetHeaders(authz.From(ctx), req.Header())
		r, err := h.ListStartingPoints(ctx, req)
		if err != nil {
			return nil, err
		}
		return r.Msg, nil
	}
	who := authz.With(ctx, authz.Principal{Subject: "bob", Project: "PROJ-A"})
	out, err := call(who, string(ch.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Points) != 1 || out.Points[0].Id != "ship/draft" || !out.Points[0].MayRun || out.Points[0].Launch.ChangeId != string(ch.ID) ||
		out.Points[0].Launch.Agent != "ship" || out.Points[0].Launch.Goal != "ship/draft" || out.BlockedCount != 1 || out.Goal != "ship" {
		t.Fatalf("%+v", out)
	}

	// the "start" permission is checked on the methodology, on the project of the change
	h2, g2 := pointsHandler(t, denySome{deny: "pts"})
	ch2, _ := g2.CreateChange(ctx, graph.NewChange{Title: "t", Methodology: "pts", Goal: "ship", ProjectID: "PROJ-B"})
	req2 := connect.NewRequest(&enginev1.ListStartingPointsRequest{ChangeId: string(ch2.ID)})
	identity.SetHeaders(authz.From(who), req2.Header())
	if _, err := h2.ListStartingPoints(who, req2); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("start refused: %v", err)
	}

	// a personal change is its subject's only
	pc, err := g.CreateChange(ctx, graph.NewChange{Title: "mine", Methodology: "pts", Goal: "ship", ProjectID: "PROJ-B", OwnerOrg: access.PersonalUnit("alice")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := call(who, string(pc.ID)); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("personal change of another: %v", err)
	}
	if _, err := call(authz.With(ctx, authz.Principal{Subject: "alice"}), string(pc.ID)); err != nil {
		t.Fatalf("personal change of its subject: %v", err)
	}
}
