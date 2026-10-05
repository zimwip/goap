package engine

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/dsl"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/graph/graphtest"
	"github.com/zimwip/goap/pkg/typecat"
)

// A script declares, writes and reviews change impacts; the engine applies the operations.
func TestScriptChangeImpacts(t *testing.T) {
	ctx := context.Background()
	e, g, base := setup(t)
	c, err := g.CreateChange(ctx, graph.NewChange{Namespace: "alm", Title: "PSP v2", BaselineID: base, OwnBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	p := &Process{ChangeID: c.ID}
	job := dsl.Job{Language: "javascript", Action: "plan", Code: `
const req = ctx.impactNode("REQ-1", "PSP v2 changes the payment API");
const tst = ctx.impactNodeCreate("TestCase", "TST-2", "cover the new API");
ctx.writeNode(req, { props: { title: "Use PSP v2" } });
ctx.writeNode(tst, { props: { title: "PSP v2 test" }, links: [{ type: "verifies", to: req }] });
ctx.impactNodeReview(req, true, "confirmed with the PSP team");
`}
	res, err := dsl.Run(ctx, job, nopHost{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Nodes) != 5 || res.Nodes[0].Op != "declare" || res.Nodes[0].Ref != "#n1" || res.Nodes[3].Links[0].To != "#n1" {
		t.Fatalf("unexpected operations: %+v", res.Nodes)
	}
	if _, err := e.applyNodeOps(ctx, p, res.Nodes, "plan", "exec-1"); err != nil {
		t.Fatal(err)
	}
	ch, err := g.Change(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.Nodes) != 2 {
		t.Fatalf("expected 2 change impacts: %+v", ch.Nodes)
	}
	req, tst := ch.Nodes[0], ch.Nodes[1]
	if req.Key != "REQ-1" || req.Post == nil || req.Review != domain.ReviewAccepted || req.ProducedBy != "plan" || req.Execution != "exec-1" {
		t.Fatalf("REQ-1 change impact: %+v", req)
	}
	if tst.Key != "TST-2" || tst.Post == nil || tst.Review != domain.ReviewProposed {
		t.Fatalf("TST-2 change impact: %+v", tst)
	}
	// a created node has no proposal: declared then written in one script, it is one created event, then the edit
	evs, err := g.ChangeEvents(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	var tstOps []domain.ImpactOp
	for _, ev := range evs {
		if ev.Impact == tst.ID {
			tstOps = append(tstOps, ev.Op)
		}
	}
	if len(tstOps) < 2 || tstOps[0] != domain.ImpactCreated || tstOps[1] != domain.ImpactUpdated || slices.Contains(tstOps, domain.ImpactProposed) {
		t.Fatalf("TST-2 log: %v", tstOps)
	}
	// a second script reads the change impacts and accepts the other one
	bb, err := g.Blackboard(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	snap := ChangeImpactsFromBlackboard(bb)
	if len(snap) != 2 || snap[0].Planned || snap[0].Post == nil || snap[0].Post.Key != "REQ-1" {
		t.Fatalf("snapshot: %+v", snap)
	}
	job2 := dsl.Job{Language: "javascript", Action: "check", Nodes: snap, Code: `
for (const n of ctx.changeImpacts()) if (n.review === "proposed") { ctx.impactNodeReview(n.key, true, "needed"); }
`}
	res2, err := dsl.Run(ctx, job2, nopHost{})
	if err != nil || len(res2.Nodes) != 1 {
		t.Fatalf("second script: %+v %v", res2, err)
	}
	if _, err := e.applyNodeOps(ctx, p, res2.Nodes, "check", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Apply(ctx, c.ID, ""); err != nil {
		t.Fatal(err)
	}
	newReq, _ := g.NodeByKey(ctx, "alm", "REQ-1")
	newTst, err := g.NodeByKey(ctx, "alm", "TST-2")
	if err != nil {
		t.Fatal(err)
	}
	v, err := g.View(ctx, newTst.Ref())
	if err != nil || len(v.Out) != 1 || v.Out[0].To != newReq.Ref() || newReq.Properties["title"] != "Use PSP v2" || newReq.ChangeImpact != req.ID {
		t.Fatalf("the change must land with the link on the version it wrote: %+v %+v %v", v.Out, newReq, err)
	}

	// a review without a comment and an unknown key are refused
	for name, tc := range map[string]struct {
		ops []dsl.NodeOp
		msg string
	}{
		"no comment": {[]dsl.NodeOp{{Op: "review", Node: "REQ-1", Accept: true}}, "comment"},
		"unknown":    {[]dsl.NodeOp{{Op: "write", Node: "NOPE-1"}}, "no change impact"},
	} {
		if _, err := e.applyNodeOps(ctx, p, tc.ops, "x", ""); err == nil || !strings.Contains(err.Error(), tc.msg) {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

// A relaunched step redoes its change impacts on a flow branch; adopting it replaces the stale ones.
func TestScriptChangeImpactsOnAFlow(t *testing.T) {
	ctx := context.Background()
	e, g, base := setup(t)
	c, err := g.CreateChange(ctx, graph.NewChange{Namespace: "alm", Title: "PSP v2", BaselineID: base, OwnBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	run := func(p *Process, action, execution, code string) {
		t.Helper()
		res, err := dsl.Run(ctx, dsl.Job{Language: "javascript", Action: action, Code: code}, nopHost{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.applyNodeOps(ctx, p, res.Nodes, action, execution); err != nil {
			t.Fatal(err)
		}
	}
	main := &Process{ChangeID: c.ID}
	run(main, "plan", "run-1", `const r = ctx.impactNode("REQ-1", "first idea"); ctx.writeNode(r, { props: { title: "A" } });`)

	flow, err := g.OpenFlow(ctx, c.ID, graph.OpenFlowRequest{Origin: map[string]any{"step": 0, "execution": "run-1", "reason": "redo"}, StaleRuns: []string{"run-1"}})
	if err != nil {
		t.Fatal(err)
	}
	onFlow := &Process{ChangeID: c.ID, Flow: flow.ID}
	// on the flow the first idea is not there any more: a script sees no change impact and declares its own
	bb, _ := g.BlackboardIn(ctx, c.ID, flow.ID)
	if len(ChangeImpactsFromBlackboard(bb)) != 0 {
		t.Fatalf("a relaunched step starts without its stale change impacts: %+v", ChangeImpactsFromBlackboard(bb))
	}
	run(onFlow, "plan", "run-2", `const r = ctx.impactNode("REQ-1", "second idea"); ctx.writeNode(r, { props: { title: "B" } }); ctx.impactNodeReview(r, true, "better");`)
	bb, _ = g.BlackboardIn(ctx, c.ID, flow.ID)
	if snap := ChangeImpactsFromBlackboard(bb); len(snap) != 1 || snap[0].Rationale != "second idea" || snap[0].Review != "accepted" || snap[0].Post == nil {
		t.Fatalf("flow snapshot: %+v", snap)
	}
	if _, err := g.AdoptFlow(ctx, c.ID, flow.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Apply(ctx, c.ID, ""); err != nil {
		t.Fatal(err)
	}
	req, _ := g.NodeByKey(ctx, "alm", "REQ-1")
	if req.Properties["title"] != "B" || req.Comment != "better" {
		t.Fatalf("the adopted flow lands: %+v", req)
	}
}

type nopHost struct{}

func (nopHost) Complete(context.Context, dsl.CompleteRequest) (dsl.CompleteResult, error) {
	return dsl.CompleteResult{}, nil
}
func (nopHost) RunAgent(context.Context, string, string) (dsl.AgentResult, error) {
	return dsl.AgentResult{}, nil
}
func (nopHost) CallTool(context.Context, string, map[string]any) (any, error) { return nil, nil }
func (nopHost) Node(context.Context, string) (dsl.Node, error)                { return dsl.Node{}, nil }
func (nopHost) Nodes(context.Context, string) ([]dsl.Node, error)             { return nil, nil }
func (nopHost) Links(context.Context, string, string, string) ([]dsl.Link, error) {
	return nil, nil
}

// A script merges and splits nodes from the side of their parents (ADR 0077).
func TestScriptMergeAndSplit(t *testing.T) {
	ctx := context.Background()
	d, err := def.ParseDomain([]byte(`
name: docs
version: 1.0.0
nodeTypes: [{name: Folder}, {name: Item}]
linkTypes: [{name: contains, from: Folder, to: Item, compose: true}]
`))
	if err != nil {
		t.Fatal(err)
	}
	cat, err := typecat.New(d)
	if err != nil {
		t.Fatal(err)
	}
	g := graph.New(graph.NewMemory())
	g.Types = func() graph.TypeCatalog { return cat }
	var items []domain.Node
	for _, k := range []string{"A", "B", "S"} {
		n, err := graphtest.Import(ctx, g, graphtest.Node{Namespace: "docs", Key: k, Type: "docs@Item"})
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, n)
	}
	var links []graph.LinkWrite
	for _, n := range items {
		links = append(links, graph.LinkWrite{Type: "docs@contains", To: n.Ref()})
	}
	if _, err := graphtest.Import(ctx, g, graphtest.Node{Namespace: "docs", Key: "F", Type: "docs@Folder", Links: links}); err != nil {
		t.Fatal(err)
	}
	head, err := g.BranchHead(ctx, "docs", domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	c, err := g.CreateChange(ctx, graph.NewChange{Namespace: "docs", Title: "restructure", BaselineID: head.ID, OwnBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	res, err := dsl.Run(ctx, dsl.Job{Language: "javascript", Action: "restructure", Code: `
const m = ctx.impactNodeMerge(["A", "B"], "Item", "AB", "A and B say one thing");
ctx.writeNode(m, { props: { title: "AB" } });
const parts = ctx.impactNodeSplit("S", [{type: "Item", key: "S1", rationale: "first half"}, {type: "Item", key: "S2", rationale: "second half"}]);
ctx.writeNode(parts[1], { props: { title: "second" } });
`}, nopHost{})
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{Graph: g}
	if _, err := e.applyNodeOps(ctx, &Process{ChangeID: c.ID}, res.Nodes, "restructure", "x1"); err != nil {
		t.Fatal(err)
	}
	list, err := g.ListChangeImpacts(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]domain.ChangeImpact{}
	for _, cn := range list {
		byKey[cn.Key] = cn
	}
	for _, k := range []string{"A", "B", "S"} {
		if byKey[k].Via == "" || byKey[k].Via != byKey["F"].ID {
			t.Errorf("%s is realized through F: via %q", k, byKey[k].Via)
		}
	}
	for _, k := range []string{"AB", "S1", "S2"} {
		n, err := g.Node(ctx, *byKey[k].Post)
		if err != nil || len(n.Origins) == 0 {
			t.Errorf("%s: origins %v, %v", k, n.Origins, err)
		}
	}
	if n, _ := g.Node(ctx, *byKey["AB"].Post); n.Properties["title"] != "AB" {
		t.Errorf("AB was written: %v", n.Properties)
	}
	links2, err := g.OutLinksOf(ctx, *byKey["F"].Post)
	if err != nil || len(links2) != 3 {
		t.Errorf("F links AB, S1, S2: %d, %v", len(links2), err)
	}
}

// A creation declared by a script and never written is created bare when the script ends; written, it is created with
// its properties in one event (ADR 0077).
func TestScriptDeclaredCreation(t *testing.T) {
	ctx := context.Background()
	e, g, base := setup(t)
	c, err := g.CreateChange(ctx, graph.NewChange{Namespace: "alm", Title: "late", BaselineID: base, OwnBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	ops := []dsl.NodeOp{
		{Op: "declare", Ref: "#a", Intent: string(domain.IntentCreated), Key: "TST-3", Type: "TestCase", Rationale: "bare"},
		{Op: "declare", Ref: "#b", Intent: string(domain.IntentCreated), Key: "TST-4", Type: "TestCase", Rationale: "written"},
		{Op: "write", Node: "#b", Props: map[string]any{"title": "four"}},
	}
	if _, err := e.applyNodeOps(ctx, &Process{ChangeID: c.ID}, ops, "plan", "exec-2"); err != nil {
		t.Fatal(err)
	}
	ch, _ := g.Change(ctx, c.ID)
	if len(ch.Nodes) != 2 {
		t.Fatalf("change impacts: %+v", ch.Nodes)
	}
	for _, cn := range ch.Nodes {
		if cn.Intent != domain.IntentCreated || cn.Post == nil {
			t.Fatalf("a creation has its first version: %+v", cn)
		}
	}
	evs, _ := g.ChangeEvents(ctx, c.ID)
	for _, ev := range evs {
		if ev.Op == domain.ImpactProposed || ev.Op == domain.ImpactCheckedOut {
			t.Fatalf("a creation logs no %s event", ev.Op)
		}
	}
	i := slices.IndexFunc(ch.Nodes, func(cn domain.ChangeImpact) bool { return cn.Key == "TST-4" })
	n, err := g.Node(ctx, *ch.Nodes[i].Post)
	if err != nil || n.Properties["title"] != "four" {
		t.Fatalf("the properties of the write: %+v %v", n, err)
	}
}
