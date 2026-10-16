// Package domainpb converts the domains between their definition (pkg/domain/def) and the messages of the registry
// protocol (registry.v1): the contract both registries speak (ADR 0125 §3).
package domainpb

import (
	"encoding/json"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/zimwip/goap/contracts/algo"
	"github.com/zimwip/goap/contracts/domain"
	registryv1 "github.com/zimwip/goap/contracts/gen/goap/registry/v1"
	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/pbconv"
	"github.com/zimwip/goap/lib/typecat"
)

// IssuesToPB converts validation issues.
func IssuesToPB(is def.Issues) []*registryv1.Issue {
	out := make([]*registryv1.Issue, len(is))
	for i, x := range is {
		out[i] = &registryv1.Issue{Path: x.Path, Message: x.Message, Activity: x.Activity, Severity: x.Severity, Hint: x.Hint}
	}
	return out
}

// IssuesFromPB is the inverse of IssuesToPB.
func IssuesFromPB(is []*registryv1.Issue) def.Issues {
	var out def.Issues
	for _, x := range is {
		out = append(out, def.Issue{Path: x.Path, Message: x.Message, Activity: x.Activity, Severity: x.Severity, Hint: x.Hint})
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
		out = append(out, &registryv1.LinkType{Name: l.Name, Description: l.Description, From: l.From, To: l.To, Compose: l.Compose, Attributes: attributesToPB(l.Attributes), Validators: l.Validators,
			Label: l.Label, InverseLabel: l.InverseLabel})
	}
	return out
}

// DomainToPB converts a stored domain record.
func DomainToPB(r def.Record) *registryv1.Domain {
	d := r.Domain
	return &registryv1.Domain{Name: d.Name, Version: d.Version, Description: d.Description, Status: string(r.Status),
		NodeTypes: nodeTypesToPB(d.NodeTypes), LinkTypes: linkTypesToPB(d.LinkTypes), Enums: enumsToPB(d.Enums), Lifecycles: LifecyclesToPB(d.Lifecycles),
		Algorithms: algorithmsToPB(d.Algorithms), AlgorithmInstances: instancesToPB(d.Instances),
		CreatedAt: pbconv.Time(r.CreatedAt), UpdatedAt: pbconv.Time(r.UpdatedAt), PublishedAt: pbconv.Time(r.PublishedAt), UpdatedBy: r.UpdatedBy, Builtin: r.Builtin}
}

// DomainSummaryToPB converts a record to a list entry.
func DomainSummaryToPB(r def.Record) *registryv1.DomainSummary {
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
		d.LinkTypes = append(d.LinkTypes, def.LinkType{Name: l.Name, Description: l.Description, From: l.From, To: l.To, Compose: l.Compose, Attributes: attributesFromPB(l.Attributes), Validators: l.Validators,
			Label: l.Label, InverseLabel: l.InverseLabel})
	}
	d.Enums = enumsFromPB(p.Enums)
	d.Lifecycles = lifecyclesFromPB(p.Lifecycles)
	d.Algorithms, d.Instances = AlgorithmsFromPB(p.Algorithms), instancesFromPB(p.AlgorithmInstances)
	return d
}

func nodeTypeToPB(n def.NodeType) *registryv1.NodeType {
	out := &registryv1.NodeType{Name: n.Name, Description: n.Description, Attributes: attributesToPB(n.Attributes), Extends: n.Extends,
		Lifecycle: n.Lifecycle, ChangeControlled: n.ChangeControlled, Validators: n.Validators, Search: searchToPB(n.Search), Editor: n.Editor, AdminOnly: n.AdminOnly, AdditionalProperties: n.AdditionalProperties, Form: pbconv.FormToPB(n.Form)}
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
		Lifecycle: n.Lifecycle, ChangeControlled: n.ChangeControlled, Validators: nilIfNone(n.Validators), Search: searchFromPB(n.Search), Editor: n.Editor, AdminOnly: n.AdminOnly, AdditionalProperties: n.AdditionalProperties, Form: pbconv.FormFromPB(n.Form)}
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

func LifecyclesToPB(ls []domain.Lifecycle) []*registryv1.Lifecycle {
	var out []*registryv1.Lifecycle
	for _, l := range ls {
		pl := &registryv1.Lifecycle{Name: l.Name, Description: l.Description, Initial: l.Initial}
		for _, s := range l.States {
			pl.States = append(pl.States, &registryv1.LifecycleState{Name: s.Name, Description: s.Description, NotLandable: s.NotLandable, Final: s.Final})
		}
		for _, t := range l.Transitions {
			pt := &registryv1.LifecycleTransition{Name: t.Name, Description: t.Description, From: t.From, To: t.To, Permission: t.Permission, Guard: t.Guard,
				RequiresAttributes: t.Requires.Attributes, RequiresOutgoingLinks: t.Requires.OutgoingLinks, Guards: t.Guards, Actions: t.Actions}
			pt.Vetos, pt.Objectives = criteriaToPB(t.Vetos), criteriaToPB(t.Objectives)
			if t.Children != nil {
				pt.ChildrenStates = t.Children.States
			}
			pl.Transitions = append(pl.Transitions, pt)
		}
		out = append(out, pl)
	}
	return out
}

func criteriaToPB(cs []domain.Criterion) []*registryv1.Criterion {
	var out []*registryv1.Criterion
	for _, c := range cs {
		out = append(out, &registryv1.Criterion{Name: c.Name, Expr: c.Expr, Description: c.Description})
	}
	return out
}

func criteriaFromPB(cs []*registryv1.Criterion) []domain.Criterion {
	var out []domain.Criterion
	for _, c := range cs {
		out = append(out, domain.Criterion{Name: c.Name, Expr: c.Expr, Description: c.Description})
	}
	return out
}

func lifecyclesFromPB(ls []*registryv1.Lifecycle) []domain.Lifecycle {
	var out []domain.Lifecycle
	for _, l := range ls {
		dl := domain.Lifecycle{Name: l.Name, Description: l.Description, Initial: l.Initial}
		for _, s := range l.States {
			dl.States = append(dl.States, domain.LifecycleState{Name: s.Name, Description: s.Description, NotLandable: s.NotLandable, Final: s.Final})
		}
		for _, t := range l.Transitions {
			dt := domain.Transition{Name: t.Name, Description: t.Description, From: t.From, To: t.To, Permission: t.Permission, Guard: t.Guard,
				Requires: domain.TransitionRequires{Attributes: nilIfNone(t.RequiresAttributes), OutgoingLinks: nilIfNone(t.RequiresOutgoingLinks)},
				Guards:   nilIfNone(t.Guards), Actions: nilIfNone(t.Actions)}
			dt.Vetos, dt.Objectives = criteriaFromPB(t.Vetos), criteriaFromPB(t.Objectives)
			if len(t.ChildrenStates) > 0 {
				dt.Children = &domain.ChildrenRule{States: t.ChildrenStates}
			}
			dl.Transitions = append(dl.Transitions, dt)
		}
		out = append(out, dl)
	}
	return out
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

func algorithmsToPB(as []algo.Algorithm) []*registryv1.Algorithm {
	var out []*registryv1.Algorithm
	for _, a := range as {
		pa := &registryv1.Algorithm{Name: a.Name, Description: a.Description, Type: string(a.Type), Language: a.Language, Code: a.Code}
		for _, p := range a.Params {
			pp := &registryv1.AlgorithmParam{Name: p.Name, Type: p.Type, Description: p.Description, Required: p.Required, Values: p.Values}
			if p.Default != nil {
				pp.DefaultValue, _ = structpb.NewValue(normalizeJSON(p.Default))
			}
			pa.Params = append(pa.Params, pp)
		}
		out = append(out, pa)
	}
	return out
}

func AlgorithmsFromPB(as []*registryv1.Algorithm) []algo.Algorithm {
	var out []algo.Algorithm
	for _, a := range as {
		da := algo.Algorithm{Name: a.Name, Description: a.Description, Type: algo.Usage(a.Type), Language: a.Language, Code: a.Code}
		for _, p := range a.Params {
			dp := algo.Param{Name: p.Name, Type: p.Type, Description: p.Description, Required: p.Required, Values: nilIfNone(p.Values)}
			if p.DefaultValue != nil {
				dp.Default = p.DefaultValue.AsInterface()
			}
			da.Params = append(da.Params, dp)
		}
		out = append(out, da)
	}
	return out
}

func instancesToPB(is []algo.Instance) []*registryv1.AlgorithmInstance {
	var out []*registryv1.AlgorithmInstance
	for _, i := range is {
		out = append(out, &registryv1.AlgorithmInstance{Name: i.Name, Description: i.Description, Algorithm: i.Algorithm, Values: pbconv.Struct(i.Values)})
	}
	return out
}

func instancesFromPB(is []*registryv1.AlgorithmInstance) []algo.Instance {
	var out []algo.Instance
	for _, i := range is {
		out = append(out, algo.Instance{Name: i.Name, Description: i.Description, Algorithm: i.Algorithm, Values: pbconv.Map(i.Values)})
	}
	return out
}

func attributesToPB(as []def.Attribute) []*registryv1.Attribute {
	var out []*registryv1.Attribute
	for _, a := range as {
		out = append(out, &registryv1.Attribute{Name: a.Name, Label: a.Label, Description: a.Description, Type: a.Type, Widget: a.Widget,
			Enum: a.Enum, DefaultValue: a.Default, Section: a.Section, Order: int32(a.Order), Tooltip: a.Tooltip, AsName: a.AsName, Validators: a.Validators})
	}
	return out
}

func attributesFromPB(as []*registryv1.Attribute) []def.Attribute {
	var out []def.Attribute
	for _, a := range as {
		out = append(out, def.Attribute{Name: a.Name, Label: a.Label, Description: a.Description, Type: a.Type, Widget: a.Widget,
			Enum: a.Enum, Default: a.DefaultValue, Section: a.Section, Order: int(a.Order), Tooltip: a.Tooltip, AsName: a.AsName, Validators: nilIfNone(a.Validators)})
	}
	return out
}

func AttributeInfosToPB(as []typecat.Attribute) []*registryv1.AttributeInfo {
	var out []*registryv1.AttributeInfo
	for _, a := range as {
		out = append(out, &registryv1.AttributeInfo{Attribute: attributesToPB([]def.Attribute{a.Attribute})[0], From: a.From, Values: enumValuesToPB(a.Values)})
	}
	return out
}

func enumValuesToPB(vs []def.EnumValue) []*registryv1.EnumValue {
	var out []*registryv1.EnumValue
	for _, v := range vs {
		out = append(out, &registryv1.EnumValue{Value: v.Value, Label: v.Label})
	}
	return out
}

func enumsToPB(es []def.Enum) []*registryv1.Enum {
	var out []*registryv1.Enum
	for _, e := range es {
		out = append(out, &registryv1.Enum{Name: e.Name, Description: e.Description, Values: enumValuesToPB(e.Values)})
	}
	return out
}

func enumsFromPB(es []*registryv1.Enum) []def.Enum {
	var out []def.Enum
	for _, e := range es {
		me := def.Enum{Name: e.Name, Description: e.Description}
		for _, v := range e.Values {
			me.Values = append(me.Values, def.EnumValue{Value: v.Value, Label: v.Label})
		}
		out = append(out, me)
	}
	return out
}

func normalizeJSON(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

func nilIfNone(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}
