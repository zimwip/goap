package graphsvc_test

import (
	"net/http"
	"net/http/httptest"

	"context"
	"errors"
	"github.com/zimwip/goap/gen/goap/graph/v1/graphv1connect"
	"strings"
	"testing"

	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/llmcfg"
)

func aliasOf(t *testing.T, g *graph.Graph, name string) (llmcfg.Alias, domain.Node) {
	t.Helper()
	n, err := g.NodeByKey(context.Background(), domain.NamespacePlatform, llmcfg.AliasKey(name))
	if err != nil {
		t.Fatalf("alias %s: %v", name, err)
	}
	a, err := llmcfg.AliasFromProps(n.Properties)
	if err != nil {
		t.Fatal(err)
	}
	return a, n
}

func modelsWithAliases() graphsvc.ModelsConfig {
	return graphsvc.ModelsConfig{
		Providers: []llmcfg.Provider{{Name: "fake", Kind: "fake", Protocol: "fake", Enabled: true}},
		Models: []llmcfg.Model{
			{Provider: "fake", Model: "echo", Enabled: true},
			{Provider: "fake", Model: "small", Enabled: true},
		},
		Aliases: []llmcfg.Alias{{Alias: "default", Target: "fake/echo"}, {Alias: "fast", Target: "fake/small"}},
	}
}

// Boot gives every install the protected aliases: the assistant like default, the helper like fast; a second boot writes nothing.
func TestBootSeedsProtectedAliases(t *testing.T) {
	ctx := context.Background()
	g := typedGraph(t)
	r, err := graphsvc.Boot(ctx, g, graphsvc.Options{Models: modelsWithAliases()})
	if err != nil || !r.Aliases {
		t.Fatalf("boot = %+v, %v", r, err)
	}
	if a, _ := aliasOf(t, g, llmcfg.AssistantAlias); !a.Protected || a.Target != "fake/echo" {
		t.Fatalf("assistant = %+v", a)
	}
	if a, _ := aliasOf(t, g, llmcfg.HelperAlias); !a.Protected || a.Target != "fake/small" {
		t.Fatalf("helper = %+v", a)
	}
	cs, _ := g.Changes(ctx)
	if again, err := graphsvc.Boot(ctx, g, graphsvc.Options{Models: modelsWithAliases()}); err != nil || again != (graphsvc.Report{}) {
		t.Fatalf("second boot = %+v, %v", again, err)
	}
	if after, _ := g.Changes(ctx); len(after) != len(cs) {
		t.Fatalf("a second boot wrote changes: %d -> %d", len(cs), len(after))
	}
}

// With no model configured the aliases exist, target nothing, and the snapshot keeps them without a problem.
func TestProtectedAliasesWithoutModels(t *testing.T) {
	ctx := context.Background()
	g := typedGraph(t)
	if _, err := graphsvc.Boot(ctx, g, graphsvc.Options{}); err != nil {
		t.Fatal(err)
	}
	a, n := aliasOf(t, g, llmcfg.AssistantAlias)
	if !a.Protected || a.Target != "" {
		t.Fatalf("assistant = %+v", a)
	}
	s := llmcfg.BuildSnapshot("", []domain.Node{n}, nil)
	if len(s.Problems) != 0 || len(s.Aliases) != 1 {
		t.Fatalf("snapshot = %+v", s)
	}
}

// A graph that predates the aliases (models seeded, no protected alias) gets them at the next boot.
func TestProtectedAliasesOnExistingGraph(t *testing.T) {
	ctx := context.Background()
	g := typedGraph(t)
	if err := g.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := modelsWithAliases()
	if _, err := graphsvc.SeedModels(ctx, g, cfg.Providers, cfg.Models, cfg.Aliases); err != nil {
		t.Fatal(err)
	}
	if _, err := g.NodeByKey(ctx, domain.NamespacePlatform, llmcfg.AliasKey(llmcfg.AssistantAlias)); err == nil {
		t.Fatal("alias present before the seed")
	}
	if r, err := graphsvc.Boot(ctx, g, graphsvc.Options{Models: cfg}); err != nil || !r.Aliases {
		t.Fatalf("boot = %+v, %v", r, err)
	}
	if a, _ := aliasOf(t, g, llmcfg.HelperAlias); !a.Protected || a.Target != "fake/small" {
		t.Fatalf("helper = %+v", a)
	}
}

// A change may retarget a protected alias, never retire it, rename it or drop its flag.
func TestProtectedAliasGuard(t *testing.T) {
	ctx := graphsvc.System(context.Background())
	g := typedGraph(t)
	g.Validators = []graph.NodeValidator{llmcfg.ProtectedAliasValidator{}}
	if _, err := graphsvc.Boot(ctx, g, graphsvc.Options{Models: modelsWithAliases()}); err != nil {
		t.Fatal(err)
	}
	edit := func(e graph.NodeEdit) error {
		_, n := aliasOf(t, g, llmcfg.AssistantAlias)
		pre := n.Ref()
		e.Pre = &pre
		return graphsvc.SeedChange(ctx, g, domain.NamespacePlatform, "edit", []graph.NodeEdit{e})
	}
	for name, e := range map[string]graph.NodeEdit{
		"retire":      {State: "retired"},
		"rename":      {Props: map[string]any{"alias": "other"}},
		"flag clear":  {Props: map[string]any{"protected": nil}},
		"flag false":  {Props: map[string]any{"protected": false}},
		"rename+move": {Props: map[string]any{"alias": "x"}, State: "retired"},
	} {
		if err := edit(e); !errors.Is(err, graph.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if err := edit(graph.NodeEdit{Props: map[string]any{"target": "fake/small"}}); err != nil {
		t.Fatalf("retarget: %v", err)
	}
	if a, n := aliasOf(t, g, llmcfg.AssistantAlias); a.Target != "fake/small" || !a.Protected || n.State == "retired" {
		t.Fatalf("assistant = %+v (%s)", a, n.State)
	}
	// a plain alias stays free
	if err := graphsvc.SeedChange(ctx, g, domain.NamespacePlatform, "plain", []graph.NodeEdit{
		graphsvc.SeedNode(llmcfg.AliasKey("mine"), llmcfg.NodeTypeAlias, llmcfg.Alias{Alias: "mine", Target: "fake/echo"}.Props())}); err != nil {
		t.Fatal(err)
	}
	_, n := aliasOf(t, g, "mine")
	pre := n.Ref()
	if err := graphsvc.SeedChange(ctx, g, domain.NamespacePlatform, "retire", []graph.NodeEdit{{Pre: &pre, State: "retired"}}); err != nil {
		t.Fatalf("retire a plain alias: %v", err)
	}
	// the platform names cannot be created unprotected
	g2 := typedGraph(t)
	g2.Validators = g.Validators
	if err := g2.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	err := graphsvc.SeedChange(ctx, g2, domain.NamespacePlatform, "bad", []graph.NodeEdit{
		graphsvc.SeedNode(llmcfg.AliasKey("helper"), llmcfg.NodeTypeAlias, llmcfg.Alias{Alias: "helper", Target: "fake/echo"}.Props())})
	if !errors.Is(err, graph.ErrInvalid) {
		t.Fatalf("unprotected helper: %v", err)
	}
}

// Boot gives every install the built-in terse behaviour, enabled; an administrator's edit or retirement of it is kept
// (the seed never overrides an existing node), and a second boot writes nothing (ADR 0093).
func TestBootSeedsTerseBehaviorOnce(t *testing.T) {
	ctx := context.Background()
	g := typedGraph(t)
	r, err := graphsvc.Boot(ctx, g, graphsvc.Options{Models: modelsWithAliases()})
	if err != nil || !r.Behaviors {
		t.Fatalf("boot = %+v, %v", r, err)
	}
	n, err := g.NodeByKey(ctx, domain.NamespacePlatform, llmcfg.BehaviorKey("terse"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := llmcfg.BehaviorFromProps(n.Properties)
	if err != nil || !b.Enabled || !strings.Contains(b.Instruction, "Answer tersely") {
		t.Fatalf("%+v %v", b, err)
	}
	pre := n.Ref()
	if err := graphsvc.SeedChange(ctx, g, domain.NamespacePlatform, "retire", []graph.NodeEdit{{Pre: &pre, State: llmcfg.StateRetired, Rationale: "r"}}); err != nil {
		t.Fatal(err)
	}
	cs, _ := g.Changes(ctx)
	if again, err := graphsvc.Boot(ctx, g, graphsvc.Options{Models: modelsWithAliases()}); err != nil || again != (graphsvc.Report{}) {
		t.Fatalf("second boot = %+v, %v", again, err)
	}
	if after, _ := g.Changes(ctx); len(after) != len(cs) {
		t.Fatalf("a second boot wrote changes: %d -> %d", len(cs), len(after))
	}
	if n2, _ := g.NodeByKey(ctx, domain.NamespacePlatform, llmcfg.BehaviorKey("terse")); n2.State != llmcfg.StateRetired {
		t.Fatalf("the seed restored a retired behaviour: %q", n2.State)
	}
}

// The domain refuses an alias with no usable target when its draft is accepted or lands (validator alias-needs-target of
// platform@LlmAlias); a protected one may stay unconfigured.
func TestAliasNeedsTarget(t *testing.T) {
	ctx := graphsvc.System(context.Background())
	g := typedGraph(t)
	if err := g.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	land := func(a llmcfg.Alias) error {
		return graphsvc.SeedChange(ctx, g, domain.NamespacePlatform, "alias "+a.Alias, []graph.NodeEdit{
			graphsvc.SeedNode(llmcfg.AliasKey(a.Alias), llmcfg.NodeTypeAlias, a.Props())})
	}
	for _, target := range []string{"", "nomodel", "/m", "p/"} {
		err := land(llmcfg.Alias{Alias: "bad", Target: target})
		if !errors.Is(err, graph.ErrInvalid) || !strings.Contains(err.Error(), "alias bad needs a target (provider/model)") {
			t.Errorf("target %q: err = %v", target, err)
		}
	}
	if err := land(llmcfg.Alias{Alias: "unset", Protected: true}); err != nil {
		t.Errorf("protected without target: %v", err)
	}
	if err := land(llmcfg.Alias{Alias: "good", Target: "p/m"}); err != nil {
		t.Errorf("valid target: %v", err)
	}
}

// The views of a change tell, on the impact itself, that an alias with no usable target would be refused at its review,
// in process and through the service (ImpactValidation); the review gives the same message.
func TestAliasNeedsTargetShownOnTheImpact(t *testing.T) {
	ctx := graphsvc.System(context.Background())
	g := typedGraph(t)
	if err := g.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	path, handler := graphv1connect.NewGraphServiceHandler(&graphsvc.Handler{Graph: g})
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cl := graphsvc.NewClient(srv.Client(), srv.URL)

	c, err := g.CreateChange(ctx, graph.NewChange{ProjectID: "PROJ-ROOT", Namespace: domain.NamespacePlatform, Title: "alias"})
	if err != nil {
		t.Fatal(err)
	}
	cn, err := g.ImpactNodeCreate(ctx, c.ID, graph.NodeCreate{Rationale: "r", Key: llmcfg.AliasKey("bad"), Type: llmcfg.NodeTypeAlias, Properties: llmcfg.Alias{Alias: "bad", Target: "nomodel"}.Props()})
	if err != nil {
		t.Fatal(err)
	}
	_, reviewErr := g.ImpactNodeReview(ctx, c.ID, cn.ID, domain.ReviewAccepted, "r", "ok")
	if !errors.Is(reviewErr, graph.ErrInvalid) {
		t.Fatalf("review: %v", reviewErr)
	}
	want := strings.TrimSuffix(reviewErr.Error(), ": "+graph.ErrInvalid.Error())
	if !strings.Contains(want, "alias bad needs a target (provider/model)") {
		t.Fatalf("message = %q", want)
	}
	got, err := cl.Change(ctx, c.ID)
	if err != nil || len(got.Nodes) != 1 {
		t.Fatalf("client change = %+v, %v", got, err)
	}
	if v := got.Nodes[0].Validation; v == nil || v.Valid || v.Message != want {
		t.Fatalf("through the service: %+v, want %q", v, want)
	}
	bb, err := cl.Blackboard(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v := bb.Change.Nodes[0].Validation; v == nil || v.Valid || v.Message != want {
		t.Fatalf("blackboard through the service: %+v", v)
	}
	// fixed, the next view says so
	if _, err := g.ImpactNodeUpdate(ctx, c.ID, cn.ID, graph.NodeUpdate{Properties: map[string]any{"target": "p/m"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ = cl.Change(ctx, c.ID); got.Nodes[0].Validation == nil || !got.Nodes[0].Validation.Valid {
		t.Fatalf("fixed: %+v", got.Nodes[0].Validation)
	}
}
