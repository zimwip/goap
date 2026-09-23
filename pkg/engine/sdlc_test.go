package engine_test

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/intent"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/methodology"
)

// sdlcModel answers the LLM actions of methodologies/sdlc.yaml. The matched
// substrings come from the prompt templates in methodologies/sdlc.yaml.
func sdlcModel(t *testing.T) llm.Client {
	return llm.ClientFunc(func(_ context.Context, req llm.Request) (llm.Response, error) {
		p := req.Messages[0].Content
		var out string
		switch {
		case strings.Contains(p, "DIRECTLY concerned"):
			out = `{"items":[{"kind":"impact","type":"direct","target":"NEED-1","data":{"reason":"new payment method"}},
			{"kind":"impact","type":"direct","target":"REQ-1","data":{"reason":"the PSP must handle split payments"}}]}`
		case strings.Contains(p, "Revise the impacted requirements"):
			out = `{"items":[{"kind":"proposal","proposal":{"op":"update_node","node":{"base":"REQ-1","props":{"title":"Card payment (in full or in 3 installments) goes through the Acme PSP (API v2)"}}}},
			{"ref":"r1","kind":"proposal","proposal":{"op":"create_node","node":{"key":"REQ-10","type":"FunctionalRequirement","props":{"title":"Pay in 3 installments with no fees","priority":"high"}}}},
			{"kind":"proposal","proposal":{"op":"add_link","link":{"type":"satisfies","from":"#r1","to":"NEED-1"}}}]}`
		case strings.Contains(p, "Design the evolution"):
			out = `{"items":[{"ref":"c1","kind":"proposal","proposal":{"op":"create_node","node":{"key":"CMP-10","type":"Component","props":{"title":"installments-engine","technology":"java","version":"0.0.0"}}}},
			{"kind":"proposal","proposal":{"op":"add_link","link":{"type":"implements","from":"#c1","to":"FCT-1"}}},
			{"kind":"artifact","type":"design","data":{"summary":"dedicated installment scheduling engine","decisions":["new Java component"]}}]}`
		case strings.Contains(p, "Write the release note"):
			out = `{"items":[{"kind":"artifact","type":"release_note","data":{"markdown":"# Payment in 3 installments"}}]}`
		default:
			t.Errorf("unexpected prompt:\n%s", p)
		}
		return llm.Response{Text: out, Provider: "test", Model: req.Model}, nil
	})
}

func TestSDLCDelivery(t *testing.T) {
	// an ordinary contributor delivers; a release manager approves the production
	// deployment and an approver applies the change (four-eyes)
	ctx := authz.With(context.Background(), authz.Principal{Subject: "dev", Org: "acme", Roles: []string{"contributor"}})
	rm := authz.With(context.Background(), authz.Principal{Subject: "rm", Org: "acme", Roles: []string{"release_manager"}})
	approver := authz.With(context.Background(), authz.Principal{Subject: "ap", Org: "acme", Roles: []string{"approver"}})
	authorizer, err := authz.NewCasbin(nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../../methodologies/sdlc.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := methodology.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	cm, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	g := graph.New(graph.NewMemory())
	if _, err := graphsvc.SeedDemo(ctx, g); err != nil {
		t.Fatal(err)
	}
	bs, _ := g.Baselines(ctx)
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
	}
	p, err := e.Start(ctx, engine.StartRequest{Methodology: "sdlc", Agent: "delivery", Goal: "deliver", BaselineID: bs[0].ID,
		Title: "Payment in 3 installments", Intent: "Allow payment in 3 installments with no fees"})
	if err != nil {
		t.Fatal(err)
	}
	if p, err = e.Run(ctx, p.ID); err != nil {
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
	count := map[string]int{}
	var decisions []engine.ItemInput
	for _, it := range c.Items {
		if it.Kind == domain.KindProposal {
			decisions = append(decisions, engine.ItemInput{Kind: "decision", Decision: &engine.DecisionInput{Item: "@" + string(it.ID), Accept: true}})
			if it.Proposal.Node != nil && it.Proposal.Op == domain.OpCreateNode {
				count[it.Proposal.Node.Type]++
			}
		}
		if it.Kind == domain.KindArtifact {
			count["artifact:"+it.Type]++
			if it.Type == "design_check" && it.Data["ok"] != true {
				t.Fatalf("design check: %+v", it.Data)
			}
		}
	}
	// CMP-1, CMP-10 (java), CMP-4 (c), CMP-2 (go, generic)
	if count["BuildArtifact"] != 4 || count["artifact:build"] != 4 || count["TestCase"] != 2 || count["FunctionalRequirement"] != 1 {
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
	if types["Release"] != 3 || types["Deployment"] != 9 {
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
		realized = realized || (l.Type == "realizes" && from.ID == byKey["FCT-1"].ID && to.ID == byKey["REQ-10"].ID)
		deployed = deployed || (l.Type == "deploys" && from.ID == byKey["APP-1"].ID && to.ID == byKey["ART-CMP-1-1.4.3"].ID)
		contains = contains || (l.Type == "contains" && from.ID == rel.ID && to.ID == byKey["ART-CMP-4-3.0.2"].ID)
		inProd = inProd || (l.Type == "in_environment" && from.ID == byKey["DEP-REL-APP-1-5.3-ENV-PRD"].ID && to.ID == byKey["ENV-PRD"].ID)
	}
	if !realized || !deployed || !contains || !inProd {
		t.Fatalf("traceability: realized=%v deployed=%v contains=%v inProd=%v", realized, deployed, contains, inProd)
	}
}
