package lnk

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Registry holds the sources a process knows and the resolvers of their kinds. The self resolver is always there: a
// source no one declared is a namespace of this graph, of the same name, read by it (ADR 0154 §3).
type Registry struct {
	mu        sync.RWMutex
	sources   map[string]Source
	resolvers map[string]Resolver
	// Lookup, when set, finds a source not declared in process (the platform@Source nodes of the graph): found false
	// lets the registry fall back on the self source.
	Lookup func(ctx context.Context, name string) (Source, bool, error)
}

// NewRegistry returns a registry with the self resolver.
func NewRegistry(self Resolver) *Registry {
	r := &Registry{sources: map[string]Source{}, resolvers: map[string]Resolver{}}
	r.Register(KindSelf, self)
	return r
}

// Register gives a kind of source its resolver (a nil resolver removes it; the self resolver cannot be removed).
func (r *Registry) Register(kind string, res Resolver) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if res == nil {
		if kind != KindSelf {
			delete(r.resolvers, kind)
		}
		return
	}
	r.resolvers[kind] = res
}

// Declare adds or replaces a source.
func (r *Registry) Declare(src Source) error {
	if err := src.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sources[src.Name] = src
	return nil
}

// Source returns the source of a name: a declared one, else one Lookup finds, else the self source of that name.
func (r *Registry) Source(ctx context.Context, name string) (Source, error) {
	if !sourceRE.MatchString(name) {
		return Source{}, fmt.Errorf("source %q: %w", name, ErrInvalid)
	}
	r.mu.RLock()
	src, ok := r.sources[name]
	lookup := r.Lookup
	r.mu.RUnlock()
	if ok {
		return src, nil
	}
	if lookup != nil {
		src, ok, err := lookup(ctx, name)
		if err != nil {
			return Source{}, err
		}
		if ok {
			return src, nil
		}
	}
	return Source{Name: name, Kind: KindSelf}, nil
}

// Sources lists the sources declared in process.
func (r *Registry) Sources() []Source {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Source, 0, len(r.sources))
	for _, s := range r.sources {
		out = append(out, s)
	}
	return out
}

func (r *Registry) resolver(kind string) (Resolver, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	res, ok := r.resolvers[kind]
	if !ok {
		return nil, fmt.Errorf("kind %q: %w", kind, ErrNoResolver)
	}
	return res, nil
}

// Resolve reads what a URL points to, through the resolver of its source.
func (r *Registry) Resolve(ctx context.Context, u URL) (Target, error) {
	if err := u.Validate(); err != nil {
		return Target{}, err
	}
	src, err := r.Source(ctx, u.Source)
	if err != nil {
		return Target{}, err
	}
	res, err := r.resolver(src.Kind)
	if err != nil {
		return Target{}, fmt.Errorf("%s: %w", u, err)
	}
	t, err := res.Resolve(ctx, src, u)
	if err != nil {
		return Target{}, err
	}
	if t.URL.IsZero() {
		t.URL = u
	}
	return t, nil
}

// ResolveString parses a URL and resolves it.
func (r *Registry) ResolveString(ctx context.Context, s string) (Target, error) {
	u, err := Parse(s)
	if err != nil {
		return Target{}, err
	}
	return r.Resolve(ctx, u)
}

// URLOf gives the URL of a thing of a source.
func (r *Registry) URLOf(ctx context.Context, source string, t Target) (URL, error) {
	src, err := r.Source(ctx, source)
	if err != nil {
		return URL{}, err
	}
	res, err := r.resolver(src.Kind)
	if err != nil {
		return URL{}, err
	}
	u, err := res.URLOf(ctx, src, t)
	if err != nil {
		return URL{}, err
	}
	if u.Source != src.Name {
		return URL{}, fmt.Errorf("resolver of %s gave %s: %w", src.Name, u, ErrInvalid)
	}
	return u, u.Validate()
}

// IsNotFound reports a target the source does not have, or a source nobody knows.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, ErrUnknownSource)
}
