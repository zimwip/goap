package condition

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zimwip/goap/contracts/criticality"
	"github.com/zimwip/goap/contracts/domain"
	graphv1 "github.com/zimwip/goap/contracts/gen/goap/graph/v1"
	"github.com/zimwip/goap/contracts/journal"
	"github.com/zimwip/goap/lib/pbconv"
)

// rustActivationCases is where the Rust graph reads the variables of the conditions Go computes from a blackboard (ADR
// 0163 addendum, what a change follows): the Rust activation of the same wire must be the same.
const rustActivationCases = "../../rust/crates/graph-core/src/conditions/activation_cases.gen.json"

// rustLibraries is where the Rust graph reads the conditions of the libraries a methodology imports (ADR 0064).
const rustLibraries = "../../rust/crates/graph-core/src/conditions/libraries.gen.json"

// TestRustLibrariesAreCurrent writes (GOAP_UPDATE_RUST=1) or checks the libraries of the Rust graph.
func TestRustLibrariesAreCurrent(t *testing.T) {
	type def struct {
		Name        string `json:"name"`
		Expr        string `json:"expr"`
		Description string `json:"description,omitempty"`
	}
	libs := map[string][]def{}
	for _, n := range LibraryNames {
		ds, _ := Library(n)
		for _, d := range ds {
			libs[n] = append(libs[n], def{d.Name, d.Expr, d.Description})
		}
	}
	raw, err := json.MarshalIndent(map[string]any{
		"comment":   "Code generated from engine/condition (GOAP_UPDATE_RUST=1 go test ./engine/condition/ -run TestRustLibrariesAreCurrent). DO NOT EDIT.",
		"libraries": libs, "order": LibraryNames,
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want := string(raw) + "\n"
	if os.Getenv("GOAP_UPDATE_RUST") != "" {
		if err := os.WriteFile(rustLibraries, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if got, err := os.ReadFile(rustLibraries); err != nil || string(got) != want {
		t.Fatalf("%s is not current: run GOAP_UPDATE_RUST=1 go test ./engine/condition/ -run TestRustLibrariesAreCurrent", rustLibraries)
	}
}

// rustBoards are blackboards as the graph answers GetBlackboard, with the facets the graph adds.
var rustBoards = []struct {
	Name  string
	Wire  string
	Vars  map[string]any
	Types map[string][]string
	Deps  []domain.Dependency
	Hist  *journal.History
	// Policy is the criticality policy of the organisation (nil: the compiled-in table)
	Policy *criticality.Policy
}{
	{Name: "empty", Wire: `{"change": {"id": "c1", "status": "draft"}, "at": "2026-03-01T00:00:00Z"}`},
	{
		Name: "header and impacts",
		Wire: `{"change": {"id": "c2", "title": "T <a>", "intent": "Do & see", "status": "active", "methodology": "sdlc",
			"goal": "deliver", "lifecycle": "delivery", "state": "framing", "branch": "change-c2", "baselineId": "b1",
			"resultBaselineId": "", "data": {"criticality": "C3", "n": 2}, "dependsOn": ["d1", "d2"],
			"nodes": [
				{"id": "i1", "key": "REQ-1", "type": "alm@Requirement", "intent": "modified", "rationale": "why",
				 "pre": {"id": "n1", "version": 2}, "post": {"id": "n1"}, "review": "accepted",
				 "reviews": [{"status": "proposed", "by": "a"}, {"status": "accepted", "by": "b", "comment": "ok"}],
				 "via": "", "producedBy": "agent", "items": ["it1"], "recheck": true},
				{"id": "i2", "key": "REQ-2", "type": "alm@Requirement", "intent": "created"},
				{"id": "i3", "key": "REQ-3", "type": "alm@Requirement", "intent": "created", "post": {"id": "n3"}, "via": "i1"}
			]},
			"nodes": [
				{"node": {"id": "n1", "version": 2, "key": "REQ-1", "type": "alm@Requirement", "state": "draft",
				  "props": {"title": "old", "size": 3}}, "latest": 4,
				 "out": [{"id": "l1", "type": "alm@refines", "from": {"id": "n1", "version": 2}, "to": {"id": "n9", "version": 1}}],
				 "in": [{"id": "l2", "type": "alm@traces", "from": {"id": "n8", "version": 5}, "to": {"id": "n1", "version": 2}}]},
				{"node": {"id": "n1", "version": 0, "key": "REQ-1", "type": "alm@Requirement", "state": "review",
				  "props": {"title": "new", "size": 3.5}}, "latest": 4, "notCommittable": true},
				{"node": {"id": "n3", "version": 0, "key": "REQ-3", "type": "alm@Requirement", "deleted": true}}
			],
			"neighbors": [{"id": "n9", "version": 1, "key": "F-1", "type": "alm@Function"}],
			"at": "2026-03-01T00:00:00Z"}`,
		Types: map[string][]string{"alm@Requirement": {"alm@Item"}, "alm@Function": {"alm@Item", "alm@Element"}},
		Vars:  map[string]any{"event": map[string]any{"name": "x", "at": 3.0}},
		Deps: []domain.Dependency{{ID: "d1", Title: "first", Methodology: "sdlc", Status: domain.ChangeApplied, Satisfied: true},
			{ID: "d2", Status: domain.ChangeActive, State: "build", ParentID: "p", Reason: "not merged"}},
		Hist: &journal.History{Runs: []journal.Run{{Process: "p1", Methodology: "sdlc", Agent: "a", Goal: "g", Status: "completed", Seq: 4}},
			Steps: []journal.StepRun{{Path: "proc/a", Methodology: "sdlc", Process: "p1", Action: "act", Execution: "e1",
				Outcome: journal.OutcomeSucceeded, Seq: 5, Runs: 2, Current: true},
				{Path: "proc/b", Process: "p1", Outcome: "failed", Error: "boom", Seq: 7, Runs: 1, StaleBy: "proc/a", Subject: "s"}}},
	},
	{
		Name: "items",
		Wire: `{"change": {"id": "c3", "status": "active", "title": "Title", "intent": "Intent", "items": [
			{"id": "a1", "type": "artifact", "target": "spec", "status": "proposed", "data": {"x": 1}, "producedBy": "p",
			 "derivedFrom": ["a0"], "createdAt": "2026-02-01T10:00:00.5Z"},
			{"id": "a2", "type": "artifact", "target": "spec", "status": "proposed", "supersedes": ["a1"], "createdAt": "2026-02-01T10:00:01Z"},
			{"id": "dc", "type": "decision", "status": "accepted", "decision": {"item": "a2", "accept": false, "comment": "no"},
			 "createdAt": "2026-02-01T10:00:02Z"},
			{"id": "r1", "type": "risk", "status": "proposed", "data": {"key": "R1", "title": "Risk", "probability": 4, "impact": 3,
			 "actions": ["A1"], "owner": " o "}, "producedBy": "x", "createdAt": "2026-02-01T10:00:03Z"},
			{"id": "r2", "type": "risk", "status": "proposed", "data": {"key": "R1", "status": "mitigating"}, "createdAt": "2026-02-01T10:00:04Z"},
			{"id": "r3", "type": "risk", "status": "rejected", "data": {"key": "R2", "title": "Gone"}, "createdAt": "2026-02-01T10:00:05Z"},
			{"id": "r4", "type": "risk", "status": "proposed", "data": {"key": "R3", "title": "Low", "probability": 1, "impact": 1,
			 "status": "closed"}, "createdAt": "2026-02-01T10:00:05Z"},
			{"id": "x1", "type": "action", "status": "proposed", "data": {"key": "A1", "title": "Act", "for": "R1", "risks": ["R3"],
			 "due": "2026-04-01"}, "createdAt": "2026-02-01T10:00:06Z"},
			{"id": "x2", "type": "action", "status": "proposed", "data": {"key": "A2", "title": "Poc", "kind": "poc", "risks": ["R1"],
			 "hypotheses": ["H1"], "status": "done"}, "createdAt": "2026-02-01T10:00:07Z"},
			{"id": "g1", "type": "derogation", "status": "proposed", "data": {"key": "D1", "rule": "covered", "target": "T",
			 "reason": "r", "signatory": "s", "expires": "2026-03-15"}, "createdAt": "2026-02-01T10:00:08Z"},
			{"id": "g2", "type": "derogation", "status": "proposed", "data": {"key": "D2", "rule": "old", "target": "T",
			 "reason": "r", "signatory": "s", "expires": "2026-02-15T12:00:00+02:00"}, "createdAt": "2026-02-01T10:00:09Z"},
			{"id": "g3", "type": "derogation", "status": "proposed", "data": {"key": "D3", "rule": "x", "target": "T",
			 "reason": "r", "signatory": "s", "expires": "2026-01-01", "status": "closed"}, "createdAt": "2026-02-01T10:00:09Z"},
			{"id": "v1", "type": "verification", "status": "proposed", "execution": "e1", "producedBy": "agent",
			 "data": {"state": "produced", "action": "build", "impacts": ["i1", "i2"], "oracle": "tool", "independent": true},
			 "createdAt": "2026-02-01T10:00:10Z"},
			{"id": "v2", "type": "verification", "status": "proposed",
			 "data": {"state": "accepted_with_reserve", "action": "build", "impact": "i1", "derogation": "D1", "by": "rev"},
			 "createdAt": "2026-02-01T10:00:11Z"},
			{"id": "v3", "type": "verification", "status": "proposed", "execution": "e2",
			 "data": {"state": "produced", "action": "test"}, "createdAt": "2026-02-01T10:00:12Z"},
			{"id": "s1", "type": "assessment", "status": "proposed", "producedBy": "rater",
			 "data": {"key": "description", "score": 4, "verdict": "clear", "findings": ["one", {"text": "two", "severity": "high"}],
			 "suggestion": "s", "rated": "Title\n\nIntent"}, "createdAt": "2026-02-01T10:00:13Z"},
			{"id": "s2", "type": "assessment", "status": "proposed", "data": {"key": "title", "score": 2, "rated": "f1dba3a9"},
			 "createdAt": "2026-02-01T10:00:14Z"},
			{"id": "k1", "type": "comment", "status": "proposed", "data": {"about": "title", "text": "hm"}, "createdAt": "2026-02-01T10:00:15Z"},
			{"id": "s3", "type": "assessment", "status": "proposed", "data": {"key": "item:R1", "score": 3, "rated": "x"},
			 "createdAt": "2026-02-01T10:00:16Z"},
			{"id": "t1", "type": "artifact", "target": "attestation:proc/gate/arch", "status": "proposed", "producedBy": "p",
			 "data": {"verdict": "met", "comment": "fine"}, "createdAt": "2026-02-01T10:00:17Z"},
			{"id": "m1", "type": "transition", "target": "transition.start", "status": "accepted",
			 "data": {"transition": "start", "from": "framing", "to": "analysis", "decision": "dp1"}, "createdAt": "2026-02-01T10:00:18Z"},
			{"id": "m2", "type": "transition", "target": "transition.next", "status": "accepted",
			 "data": {"transition": "next", "from": "analysis", "to": "design"}, "createdAt": "2026-02-01T10:00:19Z"},
			{"id": "m3", "type": "transition", "target": "transition.back", "status": "accepted",
			 "data": {"transition": "back", "from": "design", "to": "analysis"}, "createdAt": "2026-02-01T10:00:20Z"}
		]},
		"options": [{"id": "o1", "status": "open", "option": {"name": "plan A", "hypothesis": "h"}, "optionStatus": "evaluated",
			"evaluation": "good", "active": true}, {"id": "o2", "status": "discarded", "option": {"name": "plan B"}}],
		"activeOption": "o1",
		"decisionPoints": [{"id": "dp1", "question": "go?", "options": ["go", "no_go"], "criteria": ["cost"], "status": "decided",
			"questions": [{"id": "q1", "point": "dp1", "text": "why", "status": "answered", "answer": "because", "answeredBy": "u"},
				{"id": "q2", "point": "dp1", "text": "how", "status": "open"}],
			"ruling": {"outcome": "decided", "option": "go", "confidence": 0.75, "justification": "j", "by": "u", "human": true},
			"option": "go", "decidedBy": "u", "policy": {"threshold": 0.7, "maxRounds": 3, "rule": "consent"}, "humanOnly": true,
			"escalation": "board"},
			{"id": "dp2", "question": "which?", "status": "open"}],
		"at": "2026-03-01T00:00:00Z"}`,
		Policy: &criticality.Policy{Oracles: []string{"human"}, SignatoryRole: "sig", MaxDerogation: 48 * time.Hour},
	},
}

// typed writes a value of the activation with its CEL type: an int as {"#int": n}, so that the Rust activation is
// compared with the types the expressions see.
func typed(v any) any {
	switch x := Resolve(v).(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = typed(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = typed(e)
		}
		return out
	case int64:
		return map[string]any{"#int": x}
	case int:
		return map[string]any{"#int": x}
	}
	return Resolve(v)
}

func TestRustActivationCasesAreCurrent(t *testing.T) {
	type rustCase struct {
		Name       string         `json:"name"`
		Wire       any            `json:"wire"`
		Facets     map[string]any `json:"facets"`
		Activation any            `json:"activation"`
	}
	var cases []rustCase
	for _, b := range rustBoards {
		var r graphv1.GetBlackboardResponse
		if err := protojson.Unmarshal([]byte(b.Wire), &r); err != nil {
			t.Fatalf("%s: %v", b.Name, err)
		}
		// as lib/graphclient reads the blackboard, then the facets the graph adds
		var options []domain.Flow
		for _, f := range r.Options {
			options = append(options, pbconv.FlowFromPB(f))
		}
		var points []domain.DecisionPoint
		for _, d := range r.DecisionPoints {
			points = append(points, pbconv.DecisionPointFromPB(d))
		}
		bb := domain.Blackboard{Change: pbconv.ChangeFromPB(r.Change), Nodes: map[domain.NodeRef]domain.NodeView{}, Neighbors: map[domain.NodeRef]domain.Node{},
			Facets: map[string]any{domain.FacetOptions: options, domain.FacetActiveOption: r.ActiveOption, domain.FacetDecisionPoints: points},
			At:     pbconv.FromTime(r.At), Vars: b.Vars, Supertypes: b.Types}
		for _, v := range r.Nodes {
			nv := pbconv.ViewFromPB(v)
			bb.Nodes[nv.Ref()] = nv
		}
		for _, n := range r.Neighbors {
			nn := pbconv.NodeFromPB(n)
			bb.Neighbors[nn.Ref()] = nn
		}
		facets := map[string]any{"types": b.Types, "vars": b.Vars}
		if b.Deps != nil {
			bb.Facets[domain.FacetDependencies] = b.Deps
		}
		if b.Hist != nil {
			bb.Facets[domain.FacetHistory] = *b.Hist
		}
		if b.Policy != nil {
			bb.Facets[domain.FacetCriticalityPolicy] = *b.Policy
			facets["criticalityPolicy"] = b.Policy.Map()
		}
		act := Activation(bb)
		// the facets the Rust graph builds itself, given as the conditions see them
		facets["dependencies"], facets["history"] = Resolve(act["dependencies"]), Resolve(act["history"])
		var wire any
		raw, _ := protojson.Marshal(&r)
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatal(err)
		}
		cases = append(cases, rustCase{Name: b.Name, Wire: wire, Facets: typed(facets).(map[string]any), Activation: typed(act)})
	}
	raw, err := json.MarshalIndent(map[string]any{
		"comment": "Code generated from engine/condition (GOAP_UPDATE_RUST=1 go test ./engine/condition/ -run TestRustActivationCasesAreCurrent). DO NOT EDIT.",
		"cases":   cases,
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want := string(raw) + "\n"
	if os.Getenv("GOAP_UPDATE_RUST") != "" {
		if err := os.WriteFile(rustActivationCases, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(rustActivationCases)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatal(fmt.Sprintf("%s is not what Go computes: run GOAP_UPDATE_RUST=1 go test ./engine/condition/ -run TestRustActivationCasesAreCurrent", rustActivationCases))
	}
}
