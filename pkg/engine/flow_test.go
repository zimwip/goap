package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/llm"
)

func completedRun(t *testing.T) (*Engine, context.Context, *Process, domain.ChangeSet) {
	t.Helper()
	ctx := context.Background()
	e, g, base := setup(t)
	p, err := e.Start(ctx, StartRequest{Methodology: "impact-analysis", BaselineID: base, Intent: "The PSP changes its API, what does this break?"})
	if err != nil {
		t.Fatal(err)
	}
	if p, err = e.Run(ctx, p.ID); err != nil || p.Status != StatusCompleted {
		t.Fatalf("run: %v %+v", err, p)
	}
	c, err := g.Change(ctx, p.ChangeID)
	if err != nil {
		t.Fatal(err)
	}
	return e, ctx, p, c
}

// itemsOfSteps are the items produced by the steps of p from `from` on.
func itemsOfSteps(c domain.ChangeSet, p *Process, from int) map[domain.ItemID]bool {
	execs := map[string]bool{}
	for _, s := range p.Steps[from:] {
		execs[s.Execution] = true
	}
	out := map[domain.ItemID]bool{}
	for _, it := range c.Items {
		if execs[it.Execution] {
			out[it.ID] = true
		}
	}
	return out
}

func TestRelaunchStepAdoptFlow(t *testing.T) {
	e, ctx, old, c := completedRun(t)
	g := e.Graph.(interface {
		Change(context.Context, domain.ChangeID) (domain.ChangeSet, error)
	})
	if len(old.Steps) < 3 {
		t.Fatalf("steps = %d", len(old.Steps))
	}
	replaced, kept := itemsOfSteps(c, old, 1), itemsOfSteps(c, old, 0)
	for id := range replaced {
		delete(kept, id)
	}
	if len(replaced) == 0 || len(kept) == 0 {
		t.Fatalf("replaced %d kept %d", len(replaced), len(kept))
	}

	np, err := e.Relaunch(ctx, old.ID, 1, "the PSP answer changed", "")
	if err != nil {
		t.Fatal(err)
	}
	if np.Flow == "" || np.RelaunchOf != old.ID || np.FromStep != 1 || np.Status != StatusRunning {
		t.Fatalf("relaunched process = %+v", np)
	}
	c, _ = g.Change(ctx, old.ChangeID)
	for id := range replaced {
		if st := c.EffectiveStatus(id); st != domain.ItemStale {
			t.Fatalf("item %s = %s, want stale", id, st)
		}
	}
	for id := range kept {
		if st := c.EffectiveStatus(id); st == domain.ItemStale {
			t.Fatalf("item %s of an earlier step must not be stale", id)
		}
	}
	// the relaunched run replans from the state before step 1 and waits for a human once it reaches the goal
	if np, err = e.Run(ctx, np.ID); err != nil {
		t.Fatal(err)
	}
	if np.Status != StatusWaiting || np.Pending == nil || np.Pending.Kind != TaskFlow {
		t.Fatalf("after the run: %s %+v", np.Status, np.Pending)
	}
	c, _ = g.Change(ctx, old.ChangeID)
	candidates := 0
	for _, it := range c.Items {
		if it.Flow == np.Flow && it.Kind != domain.KindFlow {
			candidates++
			if st := c.EffectiveStatus(it.ID); st != domain.ItemCandidate {
				t.Fatalf("candidate %s = %s", it.ID, st)
			}
		}
	}
	if candidates != len(replaced) {
		t.Fatalf("candidates = %d, replaced = %d", candidates, len(replaced))
	}
	// the previous run is untouched until the human decides
	if cur, _ := e.Store.Get(ctx, old.ID); cur.Status != StatusCompleted {
		t.Fatalf("old run = %s", cur.Status)
	}
	// a second relaunch in parallel is allowed
	if _, err := e.Relaunch(ctx, old.ID, 2, "again", ""); err != nil {
		t.Fatalf("parallel relaunch: %v", err)
	}

	np, err = e.DecideFlow(ctx, np.ID, true, "looks right")
	if err != nil {
		t.Fatal(err)
	}
	if np.Status != StatusCompleted {
		t.Fatalf("adopted run = %s", np.Status)
	}
	if cur, _ := e.Store.Get(ctx, old.ID); cur.Status != StatusSuperseded {
		t.Fatalf("old run = %s, want superseded", cur.Status)
	}
	c, _ = g.Change(ctx, old.ChangeID)
	for id := range replaced {
		if st := c.EffectiveStatus(id); st != domain.ItemSuperseded {
			t.Fatalf("item %s = %s, want superseded", id, st)
		}
	}
	for _, it := range c.Items {
		if it.Flow == np.Flow && it.Kind != domain.KindFlow {
			if st := c.EffectiveStatus(it.ID); st == domain.ItemCandidate || st == domain.ItemSuperseded || st == domain.ItemRejected {
				t.Fatalf("adopted item %s = %s", it.ID, st)
			}
		}
	}
	// the journal records the decision
	recs, _ := e.Graph.Journal(ctx, domain.ExecutionFilter{ChangeID: old.ChangeID})
	found := false
	for _, r := range recs {
		found = found || (r.Kind == domain.ExecApproval && r.Action == "flow" && r.Data["decision"] == "adopted")
	}
	if !found {
		t.Fatal("flow decision not journaled")
	}
}

func TestRelaunchStepDiscardFlow(t *testing.T) {
	e, ctx, old, c := completedRun(t)
	g := e.Graph.(interface {
		Change(context.Context, domain.ChangeID) (domain.ChangeSet, error)
	})
	replaced := itemsOfSteps(c, old, 1)
	np, err := e.Relaunch(ctx, old.ID, 1, "try again", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.DecideFlow(ctx, np.ID, true, ""); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("adopting before the goal is reached: %v", err)
	}
	if np, err = e.Run(ctx, np.ID); err != nil || np.Status != StatusWaiting {
		t.Fatalf("run: %v %+v", err, np)
	}
	if np, err = e.DecideFlow(ctx, np.ID, false, "not better"); err != nil || np.Status != StatusSuperseded {
		t.Fatalf("discard: %v %+v", err, np)
	}
	if cur, _ := e.Store.Get(ctx, old.ID); cur.Status != StatusCompleted {
		t.Fatalf("old run = %s, must stay completed", cur.Status)
	}
	c, _ = g.Change(ctx, old.ChangeID)
	for id := range replaced {
		if st := c.EffectiveStatus(id); st == domain.ItemStale || st == domain.ItemSuperseded || st == domain.ItemRejected {
			t.Fatalf("item %s = %s: the previous outputs count again", id, st)
		}
	}
	for _, it := range c.Items {
		if it.Flow == np.Flow && it.Kind != domain.KindFlow {
			if st := c.EffectiveStatus(it.ID); st != domain.ItemRejected {
				t.Fatalf("discarded candidate %s = %s", it.ID, st)
			}
		}
	}
	// a new relaunch is possible once the flow is decided
	if _, err := e.Relaunch(ctx, old.ID, 2, "later", ""); err != nil {
		t.Fatalf("relaunch after a decision: %v", err)
	}
}

func TestRelaunchRules(t *testing.T) {
	e, ctx, old, _ := completedRun(t)
	if _, err := e.Relaunch(ctx, old.ID, 99, "", ""); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("unknown step: %v", err)
	}
}

// corruptedBoard completes a run, then adds an item that step 1 "produced" but that refers to
// an item that does not exist, and starts a second process on the same change.
func corruptedBoard(t *testing.T) (e *Engine, ctx context.Context, a, b *Process, scheduled *[]string) {
	t.Helper()
	e, ctx, a, _ = completedRun(t)
	var ids []string
	e.Schedule = func(id string) { ids = append(ids, id) }
	bad := domain.ChangeItem{Kind: domain.KindArtifact, Type: "note", Execution: a.Steps[1].Execution, DerivedFrom: []domain.ItemID{"ghost"}, ProducedBy: "test"}
	if _, err := e.Graph.AddItems(ctx, a.ChangeID, []domain.ChangeItem{bad}); err != nil {
		t.Fatal(err)
	}
	b, err := e.Start(ctx, StartRequest{Methodology: "impact-analysis", ChangeID: a.ChangeID, Goal: a.Goal})
	if err != nil {
		t.Fatal(err)
	}
	if b, err = e.Run(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	return e, ctx, a, b, &ids
}

func TestBoardValidationProposesRelaunch(t *testing.T) {
	e, ctx, a, b, scheduled := corruptedBoard(t)
	if b.Status != StatusWaiting || b.Pending == nil || b.Pending.Kind != TaskBoard || len(b.Pending.Issues) == 0 {
		t.Fatalf("second process = %s %+v", b.Status, b.Pending)
	}
	prop := b.Pending.Proposal
	if prop == nil || prop.Process != a.ID || prop.Step != 1 || prop.Action != a.Steps[1].Action {
		t.Fatalf("proposal = %+v, want step 1 of %s", prop, a.ID)
	}
	if b.Pending.Issues[0].Code != "dangling" {
		t.Fatalf("issues = %+v", b.Pending.Issues)
	}

	// accept: the step is relaunched, the second process waits for the flow decision
	b, np, err := e.ResolveBoard(ctx, b.ID, true, "")
	if err != nil {
		t.Fatal(err)
	}
	if np == nil || np.RelaunchOf != a.ID || np.FromStep != 1 || b.Pending == nil || b.Pending.Kind != TaskRelaunched || b.Pending.FlowID != np.Flow {
		t.Fatalf("relaunch = %+v / %+v", np, b.Pending)
	}
	// the relaunched flow no longer contains the faulty item: it reaches the goal
	if np, err = e.Run(ctx, np.ID); err != nil || np.Status != StatusWaiting || np.Pending.Kind != TaskFlow {
		t.Fatalf("relaunched run: %v %s %+v", err, np.Status, np.Pending)
	}
	if _, err = e.DecideFlow(ctx, np.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	if cur, _ := e.Store.Get(ctx, b.ID); cur.Status != StatusRunning || cur.Pending != nil {
		t.Fatalf("the waiting process must resume: %s %+v", cur.Status, cur.Pending)
	}
	if len(*scheduled) != 1 || (*scheduled)[0] != b.ID {
		t.Fatalf("scheduled = %v", *scheduled)
	}
	// and the board is consistent again for it
	b, err = e.Run(ctx, b.ID)
	if err != nil || b.Status != StatusCompleted {
		t.Fatalf("resumed run: %v %s %+v", err, b.Status, b.Pending)
	}
}

func TestBoardValidationIgnore(t *testing.T) {
	e, ctx, _, b, _ := corruptedBoard(t)
	if b.Pending == nil || b.Pending.Kind != TaskBoard {
		t.Fatalf("second process = %s %+v", b.Status, b.Pending)
	}
	b, np, err := e.ResolveBoard(ctx, b.ID, false, "known problem")
	if err != nil || np != nil || b.Status != StatusRunning {
		t.Fatalf("ignore: %v %+v", err, b)
	}
	// the same issues are not asked twice
	if b, err = e.Run(ctx, b.ID); err != nil || b.Status != StatusCompleted {
		t.Fatalf("run after ignoring: %v %s %+v", err, b.Status, b.Pending)
	}
}

func TestBoardValidationDiscardedFlowResumes(t *testing.T) {
	e, ctx, _, b, _ := corruptedBoard(t)
	b, np, err := e.ResolveBoard(ctx, b.ID, true, "")
	if err != nil {
		t.Fatal(err)
	}
	if np, err = e.Run(ctx, np.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = e.DecideFlow(ctx, np.ID, false, "no"); err != nil {
		t.Fatal(err)
	}
	cur, _ := e.Store.Get(ctx, b.ID)
	if cur.Status != StatusRunning || len(cur.Dismissed) != 1 {
		t.Fatalf("after a discarded flow the process goes on with the issues ignored: %s %+v", cur.Status, cur.Dismissed)
	}
}

// A methodology targets a namespace: the change it opens acts on it.
func TestChangeOpensInTheNamespaceOfTheMethodology(t *testing.T) {
	ctx := context.Background()
	e, g, base := setup(t)
	e.Methodologies.(StaticMethodologies)["impact-analysis"].Namespace = "platform"
	p, err := e.Start(ctx, StartRequest{Methodology: "impact-analysis", BaselineID: base, Goal: "assess_impact"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := g.Change(ctx, p.ChangeID)
	if err != nil || c.Namespace != domain.NamespacePlatform {
		t.Fatalf("change namespace = %q, %v", c.Namespace, err)
	}
	// an explicit namespace on the request wins over the methodology default
	p2, err := e.Start(ctx, StartRequest{Methodology: "impact-analysis", BaselineID: base, Goal: "assess_impact", Namespace: "sdlc"})
	if err != nil {
		t.Fatal(err)
	}
	if c2, _ := g.Change(ctx, p2.ChangeID); c2.Namespace != "sdlc" {
		t.Fatalf("explicit namespace = %q", c2.Namespace)
	}
}

func TestRelaunchGuidanceReachesTheAgent(t *testing.T) {
	e, ctx, old, _ := completedRun(t)
	var prompts []string
	inner := scripted(t)
	e.Executors["llm"] = LLMExecutor{Client: llm.ClientFunc(func(ctx context.Context, req llm.Request) (llm.Response, error) {
		prompts = append(prompts, req.Messages[0].Content)
		return inner.Complete(ctx, req)
	})}
	np, err := e.Relaunch(ctx, old.ID, 1, "the PSP answer changed", "the PSP now requires API v3")
	if err != nil {
		t.Fatal(err)
	}
	if np, err = e.Run(ctx, np.ID); err != nil || np.Status != StatusWaiting || np.Pending.Kind != TaskFlow {
		t.Fatalf("run: %v %s %+v", err, np.Status, np.Pending)
	}
	if len(prompts) == 0 {
		t.Fatal("no LLM call in the relaunched flow")
	}
	for i, p := range prompts {
		if !strings.Contains(p, "the PSP now requires API v3") {
			t.Fatalf("prompt %d of the relaunched flow lacks the guidance:\n%s", i, p)
		}
	}
	// the main flow never sees the guidance
	prompts = nil
	old2, err := e.Start(ctx, StartRequest{Methodology: "impact-analysis", ChangeID: old.ChangeID, Goal: old.Goal})
	_ = old2
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range prompts {
		if strings.Contains(p, "API v3") {
			t.Fatal("guidance leaked in a prompt of the main flow")
		}
	}
}

func TestRelaunchedTitlesDoNotPileUp(t *testing.T) {
	if got := baseTitle("design it (relaunch from step 4) (relaunch from step 2)"); got != "design it" {
		t.Fatalf("baseTitle = %q", got)
	}
	e, ctx, old, _ := completedRun(t)
	np, err := e.Relaunch(ctx, old.ID, 1, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := old.Title + " (relaunch from step 2)"; np.Title != want {
		t.Fatalf("title = %q, want %q (steps are numbered from 1)", np.Title, want)
	}
}
