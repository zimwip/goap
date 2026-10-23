package engine

import (
	"strings"
	"testing"

	"github.com/zimwip/goap/engine/methodology"
)

func mkViolation(where, msg string) Violation {
	return Violation{Rule: "R", Severity: methodology.SeverityVeto, Where: where, Msg: msg}
}

// A command's result is read as SARIF, else as file:line: message lines, else its failure is one violation (ADR 0162).
func TestShellViolations(t *testing.T) {
	sarif := `{"runs":[{"results":[{"ruleId":"errorlint","message":{"text":"comparing with ==\nwill fail"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"pay/ledger.go"},"region":{"startLine":88}}}]}]}]}`
	vs := shellViolations(map[string]any{"stdout": sarif, "exitCode": float64(1)}, mkViolation)
	if len(vs) != 1 || vs[0].Where != "pay/ledger.go:88" || vs[0].Msg != "errorlint: comparing with == will fail" {
		t.Fatalf("sarif: %+v", vs)
	}
	vs = shellViolations(map[string]any{"stdout": "building\npay/a.go:3:5: unused variable x\nok\n", "stderr": "pay/b.go:9: shadowed err", "exitCode": float64(1)}, mkViolation)
	if len(vs) != 2 || vs[0].Where != "pay/a.go:3" || vs[0].Msg != "unused variable x" || vs[1].Where != "pay/b.go:9" {
		t.Fatalf("lines: %+v", vs)
	}
	vs = shellViolations(map[string]any{"stdout": "", "stderr": "boom\nno go files", "exitCode": float64(2)}, mkViolation)
	if len(vs) != 1 || !strings.Contains(vs[0].Msg, "exited with 2: no go files") {
		t.Fatalf("a failure with no line: %+v", vs)
	}
	if vs = shellViolations(map[string]any{"stdout": "all good", "exitCode": float64(0)}, mkViolation); len(vs) != 0 {
		t.Fatalf("a success with no line is no violation: %+v", vs)
	}
	if vs = shellViolations(map[string]any{"stdout": `{"runs":[{"results":[]}]}`, "exitCode": float64(0)}, mkViolation); vs == nil || len(vs) != 0 {
		t.Fatalf("an empty SARIF log: %+v", vs)
	}
}

// The brief gives one line per rule, the vetos first, each rule once; a pending control says its rules are unknown.
func TestControlSection(t *testing.T) {
	set := &ControlSet{Controls: []ControlState{
		{Condition: "coding", State: ControlEstablished, Rules: []GoverningRule{
			{ControlRule: methodology.ControlRule{ID: "NAMES", Statement: "Names say\n the business", Severity: methodology.SeverityAdvice}, Condition: "coding"},
			{ControlRule: methodology.ControlRule{ID: "ADR-1", Statement: "No dependency", Check: &methodology.ControlCheck{Language: "cypher", Code: "MATCH"}}, Version: 2, Condition: "coding"},
		}},
		{Condition: "security", State: ControlPending},
		{Condition: "again", State: ControlEstablished, Rules: []GoverningRule{{ControlRule: methodology.ControlRule{ID: "ADR-1"}, Version: 2, Condition: "again"}}},
	}}
	s := set.section()
	want := "- ADR-1 [veto] No dependency\n- NAMES [advice] Names say the business (not measured)\n- security: not collected yet"
	if !strings.Contains(s, want) {
		t.Fatalf("section:\n%s", s)
	}
	if got := strings.Join(set.Refs(), ","); got != "security?,ADR-1@2,coding/NAMES" {
		t.Fatalf("refs: %s", got)
	}
	if set.Established() {
		t.Fatal("a pending control is not established")
	}
	empty := &ControlSet{Controls: []ControlState{{Condition: "coding", State: ControlEstablished}}}
	if !strings.Contains(empty.section(), "none: the rules were collected and none governs this work") {
		t.Fatalf("collected and empty: %s", empty.section())
	}
	if (*ControlSet)(nil).section() != "" {
		t.Fatal("no control, no section")
	}
}
