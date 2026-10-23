package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/zimwip/goap/contracts/codetext"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/graphapi"
	"github.com/zimwip/goap/engine/condition"
	"github.com/zimwip/goap/engine/methodology"
)

// The control function of an execution (ADR 0162): the question is what an activity does (its action, its step, its
// subject, on a change), the answer the controls that govern it, each established (its rules collected: the rules
// that govern the subject, none being a valid answer) or pending (not collected yet: the activity waits for it).

// ControlSet is the control function of an execution: its controls and the rules they give it.
type ControlSet struct {
	// Subject is the subject the rules were resolved for (the key of a node; empty: the change as a whole).
	Subject  string         `json:"subject,omitempty"`
	Controls []ControlState `json:"controls"`
}

// ControlState is a control of an execution.
type ControlState struct {
	// Condition is the control condition; Description what it says.
	Condition   string `json:"condition"`
	Description string `json:"description,omitempty"`
	// State: established (the rules were collected; Rules may be empty) or pending (not yet: no rule is known).
	State string          `json:"state"`
	Rules []GoverningRule `json:"rules,omitempty"`
}

// GoverningRule is a rule governing an execution: a general rule of the methodology, or a node of the domain.
type GoverningRule struct {
	methodology.ControlRule
	// Version is the version of the node of a rule of the domain (0: a rule of the methodology).
	Version domain.Version `json:"version,omitempty"`
	// Condition is the control that gave the rule.
	Condition string `json:"condition"`
}

// The states of a control.
const (
	ControlEstablished = "established"
	ControlPending     = "pending"
)

// Ref is how the run records the rule (Step.Controls).
func (r GoverningRule) Ref() string {
	if r.Version > 0 {
		return fmt.Sprintf("%s@%d", r.ID, r.Version)
	}
	return r.Condition + "/" + r.ID
}

// Rules are the rules of the established controls, each once (the first control giving a rule keeps it), the vetos
// first.
func (s *ControlSet) Rules() []GoverningRule {
	if s == nil {
		return nil
	}
	var out []GoverningRule
	seen := map[string]bool{}
	for _, c := range s.Controls {
		for _, r := range c.Rules {
			if !seen[r.ID] {
				seen[r.ID] = true
				out = append(out, r)
			}
		}
	}
	slices.SortStableFunc(out, func(a, b GoverningRule) int {
		return slices.Index(methodology.Severities, a.SeverityOf()) - slices.Index(methodology.Severities, b.SeverityOf())
	})
	return out
}

// Established tells whether every control of the set is established.
func (s *ControlSet) Established() bool {
	if s == nil {
		return true
	}
	for _, c := range s.Controls {
		if c.State != ControlEstablished {
			return false
		}
	}
	return true
}

// Refs are what the run records of the controls it was given (Step.Controls).
func (s *ControlSet) Refs() []string {
	if s == nil {
		return nil
	}
	var out []string
	for _, c := range s.Controls {
		if c.State != ControlEstablished {
			out = append(out, c.Condition+"?")
		}
	}
	for _, r := range s.Rules() {
		out = append(out, r.Ref())
	}
	return out
}

// section is the controls as the system prompt of an LLM action tells them: one line per rule, the statement only
// (the cheapest feedforward: the agent asks for the rest when it needs it).
func (s *ControlSet) section() string {
	if s == nil || len(s.Controls) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nCONTROLS: the rules governing what you produce (a veto blocks your result, an objective needs a derogation, an advice is noted); a check measures them on your result and answers with the rules broken:\n")
	rules := s.Rules()
	for _, r := range rules {
		fmt.Fprintf(&b, "- %s [%s] %s", r.ID, r.SeverityOf(), oneLine(r.Statement))
		if r.Check == nil {
			b.WriteString(" (not measured)")
		}
		b.WriteString("\n")
	}
	if len(rules) == 0 {
		b.WriteString("- none: the rules were collected and none governs this work\n")
	}
	for _, c := range s.Controls {
		if c.State != ControlEstablished {
			fmt.Fprintf(&b, "- %s: not collected yet, its rules are unknown\n", c.Condition)
		}
	}
	return b.String()
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// ControlOf is the control function of an execution on a change (ADR 0162): the controls of the action and of the step
// (with the steps containing it), resolved for the subject (the key of a node of the change's view; empty: none).
func (e *Engine) ControlOf(ctx context.Context, changeID domain.ChangeID, action, step, subject string) (*ControlSet, error) {
	bb, err := e.blackboard(ctx, changeID, "")
	if err != nil {
		return nil, err
	}
	if bb.Change.Methodology == "" {
		return &ControlSet{Subject: subject, Controls: []ControlState{}}, nil
	}
	m, err := e.Methodologies.Methodology(ctx, bb.Change.MethodologyRef())
	if err != nil {
		return nil, err
	}
	bb.Supertypes = e.supertypesOf(m)
	world := m.Conditions.Evaluate(bb).State
	var paths []string
	if step != "" {
		paths = []string{step}
	}
	return e.resolveControls(ctx, graphapi.View{Change: changeID}, bb, world, m.ControlsOf(action, paths...), subject)
}

// controlsOf is the control function of the run of an action (nil when nothing controls it): the controls of the action
// and of its step, for the subject of a scoped run.
func (e *Engine) controlsOf(ctx context.Context, p *Process, m *methodology.Compiled, bb domain.Blackboard, action methodology.Action, sc *StepContext) (*ControlSet, error) {
	var paths []string
	if action.Step != "" {
		paths = append(paths, action.Step)
	}
	if sc != nil && sc.Path != "" && !slices.Contains(paths, sc.Path) {
		paths = append(paths, sc.Path)
	}
	controls := m.ControlsOf(action.Name, paths...)
	if len(controls) == 0 || p.ChangeID == "" {
		return nil, nil
	}
	subject, _ := bb.Vars["subject"].(string)
	world := m.Conditions.Evaluate(bb).State
	return e.resolveControls(ctx, viewOf(p), bb, world, controls, subject)
}

// resolveControls resolves controls for a subject in a view: an established control gives its general rules, then the
// nodes of its types the subject is linked from (via) and its filter keeps.
func (e *Engine) resolveControls(ctx context.Context, v graphapi.View, bb domain.Blackboard, world map[string]bool, controls []methodology.ControlInfo, subject string) (*ControlSet, error) {
	out := &ControlSet{Subject: subject, Controls: []ControlState{}}
	subj := map[string]any{}
	var in []domain.Link
	if subject != "" {
		switch n, err := e.Graph.ViewNode(ctx, v, "", subject); {
		case err == nil:
			subj, in = ruleSubject(n.Node), n.In
		case errors.Is(err, graphapi.ErrNotFound):
			subj = map[string]any{"key": subject} // a subject that is no node (an item): known by its id only
		default:
			return nil, err
		}
	}
	change, _ := condition.Resolve(condition.Activation(bb)["change"]).(map[string]any)
	byType := map[string][]domain.Node{}
	for _, c := range controls {
		st := ControlState{Condition: c.Condition, Description: c.Description, State: ControlPending}
		if world[c.Condition] {
			st.State = ControlEstablished
			for _, r := range c.Rules {
				st.Rules = append(st.Rules, GoverningRule{ControlRule: r, Condition: c.Condition})
			}
			linked := map[domain.NodeID]bool{}
			for _, l := range in {
				if l.Type == c.Via {
					linked[l.From.ID] = true
				}
			}
			for _, t := range c.Types {
				nodes, ok := byType[t]
				if !ok {
					var err error
					if nodes, err = e.Graph.ViewNodesOfType(ctx, v, t); err != nil {
						return nil, fmt.Errorf("control %s: rules of %s: %w", c.Condition, t, err)
					}
					byType[t] = nodes
				}
				for _, n := range nodes {
					if c.Via != "" && !linked[n.ID] {
						continue
					}
					if !c.Where.Keeps(ruleSubject(n), subj, change) {
						continue
					}
					st.Rules = append(st.Rules, ruleOfNode(n, c.Condition))
				}
			}
		}
		out.Controls = append(out.Controls, st)
	}
	return out, nil
}

// ruleSubject is a node as the filter of a control sees it (self, subject).
func ruleSubject(n domain.Node) map[string]any {
	props := n.Properties
	if props == nil {
		props = map[string]any{}
	}
	return map[string]any{"id": string(n.ID), "key": n.Key, "type": n.Type, "state": n.State, "props": props, "project": n.Project.Key}
}

// ruleOfNode reads a rule of the domain (alm@Rule): its statement, severity, check (a code text naming its language),
// fix.
func ruleOfNode(n domain.Node, cond string) GoverningRule {
	str := func(k string) string { s, _ := n.Properties[k].(string); return s }
	r := GoverningRule{ControlRule: methodology.ControlRule{ID: n.Key, Statement: str("statement"), Severity: str("severity"), Fix: str("fix")},
		Version: n.Version, Condition: cond}
	if text := str("check"); strings.TrimSpace(text) != "" {
		if v, err := codetext.Parse(text); err == nil && strings.TrimSpace(v.Code) != "" {
			r.Check = &methodology.ControlCheck{Language: v.Language, Code: v.Code}
		}
	}
	return r
}
