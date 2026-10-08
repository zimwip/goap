package graph

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/typecat"
)

const objectsDomain = `
name: docs
version: "1"
enums: [{name: level, values: [low, high]}]
lifecycles:
  - name: risk
    initial: open
    states: [{name: open}, {name: closed, final: true}]
    transitions:
      - {name: close, from: open, to: closed, requires: {attributes: [resolution]}, guard: 'node.props.level != "high" || has(node.props.waiver)'}
nodeTypes:
  - {name: Req, attributes: [title]}
changeObjectTypes:
  - name: Risk
    key: {kind: sequence, prefix: RISK}
    lifecycle: risk
    attributes: [{name: title}, {name: level, type: enum, enum: level}, {name: resolution}, {name: waiver}, {name: score, type: number}]
  - {name: Owner, key: {kind: natural, attributes: [role]}, attributes: [role, who]}
  - {name: Threshold, key: {kind: singleton}, attributes: [{name: value, type: number}]}
  - {name: Note, key: {kind: ref, ref: impact}, attributes: [text]}
  - {name: Mitigation, key: {kind: ref, ref: Risk}, attributes: [plan]}
  - {name: Hypothesis, key: {kind: singleton}, scope: workspace, attributes: [text]}
  - {name: Free, key: {kind: singleton}, additionalProperties: true}
`

func objectsGraph(t *testing.T, repo Repo) (*Graph, domain.Change) {
	t.Helper()
	ctx := context.Background()
	d, err := def.ParseDomain([]byte(objectsDomain))
	if err != nil {
		t.Fatal(err)
	}
	cat, err := typecat.New(d)
	if err != nil {
		t.Fatal(err)
	}
	g := New(repo)
	g.Types = func() TypeCatalog { return cat }
	g.Caller = func(context.Context) string { return "alice" }
	head := must[domain.Baseline](t)(g.BranchHead(ctx, "docs", domain.MainBranch))
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Namespace: "docs", Title: "objects", BaselineID: head.ID}))
	return g, c
}

func TestChangeObjects(t *testing.T) {
	forEachRepo(t, func(t *testing.T, repo Repo) {
		ctx := context.Background()
		g, c := objectsGraph(t, repo)
		put := func(w ...domain.ObjectWrite) []domain.ChangeObject {
			t.Helper()
			return must[[]domain.ChangeObject](t)(g.PutObjects(ctx, c.ID, w))
		}

		// a sequence allocates its keys, the first write puts the change object in the initial state
		r := put(domain.ObjectWrite{Type: "docs@Risk", Value: map[string]any{"title": "late", "level": "high"}, Labels: map[string]string{"step": "s1"}},
			domain.ObjectWrite{Type: "docs@Risk", Value: map[string]any{"title": "cost"}})
		if r[0].Key != "RISK-1" || r[1].Key != "RISK-2" || r[0].State != "open" || r[0].Version != 1 || r[0].By != "alice" || r[0].Seq == 0 {
			t.Fatalf("risks: %+v", r)
		}
		if got := must[domain.Change](t)(g.Change(ctx, c.ID)); got.Status != domain.ChangeActive {
			t.Fatalf("a change holding change objects is active: %s", got.Status)
		}
		// a new version: merged, a nil removes a property; the transition checks its required attributes and its guard
		if _, err := g.PutObjects(ctx, c.ID, []domain.ObjectWrite{{Type: "docs@Risk", Key: "RISK-1", Transition: "close", Merge: true}}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("a transition requiring an attribute: %v", err)
		}
		if _, err := g.PutObjects(ctx, c.ID, []domain.ObjectWrite{{Type: "docs@Risk", Key: "RISK-1", Merge: true, Transition: "close", Value: map[string]any{"resolution": "x"}}}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("a transition whose guard does not hold: %v", err)
		}
		v2 := put(domain.ObjectWrite{Type: "docs@Risk", Key: "RISK-1", Merge: true, Transition: "close", Value: map[string]any{"resolution": "x", "waiver": "w", "title": nil}})[0]
		if v2.Version != 2 || v2.State != "closed" || v2.Value["title"] != nil || v2.Value["level"] != "high" {
			t.Fatalf("version 2: %+v", v2)
		}
		// keys and values are checked against the type
		for name, w := range map[string]domain.ObjectWrite{
			"unknown type":        {Type: "docs@Nope"},
			"unknown attribute":   {Type: "docs@Risk", Value: map[string]any{"color": "red"}},
			"outside its enum":    {Type: "docs@Risk", Value: map[string]any{"level": "huge"}},
			"not a number":        {Type: "docs@Risk", Value: map[string]any{"score": "a lot"}},
			"a singleton key":     {Type: "docs@Threshold", Key: "x"},
			"a natural key unset": {Type: "docs@Owner", Value: map[string]any{"who": "bob"}},
			"a ref key missing":   {Type: "docs@Note"},
			"no lifecycle":        {Type: "docs@Threshold", Transition: "close"},
			"a bad label":         {Type: "docs@Threshold", Labels: map[string]string{"a b": "x"}},
		} {
			if _, err := g.PutObjects(ctx, c.ID, []domain.ObjectWrite{w}); !errors.Is(err, ErrInvalid) {
				t.Errorf("%s: %v", name, err)
			}
		}
		for name, w := range map[string]domain.ObjectWrite{
			"an unallocated sequence key": {Type: "docs@Risk", Key: "RISK-9"},
			"an unknown impact":           {Type: "docs@Note", Key: "nope"},
			"an unknown referenced risk":  {Type: "docs@Mitigation", Key: "RISK-9"},
			"an unknown workspace":        {Type: "docs@Hypothesis", Workspace: "nope"},
		} {
			if _, err := g.PutObjects(ctx, c.ID, []domain.ObjectWrite{w}); !errors.Is(err, ErrNotFound) {
				t.Errorf("%s: %v", name, err)
			}
		}
		// a failed batch writes nothing
		if _, err := g.PutObjects(ctx, c.ID, []domain.ObjectWrite{{Type: "docs@Threshold", Value: map[string]any{"value": 3}}, {Type: "docs@Nope"}}); err == nil {
			t.Fatal("a batch with an invalid write")
		}
		if got := must[[]domain.ChangeObject](t)(g.Objects(ctx, c.ID, domain.ObjectFilter{Types: []string{"docs@Threshold"}})); len(got) != 0 {
			t.Fatalf("a failed batch wrote %+v", got)
		}
		// natural, singleton, ref keys
		o := put(domain.ObjectWrite{Type: "docs@Owner", Value: map[string]any{"role": "qa", "who": "bob"}})[0]
		o2 := put(domain.ObjectWrite{Type: "docs@Owner", Value: map[string]any{"role": "qa", "who": "carol"}})[0]
		if o.Key != "qa" || o2.Key != "qa" || o2.Version != 2 {
			t.Fatalf("natural key: %+v %+v", o, o2)
		}
		if _, err := g.PutObjects(ctx, c.ID, []domain.ObjectWrite{{Type: "docs@Owner", Key: "dev", Value: map[string]any{"role": "qa"}}}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("a natural key that differs from its attributes: %v", err)
		}
		th := put(domain.ObjectWrite{Type: "docs@Threshold", Value: map[string]any{"value": 0.7}}, domain.ObjectWrite{Type: "docs@Threshold", Value: map[string]any{"value": 0.8}})
		if th[1].Key != "" || th[1].Version != 2 {
			t.Fatalf("singleton: %+v", th)
		}
		cn := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "R1", Type: "docs@Req", Rationale: "r"}))
		if n := put(domain.ObjectWrite{Type: "docs@Note", Key: string(cn.ID), Value: map[string]any{"text": "why"}})[0]; n.Key != string(cn.ID) {
			t.Fatalf("ref key: %+v", n)
		}
		put(domain.ObjectWrite{Type: "docs@Mitigation", Key: "RISK-2", Value: map[string]any{"plan": "p"}})
		if f := put(domain.ObjectWrite{Type: "docs@Free", Value: map[string]any{"anything": 1}})[0]; f.Value["anything"] == nil {
			t.Fatalf("an open type: %+v", f)
		}
		// a workspace-scoped type: one per workspace
		opt := must[domain.Flow](t)(g.OpenOption(ctx, c.ID, OpenOptionRequest{Name: "a", Hypothesis: "h", By: "alice"}))
		h1 := put(domain.ObjectWrite{Type: "docs@Hypothesis", Workspace: domain.MainFlow, Value: map[string]any{"text": "main"}})[0]
		h2 := put(domain.ObjectWrite{Type: "docs@Hypothesis", Workspace: opt.ID, Value: map[string]any{"text": "a"}})[0]
		if h1.Workspace != "" || h2.Workspace != opt.ID || h2.Version != 1 {
			t.Fatalf("workspaces: %+v %+v", h1, h2)
		}
		// reading: the last versions, filtered; as they were at a position of the log
		all := must[[]domain.ChangeObject](t)(g.Objects(ctx, c.ID, domain.ObjectFilter{}))
		if len(all) != 9 || all[0].Key != "RISK-1" || all[0].Version != 2 {
			t.Fatalf("objects: %d %+v", len(all), all)
		}
		if got := must[[]domain.ChangeObject](t)(g.Objects(ctx, c.ID, domain.ObjectFilter{Labels: map[string]string{"step": "s1"}})); len(got) != 0 {
			t.Fatalf("the labels of the last write: %+v", got)
		}
		if got := must[[]domain.ChangeObject](t)(g.Objects(ctx, c.ID, domain.ObjectFilter{Types: []string{"docs@Risk"}, KeyPrefix: "RISK-2"})); len(got) != 1 || got[0].Key != "RISK-2" {
			t.Fatalf("by prefix: %+v", got)
		}
		if got := must[[]domain.ChangeObject](t)(g.Objects(ctx, c.ID, domain.ObjectFilter{Types: []string{"docs@Hypothesis"}, Workspaces: []string{opt.ID}})); len(got) != 1 || got[0].Value["text"] != "a" {
			t.Fatalf("by workspace: %+v", got)
		}
		at := must[[]domain.ChangeObject](t)(g.Objects(ctx, c.ID, domain.ObjectFilter{Types: []string{"docs@Risk"}, AtSeq: r[1].Seq}))
		if len(at) != 2 || at[0].Version != 1 || at[0].State != "open" || at[0].Labels["step"] != "s1" {
			t.Fatalf("at a position: %+v", at)
		}
		// the log holds every version, filtered by labels too; the object stream is the graph's
		log, _, err := g.ChangeLog(ctx, domain.LogFilter{Change: c.ID, Types: []string{domain.LogObject + "."}, Labels: map[string]string{"step": "s1"}})
		if err != nil {
			t.Fatal(err)
		}
		if len(log) != 1 || log[0].Type != "object.docs@Risk" || log[0].Subject != "RISK-1" {
			t.Fatalf("log: %+v", log)
		}
		if err := g.AppendLog(ctx, []domain.LogEntry{{Change: c.ID, Type: "object.docs@Risk", Payload: json.RawMessage(`{}`)}}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("the object stream is written by the graph only: %v", err)
		}
		// a committed change takes no more change objects
		must[domain.Flow](t)(g.DiscardFlow(ctx, c.ID, opt.ID, "alice"))
		must[domain.ChangeImpact](t)(g.ImpactNodeReview(ctx, c.ID, cn.ID, domain.ReviewAccepted, "bob", "ok"))
		must[domain.Baseline](t)(g.Apply(ctx, c.ID, ""))
		if _, err := g.PutObjects(ctx, c.ID, []domain.ObjectWrite{{Type: "docs@Threshold"}}); !errors.Is(err, ErrConflict) {
			t.Fatalf("an applied change: %v", err)
		}
	})
}

// Submit writes impact operations, items and change objects in one transaction, all or none.
func TestSubmit(t *testing.T) {
	forEachRepo(t, func(t *testing.T, repo Repo) {
		ctx := context.Background()
		g, c := objectsGraph(t, repo)
		if _, err := g.Submit(ctx, c.ID, Batch{}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("an empty batch: %v", err)
		}
		res := must[BatchResult](t)(g.Submit(ctx, c.ID, Batch{
			Creates: []NodeCreate{{Key: "R1", Type: "docs@Req", Rationale: "r", Properties: map[string]any{"title": "t"}}},
			Items:   []domain.ChangeItem{{Kind: domain.KindArtifact, Type: "note", Data: map[string]any{"x": 1}}},
			Objects: []domain.ObjectWrite{{Type: "docs@Risk", Value: map[string]any{"title": "late"}}},
		}))
		if len(res.Impacts) != 1 || len(res.Items) != 1 || len(res.Objects) != 1 || res.Objects[0].Key != "RISK-1" {
			t.Fatalf("result: %+v", res)
		}
		cn := res.Impacts[0]
		res = must[BatchResult](t)(g.Submit(ctx, c.ID, Batch{
			Updates: []ImpactUpdate{{Impact: cn.ID, NodeUpdate: NodeUpdate{Properties: map[string]any{"title": "u"}}}},
			Objects: []domain.ObjectWrite{{Type: "docs@Note", Key: string(cn.ID), Value: map[string]any{"text": "why"}}},
		}))
		if len(res.Impacts) != 1 || len(res.Objects) != 1 {
			t.Fatalf("result: %+v", res)
		}
		// all or none: the failing change object undoes the creation before it
		if _, err := g.Submit(ctx, c.ID, Batch{
			Creates: []NodeCreate{{Key: "R2", Type: "docs@Req", Rationale: "r"}},
			Objects: []domain.ObjectWrite{{Type: "docs@Nope"}},
		}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("a failing batch: %v", err)
		}
		if impacts := must[domain.Change](t)(g.Change(ctx, c.ID)).Nodes; len(impacts) != 1 {
			t.Fatalf("a failed batch wrote an impact: %+v", impacts)
		}
	})
}
