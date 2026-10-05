package registrysvc

import (
	registryv1 "github.com/zimwip/goap/gen/goap/registry/v1"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/pkg/condition"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/methodology"
)

// ToPB converts a stored record.
func ToPB(r Record) *registryv1.Methodology {
	m := r.Methodology
	out := &registryv1.Methodology{Name: m.Name, Version: m.Version, Description: m.Description, Namespace: m.Namespace, AppliesTo: m.AppliesTo, Imports: m.Imports, On: subsToPB(m.On), Lifecycle: m.Lifecycle, Status: string(r.Status),
		CreatedAt: pbconv.Time(r.CreatedAt), UpdatedAt: pbconv.Time(r.UpdatedAt), PublishedAt: pbconv.Time(r.PublishedAt), UpdatedBy: r.UpdatedBy}
	for _, c := range m.Conditions {
		out.Conditions = append(out.Conditions, &registryv1.Condition{Name: c.Name, Description: c.Description, Expr: c.Expr})
	}
	for _, a := range m.Actions {
		pa := &registryv1.Action{Name: a.Name, Description: a.Description, Kind: a.Kind, Pre: a.Pre, Effects: a.Effects, Cost: a.Cost,
			Permission: a.Permission, Model: a.Model, Prompt: a.Prompt, Tool: a.Tool, Builtin: a.Builtin, Instructions: a.Instructions,
			Params: pbconv.Struct(a.Params), Language: a.Language, Code: a.Code, Utility: a.Utility,
			Specializes: a.Specializes, When: a.When, Priority: int32(a.Priority), Incremental: a.Incremental, Mcps: a.MCPs}
		if e := a.Expects; e != nil {
			pe := &registryv1.Expectation{ForEach: e.ForEach, Where: e.Where, Produce: &registryv1.ProduceSpec{Op: e.Produce.Op, NodeType: e.Produce.NodeType}}
			if e.Link != nil {
				pe.Link = &registryv1.LinkSpec{Type: e.Link.Type, Direction: e.Link.Direction}
			}
			pa.Expects = pe
		}
		out.Actions = append(out.Actions, pa)
	}
	for _, g := range m.Goals {
		out.Goals = append(out.Goals, &registryv1.Goal{Name: g.Name, Description: g.Description, Examples: g.Examples, Pre: g.Pre, Value: g.Value})
	}
	for _, a := range m.Agents {
		pa := &registryv1.Agent{Name: a.Name, Description: a.Description, Examples: a.Examples, Planner: a.Planner, Actions: a.Actions, Goals: a.Goals, Mcps: a.MCPs, Model: a.Model}
		for _, t := range a.Triggers {
			pa.Triggers = append(pa.Triggers, &registryv1.Trigger{Name: t.Name, Description: t.Description, Type: t.Type, Event: t.Event, Filter: t.Filter,
				Schedule: t.Schedule, Goal: t.Goal, Intent: t.Intent, Target: t.Target, Roles: t.Roles, Enabled: t.Enabled})
		}
		out.Agents = append(out.Agents, pa)
	}
	for _, r := range m.Roles {
		out.Roles = append(out.Roles, &registryv1.Role{Name: r.Name, Description: r.Description})
	}
	for _, me := range m.Methods {
		out.Methods = append(out.Methods, &registryv1.Method{Name: me.Name, For: me.For, When: me.When, Priority: int32(me.Priority), Description: me.Description,
			Guidance: me.Guidance, Checklist: me.Checklist, Deliverables: me.Deliverables, References: refsToPB(me.References), Roles: respToPB(me.Roles),
			Steps: stepsToPB(me.Steps), Actions: me.Actions, Done: me.Done, Planner: me.Planner, Model: me.Model, Mcps: me.MCPs})
	}
	for _, p := range m.Processes {
		out.Processes = append(out.Processes, &registryv1.Process{Name: p.Name, Description: p.Description, Examples: p.Examples, Steps: stepsToPB(p.Steps), References: refsToPB(p.References)})
	}
	return out
}

func stepsToPB(steps []methodology.Step) []*registryv1.Step {
	out := make([]*registryv1.Step, 0, len(steps))
	for _, s := range steps {
		out = append(out, &registryv1.Step{Name: s.Name, Description: s.Description, Instructions: s.Instructions, Pre: s.Pre, Done: s.Done,
			References: refsToPB(s.References), Guidance: s.Guidance, Checklist: s.Checklist, Deliverables: s.Deliverables, Method: s.Capability, Foreach: s.Foreach, GroupBy: s.GroupBy, Roles: respToPB(s.Roles), Steps: stepsToPB(s.Steps), Action: s.Action, Actions: s.Actions, Process: s.Process})
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
			References: refsFromPB(s.References), Guidance: s.Guidance, Checklist: nilIfNone(s.Checklist), Deliverables: nilIfNone(s.Deliverables), Capability: s.Method, Foreach: s.Foreach, GroupBy: s.GroupBy, Roles: respFromPB(s.Roles), Steps: stepsFromPB(s.Steps), Action: s.Action, Actions: nilIfNone(s.Actions), Process: s.Process})
	}
	return out
}

func subsToPB(subs []methodology.Subscription) []*registryv1.Subscription {
	var out []*registryv1.Subscription
	for _, s := range subs {
		out = append(out, &registryv1.Subscription{Event: s.Event, Filter: s.Filter})
	}
	return out
}

func subsFromPB(subs []*registryv1.Subscription) []methodology.Subscription {
	var out []methodology.Subscription
	for _, s := range subs {
		out = append(out, methodology.Subscription{Event: s.Event, Filter: s.Filter})
	}
	return out
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
	m := methodology.Methodology{Name: p.Name, Version: p.Version, Description: p.Description, Namespace: p.Namespace, AppliesTo: nilIfNone(p.AppliesTo), Imports: nilIfNone(p.Imports), On: subsFromPB(p.On), Lifecycle: p.Lifecycle}
	for _, c := range p.Conditions {
		m.Conditions = append(m.Conditions, methodology.Condition{Name: c.Name, Description: c.Description, Expr: c.Expr})
	}
	for _, a := range p.Actions {
		ma := methodology.Action{Name: a.Name, Description: a.Description, Kind: a.Kind, Pre: nilIfEmpty(a.Pre), Effects: nilIfEmpty(a.Effects),
			Cost: a.Cost, Permission: a.Permission, Model: a.Model, Prompt: a.Prompt, Tool: a.Tool, Builtin: a.Builtin,
			Instructions: a.Instructions, Params: pbconv.Map(a.Params), Language: a.Language, Code: a.Code, Utility: a.Utility,
			Specializes: a.Specializes, When: a.When, Priority: int(a.Priority), Incremental: a.Incremental, MCPs: a.Mcps}
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
		m.Actions = append(m.Actions, ma)
	}
	for _, g := range p.Goals {
		m.Goals = append(m.Goals, methodology.Goal{Name: g.Name, Description: g.Description, Examples: nilIfNone(g.Examples), Pre: nilIfEmpty(g.Pre), Value: g.Value})
	}
	for _, a := range p.Agents {
		ma := methodology.Agent{Name: a.Name, Description: a.Description, Examples: nilIfNone(a.Examples), Planner: a.Planner, Model: a.Model,
			Actions: nilIfNone(a.Actions), Goals: nilIfNone(a.Goals), MCPs: nilIfNone(a.Mcps)}
		for _, t := range a.Triggers {
			ma.Triggers = append(ma.Triggers, methodology.Trigger{Name: t.Name, Description: t.Description, Type: t.Type, Event: t.Event, Filter: t.Filter,
				Schedule: t.Schedule, Goal: t.Goal, Intent: t.Intent, Target: t.Target, Roles: nilIfNone(t.Roles), Enabled: t.Enabled})
		}
		m.Agents = append(m.Agents, ma)
	}
	for _, r := range p.Roles {
		m.Roles = append(m.Roles, methodology.Role{Name: r.Name, Description: r.Description})
	}
	for _, me := range p.Methods {
		m.Methods = append(m.Methods, methodology.Method{Name: me.Name, For: me.For, When: me.When, Priority: int(me.Priority), Description: me.Description,
			Guidance: me.Guidance, Checklist: nilIfNone(me.Checklist), Deliverables: nilIfNone(me.Deliverables), References: refsFromPB(me.References), Roles: respFromPB(me.Roles),
			Steps: stepsFromPB(me.Steps), Actions: nilIfNone(me.Actions), Done: me.Done, Planner: me.Planner, Model: me.Model, MCPs: nilIfNone(me.Mcps)})
	}
	for _, p := range p.Processes {
		m.Processes = append(m.Processes, methodology.Process{Name: p.Name, Description: p.Description, Examples: nilIfNone(p.Examples), Steps: stepsFromPB(p.Steps), References: refsFromPB(p.References)})
	}
	return m
}

func nilIfNone(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}

// IssuesToPB converts validation issues.
func IssuesToPB(is def.Issues) []*registryv1.Issue {
	out := make([]*registryv1.Issue, len(is))
	for i, x := range is {
		out[i] = &registryv1.Issue{Path: x.Path, Message: x.Message, Activity: x.Activity}
	}
	return out
}

func nodeTypesToPB(ns []def.NodeType) []*registryv1.NodeType {
	var out []*registryv1.NodeType
	for _, n := range ns {
		out = append(out, nodeTypeToPB(n))
	}
	return out
}

func linkTypesToPB(ls []def.LinkType) []*registryv1.LinkType {
	var out []*registryv1.LinkType
	for _, l := range ls {
		out = append(out, &registryv1.LinkType{Name: l.Name, Description: l.Description, From: l.From, To: l.To, Compose: l.Compose, Attributes: attributesToPB(l.Attributes)})
	}
	return out
}

// DomainToPB converts a stored domain record.
func DomainToPB(r DomainRecord) *registryv1.Domain {
	d := r.Domain
	return &registryv1.Domain{Name: d.Name, Version: d.Version, Description: d.Description, Status: string(r.Status),
		NodeTypes: nodeTypesToPB(d.NodeTypes), LinkTypes: linkTypesToPB(d.LinkTypes), Lifecycles: lifecyclesToPB(d.Lifecycles),
		Algorithms: algorithmsToPB(d.Algorithms), AlgorithmInstances: instancesToPB(d.Instances),
		CreatedAt: pbconv.Time(r.CreatedAt), UpdatedAt: pbconv.Time(r.UpdatedAt), PublishedAt: pbconv.Time(r.PublishedAt), UpdatedBy: r.UpdatedBy, Builtin: r.Builtin}
}

// DomainSummaryToPB converts a record to a list entry.
func DomainSummaryToPB(r DomainRecord) *registryv1.DomainSummary {
	d := r.Domain
	return &registryv1.DomainSummary{Name: d.Name, Version: d.Version, Description: d.Description, Status: string(r.Status),
		NodeTypeCount: int32(len(d.NodeTypes)), LinkTypeCount: int32(len(d.LinkTypes)),
		UpdatedAt: pbconv.Time(r.UpdatedAt), PublishedAt: pbconv.Time(r.PublishedAt), Builtin: r.Builtin}
}

// DomainFromPB converts an edited domain (status and timestamps are ignored).
func DomainFromPB(p *registryv1.Domain) def.Domain {
	if p == nil {
		return def.Domain{}
	}
	d := def.Domain{Name: p.Name, Version: p.Version, Description: p.Description}
	for _, n := range p.NodeTypes {
		d.NodeTypes = append(d.NodeTypes, nodeTypeFromPB(n))
	}
	for _, l := range p.LinkTypes {
		d.LinkTypes = append(d.LinkTypes, def.LinkType{Name: l.Name, Description: l.Description, From: l.From, To: l.To, Compose: l.Compose, Attributes: attributesFromPB(l.Attributes)})
	}
	d.Enums = enumsFromPB(p.Enums)
	d.Lifecycles = lifecyclesFromPB(p.Lifecycles)
	d.Algorithms, d.Instances = algorithmsFromPB(p.Algorithms), instancesFromPB(p.AlgorithmInstances)
	return d
}

func nodeTypeToPB(n def.NodeType) *registryv1.NodeType {
	out := &registryv1.NodeType{Name: n.Name, Description: n.Description, Attributes: attributesToPB(n.Attributes), Extends: n.Extends,
		Lifecycle: n.Lifecycle, ChangeControlled: n.ChangeControlled, Validators: n.Validators, Search: searchToPB(n.Search), Editor: n.Editor, AdminOnly: n.AdminOnly}
	if d := n.Document; d != nil {
		out.Document = &registryv1.DocumentSpec{Contains: d.Contains}
	}
	if t := n.Structure; t != nil {
		out.Structure = &registryv1.StructureTag{Kind: t.Kind, Parent: t.Parent, Root: t.Root, SelfParent: t.SelfParent}
	}
	for _, r := range n.Requires {
		out.Requires = append(out.Requires, &registryv1.RequiredLink{Link: r.Link, Count: int32(r.Count)})
	}
	return out
}

func nodeTypeFromPB(n *registryv1.NodeType) def.NodeType {
	out := def.NodeType{Name: n.Name, Description: n.Description, Attributes: attributesFromPB(n.Attributes), Extends: n.Extends,
		Lifecycle: n.Lifecycle, ChangeControlled: n.ChangeControlled, Validators: nilIfNone(n.Validators), Search: searchFromPB(n.Search), Editor: n.Editor, AdminOnly: n.AdminOnly}
	if d := n.Document; d != nil {
		out.Document = &domain.DocumentSpec{Contains: nilIfNone(d.Contains)}
	}
	if t := n.Structure; t != nil {
		out.Structure = &def.StructureTag{Kind: t.Kind, Parent: t.Parent, Root: t.Root, SelfParent: t.SelfParent}
	}
	for _, r := range n.Requires {
		out.Requires = append(out.Requires, domain.RequiredLink{Link: r.Link, Count: int(r.Count)})
	}
	return out
}

func lifecyclesToPB(ls []domain.Lifecycle) []*registryv1.Lifecycle {
	var out []*registryv1.Lifecycle
	for _, l := range ls {
		pl := &registryv1.Lifecycle{Name: l.Name, Description: l.Description, Initial: l.Initial, RestInEditable: l.RestInEditable}
		for _, s := range l.States {
			pl.States = append(pl.States, &registryv1.LifecycleState{Name: s.Name, Description: s.Description, Editable: s.Editable, Final: s.Final})
		}
		for _, t := range l.Transitions {
			pt := &registryv1.LifecycleTransition{Name: t.Name, Description: t.Description, From: t.From, To: t.To, Permission: t.Permission, Guard: t.Guard,
				RequiresAttributes: t.Requires.Attributes, RequiresOutgoingLinks: t.Requires.OutgoingLinks, Guards: t.Guards, Actions: t.Actions}
			if t.Children != nil {
				pt.ChildrenStates = t.Children.States
			}
			pl.Transitions = append(pl.Transitions, pt)
		}
		out = append(out, pl)
	}
	return out
}

func lifecyclesFromPB(ls []*registryv1.Lifecycle) []domain.Lifecycle {
	var out []domain.Lifecycle
	for _, l := range ls {
		dl := domain.Lifecycle{Name: l.Name, Description: l.Description, Initial: l.Initial, RestInEditable: l.RestInEditable}
		for _, s := range l.States {
			dl.States = append(dl.States, domain.LifecycleState{Name: s.Name, Description: s.Description, Editable: s.Editable, Final: s.Final})
		}
		for _, t := range l.Transitions {
			dt := domain.Transition{Name: t.Name, Description: t.Description, From: t.From, To: t.To, Permission: t.Permission, Guard: t.Guard,
				Requires: domain.TransitionRequires{Attributes: nilIfNone(t.RequiresAttributes), OutgoingLinks: nilIfNone(t.RequiresOutgoingLinks)},
				Guards:   nilIfNone(t.Guards), Actions: nilIfNone(t.Actions)}
			if len(t.ChildrenStates) > 0 {
				dt.Children = &domain.ChildrenRule{States: t.ChildrenStates}
			}
			dl.Transitions = append(dl.Transitions, dt)
		}
		out = append(out, dl)
	}
	return out
}

func nilIfEmpty[M ~map[K]V, K comparable, V any](m M) M {
	if len(m) == 0 {
		return nil
	}
	return m
}

func searchToPB(in []def.SearchProperty) []*registryv1.SearchProperty {
	var out []*registryv1.SearchProperty
	for _, s := range in {
		out = append(out, &registryv1.SearchProperty{Property: s.Property, Text: s.Text, Facet: s.Facet})
	}
	return out
}

func searchFromPB(in []*registryv1.SearchProperty) []def.SearchProperty {
	var out []def.SearchProperty
	for _, s := range in {
		out = append(out, def.SearchProperty{Property: s.Property, Text: s.Text, Facet: s.Facet})
	}
	return out
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
			Roles: respToPB(me.Roles), Steps: stepsToPB(me.Steps), Actions: me.Actions, Done: me.Done, Planner: me.Planner, Model: me.Model, Mcps: me.MCPs})
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
