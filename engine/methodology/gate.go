package methodology

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/zimwip/goap/contracts/builtins"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/form"
	"github.com/zimwip/goap/engine/condition"
	"github.com/zimwip/goap/lib/def"
)

// Gate declares a step a gate of its process (ADR 0132): a milestone where the maturity of the change is checked
// against a checklist split among the roles that answer for its items, a decision is taken (by the accountable role
// of the step, or a board), and a go takes a transition of the lifecycle of the change, freezing what it declares.
// The gate is organisation: it carries no action of its own; the engine opens its decision, people take it, the engine
// carries out the move. How the decision is reached may be a method (the step's method: a capability).
type Gate struct {
	// Transition is the transition of the lifecycle of the change a go takes. With a maturity (ADR 0179) it only names
	// the transition the gate derives (default: the name of the step).
	Transition string `yaml:"transition,omitempty" json:"transition,omitempty"`
	// Enters is the phase of the maturity of the change the go enters (ADR 0179: the methodology states a maturity).
	Enters string `yaml:"enters,omitempty" json:"enters,omitempty"`
	// From is the phase the gate is passed from (default: the phase before the one it enters).
	From string `yaml:"from,omitempty" json:"from,omitempty"`
	// Checklist are the items checked on the change before the decision.
	Checklist []GateItem `yaml:"checklist,omitempty" json:"checklist,omitempty"`
	// Decision says how the go is decided (nil: the accountable role of the step rules it alone).
	Decision *GateDecision `yaml:"decision,omitempty" json:"decision,omitempty"`
	// Freezes is what the go freezes beyond the impacts of the state it leaves.
	Freezes *domain.Freezes `yaml:"freezes,omitempty" json:"freezes,omitempty"`
}

// GateItem is an item of the checklist of a gate: a condition of the methodology, or an attestation of a person.
type GateItem struct {
	// Name is a condition of the methodology (declared, of an imported library, or generated); for an attested item,
	// the name of the item (its condition is generated: attested:<step path>/<name>).
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// Role answers for the item (a role of the methodology; default the accountable role of the step).
	Role string `yaml:"role,omitempty" json:"role,omitempty"`
	// Veto: an unmet item blocks the gate, never compensated; otherwise it is an objective, covered by a derogation in
	// force whose rule is its name (the gate goes with reserve, ADR 0075 §3).
	Veto bool `yaml:"veto,omitempty" json:"veto,omitempty"`
	// When (CEL over the change) restricts the item to the changes it holds for (by criticality, say).
	When string `yaml:"when,omitempty" json:"when,omitempty"`
	// Attest: the item is attested by a person of its role, not derived from the work.
	Attest bool `yaml:"attest,omitempty" json:"attest,omitempty"`
}

// GateDecision is how the go of a gate is decided.
type GateDecision struct {
	// Question is asked by the decision point (default: "Go for <step>?").
	Question string `yaml:"question,omitempty" json:"question,omitempty"`
	// Back names a transition the decision may take instead (back to an earlier state): an option back. With a
	// maturity it only names the transition back (default: back_<step>).
	Back string `yaml:"back,omitempty" json:"back,omitempty"`
	// BackTo is the phase of the maturity the decision may send the change back to (ADR 0179): an option back.
	BackTo string `yaml:"backTo,omitempty" json:"backTo,omitempty"`
	// By is who takes the decision (ADR 0179): human (default: the board, people in their seats) or script (the code
	// of the gate rules it, an agent's ruling, which the policy of the point may leave to a ratification).
	By string `yaml:"by,omitempty" json:"by,omitempty"`
	// Code is the JavaScript ruling the decision when By is script: it reads the change and calls
	// ctx.decide(point, "go" | "no_go" | "back", confidence, justification) on the open point of the gate
	// (ctx.param("gate") names the gate).
	Code string `yaml:"code,omitempty" json:"code,omitempty"`
	// Board are the roles that sit on the decision (default: the accountable role of the step alone).
	Board []string `yaml:"board,omitempty" json:"board,omitempty"`
	// Rule settles the rulings of a board: unanimity (default), majority or consent.
	Rule string `yaml:"rule,omitempty" json:"rule,omitempty"`
	// MaxDuration is how long the decision may take before it is escalated to the accountable role ("72h").
	MaxDuration string `yaml:"maxDuration,omitempty" json:"maxDuration,omitempty"`
}

// Who takes the decision of a gate (GateDecision.By).
const (
	DecidedByHuman  = "human"
	DecidedByScript = "script"
)

// MethodGate is how a gate step is done: its decision (ADR 0132).
const MethodGate = "gate"

// The rules of a board (those of contracts/decision, which the methodology does not import).
var gateRules = []string{"unanimity", "majority", "consent"}

// The options of the decision of a gate.
const (
	OptionGo   = "go"
	OptionNoGo = "no_go"
	OptionBack = "back"
)

// The generated conditions of a gate.
const (
	gatePassedPrefix   = "gate:"
	gateOpenPrefix     = "gate_open:"
	gateGoPrefix       = "gate_go:"
	gateAttestedPrefix = "attested:"
)

// GatePassed is the condition a gate step makes true: its transition stands (taken, not gone back from since).
func GatePassed(path string) string { return gatePassedPrefix + path }

// GateOpen is the condition of a decision of the gate waiting for its rulings.
func GateOpen(path string) string { return gateOpenPrefix + path }

// GateGo is the condition of a decision of the gate that said go and gated no move yet.
func GateGo(path string) string { return gateGoPrefix + path }

// AttestedCondition is the condition of an attested item of the checklist of a gate.
func AttestedCondition(path, item string) string { return gateAttestedPrefix + path + "/" + item }

// AttestAction names the human action attesting an item of the checklist of a gate.
func AttestAction(path, item string) string { return "attest:" + path + "/" + item }

// GateInfo is a gate as the compiled methodology holds it: what the registry adds to the transition of the lifecycle
// of the change (its criteria and freezes), what the engine opens (its decision) and what the views show.
type GateInfo struct {
	// Path is the step of the gate.
	Path       string
	Transition string
	// Accountable is the accountable role of the step: it chairs the decision.
	Accountable string
	Items       []GateItemInfo
	Board       []string
	Rule        string
	Question    string
	Back        string
	MaxDuration string
	Freezes     *domain.Freezes
	// From, Enters and BackTo are the phases of the maturity the gate moves between (ADR 0179); empty: the
	// methodology names the lifecycle of a domain.
	From, Enters, BackTo string
	// Entry is the entry of the gate step, part of the guard of its transition when the lifecycle is derived from the
	// maturity (ADR 0179: the guard of a transition is the entry of its gate).
	Entry map[string]bool
	// By is who decides: human or script.
	By string
	// Method names the capability of the decision workflow (empty: the default decision).
	Method string
}

// GateItemInfo is an item of the checklist of a gate as compiled: the condition it checks and its role.
type GateItemInfo struct {
	GateItem
	// Condition is the condition the item checks (the name, or the generated attested:<path>/<name>).
	Condition string
}

// Options are the options of the decision of the gate.
func (g GateInfo) Options() []string {
	out := []string{OptionGo, OptionNoGo}
	if g.Back != "" {
		out = append(out, OptionBack)
	}
	return out
}

// Policy are the values of the decision policy the decision point of the gate opens with (contracts/decision).
func (g GateInfo) Policy() map[string]any {
	if g.By == DecidedByScript {
		// ruled by its code: an agent rules alone, no board
		return map[string]any{"gate": g.Path, "transition": g.Transition, "decider": "agent"}
	}
	p := map[string]any{"gate": g.Path, "transition": g.Transition, "board": slices.Clone(g.Board), "chair": g.Accountable}
	if g.Rule != "" {
		p["rule"] = g.Rule
	}
	if g.MaxDuration != "" {
		p["maxDuration"] = g.MaxDuration
	}
	return p
}

// Criteria are the criteria the gate adds to its transition (ADR 0132 §2): its vetos and objectives, in the
// environment of a guard (the world state, the change), and the decision, a veto: a decided go of its point.
func (g GateInfo) Criteria() (vetos, objectives []domain.Criterion) {
	for _, cond := range slices.Sorted(maps.Keys(g.Entry)) {
		vetos = append(vetos, domain.Criterion{Name: "entry:" + cond, Description: "the entry of the gate: " + cond,
			Expr: fmt.Sprintf("world[%q] == %t", cond, g.Entry[cond])})
	}
	for _, it := range g.Items {
		expr := fmt.Sprintf("world[%q]", it.Condition)
		if it.When != "" {
			expr = fmt.Sprintf("!(%s) || %s", it.When, expr)
		}
		c := domain.Criterion{Name: it.Name, Expr: expr, Description: it.Description}
		if it.Veto {
			vetos = append(vetos, c)
		} else {
			objectives = append(objectives, c)
		}
	}
	vetos = append(vetos, domain.Criterion{Name: "decision", Description: "the decision of the gate said go",
		Expr: fmt.Sprintf(`decisionPoints.exists(d, d.id == change.decision && d.status == "decided" && d.option == %q && "gate" in d.policy && d.policy.gate == %q)`, OptionGo, g.Path)})
	return vetos, objectives
}

// Gates are the gates of the methodology, by step path.
func (c *Compiled) Gates() []GateInfo { return c.gates }

// GateOf is the gate a transition of the lifecycle of the change is the transition of.
func (c *Compiled) GateOf(transition string) (GateInfo, bool) {
	for _, g := range c.gates {
		if g.Transition == transition {
			return g, true
		}
	}
	return GateInfo{}, false
}

// GateAt is the gate of a step.
func (c *Compiled) GateAt(path string) (GateInfo, bool) {
	for _, g := range c.gates {
		if g.Path == path {
			return g, true
		}
	}
	return GateInfo{}, false
}

// gate generates a gate step (ADR 0132): the conditions of its passage, of its decision and of its attested items, the
// human actions attesting them, the opening of its decision (builtin gate.open, or the decision method), and the move
// once its decision says go (builtin gate.pass). It returns the exit of the step: the gate passed.
func (w *stepWalker) gate(s Step, sp, path string, need map[string]bool) map[string]bool {
	g := s.Gate
	switch {
	case len(s.Steps) > 0 || s.Action != "" || len(s.Actions) > 0 || s.Process != "":
		w.add(sp+".gate", "a gate decides, it does no work: no sub-steps, action or process (a method may say how it decides)")
		return nil
	case s.Foreach != "" || s.Where != "" || s.Review != nil:
		w.add(sp+".gate", "a gate is decided once: no foreach, where or review")
		return nil
	case !w.m.HasLifecycle():
		w.add(sp+".gate", "a gate takes a transition of the lifecycle of the change: the methodology names none (lifecycle) and states no maturity (maturity)")
		return nil
	case w.m.Maturity == nil && (g.Enters != "" || g.From != "" || (g.Decision != nil && g.Decision.BackTo != "")):
		w.add(sp+".gate.enters", "enters, from and backTo name phases of a maturity: the methodology names the lifecycle of a domain, name its transitions")
		return nil
	case w.m.Maturity == nil && g.Transition == "":
		w.add(sp+".gate.transition", "name the transition of the lifecycle of the change the go takes")
		return nil
	case len(s.Done) > 0:
		w.add(sp+".done", "a gate is done once its transition is taken: no done of its own")
		return nil
	}
	var from, enters, backTo string
	transition, back := g.Transition, ""
	if g.Decision != nil {
		back = g.Decision.Back
	}
	if w.m.Maturity != nil {
		var ok bool
		if from, enters, ok = w.gatePhases(sp, g); !ok {
			return nil
		}
		if transition == "" {
			transition = s.Name
		}
		if d := g.Decision; d != nil && d.BackTo != "" {
			backTo = d.BackTo
			if back == "" {
				back = "back_" + s.Name
			}
		} else if back != "" {
			w.add(sp+".gate.decision.back", "with a maturity, the decision goes back to a phase: name it (backTo)")
			return nil
		}
	}
	if w.gates[transition] != "" {
		w.add(sp+".gate.transition", "the transition %s is already the gate %s", transition, w.gates[transition])
		return nil
	}
	roles := s.Roles
	if roles == nil || roles.Accountable == "" {
		w.add(sp+".roles.accountable", "a gate answers to an accountable role of its own (roles.accountable): it decides, or chairs the board")
		return nil
	}
	info := GateInfo{Path: path, Transition: transition, Accountable: roles.Accountable, Freezes: g.Freezes, Method: s.Capability,
		Question: fmt.Sprintf("Go for %s?", s.Name), From: from, Enters: enters, BackTo: backTo, By: DecidedByHuman}
	if w.m.Maturity != nil && len(need) > 0 {
		info.Entry = maps.Clone(need)
	}
	if s.Description != "" {
		info.Question = fmt.Sprintf("Go for %s (%s)?", s.Name, strings.TrimSuffix(s.Description, "."))
	}
	if d := g.Decision; d != nil {
		if d.Question != "" {
			info.Question = d.Question
		}
		info.Back, info.Rule, info.MaxDuration, info.Board = back, d.Rule, d.MaxDuration, slices.Clone(d.Board)
		switch d.By {
		case "", DecidedByHuman:
			if d.Code != "" {
				w.add(sp+".gate.decision.code", "code rules a decision taken by a script (by: script)")
			}
		case DecidedByScript:
			info.By = DecidedByScript
			switch {
			case d.Code == "":
				w.add(sp+".gate.decision.code", "a script decides with code: write it (ctx.decide on the open point of the gate)")
			case len(d.Board) > 0 || d.Rule != "":
				w.add(sp+".gate.decision.by", "a board is people: a script decides alone (no board, no rule)")
			case s.Capability != "":
				w.add(sp+".gate.decision.by", "the decision is a method of the step or a script, not both")
			}
		default:
			w.add(sp+".gate.decision.by", "a gate is decided by a human or a script, not %q", d.By)
		}
		if d.Rule != "" && !slices.Contains(gateRules, d.Rule) {
			w.add(sp+".gate.decision.rule", "a board rules by unanimity, majority or consent, not %q", d.Rule)
		}
		if d.Rule != "" && len(d.Board) == 0 {
			w.add(sp+".gate.decision.rule", "a rule settles the rulings of a board: name its roles (board)")
		}
		for j, r := range d.Board {
			if !w.roles[r] {
				w.add(fmt.Sprintf("%s.gate.decision.board[%d]", sp, j), "unknown role %q", r)
			}
		}
		if back != "" && back == transition {
			w.add(sp+".gate.decision.back", "back names another transition than the one the go takes")
		}
	}
	if info.By == DecidedByScript {
		info.Board = nil
	} else if len(info.Board) == 0 {
		info.Board = []string{roles.Accountable}
	} else if !slices.Contains(info.Board, roles.Accountable) {
		info.Board = append(info.Board, roles.Accountable) // the accountable role sits on every board
	}
	if f := g.Freezes; f != nil {
		for j, t := range f.Types {
			if msg := w.m.checkTypeRef(t, false); msg != "" {
				w.add(fmt.Sprintf("%s.gate.freezes.types[%d]", sp, j), "%s", msg)
			}
		}
		for j, fl := range f.Fields {
			if !slices.Contains([]string{domain.FreezeTitle, domain.FreezeIntent, domain.FreezeDescription}, fl) {
				w.add(fmt.Sprintf("%s.gate.freezes.fields[%d]", sp, j), "a gate freezes title, intent or description, not %q", fl)
			}
		}
	}
	open := maps.Clone(need)
	seen := map[string]bool{}
	for j, it := range g.Checklist {
		ip := fmt.Sprintf("%s.gate.checklist[%d]", sp, j)
		switch {
		case it.Name == "":
			w.add(ip+".name", "name the condition the item checks")
			continue
		case seen[it.Name]:
			w.add(ip+".name", "duplicate item %s", it.Name)
			continue
		}
		seen[it.Name] = true
		if it.Role == "" {
			it.Role = roles.Accountable
		} else if !w.roles[it.Role] {
			w.add(ip+".role", "unknown role %q", it.Role)
		}
		if it.When != "" {
			if _, err := condition.Compile([]condition.Definition{{Name: "when", Expr: it.When}}); err != nil {
				w.add(ip+".when", "%s", strings.TrimPrefix(err.Error(), `condition "when": `))
				continue
			}
		}
		cond := it.Name
		if it.Attest {
			if !def.NameRE.MatchString(it.Name) {
				w.add(ip+".name", "an attested item is named with lowercase letters, digits, '-' or '_'")
				continue
			}
			cond = AttestedCondition(path, it.Name)
			key := path + "/" + it.Name
			w.known[cond] = true
			w.out.conditions = append(w.out.conditions, condition.Definition{Name: cond,
				Expr:        fmt.Sprintf("%q in attestations && attestations[%q].verdict == %q", key, key, "met"),
				Description: fmt.Sprintf("a %s attested %s for the gate %s", it.Role, firstNonEmpty(it.Description, it.Name), path)})
			w.out.actions = append(w.out.actions, attestAction(path, it, need))
		} else {
			w.refs = append(w.refs, conditionRef{ip + ".name", cond})
		}
		if it.Veto && it.When == "" {
			// an applicable veto is needed before the decision is opened; an objective is not
			open[cond] = true
		}
		info.Items = append(info.Items, GateItemInfo{GateItem: it, Condition: cond})
	}
	passed, opened, went := GatePassed(path), GateOpen(path), GateGo(path)
	for _, d := range []condition.Definition{
		{Name: passed, Expr: fmt.Sprintf("%q in change.passed", transition),
			Description: fmt.Sprintf("the gate %s is passed: the change took %s and has not gone back since", path, transition)},
		{Name: opened, Expr: fmt.Sprintf(`decisionPoints.exists(d, "gate" in d.policy && d.policy.gate == %q && d.status != "decided")`, path),
			Description: fmt.Sprintf("the decision of the gate %s is open, waiting for its rulings", path)},
		{Name: went, Expr: fmt.Sprintf(`decisionPoints.exists(d, "gate" in d.policy && d.policy.gate == %q && d.status == "decided" && d.option == %q && !(d.id in change.consumed))`, path, OptionGo),
			Description: fmt.Sprintf("the decision of the gate %s said go", path)},
	} {
		w.known[d.Name] = true
		w.out.conditions = append(w.out.conditions, d)
	}
	params := map[string]any{"gate": path, "transition": transition}
	if s.Capability != "" {
		// the decision workflow is a method: run as a call once the decision is open, towards its go
		candidates := w.m.methodsFor(s.Capability, w.methods.goals)
		if len(candidates) == 0 {
			w.add(sp+".method", "no valid method provides %q (methods[].for)", s.Capability)
			return nil
		}
		w.out.actions = append(w.out.actions, Action{Name: path + ":decide", Description: "Decide the gate " + path, Kind: KindCall,
			Call: &Call{Capability: s.Capability}, Pre: with(open, opened, true), Effects: map[string]bool{went: true}, Cost: 1, Step: path})
	} else if info.By == DecidedByScript {
		// the decision is ruled by the code of the gate, an agent
		w.out.actions = append(w.out.actions, Action{Name: RuleAction(path), Kind: KindScript, Description: "Rule the gate " + path + " by its script",
			Language: LangJavaScript, Code: g.Decision.Code, Pre: with(open, opened, true), Effects: map[string]bool{went: true}, Cost: 1, Step: path, Incremental: true,
			Params: map[string]any{"gate": path}})
	} else {
		// the default decision: the people of the board rule it, each in their seat, until its rule settles it
		w.out.actions = append(w.out.actions, ruleAction(path, info, with(open, opened, true)))
	}
	w.out.actions = append(w.out.actions,
		Action{Name: path + ":open", Description: "Open the decision of the gate " + path, Kind: KindBuiltin, Builtin: builtins.GateOpen,
			Incremental: true, Params: params, Pre: maps.Clone(open), Effects: map[string]bool{opened: true}, Cost: 1, Step: path},
		Action{Name: path, Description: firstNonEmpty(s.Description, "Pass the gate "+path), Kind: KindBuiltin, Builtin: builtins.GatePass,
			Params: params, Pre: with(need, went, true), Effects: map[string]bool{passed: true}, Cost: 1, Step: path})
	w.gates[transition] = path
	w.root.gates = append(w.root.gates, info)
	return map[string]bool{passed: true}
}

// attestAction is the human action a person of the role of an item attests it with: a verdict and a comment, recorded
// as the artifact attestation:<path>/<item>.
func attestAction(path string, it GateItem, need map[string]bool) Action {
	what := firstNonEmpty(it.Description, it.Name)
	return Action{Name: AttestAction(path, it.Name), Kind: KindHuman, Description: fmt.Sprintf("Attest %s for the gate %s", what, path),
		Instructions: fmt.Sprintf("As %s, attest whether %s holds for this change: met or not met, with what you checked.", it.Role, what),
		Roles:        []string{it.Role}, Pre: maps.Clone(need), Effects: map[string]bool{AttestedCondition(path, it.Name): true}, Cost: 1, Step: path,
		Form: &form.Form{Artifact: condition.AttestationPrefix + path + "/" + it.Name, Fields: []form.Field{
			{Name: "verdict", Label: "Verdict", Widget: form.WidgetSelect, Required: true, Constraints: map[string]any{"options": []any{"met", "not_met"}}},
			{Name: "comment", Label: "What was checked", Widget: form.WidgetTextarea, Required: true},
		}}}
}

// RuleAction is the name of the human action the board of a gate rules its decision with.
func RuleAction(path string) string { return path + ":rule" }

// ruleAction is the human action a person of the board of a gate rules its decision with, in one of the seats: go,
// no go or back, and why. It runs again until the rule of the board settles the decision (incremental).
func ruleAction(path string, g GateInfo, pre map[string]bool) Action {
	seats := make([]any, 0, len(g.Board))
	for _, r := range g.Board {
		seats = append(seats, r)
	}
	return Action{Name: RuleAction(path), Kind: KindHuman, Description: "Rule the gate " + path,
		Instructions: fmt.Sprintf("In your seat on the board of the gate, rule the go for %s: go, no go or back, and why.", g.Transition),
		Roles:        slices.Clone(g.Board), Pre: pre, Effects: map[string]bool{GateGo(path): true}, Cost: 1, Step: path, Incremental: true,
		Params: map[string]any{"gate": path},
		Form: &form.Form{Artifact: "ruling:" + path, Fields: []form.Field{
			{Name: "ruling", Label: "Ruling", Widget: form.WidgetDecision, Required: true, Constraints: map[string]any{"roles": seats, "justify": true}},
		}}}
}

func with(set map[string]bool, k string, v bool) map[string]bool {
	out := maps.Clone(set)
	if out == nil {
		out = map[string]bool{}
	}
	out[k] = v
	return out
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// checkManualSteps remarks the process steps that do nothing by themselves (ADR 0132 §3): a person is in the loop
// through a human action of a method or the decision of a gate, never by the engine waiting on a step. A warning: the
// methodologies already written keep their manual steps, done by a generated human action.
func (s *compileState) checkManualSteps() {
	var walk func(list []Step, path string)
	walk = func(list []Step, path string) {
		for i, st := range list {
			sp := fmt.Sprintf("%s.steps[%d]", path, i)
			if st.Method() == MethodManual {
				s.issues = append(s.issues, def.Issue{Path: sp, Severity: def.SeverityWarning,
					Message: fmt.Sprintf("the step %s does nothing by itself: name a method (its human action says who does what), or make it a gate (gate:) when it is a go of a person", st.Name)})
			}
			walk(st.Steps, sp)
		}
	}
	for i, p := range s.m.Processes {
		walk(p.Steps, fmt.Sprintf("processes[%d]", i))
	}
}

// gatePaths are the paths of the gate steps of the processes of m, as declared.
func (m *Methodology) gatePaths() []string {
	var out []string
	var walk func(prefix string, steps []Step)
	walk = func(prefix string, steps []Step) {
		for _, st := range steps {
			path := prefix + "/" + st.Name
			if st.Gate != nil {
				out = append(out, path)
			}
			walk(path, st.Steps)
		}
	}
	for _, p := range m.Processes {
		walk(p.Name, p.Steps)
	}
	return out
}

// gateActions are the actions generated for the gates (attestations, opening, ruling or decision, passage).
func (c *Compiled) gateActions() []Action {
	gates := map[string]bool{}
	for _, g := range c.gates {
		gates[g.Path] = true
	}
	var out []Action
	for _, p := range c.definition.Processes {
		for _, a := range c.StepActions(p.Name) {
			if gates[a.Step] {
				out = append(out, a)
			}
		}
	}
	return out
}
