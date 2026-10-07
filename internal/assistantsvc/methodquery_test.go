package assistantsvc

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/methodology"
)

const queryYAML = `
name: shop
version: 1.0.0
namespace: alm
description: |
  Sell things online.
  Second line that a summary must drop.
imports: [decisions]
roles:
  - {name: developer, description: Writes code}
  - {name: release_manager}
conditions:
  - {name: built, description: The build exists, expr: 'artifacts.exists(a, a.type == "build")'}
  - {name: released, expr: 'artifacts.exists(a, a.type == "release")'}
actions:
  - {name: build, description: Compile the shop, kind: builtin, builtin: test.emit, effects: {built: true}, params: {kind: artifact, type: build}, roles: [developer]}
  - {name: release, description: Publish the shop, kind: builtin, builtin: test.emit, pre: {built: true}, effects: {released: true}, params: {kind: artifact, type: release}}
goals:
  - {name: built, description: The build exists, examples: ["compile the shop"], pre: {built: true}}
  - {name: ship, description: Released, pre: {released: true}}
agents:
  - name: builder
    description: Builds the software
    examples: ["build it"]
    roles: [developer]
    actions: [build]
    goals: [built]
    triggers:
      - {name: nightly, type: schedule, schedule: "0 2 * * *", goal: built, enabled: true}
  - {name: shipper, description: Ships the release, roles: [release_manager], actions: [build, release], goals: [built, ship]}
processes:
  - name: delivery
    description: From code to release
    steps:
      - name: compile
        description: Build it
        action: build
      - name: publish
        description: Release it
        steps:
          - {name: tag, action: release}
          - {name: announce, description: Tell people}
`

func shop(t *testing.T) *methodology.Methodology {
	t.Helper()
	return compiled(t, queryYAML).Methodology
}

func query(t *testing.T, q Query) QueryResult {
	t.Helper()
	r, err := RunQuery(q, []*methodology.Methodology{shop(t)})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func names(r QueryResult) []string {
	var out []string
	for _, it := range r.Items {
		out = append(out, it["name"].(string))
	}
	return out
}

func TestQueryOverviewCountsAndNames(t *testing.T) {
	r := query(t, Query{})
	want := map[string]int{"agent": 2, "action": 2, "goal": 2, "process": 1, "step": 4, "role": 2, "trigger": 1, "condition": 2, "library": 1}
	for k, v := range want {
		if r.Counts[k] != v {
			t.Errorf("count %s = %d, want %d (%v)", k, r.Counts[k], v, r.Counts)
		}
	}
	if r.Methodology != "shop" || r.Total != 17 || r.Returned != 17 || r.Truncated || r.Next != nil {
		t.Fatalf("%+v", r)
	}
	// the overview gives names only
	if it := r.Items[0]; len(it) != 2 || it["kind"] != "agent" || it["name"] != "builder" {
		t.Fatalf("%v", it)
	}
}

func TestQueryEachKind(t *testing.T) {
	want := map[string][]string{
		"agent":     {"builder", "shipper"},
		"action":    {"build", "release"},
		"goal":      {"built", "ship"},
		"process":   {"delivery"},
		"step":      {"delivery/compile", "delivery/publish", "delivery/publish/tag", "delivery/publish/announce"},
		"role":      {"developer", "release_manager"},
		"trigger":   {"nightly"},
		"condition": {"built", "released"},
		"library":   {"decisions"},
	}
	for kind, w := range want {
		r := query(t, Query{Kind: kind})
		if got := names(r); !slices.Equal(got, w) {
			t.Errorf("%s: %v", kind, got)
		}
		if r.Counts != nil || r.Kind != kind {
			t.Errorf("%s: counts only for the overview: %+v", kind, r)
		}
	}
	if r := query(t, Query{Kind: "method"}); r.Total != 0 || len(r.Items) != 0 {
		t.Fatalf("%+v", r)
	}
	if _, err := RunQuery(Query{Kind: "tool"}, nil); err == nil || !strings.Contains(err.Error(), "agent, action") {
		t.Fatalf("%v", err)
	}
}

func TestQueryNameGlobAndText(t *testing.T) {
	if got := names(query(t, Query{Name: "RELEASE"})); !slices.Equal(got, []string{"release"}) {
		t.Fatalf("exact is case-insensitive: %v", got)
	}
	if got := names(query(t, Query{Kind: "action", Name: "re*"})); !slices.Equal(got, []string{"release"}) {
		t.Fatalf("%v", got)
	}
	if got := names(query(t, Query{Kind: "role", Name: "*_manage?"})); !slices.Equal(got, []string{"release_manager"}) {
		t.Fatalf("%v", got)
	}
	// a step matches its path or its own name
	if got := names(query(t, Query{Kind: "step", Name: "tag"})); !slices.Equal(got, []string{"delivery/publish/tag"}) {
		t.Fatalf("%v", got)
	}
	if got := names(query(t, Query{Kind: "step", Name: "delivery/*/t?g"})); !slices.Equal(got, []string{"delivery/publish/tag"}) {
		t.Fatalf("%v", got)
	}
	// q: name, description and examples
	if got := names(query(t, Query{Q: "compile THE shop"})); !slices.Equal(got, []string{"build", "built"}) {
		t.Fatalf("examples: %v", got)
	}
	if got := names(query(t, Query{Q: "publish"})); !slices.Equal(got, []string{"release", "delivery/publish", "delivery/publish/tag", "delivery/publish/announce"}) {
		t.Fatalf("%v", got)
	}
	if got := names(query(t, Query{Kind: "agent", Q: "ships"})); !slices.Equal(got, []string{"shipper"}) {
		t.Fatalf("%v", got)
	}
}

func TestQueryParent(t *testing.T) {
	if got := names(query(t, Query{Kind: "step", Parent: "delivery"})); !slices.Equal(got, []string{"delivery/compile", "delivery/publish"}) {
		t.Fatalf("%v", got)
	}
	if got := names(query(t, Query{Kind: "step", Parent: "delivery/publish"})); !slices.Equal(got, []string{"delivery/publish/tag", "delivery/publish/announce"}) {
		t.Fatalf("%v", got)
	}
	if got := names(query(t, Query{Kind: "action", Parent: "builder"})); !slices.Equal(got, []string{"build"}) {
		t.Fatalf("%v", got)
	}
	if got := names(query(t, Query{Parent: "shipper"})); !slices.Equal(got, []string{"build", "release", "built", "ship"}) {
		t.Fatalf("%v", got)
	}
	if got := names(query(t, Query{Kind: "trigger", Parent: "builder"})); !slices.Equal(got, []string{"nightly"}) {
		t.Fatalf("%v", got)
	}
	if r := query(t, Query{Parent: "nobody"}); r.Total != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestQueryDetailLevels(t *testing.T) {
	// summary is the default with a kind: a one-line description and the defining fields
	r := query(t, Query{Kind: "action", Name: "build"})
	it := r.Items[0]
	if it["description"] != "Compile the shop" || it["actionKind"] != "builtin" || it["kind"] != "action" || it["code"] != nil || it["params"] != nil {
		t.Fatalf("%v", it)
	}
	// the description of a methodology-sized text is cut to a line in a summary
	m := shop(t)
	m.Agents[0].Description = "first line\nsecond line"
	rr, _ := RunQuery(Query{Kind: "agent", Name: "builder"}, []*methodology.Methodology{m})
	if rr.Items[0]["description"] != "first line" {
		t.Fatalf("%v", rr.Items[0])
	}
	m.Agents[0].Description = strings.Repeat("x", 500)
	rr, _ = RunQuery(Query{Kind: "agent", Name: "builder"}, []*methodology.Methodology{m})
	if d := rr.Items[0]["description"].(string); len(d) > summaryText+4 {
		t.Fatalf("%d", len(d))
	}
	// full: the definition, with the parameters; the steps of a process are named, not nested
	r = query(t, Query{Kind: "action", Name: "build", Detail: DetailFull})
	it = r.Items[0]
	if it["params"] == nil || it["builtin"] != "test.emit" || it["effects"] == nil {
		t.Fatalf("%v", it)
	}
	r = query(t, Query{Kind: "process", Detail: DetailFull})
	if st, _ := r.Items[0]["steps"].([]string); len(st) != 2 || st[0] != "delivery/compile" {
		t.Fatalf("%v", r.Items[0])
	}
	r = query(t, Query{Kind: "step", Name: "delivery/publish", Detail: DetailFull})
	if st, _ := r.Items[0]["steps"].([]string); len(st) != 2 || r.Items[0]["how"] != "steps" {
		t.Fatalf("%v", r.Items[0])
	}
	// names
	r = query(t, Query{Kind: "agent", Detail: DetailNames})
	if len(r.Items[0]) != 2 {
		t.Fatalf("%v", r.Items[0])
	}
	if _, err := RunQuery(Query{Detail: "huge"}, nil); err == nil {
		t.Fatal("unknown detail")
	}
	// the library names its conditions
	r = query(t, Query{Kind: "library"})
	if c, _ := r.Items[0]["conditions"].([]string); len(c) == 0 {
		t.Fatalf("%v", r.Items[0])
	}
}

func TestQueryFieldsProjection(t *testing.T) {
	r := query(t, Query{Kind: "agent", Fields: []string{"roles", "planner"}})
	it := r.Items[0]
	if len(it) != 3 || it["name"] != "builder" || it["roles"] == nil || it["kind"] != "agent" {
		t.Fatalf("%v", it)
	}
	// without a kind, a field of any kind is valid and present where it exists
	r = query(t, Query{Name: "build", Fields: []string{"actionKind", "planner"}})
	if len(r.Items) != 1 || r.Items[0]["actionKind"] != "builtin" {
		t.Fatalf("%+v", r.Items)
	}
	_, err := RunQuery(Query{Kind: "goal", Fields: []string{"pre", "colour"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "colour") {
		t.Fatalf("%v", err)
	}
	if !strings.Contains(err.Error(), "description, examples") {
		t.Fatalf("lists the valid fields: %v", err)
	}
}

func TestQueryPagination(t *testing.T) {
	r := query(t, Query{Kind: "step", Limit: 3})
	if r.Total != 4 || r.Returned != 3 || r.Next == nil || r.Next.Offset != 3 || r.Truncated {
		t.Fatalf("%+v", r)
	}
	r = query(t, Query{Kind: "step", Limit: 3, Offset: 3})
	if r.Returned != 1 || r.Next != nil || r.Offset != 3 || names(r)[0] != "delivery/publish/announce" {
		t.Fatalf("%+v", r)
	}
	if r = query(t, Query{Kind: "step", Offset: 99}); r.Returned != 0 || r.Next != nil || r.Total != 4 {
		t.Fatalf("%+v", r)
	}
	// the limit is capped
	m := &methodology.Methodology{Name: "big"}
	for i := range 120 {
		m.Roles = append(m.Roles, methodology.Role{Name: fmt.Sprintf("r%03d", i)})
	}
	r, _ = RunQuery(Query{Kind: "role", Detail: DetailNames, Limit: 1000}, []*methodology.Methodology{m})
	if r.Returned != MaxQueryLimit || r.Total != 120 || r.Next.Offset != MaxQueryLimit {
		t.Fatalf("%+v", r)
	}
	r, _ = RunQuery(Query{Kind: "role", Detail: DetailNames}, []*methodology.Methodology{m})
	if r.Returned != DefaultQueryLimit {
		t.Fatalf("%d", r.Returned)
	}
	if _, err := RunQuery(Query{Limit: -1}, nil); err == nil {
		t.Fatal("negative limit")
	}
}

func TestQueryByteCap(t *testing.T) {
	m := &methodology.Methodology{Name: "wide"}
	for i := range 50 {
		m.Roles = append(m.Roles, methodology.Role{Name: fmt.Sprintf("role%02d", i), Description: strings.Repeat("d", 400)})
	}
	r, err := RunQuery(Query{Kind: "role", Detail: DetailFull, Fields: nil, Limit: 50}, []*methodology.Methodology{m})
	if err != nil {
		t.Fatal(err)
	}
	// full detail is capped by count before the bytes
	if r.Returned != MaxFullItems || !r.Truncated || r.Next == nil || !strings.Contains(r.Hint, "at most 3") {
		t.Fatalf("%+v", r)
	}
	// a projection of long texts hits the byte cap
	r, _ = RunQuery(Query{Kind: "role", Fields: []string{"description"}, Limit: 50}, []*methodology.Methodology{m})
	b, _ := json.Marshal(r)
	if len(b) > MaxQueryBytes || !r.Truncated || r.Returned >= 50 || r.Returned == 0 || r.Next == nil || r.Next.Offset != r.Returned || !strings.Contains(r.Hint, "narrow") {
		t.Fatalf("%d bytes %+v", len(b), r)
	}
	// a single item over the cap
	m.Roles = []methodology.Role{{Name: "huge", Description: "x"}}
	m.Actions = []methodology.Action{{Name: "a", Params: map[string]any{"k": strings.Repeat("y", 100)}}}
	for i := range 200 {
		m.Actions[0].Params[fmt.Sprintf("k%d", i)] = strings.Repeat("z", 100)
	}
	r, _ = RunQuery(Query{Kind: "action", Detail: DetailFull}, []*methodology.Methodology{m})
	if r.Returned != 0 || !r.Truncated || !strings.Contains(r.Hint, "fields") {
		t.Fatalf("%+v", r)
	}
}

func TestQueryManyMethodologiesNamesThem(t *testing.T) {
	a, b := shop(t), &methodology.Methodology{Name: "other", Roles: []methodology.Role{{Name: "tester"}}}
	r, err := RunQuery(Query{Kind: "role"}, []*methodology.Methodology{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if r.Methodology != "" || r.Counts != nil || r.Total != 3 || r.Items[2]["methodology"] != "other" || r.Items[0]["methodology"] != "shop" {
		t.Fatalf("%+v", r)
	}
}

// ---- the tool, through the model loop --------------------------------------------------------------------------

type recorded struct {
	Result QueryResult `json:"result"`
	Error  string      `json:"error"`
}

func (e *env) queryResult(t *testing.T, round int) recorded {
	t.Helper()
	res := e.toolResult(round)
	var rs []recorded
	if err := json.Unmarshal([]byte(res[strings.Index(res, "[{"):]), &rs); err != nil {
		t.Fatalf("%v in %s", err, res)
	}
	return rs[0]
}

func queryEnv(t *testing.T, args string) (*env, recorded) {
	t.Helper()
	e := newEnv(t, call(ToolMethodologyQuery, args), `{"message":"ok"}`)
	if _, _, err := e.send("what is in it?", inProject("PROJ-A")); err != nil {
		t.Fatal(err)
	}
	return e, e.queryResult(t, 1)
}

func TestMethodologyQueryDefaultsToTheProjectsMethodologies(t *testing.T) {
	e, r := queryEnv(t, `{"kind":"agent"}`)
	if r.Error != "" || r.Result.Total != 3 || r.Result.Methodology != "" {
		t.Fatalf("%+v", r)
	}
	// PROJ-A applies sdlc (no agent) and delivery (3); "other" belongs to PROJ-B only
	for _, it := range r.Result.Items {
		if it["methodology"] != "delivery" {
			t.Fatalf("%v", it)
		}
	}
	if len(e.graph.created) != 0 || e.engine.checks != 0 || len(e.engine.starts) != 0 {
		t.Fatal("read-only: no graph and no engine call")
	}
	// the call is a read: the message holds no UI action
	if a := e.answer(t).Actions; len(a) != 0 {
		t.Fatalf("%v", a)
	}
	if got := e.model.who[1].Subject; got != "u1" {
		t.Fatalf("acts as the caller: %q", got)
	}
}

func TestMethodologyQueryOneMethodology(t *testing.T) {
	_, r := queryEnv(t, `{"methodology":"delivery"}`)
	if r.Error != "" || r.Result.Methodology != "delivery" || r.Result.Counts["agent"] != 3 || r.Result.Counts["action"] != 2 || r.Result.Items[0]["kind"] != "agent" {
		t.Fatalf("%+v", r)
	}
	_, r = queryEnv(t, `{"methodology":"delivery","kind":"action","name":"rel*","detail":"full"}`)
	if r.Error != "" || r.Result.Total != 1 || r.Result.Items[0]["builtin"] != "test.emit" {
		t.Fatalf("%+v", r)
	}
	_, r = queryEnv(t, `{"methodology":"delivery","kind":"agent","parent":"","q":"ships","fields":["roles"],"limit":1,"offset":0}`)
	if r.Error != "" || r.Result.Total != 1 || r.Result.Items[0]["roles"] == nil {
		t.Fatalf("%+v", r)
	}
}

func TestMethodologyQueryRefusesWhatTheProjectDoesNotApply(t *testing.T) {
	_, r := queryEnv(t, `{"methodology":"other"}`)
	if !strings.Contains(r.Error, `methodology "other" does not apply to the active project`) {
		t.Fatalf("%+v", r)
	}
	// the message is the one create_change gives
	e := newEnv(t, call(ToolCreateChange, `{"title":"t","intent":"i","methodology":"other"}`), `{"message":"ok"}`)
	if _, _, err := e.send("x", inProject("PROJ-A")); err != nil {
		t.Fatal(err)
	}
	if r2 := e.queryResult(t, 1); r2.Error != r.Error {
		t.Fatalf("%q != %q", r2.Error, r.Error)
	}
	// an unknown methodology answers the same
	_, r = queryEnv(t, `{"methodology":"nope"}`)
	if !strings.Contains(r.Error, "does not apply") {
		t.Fatalf("%+v", r)
	}
}

func TestMethodologyQueryRefusesBadArguments(t *testing.T) {
	for args, want := range map[string]string{
		`{"kind":"tool"}`:              "unknown kind",
		`{"detail":"huge"}`:            "unknown detail",
		`{"fields":["nope"]}`:          "unknown fields nope",
		`{"fields":"roles"}`:           "list of field names",
		`{"limit":"ten"}`:              "whole number",
		`{"limit":1.5}`:                "whole number",
		`{"offset":-1}`:                "negative",
		`{"colour":"red"}`:             `unknown argument "colour"`,
		`{"kind":"goal","fields":[1]}`: "list of field names",
	} {
		_, r := queryEnv(t, args)
		if !strings.Contains(r.Error, want) {
			t.Errorf("%s: %+v", args, r)
		}
	}
}

func TestMethodologyQueryCountsAsAToolCall(t *testing.T) {
	calls := strings.Repeat(`{"name":"methodology_query","arguments":{"kind":"role"}},`, MaxToolCalls+1)
	e := newEnv(t, `{"message":"","tool_calls":[`+strings.TrimSuffix(calls, ",")+`]}`, `{"message":"ok"}`)
	if _, _, err := e.send("x", inProject("PROJ-A")); err != nil {
		t.Fatal(err)
	}
	if res := e.toolResult(1); !strings.Contains(res, "too many tool calls") {
		t.Fatalf("%s", res)
	}
}

func TestMethodologyQueryLeavesTheOtherToolsAlone(t *testing.T) {
	e := newEnv(t, call(ToolListMethodologies, `{}`), `{"message":"ok"}`)
	if _, _, err := e.send("x", inProject("PROJ-A")); err != nil {
		t.Fatal(err)
	}
	if res := e.toolResult(1); !strings.Contains(res, "Deliver software") || strings.Contains(res, "total") {
		t.Fatalf("%s", res)
	}
	for _, n := range []string{ToolListMethodologies, ToolSelectProject, ToolCreateChange, ToolOpenChange, ToolListAgents, ToolStartAgent} {
		if !slices.Contains(Tools(), n) {
			t.Errorf("%s", n)
		}
	}
	if !strings.Contains(systemRules, "methodology_query:") || !strings.Contains(systemRules, "Always start narrow") {
		t.Fatal("prompt")
	}
}
