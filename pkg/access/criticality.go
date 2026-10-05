package access

import (
	"context"
	"fmt"
	"time"

	"github.com/zimwip/goap/pkg/criticality"
	"github.com/zimwip/goap/pkg/domain"
)

// The policy of a criticality level is organisation data (ADR 0075 §3): a CriticalityPolicy node owned by a unit says
// what the changes held by that unit and the units below it require at one level. The nearest unit holding a policy for
// a level wins (design rule 3), the whole policy at once; a level no unit of the chain holds takes the compiled-in table
// (criticality.Defaults). This file is the one place that reads those nodes: nothing else names the type.

type (
	criticalityLevel  = criticality.Level
	criticalityPolicy = criticality.Policy
)

// readCriticality reads the CriticalityPolicy nodes, each attached to its owner unit; a node that cannot be read, has no
// owner unit or repeats a level of its unit is a problem of the snapshot (the first one stays).
func (s *Snapshot) readCriticality(nodes []domain.Node, byID map[domain.NodeID]domain.Node) {
	s.criticality = map[string]map[criticalityLevel]criticalityPolicy{}
	for _, n := range nodes {
		owner, ok := byID[n.Owner]
		if !ok || !s.structures.In(domain.StructureOrganisation, owner.Type) {
			s.Problems = append(s.Problems, fmt.Sprintf("%s: its owner %s is not a unit of the organisation", n.Key, n.Owner))
			continue
		}
		lvl, p, err := CriticalityPolicyFromProps(n.Properties)
		if err != nil {
			s.Problems = append(s.Problems, fmt.Sprintf("%s: %v", n.Key, err))
			continue
		}
		if s.criticality[owner.Key] == nil {
			s.criticality[owner.Key] = map[criticalityLevel]criticalityPolicy{}
		}
		if _, dup := s.criticality[owner.Key][lvl]; dup {
			s.Problems = append(s.Problems, fmt.Sprintf("%s: unit %s already has a policy for %s", n.Key, owner.Key, lvl))
			continue
		}
		s.criticality[owner.Key][lvl] = p
	}
}

// CriticalityPolicyFromProps reads the level and the policy of a CriticalityPolicy node.
func CriticalityPolicyFromProps(props map[string]any) (criticality.Level, criticality.Policy, error) {
	lvl := criticality.Level(str(props, "level"))
	if !criticality.Valid(lvl) {
		return lvl, criticality.Policy{}, fmt.Errorf("criticality policy: unknown level %q", lvl)
	}
	oracles, err := strList(props, "oracles")
	if err != nil {
		return lvl, criticality.Policy{}, fmt.Errorf("criticality policy: %w", err)
	}
	p := criticality.Policy{Oracles: oracles, SignatoryRole: str(props, "signatoryRole")}
	p.Sampling, _ = props["sampling"].(bool)
	var hours float64
	switch v := props["maxDerogationHours"].(type) {
	case nil:
	case float64:
		hours = v
	case int:
		hours = float64(v)
	case int64:
		hours = float64(v)
	default:
		return lvl, p, fmt.Errorf("criticality policy: maxDerogationHours must be a number")
	}
	if hours < 0 {
		return lvl, p, fmt.Errorf("criticality policy: maxDerogationHours must not be negative")
	}
	p.MaxDerogation = time.Duration(hours * float64(time.Hour))
	return lvl, p, nil
}

// CriticalityProps returns the properties of a CriticalityPolicy node.
func CriticalityProps(l criticality.Level, p criticality.Policy) map[string]any {
	m := map[string]any{"level": string(l), "sampling": p.Sampling, "maxDerogationHours": float64(p.MaxDerogation) / float64(time.Hour)}
	if len(p.Oracles) > 0 {
		m["oracles"] = toAnyList(p.Oracles)
	}
	if p.SignatoryRole != "" {
		m["signatoryRole"] = p.SignatoryRole
	}
	return m
}

// CriticalityPolicy is what the level requires of the changes held by the unit: the policy of the nearest unit of its
// chain (the unit, its ancestors, the root) that holds one for the level, else the compiled-in table.
func (s *Snapshot) CriticalityPolicy(unit string, l criticality.Level) criticality.Policy {
	if s != nil {
		for _, u := range s.Chain(unit) {
			if p, ok := s.criticality[u][l]; ok {
				return p
			}
		}
	}
	return criticality.Defaults()[l]
}

// CriticalityOf is the policy of the criticality level l for a change: resolved from the unit that holds it.
func (s *Snapshot) CriticalityOf(c domain.Change, l criticality.Level) criticality.Policy {
	return s.CriticalityPolicy(c.OwnerOrg, l)
}

// CriticalityResolver is the criticality.Resolver of the directory: it reads the current snapshot (the compiled-in table
// when the graph cannot be read).
func (d *Directory) CriticalityResolver() criticality.Resolver {
	return func(ctx context.Context, c domain.Change, l criticality.Level) criticality.Policy {
		s, _ := d.Snapshot(ctx)
		return s.CriticalityOf(c, l)
	}
}

// CriticalityFacet is the provider of the facet of the blackboard that gives conditions and guards the policy of the
// level of the change (domain.FacetCriticalityPolicy, Graph.Facets): it reads the snapshot last built and never the
// graph, as a facet does no I/O. Before any snapshot, or when none was built, the compiled-in table applies.
func (d *Directory) CriticalityFacet() func(c domain.Change, now time.Time) any {
	return func(c domain.Change, _ time.Time) any {
		return d.peek().CriticalityOf(c, criticality.Of(c.Data))
	}
}

// peek is the snapshot last built, nil when none.
func (d *Directory) peek() *Snapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cache == nil {
		return nil
	}
	s, _ := d.cache.Peek()
	return s
}

// CriticalityPolicyKey is the key of the CriticalityPolicy node of a unit for a level.
func CriticalityPolicyKey(unit string, l criticality.Level) string {
	return "CRIT:" + unit + "/" + string(l)
}
