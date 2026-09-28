package platform

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// Lease is a distributed lock over a NATS JetStream KV bucket, used to elect
// a single leader among several replicas of the same service (e.g. which
// engine replica may fire schedule/cron triggers).
type Lease struct {
	kv jetstream.KeyValue
}

// NewLease ensures the given JetStream KV bucket exists and returns a Lease
// over it, reusing this Events' NATS/JetStream connection.
func (e *Events) NewLease(ctx context.Context, bucket string) (*Lease, error) {
	if e == nil {
		return nil, nil
	}
	kv, err := e.js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bucket})
	if err != nil {
		return nil, err
	}
	return &Lease{kv: kv}, nil
}

type leaseValue struct {
	Holder    string    `json:"holder"`
	RenewedAt time.Time `json:"renewedAt"`
}

// Acquire takes or renews the lease identified by key for holder. It
// returns true if holder now holds the lease (fresh acquisition, or
// renewal of a lease it already held), false if another holder's lease is
// still within ttl. The caller is expected to call Acquire again well
// before ttl elapses (e.g. every ttl/3) to keep renewing it.
func (l *Lease) Acquire(ctx context.Context, key, holder string, ttl time.Duration) (bool, error) {
	if l == nil {
		return false, nil
	}
	entry, err := l.kv.Get(ctx, key)
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		data, err := json.Marshal(leaseValue{Holder: holder, RenewedAt: time.Now().UTC()})
		if err != nil {
			return false, err
		}
		if _, err := l.kv.Create(ctx, key, data); err != nil {
			if errors.Is(err, jetstream.ErrKeyExists) {
				return false, nil // someone else just took it
			}
			return false, err
		}
		return true, nil
	}
	if err != nil {
		return false, err
	}
	var cur leaseValue
	if err := json.Unmarshal(entry.Value(), &cur); err != nil {
		return false, err
	}
	if cur.Holder != holder && time.Since(cur.RenewedAt) < ttl {
		return false, nil // still held by someone else
	}
	data, err := json.Marshal(leaseValue{Holder: holder, RenewedAt: time.Now().UTC()})
	if err != nil {
		return false, err
	}
	if _, err := l.kv.Update(ctx, key, data, entry.Revision()); err != nil {
		return false, nil // lost the race to another replica's renewal/steal
	}
	return true, nil
}
