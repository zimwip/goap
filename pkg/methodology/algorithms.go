package methodology

import (
	"fmt"

	"github.com/zimwip/goap/pkg/algo"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/dsl"
)

// propertiesOf lists the properties of a node type, its own and the inherited ones.
func (s Schema) propertiesOf(name string) map[string]bool {
	byName := map[string]NodeType{}
	for _, n := range s.NodeTypes {
		byName[n.Name] = n
	}
	out := map[string]bool{}
	for seen := map[string]bool{}; name != "" && !seen[name]; name = byName[name].Extends {
		seen[name] = true
		for _, p := range byName[name].Properties {
			out[p] = true
		}
	}
	return out
}

// checkAlgorithms validates the algorithms, their instances and where the
// instances are plugged (ADR 0018).
func (s Schema) checkAlgorithms(prefix string, add func(path, format string, args ...any)) {
	algs := map[string]algo.Algorithm{}
	for i, a := range s.Algorithms {
		path := fmt.Sprintf(prefix+"algorithms[%d]", i)
		for _, msg := range a.Issues() {
			add(path, "%s", msg)
		}
		if a.Name != "" {
			if _, dup := algs[a.Name]; dup {
				add(path+".name", "duplicate algorithm %s", a.Name)
			}
			algs[a.Name] = a
		}
		if a.Code != "" && (a.Language == algo.JavaScript || a.Language == algo.Go) {
			if err := dsl.CheckAlgorithmCode(a.Language, a.Code); err != nil {
				add(path+".code", "%v", err)
			}
		}
	}
	insts := map[string]algo.Instance{}
	for i, in := range s.Instances {
		path := fmt.Sprintf(prefix+"algorithmInstances[%d]", i)
		switch {
		case in.Name == "":
			add(path+".name", "name required")
		case !algo.ValidName(in.Name):
			add(path+".name", "name must be lowercase letters, digits, '-' or '_' and start with a letter")
		default:
			if _, dup := insts[in.Name]; dup {
				add(path+".name", "duplicate algorithm instance %s", in.Name)
			}
			insts[in.Name] = in
		}
		a, ok := algs[in.Algorithm]
		if !ok {
			add(path+".algorithm", "unknown algorithm %q", in.Algorithm)
			continue
		}
		if _, issues := a.Resolve(in.Values); len(issues) > 0 {
			for _, msg := range issues {
				add(path+".values", "%s", msg)
			}
		}
	}
	plug := func(path, instance string, want algo.Usage) {
		in, ok := insts[instance]
		if !ok {
			add(path, "unknown algorithm instance %s", instance)
			return
		}
		if a, ok := algs[in.Algorithm]; ok && a.Type != want {
			add(path, "instance %s is of type %s, expected %s", instance, a.Type, want)
		}
	}
	props := map[string]map[string]bool{}
	for i, n := range s.NodeTypes {
		for j, v := range n.Validators {
			path := fmt.Sprintf(prefix+"nodeTypes[%d].validators[%d]", i, j)
			if props[n.Name] == nil {
				props[n.Name] = s.propertiesOf(n.Name)
			}
			if !props[n.Name][v.Property] {
				add(path+".property", "unknown property %q of %s", v.Property, n.Name)
			}
			plug(path+".instance", v.Instance, algo.UsagePropertyValidator)
		}
	}
	for i, l := range s.Lifecycles {
		for j, t := range l.Transitions {
			for k, g := range t.Guards {
				plug(fmt.Sprintf(prefix+"lifecycles[%d].transitions[%d].guards[%d]", i, j, k), g, algo.UsageTransitionGuard)
			}
			for k, a := range t.Actions {
				plug(fmt.Sprintf(prefix+"lifecycles[%d].transitions[%d].actions[%d]", i, j, k), a, algo.UsageTransitionAction)
			}
		}
	}
}

// BoundValidators returns the property validators of a node type resolved with
// their algorithm, in call order: the ones declared by the supertypes first.
// Instances that do not resolve are left out (Validate reports them).
func (s Schema) BoundValidators(name string) []algo.Bound {
	byName := map[string]NodeType{}
	for _, n := range s.NodeTypes {
		byName[n.Name] = n
	}
	var chain []NodeType
	for seen := map[string]bool{}; name != "" && !seen[name]; name = byName[name].Extends {
		seen[name] = true
		if n, ok := byName[name]; ok {
			chain = append([]NodeType{n}, chain...)
		}
	}
	set := s.algorithms()
	var out []algo.Bound
	for _, n := range chain {
		for _, v := range n.Validators {
			if b, err := set.Bind(v.Instance, algo.UsagePropertyValidator); err == nil {
				b.Property = v.Property
				out = append(out, b)
			}
		}
	}
	return out
}

// OwnBoundValidators is BoundValidators of the type itself, without the inherited ones.
func (s Schema) OwnBoundValidators(t NodeType) []algo.Bound {
	set := s.algorithms()
	var out []algo.Bound
	for _, v := range t.Validators {
		if b, err := set.Bind(v.Instance, algo.UsagePropertyValidator); err == nil {
			b.Property = v.Property
			out = append(out, b)
		}
	}
	return out
}

// BindLifecycle returns a copy of the lifecycle with the guard and action
// instances of its transitions resolved (GuardAlgos, ActionAlgos).
func (s Schema) BindLifecycle(l *domain.Lifecycle) *domain.Lifecycle {
	if l == nil {
		return nil
	}
	set := s.algorithms()
	c := l.Clone()
	for i, t := range c.Transitions {
		c.Transitions[i].GuardAlgos, c.Transitions[i].ActionAlgos = nil, nil
		for _, g := range t.Guards {
			if b, err := set.Bind(g, algo.UsageTransitionGuard); err == nil {
				c.Transitions[i].GuardAlgos = append(c.Transitions[i].GuardAlgos, b)
			}
		}
		for _, a := range t.Actions {
			if b, err := set.Bind(a, algo.UsageTransitionAction); err == nil {
				c.Transitions[i].ActionAlgos = append(c.Transitions[i].ActionAlgos, b)
			}
		}
	}
	return c
}
