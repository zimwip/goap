package assistantsvc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	assistantv1 "github.com/zimwip/goap/gen/goap/assistant/v1"
	"github.com/zimwip/goap/gen/goap/assistant/v1/assistantv1connect"
	"github.com/zimwip/goap/internal/convsvc"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/modelgw"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/methodology"
)

// ---- fakes ----------------------------------------------------------------------------------------------------

type fakeModel struct {
	answers []string
	err     error
	aliases []modelgw.AliasEntry
	got     []llm.Request
	who     []authz.Principal
	metas   []llm.CallMeta
}

func (m *fakeModel) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	m.got = append(m.got, req)
	m.metas = append(m.metas, llm.MetaFrom(ctx))
	m.who = append(m.who, authz.From(ctx))
	if m.err != nil {
		return llm.Response{}, m.err
	}
	i := len(m.got) - 1
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
	return domain.Change{ID: "CHG-NEW", Title: in.Title, Methodology: in.Methodology, ProjectID: in.ProjectID}, nil
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
}

func newEnv(t *testing.T, answers ...string) *env {
	t.Helper()
	e := &env{
		model: &fakeModel{answers: answers, aliases: []modelgw.AliasEntry{{Alias: Alias, Target: "fake/echo"}}},
		graph: &fakeGraph{changes: map[domain.ChangeID]domain.Change{
			"CHG-1":    {ID: "CHG-1", Title: "Existing", ProjectID: "PROJ-A", Status: domain.ChangeDraft},
			"CHG-MINE": {ID: "CHG-MINE", Title: "Mine", OwnerOrg: access.PersonalUnit("u1")},
			"CHG-HERS": {ID: "CHG-HERS", Title: "Hers", OwnerOrg: access.PersonalUnit("u2")},
			"CHG-D":    {ID: "CHG-D", Title: "Delivery", Methodology: "delivery", Namespace: "alm", ProjectID: "PROJ-A", Status: domain.ChangeActive, State: "design"},
			"CHG-DONE": {ID: "CHG-DONE", Title: "Done", Methodology: "delivery", Namespace: "alm", ProjectID: "PROJ-A", Status: domain.ChangeApplied},
		}},
		convs: &convsvc.Service{Store: convsvc.NewMemoryStore()},
		user:  authz.Principal{Subject: "u1", Project: "PROJ-A"},
	}
	e.ctx = authz.With(context.Background(), e.user)
	e.svc = &Service{
		Convs: e.convs, Model: e.model, Graph: e.graph,
		Methodologies: fakeMethodologies{
			"sdlc": {Methodology: &methodology.Methodology{Name: "sdlc", Namespace: "alm", Description: "Software lifecycle",
				Goals: []methodology.Goal{{Name: "release", Examples: []string{"ship v2"}}}}},
			"risk":     {Methodology: &methodology.Methodology{Name: "risk", Namespace: "alm"}},
			"delivery": compiled(t, deliveryYAML),
			"other":    compiled(t, otherYAML),
		},
		Projects: fakeProjects{
			projects: map[string][]string{"PROJ-A": {"sdlc", "delivery"}, "PROJ-B": {"sdlc", "risk", "other"}, "PROJ-SECRET": nil},
			access:   map[string][]string{"u1": {"PROJ-A", "PROJ-B"}},
		},
		Go: func(f func()) { f() },
	}
	e.engine = &fakeEngine{held: map[string][]string{"u1": {"developer"}}, methodologies: e.svc.Methodologies.(fakeMethodologies)}
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

func call(name, arguments string) string {
	return fmt.Sprintf(`{"message":"","tool_calls":[{"name":%q,"arguments":%s}]}`, name, arguments)
}

// ---- tests ----------------------------------------------------------------------------------------------------

func TestSendAnswersAndStoresTheTurn(t *testing.T) {
	e := newEnv(t, call(ToolListMethodologies, `{}`), `{"message":"Use sdlc."}`)
	user, pending, err := e.send("what can I do here?", Context{TabKind: "change", Subject: "CHG-1", Project: "PROJ-A"})
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
	if first.Model != "assistant" || !first.JSON || !strings.Contains(first.System, `"subject":"CHG-1"`) || !strings.Contains(first.System, `"activeProject":"PROJ-A"`) {
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
	user, _, err := e.send("help", Context{TabKind: "node", TabParams: map[string]string{"k": "v"}, Subject: "NODE-7", Selection: secret, Project: "PROJ-A"})
	if err != nil {
		t.Fatal(err)
	}
	if user.Text != "help" {
		t.Fatalf("text %q", user.Text)
	}
	if user.Context == "" || len(user.Context) > convsvc.MaxContextBytes || strings.Contains(user.Context, "hunter2") || strings.Contains(user.Context, "k") && strings.Contains(user.Context, "v}") {
		t.Fatalf("context %q", user.Context)
	}
	if !strings.Contains(user.Context, "tab node") || !strings.Contains(user.Context, "about NODE-7") || !strings.Contains(user.Context, "selected characters") {
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
	if _, _, err := e.send("again", Context{TabKind: "home"}); err != nil {
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
	if _, _, err := e.send("go to B", Context{Project: "PROJ-A"}); err != nil {
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
	if _, _, err := e.send("start a change", Context{Project: "PROJ-A"}); err != nil {
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
	// the context names the principal's own project (not re-checked at Send): the tool refuses it
	if _, _, err := e.send("hi", Context{Project: "PROJ-SECRET"}); err != nil {
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
	if _, _, err := e.send("go", Context{}); err != nil {
		t.Fatal(err)
	}
	if a := e.answer(t); a.Status != convsvc.StatusDone || len(a.Actions) != 0 {
		t.Fatalf("%+v", a)
	}
}

func TestSendRefusesAProjectTheCallerCannotUse(t *testing.T) {
	e := newEnv(t, `{"message":"x"}`)
	_, _, err := e.send("hi", Context{Project: "PROJ-SECRET"})
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
	a := e.answer(t)
	if a.Status != convsvc.StatusError || !strings.Contains(a.Error, "did not finish") {
		t.Fatalf("%+v", a)
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
	if _, _, err := e.send("hi", Context{Selection: strings.Repeat("x", MaxSelectionBytes+1)}); !errors.Is(err, ErrInvalid) {
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
		Context: &assistantv1.PageContext{Tab: &assistantv1.PageTab{Kind: "change", Params: map[string]string{"id": "CHG-1"}}, Subject: "CHG-1", Project: "PROJ-A"}}))
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

// A change is never created without a project (ADR 0091): with no active project the assistant creates it in the root one.
func TestCreateChangeWithNoActiveProjectActsInTheRoot(t *testing.T) {
	e := newEnv(t, call(ToolCreateChange, `{"title":"x","intent":"y"}`), `{"message":"Created."}`)
	e.user.Project = ""
	e.ctx = authz.With(context.Background(), e.user)
	if _, _, err := e.send("go", Context{}); err != nil {
		t.Fatal(err)
	}
	if len(e.graph.created) != 1 || e.graph.created[0].ProjectID != "PROJ-ROOT" {
		t.Fatalf("created %+v", e.graph.created)
	}
}
