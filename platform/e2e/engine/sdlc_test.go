package enginee2e

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/zimwip/goap/contracts/assess"
	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/decision"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/llm"
	"github.com/zimwip/goap/engine/engine"
	"github.com/zimwip/goap/engine/intent"
	"github.com/zimwip/goap/engine/methodology"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/graph/graph/graphtest"
	"github.com/zimwip/goap/graph/graphsvc"
	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/dsl"
	"github.com/zimwip/goap/lib/typecat"
	"github.com/zimwip/goap/platform/devseed"
	"github.com/zimwip/goap/platform/registrysvc"
)

// sdlcModel answers the LLM actions of methodologies/sdlc.yaml. The matched
// substrings come from the prompt templates in methodologies/sdlc.yaml.
func sdlcModel(t *testing.T) llm.Client {
	return sdlcModelOf(t, new([]string))
}

// between is the text of s between the first from and the next to.
func between(s, from, to string) string {
	_, rest, _ := strings.Cut(s, from)
	out, _, _ := strings.Cut(rest, to)
	return out
}

// sdlcModelOf is sdlcModel recording the impacts it qualified.
func sdlcModelOf(t *testing.T, record *[]string) llm.Client {
	var mu sync.Mutex // the impacts are qualified in parallel
	return llm.ClientFunc(func(_ context.Context, req llm.Request) (llm.Response, error) {
		p := req.Messages[0].Content
		var out string
		switch {
		case strings.Contains(p, `"layer":"requirements"`):
			// the first pass of the impact analysis names what the intent directly concerns
			out = `{"items":[{"type":"changeImpact","changeImpact":{"op":"declare","intent":"modified","key":"NEED-1","rationale":"new payment method"}},
			{"type":"changeImpact","changeImpact":{"op":"declare","intent":"modified","key":"REQ-1","rationale":"the PSP must handle split payments"}},
			{"type":"artifact","target":"impact_pass","data":{"layer":"requirements","summary":"the payment need and its PSP requirement"}}]}`
		case strings.Contains(p, `"target":"impact_pass","data":{"layer":"`):
			// the next passes name nothing directly: the propagation finds their elements
			layer := between(p, `"target":"impact_pass","data":{"layer":"`, `"`)
			out = `{"items":[{"type":"artifact","target":"impact_pass","data":{"layer":"` + layer + `","summary":"nothing beyond what propagates"}}]}`
		case strings.Contains(p, "You qualify ONE suspected impact"):
			// one suspected impact at a time: the settlement batch, its artifact and its settlement file are not concerned
			key, fp := between(p, "Suspected impact: ", " ("), between(p, `"rated":"`, `"`)
			verdict, ops := "confirmed", ""
			if key == "CMP-3" || key == "ART-3" || key == "ITF-3" {
				verdict = "refuted"
				ops = `,{"type":"changeImpact","changeImpact":{"op":"review","node":"` + key + `","accept":false,"comment":"not impacted: settlement is untouched"}},
				{"type":"changeImpact","changeImpact":{"op":"remove","node":"` + key + `"}}`
			}
			mu.Lock()
			*record = append(*record, key)
			mu.Unlock()
			out = `{"items":[{"type":"assessment","data":{"key":"impact:` + key + `","score":4,"verdict":"` + verdict + `","findings":[],"rated":"` + fp + `"}}` + ops + `]}`
		case strings.Contains(p, "Revise the impacted requirements"):
			// REQ-1 starts proposed: a transition moves it to draft, then it is written, the way the
			// system prompt's lifecycle instructions ask of any compliant agent (llmSystem, ADR 0076). REQ-10 is
			// created. The review accepts both, approve_requirements approves them.
			out = `{"items":[{"type":"changeImpact","changeImpact":{"op":"transition","node":"REQ-1","state":"draft"}},
			{"type":"changeImpact","changeImpact":{"op":"write","node":"REQ-1","props":{"title":"Card payment (in full or in 3 installments) goes through the Acme PSP (API v2)"}}},
			{"type":"changeImpact","changeImpact":{"op":"declare","ref":"#r1","intent":"created","type":"FunctionalRequirement","key":"REQ-10","rationale":"pay in installments"}},
			{"type":"changeImpact","changeImpact":{"op":"write","node":"#r1","props":{"title":"Pay in 3 installments with no fees","priority":"high"},"links":[{"type":"satisfies","to":"NEED-1"}]}}]}`
		case strings.Contains(p, "Design the evolution"):
			out = `{"items":[{"type":"changeImpact","changeImpact":{"op":"declare","ref":"#c1","intent":"created","type":"Component","key":"CMP-10","rationale":"a dedicated engine"}},
			{"type":"changeImpact","changeImpact":{"op":"write","node":"#c1","props":{"title":"installments-engine","technology":"java","version":"0.0.0"},"links":[{"type":"implements","to":"FCT-1"}]}},
			{"type":"artifact","target":"design","data":{"summary":"dedicated installment scheduling engine","decisions":["new Java component"]}}]}`
		case strings.Contains(p, "Write the release note"):
			out = `{"items":[{"type":"artifact","target":"release_note","data":{"markdown":"# Payment in 3 installments"}}]}`
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
	m, err := methodology.LoadFile("../../../methodologies/sdlc.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cm, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	// the graph and the engine judge by the types of the repository's domains (ADR 0012)
	ds, err := def.LoadDomains("../../../domains")
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
	// the changes follow the maturity of the alm domain, with the criteria of the gates of sdlc (ADR 0132)
	var maturity *domain.Lifecycle
	for _, d := range ds {
		if lc := d.Lifecycle(m.Lifecycle); lc != nil {
			maturity = lc
		}
	}
	lc, err := registrysvc.WithGates(maturity, cm.Gates())
	if err != nil {
		t.Fatal(err)
	}
	g.Lifecycles = gatedLifecycles{mainGoals{cm}, lc}
	g.DecisionPolicy = decision.Policy{}
	g.Caller = graphsvc.Caller
	if _, err := devseed.Demo(ctx, g); err != nil {
		t.Fatal(err)
	}
	if _, err := graphtest.Project(ctx, g, testProject, "Test"); err != nil {
		t.Fatal(err)
	}
	// the latest baseline, not bs[0] (ADR 0049: SeedDemo imports the organisation and the alm data as a change each,
	// so the head is the one the last of them left).
	head, err := g.BranchHead(ctx, "alm", domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	e := &engine.Engine{
		Graph:         g,
		Methodologies: engine.StaticMethodologies{cm.Definition().Name: cm},
		Executors: map[string]engine.Executor{
			methodology.KindLLM:     engine.LLMExecutor{Client: sdlcModel(t)},
			methodology.KindHuman:   engine.HumanExecutor{},
			methodology.KindScript:  engine.ScriptExecutor{Sandboxes: engine.InprocSandboxes{}},
			methodology.KindBuiltin: engine.DefaultBuiltins(),
		},
		Intent: intent.Resolver{Ranker: intent.Lexical{}},
		Store:  engine.NewMemoryStore(),
		Scope:  engine.AuthzScope{Authz: authorizer},
		Types:  func() def.TypeSet { return cat },
	}
	return e, g, head.ID
}

// boardCtx is a person holding every role of sdlc: the seats of its boards, the attestations of its gates.
var boardCtx = authz.With(context.Background(), authz.Principal{Subject: "board", Org: "acme", Roles: []string{"product_owner", "business_analyst",
	"solution_architect", "tech_lead", "developer", "release_manager", "service_owner", "change_approver"}})

// passGate answers the gate the run is at (ADR 0132): its attestations met, then a go in each seat of its board, until
// the run moves past it. It returns the run as the gate left it.
func passGate(t *testing.T, e *engine.Engine, g *graph.Graph, p *engine.Process) *engine.Process {
	t.Helper()
	m, err := e.Methodologies.Methodology(boardCtx, "sdlc")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; p.Pending != nil && i < 10; i++ {
		action := p.Pending.Action
		switch {
		case strings.HasPrefix(action, "attest:"):
			_, err = e.SubmitForm(boardCtx, p.ID, map[string]any{"verdict": "met", "comment": "checked"})
		case strings.HasSuffix(action, ":rule"):
			path := strings.TrimSuffix(action, ":rule")
			gate, _ := m.GateAt(path)
			bb, err := g.BlackboardIn(boardCtx, p.ChangeID, "")
			if err != nil {
				t.Fatal(err)
			}
			var point domain.DecisionPoint
			for _, d := range domain.DecisionPointsOf(bb) {
				if d.Policy["gate"] == path && d.Status != domain.PointDecided {
					point = d
				}
			}
			ruled := decision.Votes(point)
			for _, seat := range gate.Board {
				if _, done := ruled[seat]; !done {
					_, err = e.SubmitForm(boardCtx, p.ID, map[string]any{"ruling": map[string]any{"point": point.ID, "option": methodology.OptionGo,
						"role": seat, "justification": "ready"}})
					break
				}
			}
		default:
			return p
		}
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		if p, err = e.Run(context.Background(), p.ID); err != nil { // the run acts for its initiator
			t.Fatal(err)
		}
	}
	return p
}

// rateDescription rates the description of a change sdlcSetup starts ("Payment in 3 installments") acceptable, as the
// automatic review of the framing would (ADR 0130).
func rateDescription(t *testing.T, g *graph.Graph, id domain.ChangeID) {
	t.Helper()
	if _, err := g.AddItems(devCtx, id, []domain.ChangeItem{{Type: assess.TypeAssessment, Status: domain.ItemProposed, ProducedBy: "rate_description",
		Data: map[string]any{"key": assess.SubjectDescription, "score": 4, "verdict": assess.VerdictClear,
			"rated": "Payment in 3 installments\n\nAllow payment in 3 installments with no fees"}}}); err != nil {
		t.Fatal(err)
	}
}

// validation is what a person submits to validate the impact analysis.
var validation = engine.ItemInput{Type: "artifact", Target: "impact_validation", Data: map[string]any{"accepted": true, "comment": "the four layers are right"}}

// qualified are the subjects the qualify step of the impact analysis ran for, in order.
func qualified(p *engine.Process) []string {
	var out []string
	for _, s := range p.Steps {
		if s.Subject != "" {
			out = append(out, s.Subject)
		}
	}
	return out
}

// checkImpactAnalysis checks what the four passes of the impact analysis left: the propagated impacts qualified
// once each, the ones the change does not concern rejected with their reason and withdrawn.
func checkImpactAnalysis(t *testing.T, g *graph.Graph, id domain.ChangeID, qualified []string) {
	t.Helper()
	t.Logf("qualified: %v", qualified)
	seen := map[string]bool{}
	for _, k := range qualified {
		if seen[k] {
			t.Fatalf("%s qualified twice: %v", k, qualified)
		}
		seen[k] = true
	}
	for _, k := range []string{"CMP-3", "ART-3", "ITF-3"} {
		if !seen[k] {
			t.Fatalf("%s not qualified: %v", k, qualified)
		}
	}
	c, err := g.Change(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, n := range c.Nodes {
		keys[n.Key] = true
	}
	for _, k := range []string{"NEED-1", "REQ-1", "FCT-1", "APP-1", "CMP-1"} {
		if !keys[k] {
			t.Fatalf("%s is not an impact: %v", k, keys)
		}
	}
	for _, k := range []string{"CMP-3", "ART-3", "ITF-3"} {
		if keys[k] {
			t.Fatalf("%s is not concerned and must be withdrawn", k)
		}
	}
}

func TestSDLCDelivery(t *testing.T) {
	ctx, rm, approver := devCtx, rmCtx, approverCtx
	e, g, base := sdlcSetup(t)
	p, err := e.Start(ctx, engine.StartRequest{Methodology: "sdlc", Agent: "delivery", Goal: "deliver", BaselineID: base,
		Title: "Payment in 3 installments", Intent: "Allow payment in 3 installments with no fees", ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	// the description is rated acceptable (ADR 0110): the framing gate lets the analysis start (ADR 0132)
	rateDescription(t, g, p.ChangeID)
	// run as the background scheduler does, without a caller: the run acts for the initiator
	if p, err = e.Run(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	p = passGate(t, e, g, p)
	// the impact analysis runs its four passes, then a person validates it
	if p.Status != engine.StatusWaiting || p.Pending.Action != "validate_impact_analysis" {
		t.Fatalf("must wait for the validation of the impact analysis: %s %s %+v", p.Status, p.Error, p.Pending)
	}
	checkImpactAnalysis(t, g, p.ChangeID, qualified(p))
	if _, err = e.Submit(ctx, p.ID, []engine.ItemInput{validation}); err != nil {
		t.Fatal(err)
	}
	if p, err = e.Run(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	// the gates the change passes on its way (ADR 0132)
	p = passGate(t, e, g, p)
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
		if (e.Op == domain.ImpactProposed || e.Op.WritesPost()) && e.Execution == "" {
			t.Fatalf("event %d (%s of %s) has no action run", e.Seq, e.Op, e.Impact)
		}
		// a review an action makes is the action's (the verifier is not the producer, ADR 0075)
		if e.By != "dev" && !(e.Op == domain.ImpactReviewed && e.By == "qualify_impact") {
			t.Fatalf("event %d (%s of %s) is recorded for %q, not the initiator", e.Seq, e.Op, e.Impact, e.By)
		}
	}
	count := map[string]int{}
	var decisions []engine.ItemInput
	for _, n := range c.Nodes {
		if n.Review == domain.ReviewProposed {
			// a candidate the run did not modify is taken out of the change (ADR 0102)
			op := &dsl.NodeOp{Op: "review", Node: n.Key, Accept: true, Comment: "reviewed"}
			if n.Post == nil {
				op = &dsl.NodeOp{Op: "remove", Node: n.Key}
			}
			decisions = append(decisions, engine.ItemInput{Type: "changeImpact", ChangeImpact: op})
		}
		if n.Intent == domain.IntentCreated {
			count[n.Type]++
		}
	}
	for _, it := range c.Items {
		if it.Type == domain.TypeArtifact {
			count["artifact:"+it.Target]++
			if it.Target == "design_check" && it.Data["ok"] != true {
				t.Fatalf("design check: %+v", it.Data)
			}
		}
	}
	// CMP-1, CMP-10 (java), CMP-4 (c), CMP-2 (go, generic)
	if count["alm@BuildArtifact"] != 4 || count["artifact:build"] != 4 || count["alm@TestCase"] != 2 || count["alm@FunctionalRequirement"] != 1 {
		t.Fatalf("items: %v", count)
	}
	// the review is a tech lead's: the role of the step refining it (ADR 0139)
	lead := authz.With(context.Background(), authz.Principal{Subject: "lead", Org: "acme", Roles: []string{"tech_lead"}})
	if p, err = e.Submit(lead, p.ID, decisions); err != nil {
		t.Fatal(err)
	}
	// dev, test, staging are deployed; production waits for a release manager
	if p, err = e.Run(ctx, p.ID); err != nil || p.Status != engine.StatusWaiting || p.Pending.Permission != "release:deploy" {
		t.Fatalf("production must wait for a release manager: %v %s %s %+v", err, p.Status, p.Error, p.Pending)
	}
	if _, err := e.Approve(approver, p.ID, true, ""); err == nil {
		t.Fatal("an approver is not a release manager")
	}
	if p, err = e.Approve(rm, p.ID, true, "Thursday window"); err != nil {
		t.Fatal(err)
	}
	// the change itself is applied by an approver (four-eyes), once its gate delivered it
	if p, err = e.Run(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if p = passGate(t, e, g, p); p.Status != engine.StatusWaiting || p.Pending.Permission != "change:apply" {
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
	// the roles of the process's steps, held in the default unit (the one holding the change) or unscoped
	ctx := authz.With(context.Background(), authz.Principal{Subject: "dev", Org: "acme",
		Roles: []string{"contributor", "business_analyst@ORG-DEFAULT", "tech_lead"}})
	e, g, base := sdlcSetup(t)
	p, err := e.Start(ctx, engine.StartRequest{Methodology: "sdlc", Goal: "software_delivery", BaselineID: base,
		Title: "Payment in 3 installments", Intent: "Allow payment in 3 installments with no fees", ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	// the analysis waits for the description to be accepted (ADR 0110): the automatic review of the framing rated it
	// (ADR 0130), so the run does not rate it itself
	if _, err := g.AddItems(ctx, p.ChangeID, []domain.ChangeItem{{Type: assess.TypeAssessment, Status: domain.ItemProposed, ProducedBy: "rate_description",
		Data: map[string]any{"key": assess.SubjectDescription, "score": 4, "verdict": assess.VerdictClear,
			"rated": "Payment in 3 installments\n\nAllow payment in 3 installments with no fees"}}}); err != nil {
		t.Fatal(err)
	}
	if p, err = e.Run(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	// the framing ends on its gate (ADR 0132): the product owner rules its go
	if p.Agent != "software_delivery" || p.Status != engine.StatusWaiting || p.Pending.Action != methodology.RuleAction("software_delivery/framing/confirmation") || p.Pending.Instructions == "" {
		t.Fatalf("the process starts with the confirmation of the framing, its description reviewed: %s %s %+v %s", p.Agent, p.Status, p.Pending, p.Error)
	}
	if c := p.Pending.Context; c == nil || c.Guidance == "" || len(c.Checklist) != 3 || len(c.References) != 1 {
		t.Fatalf("the framing task carries its guidance, checklist and references: %+v", c)
	}
	p = passGate(t, e, g, p)
	if c, _ := g.Change(ctx, p.ChangeID); c.State != "analysing" {
		t.Fatalf("the go of the framing moved the change: %s", c.State)
	}
	// the impacts step names a capability: the impact_analysis method runs its four passes as a sub-agent, then
	// waits for a person to validate them
	if p.Status != engine.StatusWaiting || p.Pending.Kind != engine.TaskAgent || p.Pending.Action != "software_delivery/analysis/impacts" {
		t.Fatalf("expected the impact analysis: %s %s %+v", p.Status, p.Error, p.Pending)
	}
	analysis, err := e.Store.Get(ctx, p.Pending.ChildProcessID)
	if err != nil || analysis.Agent != "impact_analysis" || analysis.Pending == nil || analysis.Pending.Action != "impact_analysis/validation" {
		t.Fatalf("impact analysis %s %s %+v %v", analysis.Status, analysis.Error, analysis.Pending, err)
	}
	var passes []string
	for _, s := range analysis.Steps {
		if len(passes) == 0 || passes[len(passes)-1] != s.Action {
			passes = append(passes, s.Action)
		}
	}
	wantPasses := []string{"impact_analysis/requirements/identify:identify_requirements", "impact_analysis/requirements/propagate", "impact_analysis/requirements/qualify",
		"impact_analysis/functions/identify", "impact_analysis/functions/propagate", "impact_analysis/functions/qualify",
		"impact_analysis/applications/identify", "impact_analysis/applications/propagate", "impact_analysis/applications/qualify",
		"impact_analysis/components/identify", "impact_analysis/components/propagate", "impact_analysis/components/qualify", "impact_analysis/validation"}
	if !slices.Equal(passes, wantPasses) {
		t.Fatalf("passes %v", passes)
	}
	checkImpactAnalysis(t, g, p.ChangeID, qualified(analysis))
	if _, err = e.Submit(ctx, analysis.ID, []engine.ItemInput{validation}); err != nil {
		t.Fatal(err)
	}
	if analysis, err = e.Run(ctx, analysis.ID); err != nil || analysis.Status != engine.StatusCompleted {
		t.Fatalf("impact analysis: %v %s %s", err, analysis.Status, analysis.Error)
	}
	e.Drain()
	if p, err = e.Store.Get(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	// the gates of the analysis, the design and the build (ADR 0132): each ruled by its board, the run goes on
	for i := 0; i < 6 && p.Pending != nil && p.Pending.Action != "software_delivery/release"; i++ {
		p = passGate(t, e, g, p)
		e.Drain()
		if p, err = e.Store.Get(ctx, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	if c, _ := g.Change(ctx, p.ChangeID); c.State != "releasing" {
		t.Fatalf("the gates moved the change to the release: %s", c.State)
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
	want := []string{"software_delivery/framing/confirmation:open", "software_delivery/framing/confirmation:rule", "software_delivery/framing/confirmation",
		"software_delivery/analysis/impacts",
		// the traceability sub-step is skipped: its criteria hold (the new requirement satisfies a need)
		"software_delivery/analysis/specification/requirements:specify_requirements", "software_delivery/analysis/specification/test_plan",
		"attest:software_delivery/analysis/gate/architecture_compliant", "software_delivery/analysis/gate:open", "software_delivery/analysis/gate:rule", "software_delivery/analysis/gate",
		"software_delivery/design", "software_delivery/design_gate:open", "software_delivery/design_gate:rule", "software_delivery/design_gate",
		"software_delivery/build", "software_delivery/build_gate:open", "software_delivery/build_gate:rule", "software_delivery/build_gate",
		"software_delivery/release"}
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
		"software_delivery/analysis/gate": engine.StepDone, "software_delivery/build_gate": engine.StepDone, "software_delivery/delivery_gate": engine.StepTodo,
		// no proof of concept was proposed (ADR 0131): nothing to open, nothing to conclude
		"software_delivery/proofs": engine.StepSkipped,
	} {
		if states[path] != want {
			t.Fatalf("%s is %s, want %s (%v)", path, states[path], want, states)
		}
	}
	// the framing counts its review and its confirmation, the proofs their two steps; the gates of the analysis, the
	// design, the build and the delivery count
	if pr.Total != 16 || pr.Done != 11 {
		t.Fatalf("progress %d/%d", pr.Done, pr.Total)
	}
	// the build step names a capability: no component has a repository, so the recorded_build method was chosen
	// (ADR 0118), and the specializations of its build action apply
	var builds []string
	for _, s := range p.Steps {
		if s.Action != "software_delivery/build" || len(s.Children) == 0 {
			continue
		}
		if s.Specialization != "recorded_build" {
			t.Fatalf("build method: %q", s.Specialization)
		}
		builder, err := e.Store.Get(ctx, s.Children[0])
		if err != nil || builder.Status != engine.StatusCompleted {
			t.Fatalf("build step: %+v %v", builder, err)
		}
		for _, bs := range builder.Steps {
			builds = append(builds, bs.Specialization)
		}
	}
	if !slices.Equal(builds, []string{"build_java", "build_c", "build_generic"}) {
		t.Fatalf("builds %v", builds)
	}
	// the design step names a capability: components are impacted, so the solution_design method was chosen, and its
	// agent generated for the method ran as a sub-agent on the same change, towards its goal
	for _, s := range p.Steps {
		if s.Action != "software_delivery/design" || len(s.Children) == 0 {
			continue
		}
		if s.Specialization != "solution_design" {
			t.Fatalf("design method: %q", s.Specialization)
		}
		architect, err := e.Store.Get(ctx, s.Children[0])
		if err != nil || architect.Agent != "solution_design" || architect.Goal != "solution_design" || architect.ChangeID != p.ChangeID || architect.Status != engine.StatusCompleted ||
			architect.Step == nil || architect.Step.Method != "solution_design" {
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
			// a candidate the run did not modify is taken out of the change (ADR 0102)
			op := &dsl.NodeOp{Op: "review", Node: n.Key, Accept: true, Comment: "reviewed"}
			if n.Post == nil {
				op = &dsl.NodeOp{Op: "remove", Node: n.Key}
			}
			decisions = append(decisions, engine.ItemInput{Type: "changeImpact", ChangeImpact: op})
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
	// back in the delivery: its gate delivers the change (a final state: it may land), then the application waits for
	// an approver (four-eyes)
	if p, err = e.Store.Get(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	p = passGate(t, e, g, p)
	if c, _ := g.Change(ctx, p.ChangeID); c.State != "delivered" {
		t.Fatalf("the delivery gate delivered the change: %s", c.State)
	}
	if p, err = e.Store.Get(ctx, p.ID); err != nil || p.Status != engine.StatusWaiting || p.Pending.Permission != "change:apply" || p.Pending.Action != "software_delivery/application" {
		t.Fatalf("application must wait for an approver: %v %s %+v", err, p.Status, p.Pending)
	}
	// the accountable role of the application step answers for it: a change_approver of the unit may approve the
	// gate, without the approver role the change:apply permission asks for; never on its own change
	if c := p.Pending.Context; c == nil || c.Roles == nil || c.Roles.Accountable != "change_approver" {
		t.Fatalf("the application gate carries its roles: %+v", c)
	}
	own := authz.With(context.Background(), authz.Principal{Subject: "dev", Org: "acme", Roles: []string{"change_approver"}})
	if _, err = e.Approve(own, p.ID, true, ""); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("an accountable role never approves its own change: %v", err)
	}
	accountable := authz.With(context.Background(), authz.Principal{Subject: "ca", Org: "acme", Roles: []string{"change_approver@ORG-DEFAULT"}})
	if _, err = e.Approve(accountable, p.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	if p, err = e.Run(ctx, p.ID); err != nil || p.Status != engine.StatusCompleted {
		t.Fatalf("delivery: %v %s %s", err, p.Status, p.Error)
	}
	if c, _ = g.Change(ctx, p.ChangeID); c.Status != domain.ChangeApplied {
		t.Fatalf("change %s", c.Status)
	}
}
