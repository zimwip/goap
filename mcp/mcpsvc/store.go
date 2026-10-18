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

	connectorv1 "github.com/zimwip/goap/contracts/gen/goap/connector/v1"
	"github.com/zimwip/goap/lib/outbox"
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
	// LostAt is when the hub reported the registration lost (its lease expired, ADR 0136); zero while it is live or
	// not reported since it last registered.
	LostAt time.Time
}

// SaveEvents decides the events of a registration from the row it replaces (found false: there is none), read in the
// transaction that writes it; the store writes them in the outbox with the row.
type SaveEvents func(prev ConnectorReg, found bool) ([]outbox.Message, error)

// Store persists the registry of connectors.
type Store interface {
	// SaveConnector records a registration, with the events decides (nil: none) in the same transaction.
	SaveConnector(ctx context.Context, r ConnectorReg, events SaveEvents) error
	// MarkLost records that the registration of id, last seen at lastSeen, was reported lost at at, with msgs in
	// the same transaction; false (nothing written) when it registered again since or was reported already.
	MarkLost(ctx context.Context, id string, lastSeen, at time.Time, msgs ...outbox.Message) (bool, error)
	Connector(ctx context.Context, id string) (ConnectorReg, error)
	Connectors(ctx context.Context) ([]ConnectorReg, error)
}

// MemoryStore is an in-memory Store.
type MemoryStore struct {
	mu         sync.Mutex
	connectors map[string]ConnectorReg
	out        outbox.Memory
}

var (
	_ Store        = (*MemoryStore)(nil)
	_ outbox.Store = (*MemoryStore)(nil)
)

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore { return &MemoryStore{connectors: map[string]ConnectorReg{}} }

func (s *MemoryStore) SaveConnector(_ context.Context, r ConnectorReg, events SaveEvents) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var msgs []outbox.Message
	if events != nil {
		prev, found := s.connectors[r.Info.Id]
		var err error
		if msgs, err = events(prev, found); err != nil {
			return err
		}
	}
	s.connectors[r.Info.Id] = r
	s.out.Append(msgs...)
	return nil
}

func (s *MemoryStore) MarkLost(_ context.Context, id string, lastSeen, at time.Time, msgs ...outbox.Message) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.connectors[id]
	if !ok || !r.LastSeen.Equal(lastSeen) || !r.LostAt.IsZero() {
		return false, nil
	}
	r.LostAt = at
	s.connectors[id] = r
	s.out.Append(msgs...)
	return true, nil
}

// Pending implements outbox.Store.
func (s *MemoryStore) Pending(ctx context.Context, limit int) ([]outbox.Message, error) {
	return s.out.Pending(ctx, limit)
}

// MarkPublished implements outbox.Store.
func (s *MemoryStore) MarkPublished(ctx context.Context, seqs []int64, at time.Time) error {
	return s.out.MarkPublished(ctx, seqs, at)
}

// PurgePublished implements outbox.Store.
func (s *MemoryStore) PurgePublished(ctx context.Context, before time.Time) error {
	return s.out.PurgePublished(ctx, before)
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
