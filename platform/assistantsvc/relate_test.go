package assistantsvc

import (
	"strings"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

// On a change, attach_change and detach_change record proposals; accepted, they attach or detach the change as the
// person (ADR 0106).
func TestAttachAndDetachProposals(t *testing.T) {
	e := newEnv(t, call(ToolAttachChange, `{"parent":"CHG-R","rationale":"it ships with R1"}`), `{"message":"proposed"}`)
	e.graph.changes["CHG-R"] = domain.Change{ID: "CHG-R", Title: "R1", Methodology: "release-based", ProjectID: "PROJ-A", Status: domain.ChangeActive}
	if _, _, err := e.send("put it in R1", changeCtx("CHG-D")); err != nil {
		t.Fatal(err)
	}
	m := e.answer(t)
	if len(m.Actions) != 1 || m.Actions[0]["type"] != ActionAttachChange || m.Actions[0]["status"] != StatusProposed {
		t.Fatalf("actions %+v", m.Actions)
	}
	if e.graph.changes["CHG-D"].ParentID != "" {
		t.Fatal("a proposal changes nothing")
	}
	got, err := e.confirm(m, DecisionAccept)
	if err != nil {
		t.Fatal(err)
	}
	if got.Actions[0]["status"] != StatusDone || e.graph.changes["CHG-D"].ParentID != "CHG-R" {
		t.Fatalf("accepted: %+v, parent %q", got.Actions[0], e.graph.changes["CHG-D"].ParentID)
	}
	if _, err := e.confirm(m, DecisionAccept); err == nil {
		t.Fatal("decided twice")
	}
	if d := describeActions(got.Actions); !strings.Contains(d, "attach change CHG-D to CHG-R: done") {
		t.Fatalf("history: %s", d)
	}

	// attached, the change is proposed to be detached
	e.model.answers = append(e.model.answers, call(ToolDetachChange, `{}`), `{"message":"proposed"}`)
	if _, _, err := e.send("take it out", changeCtx("CHG-D")); err != nil {
		t.Fatal(err)
	}
	m = e.answer(t)
	if len(m.Actions) != 1 || m.Actions[0]["type"] != ActionDetachChange {
		t.Fatalf("actions %+v", m.Actions)
	}
	if _, err := e.confirm(m, DecisionAccept); err != nil || e.graph.changes["CHG-D"].ParentID != "" {
		t.Fatalf("detach: %v, parent %q", err, e.graph.changes["CHG-D"].ParentID)
	}
}
