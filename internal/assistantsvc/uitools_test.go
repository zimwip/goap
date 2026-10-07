package assistantsvc

import (
	"context"

	"errors"
	"github.com/zimwip/goap/pkg/authz"
	"strings"
	"testing"

	assistantv1 "github.com/zimwip/goap/gen/goap/assistant/v1"
	"github.com/zimwip/goap/internal/convsvc"
	"github.com/zimwip/goap/pkg/llm"
)

func (e *env) sendTools(text string, c Context, tools []UITool) error {
	_, _, err := e.svc.Send(e.ctx, SendInput{ConversationID: e.conv.ID, Text: text, Context: c, UITools: tools})
	return err
}

func filterTool() UITool {
	return UITool{Name: "filter_list", Description: "Filter the list", Guidance: "give the status", Level: LevelEffect,
		Args: UIArgs{Properties: map[string]UIParam{
			"status": {Type: "enum", Enum: []string{"open", "closed"}},
			"tags":   {Type: "array", Items: &UIParam{Type: "string"}},
			"limit":  {Type: "number"},
		}, Required: []string{"status"}}}
}

func setFieldTool() UITool {
	return UITool{Name: "set_field", Description: "Set the title of the change", Guidance: "give the new title", Level: LevelWrite, Target: "field:title",
		Args: UIArgs{Properties: map[string]UIParam{"value": {Type: "string"}, "force": {Type: "boolean"}}, Required: []string{"value"}}}
}

func TestContextCapsAndSanitising(t *testing.T) {
	c := Context{
		App:    App{Project: "PROJ-A\x00", Tab: Tab{Kind: "change", Params: map[string]string{"id": "CHG-1"}}},
		Screen: Screen{Kind: "change", Title: "A\x1b[31mtitle\nline", Summary: strings.Repeat("s", 5000)},
		Focus:  Focus{PendingAction: "typing\x07", Selection: "a\x00b\nc"},
	}
	for i := 0; i < 80; i++ {
		c.Screen.Entities = append(c.Screen.Entities, Entity{Type: "impact", ID: strings.Repeat("i", 90), Label: strings.Repeat("l", 400), State: "draft",
			Props: map[string]string{"a": "1", "b": "2", "c": "3", "d": "4", "e": "5", "f": "6", "g": "7", "h": strings.Repeat("v", 500)}})
	}
	c.Focus.Element = &Element{Type: "impact", ID: "FOCUSED", Label: "the one"}
	c.Screen.Entities = append(c.Screen.Entities, Entity{Type: "impact", ID: "FOCUSED", Label: "the one"})
	for i := 0; i < 20; i++ {
		c.Focus.Errors = append(c.Focus.Errors, strings.Repeat("e", 900))
	}
	o, err := c.normalise()
	if err != nil {
		t.Fatal(err)
	}
	if n := len(o.render("")); n > MaxContextBytes {
		t.Fatalf("%d bytes", n)
	}
	if len(o.Screen.Entities) > MaxEntities || len(o.Focus.Errors) != MaxErrors || len(o.Focus.Errors[0]) > maxErrorBytes || len(o.Screen.Summary) > maxSummaryBytes {
		t.Fatalf("caps: %d entities, %d errors", len(o.Screen.Entities), len(o.Focus.Errors))
	}
	if o.Screen.Entities[0].ID != "FOCUSED" {
		t.Fatalf("the focused entity is not first: %+v", o.Screen.Entities[0])
	}
	if len(o.Screen.Entities[1].Props) > maxProps {
		t.Fatal("props not capped")
	}
	r := o.render("")
	for _, bad := range []string{"\x00", "\x1b", "\x07", "\\u0000"} {
		if strings.Contains(r, bad) {
			t.Fatalf("control character %q in %s", bad, r[:200])
		}
	}
	if o.Focus.Selection != "a b\nc" && o.Focus.Selection != "ab\nc" {
		// control characters are blanked, the newline of a selection is kept
		if !strings.Contains(o.Focus.Selection, "\n") || strings.Contains(o.Focus.Selection, "\x00") {
			t.Fatalf("selection %q", o.Focus.Selection)
		}
	}
	if strings.Contains(o.Screen.Title, "\n") {
		t.Fatalf("title %q", o.Screen.Title)
	}
	// the selection itself is refused beyond its cap
	if _, err := (Context{Focus: Focus{Selection: strings.Repeat("x", MaxSelectionBytes+1)}}).normalise(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("got %v", err)
	}
}

func TestContextIsRenderedFocusFirst(t *testing.T) {
	e := newEnv(t, `{"message":"ok"}`)
	c := Context{App: App{Project: "PROJ-A", Tab: Tab{Kind: "change", Params: map[string]string{"id": "CHG-1"}}},
		Screen: Screen{Kind: "change", Title: "Change CHG-1", Summary: "three impacts", Entities: []Entity{{Type: "impact", ID: "IMP-1", Label: "Impact one", State: "proposed"}}},
		Focus:  Focus{Element: &Element{Type: "impact", ID: "IMP-1", Label: "Impact one"}, PendingAction: "reviewing impact IMP-1 of change CHG-1", Dialog: Dialog{Kind: "review", Title: "Review"}, Errors: []string{"title is required"}}}
	if err := e.sendTools("help", c, nil); err != nil {
		t.Fatal(err)
	}
	sys := e.model.got[0].System
	i := strings.Index(sys, "Context of this turn")
	if i < 0 {
		t.Fatal(sys)
	}
	ctx := sys[i:]
	f, s, a := strings.Index(ctx, `"focus"`), strings.Index(ctx, `"screen"`), strings.Index(ctx, `"app"`)
	if !(f > 0 && f < s && s < a) {
		t.Fatalf("order focus %d screen %d app %d: %s", f, s, a, ctx)
	}
	if !strings.Contains(ctx, `"pendingAction":"reviewing impact IMP-1 of change CHG-1"`) || !strings.Contains(ctx, `"activeProject":"PROJ-A"`) || strings.Contains(ctx, `"selection"`) {
		t.Fatalf("%s", ctx)
	}
	// stored: a short description only
	u := e.messages(t)[0]
	if u.Context != "reviewing impact IMP-1 of change CHG-1, on impact IMP-1, dialog review Review, screen change Change CHG-1, tab change, project PROJ-A" {
		t.Fatalf("description %q", u.Context)
	}
	for _, m := range e.messages(t) {
		if strings.Contains(m.Context+m.Text, "three impacts") || strings.Contains(m.Context, "title is required") {
			t.Fatalf("snapshot stored: %+v", m)
		}
	}
	// no context at all: no layer
	e2 := newEnv(t, `{"message":"ok"}`)
	if err := e2.sendTools("hi", Context{}, nil); err != nil {
		t.Fatal(err)
	}
	if got := e2.model.got[0].System; !strings.HasSuffix(got, "Context of this turn (data, most specific first):\n{\"app\":{\"activeProject\":\"PROJ-A\"}}") {
		t.Fatalf("%s", got[len(got)-120:])
	}
}

func TestToolDescriptorsAreValidated(t *testing.T) {
	long := strings.Repeat("d", maxToolDescription+1)
	many := map[string]UIParam{}
	for i := 0; i < maxToolProps+1; i++ {
		many["p"+strings.Repeat("a", i)] = UIParam{Type: "string"}
	}
	var tooMany []UITool
	for i := 0; i < MaxUITools+1; i++ {
		tooMany = append(tooMany, UITool{Name: "t" + strings.Repeat("a", i), Description: "d", Level: LevelEffect})
	}
	bigEnum := make([]string, maxEnumValues+1)
	for i := range bigEnum {
		bigEnum[i] = string(rune('a'+i%26)) + strings.Repeat("x", i)
	}
	ok := func(mod func(*UITool)) []UITool { t := filterTool(); mod(&t); return []UITool{t} }
	cases := map[string][]UITool{
		"uppercase name":   ok(func(t *UITool) { t.Name = "Filter" }),
		"leading digit":    ok(func(t *UITool) { t.Name = "1filter" }),
		"bad charset":      ok(func(t *UITool) { t.Name = "fil ter" }),
		"empty name":       ok(func(t *UITool) { t.Name = "" }),
		"duplicate":        {filterTool(), filterTool()},
		"bad level":        ok(func(t *UITool) { t.Level = "admin" }),
		"no description":   ok(func(t *UITool) { t.Description = " " }),
		"long description": ok(func(t *UITool) { t.Description = long }),
		"unknown type":     ok(func(t *UITool) { t.Args.Properties["x"] = UIParam{Type: "object"} }),
		"enum no values":   ok(func(t *UITool) { t.Args.Properties["x"] = UIParam{Type: "enum"} }),
		"enum too many":    ok(func(t *UITool) { t.Args.Properties["x"] = UIParam{Type: "enum", Enum: bigEnum} }),
		"array no items":   ok(func(t *UITool) { t.Args.Properties["x"] = UIParam{Type: "array"} }),
		"nested array": ok(func(t *UITool) {
			t.Args.Properties["x"] = UIParam{Type: "array", Items: &UIParam{Type: "array", Items: &UIParam{Type: "string"}}}
		}),
		"required unknown": ok(func(t *UITool) { t.Args.Required = []string{"nope"} }),
		"bad property":     ok(func(t *UITool) { t.Args.Properties["bad name"] = UIParam{Type: "string"} }),
		"too many props":   ok(func(t *UITool) { t.Args.Properties = many }),
		"too many tools":   tooMany,
	}
	for name, tools := range cases {
		e := newEnv(t, `{"message":"ok"}`)
		if err := e.sendTools("hi", Context{}, tools); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: got %v", name, err)
		}
		if len(e.messages(t)) != 0 {
			t.Errorf("%s: a refused request appended something", name)
		}
	}
	if _, err := checkTools([]UITool{filterTool(), setFieldTool()}); err != nil {
		t.Fatal(err)
	}
}

func TestPromptListsTheScreenToolsWithTheirPrefix(t *testing.T) {
	e := newEnv(t, `{"message":"ok"}`)
	if err := e.sendTools("hi", Context{}, []UITool{filterTool(), setFieldTool()}); err != nil {
		t.Fatal(err)
	}
	sys := e.model.got[0].System
	for _, want := range []string{`"name":"ui.filter_list"`, `"name":"ui.set_field"`, `"level":"write"`, `"guidance":"give the status"`, `"target":"field:title"`, "most specific first", "ask one short question"} {
		if !strings.Contains(sys, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(sys, `"name":"filter_list"`) {
		t.Error("an unprefixed name is offered")
	}
}

func TestUIToolNotInTheDescriptorsIsRefused(t *testing.T) {
	e := newEnv(t, call("ui.delete_everything", `{}`)+"", `{"message":"sorry"}`)
	if err := e.sendTools("do it", Context{}, []UITool{filterTool()}); err != nil {
		t.Fatal(err)
	}
	if r := e.toolResult(1); !strings.Contains(r, "offers no tool") || !strings.Contains(r, "ui.filter_list") {
		t.Fatalf("%s", r)
	}
	// a server tool name cannot be shadowed: a screen tool called list_agents is only ever ui.list_agents
	e = newEnv(t, call("ui.list_agents", `{}`), `{"message":"x"}`)
	if err := e.sendTools("do it", Context{}, []UITool{{Name: "list_agents", Description: "d", Level: LevelEffect}}); err != nil {
		t.Fatal(err)
	}
	if a := e.answer(t); len(a.Actions) != 1 || a.Actions[0]["type"] != ActionUITool {
		t.Fatalf("%+v", a.Actions)
	}
	// without descriptors, none exists; and the turn emitted nothing
	e = newEnv(t, call("ui.filter_list", `{"status":"open"}`), `{"message":"x"}`)
	if err := e.sendTools("do it", Context{}, nil); err != nil {
		t.Fatal(err)
	}
	if a := e.answer(t); len(a.Actions) != 0 || !strings.Contains(e.toolResult(1), "none") {
		t.Fatalf("%+v %s", a.Actions, e.toolResult(1))
	}
}

func TestUIToolArgumentsFailingTheSchemaAreRefused(t *testing.T) {
	bad := map[string]string{
		"missing required": `{}`,
		"enum":             `{"status":"later"}`,
		"type":             `{"status":"open","limit":"ten"}`,
		"unknown property": `{"status":"open","extra":1}`,
		"array element":    `{"status":"open","tags":["a",3]}`,
		"not an array":     `{"status":"open","tags":"a"}`,
		"too big":          `{"status":"open","tags":["` + strings.Repeat("x", 2000) + `","` + strings.Repeat("y", 2000) + `","` + strings.Repeat("z", 2000) + `"]}`,
	}
	for name, a := range bad {
		e := newEnv(t, call("ui."+filterTool().Name, a), `{"message":"x"}`)
		if err := e.sendTools("go", Context{}, []UITool{filterTool()}); err != nil {
			t.Fatal(err)
		}
		if got := e.answer(t); len(got.Actions) != 0 || !strings.Contains(e.toolResult(1), `"error"`) {
			t.Errorf("%s: %+v %s", name, got.Actions, e.toolResult(1))
		}
	}
}

func TestEffectToolIsEmittedRequested(t *testing.T) {
	e := newEnv(t, call("ui.filter_list", `{"status":"open","tags":["a","b"]}`), `{"message":"Filtered."}`)
	if err := e.sendTools("show open ones", inProject("PROJ-A"), []UITool{filterTool()}); err != nil {
		t.Fatal(err)
	}
	a := e.answer(t)
	if len(a.Actions) != 1 {
		t.Fatalf("%+v", a)
	}
	act := a.Actions[0]
	if act["type"] != ActionUITool || act["status"] != StatusRequested || act["tool"] != "filter_list" || act["level"] != LevelEffect || !strings.Contains(act["label"].(string), "Filter the list") {
		t.Fatalf("%+v", act)
	}
	if got := mapOf(act["args"]); got["status"] != "open" {
		t.Fatalf("%+v", act["args"])
	}
	if r := e.toolResult(1); !strings.Contains(r, "no result is available") {
		t.Fatalf("%s", r)
	}
	if len(e.graph.created) != 0 || len(e.engine.starts) != 0 {
		t.Fatal("a screen tool ran something on the server")
	}
}

func TestWriteToolIsRecordedProposedWithNoSideEffect(t *testing.T) {
	e := newEnv(t, `{"message":"","tool_calls":[{"name":"ui.set_field","arguments":{"value":"New title"},"rationale":"you asked"}]}`, `{"message":"I propose it."}`)
	if err := e.sendTools("rename it", inProject("PROJ-A"), []UITool{setFieldTool()}); err != nil {
		t.Fatal(err)
	}
	a := e.answer(t)
	if len(a.Actions) != 1 {
		t.Fatalf("%+v", a)
	}
	act := a.Actions[0]
	if act["status"] != StatusProposed || act["level"] != LevelWrite || act["rationale"] != "you asked" || act["project"] != "PROJ-A" || act["target"] != "field:title" {
		t.Fatalf("%+v", act)
	}
	if !strings.Contains(e.toolResult(1), "nothing was changed") {
		t.Fatalf("%s", e.toolResult(1))
	}
	if len(e.graph.created) != 0 || len(e.engine.starts) != 0 {
		t.Fatal("a proposal ran something on the server")
	}
}

func TestAtMostThreeProposalsPerAnswer(t *testing.T) {
	var calls []string
	for i := 0; i < 4; i++ {
		calls = append(calls, `{"name":"ui.set_field","arguments":{"value":"v"}}`)
	}
	e := newEnv(t, `{"message":"","tool_calls":[`+strings.Join(calls, ",")+`]}`, `{"message":"x"}`)
	if err := e.sendTools("rename", Context{}, []UITool{setFieldTool()}); err != nil {
		t.Fatal(err)
	}
	if n := len(e.answer(t).Actions); n != MaxProposalsPerAnswer {
		t.Fatalf("%d proposals", n)
	}
}

// uiProposal runs a turn that ends with a write proposal and returns its message.
func (e *env) uiProposal(t *testing.T, c Context) convsvc.Message {
	t.Helper()
	e.model.answers = []string{call("ui.set_field", `{"value":"New title"}`), `{"message":"I propose it."}`}
	e.model.got = nil
	if err := e.sendTools("rename", c, []UITool{setFieldTool()}); err != nil {
		t.Fatal(err)
	}
	m := e.answer(t)
	if len(m.Actions) != 1 {
		t.Fatalf("%+v", m)
	}
	return m
}

func (e *env) report(m convsvc.Message, status, msg string) (convsvc.Message, error) {
	return e.svc.Report(e.ctx, ReportInput{ConversationID: e.conv.ID, MessageID: m.ID, Status: status, Error: msg})
}

func TestConfirmUIToolAcceptOnlyRecordsTheDecision(t *testing.T) {
	e := newEnv(t)
	m := e.uiProposal(t, inProject("PROJ-A"))
	got, err := e.confirm(m, DecisionAccept)
	if err != nil {
		t.Fatal(err)
	}
	act := got.Actions[0]
	if act["status"] != StatusAccepted || mapOf(act["decided"])["by"] != "u1" || mapOf(act["decided"])["decision"] != DecisionAccept {
		t.Fatalf("%+v", act)
	}
	if len(e.engine.starts) != 0 || len(e.graph.created) != 0 {
		t.Fatal("the server did something on accept")
	}
	if _, err := e.confirm(m, DecisionAccept); !errors.Is(err, ErrDecided) {
		t.Fatalf("double accept: %v", err)
	}
	if _, err := e.confirm(m, DecisionReject); !errors.Is(err, ErrDecided) {
		t.Fatalf("reject after accept: %v", err)
	}
}

func TestConfirmUIToolReject(t *testing.T) {
	e := newEnv(t)
	m := e.uiProposal(t, inProject("PROJ-A"))
	got, err := e.confirm(m, DecisionReject)
	if err != nil || got.Actions[0]["status"] != StatusRejected {
		t.Fatalf("%v %+v", err, got.Actions)
	}
	if _, err := e.confirm(m, DecisionAccept); !errors.Is(err, ErrDecided) {
		t.Fatalf("got %v", err)
	}
	if _, err := e.report(m, StatusDone, ""); !errors.Is(err, ErrState) {
		t.Fatalf("report on a rejected one: %v", err)
	}
}

func TestConfirmUIToolStaleOnAnotherProject(t *testing.T) {
	e := newEnv(t)
	m := e.uiProposal(t, inProject("PROJ-A"))
	_, err := e.svc.Confirm(e.ctx, ConfirmInput{ConversationID: e.conv.ID, MessageID: m.ID, Decision: DecisionAccept, Project: "PROJ-B"})
	if !errors.Is(err, ErrStale) {
		t.Fatalf("got %v", err)
	}
	if got := e.answer(t); got.Actions[0]["status"] != StatusProposed {
		t.Fatalf("not left proposed: %+v", got.Actions)
	}
	// still rejectable
	if got, err := e.svc.Confirm(e.ctx, ConfirmInput{ConversationID: e.conv.ID, MessageID: m.ID, Decision: DecisionReject, Project: "PROJ-B"}); err != nil || got.Actions[0]["status"] != StatusRejected {
		t.Fatalf("%v %+v", err, got.Actions)
	}
}

func TestReportActionOwnerOnlyAndOnlyFromAccepted(t *testing.T) {
	e := newEnv(t)
	m := e.uiProposal(t, inProject("PROJ-A"))
	if _, err := e.report(m, StatusDone, ""); !errors.Is(err, ErrState) {
		t.Fatalf("report before accept: %v", err)
	}
	if _, err := e.report(m, "maybe", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad status: %v", err)
	}
	if _, err := e.confirm(m, DecisionAccept); err != nil {
		t.Fatal(err)
	}
	// another user, and a service
	other := authzUser("u2")
	if _, err := e.svc.Report(other, ReportInput{ConversationID: e.conv.ID, MessageID: m.ID, Status: StatusDone}); !errors.Is(err, convsvc.ErrNotFound) {
		t.Fatalf("another user: %v", err)
	}
	if _, err := e.svc.Report(authzSystem(), ReportInput{ConversationID: e.conv.ID, MessageID: m.ID, Status: StatusDone}); !errors.Is(err, convsvc.ErrForbidden) {
		t.Fatalf("a service: %v", err)
	}
	got, err := e.report(m, StatusFailed, "the field is read-only\x00")
	if err != nil {
		t.Fatal(err)
	}
	if a := got.Actions[0]; a["status"] != StatusFailed || a["error"] != "the field is read-only" || mapOf(a["reported"])["by"] != "u1" {
		t.Fatalf("%+v", a)
	}
	if _, err := e.report(m, StatusDone, ""); !errors.Is(err, ErrDecided) {
		t.Fatalf("second report: %v", err)
	}
	// not a screen tool action
	e.model.answers = []string{call(ToolOpenChange, `{"changeId":"CHG-1"}`), `{"message":"ok"}`}
	if _, _, err := e.send("open", Context{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.report(e.answer(t), StatusDone, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("report on open_change: %v", err)
	}
}

func TestReportActionOnAnEffect(t *testing.T) {
	e := newEnv(t, call("ui.filter_list", `{"status":"open"}`), `{"message":"ok"}`)
	if err := e.sendTools("filter", Context{}, []UITool{filterTool()}); err != nil {
		t.Fatal(err)
	}
	m := e.answer(t)
	if _, err := e.confirm(m, DecisionAccept); err != nil && !errors.Is(err, ErrDecided) {
		t.Fatalf("accept on an effect: %v", err)
	}
	got, err := e.report(m, StatusDone, "")
	if err != nil || got.Actions[0]["status"] != StatusDone {
		t.Fatalf("%v %+v", err, got.Actions)
	}
}

func TestHistoryCarriesDecisionsAndOutcomes(t *testing.T) {
	steps := []struct {
		name string
		do   func(*env, convsvc.Message)
		want string
	}{
		{"undecided", func(*env, convsvc.Message) {}, "has not decided yet, nothing was changed"},
		{"rejected", func(e *env, m convsvc.Message) { e.confirm(m, DecisionReject) }, "rejected the proposal ui.set_field"},
		{"accepted", func(e *env, m convsvc.Message) { e.confirm(m, DecisionAccept) }, "accepted the proposal ui.set_field value=\"New title\": the interface is applying it"},
		{"done", func(e *env, m convsvc.Message) { e.confirm(m, DecisionAccept); e.report(m, StatusDone, "") }, "ui.set_field value=\"New title\" was done"},
		{"failed", func(e *env, m convsvc.Message) { e.confirm(m, DecisionAccept); e.report(m, StatusFailed, "locked") }, "failed in the interface: locked"},
	}
	for _, s := range steps {
		e := newEnv(t)
		m := e.uiProposal(t, inProject("PROJ-A"))
		s.do(e, m)
		e.model.answers, e.model.got = []string{`{"message":"ok"}`}, nil
		if _, _, err := e.send("and then?", inProject("PROJ-A")); err != nil {
			t.Fatal(err)
		}
		var hist string
		for _, m := range e.model.got[0].Messages {
			hist += m.Content + "\n"
		}
		if !strings.Contains(hist, s.want) {
			t.Errorf("%s: history lacks %q: %s", s.name, s.want, hist)
		}
	}
	// an effect
	e := newEnv(t, call("ui.filter_list", `{"status":"open"}`), `{"message":"ok"}`)
	if err := e.sendTools("filter", Context{}, []UITool{filterTool()}); err != nil {
		t.Fatal(err)
	}
	e.model.answers, e.model.got = []string{`{"message":"ok"}`}, nil
	if _, _, err := e.send("and then?", Context{}); err != nil {
		t.Fatal(err)
	}
	if h := e.model.got[0].Messages[1].Content; !strings.Contains(h, "asked to run ui.filter_list") {
		t.Fatalf("%s", h)
	}
}

func TestScreenToolsKeepTheLedgerSourceAssistant(t *testing.T) {
	e := newEnv(t, call("ui.filter_list", `{"status":"open"}`), `{"message":"ok"}`)
	if err := e.sendTools("filter", Context{}, []UITool{filterTool()}); err != nil {
		t.Fatal(err)
	}
	for _, m := range e.model.metas {
		if m.Source != llm.SourceAssistant {
			t.Fatalf("%+v", m)
		}
	}
}

func TestToolsFromTheProtocolMessage(t *testing.T) {
	pb := &assistantv1.UiTool{Name: "x", Description: "d", Level: "write", Target: "t", Args: &assistantv1.UiArgs{Required: []string{"a"},
		Properties: map[string]*assistantv1.UiParam{"a": {Type: "array", Items: &assistantv1.UiParam{Type: "enum", Enum: []string{"u", "v"}}}}}}
	got := toolFromPB(pb)
	if got.Name != "x" || got.Args.Properties["a"].Items == nil || got.Args.Properties["a"].Items.Enum[1] != "v" {
		t.Fatalf("%+v", got)
	}
	if _, err := checkTools([]UITool{got}); err != nil {
		t.Fatal(err)
	}
}

func authzUser(sub string) context.Context {
	return authz.With(context.Background(), authz.Principal{Subject: sub})
}

func authzSystem() context.Context { return authz.With(context.Background(), authz.System("x")) }
