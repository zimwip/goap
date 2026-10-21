package assistantsvc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	assistantv1 "github.com/zimwip/goap/contracts/gen/goap/assistant/v1"
	"github.com/zimwip/goap/contracts/gen/goap/assistant/v1/assistantv1connect"
	"github.com/zimwip/goap/contracts/llm"
	"github.com/zimwip/goap/engine/methodology"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/identity"
	"github.com/zimwip/goap/platform/convsvc"
	"github.com/zimwip/goap/platform/modelgw"
)

// ---- fakes ----------------------------------------------------------------------------------------------------

type fakeModel struct {
	answers []string
	// responses, when set, are answered instead of answers (native tool calls, ADR 0098)
	responses []llm.Response
	err       error
	aliases   []modelgw.AliasEntry
	got       []llm.Request
	who       []authz.Principal
	metas     []llm.CallMeta
	// onCall, when set, runs before the n-th call (1-based) is answered
	onCall func(n int)
}

func (m *fakeModel) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	m.got = append(m.got, req)
	m.metas = append(m.metas, llm.MetaFrom(ctx))
	m.who = append(m.who, authz.From(ctx))
	if m.onCall != nil {
		m.onCall(len(m.got))
	}
	if m.err != nil {
		return llm.Response{}, m.err
	}
	i := len(m.got) - 1
	if m.responses != nil {
		return m.responses[min(i, len(m.responses)-1)], nil
	}
	if i >= len(m.answers) {
		i = len(m.answers) - 1
	}
	return llm.Response{Text: m.answers[i]}, nil
}

func (m *fakeModel) Available(context.Context) ([]modelgw.ModelEntry, []modelgw.AliasEntry, error) {
	return nil, m.aliases, nil
}

type fakeGraph struct {
	created []graph.NewChange
	who     []authz.Principal
	changes map[domain.ChangeID]domain.Change
	err     error
}

func (g *fakeGraph) CreateChange(ctx context.Context, in graph.NewChange) (domain.Change, error) {
	if g.err != nil {
		return domain.Change{}, g.err
	}
	g.created = append(g.created, in)
	g.who = append(g.who, authz.From(ctx))
	return domain.Change{ID: "CHG-NEW", Title: in.Title, Methodology: in.Methodology, ProjectID: access.ProjectURL(in.ProjectID)}, nil
}

func (g *fakeGraph) ListChanges(_ context.Context, f graph.ChangesFilter) ([]domain.Change, error) {
	var out []domain.Change
	for _, c := range g.changes {
		if f.Match(c) {
			out = append(out, c)
		}
	}
	return out, nil
}

func (g *fakeGraph) AttachChange(_ context.Context, id, parent domain.ChangeID) (domain.Change, error) {
	if g.err != nil {
		return domain.Change{}, g.err
	}
	c := g.changes[id]
	c.ParentID = parent
	g.changes[id] = c
	return c, nil
}

func (g *fakeGraph) DetachChange(_ context.Context, id domain.ChangeID) (domain.Change, error) {
	if g.err != nil {
		return domain.Change{}, g.err
	}
	c := g.changes[id]
	c.ParentID = ""
	g.changes[id] = c
	return c, nil
}

func (g *fakeGraph) Change(_ context.Context, id domain.ChangeID) (domain.Change, error) {
	if c, ok := g.changes[id]; ok {
		return c, nil
	}
	return domain.Change{}, graph.ErrNotFound
}

// fakeProjects: projects maps a project to its own methodologies; access maps a subject to the projects it may work on.
type fakeProjects struct {
	projects map[string][]string
	access   map[string][]string
}

func (f fakeProjects) RootProject(context.Context) (string, error) { return "PROJ-ROOT", nil }

func (f fakeProjects) HasProject(_ context.Context, p string) (bool, error) {
	_, ok := f.projects[p]
	return ok, nil
}

func (f fakeProjects) MayAccessProject(_ context.Context, who authz.Principal, p string) (bool, error) {
	for _, a := range f.access[who.Subject] {
		if a == p {
			return true, nil
		}
	}
	return false, nil
}

func (f fakeProjects) ApplicableMethodologies(_ context.Context, p string) ([]string, error) {
	return f.projects[p], nil
}

type fakeMethodologies map[string]*methodology.Compiled

func (f fakeMethodologies) Methodology(_ context.Context, n string) (*methodology.Compiled, error) {
	if m, ok := f[n]; ok {
		return m, nil
	}
	return nil, errors.New("not found")
}

// ---- environment ----------------------------------------------------------------------------------------------

type env struct {
	svc    *Service
	model  *fakeModel
	graph  *fakeGraph
	engine *fakeEngine
	convs  *convsvc.Service
	user   authz.Principal
	conv   convsvc.Conversation
	ctx    context.Context
	later  []func() // turns not run yet, when deferred
	// didSearch: searched was called (propose seeds it once)
	didSearch bool
}

func newEnv(t *testing.T, answers ...string) *env {
	t.Helper()
	e := &env{
		model: &fakeModel{answers: answers, aliases: []modelgw.AliasEntry{{Alias: Alias, Target: "fake/echo"}}},
		graph: &fakeGraph{changes: map[domain.ChangeID]domain.Change{
			"CHG-1":    {ID: "CHG-1", Title: "Existing", ProjectID: access.ProjectURL("PROJ-A"), Status: domain.ChangeDraft},
			"CHG-MINE": {ID: "CHG-MINE", Title: "Mine", OwnerOrg: access.UnitURL(access.PersonalUnit("u1"))},
			"CHG-HERS": {ID: "CHG-HERS", Title: "Hers", OwnerOrg: access.UnitURL(access.PersonalUnit("u2"))},
			"CHG-D":    {ID: "CHG-D", Title: "Delivery", Methodology: "delivery", Namespace: "alm", ProjectID: access.ProjectURL("PROJ-A"), Status: domain.ChangeActive, State: "design"},
			"CHG-DONE": {ID: "CHG-DONE", Title: "Done", Methodology: "delivery", Namespace: "alm", ProjectID: access.ProjectURL("PROJ-A"), Status: domain.ChangeApplied},
		}},
		convs: &convsvc.Service{Store: convsvc.NewMemoryStore()},
		user:  authz.Principal{Subject: "u1", Project: "PROJ-A"},
	}
	e.ctx = authz.With(context.Background(), e.user)
	e.svc = &Service{
		Convs: e.convs, Model: e.model, Graph: e.graph,
		Methodologies: fakeMethodologies{
			"sdlc": (&methodology.Compiled{}).WithDefinition(&methodology.Methodology{Name: "sdlc", Namespace: "alm", Description: "Software lifecycle",
				Goals: []methodology.Goal{{Name: "release", Examples: []string{"ship v2"}}}}),
			"risk":     (&methodology.Compiled{}).WithDefinition(&methodology.Methodology{Name: "risk", Namespace: "alm"}),
			"delivery": compiled(t, deliveryYAML),
			"other":    compiled(t, otherYAML),
		},
		Projects: fakeProjects{
			projects: map[string][]string{"PROJ-A": {"sdlc", "delivery"}, "PROJ-B": {"sdlc", "risk", "other"}, "PROJ-SECRET": nil},
			access:   map[string][]string{"u1": {"PROJ-A", "PROJ-B"}},
		},
		Go: func(f func()) { f() },
	}
	e.engine = &fakeEngine{held: map[string][]string{"u1": {"developer"}}, methodologies: e.svc.Methodologies.(fakeMethodologies), points: map[string]Points{
		"CHG-D": {Goal: "ship", BlockedCount: 2, Blocked: []string{"delivery/design: noted"}, Points: []Point{
			{ID: "delivery/build", Kind: "step", Name: "build", Description: "Build it", Why: []string{"noted"}, Produces: []string{"built"}, Agent: "builder", AgentGoal: "delivery/build", MayRun: true},
			{ID: "delivery/running", Kind: "step", Name: "running", Agent: "builder", AgentGoal: "delivery/running", MayRun: true, Running: true},
			{ID: "delivery/restricted", Kind: "step", Name: "restricted", Responsible: "release_manager", Agent: "shipper", AgentGoal: "delivery/restricted"},
		}}}}
	e.svc.Engine = e.engine
	c, err := e.convs.Create(e.ctx, e.user, "t")
	if err != nil {
		t.Fatal(err)
	}
	e.conv = c
	return e
}

func (e *env) send(text string, c Context) (convsvc.Message, convsvc.Message, error) {
	return e.svc.Send(e.ctx, SendInput{ConversationID: e.conv.ID, Text: text, Context: c})
}

func (e *env) messages(t *testing.T) []convsvc.Message {
	t.Helper()
	_, ms, err := e.convs.Get(e.ctx, e.user, e.conv.ID)
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

// answer is the assistant message of the last turn.
func (e *env) answer(t *testing.T) convsvc.Message {
	t.Helper()
	ms := e.messages(t)
	return ms[len(ms)-1]
}

func inProject(p string) Context { return Context{App: App{Project: p}} }

// searched records an earlier turn that searched the open changes: creating a change is offered from then on.
func (e *env) searched(t *testing.T) {
	t.Helper()
	if _, err := e.convs.Append(e.ctx, e.user, e.conv.ID, convsvc.RoleUser, convsvc.Content{Text: "is there a change for this?"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.convs.Append(authz.With(e.ctx, Principal), Principal, e.conv.ID, convsvc.RoleAssistant, convsvc.Content{Text: "None fits.", Status: convsvc.StatusDone,
		Actions: []convsvc.Action{{"type": ActionSearchChanges, "args": map[string]any{"q": "this"}, "result": map[string]any{"count": 0}}}}); err != nil {
		t.Fatal(err)
	}
}

func call(name, arguments string) string {
	return fmt.Sprintf(`{"message":"","tool_calls":[{"name":%q,"arguments":%s}]}`, name, arguments)
}

// ---- tests ----------------------------------------------------------------------------------------------------

func TestSendAnswersAndStoresTheTurn(t *testing.T) {
	e := newEnv(t, call(ToolListMethodologies, `{}`), `{"message":"Use sdlc."}`)
	user, pending, err := e.send("what can I do here?", Context{App: App{Project: "PROJ-A", Tab: Tab{Kind: "node"}}, Focus: Focus{Element: &Element{Type: "node", ID: "NODE-1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if user.Role != convsvc.RoleUser || pending.Role != convsvc.RoleAssistant || pending.Status != convsvc.StatusPending || pending.ConversationID != e.conv.ID {
		t.Fatalf("messages %+v %+v", user, pending)
	}
	got := e.answer(t)
	if got.ID != pending.ID || got.Status != convsvc.StatusDone || got.Text != "Use sdlc." || len(got.Actions) != 0 {
		t.Fatalf("answer %+v", got)
	}
	if len(e.model.got) != 2 {
		t.Fatalf("%d model calls", len(e.model.got))
	}
	first, second := e.model.got[0], e.model.got[1]
	if first.Model != "assistant" || first.JSON || len(first.Tools) != len(levelTools[LevelProject])-1 || !strings.Contains(first.System, `"element":{"type":"node","id":"NODE-1"}`) || !strings.Contains(first.System, `"activeProject":"PROJ-A"`) {
		t.Fatalf("request %+v", first)
	}
	for _, m := range e.model.metas { // the ledger of the gateway names the assistant and its conversation (ADR 0089)
		if m.Source != llm.SourceAssistant || m.ConversationID != e.conv.ID {
			t.Fatalf("meta of a model call: %+v", m)
		}
	}
	if e.model.who[0].Subject != "u1" {
		t.Fatalf("the model was called as %+v", e.model.who[0])
	}
	// the tool result went back: sdlc with its description and goal examples, and nothing of another project
	last := second.Messages[len(second.Messages)-1]
	if last.Role != "user" || !strings.Contains(last.Content, `"name":"sdlc"`) || !strings.Contains(last.Content, "Software lifecycle") || !strings.Contains(last.Content, "ship v2") || strings.Contains(last.Content, "risk") {
		t.Fatalf("tool result %q", last.Content)
	}
}

func TestContextIsDescribedNotStored(t *testing.T) {
	e := newEnv(t, `{"message":"ok"}`)
	secret := "my password is hunter2 " + strings.Repeat("x", 500)
	user, _, err := e.send("help", Context{App: App{Project: "PROJ-A", Tab: Tab{Kind: "node", Params: map[string]string{"k": "v"}}}, Focus: Focus{Element: &Element{Type: "node", ID: "NODE-7"}, Selection: secret}})
	if err != nil {
		t.Fatal(err)
	}
	if user.Text != "help" {
		t.Fatalf("text %q", user.Text)
	}
	if user.Context == "" || len(user.Context) > convsvc.MaxContextBytes || strings.Contains(user.Context, "hunter2") || strings.Contains(user.Context, "k") && strings.Contains(user.Context, "v}") {
		t.Fatalf("context %q", user.Context)
	}
	if !strings.Contains(user.Context, "tab node") || !strings.Contains(user.Context, "on node NODE-7") || !strings.Contains(user.Context, "selected characters") {
		t.Fatalf("context %q", user.Context)
	}
	// the model saw the selection for this turn only
	if !strings.Contains(e.model.got[0].System, "hunter2") {
		t.Fatal("the turn does not see its selection")
	}
	for _, m := range e.messages(t) {
		if strings.Contains(m.Text, "hunter2") || strings.Contains(m.Context, "hunter2") {
			t.Fatalf("selection stored: %+v", m)
		}
	}
	// a second turn: the old snapshot is gone from the prompt
	if _, _, err := e.send("again", Context{App: App{Tab: Tab{Kind: "home"}}}); err != nil {
		t.Fatal(err)
	}
	last := e.model.got[len(e.model.got)-1]
	if strings.Contains(last.System, "hunter2") || strings.Contains(last.System, "NODE-7") {
		t.Fatalf("an old context leaked: %s", last.System)
	}
}

func TestUnknownToolIsAnErrorAndNeverRun(t *testing.T) {
	e := newEnv(t, call("delete_everything", `{"x":1}`), `{"message":"I cannot."}`)
	if _, _, err := e.send("do it", Context{}); err != nil {
		t.Fatal(err)
	}
	last := e.model.got[1].Messages[len(e.model.got[1].Messages)-1].Content
	if !strings.Contains(last, `unknown tool \"delete_everything\"`) {
		t.Fatalf("result %q", last)
	}
	if a := e.answer(t); a.Status != convsvc.StatusDone || a.Text != "I cannot." || len(a.Actions) != 0 || len(e.graph.created) != 0 {
		t.Fatalf("%+v", a)
	}
}

func TestSelectProject(t *testing.T) {
	e := newEnv(t,
		call(ToolSelectProject, `{"project":"PROJ-SECRET"}`), // exists, no access
		call(ToolSelectProject, `{"project":"PROJ-NOPE"}`),   // does not exist
		call(ToolSelectProject, `{"project":"PROJ-B"}`),
		`{"message":"Switched."}`)
	if _, _, err := e.send("go to B", inProject("PROJ-A")); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"PROJ-SECRET", "PROJ-NOPE"} {
		res := e.model.got[i+1].Messages[len(e.model.got[i+1].Messages)-1].Content
		if !strings.Contains(res, "does not exist or you may not work on it") || !strings.Contains(res, want) {
			t.Fatalf("round %d result %q", i, res)
		}
	}
	a := e.answer(t)
	if len(a.Actions) != 1 || a.Actions[0]["type"] != "select_project" || a.Actions[0]["args"].(map[string]any)["project"] != "PROJ-B" {
		t.Fatalf("actions %+v", a.Actions)
	}
	// the project selected is the active one of the next round
	if !strings.Contains(e.model.got[3].System, `"activeProject":"PROJ-B"`) {
		t.Fatalf("system %s", e.model.got[3].System)
	}
}

func TestCreateChangeActsAsTheCaller(t *testing.T) {
	e := newEnv(t,
		call(ToolCreateChange, `{"title":"Ship v2","intent":"Customers need it","methodology":"risk"}`), // not applicable to PROJ-A
		call(ToolCreateChange, `{"title":"Ship v2","intent":"Customers need it","methodology":"sdlc"}`),
		`{"message":"Created."}`)
	e.searched(t)
	if _, _, err := e.send("start a change", inProject("PROJ-A")); err != nil {
		t.Fatal(err)
	}
	if res := e.model.got[1].Messages[len(e.model.got[1].Messages)-1].Content; !strings.Contains(res, "does not apply to the active project") {
		t.Fatalf("result %q", res)
	}
	if len(e.graph.created) != 1 {
		t.Fatalf("%d changes created", len(e.graph.created))
	}
	in, who := e.graph.created[0], e.graph.who[0]
	if in.Title != "Ship v2" || in.Intent != "Customers need it" || in.Methodology != "sdlc" || in.Namespace != "alm" || in.ProjectID != "PROJ-A" {
		t.Fatalf("new change %+v", in)
	}
	if who.Subject != "u1" || who.Project != "PROJ-A" || in.Data["createdBy"] != "u1" || in.Data["via"] != "assistant" {
		t.Fatalf("created as %+v data %+v", who, in.Data)
	}
	a := e.answer(t)
	if len(a.Actions) != 1 || a.Actions[0]["type"] != "create_change" || a.Actions[0]["result"].(map[string]any)["changeId"] != "CHG-NEW" {
		t.Fatalf("actions %+v", a.Actions)
	}
}

func TestCreateChangeInAProjectTheCallerCannotUse(t *testing.T) {
	e := newEnv(t, call(ToolCreateChange, `{"title":"x","intent":"y"}`), `{"message":"no"}`)
	e.user.Project = "PROJ-SECRET" // a stale claim; the project of the context is not usable either
	e.ctx = authz.With(context.Background(), e.user)
	e.searched(t)
	// the context names the principal's own project (not re-checked at Send): the tool refuses it
	if _, _, err := e.send("hi", inProject("PROJ-SECRET")); err != nil {
		t.Fatalf("the principal's own project is not re-checked: %v", err)
	}
	if len(e.graph.created) != 0 {
		t.Fatal("a change was created in a project the caller may not use")
	}
	if res := e.model.got[1].Messages[len(e.model.got[1].Messages)-1].Content; !strings.Contains(res, "you may not work on project") {
		t.Fatalf("result %q", res)
	}
}

func TestCreateChangeRefusedByTheGraph(t *testing.T) {
	e := newEnv(t, call(ToolCreateChange, `{"title":"x","intent":"y"}`), `{"message":"It failed."}`)
	e.graph.err = errors.New("boom")
	e.searched(t)
	if _, _, err := e.send("go", Context{}); err != nil {
		t.Fatal(err)
	}
	if a := e.answer(t); a.Status != convsvc.StatusDone || len(a.Actions) != 0 {
		t.Fatalf("%+v", a)
	}
}

func TestSendRefusesAProjectTheCallerCannotUse(t *testing.T) {
	e := newEnv(t, `{"message":"x"}`)
	_, _, err := e.send("hi", inProject("PROJ-SECRET"))
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("got %v", err)
	}
	if len(e.messages(t)) != 0 {
		t.Fatal("something was appended")
	}
}

func TestOpenChange(t *testing.T) {
	e := newEnv(t,
		call(ToolOpenChange, `{"changeId":"CHG-404"}`),
		call(ToolOpenChange, `{"changeId":"CHG-HERS"}`),
		call(ToolOpenChange, `{"changeId":"CHG-1"}`),
		`{"message":"Opened."}`)
	if _, _, err := e.send("open CHG-1", Context{}); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"CHG-404", "CHG-HERS"} {
		res := e.model.got[i+1].Messages[len(e.model.got[i+1].Messages)-1].Content
		if !strings.Contains(res, "does not exist or is not visible") || !strings.Contains(res, id) {
			t.Fatalf("round %d result %q", i, res)
		}
	}
	a := e.answer(t)
	if len(a.Actions) != 1 || a.Actions[0]["type"] != "open_change" || a.Actions[0]["args"].(map[string]any)["changeId"] != "CHG-1" {
		t.Fatalf("actions %+v", a.Actions)
	}
	if a.Actions[0]["result"].(map[string]any)["project"] != "PROJ-A" {
		t.Fatalf("result %+v", a.Actions[0]["result"])
	}
}

func TestOneTurnAtATime(t *testing.T) {
	e := newEnv(t, `{"message":"first"}`, `{"message":"second"}`)
	e.svc.Go = func(f func()) { e.later = append(e.later, f) }
	if _, _, err := e.send("one", Context{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.send("two", Context{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("got %v", err)
	}
	if n := len(e.messages(t)); n != 2 {
		t.Fatalf("%d messages", n)
	}
	// the busy mark of the process is gone (another replica sees the pending message instead)
	e.later[0]()
	if a := e.answer(t); a.Text != "first" || a.Status != convsvc.StatusDone {
		t.Fatalf("%+v", a)
	}
	if _, _, err := e.send("two", Context{}); err != nil {
		t.Fatal(err)
	}
	e.later[1]()
	if a := e.answer(t); a.Text != "second" {
		t.Fatalf("%+v", a)
	}
}

func TestPendingFromAnotherProcessBlocksUntilStale(t *testing.T) {
	e := newEnv(t, `{"message":"ok"}`)
	if _, err := e.convs.Append(e.ctx, Principal, e.conv.ID, convsvc.RoleAssistant, convsvc.Content{Status: convsvc.StatusPending}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.send("hi", Context{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("got %v", err)
	}
	// a pending message that died with its process no longer blocks
	e.svc.Now = func() time.Time { return time.Now().Add(StaleAfter + time.Minute) }
	if _, _, err := e.send("hi", Context{}); err != nil {
		t.Fatal(err)
	}
}

func TestAliasUnavailable(t *testing.T) {
	e := newEnv(t, `{"message":"x"}`)
	e.model.aliases = []modelgw.AliasEntry{{Alias: "helper", Target: "fake/echo"}}
	if _, _, err := e.send("hi", Context{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
	if len(e.messages(t)) != 0 || len(e.model.got) != 0 {
		t.Fatal("nothing must be appended nor called")
	}
	if rpcErr(ErrUnavailable).(*connect.Error).Code() != connect.CodeFailedPrecondition {
		t.Fatal("not FailedPrecondition")
	}
}

func TestHistoryIsCapped(t *testing.T) {
	e := newEnv(t, `{"message":"ok"}`)
	big := strings.Repeat("w", 3000)
	for i := 0; i < 20; i++ {
		role := convsvc.RoleUser
		var p authz.Principal = e.user
		if i%2 == 1 {
			role, p = convsvc.RoleAssistant, Principal
		}
		if _, err := e.convs.Append(authz.With(e.ctx, p), p, e.conv.ID, role, convsvc.Content{Text: fmt.Sprintf("%02d%s", i, big)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := e.send("latest", Context{}); err != nil {
		t.Fatal(err)
	}
	msgs := e.model.got[0].Messages
	total := 0
	for _, m := range msgs {
		total += len(m.Content)
	}
	if len(msgs) > MaxHistoryMessages || total > MaxHistoryBytes+MaxTextBytes {
		t.Fatalf("%d messages, %d bytes sent", len(msgs), total)
	}
	if msgs[0].Role != "user" || msgs[len(msgs)-1].Content != "latest" || msgs[len(msgs)-1].Role != "user" {
		t.Fatalf("shape %+v", msgs)
	}
	if !strings.HasPrefix(msgs[len(msgs)-2].Content, "19") && !strings.HasPrefix(msgs[len(msgs)-2].Content, "18") {
		t.Fatalf("the newest messages are kept: %.5q", msgs[len(msgs)-2].Content)
	}
}

func TestModelFailureWritesStatusError(t *testing.T) {
	e := newEnv(t, "unused")
	e.model.err = modelgw.ErrQuotaExceeded
	if _, _, err := e.send("hi", Context{}); err != nil {
		t.Fatal(err)
	}
	a := e.answer(t)
	if a.Status != convsvc.StatusError || !strings.Contains(a.Error, "quota exceeded") || a.Text != "" {
		t.Fatalf("%+v", a)
	}
	// the conversation is free again
	e.model.err = nil
	e.model.answers = []string{`{"message":"back"}`}
	if _, _, err := e.send("again", Context{}); err != nil {
		t.Fatal(err)
	}
	if a := e.answer(t); a.Text != "back" {
		t.Fatalf("%+v", a)
	}
}

func TestRoundsAreCapped(t *testing.T) {
	e := newEnv(t, call(ToolOpenChange, `{"changeId":"CHG-1"}`))
	if _, _, err := e.send("loop", Context{}); err != nil {
		t.Fatal(err)
	}
	if len(e.model.got) != MaxRounds {
		t.Fatalf("%d model calls", len(e.model.got))
	}
	// the last round is offered no tool and told to answer
	last := e.model.got[MaxRounds-1]
	if last.Tools != nil || !strings.Contains(last.System, "last step") || e.model.got[0].Tools == nil {
		t.Fatalf("last round: %d tools, system %q", len(last.Tools), last.System[len(last.System)-200:])
	}
	a := e.answer(t)
	if a.Status != convsvc.StatusError || !strings.Contains(a.Error, "did not finish") {
		t.Fatalf("%+v", a)
	}
}

func TestLastRoundAnswersWithWhatItHas(t *testing.T) {
	answers := make([]string, MaxRounds)
	for i := range answers[:MaxRounds-1] {
		answers[i] = call(ToolListMethodologies, `{}`)
	}
	answers[MaxRounds-1] = `{"message":"Here is what I found so far."}`
	e := newEnv(t, answers...)
	if _, _, err := e.send("dig", Context{}); err != nil {
		t.Fatal(err)
	}
	if a := e.answer(t); a.Status != convsvc.StatusDone || a.Text != "Here is what I found so far." || len(e.model.got) != MaxRounds {
		t.Fatalf("%d calls, %+v", len(e.model.got), a)
	}
}

func TestCancelStopsTheTurnInThisProcess(t *testing.T) {
	e := newEnv(t, call(ToolOpenChange, `{"changeId":"CHG-1"}`))
	e.model.onCall = func(n int) {
		if n == 2 {
			m, err := e.svc.Cancel(e.ctx, e.conv.ID)
			if err != nil || m.Status != convsvc.StatusCancelled {
				t.Errorf("cancel: %+v %v", m, err)
			}
		}
	}
	if _, _, err := e.send("loop", Context{}); err != nil {
		t.Fatal(err)
	}
	if len(e.model.got) != 2 {
		t.Fatalf("%d model calls after the stop", len(e.model.got))
	}
	// what the turn recorded stays, never an answer
	a := e.answer(t)
	if a.Status != convsvc.StatusCancelled || a.Text != "" || len(a.Actions) == 0 {
		t.Fatalf("%+v", a)
	}
	// the conversation is free again, and the model is told the answer was stopped
	e.model.onCall, e.model.answers, e.model.got = nil, []string{`{"message":"ok"}`}, nil
	if _, _, err := e.send("again", Context{}); err != nil {
		t.Fatal(err)
	}
	if a := e.answer(t); a.Status != convsvc.StatusDone || a.Text != "ok" {
		t.Fatalf("%+v", a)
	}
	var sent strings.Builder
	for _, m := range e.model.got[0].Messages {
		sent.WriteString(m.Content)
	}
	if !strings.Contains(sent.String(), "stopped this answer") {
		t.Fatalf("history: %q", sent.String())
	}
}

func TestCancelFromAnotherProcess(t *testing.T) {
	e := newEnv(t, call(ToolOpenChange, `{"changeId":"CHG-1"}`))
	other := &Service{Convs: e.convs} // a replica serving the Cancel: the turn is not its own
	e.model.onCall = func(n int) {
		if n == 3 {
			if _, err := other.Cancel(e.ctx, e.conv.ID); err != nil {
				t.Error(err)
			}
		}
	}
	if _, _, err := e.send("loop", Context{}); err != nil {
		t.Fatal(err)
	}
	// the turn sees its message cancelled before its next round
	if len(e.model.got) != 3 {
		t.Fatalf("%d model calls", len(e.model.got))
	}
	if a := e.answer(t); a.Status != convsvc.StatusCancelled || len(a.Actions) == 0 {
		t.Fatalf("%+v", a)
	}
}

func TestCancelWithNothingPending(t *testing.T) {
	e := newEnv(t, `{"message":"done"}`)
	if _, _, err := e.send("hi", Context{}); err != nil {
		t.Fatal(err)
	}
	m, err := e.svc.Cancel(e.ctx, e.conv.ID)
	if err != nil || m.Status != convsvc.StatusDone || m.Text != "done" {
		t.Fatalf("%+v %v", m, err)
	}
	// someone else's conversation
	other := authz.Principal{Subject: "u2"}
	if _, err := e.svc.Cancel(authz.With(context.Background(), other), e.conv.ID); err == nil {
		t.Fatal("cancelled the conversation of another")
	}
}

func TestToolCallsOfARoundAreCapped(t *testing.T) {
	calls := strings.Repeat(`{"name":"open_change","arguments":{"changeId":"CHG-1"}},`, MaxToolCalls+1)
	e := newEnv(t, `{"message":"","tool_calls":[`+strings.TrimSuffix(calls, ",")+`]}`, `{"message":"ok"}`)
	if _, _, err := e.send("many", Context{}); err != nil {
		t.Fatal(err)
	}
	if a := e.answer(t); len(a.Actions) != MaxToolCalls {
		t.Fatalf("%d actions", len(a.Actions))
	}
}

func TestAnswerThatIsNotJSONIsTheAnswer(t *testing.T) {
	e := newEnv(t, "Just text, no JSON.")
	if _, _, err := e.send("hi", Context{}); err != nil {
		t.Fatal(err)
	}
	if a := e.answer(t); a.Text != "Just text, no JSON." || a.Status != convsvc.StatusDone {
		t.Fatalf("%+v", a)
	}
}

func TestSendValidation(t *testing.T) {
	e := newEnv(t, `{"message":"x"}`)
	if _, _, err := e.send("  ", Context{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty: %v", err)
	}
	if _, _, err := e.send(strings.Repeat("x", MaxTextBytes+1), Context{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("long: %v", err)
	}
	if _, _, err := e.send("hi", Context{Focus: Focus{Selection: strings.Repeat("x", MaxSelectionBytes+1)}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("selection: %v", err)
	}
	if _, _, err := e.svc.Send(e.ctx, SendInput{ConversationID: "CONV-nope", Text: "hi"}); !errors.Is(err, convsvc.ErrNotFound) {
		t.Fatalf("unknown conversation: %v", err)
	}
	other := authz.With(context.Background(), authz.Principal{Subject: "u2"})
	if _, _, err := e.svc.Send(other, SendInput{ConversationID: e.conv.ID, Text: "hi"}); !errors.Is(err, convsvc.ErrNotFound) {
		t.Fatalf("someone else's conversation: %v", err)
	}
	if _, _, err := e.svc.Send(context.Background(), SendInput{ConversationID: e.conv.ID, Text: "hi"}); !errors.Is(err, convsvc.ErrAnonymous) {
		t.Fatalf("anonymous: %v", err)
	}
	if _, _, err := e.svc.Send(authz.With(context.Background(), authz.System("x")), SendInput{ConversationID: e.conv.ID, Text: "hi"}); !errors.Is(err, convsvc.ErrForbidden) {
		t.Fatalf("system: %v", err)
	}
	if len(e.messages(t)) != 0 {
		t.Fatal("a refused request appended something")
	}
}

func TestHandler(t *testing.T) {
	e := newEnv(t, `{"message":"hello"}`)
	h := &Handler{Service: e.svc, Identity: identity.Extractor{Default: &e.user}}
	mux := http.NewServeMux()
	mux.Handle(assistantv1connect.NewAssistantServiceHandler(h))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cl := assistantv1connect.NewAssistantServiceClient(srv.Client(), srv.URL)
	r, err := cl.Send(context.Background(), connect.NewRequest(&assistantv1.SendRequest{ConversationId: e.conv.ID, Text: "hi",
		Context: &assistantv1.PageContext{App: &assistantv1.AppContext{Project: "PROJ-A", Tab: &assistantv1.PageTab{Kind: "change", Params: map[string]string{"id": "CHG-1"}}}}}))
	if err != nil {
		t.Fatal(err)
	}
	if r.Msg.UserMessage.Text != "hi" || r.Msg.AssistantMessage.Status != "pending" || !strings.Contains(r.Msg.UserMessage.Context, "tab change") {
		t.Fatalf("%+v", r.Msg)
	}
	if a := e.answer(t); a.Text != "hello" {
		t.Fatalf("%+v", a)
	}
	e.model.aliases = nil
	_, err = cl.Send(context.Background(), connect.NewRequest(&assistantv1.SendRequest{ConversationId: e.conv.ID, Text: "again"}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("got %v", err)
	}
}

// A change is never created without a project (ADR 0091): outside any project the assistant has only the tools that
// find one; once it selected a project, the next step has the tools of that project and creates the change there.
func TestOutsideAnyProjectSelectingOneOffersTheRest(t *testing.T) {
	e := newEnv(t, call(ToolCreateChange, `{"title":"x","intent":"y"}`), call(ToolListProjects, `{}`), call(ToolSelectProject, `{"project":"PROJ-A"}`),
		call(ToolCreateChange, `{"title":"x","intent":"y"}`), `{"message":"Created."}`)
	e.user.Project = ""
	e.ctx = authz.With(context.Background(), e.user)
	e.svc.Projects = subProjectsOf{fakeProjects: e.svc.Projects.(fakeProjects), subs: map[string][]string{"PROJ-ROOT": {"PROJ-A", "PROJ-B", "PROJ-SECRET"}}}
	e.searched(t)
	if _, _, err := e.send("go", Context{}); err != nil {
		t.Fatal(err)
	}
	if got := toolNames(e.model.got[0].Tools); !slices.Equal(got, levelTools[LevelNone]) || !strings.Contains(e.model.got[0].System, "level: none") {
		t.Fatalf("tools outside any project %v", got)
	}
	if res := e.toolResult(1); !strings.Contains(res, "not available at level none") {
		t.Fatalf("create_change outside any project: %s", res)
	}
	if res := e.toolResult(2); !strings.Contains(res, `"project":"PROJ-A"`) || !strings.Contains(res, `{"project":"PROJ-A","methodologies":["sdlc","delivery"]},{"project":"PROJ-B"`) || strings.Contains(res, "SECRET") {
		t.Fatalf("list_projects: %s", res)
	}
	if got := toolNames(e.model.got[3].Tools); !slices.Contains(got, ToolCreateChange) || !strings.Contains(e.model.got[3].System, "project: PROJ-A") {
		t.Fatalf("tools after select_project %v", got)
	}
	if len(e.graph.created) != 1 || e.graph.created[0].ProjectID != "PROJ-A" {
		t.Fatalf("created %+v", e.graph.created)
	}
	if a := e.answer(t); a.Context != "project PROJ-A" {
		t.Fatalf("context of the answer %q", a.Context)
	}
}

// Opening a change moves the turn to it: the next step has the tools of a change, and the answer says where it worked.
func TestOpeningAChangeOffersItsTools(t *testing.T) {
	e := newEnv(t, call(ToolOpenChange, `{"changeId":"CHG-D"}`), call(ToolListAgents, `{}`), `{"message":"Here."}`)
	e.user.Project = ""
	e.ctx = authz.With(context.Background(), e.user)
	if _, _, err := e.send("work on the delivery change", Context{}); err != nil {
		t.Fatal(err)
	}
	if got := toolNames(e.model.got[1].Tools); !slices.Contains(got, ToolAttachChange) || !strings.Contains(e.model.got[1].System, "level: change") {
		t.Fatalf("tools after open_change %v", got)
	}
	if res := e.toolResult(2); strings.Contains(res, "not available") {
		t.Fatalf("list_agents on the opened change: %s", res)
	}
	if a := e.answer(t); !strings.HasPrefix(a.Context, "project ") || !strings.Contains(a.Context, "change CHG-D (delivery)") {
		t.Fatalf("context of the answer %q", a.Context)
	}
}

// A round whose tools only hand something to the person (open a change) and all succeeded, with a message, is the final
// answer: no second model call. Anything that returns data to build on, or fails, or comes without a message, goes on.
func TestSettlingToolsWithAMessageSaveTheLastModelCall(t *testing.T) {
	withMsg := func(name, args string) string {
		return fmt.Sprintf(`{"message":"On it.","tool_calls":[{"name":%q,"arguments":%s}]}`, name, args)
	}
	cases := []struct {
		name   string
		first  string
		calls  int
		answer string
	}{
		{"settling tool and message", withMsg(ToolOpenChange, `{"changeId":"CHG-1"}`), 1, "On it."},
		{"no message", call(ToolOpenChange, `{"changeId":"CHG-1"}`), 2, "after"},
		{"data tool", withMsg(ToolListMethodologies, `{}`), 2, "after"},
		{"failed settling tool", withMsg(ToolOpenChange, `{"changeId":"CHG-NOPE"}`), 2, "after"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, c.first, `{"message":"after"}`)
			if _, _, err := e.send("open it", inProject("PROJ-A")); err != nil {
				t.Fatal(err)
			}
			if got := len(e.model.got); got != c.calls {
				t.Fatalf("%d model calls, want %d", got, c.calls)
			}
			if a := e.answer(t); a.Status != convsvc.StatusDone || a.Text != c.answer {
				t.Fatalf("answer %+v, want %q", a, c.answer)
			}
		})
	}
}

// The part of a turn that is ours (context sanitising, prompt, tools, conversation writes) must stay negligible next to
// the model call (about a second): measured with a model answering at once, a 40-entity context and the tools of a
// real turn. `go test -run TurnOverhead -v` prints the figures.
func TestTurnOverheadIsNegligible(t *testing.T) {
	const turns = 50
	ent := make([]Entity, 40)
	for i := range ent {
		ent[i] = Entity{Type: "impact", ID: fmt.Sprintf("IMP-%d", i), Label: fmt.Sprintf("key-%d", i), State: "proposed", Props: map[string]string{"type": "alm@Requirement", "intent": "created"}}
	}
	c := Context{App: App{Project: "PROJ-A", Change: "CHG-D", Tab: Tab{Kind: "change", Params: map[string]string{"id": "CHG-D"}}}, Screen: Screen{Kind: "change", Title: "t", Summary: "s", Entities: ent}}
	e := newEnv(t, call(ToolListAgents, `{}`), call(ToolListMethodologies, `{}`), `{"message":"done"}`)
	var sendMs, total time.Duration
	for i := 0; i < turns; i++ {
		e.model.got = nil
		start := time.Now()
		s0 := time.Now()
		if _, _, err := e.send(fmt.Sprintf("message %d", i), c); err != nil { // the turn runs inline (Go: f())
			t.Fatal(err)
		}
		sendMs += time.Since(s0)
		total += time.Since(start)
	}
	per := total / turns
	t.Logf("a turn (Send + 3 model rounds with tools, model excluded): %v; system prompt %d bytes", per, len(e.model.got[0].System))
	if per > 20*time.Millisecond {
		t.Fatalf("a turn costs %v of our own time", per)
	}
}
