package assistantsvc

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/internal/convsvc"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/methodology"
)

const deliveryYAML = `
name: delivery
version: 1.0.0
namespace: alm
description: Deliver software
roles:
  - {name: developer}
  - {name: release_manager}
conditions:
  - {name: built, expr: 'artifacts.exists(a, a.type == "build")'}
  - {name: released, expr: 'artifacts.exists(a, a.type == "release")'}
actions:
  - {name: build, kind: builtin, builtin: test.emit, effects: {built: true}, params: {kind: artifact, type: build}}
  - {name: release, kind: builtin, builtin: test.emit, pre: {built: true}, effects: {released: true}, params: {kind: artifact, type: release}}
goals:
  - {name: built, description: The build exists, pre: {built: true}}
  - {name: ship, description: Released, pre: {released: true}}
agents:
  - {name: builder, description: Builds the software, examples: ["build it"], roles: [developer], actions: [build], goals: [built]}
  - {name: shipper, description: Ships the release, roles: [release_manager], actions: [build, release], goals: [built, ship]}
  - {name: helper, description: Anyone may run it, actions: [build], goals: [built]}
`

const otherYAML = `
name: other
version: 1.0.0
namespace: alm
conditions:
  - {name: built, expr: 'artifacts.exists(a, a.type == "build")'}
actions:
  - {name: build, kind: builtin, builtin: test.emit, effects: {built: true}, params: {kind: artifact, type: build}}
goals:
  - {name: built, pre: {built: true}}
agents:
  - {name: stranger, actions: [build], goals: [built]}
`

func compiled(t *testing.T, src string) *methodology.Compiled {
	t.Helper()
	m, err := methodology.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// fakeEngine decides the roles from the agents of the methodologies and the roles `held` gives each subject.
type fakeEngine struct {
	held          map[string][]string
	methodologies fakeMethodologies
	active        []ProcessInfo
	starts        []StartProcess
	startWho      []authz.Principal
	startErr      error
	checks        int
}

func (f *fakeEngine) CheckAgents(ctx context.Context, name, _ string, _ []string) (AgentChecks, error) {
	f.checks++
	m := f.methodologies[name]
	out := AgentChecks{MayStart: true, MayRun: map[string]bool{}}
	who := authz.From(ctx)
	for _, ag := range m.AgentList() {
		out.MayRun[ag.Name] = len(ag.Roles) == 0 || slices.ContainsFunc(ag.Roles, func(r string) bool { return slices.Contains(f.held[who.Subject], r) })
	}
	return out, nil
}

func (f *fakeEngine) StartProcess(ctx context.Context, in StartProcess) (ProcessInfo, error) {
	if f.startErr != nil {
		return ProcessInfo{}, f.startErr
	}
	f.starts = append(f.starts, in)
	f.startWho = append(f.startWho, authz.From(ctx))
	change := in.ChangeID
	if change == "" {
		change = "CHG-AUTO"
	}
	return ProcessInfo{ID: "PRC-1", Methodology: in.Methodology, Agent: in.Agent, ChangeID: change, Status: "running"}, nil
}

func (f *fakeEngine) Active(context.Context) ([]ProcessInfo, error) { return f.active, nil }

// toolResult is the result of the tool of the last round given back to the model.
func (e *env) toolResult(round int) string {
	ms := e.model.got[round].Messages
	return ms[len(ms)-1].Content
}

func changeCtx(id string) Context {
	return Context{TabKind: "change", TabParams: map[string]string{"id": id}, Subject: id, Project: "PROJ-A"}
}

func agentNames(t *testing.T, result string) []string {
	t.Helper()
	i := strings.Index(result, "[{")
	var rs []struct {
		Result struct {
			Agents []agentEntry `json:"agents"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(result[i:]), &rs); err != nil {
		t.Fatalf("%v in %q", err, result)
	}
	var out []string
	for _, a := range rs[0].Result.Agents {
		out = append(out, a.Methodology+"/"+a.Agent)
	}
	return out
}

func TestListAgentsFiltersByProjectAndRoles(t *testing.T) {
	e := newEnv(t, call(ToolListAgents, `{}`), `{"message":"ok"}`)
	if _, _, err := e.send("what can I run?", Context{Project: "PROJ-A"}); err != nil {
		t.Fatal(err)
	}
	res := e.toolResult(1)
	// u1 is a developer: not the release manager's agent, and nothing of a methodology of another project
	if got := agentNames(t, res); !slices.Equal(got, []string{"delivery/builder", "delivery/helper"}) {
		t.Fatalf("%v in %s", got, res)
	}
	if !strings.Contains(res, `"requiredRoles":["developer"]`) || !strings.Contains(res, "Builds the software") || !strings.Contains(res, "build it") {
		t.Fatalf("entry %s", res)
	}
	// another project: the methodologies of that one
	e = newEnv(t, call(ToolListAgents, `{}`), `{"message":"ok"}`)
	e.user.Project = "PROJ-B"
	e.ctx = authz.With(context.Background(), e.user)
	if _, _, err := e.send("and here?", Context{Project: "PROJ-B"}); err != nil {
		t.Fatal(err)
	}
	if got := agentNames(t, e.toolResult(1)); !slices.Equal(got, []string{"other/stranger"}) {
		t.Fatalf("%v", got)
	}
	// a release manager sees the shipper too
	e = newEnv(t, call(ToolListAgents, `{}`), `{"message":"ok"}`)
	e.engine.held["u1"] = []string{"release_manager"}
	if _, _, err := e.send("what can I run?", Context{Project: "PROJ-A"}); err != nil {
		t.Fatal(err)
	}
	if got := agentNames(t, e.toolResult(1)); !slices.Equal(got, []string{"delivery/shipper", "delivery/helper"}) {
		t.Fatalf("%v", got)
	}
}

func TestListAgentsOfTheChangeInContext(t *testing.T) {
	e := newEnv(t, call(ToolListAgents, `{}`), `{"message":"ok"}`)
	e.engine.active = []ProcessInfo{{ID: "PRC-9", Agent: "builder", ChangeID: "CHG-D", Status: "running"}, {ID: "PRC-8", Agent: "helper", ChangeID: "CHG-OTHER", Status: "running"}}
	if _, _, err := e.send("what now?", changeCtx("CHG-D")); err != nil {
		t.Fatal(err)
	}
	res := e.toolResult(1)
	if got := agentNames(t, res); !slices.Equal(got, []string{"delivery/builder", "delivery/helper"}) {
		t.Fatalf("%v", got)
	}
	var rs []struct {
		Result struct {
			Change changeView   `json:"change"`
			Agents []agentEntry `json:"agents"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(res[strings.Index(res, "[{"):]), &rs); err != nil {
		t.Fatal(err)
	}
	r := rs[0].Result
	if r.Change.ID != "CHG-D" || r.Change.Methodology != "delivery" || r.Change.Status != "active" || r.Change.State != "design" || len(r.Change.Processes) != 1 || r.Change.Processes[0].ID != "PRC-9" {
		t.Fatalf("change %+v", r.Change)
	}
	// only the agent with a process of its own on this change is flagged
	if !r.Agents[0].Running || r.Agents[1].Running || !r.Agents[0].NeedsChange {
		t.Fatalf("agents %+v", r.Agents)
	}
	// the change was read as the caller
	// a change that cannot take a process any more lists nothing, and says why
	e = newEnv(t, call(ToolListAgents, `{}`), `{"message":"ok"}`)
	if _, _, err := e.send("what now?", changeCtx("CHG-DONE")); err != nil {
		t.Fatal(err)
	}
	if res := e.toolResult(1); len(agentNames(t, res)) != 0 || !strings.Contains(res, "applied") {
		t.Fatalf("%s", res)
	}
	// a change of someone else is not visible
	e = newEnv(t, call(ToolListAgents, `{}`), `{"message":"ok"}`)
	if _, _, err := e.send("what now?", changeCtx("CHG-HERS")); err != nil {
		t.Fatal(err)
	}
	if res := e.toolResult(1); !strings.Contains(res, "not visible") {
		t.Fatalf("%s", res)
	}
}

func proposeArgs(extra string) string {
	return `{"methodology":"delivery","agent":"builder","goal":"built","rationale":"you asked for a build"` + extra + `}`
}

func TestStartAgentOnlyRecordsAProposal(t *testing.T) {
	e := newEnv(t, call(ToolStartAgent, proposeArgs(`,"newChange":{"title":"Build v2","intent":"a build of v2"}`)), `{"message":"I propose to start the builder."}`)
	if _, _, err := e.send("build v2", Context{Project: "PROJ-A"}); err != nil {
		t.Fatal(err)
	}
	if len(e.engine.starts) != 0 || len(e.graph.created) != 0 {
		t.Fatalf("something ran: %+v %+v", e.engine.starts, e.graph.created)
	}
	if res := e.toolResult(1); !strings.Contains(res, "proposed") || !strings.Contains(res, "nothing was started") {
		t.Fatalf("%s", res)
	}
	a := e.answer(t)
	if a.Status != convsvc.StatusDone || len(a.Actions) != 1 {
		t.Fatalf("%+v", a)
	}
	act := a.Actions[0]
	args := act["args"].(map[string]any)
	if act["type"] != ActionStartAgent || act["status"] != StatusProposed || act["rationale"] != "you asked for a build" || !strings.Contains(act["label"].(string), "builder") ||
		args["methodology"] != "delivery" || args["agent"] != "builder" || args["goal"] != "built" || args["project"] != "PROJ-A" ||
		args["newChange"].(map[string]any)["title"] != "Build v2" || args["changeId"] != nil {
		t.Fatalf("action %+v", act)
	}
	for _, m := range e.model.metas { // the ledger still says assistant
		if m.Source != llm.SourceAssistant || m.ConversationID != e.conv.ID {
			t.Fatalf("meta %+v", m)
		}
	}
}

func TestStartAgentRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		args string
		ctx  Context
		want string
	}{
		"a role the caller lacks":  {`{"methodology":"delivery","agent":"shipper","goal":"ship","newChange":{"title":"t","intent":"i"}}`, Context{Project: "PROJ-A"}, "not one you may run here"},
		"another project":          {`{"methodology":"other","agent":"stranger","newChange":{"title":"t","intent":"i"}}`, Context{Project: "PROJ-A"}, "not one you may run here"},
		"an unknown goal":          {`{"methodology":"delivery","agent":"builder","goal":"nope","newChange":{"title":"t","intent":"i"}}`, Context{Project: "PROJ-A"}, "no goal"},
		"no change and no new":     {proposeArgs(``), Context{Project: "PROJ-A"}, "newChange"},
		"a change out of context":  {proposeArgs(`,"changeId":"CHG-OTHER"`), changeCtx("CHG-D"), "looking at"},
		"a new change in a change": {proposeArgs(`,"newChange":{"title":"t","intent":"i"}`), changeCtx("CHG-D"), "no newChange"},
		"a closed change":          {proposeArgs(``), changeCtx("CHG-DONE"), "not one you may run here"},
		"a goal to choose":         {`{"methodology":"delivery","agent":"helper","goal":"","newChange":{"title":"t","intent":"i"}}`, Context{Project: "PROJ-A"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, call(ToolStartAgent, tc.args), `{"message":"no"}`)
			if _, _, err := e.send("run it", tc.ctx); err != nil {
				t.Fatal(err)
			}
			res := e.toolResult(1)
			if tc.want == "" { // helper has one goal: it is chosen, the proposal stands
				if got := e.answer(t); len(got.Actions) != 1 {
					t.Fatalf("%s", res)
				}
				return
			}
			if !strings.Contains(res, `"error"`) || !strings.Contains(res, tc.want) {
				t.Fatalf("%s", res)
			}
			if a := e.answer(t); len(a.Actions) != 0 || len(e.engine.starts) != 0 {
				t.Fatalf("%+v", a)
			}
		})
	}
}

func TestStartAgentOnTheChangeInContext(t *testing.T) {
	e := newEnv(t, call(ToolStartAgent, proposeArgs(``)), `{"message":"proposed"}`)
	if _, _, err := e.send("build", changeCtx("CHG-D")); err != nil {
		t.Fatal(err)
	}
	args := e.answer(t).Actions[0]["args"].(map[string]any)
	if args["changeId"] != "CHG-D" || args["newChange"] != nil {
		t.Fatalf("%+v", args)
	}
}

func TestOneProposalPerAnswer(t *testing.T) {
	two := `{"message":"","tool_calls":[{"name":"start_agent","arguments":` + proposeArgs(`,"newChange":{"title":"t","intent":"i"}`) + `},{"name":"start_agent","arguments":` + proposeArgs(`,"newChange":{"title":"t","intent":"i"}`) + `}]}`
	e := newEnv(t, two, `{"message":"proposed"}`)
	if _, _, err := e.send("build", Context{Project: "PROJ-A"}); err != nil {
		t.Fatal(err)
	}
	if n := len(e.answer(t).Actions); n != 1 || !strings.Contains(e.toolResult(1), "one proposal per answer") {
		t.Fatalf("%d actions, %s", n, e.toolResult(1))
	}
}

// propose runs a turn that ends with a proposal and returns its message.
func (e *env) propose(t *testing.T, extra string, c Context) convsvc.Message {
	t.Helper()
	e.model.answers = []string{call(ToolStartAgent, proposeArgs(extra)), `{"message":"I propose it."}`}
	e.model.got = nil
	if _, _, err := e.send("build", c); err != nil {
		t.Fatal(err)
	}
	m := e.answer(t)
	if len(m.Actions) != 1 {
		t.Fatalf("no proposal: %+v", m)
	}
	return m
}

func (e *env) confirm(m convsvc.Message, decision string) (convsvc.Message, error) {
	return e.svc.Confirm(e.ctx, ConfirmInput{ConversationID: e.conv.ID, MessageID: m.ID, Decision: decision, Project: "PROJ-A"})
}

const newChangeArg = `,"newChange":{"title":"Build v2","intent":"a build of v2"}`

func TestConfirmAcceptCreatesTheChangeAndStartsAsTheCaller(t *testing.T) {
	e := newEnv(t)
	m := e.propose(t, newChangeArg, Context{Project: "PROJ-A"})
	got, err := e.confirm(m, DecisionAccept)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.graph.created) != 1 || e.graph.created[0].Title != "Build v2" || e.graph.created[0].Methodology != "delivery" || e.graph.created[0].Namespace != "alm" ||
		e.graph.created[0].ProjectID != "PROJ-A" || e.graph.who[0].Subject != "u1" {
		t.Fatalf("change %+v %+v", e.graph.created, e.graph.who)
	}
	if len(e.engine.starts) != 1 {
		t.Fatalf("starts %+v", e.engine.starts)
	}
	st, who := e.engine.starts[0], e.engine.startWho[0]
	if st.Methodology != "delivery" || st.Agent != "builder" || st.Goal != "built" || st.ChangeID != "CHG-NEW" || st.ProjectID != "PROJ-A" ||
		st.Vars["conversationId"] != e.conv.ID || st.Vars["messageId"] != m.ID || who.Subject != "u1" || who.Project != "PROJ-A" {
		t.Fatalf("start %+v as %+v", st, who)
	}
	act := got.Actions[0]
	res := act["result"].(map[string]any)
	if act["status"] != StatusStarted || res["processId"] != "PRC-1" || res["changeId"] != "CHG-NEW" || act["decided"].(map[string]any)["by"] != "u1" {
		t.Fatalf("action %+v", act)
	}
	if got.Text != m.Text || got.Status != convsvc.StatusDone {
		t.Fatalf("the message changed: %+v", got)
	}
	// stored
	if stored := e.answer(t); stored.Actions[0]["status"] != StatusStarted {
		t.Fatalf("%+v", stored.Actions)
	}
	// a second decision, either way, is a conflict and starts nothing more
	for _, d := range []string{DecisionAccept, DecisionReject} {
		if _, err := e.confirm(m, d); !errors.Is(err, ErrDecided) {
			t.Fatalf("%s again: %v", d, err)
		}
	}
	if len(e.engine.starts) != 1 || len(e.graph.created) != 1 {
		t.Fatal("started twice")
	}
}

func TestConfirmAcceptOnAnExistingChange(t *testing.T) {
	e := newEnv(t)
	m := e.propose(t, ``, changeCtx("CHG-D"))
	if _, err := e.confirm(m, DecisionAccept); err != nil {
		t.Fatal(err)
	}
	if len(e.graph.created) != 0 || len(e.engine.starts) != 1 || e.engine.starts[0].ChangeID != "CHG-D" || e.engine.starts[0].ProjectID != "PROJ-A" {
		t.Fatalf("%+v %+v", e.graph.created, e.engine.starts)
	}
}

func TestConfirmRejectStartsNothing(t *testing.T) {
	e := newEnv(t)
	m := e.propose(t, newChangeArg, Context{Project: "PROJ-A"})
	got, err := e.confirm(m, DecisionReject)
	if err != nil {
		t.Fatal(err)
	}
	if got.Actions[0]["status"] != StatusRejected || len(e.engine.starts) != 0 || len(e.graph.created) != 0 {
		t.Fatalf("%+v", got.Actions)
	}
	if _, err := e.confirm(m, DecisionAccept); !errors.Is(err, ErrDecided) {
		t.Fatalf("%v", err)
	}
}

func TestConfirmOnlyByTheOwnerOnAProposal(t *testing.T) {
	e := newEnv(t)
	m := e.propose(t, newChangeArg, Context{Project: "PROJ-A"})
	other := authz.Principal{Subject: "u2", Project: "PROJ-A"}
	_, err := e.svc.Confirm(authz.With(context.Background(), other), ConfirmInput{ConversationID: e.conv.ID, MessageID: m.ID, Decision: DecisionAccept})
	if !errors.Is(err, convsvc.ErrNotFound) {
		t.Fatalf("%v", err)
	}
	sys := authz.With(context.Background(), Principal)
	if _, err := e.svc.Confirm(sys, ConfirmInput{ConversationID: e.conv.ID, MessageID: m.ID, Decision: DecisionAccept}); !errors.Is(err, convsvc.ErrForbidden) {
		t.Fatalf("%v", err)
	}
	// not a proposal, not a decision
	for _, in := range []ConfirmInput{
		{ConversationID: e.conv.ID, MessageID: m.ID, ActionIndex: 3, Decision: DecisionAccept},
		{ConversationID: e.conv.ID, MessageID: m.ID, Decision: "maybe"},
		{ConversationID: e.conv.ID, MessageID: "MSG-NOPE", Decision: DecisionAccept},
	} {
		if _, err := e.svc.Confirm(e.ctx, in); err == nil {
			t.Fatalf("accepted %+v", in)
		}
	}
	if len(e.engine.starts) != 0 || len(e.graph.created) != 0 {
		t.Fatal("something ran")
	}
}

func TestConfirmStaleProposalStaysProposed(t *testing.T) {
	e := newEnv(t)
	m := e.propose(t, newChangeArg, Context{Project: "PROJ-A"})
	// the active project is no longer the proposal's
	_, err := e.svc.Confirm(e.ctx, ConfirmInput{ConversationID: e.conv.ID, MessageID: m.ID, Decision: DecisionAccept, Project: "PROJ-B"})
	if !errors.Is(err, ErrStale) || !strings.Contains(err.Error(), "PROJ-B") {
		t.Fatalf("%v", err)
	}
	// the role was lost since
	e.engine.held["u1"] = nil
	if _, err := e.confirm(m, DecisionAccept); !errors.Is(err, ErrStale) {
		t.Fatalf("%v", err)
	}
	if got := e.answer(t).Actions[0]["status"]; got != StatusProposed || len(e.engine.starts) != 0 || len(e.graph.created) != 0 {
		t.Fatalf("status %v", got)
	}
	// a change closed since
	e = newEnv(t)
	m = e.propose(t, ``, changeCtx("CHG-D"))
	ch := e.graph.changes["CHG-D"]
	ch.Status = "applied"
	e.graph.changes["CHG-D"] = ch
	if _, err := e.confirm(m, DecisionAccept); !errors.Is(err, ErrStale) || !strings.Contains(err.Error(), "applied") {
		t.Fatalf("%v", err)
	}
	// a stale proposal can still be rejected
	if got, err := e.confirm(m, DecisionReject); err != nil || got.Actions[0]["status"] != StatusRejected {
		t.Fatalf("%v %+v", err, got)
	}
}

func TestConfirmStartFailureIsRecorded(t *testing.T) {
	e := newEnv(t)
	m := e.propose(t, newChangeArg, Context{Project: "PROJ-A"})
	e.engine.startErr = errors.New("engine is down")
	got, err := e.confirm(m, DecisionAccept)
	if err != nil {
		t.Fatal(err)
	}
	act := got.Actions[0]
	if act["status"] != StatusFailed || !strings.Contains(act["error"].(string), "engine is down") {
		t.Fatalf("%+v", act)
	}
	// the change created before the failure is reported
	if res, _ := act["result"].(map[string]any); res["changeId"] != "CHG-NEW" {
		t.Fatalf("%+v", act)
	}
	if _, err := e.confirm(m, DecisionAccept); !errors.Is(err, ErrDecided) {
		t.Fatalf("%v", err)
	}
	// the change creation failing
	e = newEnv(t)
	m = e.propose(t, newChangeArg, Context{Project: "PROJ-A"})
	e.graph.err = errors.New("graph is down")
	got, err = e.confirm(m, DecisionAccept)
	if err != nil || got.Actions[0]["status"] != StatusFailed || len(e.engine.starts) != 0 {
		t.Fatalf("%v %+v", err, got.Actions)
	}
}

func TestTheDecisionIsInTheNextHistory(t *testing.T) {
	for decision, want := range map[string]string{
		DecisionAccept: "accepted the proposal to start agent builder of delivery: it was started (process PRC-1 on change CHG-NEW)",
		DecisionReject: "rejected the proposal to start agent builder of delivery",
	} {
		e := newEnv(t)
		m := e.propose(t, newChangeArg, Context{Project: "PROJ-A"})
		// before the decision: nothing was started
		if h := historyOf(e.messages(t)); !strings.Contains(h[len(h)-1].Content, "has not decided yet, nothing was started") {
			t.Fatalf("%+v", h)
		}
		if _, err := e.confirm(m, decision); err != nil {
			t.Fatal(err)
		}
		e.model.answers = []string{`{"message":"ok"}`}
		e.model.got = nil
		if _, _, err := e.send("and then?", Context{Project: "PROJ-A"}); err != nil {
			t.Fatal(err)
		}
		var sent []string
		for _, msg := range e.model.got[0].Messages {
			sent = append(sent, msg.Content)
		}
		if all := strings.Join(sent, "\n"); !strings.Contains(all, want) {
			t.Fatalf("%s: %s", decision, all)
		}
	}
}

func TestAgentToolsNeedTheEngine(t *testing.T) {
	e := newEnv(t, call(ToolListAgents, `{}`), `{"message":"no"}`)
	e.svc.Engine = nil
	if _, _, err := e.send("what can I run?", Context{Project: "PROJ-A"}); err != nil {
		t.Fatal(err)
	}
	if res := e.toolResult(1); !strings.Contains(res, "engine is not reachable") {
		t.Fatalf("%s", res)
	}
}

func TestToolsAreSix(t *testing.T) {
	if got := Tools(); len(got) != 6 {
		t.Fatalf("%v", got)
	}
	for _, name := range Tools() {
		if !strings.Contains(systemRules, name+":") {
			t.Errorf("the prompt does not describe %s", name)
		}
	}
}

func TestRPCErrorCodes(t *testing.T) {
	for err, want := range map[error]connect.Code{ErrDecided: connect.CodeAborted, ErrStale: connect.CodeFailedPrecondition} {
		if got := connect.CodeOf(rpcErr(err)); got != want {
			t.Errorf("%v: %v", err, got)
		}
	}
}
