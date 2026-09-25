package registrysvc

import (
	registryv1 "github.com/zimwip/goap/gen/goap/registry/v1"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/pkg/condition"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/methodology"
)

// ToPB converts a stored record.
func ToPB(r Record) *registryv1.Methodology {
	m := r.Methodology
	out := &registryv1.Methodology{Name: m.Name, Version: m.Version, Description: m.Description, DomainRef: m.DomainRef, Namespace: m.Namespace, Status: string(r.Status),
		CreatedAt: pbconv.Time(r.CreatedAt), UpdatedAt: pbconv.Time(r.UpdatedAt), PublishedAt: pbconv.Time(r.PublishedAt), UpdatedBy: r.UpdatedBy}
	for _, n := range m.Domain.NodeTypes {
		out.NodeTypes = append(out.NodeTypes, nodeTypeToPB(n))
	}
	for _, l := range m.Domain.LinkTypes {
		out.LinkTypes = append(out.LinkTypes, &registryv1.LinkType{Name: l.Name, From: l.From, To: l.To})
	}
	out.Lifecycles = lifecyclesToPB(m.Domain.Lifecycles)
	for _, c := range m.Conditions {
		out.Conditions = append(out.Conditions, &registryv1.Condition{Name: c.Name, Description: c.Description, Expr: c.Expr})
	}
	for _, a := range m.Actions {
		pa := &registryv1.Action{Name: a.Name, Description: a.Description, Kind: a.Kind, Pre: a.Pre, Effects: a.Effects, Cost: a.Cost,
			Permission: a.Permission, Model: a.Model, Prompt: a.Prompt, Tool: a.Tool, Builtin: a.Builtin, Instructions: a.Instructions,
			Params: pbconv.Struct(a.Params), Language: a.Language, Code: a.Code, Utility: a.Utility,
			Specializes: a.Specializes, When: a.When, Priority: int32(a.Priority), Incremental: a.Incremental}
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
		pa := &registryv1.Agent{Name: a.Name, Description: a.Description, Examples: a.Examples, Planner: a.Planner, Actions: a.Actions, Goals: a.Goals}
		for _, t := range a.Triggers {
			pa.Triggers = append(pa.Triggers, &registryv1.Trigger{Name: t.Name, Description: t.Description, Type: t.Type, Event: t.Event, Filter: t.Filter,
				Schedule: t.Schedule, Goal: t.Goal, Intent: t.Intent, Target: t.Target, Roles: t.Roles, Enabled: t.Enabled})
		}
		out.Agents = append(out.Agents, pa)
	}
	return out
}

// SummaryToPB converts a record to a list entry.
func SummaryToPB(r Record) *registryv1.MethodologySummary {
	out := &registryv1.MethodologySummary{Name: r.Methodology.Name, Version: r.Methodology.Version, Description: r.Methodology.Description,
		Status: string(r.Status), UpdatedAt: pbconv.Time(r.UpdatedAt), PublishedAt: pbconv.Time(r.PublishedAt)}
	for _, g := range r.Methodology.Goals {
		out.Goals = append(out.Goals, &registryv1.GoalSummary{Name: g.Name, Description: g.Description})
	}
	for _, a := range r.Methodology.Agents {
		out.Agents = append(out.Agents, &registryv1.AgentSummary{Name: a.Name, Description: a.Description, Planner: a.Planner})
	}
	return out
}

// FromPB converts an edited definition (status and timestamps are ignored).
func FromPB(p *registryv1.Methodology) methodology.Methodology {
	if p == nil {
		return methodology.Methodology{}
	}
	m := methodology.Methodology{Name: p.Name, Version: p.Version, Description: p.Description, DomainRef: p.DomainRef, Namespace: p.Namespace}
	for _, n := range p.NodeTypes {
		m.Domain.NodeTypes = append(m.Domain.NodeTypes, nodeTypeFromPB(n))
	}
	for _, l := range p.LinkTypes {
		m.Domain.LinkTypes = append(m.Domain.LinkTypes, methodology.LinkType{Name: l.Name, From: l.From, To: l.To})
	}
	m.Domain.Lifecycles = lifecyclesFromPB(p.Lifecycles)
	for _, c := range p.Conditions {
		m.Conditions = append(m.Conditions, methodology.Condition{Name: c.Name, Description: c.Description, Expr: c.Expr})
	}
	for _, a := range p.Actions {
		ma := methodology.Action{Name: a.Name, Description: a.Description, Kind: a.Kind, Pre: nilIfEmpty(a.Pre), Effects: nilIfEmpty(a.Effects),
			Cost: a.Cost, Permission: a.Permission, Model: a.Model, Prompt: a.Prompt, Tool: a.Tool, Builtin: a.Builtin,
			Instructions: a.Instructions, Params: pbconv.Map(a.Params), Language: a.Language, Code: a.Code, Utility: a.Utility,
			Specializes: a.Specializes, When: a.When, Priority: int(a.Priority), Incremental: a.Incremental}
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
		ma := methodology.Agent{Name: a.Name, Description: a.Description, Examples: nilIfNone(a.Examples), Planner: a.Planner,
			Actions: nilIfNone(a.Actions), Goals: nilIfNone(a.Goals)}
		for _, t := range a.Triggers {
			ma.Triggers = append(ma.Triggers, methodology.Trigger{Name: t.Name, Description: t.Description, Type: t.Type, Event: t.Event, Filter: t.Filter,
				Schedule: t.Schedule, Goal: t.Goal, Intent: t.Intent, Target: t.Target, Roles: nilIfNone(t.Roles), Enabled: t.Enabled})
		}
		m.Agents = append(m.Agents, ma)
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
func IssuesToPB(is methodology.Issues) []*registryv1.Issue {
	out := make([]*registryv1.Issue, len(is))
	for i, x := range is {
		out[i] = &registryv1.Issue{Path: x.Path, Message: x.Message}
	}
	return out
}

func nodeTypesToPB(ns []methodology.NodeType) []*registryv1.NodeType {
	var out []*registryv1.NodeType
	for _, n := range ns {
		out = append(out, nodeTypeToPB(n))
	}
	return out
}

func linkTypesToPB(ls []methodology.LinkType) []*registryv1.LinkType {
	var out []*registryv1.LinkType
	for _, l := range ls {
		out = append(out, &registryv1.LinkType{Name: l.Name, From: l.From, To: l.To})
	}
	return out
}

// DomainToPB converts a stored domain record.
func DomainToPB(r DomainRecord) *registryv1.Domain {
	d := r.Domain
	return &registryv1.Domain{Name: d.Name, Version: d.Version, Description: d.Description, Status: string(r.Status),
		NodeTypes: nodeTypesToPB(d.NodeTypes), LinkTypes: linkTypesToPB(d.LinkTypes), Lifecycles: lifecyclesToPB(d.Lifecycles),
		CreatedAt: pbconv.Time(r.CreatedAt), UpdatedAt: pbconv.Time(r.UpdatedAt), PublishedAt: pbconv.Time(r.PublishedAt), UpdatedBy: r.UpdatedBy}
}

// DomainSummaryToPB converts a record to a list entry.
func DomainSummaryToPB(r DomainRecord) *registryv1.DomainSummary {
	d := r.Domain
	return &registryv1.DomainSummary{Name: d.Name, Version: d.Version, Description: d.Description, Status: string(r.Status),
		NodeTypeCount: int32(len(d.NodeTypes)), LinkTypeCount: int32(len(d.LinkTypes)),
		UpdatedAt: pbconv.Time(r.UpdatedAt), PublishedAt: pbconv.Time(r.PublishedAt)}
}

// DomainFromPB converts an edited domain (status and timestamps are ignored).
func DomainFromPB(p *registryv1.Domain) methodology.Domain {
	if p == nil {
		return methodology.Domain{}
	}
	d := methodology.Domain{Name: p.Name, Version: p.Version, Description: p.Description}
	for _, n := range p.NodeTypes {
		d.NodeTypes = append(d.NodeTypes, nodeTypeFromPB(n))
	}
	for _, l := range p.LinkTypes {
		d.LinkTypes = append(d.LinkTypes, methodology.LinkType{Name: l.Name, From: l.From, To: l.To})
	}
	d.Lifecycles = lifecyclesFromPB(p.Lifecycles)
	return d
}

func nodeTypeToPB(n methodology.NodeType) *registryv1.NodeType {
	out := &registryv1.NodeType{Name: n.Name, Description: n.Description, Properties: n.Properties, Extends: n.Extends,
		Lifecycle: n.Lifecycle, ChangeControlled: n.ChangeControlled}
	if d := n.Document; d != nil {
		out.Document = &registryv1.DocumentSpec{Contains: d.Contains}
	}
	return out
}

func nodeTypeFromPB(n *registryv1.NodeType) methodology.NodeType {
	out := methodology.NodeType{Name: n.Name, Description: n.Description, Properties: nilIfNone(n.Properties), Extends: n.Extends,
		Lifecycle: n.Lifecycle, ChangeControlled: n.ChangeControlled}
	if d := n.Document; d != nil {
		out.Document = &domain.DocumentSpec{Contains: nilIfNone(d.Contains)}
	}
	return out
}

func lifecyclesToPB(ls []domain.Lifecycle) []*registryv1.Lifecycle {
	var out []*registryv1.Lifecycle
	for _, l := range ls {
		pl := &registryv1.Lifecycle{Name: l.Name, Initial: l.Initial}
		for _, s := range l.States {
			pl.States = append(pl.States, &registryv1.LifecycleState{Name: s.Name, Description: s.Description, Editable: s.Editable, Final: s.Final})
		}
		for _, t := range l.Transitions {
			pt := &registryv1.LifecycleTransition{Name: t.Name, From: t.From, To: t.To, Permission: t.Permission, Guard: t.Guard,
				RequiresAttributes: t.Requires.Attributes, RequiresOutgoingLinks: t.Requires.OutgoingLinks}
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
		dl := domain.Lifecycle{Name: l.Name, Initial: l.Initial}
		for _, s := range l.States {
			dl.States = append(dl.States, domain.LifecycleState{Name: s.Name, Description: s.Description, Editable: s.Editable, Final: s.Final})
		}
		for _, t := range l.Transitions {
			dt := domain.Transition{Name: t.Name, From: t.From, To: t.To, Permission: t.Permission, Guard: t.Guard,
				Requires: domain.TransitionRequires{Attributes: nilIfNone(t.RequiresAttributes), OutgoingLinks: nilIfNone(t.RequiresOutgoingLinks)}}
			if len(t.ChildrenStates) > 0 {
				dt.Children = &domain.ChildrenRule{States: t.ChildrenStates}
			}
			dl.Transitions = append(dl.Transitions, dt)
		}
		out = append(out, dl)
	}
	return out
}
