// Package builtins names the builtin actions of the platform (action kind `builtin`, ADR 0062): the names a
// methodology may use, known to the compiler and the registry without an engine, and implemented by the engine
// (generic ones) or by the packages that register their own (self-improvement). A leaf package: no project import.
package builtins

import "slices"

// The builtins of the platform.
const (
	GraphPropagate      = "graph.propagate"      // follow links backwards from the impacted nodes
	GraphApply          = "graph.apply"          // apply the change
	DecisionInvestigate = "decision.investigate" // open options on a decision point
	// DerogationExpire closes the derogations that ran out and sends what they covered back to review (ADR 0075 §2).
	DerogationExpire = "derogation.expire"
	// GateOpen opens the decision of a gate of a process, GatePass takes its transition once it said go (ADR 0132).
	GateOpen = "gate.open"
	GatePass = "gate.pass"
	// Builtins of the self-observation methodology (ADR 0011).
	ObserveAnalyze   = "observe.analyze"
	ObservePropose   = "observe.propose"
	MethodologyDraft = "methodology.draft"
)

// All lists the known builtin names.
func All() []string {
	return []string{GraphPropagate, GraphApply, DecisionInvestigate, DerogationExpire, GateOpen, GatePass, ObserveAnalyze, ObservePropose, MethodologyDraft}
}

// Known is the static set of the builtin names; it implements methodology.BuiltinSet.
type Known struct{}

// HasBuiltin reports whether name is a builtin of the platform.
func (Known) HasBuiltin(name string) bool { return slices.Contains(All(), name) }
