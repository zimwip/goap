package lnk

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// Addresses (ADR 0171 §4): an address is resolved in two levels. Its scheme names a type of source: lnk is the
// built-in second level, reading <source>/<type>/<key> through the source of that name (Registry.OpenIn); any other
// scheme is the address of a type whose resolver reads it with no declared source (the web: http, https). A resolver
// of a kind declares the schemes it reads by implementing SchemeResolver; a source never declares one, and no source is
// of the kind lnk.

// SchemeResolver is a resolver that also reads the addresses of its own schemes, with no declared source.
type SchemeResolver interface {
	// Schemes are the schemes it reads (lower case).
	Schemes() []string
	// ResolveDirect reads an address of one of its schemes; src is the source of its kind no one declared (its name and
	// kind are the kind's).
	ResolveDirect(ctx context.Context, src Source, u *url.URL) (Target, error)
}

// Opener is a resolver that says how a reader opens what it resolved (ADR 0171 §5): it reads what the reader is given
// (a text, an image) and sets Target.Open. Resolving reads nothing more than the target itself; opening may read its
// content, so only the paths that open a target for a reader ask for it.
type Opener interface {
	Open(ctx context.Context, src Source, t Target) (Target, error)
}

// OpenIn resolves a URL in a scope (ResolveIn), then opens it: through the resolver of its source when it is an
// Opener, else a node is opened in its editor and a web address externally.
func (r *Registry) OpenIn(ctx context.Context, m Mapping, u URL) (Target, error) {
	src, res, err := r.sourceIn(ctx, m, u)
	if err != nil {
		return Target{}, err
	}
	t, err := r.resolveWith(ctx, src, res, u)
	if err != nil {
		return Target{}, err
	}
	if o, ok := res.(Opener); ok {
		if t, err = o.Open(ctx, src, t); err != nil {
			return Target{}, err
		}
	}
	return opened(t), nil
}

// ResolveAddress reads and opens an address in a scope: a logical URL through its source (OpenIn), else an address of
// the scheme of a type of source (ErrNoResolver when no resolver reads that scheme).
func (r *Registry) ResolveAddress(ctx context.Context, m Mapping, address string) (Target, error) {
	address = strings.TrimSpace(address)
	scheme, _, ok := strings.Cut(address, ":")
	if !ok || scheme == "" {
		return Target{}, fmt.Errorf("%q: an address names its scheme: %w", address, ErrInvalid)
	}
	scheme = strings.ToLower(scheme)
	if scheme == Scheme {
		u, err := Parse(address)
		if err != nil {
			return Target{}, err
		}
		return r.OpenIn(ctx, m, u)
	}
	r.mu.RLock()
	kind, ok := r.schemes[scheme]
	res := r.resolvers[kind]
	r.mu.RUnlock()
	sr, isScheme := res.(SchemeResolver)
	if !ok || !isScheme {
		return Target{}, fmt.Errorf("scheme %q: %w", scheme, ErrNoResolver)
	}
	u, err := url.Parse(address)
	if err != nil {
		return Target{}, fmt.Errorf("%q: %w", address, ErrInvalid)
	}
	src := Source{Name: kind, Kind: kind}
	t, err := sr.ResolveDirect(ctx, src, u)
	if err != nil {
		return Target{}, err
	}
	if o, ok := res.(Opener); ok && t.Open == nil {
		if t, err = o.Open(ctx, src, t); err != nil {
			return Target{}, err
		}
	}
	return opened(t), nil
}

// Schemes lists the schemes the registry reads: lnk, then those of the resolvers registered, with their kind.
func (r *Registry) Schemes() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := map[string]string{Scheme: KindLnk}
	for s, k := range r.schemes {
		out[s] = k
	}
	return out
}

// opened gives a target no resolver opened its default way: a node in its editor, a web location externally.
func opened(t Target) Target {
	if t.Open != nil {
		return t
	}
	switch {
	case t.Node != nil:
		t.Open = &Open{Mode: OpenNode}
	case strings.HasPrefix(t.Location, "https://") || strings.HasPrefix(t.Location, "http://"):
		t.Open = &Open{Mode: OpenExternal, Href: t.Location}
	case t.Location != "":
		t.Open = &Open{Mode: OpenPath, Href: t.Location}
	}
	return t
}
