package engine_test

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/dsl"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/intent"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/methodology"
	"github.com/zimwip/goap/pkg/typecat"
)

// sdlcModel answers the LLM actions of methodologies/sdlc.yaml. The matched
// substrings come from the prompt templates in methodologies/sdlc.yaml.
func sdlcModel(t *testing.T) llm.Client {
	return llm.ClientFunc(func(_ context.Context, req llm.Request) (llm.Response, error) {
		p := req.Messages[0].Content
		var out string
		switch {
		case strings.Contains(p, "DIRECTLY concerned"):
			out = `{"items":[{"kind":"changeImpact","changeImpact":{"op":"declare","intent":"modified","key":"NEED-1","rationale":"new payment method"}},
			{"kind":"changeImpact","changeImpact":{"op":"declare","intent":"modified","key":"REQ-1","rationale":"the PSP must handle split payments"}}]}`
		case strings.Contains(p, "Revise the impacted requirements"):
			out = `{"items":[{"kind":"changeImpact","changeImpact":{"op":"write","node":"REQ-1","props":{"title":"Card payment (in full or in 3 installments) goes through the Acme PSP (API v2)"}}},
			{"kind":"changeImpact","changeImpact":{"op":"declare","ref":"#r1","intent":"created","type":"FunctionalRequirement","key":"REQ-10","rationale":"pay in installments"}},
			{"kind":"changeImpact","changeImpact":{"op":"write","node":"#r1","props":{"title":"Pay in 3 installments with no fees","priority":"high"},"links":[{"type":"satisfies","to":"NEED-1"}]}}]}`
		case strings.Contains(p, "Design the evolution"):
			out = `{"items":[{"kind":"changeImpact","changeImpact":{"op":"declare","ref":"#c1","intent":"created","type":"Component","key":"CMP-10","rationale":"a dedicated engine"}},
			{"kind":"changeImpact","changeImpact":{"op":"write","node":"#c1","props":{"title":"installments-engine","technology":"java","version":"0.0.0"},"links":[{"type":"implements","to":"FCT-1"}]}},
			{"kind":"artifact","type":"design","data":{"summary":"dedicated installment scheduling engine","decisions":["new Java component"]}}]}`
		case strings.Contains(p, "Write the release note"):
			out = `{"items":[{"kind":"artifact","type":"release_note","data":{"markdown":"# Payment in 3 installments"}}]}`
		default:
			t.Errorf("unexpected prompt:\n%s", p)
		}
		return llm.Response{Text: out, Provider: "test", Model: req.Model}, nil
	})
}

// an ordinary contributor delivers; a release manager approves the production
// deployment and an approver applies the change (four-eyes)
var (
	devCtx      = authz.With(context.Background(), authz.Principal{Subject: "dev", Org: "acme", Roles: []string{"contributor"}})
	rmCtx       = authz.With(context.Background(), authz.Principal{Subject: "rm", Org: "acme", Roles: []string{"release_manager"}})
	approverCtx = authz.With(context.Background(), authz.Principal{Subject: "ap", Org: "acme", Roles: []string{"approver"}})
)

// sdlcSetup runs methodologies/sdlc.yaml on the demo ALM repository, with the types of the repository's domains.
func sdlcSetup(t *testing.T) (*engine.Engine, *graph.Graph, domain.BaselineID) {
	t.Helper()
	ctx := devCtx
	authorizer, err := authz.NewCasbin(nil)
	if err != nil {
		t.Fatal(err)
	}
	m, err := methodology.LoadFile("../../methodologies/sdlc.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cm, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	// the graph and the engine judge by the types of the repository's domains (ADR 0012)
	ds, err := methodology.LoadDomains("../../domains")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := typecat.New(ds...)
	if err != nil {
		t.Fatal(err)
	}
	if want := m.Supertypes(); !reflect.DeepEqual(cat.Supertypes()["alm@SecurityRequirement"], want["alm@SecurityRequirement"]) {
		t.Fatalf("catalogue supertypes = %v, want %v", cat.Supertypes(), want)
	}
	g := graph.New(graph.NewMemory())
	g.Types = func() graph.TypeCatalog { return cat }
	g.Caller = graphsvc.Caller
	if _, err := graphsvc.SeedDemo(ctx, g); err != nil {
		t.Fatal(err)
	}
	bs, _ := g.Baselines(ctx, "alm")
	e := &engine.Engine{
		Graph:         g,
		Methodologies: engine.StaticMethodologies{cm.Name: cm},
		Executors: map[string]engine.Executor{
			methodology.KindLLM:     engine.LLMExecutor{Client: sdlcModel(t)},
			methodology.KindHuman:   engine.HumanExecutor{},
			methodology.KindScript:  engine.ScriptExecutor{Sandboxes: engine.InprocSandboxes{}},
			methodology.KindBuiltin: engine.DefaultBuiltins(),
		},
		Intent: intent.Resolver{Ranker: intent.Lexical{}},
		Store:  engine.NewMemoryStore(),
		Authz:  authorizer,
		Types:  func() methodology.TypeSet { return cat },
	}
	return e, g, bs[0].ID
}

func TestSDLCDelivery(t *testing.T) {
	ctx, rm, approver := devCtx, rmCtx, approverCtx
	e, g, base := sdlcSetup(t)
	p, err := e.Start(ctx, engine.StartRequest{Methodology: "sdlc", Agent: "delivery", Goal: "deliver", BaselineID: base,
		Title: "Payment in 3 installments", Intent: "Allow payment in 3 installments with no fees"})
	if err != nil {
		t.Fatal(err)
	}
	// run as the background scheduler does, without a caller: the run acts for the initiator
	if p, err = e.Run(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	if p.Status != engine.StatusWaiting || p.Pending.Action != "review" {
		t.Fatalf("must wait for the review: %s %s %+v", p.Status, p.Error, p.Steps)
	}
	var actions, builds []string
	for _, s := range p.Steps {
		actions = append(actions, s.Action)
		if s.Action == "build" {
			builds = append(builds, s.Specialization)
		}
	}
	t.Logf("steps: %v", actions)
	if !slices.Equal(builds, []string{"build_java", "build_c", "build_generic"}) {
		t.Fatalf("builds %v (steps %v)", builds, actions)
	}
	c, _ := g.Change(ctx, p.ChangeID)
	// every operation of the run on the change impacts is an event linked to its action run (ADR 0029)
	evs, err := g.ChangeEvents(ctx, p.ChangeID)
	if err != nil || len(evs) == 0 {
		t.Fatalf("impact log: %d events, %v", len(evs), err)
	}
	for _, e := range evs {
		if (e.Op == domain.ImpactDeclared || e.Op == domain.ImpactWritten) && e.Execution == "" {
			t.Fatalf("event %d (%s of %s) has no action run", e.Seq, e.Op, e.Impact)
		}
		if e.By != "dev" {
			t.Fatalf("event %d (%s of %s) is recorded for %q, not the initiator", e.Seq, e.Op, e.Impact, e.By)
		}
	}
	count := map[string]int{}
	var decisions []engine.ItemInput
	for _, n := range c.Nodes {
		if n.Review == domain.ReviewProposed {
			decisions = append(decisions, engine.ItemInput{Kind: "changeImpact", ChangeImpact: &dsl.NodeOp{Op: "review", Node: n.Key, Accept: true, Comment: "reviewed"}})
		}
		if n.Intent == domain.IntentCreated {
			count[n.Type]++
		}
	}
	for _, it := range c.Items {
		if it.Kind == domain.KindArtifact {
			count["artifact:"+it.Type]++
			if it.Type == "design_check" && it.Data["ok"] != true {
				t.Fatalf("design check: %+v", it.Data)
			}
		}
	}
	// CMP-1, CMP-10 (java), CMP-4 (c), CMP-2 (go, generic)
	if count["alm@BuildArtifact"] != 4 || count["artifact:build"] != 4 || count["alm@TestCase"] != 2 || count["alm@FunctionalRequirement"] != 1 {
		t.Fatalf("items: %v", count)
	}
	if p, err = e.Submit(ctx, p.ID, decisions); err != nil {
		t.Fatal(err)
	}
	// dev, test, staging are deployed; production waits for a release manager
	if p, err = e.Run(ctx, p.ID); err != nil || p.Status != engine.StatusWaiting || p.Pending.Permission != "release:deploy" {
		t.Fatalf("production must wait for a release manager: %v %s %+v", err, p.Status, p.Pending)
	}
	if _, err := e.Approve(approver, p.ID, true, ""); err == nil {
		t.Fatal("an approver is not a release manager")
	}
	if p, err = e.Approve(rm, p.ID, true, "Thursday window"); err != nil {
		t.Fatal(err)
	}
	// the change itself is applied by an approver (four-eyes)
	if p, err = e.Run(ctx, p.ID); err != nil || p.Status != engine.StatusWaiting || p.Pending.Permission != "change:apply" {
		t.Fatalf("apply must wait for an approver: %v %s %+v", err, p.Status, p.Pending)
	}
	if p, err = e.Approve(approver, p.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	if p, err = e.Run(ctx, p.ID); err != nil || p.Status != engine.StatusCompleted {
		t.Fatalf("delivery: %v %s %s", err, p.Status, p.Error)
	}
	var deploys []string
	for _, s := range p.Steps {
		if s.Action == "deploy" {
			deploys = append(deploys, s.Specialization)
		}
	}
	if !slices.Equal(deploys, []string{"deploy_auto", "deploy_auto", "deploy_staging", "deploy_production"}) {
		t.Fatalf("deployment waves: %v", deploys)
	}
	c, _ = g.Change(ctx, p.ChangeID)
	nodes, links, err := g.BaselineGraph(ctx, c.ResultBaselineID)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]domain.Node{}
	types := map[string]int{}
	for _, n := range nodes {
		byKey[n.Key] = n
		types[n.Type]++
	}
	// releases of APP-1 (CMP-1, CMP-4) and APP-2 (CMP-2), each on 4 environments (+1 seeded deployment)
	if types["alm@Release"] != 3 || types["alm@Deployment"] != 9 {
		t.Fatalf("releases / deployments: %v", types)
	}
	art := byKey["ART-CMP-10-0.0.1"]
	if art.Properties["coordinates"] != "com.acme:installments-engine:0.0.1" {
		t.Fatalf("java artifact: %+v", art)
	}
	rel := byKey["REL-APP-1-5.3"]
	var realized, deployed, contains, inProd bool
	for _, l := range links {
		from, to := l.From, l.To
		realized = realized || (l.Type == "alm@realizes" && from.ID == byKey["FCT-1"].ID && to.ID == byKey["REQ-10"].ID)
		deployed = deployed || (l.Type == "alm@deploys" && from.ID == byKey["APP-1"].ID && to.ID == byKey["ART-CMP-1-1.4.3"].ID)
		contains = contains || (l.Type == "alm@contains" && from.ID == rel.ID && to.ID == byKey["ART-CMP-4-3.0.2"].ID)
		inProd = inProd || (l.Type == "alm@in_environment" && from.ID == byKey["DEP-REL-APP-1-5.3-ENV-PRD"].ID && to.ID == byKey["ENV-PRD"].ID)
	}
	if !realized || !deployed || !contains || !inProd {
		t.Fatalf("traceability: realized=%v deployed=%v contains=%v inProd=%v", realized, deployed, contains, inProd)
	}
}

// TestSDLCProcess runs the change through the software_delivery process of the SDLC (ADR 0034): a manual framing
// step, the analysis sub-steps done by actions or their alternatives, the design by the architect agent, the build,
// then the release_train process nested as a sub-agent, and the application.
func TestSDLCProcess(t *testing.T) {
	ctx := devCtx
	e, g, base := sdlcSetup(t)
	p, err := e.Start(ctx, engine.StartRequest{Methodology: "sdlc", Goal: "software_delivery", BaselineID: base,
		Title: "Payment in 3 installments", Intent: "Allow payment in 3 installments with no fees"})
	if err != nil {
		t.Fatal(err)
	}
	if p, err = e.Run(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	if p.Agent != "software_delivery" || p.Status != engine.StatusWaiting || p.Pending.Action != "software_delivery/framing" || p.Pending.Instructions == "" {
		t.Fatalf("the process starts with its manual framing step: %s %s %+v %s", p.Agent, p.Status, p.Pending, p.Error)
	}
	if c := p.Pending.Context; c == nil || c.Guidance == "" || len(c.Checklist) != 3 || len(c.References) != 1 {
		t.Fatalf("the framing task carries its guidance, checklist and references: %+v", c)
	}
	if _, err = e.Submit(ctx, p.ID, nil); err != nil {
		t.Fatal(err)
	}
	if p, err = e.Run(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	// the nested release process waits for the review
	if p.Status != engine.StatusWaiting || p.Pending.Kind != engine.TaskAgent || p.Pending.Action != "software_delivery/release" {
		t.Fatalf("expected the release train: %s %s %+v", p.Status, p.Error, p.Pending)
	}
	var steps []string
	for _, s := range p.Steps {
		if len(steps) == 0 || steps[len(steps)-1] != s.Action {
			steps = append(steps, s.Action)
		}
	}
	want := []string{"software_delivery/framing", "software_delivery/analysis/scope:identify_scope", "software_delivery/analysis/impacts",
		// the traceability sub-step is skipped: its criteria hold (the new requirement satisfies a need)
		"software_delivery/analysis/specification/requirements:specify_requirements", "software_delivery/analysis/specification/test_plan", "software_delivery/design", "software_delivery/build", "software_delivery/release"}
	if !slices.Equal(steps, want) {
		t.Fatalf("steps %v", steps)
	}
	// where the delivery stands while the release train works
	pr, err := e.Progress(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	var walk func([]engine.StepProgress)
	walk = func(steps []engine.StepProgress) {
		for _, s := range steps {
			states[s.Path] = s.State
			walk(s.Steps)
		}
	}
	walk(pr.Steps)
	for path, want := range map[string]string{
		"software_delivery/framing": engine.StepDone, "software_delivery/analysis": engine.StepDone,
		"software_delivery/analysis/specification/traceability": engine.StepSkipped, "software_delivery/design": engine.StepDone,
		"software_delivery/build": engine.StepDone, "software_delivery/release": engine.StepActive, "software_delivery/application": engine.StepTodo,
	} {
		if states[path] != want {
			t.Fatalf("%s is %s, want %s (%v)", path, states[path], want, states)
		}
	}
	if pr.Total != 10 || pr.Done != 8 {
		t.Fatalf("progress %d/%d", pr.Done, pr.Total)
	}
	var builds []string
	for _, s := range p.Steps {
		if s.Action == "software_delivery/build" {
			builds = append(builds, s.Specialization) // the specializations of the action a step runs apply
		}
	}
	if !slices.Equal(builds, []string{"build_java", "build_c", "build_generic"}) {
		t.Fatalf("builds %v", builds)
	}
	// the design step ran the architect as a sub-agent on the same change, towards its goal
	for _, s := range p.Steps {
		if s.Action != "software_delivery/design" || len(s.Children) == 0 {
			continue
		}
		architect, err := e.Store.Get(ctx, s.Children[0])
		if err != nil || architect.Agent != "architect" || architect.Goal != "design" || architect.ChangeID != p.ChangeID || architect.Status != engine.StatusCompleted {
			t.Fatalf("design step: %+v %v", architect, err)
		}
	}
	train, err := e.Store.Get(ctx, p.Pending.ChildProcessID)
	if err != nil || train.Agent != "release_train" || train.ParentID != p.ID || train.Pending == nil || train.Pending.Action != "release_train/review" {
		t.Fatalf("release train %+v %v", train, err)
	}
	c, _ := g.Change(ctx, p.ChangeID)
	var decisions []engine.ItemInput
	for _, n := range c.Nodes {
		if n.Review == domain.ReviewProposed {
			decisions = append(decisions, engine.ItemInput{Kind: "changeImpact", ChangeImpact: &dsl.NodeOp{Op: "review", Node: n.Key, Accept: true, Comment: "reviewed"}})
		}
	}
	if _, err = e.Submit(ctx, train.ID, decisions); err != nil {
		t.Fatal(err)
	}
	if train, err = e.Run(ctx, train.ID); err != nil || train.Pending == nil || train.Pending.Permission != "release:deploy" {
		t.Fatalf("production must wait for a release manager: %v %+v", err, train)
	}
	if _, err = e.Approve(rmCtx, train.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	if train, err = e.Run(ctx, train.ID); err != nil || train.Status != engine.StatusCompleted {
		t.Fatalf("release train: %v %s %s", err, train.Status, train.Error)
	}
	e.Drain()
	// back in the delivery: the application waits for an approver (four-eyes)
	if p, err = e.Store.Get(ctx, p.ID); err != nil || p.Status != engine.StatusWaiting || p.Pending.Permission != "change:apply" || p.Pending.Action != "software_delivery/application" {
		t.Fatalf("application must wait for an approver: %v %s %+v", err, p.Status, p.Pending)
	}
	if _, err = e.Approve(approverCtx, p.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	if p, err = e.Run(ctx, p.ID); err != nil || p.Status != engine.StatusCompleted {
		t.Fatalf("delivery: %v %s %s", err, p.Status, p.Error)
	}
	if c, _ = g.Change(ctx, p.ChangeID); c.Status != domain.ChangeApplied {
		t.Fatalf("change %s", c.Status)
	}
}
