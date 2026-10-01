package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/brief"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/dsl"
	"github.com/zimwip/goap/pkg/methodology"
)

// RunStep is the builtin process.step (ADR 0034): the action generated for a step of a process done by an agent or a
// nested process. It starts that agent (towards the step's goal) or the agent of the nested process as a sub-agent on
// the same change and waits for it; once it has completed, a step_done artifact records the step. A sub-agent that
// fails (or is abandoned) fails the step; one that is stuck keeps the step waiting until a person unblocks it. Params: step (path), agent, goal, methodology (empty: this one).
func RunStep(ctx context.Context, ac ActionContext) (ActionResult, error) {
	h := ac.Host
	if h == nil || ac.Process.ChangeID == "" {
		return ActionResult{}, errors.New("process.step needs a process on a change")
	}
	step, _ := ac.Action.Params["step"].(string)
	agent, _ := ac.Action.Params["agent"].(string)
	goal, _ := ac.Action.Params["goal"].(string)
	methodologyName, _ := ac.Action.Params["methodology"].(string)
	if methodologyName == "" {
		methodologyName = ac.Process.Methodology
	}
	sc := ac.Step
	method := ""
	if foreach, _ := ac.Action.Params["foreach"].(string); foreach != "" {
		return runForeach(ctx, ac, foreach)
	}
	if capability, _ := ac.Action.Params["capability"].(string); capability != "" {
		me, err := h.e.chooseMethod(ctx, ac, capability, ac.Blackboard, true)
		if err != nil {
			return ActionResult{}, fmt.Errorf("step %s: %w", step, err)
		}
		agent, goal, method, sc = me.ActorAgent(), me.AgentGoal, me.Name, sc.withMethod(me.Method)
	}
	if step == "" || agent == "" || goal == "" {
		return ActionResult{}, fmt.Errorf("process.step %s: step, agent and goal are required", ac.Action.Name)
	}
	intent := ac.Action.Description
	if intent == "" {
		intent = "step " + step
	}
	if ac.Blackboard.Change.Intent != "" {
		intent += "\n\n(change: " + ac.Blackboard.Change.Intent + ")"
	}
	res, err := h.e.runChildStep(authz.With(ctx, h.e.actor(ac.Process)), h, ac.Action.Name+"#step", methodologyName, agent, goal, intent, false, sc)
	if errors.Is(err, dsl.ErrSuspended) {
		return ActionResult{Suspended: true, Child: h.waitingOn, Method: method, Output: fmt.Sprintf("step %s: %s/%s at work", step, methodologyName, agent)}, nil
	}
	if err != nil {
		return ActionResult{}, fmt.Errorf("step %s: %w", step, err)
	}
	if res.Status != string(StatusCompleted) {
		return ActionResult{}, fmt.Errorf("step %s: %s/%s ended %s (process %s)", step, methodologyName, agent, res.Status, res.ProcessID)
	}
	return ActionResult{
		Items: []ItemInput{{Kind: string(domain.KindArtifact), Type: methodology.ArtifactStepDone,
			Data: map[string]any{"step": step, "process": res.ProcessID, "agent": agent, "goal": goal, "method": method}}},
		Method: method,
		Output: fmt.Sprintf("step %s done by %s/%s (process %s)", step, methodologyName, agent, res.ProcessID),
	}, nil
}

// chooseMethod picks the method of a capability for the step being run (ADR 0035 §1): the applicable one (its context
// holds on the blackboard) with the highest priority whose agent the unit holding the change can run (the MCPs the
// agent declares are bound, design rule 5). A step resumed after its sub-agent keeps the sub-agent it started.
func (e *Engine) chooseMethod(ctx context.Context, ac ActionContext, capability string, bb domain.Blackboard, resumable bool) (methodology.MethodChoice, error) {
	m, err := e.Methodologies.Methodology(ctx, ac.Process.Methodology)
	if err != nil {
		return methodology.MethodChoice{}, err
	}
	if _, resumed := ac.Process.Children[ac.Action.Name+"#step"]; resumed && resumable {
		// the sub-agent started for the step is at work: keep the method it was started for
		for i := len(ac.Process.Steps) - 1; i >= 0; i-- {
			if st := ac.Process.Steps[i]; st.Action == ac.Action.Name && st.Specialization != "" {
				for _, me := range m.MethodsFor(capability, bb) {
					if me.Name == st.Specialization {
						return me, nil
					}
				}
				if me, ok := m.MethodByName(st.Specialization); ok {
					return methodology.MethodChoice{Method: me, AgentGoal: m.MethodGoal(me.Name)}, nil
				}
			}
		}
	}
	candidates := m.MethodsFor(capability, bb)
	var bound map[string]bool
	for _, me := range candidates {
		ag, _ := m.Agent(me.ActorAgent())
		if len(ag.MCPs) > 0 && bound == nil {
			if bound, err = e.boundMCPs(ctx, ac.Process); err != nil {
				return methodology.MethodChoice{}, err
			}
		}
		if !slices.ContainsFunc(ag.MCPs, func(n string) bool { return !bound[n] }) {
			return me, nil
		}
	}
	return methodology.MethodChoice{}, fmt.Errorf("no method for %q applies here (%d in this context, none the unit can run)", capability, len(candidates))
}

// withMethod adds what the chosen method says to the step's context: the method is the documentary reference of how
// the step is carried out here.
func (s *StepContext) withMethod(me methodology.Method) *StepContext {
	out := StepContext{Method: me.Name}
	if s != nil {
		out = *s
		out.Method = me.Name
	}
	if me.Guidance != "" {
		out.Guidance = strings.TrimSpace(strings.TrimSpace(out.Guidance) + "\n\n" + me.Guidance)
	}
	out.Checklist = append(slices.Clone(out.Checklist), me.Checklist...)
	out.Deliverables = append(slices.Clone(out.Deliverables), me.Deliverables...)
	out.References = append(slices.Clone(out.References), me.References...)
	if me.Roles != nil {
		out.Roles = me.Roles // the method is the more precise reference
	}
	return &out
}

// StepContext is the step of a process an action or a human task carries out (ADR 0034, ADR 0035 §2): what it is for
// and how to go about it, for the person doing it or the agent.
type StepContext struct {
	Process string `json:"process"`
	Path    string `json:"path"`
	// Roles assign the step (ADR 0035 §2): the responsible role performs its tasks, the accountable role may approve
	// its gates.
	Roles *methodology.Responsibilities `json:"roles,omitempty"`
	// Method is the method chosen to carry the step out, whose guidance and references are included.
	Method string `json:"method,omitempty"`
	// Item is the element a stream of a step with foreach is carried out for, and ItemKey its identity (ADR 0050).
	Item         any                     `json:"item,omitempty"`
	ItemKey      string                  `json:"itemKey,omitempty"`
	Name         string                  `json:"name"`
	Description  string                  `json:"description,omitempty"`
	Guidance     string                  `json:"guidance,omitempty"`
	Checklist    []string                `json:"checklist,omitempty"`
	Deliverables []string                `json:"deliverables,omitempty"`
	References   []methodology.Reference `json:"references,omitempty"`
}

// stepContext is the step an action of p carries out: the step it was generated for, else the step p itself carries
// out for its parent (nil: none).
func stepContext(m *methodology.Compiled, p *Process, a methodology.Action) *StepContext {
	if a.Step == "" {
		return p.Step
	}
	s, ok := m.StepByPath(a.Step)
	if !ok {
		return p.Step
	}
	process, _, _ := strings.Cut(s.Path, "/")
	return &StepContext{Process: process, Path: s.Path, Name: s.Name, Description: s.Description, Guidance: s.Guidance,
		Checklist: s.Checklist, Deliverables: s.Deliverables, References: s.References, Roles: s.Effective}
}

// section is the step as the system prompt of an LLM action tells it ("" when there is none).
func (s *StepContext) section() string {
	if s == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nYou are carrying out the step %q of the process %q", s.Path, s.Process)
	if s.Method != "" {
		fmt.Fprintf(&b, " with the method %q", s.Method)
	}
	if s.Description != "" {
		fmt.Fprintf(&b, ": %s", s.Description)
	}
	b.WriteString(".\n")
	if s.Roles != nil && s.Roles.Responsible != "" {
		fmt.Fprintf(&b, "You act as the role %q for this step.\n", s.Roles.Responsible)
	}
	// ADR 0050: an agent instance is created for the step, it has no memory of the earlier ones
	b.WriteString("You start this step fresh: nothing was said to you before. What matters is on the change; read it (the brief below, goap-change/brief, trace) and record what you produce on the change as items, not only in your answer.\n")
	if s.Guidance != "" {
		fmt.Fprintf(&b, "Guidance for this step:\n%s\n", strings.TrimSpace(s.Guidance))
	}
	if len(s.Deliverables) > 0 {
		fmt.Fprintf(&b, "Deliverables of this step: %s.\n", strings.Join(s.Deliverables, ", "))
	}
	if len(s.References) > 0 {
		b.WriteString("Reference documents of this step:\n")
		for _, r := range s.References {
			fmt.Fprintf(&b, "- %s\n", r.String())
		}
	}
	return b.String()
}

// stepAllowed reports whether who may act on the step of a context: "perform" (its responsible role) or "approve" (its
// accountable role), in the unit holding the change (ADR 0035 §2). A step that assigns no such role leaves the
// decision to the process's own permissions (true).
func (e *Engine) stepAllowed(ctx context.Context, p *Process, who authz.Principal, sc *StepContext, act string) (bool, error) {
	if e.Authz == nil || sc == nil || sc.Roles == nil {
		return true, nil
	}
	role := sc.Roles.Responsible
	if act == "approve" {
		role = sc.Roles.Accountable
	}
	if role == "" {
		return true, nil
	}
	return e.Authz.Authorize(ctx, authz.Request{Subject: who, Action: act, Resource: authz.Resource{Type: "step", ID: p.ID, Name: sc.Path,
		Org: e.orgOf(p), ProjectID: e.projectOf(p), Owner: p.Initiator.Subject, Role: sc.Roles.Responsible, Accountable: sc.Roles.Accountable}})
}

// briefSection is the compact brief of the change an LLM action works on (ADR 0036 §2): the most information in the
// fewest tokens, one line per fact.
func briefSection(ac ActionContext) string {
	if ac.Blackboard.Change.ID == "" {
		return ""
	}
	var st *brief.Step
	if s := ac.Step; s != nil {
		st = &brief.Step{Process: s.Process, Path: s.Path, Method: s.Method}
		if s.Roles != nil {
			st.Responsible, st.Accountable = s.Roles.Responsible, s.Roles.Accountable
		}
	}
	return "\n\nThe change you work on, in brief (goap-change/trace follows an item, a risk or a node through it):\n" + brief.Of(ac.Blackboard, st)
}
