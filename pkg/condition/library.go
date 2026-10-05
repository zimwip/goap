package condition

import (
	"fmt"
	"slices"
)

// Names of the built-in condition libraries (ADR 0064). A methodology gets the conditions of a library only by
// importing it (`imports:`); the engine adds nothing the methodology did not ask for.
const (
	// LibraryDecisions: questions, decision points and options (ADR 0009 §4).
	LibraryDecisions = "decisions"
	// LibraryRisks: the risk register and the actions of the change (ADR 0036 §1).
	LibraryRisks = "risks"
)

// HighRisk is the score (probability × impact, 1–25) from which a live risk needs a mitigation action: the parameter
// of the conditions of the `risks` library.
const HighRisk = 9

// LibraryNames lists the built-in libraries, in a stable order.
var LibraryNames = []string{LibraryDecisions, LibraryRisks}

var unmitigated = fmt.Sprintf(`risks.exists(r, r.live && r.score >= %d && !actions.exists(a, a.for == r.key && a.status != "cancelled"))`, HighRisk)

var libraries = map[string][]Definition{
	LibraryDecisions: {
		{Name: "open_questions", Expr: `questions.exists(q, q.status == "open")`},
		{Name: "no_open_questions", Expr: `!questions.exists(q, q.status == "open")`},
		// a decision point waits for a ruling it can take now: open, or escalated to a person
		{Name: "decision_ready", Expr: `decisionPoints.exists(d, d.status == "open" || d.status == "escalated")`},
		{Name: "decision_pending", Expr: `decisionPoints.exists(d, d.status != "decided")`},
		{Name: "no_decision_pending", Expr: `!decisionPoints.exists(d, d.status != "decided")`},
		{Name: "ratification_pending", Expr: `decisionPoints.exists(d, d.status == "ratifying")`},
		{Name: "decision_escalated", Expr: `decisionPoints.exists(d, d.escalation != "")`},
		{Name: "options_open", Expr: `options.exists(o, o.status == "exploring" || o.status == "evaluated")`},
		// every open option is evaluated (and there is one)
		{Name: "options_evaluated", Expr: `options.exists(o, o.status == "evaluated") && !options.exists(o, o.status == "exploring")`},
		{Name: "option_selected", Expr: `options.exists(o, o.status == "selected")`},
	},
	LibraryRisks: {
		// a live risk is open or being mitigated; a high one (score >= HighRisk) needs an action
		{Name: "open_risks", Expr: `risks.exists(r, r.live)`},
		{Name: "unmitigated_risks", Expr: unmitigated},
		{Name: "risks_under_control", Expr: "!" + unmitigated},
		{Name: "open_actions", Expr: `actions.exists(a, a.status == "open")`},
		{Name: "no_open_actions", Expr: `!actions.exists(a, a.status == "open")`},
	},
}

// Library returns a copy of the conditions of a built-in library.
func Library(name string) ([]Definition, bool) {
	defs, ok := libraries[name]
	return slices.Clone(defs), ok
}

// MustLibrary is Library for a name known to exist (tests, shipped code).
func MustLibrary(names ...string) []Definition {
	var out []Definition
	for _, n := range names {
		defs, ok := Library(n)
		if !ok {
			panic("condition: unknown library " + n)
		}
		out = append(out, defs...)
	}
	return out
}
