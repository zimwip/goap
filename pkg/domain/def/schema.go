package def

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/zimwip/goap/pkg/algo"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/guard"
)

// Schema is the model of a domain (ADR 0013): its node types, link types, lifecycles and algorithms.
type Schema struct {
	NodeTypes []NodeType `yaml:"nodeTypes" json:"nodeTypes"`
	LinkTypes []LinkType `yaml:"linkTypes" json:"linkTypes"`
	// Enums are the closed lists of values that enum attributes refer to.
	Enums []Enum `yaml:"enums,omitempty" json:"enums,omitempty"`
	// Lifecycles are the state machines node types refer to by name.
	Lifecycles []domain.Lifecycle `yaml:"lifecycles,omitempty" json:"lifecycles,omitempty"`
	// Algorithms are the scripts of the domain (ADR 0018) and Instances their
	// parameterized uses; node types and lifecycle transitions plug instances.
	Algorithms []algo.Algorithm `yaml:"algorithms,omitempty" json:"algorithms,omitempty"`
	Instances  []algo.Instance  `yaml:"algorithmInstances,omitempty" json:"algorithmInstances,omitempty"`
}

// algorithms returns the algorithm set of the schema.
func (s Schema) algorithms() algo.Set {
	return algo.Set{Algorithms: s.Algorithms, Instances: s.Instances}
}

// HasAlgorithms tells whether the schema declares or plugs algorithms.
func (s Schema) HasAlgorithms() bool {
	if len(s.Algorithms) > 0 || len(s.Instances) > 0 {
		return true
	}
	for _, n := range s.NodeTypes {
		if len(n.Validators) > 0 {
			return true
		}
		for _, a := range n.Attributes {
			if len(a.Validators) > 0 {
				return true
			}
		}
	}
	for _, l := range s.Lifecycles {
		for _, t := range l.Transitions {
			if len(t.Guards) > 0 || len(t.Actions) > 0 {
				return true
			}
		}
	}
	return false
}

// check validates the schema and returns the sets of node and link type
// names. Issue paths start with prefix.
func (s Schema) check(prefix string, add func(path, format string, args ...any)) (nodeTypes, linkTypes map[string]bool) {
	nodeTypes = map[string]bool{}
	for i, n := range s.NodeTypes {
		path := fmt.Sprintf(prefix+"nodeTypes[%d]", i)
		if n.Name == "" {
			add(path+".name", "name required")
		} else if nodeTypes[n.Name] {
			add(path+".name", "duplicate node type %s", n.Name)
		}
		nodeTypes[n.Name] = true
		if n.Editor != "" && !NameRE.MatchString(n.Editor) {
			add(path+".editor", "invalid editor name %q (lowercase letters, digits, - and _)", n.Editor)
		}
	}
	parents := map[string]string{}
	for i, n := range s.NodeTypes {
		if n.Extends == "" || foreign(n.Extends) {
			continue
		}
		if !nodeTypes[n.Extends] {
			add(fmt.Sprintf(prefix+"nodeTypes[%d].extends", i), "unknown node type %s", n.Extends)
			continue
		}
		parents[n.Name] = n.Extends
	}
	for i, n := range s.NodeTypes {
		seen := map[string]bool{n.Name: true}
		for t := parents[n.Name]; t != ""; t = parents[t] {
			if seen[t] {
				add(fmt.Sprintf(prefix+"nodeTypes[%d].extends", i), "cyclic subtyping through %s", t)
				break
			}
			seen[t] = true
		}
	}
	s.checkLifecycles(prefix, nodeTypes, add)
	s.checkAlgorithms(prefix, add)
	s.checkAttributes(prefix, add)
	linkTypes = map[string]bool{}
	for i, l := range s.LinkTypes {
		path := fmt.Sprintf(prefix+"linkTypes[%d]", i)
		if l.Name == "" {
			add(path+".name", "name required")
		} else if linkTypes[l.Name] {
			add(path+".name", "duplicate link type %s", l.Name)
		}
		linkTypes[l.Name] = true
		if l.From != "" && !foreign(l.From) && !nodeTypes[l.From] {
			add(path+".from", "unknown node type %s", l.From)
		}
		if l.To != "" && !foreign(l.To) && !nodeTypes[l.To] {
			add(path+".to", "unknown node type %s", l.To)
		}
	}
	s.checkStructures(prefix, add)
	s.checkRequires(prefix, add)
	return nodeTypes, linkTypes
}

// checkStructures validates the structure tags (ADR 0054): a known kind, once per schema, a root key, and a parent
// link type of the schema itself going from and to the tagged type.
func (s Schema) checkStructures(prefix string, add func(path, format string, args ...any)) {
	kinds := map[string]bool{}
	for i, n := range s.NodeTypes {
		t := n.Structure
		if t == nil {
			continue
		}
		path := fmt.Sprintf(prefix+"nodeTypes[%d].structure", i)
		if !slices.Contains(domain.StructureKinds, t.Kind) {
			add(path+".kind", "unknown structure kind %q (one of %s)", t.Kind, strings.Join(domain.StructureKinds, ", "))
		} else if kinds[t.Kind] {
			add(path+".kind", "structure %s tagged twice", t.Kind)
		}
		kinds[t.Kind] = true
		if t.Root == "" {
			add(path+".root", "the key of the root node is required")
		}
		if n.Lifecycle != "" {
			add(path, "a structure type has no lifecycle: its root is created by the bootstrap")
		}
		var parent *LinkType
		for j := range s.LinkTypes {
			if s.LinkTypes[j].Name == t.Parent {
				parent = &s.LinkTypes[j]
			}
		}
		switch {
		case t.Parent == "":
			add(path+".parent", "the parent link type is required")
		case parent == nil:
			add(path+".parent", "unknown link type %s (a link type of this domain)", t.Parent)
		case parent.From != n.Name || parent.To != n.Name:
			add(path+".parent", "link type %s must go from %s to %s", t.Parent, n.Name, n.Name)
		}
	}
}

// checkRequires validates the required links of the node types (ADR 0065): a link type of the schema or of another
// domain, and a count that is not negative.
func (s Schema) checkRequires(prefix string, add func(path, format string, args ...any)) {
	for i, n := range s.NodeTypes {
		seen := map[string]bool{}
		for j, r := range n.Requires {
			path := fmt.Sprintf(prefix+"nodeTypes[%d].requires[%d]", i, j)
			switch {
			case r.Link == "":
				add(path+".link", "the link type is required")
			case seen[r.Link]:
				add(path+".link", "link type %s required twice", r.Link)
			case !foreign(r.Link) && !slices.ContainsFunc(s.LinkTypes, func(l LinkType) bool { return l.Name == r.Link }):
				add(path+".link", "unknown link type %s", r.Link)
			}
			seen[r.Link] = true
			if r.Count < 0 {
				add(path+".count", "the count must be positive")
			}
		}
	}
}

// foreign reports a qualified reference ("base@Item"): a type of another domain, which the type catalogue resolves
// (pkg/typecat); a bare name is a type of the domain itself.
func foreign(ref string) bool { return strings.Contains(ref, domain.TypeSep) }

// Lifecycle returns the named lifecycle of the schema.
func (s Schema) Lifecycle(name string) *domain.Lifecycle {
	for i := range s.Lifecycles {
		if s.Lifecycles[i].Name == name {
			return &s.Lifecycles[i]
		}
	}
	return nil
}

// LifecycleOf returns the lifecycle of a node type: the one it names, else the
// one of its nearest ancestor that names one. Nil when the nodes have no state.
func (s Schema) LifecycleOf(name string) *domain.Lifecycle {
	byName := map[string]NodeType{}
	for _, n := range s.NodeTypes {
		byName[n.Name] = n
	}
	for seen := map[string]bool{}; name != "" && !seen[name]; name = byName[name].Extends {
		seen[name] = true
		if ref := byName[name].Lifecycle; ref != "" {
			return s.Lifecycle(ref)
		}
	}
	return nil
}

// checkLifecycles validates the lifecycles, the references to them and the
// document declarations.
func (s Schema) checkLifecycles(prefix string, nodeTypes map[string]bool, add func(path, format string, args ...any)) {
	names := map[string]bool{}
	for i, l := range s.Lifecycles {
		path := fmt.Sprintf(prefix+"lifecycles[%d]", i)
		switch {
		case l.Name == "":
			add(path+".name", "name required")
		case !lifecycleNameRE.MatchString(l.Name):
			add(path+".name", "name must be lowercase letters, digits, '-' or '_' and start with a letter")
		case names[l.Name]:
			add(path+".name", "duplicate lifecycle %s", l.Name)
		}
		names[l.Name] = true
		for _, msg := range l.Issues() {
			add(path, "%s", msg)
		}
		for j, t := range l.Transitions {
			if _, err := guard.Compile(t.Guard); err != nil {
				add(fmt.Sprintf(path+".transitions[%d].guard", j), "%v", err)
			}
		}
	}
	for i, n := range s.NodeTypes {
		path := fmt.Sprintf(prefix+"nodeTypes[%d]", i)
		if n.Lifecycle != "" {
			if !names[n.Lifecycle] {
				add(path+".lifecycle", "unknown lifecycle %s", n.Lifecycle)
			}
			if !n.IsChangeControlled() {
				add(path+".changeControlled", "a type with a lifecycle must be change controlled")
			}
		}
		if d := n.Document; d != nil {
			if len(d.Contains) == 0 {
				add(path+".document.contains", "a document contains at least one node type")
			}
			for _, c := range d.Contains {
				if !foreign(c) && !nodeTypes[c] {
					add(path+".document.contains", "unknown node type %s", c)
				}
			}
		}
	}
	// child states of a document transition must exist on a contained type
	for i, n := range s.NodeTypes {
		l := s.LifecycleOf(n.Name)
		if l == nil {
			continue
		}
		for _, t := range l.Transitions {
			if t.Children == nil {
				continue
			}
			path := fmt.Sprintf(prefix+"nodeTypes[%d].lifecycle", i)
			doc := s.documentOf(n.Name)
			if doc == nil {
				add(path, "transition %s of lifecycle %s constrains children but %s is not a document", t.Name, l.Name, n.Name)
				continue
			}
			for _, st := range t.Children.States {
				found := false
				for _, c := range doc.Contains {
					if cl := s.LifecycleOf(c); cl != nil {
						if _, ok := cl.State(st); ok {
							found = true
						}
					}
				}
				if !found {
					add(path, "child state %q of transition %s exists on none of the contained types", st, t.Name)
				}
			}
		}
	}
}

// documentOf returns the document spec of a type (inherited through extends).
func (s Schema) documentOf(name string) *domain.DocumentSpec {
	byName := map[string]NodeType{}
	for _, n := range s.NodeTypes {
		byName[n.Name] = n
	}
	for seen := map[string]bool{}; name != "" && !seen[name]; name = byName[name].Extends {
		seen[name] = true
		if d := byName[name].Document; d != nil {
			return d
		}
	}
	return nil
}

// checkAttributes validates the enums of the domain and the attributes of its node and link types.
func (s Schema) checkAttributes(prefix string, add func(path, format string, args ...any)) {
	enums := map[string]bool{}
	for i, e := range s.Enums {
		path := fmt.Sprintf(prefix+"enums[%d]", i)
		switch {
		case e.Name == "":
			add(path+".name", "name required")
		case enums[e.Name]:
			add(path+".name", "duplicate enum %s", e.Name)
		}
		enums[e.Name] = true
		if len(e.Values) == 0 {
			add(path+".values", "an enum has at least one value")
		}
		seen := map[string]bool{}
		for j, v := range e.Values {
			switch {
			case v.Value == "":
				add(fmt.Sprintf(path+".values[%d].value", j), "value required")
			case seen[v.Value]:
				add(fmt.Sprintf(path+".values[%d].value", j), "duplicate value %s", v.Value)
			}
			seen[v.Value] = true
		}
	}
	check := func(path string, as []Attribute, node bool) {
		names := map[string]bool{}
		asName := 0
		for i, a := range as {
			p := fmt.Sprintf(path+".attributes[%d]", i)
			switch {
			case a.Name == "":
				add(p+".name", "name required")
			case !attributeNameRE.MatchString(a.Name):
				add(p+".name", "name must be letters, digits and '_' and start with a letter")
			case names[a.Name]:
				add(p+".name", "duplicate attribute %s", a.Name)
			}
			names[a.Name] = true
			if a.Type != "" && !slices.Contains(AttributeTypes, a.Type) {
				add(p+".type", "unknown type %q (%s)", a.Type, strings.Join(AttributeTypes, ", "))
			}
			if a.Widget != "" && !slices.Contains(AttributeWidgets, a.Widget) {
				add(p+".widget", "unknown widget %q (%s)", a.Widget, strings.Join(AttributeWidgets, ", "))
			}
			switch {
			case a.Type == AttrEnum && a.Enum == "":
				add(p+".enum", "an enum attribute names its enum")
			case a.Enum != "" && a.Type != AttrEnum:
				add(p+".enum", "only an enum attribute names an enum")
			case a.Enum != "" && !enums[a.Enum]:
				add(p+".enum", "unknown enum %s", a.Enum)
			}
			if a.AsName {
				asName++
			}
		}
		if node && asName > 1 {
			add(path+".attributes", "at most one attribute is the name of the node")
		}
	}
	for i, n := range s.NodeTypes {
		check(fmt.Sprintf(prefix+"nodeTypes[%d]", i), n.Attributes, true)
	}
	for i, l := range s.LinkTypes {
		check(fmt.Sprintf(prefix+"linkTypes[%d]", i), l.Attributes, false)
	}
}

var attributeNameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)
