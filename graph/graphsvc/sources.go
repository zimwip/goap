package graphsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/graphapi"
	"github.com/zimwip/goap/contracts/lnk"
	"github.com/zimwip/goap/graph/graph"
)

// The sources of the platform (ADR 0154): the platform graph is a source of its own, the one every namespace no other
// graph holds belongs to; the other graphs (the content graph, §9) are declared sources of kind graph, the driver of the
// namespaces they hold. Both are seeded by the bootstrap, then edited like any setting.

// PlatformSource is the name of the source of the platform graph: this graph, no location.
const PlatformSource = "platform"

// PlatformSourceOf is the declaration of the platform graph's own source.
func PlatformSourceOf() lnk.Source {
	return lnk.Source{Name: PlatformSource, Kind: lnk.KindGraph,
		Description: "This graph: the platform, the organisation and the methodologies, and every namespace no other graph holds"}
}

// SourceProps are the properties of the platform@Source node declaring a source.
func SourceProps(s lnk.Source) map[string]any {
	p := map[string]any{"name": s.Name, "kind": s.Kind}
	for k, v := range map[string]string{"description": s.Description, "namespace": s.Namespace, "location": s.Location, "branch": s.Branch} {
		if v != "" {
			p[k] = v
		}
	}
	if len(s.Namespaces) > 0 {
		ns := make([]any, len(s.Namespaces))
		for i, n := range s.Namespaces {
			ns[i] = n
		}
		p["namespaces"] = ns
	}
	if len(s.Params) > 0 {
		p["params"] = s.Params
	}
	return p
}

// SeedSources declares the platform graph's source and the sources given (Options.Sources) that the graph does not
// declare yet: a declaration an administrator edited or retired stays so. It reports whether it wrote anything.
func SeedSources(ctx context.Context, g *graph.Graph, extra []lnk.Source) (bool, error) {
	var edits []graph.NodeEdit
	for _, s := range append([]lnk.Source{PlatformSourceOf()}, extra...) {
		if err := s.Validate(); err != nil {
			return false, err
		}
		key := domain.SourceKey(s.Name)
		if _, err := g.NodeByKey(ctx, domain.NamespacePlatform, key); err == nil {
			continue
		} else if !errors.Is(err, graph.ErrNotFound) {
			return false, err
		}
		edits = append(edits, SeedNode(key, domain.TypeSource, SourceProps(s)))
	}
	if len(edits) == 0 {
		return false, nil
	}
	return true, SeedChange(ctx, g, domain.NamespacePlatform, "Sources of the platform", edits)
}

// ParseSources reads the graph sources a bootstrap declares: "name=ns1,ns2@url;name2=ns3@url2", each the name of the
// source, the namespaces the graph at url holds, then its base URL (GOAP_GRAPH_SOURCES).
func ParseSources(spec string) ([]lnk.Source, error) {
	var out []lnk.Source
	for _, group := range strings.Split(spec, ";") {
		group = strings.TrimSpace(group)
		if group == "" {
			continue
		}
		name, rest, ok := strings.Cut(group, "=")
		names, base, ok2 := strings.Cut(rest, "@")
		if !ok || !ok2 || strings.TrimSpace(base) == "" {
			return nil, fmt.Errorf("graph sources: want name=namespaces@url, got %q", group)
		}
		s := lnk.Source{Name: strings.TrimSpace(name), Kind: lnk.KindGraph, Location: strings.TrimSpace(base)}
		for _, ns := range strings.Split(names, ",") {
			if ns = strings.TrimSpace(ns); ns != "" {
				s.Namespaces = append(s.Namespaces, ns)
			}
		}
		if len(s.Namespaces) == 0 {
			return nil, fmt.Errorf("graph sources: %s holds no namespace", s.Name)
		}
		if err := s.Validate(); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// PlatformSourceValidator keeps the platform graph's own source as the bootstrap declares it: the namespaces no other
// graph holds are this graph's, so no change may edit, retarget or retire SRC:platform. Its creation must be the
// declaration of this graph (kind graph, no location, no namespace held). Wired onto Graph.Validators (cmd/graph).
type PlatformSourceValidator struct{}

// Types runs the validator whenever a change touches a source.
func (PlatformSourceValidator) Types() []string { return []string{domain.TypeSource} }

// Validate refuses any version of the platform source but its creation as this graph.
func (PlatformSourceValidator) Validate(_ context.Context, _ graphapi.ValidatorQuery, impacted []graphapi.ValidatedNode) error {
	key := domain.SourceKey(PlatformSource)
	for _, v := range impacted {
		if v.Post.Key != key {
			continue
		}
		if v.Pre == nil {
			s, err := domain.SourceOf(v.Post)
			if err != nil || s.Kind != lnk.KindGraph || s.Location != "" || len(s.Namespaces) > 0 {
				return fmt.Errorf("source %s is this graph: it is declared of kind graph, with no location and no namespace held: %w", PlatformSource, graphapi.ErrInvalid)
			}
			continue
		}
		pre, _ := json.Marshal(v.Pre.Properties)
		post, _ := json.Marshal(v.Post.Properties)
		if v.Post.State != v.Pre.State || string(pre) != string(post) {
			return fmt.Errorf("source %s is the platform graph: it cannot be edited or retired: %w", PlatformSource, graphapi.ErrInvalid)
		}
	}
	return nil
}
