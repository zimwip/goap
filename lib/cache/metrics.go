package cache

import (
	"context"
	"sync"
	"weak"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// What the caches of a process hold and how often they find what is asked, by name: a cache given a name (Named) is
// measured while it lives, the caches of one name added up (several graphs in a process). Prometheus: goap_cache_*
// (deploy/grafana/dashboards/goap-perf.json).

// stats reads a cache while it lives (false once collected).
type stats func() (Stats, bool)

var named struct {
	sync.Mutex
	caches map[string][]stats
	once   sync.Once
}

// Named measures the cache under name (goap_cache_* with cache=name) while it lives, and returns it.
func (c *Cache[K, V]) Named(name string) *Cache[K, V] {
	w := weak.Make(c)
	register(name, func() (Stats, bool) {
		if c := w.Value(); c != nil {
			return c.Stats(), true
		}
		return Stats{}, false
	})
	return c
}

func register(name string, s stats) {
	named.once.Do(observe)
	named.Lock()
	defer named.Unlock()
	if named.caches == nil {
		named.caches = map[string][]stats{}
	}
	named.caches[name] = append(named.caches[name], s)
}

// totals adds up the live caches of each name, dropping those collected.
func totals() map[string]Stats {
	named.Lock()
	defer named.Unlock()
	out := map[string]Stats{}
	for name, ps := range named.caches {
		live := ps[:0]
		var t Stats
		for _, p := range ps {
			st, ok := p()
			if !ok {
				continue
			}
			live = append(live, p)
			t.Hits += st.Hits
			t.Misses += st.Misses
			t.Evicted += st.Evicted
			t.Expired += st.Expired
			t.TooLarge += st.TooLarge
			t.Entries += st.Entries
			t.Bytes += st.Bytes
			t.Limit += st.Limit
		}
		named.caches[name] = live
		out[name] = t
	}
	return out
}

func observe() {
	m := otel.Meter("github.com/zimwip/goap/lib/cache")
	size, _ := m.Int64ObservableGauge("cache.size", metric.WithUnit("By"), metric.WithDescription("Estimated memory the entries of a cache hold"))
	limit, _ := m.Int64ObservableGauge("cache.limit", metric.WithUnit("By"), metric.WithDescription("Memory a cache may hold"))
	entries, _ := m.Int64ObservableGauge("cache.entries", metric.WithUnit("{entry}"), metric.WithDescription("Entries a cache holds"))
	reads, _ := m.Int64ObservableCounter("cache.reads", metric.WithUnit("{read}"), metric.WithDescription("Reads of a cache, by result (hit, miss)"))
	drops, _ := m.Int64ObservableCounter("cache.drops", metric.WithUnit("{entry}"), metric.WithDescription("Entries a cache dropped or never kept, by reason (evicted: for room, expired: unread for the TTL, too_large)"))
	_, _ = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		for name, t := range totals() {
			a := attribute.String("cache", name)
			o.ObserveInt64(size, t.Bytes, metric.WithAttributes(a))
			o.ObserveInt64(limit, t.Limit, metric.WithAttributes(a))
			o.ObserveInt64(entries, int64(t.Entries), metric.WithAttributes(a))
			o.ObserveInt64(reads, t.Hits, metric.WithAttributes(a, attribute.String("result", "hit")))
			o.ObserveInt64(reads, t.Misses, metric.WithAttributes(a, attribute.String("result", "miss")))
			o.ObserveInt64(drops, t.Evicted, metric.WithAttributes(a, attribute.String("reason", "evicted")))
			o.ObserveInt64(drops, t.Expired, metric.WithAttributes(a, attribute.String("reason", "expired")))
			o.ObserveInt64(drops, t.TooLarge, metric.WithAttributes(a, attribute.String("reason", "too_large")))
		}
		return nil
	}, size, limit, entries, reads, drops)
}
