package registrysvc

import (
	"github.com/zimwip/goap/contracts/domain"
	registryv1 "github.com/zimwip/goap/contracts/gen/goap/registry/v1"
	"github.com/zimwip/goap/engine/condition"
	"github.com/zimwip/goap/engine/engine"
	"github.com/zimwip/goap/engine/methodology"
	"github.com/zimwip/goap/lib/pbconv"
)

// ToPB converts a stored record.
func ToPB(r Record) *registryv1.Methodology {
	m := r.Methodology
	out := &registryv1.Methodology{Name: m.Name, Version: m.Version, Description: m.Description, Namespace: m.Namespace, AppliesTo: m.AppliesTo, Imports: m.Imports, On: subsToPB(m.On), Lifecycle: m.Lifecycle, Criticality: m.Criticality, Goal: m.Goal, Integrates: integrationsToPB(m.Integrates), Guardian: guardianToPB(m.Guardian), Events: eventsToPB(m.Events), Assessments: assessmentsToPB(m.Assessments), Domains: m.Domains, Status: string(r.Status),
		CreatedAt: pbconv.Time(r.CreatedAt), UpdatedAt: pbconv.Time(r.UpdatedAt), PublishedAt: pbconv.Time(r.PublishedAt), UpdatedBy: r.UpdatedBy}
	for _, c := range m.Conditions {
		out.Conditions = append(out.Conditions, &registryv1.Condition{Name: c.Name, Description: c.Description, Expr: c.Expr,
			Scope: c.Scope, Where: c.Where, All: c.All, Any: c.Any, None: c.None, Empty: c.Empty})
	}
	for _, a := range m.Actions {
		pa := &registryv1.Action{Name: a.Name, Description: a.Description, Kind: a.Kind, Pre: a.Pre, Effects: a.Effects, Cost: a.Cost,
			Permission: a.Permission, Model: a.Model, Prompt: a.Prompt, Tool: a.Tool, Builtin: a.Builtin, Instructions: a.Instructions,
			Params: pbconv.Struct(a.Params), Language: a.Language, Code: a.Code, Utility: a.Utility,
			Specializes: a.Specializes, When: a.When, Priority: int32(a.Priority), Incremental: a.Incremental, Mcps: a.MCPs, On: subsToPB(a.On), Emits: a.Emits, Scope: a.Scope, Where: a.Where, Parallel: int32(a.Parallel),
			Form: pbconv.FormToPB(a.Form), Loop: loopToPB(a.Loop), Roles: a.Roles}
		if e := a.Expects; e != nil {
			pe := &registryv1.Expectation{ForEach: e.ForEach, Where: e.Where, Produce: &registryv1.ProduceSpec{Op: e.Produce.Op, NodeType: e.Produce.NodeType}}
			if e.Link != nil {
				pe.Link = &registryv1.LinkSpec{Type: e.Link.Type, Direction: e.Link.Direction}
			}
			pa.Expects = pe
		}
		if v := a.Verify; v != nil {
			pa.Verify = &registryv1.Verify{Oracle: v.Oracle, Independent: v.Independent}
		}
		out.Actions = append(out.Actions, pa)
	}
	for _, g := range m.Goals {
		out.Goals = append(out.Goals, &registryv1.Goal{Name: g.Name, Description: g.Description, Examples: g.Examples, Pre: g.Pre, Value: g.Value})
	}
	for _, a := range m.Agents {
		pa := &registryv1.Agent{Name: a.Name, Description: a.Description, Examples: a.Examples, Planner: a.Planner, Actions: a.Actions, Goals: a.Goals, Mcps: a.MCPs, Model: a.Model, Persistent: persistToPB(a.Persistent), Roles: a.Roles}
		for _, t := range a.Triggers {
			pa.Triggers = append(pa.Triggers, &registryv1.Trigger{Name: t.Name, Description: t.Description, Type: t.Type, Event: t.Event, Filter: t.Filter,
				Schedule: t.Schedule, Goal: t.Goal, Intent: t.Intent, Target: t.Target, Roles: t.Roles, Enabled: t.Enabled, Supersede: t.Supersede})
		}
		out.Agents = append(out.Agents, pa)
	}
	for _, r := range m.Roles {
		out.Roles = append(out.Roles, &registryv1.Role{Name: r.Name, Description: r.Description, Operations: r.Operations})
	}
	for _, me := range m.Methods {
		out.Methods = append(out.Methods, &registryv1.Method{Name: me.Name, For: me.For, When: me.When, Priority: int32(me.Priority), Description: me.Description,
			Guidance: me.Guidance, Checklist: me.Checklist, Deliverables: me.Deliverables, References: refsToPB(me.References), Roles: respToPB(me.Roles),
			Steps: stepsToPB(me.Steps), Actions: me.Actions, Done: me.Done, Planner: me.Planner, Model: me.Model, Mcps: me.MCPs, On: subsToPB(me.On), Persistent: persistToPB(me.Persistent)})
	}
	for _, p := range m.Processes {
		out.Processes = append(out.Processes, &registryv1.Process{Name: p.Name, Description: p.Description, Examples: p.Examples, Steps: stepsToPB(p.Steps), References: refsToPB(p.References), On: subsToPB(p.On)})
	}
	return out
}

func stepsToPB(steps []methodology.Step) []*registryv1.Step {
	out := make([]*registryv1.Step, 0, len(steps))
	for _, s := range steps {
		out = append(out, &registryv1.Step{Name: s.Name, Description: s.Description, Instructions: s.Instructions, Pre: s.Pre, Done: s.Done,
			References: refsToPB(s.References), Guidance: s.Guidance, Checklist: s.Checklist, Deliverables: s.Deliverables, Method: s.Capability, Foreach: s.Foreach, GroupBy: s.GroupBy, Roles: respToPB(s.Roles), Steps: stepsToPB(s.Steps), Action: s.Action, Actions: s.Actions, Process: s.Process, On: subsToPB(s.On), Where: s.Where})
	}
	return out
}

func stepsFromPB(steps []*registryv1.Step) []methodology.Step {
	if len(steps) == 0 {
		return nil
	}
	out := make([]methodology.Step, 0, len(steps))
	for _, s := range steps {
		out = append(out, methodology.Step{Name: s.Name, Description: s.Description, Instructions: s.Instructions, Pre: nilIfEmpty(s.Pre), Done: nilIfEmpty(s.Done),
			References: refsFromPB(s.References), Guidance: s.Guidance, Checklist: nilIfNone(s.Checklist), Deliverables: nilIfNone(s.Deliverables), Capability: s.Method, Foreach: s.Foreach, GroupBy: s.GroupBy, Roles: respFromPB(s.Roles), Steps: stepsFromPB(s.Steps), Action: s.Action, Actions: nilIfNone(s.Actions), Process: s.Process, On: subsFromPB(s.On), Where: s.Where})
	}
	return out
}

func eventsToPB(evs []methodology.Event) []*registryv1.EventDef {
	var out []*registryv1.EventDef
	for _, e := range evs {
		out = append(out, &registryv1.EventDef{Name: e.Name, Description: e.Description})
	}
	return out
}

func eventsFromPB(evs []*registryv1.EventDef) []methodology.Event {
	var out []methodology.Event
	for _, e := range evs {
		out = append(out, methodology.Event{Name: e.Name, Description: e.Description})
	}
	return out
}

func assessmentsToPB(vs []methodology.AssessmentView) []*registryv1.AssessmentView {
	var out []*registryv1.AssessmentView
	for _, v := range vs {
		pv := &registryv1.AssessmentView{Subject: v.Subject}
		for _, s := range v.States {
			pv.States = append(pv.States, &registryv1.AssessmentState{When: s.When, Conditions: s.Conditions, Tone: s.Tone, Label: s.Label, Open: s.Open})
		}
		out = append(out, pv)
	}
	return out
}

func assessmentsFromPB(vs []*registryv1.AssessmentView) []methodology.AssessmentView {
	var out []methodology.AssessmentView
	for _, v := range vs {
		mv := methodology.AssessmentView{Subject: v.Subject}
		for _, s := range v.States {
			mv.States = append(mv.States, methodology.AssessmentState{When: s.When, Conditions: nilIfEmpty(s.Conditions), Tone: s.Tone, Label: s.Label, Open: s.Open})
		}
		out = append(out, mv)
	}
	return out
}

func subsToPB(subs []methodology.Subscription) []*registryv1.Subscription {
	var out []*registryv1.Subscription
	for _, s := range subs {
		out = append(out, &registryv1.Subscription{Event: s.Event, Filter: s.Filter, From: s.From, Supersede: s.Supersede})
	}
	return out
}

func subsFromPB(subs []*registryv1.Subscription) []methodology.Subscription {
	var out []methodology.Subscription
	for _, s := range subs {
		out = append(out, methodology.Subscription{Event: s.Event, Filter: s.Filter, From: s.From, Supersede: s.Supersede})
	}
	return out
}

func integrationsToPB(ins []methodology.Integration) []*registryv1.Integration {
	var out []*registryv1.Integration
	for _, in := range ins {
		out = append(out, &registryv1.Integration{Methodologies: in.Methodologies, Exclusive: in.Exclusive, Attach: in.Attach})
	}
	return out
}

func integrationsFromPB(ins []*registryv1.Integration) []methodology.Integration {
	var out []methodology.Integration
	for _, in := range ins {
		out = append(out, methodology.Integration{Methodologies: nilIfNone(in.Methodologies), Exclusive: in.Exclusive, Attach: in.Attach})
	}
	return out
}

func guardianToPB(g *methodology.Guardian) *registryv1.Guardian {
	if g == nil {
		return nil
	}
	return &registryv1.Guardian{Merge: g.Merge, Transitions: g.Transitions}
}

func guardianFromPB(g *registryv1.Guardian) *methodology.Guardian {
	if g == nil || (g.Merge == "" && len(g.Transitions) == 0) {
		return nil
	}
	return &methodology.Guardian{Merge: g.Merge, Transitions: g.Transitions}
}

func respToPB(r *methodology.Responsibilities) *registryv1.Responsibilities {
	if r == nil {
		return nil
	}
	return &registryv1.Responsibilities{Responsible: r.Responsible, Accountable: r.Accountable, Consulted: r.Consulted, Informed: r.Informed}
}

func respFromPB(r *registryv1.Responsibilities) *methodology.Responsibilities {
	if r == nil {
		return nil
	}
	return &methodology.Responsibilities{Responsible: r.Responsible, Accountable: r.Accountable, Consulted: nilIfNone(r.Consulted), Informed: nilIfNone(r.Informed)}
}

func refsToPB(refs []methodology.Reference) []*registryv1.Reference {
	var out []*registryv1.Reference
	for _, r := range refs {
		out = append(out, &registryv1.Reference{Title: r.Title, Ref: r.Ref, Section: r.Section})
	}
	return out
}

func refsFromPB(refs []*registryv1.Reference) []methodology.Reference {
	var out []methodology.Reference
	for _, r := range refs {
		out = append(out, methodology.Reference{Title: r.Title, Ref: r.Ref, Section: r.Section})
	}
	return out
}

// SummaryToPB converts a record to a list entry.
func SummaryToPB(r Record) *registryv1.MethodologySummary {
	out := &registryv1.MethodologySummary{Name: r.Methodology.Name, Version: r.Methodology.Version, Description: r.Methodology.Description,
		Status: string(r.Status), UpdatedAt: pbconv.Time(r.UpdatedAt), PublishedAt: pbconv.Time(r.PublishedAt), Namespace: domain.NamespaceOf(r.Methodology.Namespace)}
	for _, g := range r.Methodology.Goals {
		out.Goals = append(out.Goals, &registryv1.GoalSummary{Name: g.Name, Description: g.Description})
	}
	for _, a := range r.Methodology.Agents {
		out.Agents = append(out.Agents, &registryv1.AgentSummary{Name: a.Name, Description: a.Description, Planner: a.Planner})
	}
	// a process is run by an agent of its name towards a goal of its name (ADR 0034)
	for _, p := range r.Methodology.Processes {
		out.Goals = append(out.Goals, &registryv1.GoalSummary{Name: p.Name, Description: p.Description})
		out.Agents = append(out.Agents, &registryv1.AgentSummary{Name: p.Name, Description: p.Description, Planner: "process"})
	}
	return out
}

// FromPB converts an edited definition (status and timestamps are ignored).
func FromPB(p *registryv1.Methodology) methodology.Methodology {
	if p == nil {
		return methodology.Methodology{}
	}
	m := methodology.Methodology{Name: p.Name, Version: p.Version, Description: p.Description, Namespace: p.Namespace, AppliesTo: nilIfNone(p.AppliesTo), Imports: nilIfNone(p.Imports), On: subsFromPB(p.On), Lifecycle: p.Lifecycle, Criticality: p.Criticality, Goal: p.Goal,
		Integrates: integrationsFromPB(p.Integrates), Guardian: guardianFromPB(p.Guardian), Events: eventsFromPB(p.Events), Assessments: assessmentsFromPB(p.Assessments), Domains: nilIfEmpty(p.Domains)}
	for _, c := range p.Conditions {
		m.Conditions = append(m.Conditions, methodology.Condition{Name: c.Name, Description: c.Description, Expr: c.Expr,
			Scope: c.Scope, Where: c.Where, All: c.All, Any: c.Any, None: c.None, Empty: c.Empty})
	}
	for _, a := range p.Actions {
		ma := methodology.Action{Name: a.Name, Description: a.Description, Kind: a.Kind, Pre: nilIfEmpty(a.Pre), Effects: nilIfEmpty(a.Effects),
			Cost: a.Cost, Permission: a.Permission, Model: a.Model, Prompt: a.Prompt, Tool: a.Tool, Builtin: a.Builtin,
			Instructions: a.Instructions, Params: pbconv.Map(a.Params), Language: a.Language, Code: a.Code, Utility: a.Utility,
			Specializes: a.Specializes, When: a.When, Priority: int(a.Priority), Incremental: a.Incremental, MCPs: a.Mcps, On: subsFromPB(a.On), Emits: nilIfNone(a.Emits), Scope: a.Scope, Where: a.Where, Parallel: int(a.Parallel),
			Form: pbconv.FormFromPB(a.Form), Loop: loopFromPB(a.Loop), Roles: nilIfNone(a.Roles)}
		if e := a.Expects; e != nil && e.ForEach != "" {
			me := &condition.Expectation{ForEach: e.ForEach, Where: e.Where}
			if e.Produce != nil {
				me.Produce = condition.ProduceSpec{Op: e.Produce.Op, NodeType: e.Produce.NodeType}
			}
			if e.Link != nil && e.Link.Type != "" {
				me.Link = &condition.LinkSpec{Type: e.Link.Type, Direction: e.Link.Direction}
			}
			ma.Expects = me
		}
		if v := a.Verify; v != nil {
			ma.Verify = &methodology.Verify{Oracle: v.Oracle, Independent: v.Independent}
		}
		m.Actions = append(m.Actions, ma)
	}
	for _, g := range p.Goals {
		m.Goals = append(m.Goals, methodology.Goal{Name: g.Name, Description: g.Description, Examples: nilIfNone(g.Examples), Pre: nilIfEmpty(g.Pre), Value: g.Value})
	}
	for _, a := range p.Agents {
		ma := methodology.Agent{Name: a.Name, Description: a.Description, Examples: nilIfNone(a.Examples), Planner: a.Planner, Model: a.Model,
			Actions: nilIfNone(a.Actions), Goals: nilIfNone(a.Goals), MCPs: nilIfNone(a.Mcps), Persistent: persistFromPB(a.Persistent), Roles: nilIfNone(a.Roles)}
		for _, t := range a.Triggers {
			ma.Triggers = append(ma.Triggers, methodology.Trigger{Name: t.Name, Description: t.Description, Type: t.Type, Event: t.Event, Filter: t.Filter,
				Schedule: t.Schedule, Goal: t.Goal, Intent: t.Intent, Target: t.Target, Roles: nilIfNone(t.Roles), Enabled: t.Enabled, Supersede: t.Supersede})
		}
		m.Agents = append(m.Agents, ma)
	}
	for _, r := range p.Roles {
		m.Roles = append(m.Roles, methodology.Role{Name: r.Name, Description: r.Description, Operations: nilIfNone(r.Operations)})
	}
	for _, me := range p.Methods {
		m.Methods = append(m.Methods, methodology.Method{Name: me.Name, For: me.For, When: me.When, Priority: int(me.Priority), Description: me.Description,
			Guidance: me.Guidance, Checklist: nilIfNone(me.Checklist), Deliverables: nilIfNone(me.Deliverables), References: refsFromPB(me.References), Roles: respFromPB(me.Roles),
			Steps: stepsFromPB(me.Steps), Actions: nilIfNone(me.Actions), Done: me.Done, Planner: me.Planner, Model: me.Model, MCPs: nilIfNone(me.Mcps), On: subsFromPB(me.On), Persistent: persistFromPB(me.Persistent)})
	}
	for _, p := range p.Processes {
		m.Processes = append(m.Processes, methodology.Process{Name: p.Name, Description: p.Description, Examples: nilIfNone(p.Examples), Steps: stepsFromPB(p.Steps), References: refsFromPB(p.References), On: subsFromPB(p.On)})
	}
	return m
}

func nilIfNone(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}

func nilIfEmpty[M ~map[K]V, K comparable, V any](m M) M {
	if len(m) == 0 {
		return nil
	}
	return m
}

// LevelsToPB converts the level-by-level coherence of a process or method.
func LevelsToPB(ls []methodology.LevelCheck) []*registryv1.LevelCheck {
	out := make([]*registryv1.LevelCheck, 0, len(ls))
	for _, l := range ls {
		pb := &registryv1.LevelCheck{Path: l.Path, Kind: l.Kind, Inputs: l.Inputs, Outputs: l.Outputs, Ok: l.OK(), Agent: l.Agent, Goal: l.Goal}
		for _, s := range l.Order {
			pb.Order = append(pb.Order, &registryv1.LevelStep{Name: s.Name, Layer: int32(s.Layer)})
		}
		for _, n := range l.Steps {
			pb.Steps = append(pb.Steps, &registryv1.LevelNode{Name: n.Name, Path: n.Path, Method: n.Method, Target: n.Target, Capability: n.Capability,
				Entry: n.Entry, Exit: n.Exit, Composite: n.Composite, Broken: n.Broken, SubSteps: int32(n.SubSteps), Foreach: n.Foreach, GroupBy: n.GroupBy})
		}
		for _, e := range l.Edges {
			pb.Edges = append(pb.Edges, &registryv1.GraphEdge{From: e.From, To: e.To, Conditions: e.Conditions})
		}
		for _, g := range l.Gaps {
			pb.Gaps = append(pb.Gaps, &registryv1.LevelGap{Step: g.Step, Kind: g.Kind, Missing: g.Missing, Message: g.Message})
		}
		out = append(out, pb)
	}
	return out
}

// ProcessGraphToPB converts the graph of a process (ADR 0036 §4).
func ProcessGraphToPB(g methodology.ProcessGraph) *registryv1.ProcessGraph {
	out := &registryv1.ProcessGraph{Process: g.Process, Description: g.Description, References: refsToPB(g.References), MethodGoals: map[string]string{}}
	for _, s := range g.Steps {
		out.Steps = append(out.Steps, &registryv1.GraphStep{Path: s.Path, Name: s.Name, Description: s.Description, Parent: s.Parent, Depth: int32(s.Depth),
			Leaf: s.Leaf, Method: s.Method, Target: s.Target, Entry: s.Entry, Exit: s.Exit, Roles: respToPB(s.Roles), Guidance: s.Guidance,
			References: refsToPB(s.References), Process: s.Process, Capability: s.Capability, Foreach: s.Foreach, GroupBy: s.GroupBy})
	}
	for _, e := range g.Edges {
		out.Edges = append(out.Edges, &registryv1.GraphEdge{From: e.From, To: e.To, Conditions: e.Conditions})
	}
	for _, me := range g.Methods {
		out.Methods = append(out.Methods, &registryv1.Method{Name: me.Name, For: me.For, When: me.When, Priority: int32(me.Priority), Description: me.Description,
			Guidance: me.Guidance, Checklist: me.Checklist, Deliverables: me.Deliverables, References: refsToPB(me.References),
			Roles: respToPB(me.Roles), Steps: stepsToPB(me.Steps), Actions: me.Actions, Done: me.Done, Planner: me.Planner, Model: me.Model, Mcps: me.MCPs, On: subsToPB(me.On), Persistent: persistToPB(me.Persistent)})
		out.MethodGoals[me.Name] = me.AgentGoal
	}
	for _, a := range g.Agents {
		ga := &registryv1.GraphAgent{Name: a.Name, Description: a.Description, Planner: a.Planner, Goals: a.Goals}
		for _, x := range a.Actions {
			ga.Actions = append(ga.Actions, &registryv1.GraphAction{Name: x.Name, Kind: x.Kind, Description: x.Description, Pre: x.Pre, Effects: x.Effects})
		}
		out.Agents = append(out.Agents, ga)
	}
	return out
}

// PlanPreviewToPB converts the result of planning a goal from a (possibly overridden) world state.
func PlanPreviewToPB(p *engine.PlanPreview) *registryv1.PlanPreview {
	out := &registryv1.PlanPreview{Agent: p.Agent, Goal: p.Goal, Planner: p.Planner, Reached: p.Reached, Cost: p.Cost, Awaiting: p.Awaiting}
	for _, a := range p.Actions {
		out.Actions = append(out.Actions, &registryv1.PlanStep{Name: a.Name, Step: a.Step, Kind: a.Kind, Cost: a.Cost})
	}
	return out
}

// persistToPB and persistFromPB convert the persistence of an agent or a method (ADR 0112).
func persistToPB(p *methodology.Persistence) *registryv1.Persistence {
	if p == nil {
		return nil
	}
	return &registryv1.Persistence{Scope: p.Scope, Until: p.Until, Memory: int32(p.Memory)}
}

func persistFromPB(p *registryv1.Persistence) *methodology.Persistence {
	if p == nil {
		return nil
	}
	return &methodology.Persistence{Scope: p.Scope, Until: nilIfEmpty(p.Until), Memory: int(p.Memory)}
}

func loopToPB(l *methodology.Loop) *registryv1.ActionLoop {
	if l == nil {
		return nil
	}
	return &registryv1.ActionLoop{MaxRounds: int32(l.MaxRounds), MaxToolResultBytes: int32(l.MaxToolResultBytes),
		MaxTokens: int64(l.MaxTokens), Timeout: l.Timeout, CompactBytes: int32(l.CompactBytes)}
}

func loopFromPB(l *registryv1.ActionLoop) *methodology.Loop {
	if l == nil {
		return nil
	}
	return &methodology.Loop{MaxRounds: int(l.MaxRounds), MaxToolResultBytes: int(l.MaxToolResultBytes),
		MaxTokens: int(l.MaxTokens), Timeout: l.Timeout, CompactBytes: int(l.CompactBytes)}
}

// MethodologyViewToPB converts the compiled view of a methodology (ADR 0126 step 3).
func MethodologyViewToPB(v *methodology.View) *registryv1.MethodologyView {
	out := &registryv1.MethodologyView{Methodology: v.Methodology, Version: v.Version, Goal: v.Goal,
		Processes: activityViewsToPB(v.Processes), Methods: activityViewsToPB(v.Methods), Conditions: map[string]*registryv1.ViewCondition{}}
	for name, c := range v.Conditions {
		out.Conditions[name] = &registryv1.ViewCondition{Description: c.Description, ProducedBy: c.ProducedBy, Requires: c.Requires}
	}
	return out
}

func activityViewsToPB(as []methodology.ActivityView) []*registryv1.ViewActivity {
	out := make([]*registryv1.ViewActivity, 0, len(as))
	for _, a := range as {
		out = append(out, &registryv1.ViewActivity{Name: a.Name, Kind: a.Kind, Description: a.Description, Agent: a.Agent, Goal: a.Goal,
			Done: a.Done, Nested: a.Nested, Reactive: a.Reactive, For: a.For, Roles: respToPB(a.Roles), Steps: stepViewsToPB(a.Steps)})
	}
	return out
}

func stepViewsToPB(steps []methodology.StepView) []*registryv1.ViewStep {
	var out []*registryv1.ViewStep
	for _, s := range steps {
		out = append(out, &registryv1.ViewStep{Path: s.Path, Name: s.Name, Owner: s.Owner, Method: s.Method, Target: s.Target,
			Capability: s.Capability, Foreach: s.Foreach, Description: s.Description, Guidance: s.Guidance, Checklist: s.Checklist,
			References: refsToPB(s.References), Roles: respToPB(s.Roles), Entry: s.Entry, Exit: s.Exit, Planned: s.Planned,
			Unavailable: s.Unavailable, Steps: stepViewsToPB(s.Steps)})
	}
	return out
}
