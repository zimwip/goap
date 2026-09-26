// Package mcpsvc is the MCP hub: the live registry of connectors and the resolution and
// invocation of tools for the organisational unit holding a change. MCPs and adapters
// are nodes of the graph, read here (ADR 0019).
package mcpsvc

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	connectorv1 "github.com/zimwip/goap/gen/goap/connector/v1"
)

var (
	// ErrNotFound is returned for an unknown connector, MCP or tool.
	ErrNotFound = errors.New("not found")
)

// ConnectorReg is the registration of a connector: what it announced and where it is.
type ConnectorReg struct {
	Info     *connectorv1.ConnectorInfo
	Endpoint string
	LastSeen time.Time
}

// Store persists the registry of connectors.
type Store interface {
	SaveConnector(ctx context.Context, r ConnectorReg) error
	Connector(ctx context.Context, id string) (ConnectorReg, error)
	Connectors(ctx context.Context) ([]ConnectorReg, error)
}

// MemoryStore is an in-memory Store.
type MemoryStore struct {
	mu         sync.Mutex
	connectors map[string]ConnectorReg
}

var _ Store = (*MemoryStore)(nil)

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore { return &MemoryStore{connectors: map[string]ConnectorReg{}} }

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
