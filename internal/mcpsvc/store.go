// Package mcpsvc is the MCP hub: the live registry of connectors, the generic
// MCP definitions, their adapters, the bindings of the organizations and the
// invocation of tools (ADR 0019).
package mcpsvc

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	connectorv1 "github.com/zimwip/goap/gen/goap/connector/v1"
	"github.com/zimwip/goap/pkg/mcp"
)

var (
	// ErrNotFound is returned for an unknown MCP, adapter, binding or connector.
	ErrNotFound = errors.New("not found")
	// ErrConflict is returned when a delete would leave dangling references.
	ErrConflict = errors.New("conflict")
)

// ConnectorReg is the registration of a connector: what it announced and where it is.
type ConnectorReg struct {
	Info     *connectorv1.ConnectorInfo
	Endpoint string
	LastSeen time.Time
}

// Store persists the hub state.
type Store interface {
	SaveMcp(ctx context.Context, d mcp.Def) error
	Mcp(ctx context.Context, name string) (mcp.Def, error)
	Mcps(ctx context.Context) ([]mcp.Def, error)
	// DeleteMcp fails with ErrConflict while adapters implement the MCP.
	DeleteMcp(ctx context.Context, name string) error

	SaveAdapter(ctx context.Context, a mcp.Adapter) error
	Adapter(ctx context.Context, mcpName, connector string) (mcp.Adapter, error)
	// Adapters lists the adapters of an MCP ("" = all).
	Adapters(ctx context.Context, mcpName string) ([]mcp.Adapter, error)
	// DeleteAdapter fails with ErrConflict while an organization binds it.
	DeleteAdapter(ctx context.Context, mcpName, connector string) error

	// SaveBinding requires the adapter of (mcp, connector) to exist.
	SaveBinding(ctx context.Context, b mcp.Binding) error
	Binding(ctx context.Context, org, mcpName string) (mcp.Binding, error)
	Bindings(ctx context.Context, org string) ([]mcp.Binding, error)
	DeleteBinding(ctx context.Context, org, mcpName string) error

	SaveConnector(ctx context.Context, r ConnectorReg) error
	Connector(ctx context.Context, id string) (ConnectorReg, error)
	Connectors(ctx context.Context) ([]ConnectorReg, error)
}

// MemoryStore is an in-memory Store.
type MemoryStore struct {
	mu         sync.Mutex
	mcps       map[string]mcp.Def
	adapters   map[[2]string]mcp.Adapter
	bindings   map[[2]string]mcp.Binding
	connectors map[string]ConnectorReg
}

var _ Store = (*MemoryStore)(nil)

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{mcps: map[string]mcp.Def{}, adapters: map[[2]string]mcp.Adapter{}, bindings: map[[2]string]mcp.Binding{}, connectors: map[string]ConnectorReg{}}
}

func (s *MemoryStore) SaveMcp(_ context.Context, d mcp.Def) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mcps[d.Name] = d
	return nil
}

func (s *MemoryStore) Mcp(_ context.Context, name string) (mcp.Def, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.mcps[name]
	if !ok {
		return d, errNotFound("mcp " + name)
	}
	return d, nil
}

func (s *MemoryStore) Mcps(context.Context) ([]mcp.Def, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]mcp.Def, 0, len(s.mcps))
	for _, d := range s.mcps {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *MemoryStore) DeleteMcp(_ context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.mcps[name]; !ok {
		return errNotFound("mcp " + name)
	}
	for k := range s.adapters {
		if k[0] == name {
			return errConflict("mcp " + name + " is implemented by adapters")
		}
	}
	delete(s.mcps, name)
	return nil
}

func (s *MemoryStore) SaveAdapter(_ context.Context, a mcp.Adapter) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.mcps[a.MCP]; !ok {
		return errNotFound("mcp " + a.MCP)
	}
	s.adapters[[2]string{a.MCP, a.Connector}] = a
	return nil
}

func (s *MemoryStore) Adapter(_ context.Context, m, c string) (mcp.Adapter, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.adapters[[2]string{m, c}]
	if !ok {
		return a, errNotFound("adapter " + m + "/" + c)
	}
	return a, nil
}

func (s *MemoryStore) Adapters(_ context.Context, m string) ([]mcp.Adapter, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []mcp.Adapter
	for k, a := range s.adapters {
		if m == "" || k[0] == m {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MCP+"/"+out[i].Connector < out[j].MCP+"/"+out[j].Connector })
	return out, nil
}

func (s *MemoryStore) DeleteAdapter(_ context.Context, m, c string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.adapters[[2]string{m, c}]; !ok {
		return errNotFound("adapter " + m + "/" + c)
	}
	for _, b := range s.bindings {
		if b.MCP == m && b.Connector == c {
			return errConflict("adapter " + m + "/" + c + " is bound by organization " + b.OrgID)
		}
	}
	delete(s.adapters, [2]string{m, c})
	return nil
}

func (s *MemoryStore) SaveBinding(_ context.Context, b mcp.Binding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.adapters[[2]string{b.MCP, b.Connector}]; !ok {
		return errNotFound("adapter " + b.MCP + "/" + b.Connector)
	}
	s.bindings[[2]string{b.OrgID, b.MCP}] = b
	return nil
}

func (s *MemoryStore) Binding(_ context.Context, org, m string) (mcp.Binding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.bindings[[2]string{org, m}]
	if !ok {
		return b, errNotFound("binding " + org + "/" + m)
	}
	return b, nil
}

func (s *MemoryStore) Bindings(_ context.Context, org string) ([]mcp.Binding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []mcp.Binding
	for k, b := range s.bindings {
		if org == "" || k[0] == org {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OrgID+"/"+out[i].MCP < out[j].OrgID+"/"+out[j].MCP })
	return out, nil
}

func (s *MemoryStore) DeleteBinding(_ context.Context, org, m string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.bindings[[2]string{org, m}]; !ok {
		return errNotFound("binding " + org + "/" + m)
	}
	delete(s.bindings, [2]string{org, m})
	return nil
}

func (s *MemoryStore) SaveConnector(_ context.Context, r ConnectorReg) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connectors[r.Info.Id] = r
	return nil
}

func (s *MemoryStore) Connector(_ context.Context, id string) (ConnectorReg, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.connectors[id]
	if !ok {
		return r, errNotFound("connector " + id)
	}
	return r, nil
}

func (s *MemoryStore) Connectors(context.Context) ([]ConnectorReg, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ConnectorReg, 0, len(s.connectors))
	for _, r := range s.connectors {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Info.Id < out[j].Info.Id })
	return out, nil
}
