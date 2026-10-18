package graphapi

// OpenDecisionRequest opens a decision point.
type OpenDecisionRequest struct {
	Question string
	// Options are the options it chooses among; nil: the open options of the change, [] none (a free question).
	Options []string
	// Choices are what the point chooses among beyond the options of the change: plain labels (go, no go, back of a
	// gate, ADR 0132), ruled as they are named and selecting no flow.
	Choices  []string
	Criteria []string
	// Policy is the policy values the opener gives (who decides, a confidence threshold, a number of rounds, a
	// duration: the keys of pkg/decision). The graph hands them to Graph.DecisionPolicy, which resolves them, and
	// stores the result on the point.
	Policy map[string]any
	By     string
}

// RuleRequest is a ruling of a decision point.
type RuleRequest struct {
	Point string
	// Outcome is decided (Option, Confidence) or undecidable (Questions: what blocks it).
	Outcome       string
	Option        string
	Confidence    float64
	Justification string
	Questions     []string
	// Human tells the ruling comes from a person: the only one a point reserved to a person accepts.
	Human bool
	By    string
	// Role is the seat of the ruling on a point of a board (ADR 0132 §3): a role of the board the caller holds.
	Role string
}
