package graph

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/lnk"
)

// Logical URLs (ADR 0154): every namespace of the graph is a self source of its name, read by the self resolver; the
// other sources are platform@Source nodes, read by the resolver of their kind the composition plugs (Graph.Resolvers).

// lnkState is the registry of the graph, built at its first use.
type lnkState struct {
	once sync.Once
	reg  *lnk.Registry
}

// Lnk is the registry of the sources of the graph: the self resolver, the resolvers of Graph.Resolvers, the sources the
// platform@Source nodes on main declare.
func (g *Graph) Lnk() *lnk.Registry {
	g.lnk.once.Do(func() {
		r := lnk.NewRegistry(SelfResolver{G: g})
		for kind, res := range g.Resolvers {
			if kind != lnk.KindSelf {
				r.Register(kind, res)
			}
		}
		r.Lookup = g.declaredSource
		g.lnk.reg = r
	})
	return g.lnk.reg
}

// declaredSource reads the platform@Source node of a name, in force on main.
func (g *Graph) declaredSource(ctx context.Context, name string) (lnk.Source, bool, error) {
	n, err := g.NodeByKeyOn(ctx, domain.NamespacePlatform, domain.MainBranch, domain.SourceKey(name))
	if errors.Is(err, ErrNotFound) {
		return lnk.Source{}, false, nil
	}
	if err != nil {
		return lnk.Source{}, false, err
	}
	if n.Deleted || n.Type != domain.TypeSource || n.State == "retired" {
		return lnk.Source{}, false, nil
	}
	src, err := domain.SourceOf(n)
	if err != nil {
		return lnk.Source{}, false, err
	}
	return src, true, nil
}

// Resolve reads what a logical URL points to.
func (g *Graph) Resolve(ctx context.Context, u lnk.URL) (lnk.Target, error) {
	return g.Lnk().Resolve(ctx, u)
}

// LinkURL is the logical URL of the target of a link (ADR 0154): the URL of the node it points to, whatever its version.
func (g *Graph) LinkURL(ctx context.Context, l domain.Link) (lnk.URL, error) {
	n, err := g.Node(ctx, l.To)
	if err != nil {
		return lnk.URL{}, err
	}
	return n.URL(), nil
}

// SelfResolver reads the self sources: a namespace of the graph (Source.NamespaceOf), on a branch (Source.Branch, else
// main). A URL names a node by its key, which is unique in its namespace; its type must be the node's type or one the
// node's type extends. A URL with a version reads that version of the node.
type SelfResolver struct {
	G *Graph
}

// Resolve reads the node a URL names.
func (r SelfResolver) Resolve(ctx context.Context, src lnk.Source, u lnk.URL) (lnk.Target, error) {
	ns := src.NamespaceOf()
	n, err := r.G.NodeByKeyOn(ctx, ns, domain.BranchOf(src.Branch), u.Key)
	if err == nil && u.Version > 0 && int(n.Version) != u.Version {
		n, err = r.G.Node(ctx, domain.NodeRef{ID: n.ID, Version: domain.Version(u.Version)})
	}
	if errors.Is(err, ErrNotFound) {
		return lnk.Target{}, fmt.Errorf("%s: %w", u, lnk.ErrNotFound)
	}
	if err != nil {
		return lnk.Target{}, err
	}
	if n.Deleted || !r.isA(ns, n.Type, u.Type) {
		return lnk.Target{}, fmt.Errorf("%s: %w", u, lnk.ErrNotFound)
	}
	t := n.Target()
	t.URL = lnk.New(src.Name, lnk.BareType(n.Type), n.Key).At(u.Version)
	return t, nil
}

// isA reports a node type that is the type a URL names, or extends it (with a type catalogue).
func (r SelfResolver) isA(ns, typ, name string) bool {
	if lnk.BareType(typ) == name {
		return true
	}
	if r.G.Types == nil {
		return false
	}
	c := r.G.Types()
	return c != nil && c.IsA(typ, domain.TypeRef{Namespace: ns, Name: name}.String())
}

// URLOf gives the URL of a node of the source: the node of Target.Node, else the type and key of the target.
func (r SelfResolver) URLOf(ctx context.Context, src lnk.Source, t lnk.Target) (lnk.URL, error) {
	if t.Node != nil && t.Node.ID != "" {
		n, err := r.G.Node(ctx, domain.NodeRef{ID: domain.NodeID(t.Node.ID)})
		if errors.Is(err, ErrNotFound) {
			return lnk.URL{}, fmt.Errorf("node %s: %w", t.Node.ID, lnk.ErrNotFound)
		}
		if err != nil {
			return lnk.URL{}, err
		}
		if domain.NamespaceOf(n.Namespace) != src.NamespaceOf() {
			return lnk.URL{}, fmt.Errorf("node %s lives in %s, not in source %s: %w", n.Key, n.Namespace, src.Name, lnk.ErrNotFound)
		}
		return lnk.New(src.Name, lnk.BareType(n.Type), n.Key).At(t.Node.Version), nil
	}
	return lnk.New(src.Name, lnk.BareType(t.Type), t.Key), nil
}
