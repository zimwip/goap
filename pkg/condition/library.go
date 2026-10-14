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
	// LibraryVerification: the verification of the effects of the action runs (ADR 0075).
	LibraryVerification = "verification"
	// LibraryDerogations: the derogations of the change, waivers with a rule, a signatory and an expiry (ADR 0075 §2).
	// A library of its own, not part of `risks`: a methodology that signs none imports none (ADR 0064).
	LibraryDerogations = "derogations"
	// LibraryDependencies: the changes the change depends on (ADR 0108). A step waiting for them (`dependencies_met`)
	// waits for those changes to merge, which no action of the change produces.
	LibraryDependencies = "dependencies"
	// LibraryAssessments: the ratings of what a person wrote on the change, the description first (ADR 0110).
	LibraryAssessments = "assessments"
)

// AcceptableScore is the score (0 to 5) from which a rated description is accepted (`description_accepted`).
const AcceptableScore = 3

// DerogationDebtLimit is the number of open derogations from which the debt is too high (`derogation_debt`): the
// parameter of the conditions of the `derogations` library, like HighRisk for the risks. A gate guard or a decision
// point reads it.
const DerogationDebtLimit = 3

// HighRisk is the score (probability × impact, 1–25) from which a live risk needs a mitigation action: the parameter
// of the conditions of the `risks` library.
const HighRisk = 9

// LibraryNames lists the built-in libraries, in a stable order.
var LibraryNames = []string{LibraryDecisions, LibraryRisks, LibraryVerification, LibraryDerogations, LibraryDependencies, LibraryAssessments}

var unmitigated = fmt.Sprintf(`risks.exists(r, r.live && r.score >= %d && !actions.exists(a, a.for == r.key && a.status != "cancelled"))`, HighRisk)

var libraries = map[string][]Definition{
	LibraryDecisions: {
		{Name: "open_questions", Expr: `questions.exists(q, q.status == "open")`, Description: "a question of the change is still open"},
		{Name: "no_open_questions", Expr: `!questions.exists(q, q.status == "open")`, Description: "every question of the change is answered"},
		// a decision point waits for a ruling it can take now: open, or escalated to a person
		{Name: "decision_ready", Expr: `decisionPoints.exists(d, d.status == "open" || d.status == "escalated")`, Description: "a decision point can be ruled now (open, or escalated to a person)"},
		{Name: "decision_pending", Expr: `decisionPoints.exists(d, d.status != "decided")`, Description: "a decision point is not decided yet"},
		{Name: "no_decision_pending", Expr: `!decisionPoints.exists(d, d.status != "decided")`, Description: "every decision point of the change is decided"},
		{Name: "ratification_pending", Expr: `decisionPoints.exists(d, d.status == "ratifying")`, Description: "a decision waits for its ratification"},
		{Name: "decision_escalated", Expr: `decisionPoints.exists(d, d.escalation != "")`, Description: "a decision point was escalated"},
		{Name: "options_open", Expr: `options.exists(o, o.status == "exploring" || o.status == "evaluated")`, Description: "an option is still being explored or evaluated"},
		// every open option is evaluated (and there is one)
		{Name: "options_evaluated", Expr: `options.exists(o, o.status == "evaluated") && !options.exists(o, o.status == "exploring")`, Description: "every open option is evaluated"},
		{Name: "option_selected", Expr: `options.exists(o, o.status == "selected")`, Description: "an option has been selected"},
	},
	LibraryRisks: {
		// a live risk is open or being mitigated; a high one (score >= HighRisk) needs an action
		{Name: "open_risks", Expr: `risks.exists(r, r.live)`, Description: "a risk of the change is live (open or being mitigated)"},
		{Name: "unmitigated_risks", Expr: unmitigated, Description: "a high risk has no mitigation action"},
		{Name: "risks_under_control", Expr: "!" + unmitigated, Description: "every high risk has a mitigation action"},
		{Name: "open_actions", Expr: `actions.exists(a, a.status == "open")`, Description: "an action of the register is still open"},
		{Name: "no_open_actions", Expr: `!actions.exists(a, a.status == "open")`, Description: "every action of the register is done or cancelled"},
	},
	LibraryVerification: {
		// an effect produced or verified, with no verdict yet; rejected effects are not open: they await a new run
		{Name: "unverified_effects", Expr: `verifications.exists(v, v.open)`, Description: "an effect produced by a run waits for its verification"},
		{Name: "all_verified", Expr: `!verifications.exists(v, v.open)`, Description: "every effect produced is verified"},
		{Name: "reserves_open", Expr: `verifications.exists(v, v.state == "accepted_with_reserve")`, Description: "an effect was accepted with a reserve that is still open"},
	},
	LibraryDependencies: {
		// satisfied: the work of the dependency has reached the branch the change merges into
		{Name: "dependencies_met", Expr: `!dependencies.exists(d, !d.satisfied)`, Description: "the changes this change depends on have merged"},
		{Name: "dependencies_pending", Expr: `dependencies.exists(d, !d.satisfied)`, Description: "a change this change depends on has not merged yet"},
	},
	LibraryAssessments: {
		// current: the rating is about the description as it reads now; answered: no comment came after it
		{Name: "description_rated", Expr: `assessments.exists(a, a.key == "description" && a.current)`, Description: "the description, as it reads now, has been rated"},
		{Name: "description_reviewed", Expr: `assessments.exists(a, a.key == "description" && a.current && a.answered)`, Description: "the description is rated and the last remark on it is answered"},
		{Name: "description_accepted", Expr: fmt.Sprintf(`assessments.exists(a, a.key == "description" && a.current && a.score >= %d)`, AcceptableScore), Description: "the description, as it reads now, is rated acceptable (score 3 or more)"},
	},
	LibraryDerogations: {
		// a change cannot land on a derogation that ran out; the debt is the count of the open ones
		{Name: "no_expired_derogation", Expr: `!derogations.exists(d, d.expired)`, Description: "no derogation of the change has run out"},
		{Name: "derogation_debt", Expr: fmt.Sprintf(`derogations.filter(d, d.open).size() >= %d`, DerogationDebtLimit), Description: "too many derogations are open"},
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
