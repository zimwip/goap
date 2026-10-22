package engine

import (
	"context"
	"sort"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/graphapi"
	"github.com/zimwip/goap/engine/methodology"
)

// GraphPort is what the engine needs from the graph: the graph's contract (graphapi.Port), which the built-in MCPs
// call too. *graph.Graph implements it in-process, graphclient.Client over RPC.
type GraphPort = graphapi.Port

// viewOf is the view a process reads the graph in (ADR 0157): its change on its flow (the active option when it runs on
// the main flow), else the main branch of its namespace at the baseline it started from.
func viewOf(p *Process) graphapi.View {
	if p.ChangeID != "" {
		return graphapi.View{Change: p.ChangeID, Flow: p.Flow}
	}
	return graphapi.View{Namespace: p.Namespace, Branch: domain.MainBranch, Baseline: p.BaselineID}
}

// MethodologyPort resolves methodologies (the registry): the latest
// published version of one, or of every methodology. Methodology takes a name
// (the latest published version) or a reference at a version
// (domain.MethodologyRef, ADR 0113: what a change or a process follows).
type MethodologyPort interface {
	Methodology(ctx context.Context, name string) (*methodology.Compiled, error)
	List(ctx context.Context) ([]*methodology.Compiled, error)
}

// Publisher publishes process events (NATS in services).
type Publisher interface {
	Publish(ctx context.Context, subject string, v any) error
}

// StaticMethodologies serves compiled methodologies from memory, one version
// of each, by name.
type StaticMethodologies map[string]*methodology.Compiled

// Methodology implements MethodologyPort: a reference naming another version
// than the one held is unknown.
func (s StaticMethodologies) Methodology(_ context.Context, ref string) (*methodology.Compiled, error) {
	name, version := domain.SplitMethodologyRef(ref)
	m, ok := s[name]
	if !ok || version != "" && m.Version() != version {
		return nil, ErrUnknownMethodology{Name: ref}
	}
	return m, nil
}

// List implements MethodologyPort.
func (s StaticMethodologies) List(context.Context) ([]*methodology.Compiled, error) {
	out := make([]*methodology.Compiled, 0, len(s))
	for _, m := range s {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}

// ErrUnknownMethodology is returned for unknown methodologies.
type ErrUnknownMethodology struct{ Name string }

func (e ErrUnknownMethodology) Error() string { return "unknown methodology " + e.Name }
