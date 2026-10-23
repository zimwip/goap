package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/graphapi"
	"github.com/zimwip/goap/engine/methodology"
)

// The measure of a control (ADR 0162): the rules governing a run are checked on what the run produced, and the answer
// is the rules broken, in as few words as can still be acted upon (the rule, where, what, the fix), never the text of
// the rules (the agent was given them in its brief). Every check costs no model call: a query over the view of the
// change, or a command in its workspace.

// Violation is a rule broken.
type Violation struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	// Where it is broken: a file and line, the nodes of a row, the subject.
	Where string `json:"where,omitempty"`
	Msg   string `json:"msg,omitempty"`
	Fix   string `json:"fix,omitempty"`
}

// ControlReport is what a check of the controls of a run found.
type ControlReport struct {
	Subject string `json:"subject,omitempty"`
	// Revision is the number of artifacts of the target the check was asked to count for the subject (its code changes):
	// a condition tells a report current by it, as the build report of a component.
	Revision *int `json:"revision,omitempty"`
	// Controls are the controls checked, with their state and their number of rules.
	Controls []map[string]any `json:"controls"`
	// Checked is the number of rules measured, Unmeasured of those with no check.
	Checked    int         `json:"checked"`
	Unmeasured int         `json:"unmeasured,omitempty"`
	Violations []Violation `json:"violations,omitempty"`
	// More counts the violations left out past the cap of a rule.
	More int `json:"more,omitempty"`
	// Failed are the checks that could not run (the rule is not known to hold).
	Failed []map[string]string `json:"failed,omitempty"`
	// Open is the number of vetos broken; Objectives of objectives broken.
	Open       int `json:"open"`
	Objectives int `json:"objectives,omitempty"`
	// OK: every control established, no veto broken, every check ran.
	OK bool `json:"ok"`
}

// maxViolationsPerRule caps what a rule reports: past it, the rule is broken many times and the first ones say how.
const maxViolationsPerRule = 10

// ControlCheck is the builtin control.check: it measures the rules of the controls governing the run (its own and its
// step's, for its subject) and records the report as an artifact (params.artifact, default control_report). A rule
// checked in shell runs in the workspace of the change (the action declares the MCP workspace), in the directory of the
// subject when it has one. params.revisionOf names the target of the artifacts counted for the subject (data.component
// or data.subject), recorded as the revision of the report.
func ControlCheck(ctx context.Context, ac ActionContext) (ActionResult, error) {
	set := ac.Controls
	if set == nil {
		return ActionResult{}, fmt.Errorf("control.check: no control governs the action %s (declare controls on it or its step)", ac.Action.Name)
	}
	rep := ControlReport{Subject: set.Subject, Controls: []map[string]any{}}
	for _, c := range set.Controls {
		rep.Controls = append(rep.Controls, map[string]any{"condition": c.Condition, "state": c.State, "rules": len(c.Rules)})
	}
	if of, _ := ac.Action.Params["revisionOf"].(string); of != "" {
		n := 0
		for _, it := range ac.Blackboard.Change.Items {
			if it.Type == domain.TypeArtifact && it.Target == of && (it.Data["component"] == set.Subject || it.Data["subject"] == set.Subject) {
				n++
			}
		}
		rep.Revision = &n
	}
	for _, r := range set.Rules() {
		if r.Check == nil {
			rep.Unmeasured++
			continue
		}
		rep.Checked++
		found, err := measure(ctx, ac, set.Subject, r)
		if err != nil {
			rep.Failed = append(rep.Failed, map[string]string{"rule": r.ID, "error": oneLine(err.Error())})
			continue
		}
		if len(found) > maxViolationsPerRule {
			rep.More += len(found) - maxViolationsPerRule
			found = found[:maxViolationsPerRule]
		}
		rep.Violations = append(rep.Violations, found...)
	}
	for _, v := range rep.Violations {
		switch v.Severity {
		case methodology.SeverityVeto:
			rep.Open++
		case methodology.SeverityObjective:
			rep.Objectives++
		}
	}
	rep.OK = set.Established() && rep.Open == 0 && len(rep.Failed) == 0
	data := map[string]any{}
	raw, _ := json.Marshal(rep)
	_ = json.Unmarshal(raw, &data)
	target, _ := ac.Action.Params["artifact"].(string)
	if target == "" {
		target = "control_report"
	}
	return ActionResult{Items: []ItemInput{{Type: string(domain.TypeArtifact), Target: target, Data: data}}, Output: rep.summary()}, nil
}

// summary is the report in a line, and the violations one per line: what an agent reads to fix its work.
func (r ControlReport) summary() string {
	subject := r.Subject
	if subject == "" {
		subject = "the change"
	}
	var b strings.Builder
	switch {
	case len(r.Violations) == 0 && len(r.Failed) == 0:
		fmt.Fprintf(&b, "%s: the rules hold (%d checked)", subject, r.Checked)
	default:
		fmt.Fprintf(&b, "%s: %d veto(s) broken, %d objective(s) broken, %d check(s) failed", subject, r.Open, r.Objectives, len(r.Failed))
	}
	for _, v := range r.Violations {
		fmt.Fprintf(&b, "\n%s [%s] %s", v.Rule, v.Severity, v.Where)
		if v.Msg != "" {
			fmt.Fprintf(&b, ": %s", v.Msg)
		}
		if v.Fix != "" {
			fmt.Fprintf(&b, " → %s", v.Fix)
		}
	}
	if r.More > 0 {
		fmt.Fprintf(&b, "\n… %d more", r.More)
	}
	for _, f := range r.Failed {
		fmt.Fprintf(&b, "\n%s: the check failed: %s", f["rule"], f["error"])
	}
	return b.String()
}

// measure runs the check of a rule and returns its violations.
func measure(ctx context.Context, ac ActionContext, subject string, r GoverningRule) ([]Violation, error) {
	mk := func(where, msg string) Violation {
		return Violation{Rule: r.ID, Severity: r.SeverityOf(), Where: where, Msg: msg, Fix: r.Fix}
	}
	switch r.Check.Language {
	case methodology.CheckCypher:
		return measureQuery(ctx, ac, subject, r.Check.Code, mk)
	case methodology.CheckShell:
		if ac.Host == nil {
			return nil, fmt.Errorf("no host to run the command")
		}
		args := map[string]any{"command": r.Check.Code, "timeout": 600}
		if subject != "" {
			args["cwd"] = subject
		}
		out, err := ac.Host.CallTool(ctx, "workspace/bash", args)
		if err != nil {
			return nil, err
		}
		return shellViolations(out, mk), nil
	}
	return nil, fmt.Errorf("unknown language %q", r.Check.Language)
}

// measureQuery runs a query in the view of the run: each row is a violation, said by its columns (a column `at` says
// where, `msg` what; the others are said as name=value); a query naming `subject` has it bound to the subject.
func measureQuery(ctx context.Context, ac ActionContext, subject, query string, mk func(where, msg string) Violation) ([]Violation, error) {
	v := viewOf(ac.Process)
	opts := graphapi.QueryOptions{MaxRows: maxViolationsPerRule * 10}
	if subject != "" && strings.Contains(query, "subject") {
		n, err := ac.Graph.ViewNode(ctx, v, "", subject)
		if err != nil {
			return nil, fmt.Errorf("subject %s: %w", subject, err)
		}
		opts.Bind = map[string]domain.NodeID{"subject": n.ID}
	}
	res, err := ac.Graph.QueryView(ctx, v, query, opts)
	if err != nil {
		return nil, err
	}
	keys := map[string]string{}
	for _, n := range res.Nodes {
		keys[string(n.ID)] = n.Key
	}
	var out []Violation
	for _, row := range res.Rows {
		var where, msg string
		var parts []string
		for i, col := range res.Columns {
			if i >= len(row) {
				break
			}
			val := cellText(row[i], keys)
			switch col {
			case "at":
				where = val
			case "msg", "message":
				msg = val
			default:
				parts = append(parts, col+"="+val)
			}
		}
		if where == "" {
			where = strings.Join(parts, " ")
		} else if msg == "" && len(parts) > 0 {
			msg = strings.Join(parts, " ")
		}
		out = append(out, mk(where, msg))
	}
	return out, nil
}

// cellText is a value of a row as a violation says it: a node by its key.
func cellText(v any, keys map[string]string) string {
	switch x := v.(type) {
	case map[string]any:
		if id, ok := x["$node"].(string); ok {
			if k := keys[id]; k != "" {
				return k
			}
			return id
		}
		raw, _ := json.Marshal(x)
		return string(raw)
	case string:
		return x
	case nil:
		return "null"
	}
	raw, _ := json.Marshal(v)
	return string(raw)
}

// lineRE is a violation as linters print them: file:line[:col]: message.
var lineRE = regexp.MustCompile(`^([^\s:][^:]*):(\d+)(?::\d+)?:\s*(.+)$`)

// shellViolations reads the result of a command (the workspace's bash: exitCode, stdout, stderr): SARIF on its standard
// output, else its `file:line: message` lines; a command that failed with neither is one violation, its last line.
func shellViolations(res any, mk func(where, msg string) Violation) []Violation {
	m, _ := res.(map[string]any)
	stdout, _ := m["stdout"].(string)
	stderr, _ := m["stderr"].(string)
	if vs, ok := sarifViolations(stdout, mk); ok {
		return vs
	}
	var out []Violation
	for _, line := range strings.Split(stdout+"\n"+stderr, "\n") {
		if g := lineRE.FindStringSubmatch(strings.TrimSpace(line)); g != nil {
			out = append(out, mk(g[1]+":"+g[2], g[3]))
		}
	}
	if len(out) > 0 {
		return out
	}
	code := exitCodeOf(m["exitCode"])
	timedOut, _ := m["timedOut"].(bool)
	if code != 0 || timedOut {
		last := lastLine(stderr)
		if last == "" {
			last = lastLine(stdout)
		}
		return []Violation{mk("", fmt.Sprintf("the check exited with %d: %s", code, last))}
	}
	return nil
}

func exitCodeOf(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case int64:
		return int(x)
	case string:
		n, _ := strconv.Atoi(x)
		return n
	}
	return 0
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return oneLine(lines[len(lines)-1])
}

// sarifViolations reads a SARIF log (ok false when the output is none): one violation per result.
func sarifViolations(s string, mk func(where, msg string) Violation) ([]Violation, bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{") {
		return nil, false
	}
	var log struct {
		Runs []struct {
			Results []struct {
				RuleID  string `json:"ruleId"`
				Message struct {
					Text string `json:"text"`
				} `json:"message"`
				Locations []struct {
					PhysicalLocation struct {
						ArtifactLocation struct {
							URI string `json:"uri"`
						} `json:"artifactLocation"`
						Region struct {
							StartLine int `json:"startLine"`
						} `json:"region"`
					} `json:"physicalLocation"`
				} `json:"locations"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal([]byte(s), &log); err != nil || log.Runs == nil {
		return nil, false
	}
	out := []Violation{}
	for _, run := range log.Runs {
		for _, r := range run.Results {
			where := ""
			if len(r.Locations) > 0 {
				pl := r.Locations[0].PhysicalLocation
				where = pl.ArtifactLocation.URI
				if pl.Region.StartLine > 0 {
					where += ":" + strconv.Itoa(pl.Region.StartLine)
				}
			}
			msg := oneLine(r.Message.Text)
			if r.RuleID != "" {
				msg = r.RuleID + ": " + msg
			}
			out = append(out, mk(where, msg))
		}
	}
	return out, true
}
