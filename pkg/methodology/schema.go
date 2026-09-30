package methodology

import (
	"fmt"
	"strings"

	"github.com/zimwip/goap/pkg/algo"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/guard"
)

// Schema is the model of a domain (ADR 0013): its node types, link types, lifecycles and algorithms.
type Schema struct {
	NodeTypes []NodeType `yaml:"nodeTypes" json:"nodeTypes"`
	LinkTypes []LinkType `yaml:"linkTypes" json:"linkTypes"`
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
		if n.Editor != "" && !nameRE.MatchString(n.Editor) {
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
	return nodeTypes, linkTypes
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
