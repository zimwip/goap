package assistantsvc

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/zimwip/goap/internal/convsvc"
	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/llm"
)

func toolNames(specs []llm.ToolSpec) []string {
	var out []string
	for _, s := range specs {
		out = append(out, s.Name)
	}
	return out
}

func hasNewChange(specs []llm.ToolSpec) bool {
	i := slices.IndexFunc(specs, func(s llm.ToolSpec) bool { return s.Name == ToolStartAgent })
	if i < 0 {
		return false
	}
	_, ok := specs[i].InputSchema["properties"].(map[string]any)["newChange"]
	return ok
}

// At level project the tools are those of a project; create_change and a new change on start_agent wait for a search.
func TestCreateChangeWaitsForASearch(t *testing.T) {
	e := newEnv(t,
		call(ToolCreateChange, `{"title":"Ship v2","intent":"Customers need it"}`),
		call(ToolStartAgent, proposeArgs(newChangeArg)),
		`{"message":"Search first."}`)
	if _, _, err := e.send("start a change", inProject("PROJ-A")); err != nil {
		t.Fatal(err)
	}
	want := []string{ToolSelectProject, ToolOpenChange, ToolListMethodologies, ToolMethodologyQuery, ToolSearchChanges, ToolListAgents, ToolStartAgent}
	if got := toolNames(e.model.got[0].Tools); !slices.Equal(got, want) || hasNewChange(e.model.got[0].Tools) {
		t.Fatalf("tools %v (newChange %v)", got, hasNewChange(e.model.got[0].Tools))
	}
	if res := e.toolResult(1); !strings.Contains(res, "not available yet") || !strings.Contains(res, "search_changes") || !strings.Contains(res, "list_agents") {
		t.Fatalf("create_change: %s", res)
	}
	if res := e.toolResult(2); !strings.Contains(res, "only once the open changes were searched") {
		t.Fatalf("start_agent: %s", res)
	}
	sys := e.model.got[0].System
	if !strings.Contains(sys, "level: project") || !strings.Contains(sys, "project: PROJ-A") || !strings.Contains(sys, "server tools of this turn: select_project") ||
		!strings.Contains(sys, "search the open changes (search_changes) on the intent") {
		t.Fatalf("system %s", sys)
	}
	if len(e.graph.created) != 0 || len(e.answer(t).Actions) != 0 {
		t.Fatal("something was created or proposed")
	}
}

// A search in the turn offers create_change to the next rounds of the same turn.
func TestCreateChangeAfterASearchInTheTurn(t *testing.T) {
	e := newEnv(t,
		call(ToolSearchChanges, `{"q":"ship version two"}`),
		call(ToolCreateChange, `{"title":"Ship v2","intent":"Customers need it"}`),
		`{"message":"Created."}`)
	if _, _, err := e.send("start a change", inProject("PROJ-A")); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(toolNames(e.model.got[0].Tools), ToolCreateChange) {
		t.Fatal("create_change offered before the search")
	}
	if !slices.Contains(toolNames(e.model.got[1].Tools), ToolCreateChange) || !hasNewChange(e.model.got[1].Tools) {
		t.Fatalf("round 2 tools %v", toolNames(e.model.got[1].Tools))
	}
	if len(e.graph.created) != 1 {
		t.Fatalf("created %+v (%s)", e.graph.created, e.toolResult(2))
	}
	a := e.answer(t)
	if len(a.Actions) != 2 || a.Actions[0]["type"] != ActionSearchChanges || a.Actions[1]["type"] != ToolCreateChange {
		t.Fatalf("actions %+v", a.Actions)
	}
	// the next turn knows from the history that it searched
	e.model.answers, e.model.got = []string{`{"message":"ok"}`}, nil
	if _, _, err := e.send("and another", inProject("PROJ-A")); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(toolNames(e.model.got[0].Tools), ToolCreateChange) {
		t.Fatal("the search of an earlier turn is forgotten")
	}
	if last := e.model.got[0].Messages; !strings.Contains(last[len(last)-2].Content, `searched the open changes for "ship version two": 0 found`) {
		t.Fatalf("history %q", last[len(last)-2].Content)
	}
}

// A search of an earlier turn, read from the history, offers create_change at once.
func TestCreateChangeAfterASearchInTheHistory(t *testing.T) {
	e := newEnv(t, call(ToolCreateChange, `{"title":"Ship v2","intent":"Customers need it"}`), `{"message":"Created."}`)
	e.searched(t)
	if _, _, err := e.send("create it", inProject("PROJ-A")); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(toolNames(e.model.got[0].Tools), ToolCreateChange) || len(e.graph.created) != 1 {
		t.Fatalf("tools %v, created %+v", toolNames(e.model.got[0].Tools), e.graph.created)
	}
	if strings.Contains(e.model.got[0].System, "are offered once the open changes were searched") {
		t.Fatal("the frame still says to search")
	}
}

// On a change the tools are those of a change, whatever was searched; the frame names the change and its methodology.
func TestToolsFollowTheFrameOnAChange(t *testing.T) {
	e := newEnv(t, call(ToolListMethodologies, `{}`), call(ToolCreateChange, `{"title":"t","intent":"i"}`), `{"message":"ok"}`)
	e.searched(t)
	if _, _, err := e.send("what now?", changeCtx("CHG-D")); err != nil {
		t.Fatal(err)
	}
	want := []string{ToolSelectProject, ToolOpenChange, ToolMethodologyQuery, ToolListAgents, ToolStartAgent, ToolSearchChanges}
	if got := toolNames(e.model.got[0].Tools); !slices.Equal(got, want) || hasNewChange(e.model.got[0].Tools) {
		t.Fatalf("tools %v", got)
	}
	for round, tool := range map[int]string{1: ToolListMethodologies, 2: ToolCreateChange} {
		if res := e.toolResult(round); !strings.Contains(res, `not available at level change`) || !strings.Contains(res, tool) || !strings.Contains(res, "search_changes") {
			t.Fatalf("%s: %s", tool, res)
		}
	}
	sys := e.model.got[0].System
	if !strings.Contains(sys, "level: change") || !strings.Contains(sys, "change: CHG-D") || !strings.Contains(sys, "methodology: delivery") {
		t.Fatalf("system %s", sys)
	}
	if len(e.graph.created) != 0 {
		t.Fatal("created on a change")
	}
	// the same person on the project: the tools of a project
	e.model.answers, e.model.got = []string{`{"message":"ok"}`}, nil
	if _, _, err := e.send("and here?", inProject("PROJ-A")); err != nil {
		t.Fatal(err)
	}
	if got := toolNames(e.model.got[0].Tools); !slices.Equal(got, levelTools[LevelProject]) {
		t.Fatalf("tools %v", got)
	}
}

// A change of the context the caller cannot read is no frame: the turn stays at the project, with a note.
func TestUnreadableChangeLeavesTheProjectFrame(t *testing.T) {
	e := newEnv(t, `{"message":"ok"}`)
	if _, _, err := e.send("hi", changeCtx("CHG-HERS")); err != nil {
		t.Fatal(err)
	}
	if got := toolNames(e.model.got[0].Tools); !slices.Contains(got, ToolListMethodologies) {
		t.Fatalf("tools %v", got)
	}
	if sys := e.model.got[0].System; !strings.Contains(sys, "level: project") || !strings.Contains(sys, "note: change \"CHG-HERS\" does not exist") {
		t.Fatalf("system %s", sys)
	}
}

// subProjectsOf adds sub-projects to the fake directory.
type subProjectsOf struct {
	fakeProjects
	subs map[string][]string
}

func (s subProjectsOf) SubProjects(_ context.Context, p string) ([]string, error) {
	return append([]string{p}, s.subs[p]...), nil
}

func fmtCount(a convsvc.Action) string {
	b, _ := json.Marshal(mapOf(a["result"])["count"])
	return string(b)
}

type searchRecorded struct {
	Result struct {
		Total    int         `json:"total"`
		Projects []string    `json:"projects"`
		Changes  []changeHit `json:"changes"`
	} `json:"result"`
	Error string `json:"error"`
}

func searchEnv(t *testing.T, arguments string) (*env, searchRecorded) {
	t.Helper()
	e := newEnv(t, call(ToolSearchChanges, arguments), `{"message":"ok"}`)
	fp := e.svc.Projects.(fakeProjects)
	fp.access["u1"] = append(fp.access["u1"], "PROJ-A1")
	e.svc.Projects = subProjectsOf{fakeProjects: fp, subs: map[string][]string{"PROJ-A": {"PROJ-A1", "PROJ-A2"}}}
	long := strings.Repeat("payment ", 60)
	e.graph.changes = map[domain.ChangeID]domain.Change{
		"C1": {ID: "C1", Title: "Payment gateway upgrade", Intent: "Upgrade the payment provider. " + long, ProjectID: "PROJ-A", Status: domain.ChangeDraft, Methodology: "delivery", State: "design"},
		"C2": {ID: "C2", Title: "Fix login", Intent: "the payment page login fails", ProjectID: "PROJ-A", Status: domain.ChangeActive},
		"C3": {ID: "C3", Title: "Payment upgrade", ProjectID: "PROJ-A", Status: domain.ChangeApplied},
		"C4": {ID: "C4", Title: "Payment upgrade", ProjectID: "PROJ-B", Status: domain.ChangeActive},
		"C5": {ID: "C5", Title: "Upgrade docs", ProjectID: "PROJ-A1", Status: domain.ChangeCommitted},
		"C6": {ID: "C6", Title: "Payment", ProjectID: "PROJ-A2", Status: domain.ChangeActive},
		"C7": {ID: "C7", Title: "Payment upgrade", ProjectID: "PROJ-A", Status: domain.ChangeDraft, OwnerOrg: access.PersonalUnit("u2")},
		"C8": {ID: "C8", Title: "Payment upgrade notes", ProjectID: "PROJ-A", Status: domain.ChangeDraft, OwnerOrg: access.PersonalUnit("u1")},
		"C9": {ID: "C9", Title: "Unrelated", Intent: "nothing to see", ProjectID: "PROJ-A", Status: domain.ChangeDraft},
	}
	if _, _, err := e.send("find it", inProject("PROJ-A")); err != nil {
		t.Fatal(err)
	}
	res := e.toolResult(1)
	var rs []searchRecorded
	if err := json.Unmarshal([]byte(res[strings.Index(res, "[{"):]), &rs); err != nil {
		t.Fatalf("%v in %s", err, res)
	}
	return e, rs[0]
}

func TestSearchChangesRanksAndFilters(t *testing.T) {
	// "to" and "a" are too short to count
	e, r := searchEnv(t, `{"q":"Payment, upgrade to a"}`)
	var ids []string
	for _, h := range r.Result.Changes {
		ids = append(ids, h.ChangeID)
	}
	// C1: 2+2 title, 1+1 intent; C8 (the caller's own personal change): 2+2; C5 (a sub-project): 2; C2: 1. Not C3
	// (applied), C4 (another project), C6 (a sub-project the caller may not use), C7 (personal to someone else), C9 (no
	// word).
	if !slices.Equal(ids, []string{"C1", "C8", "C5", "C2"}) || r.Result.Total != 4 || r.Error != "" {
		t.Fatalf("%v total %d err %q", ids, r.Result.Total, r.Error)
	}
	if !slices.Equal(r.Result.Projects, []string{"PROJ-A", "PROJ-A1"}) {
		t.Fatalf("projects %v", r.Result.Projects)
	}
	c1 := r.Result.Changes[0]
	if c1.Score != 6 || c1.Status != "draft" || c1.State != "design" || c1.Methodology != "delivery" || c1.Project != "PROJ-A" || len(c1.Intent) > 200+len("…") {
		t.Fatalf("hit %+v", c1)
	}
	a := e.answer(t)
	if len(a.Actions) != 1 || a.Actions[0]["type"] != ActionSearchChanges || fmtCount(a.Actions[0]) != "4" {
		t.Fatalf("actions %+v", a.Actions)
	}
	if !searchedIn([]convsvc.Message{a}) {
		t.Fatal("the search is not read back from the message")
	}
}

func TestSearchChangesStatusesAndRefusals(t *testing.T) {
	_, r := searchEnv(t, `{"q":"payment upgrade","status":["applied"]}`)
	if len(r.Result.Changes) != 1 || r.Result.Changes[0].ChangeID != "C3" {
		t.Fatalf("%+v", r)
	}
	for arguments, want := range map[string]string{
		`{"q":"a b"}`:                     "at least one word",
		`{"q":"payment","status":"gone"}`: "unknown status",
		`{"q":"payment","colour":"red"}`:  "unknown argument",
	} {
		_, r := searchEnv(t, arguments)
		if !strings.Contains(r.Error, want) {
			t.Errorf("%s: %+v", arguments, r)
		}
	}
}

func TestSearchWordsAndScore(t *testing.T) {
	if got := searchWords("Fix the LOGIN, fix it; ok"); !slices.Equal(got, []string{"fix", "the", "login"}) {
		t.Fatalf("%v", got)
	}
	if n := searchScore([]string{"login", "page"}, domain.Change{Title: "Login", Intent: "the login page"}); n != 4 {
		t.Fatalf("%d", n)
	}
}
