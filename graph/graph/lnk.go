package graph

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/lnk"
)

// Logical URLs (ADR 0154): every namespace of the graph is a graph source of its name with no location, read in process
// by the graph resolver; the other sources are platform@Source nodes, read by the resolver of their kind the composition
// plugs (Graph.Resolvers; a graph source with a location by its KindGraph resolver).

// lnkState is the registry of the graph, built at its first use.
type lnkState struct {
	once sync.Once
	reg  *lnk.Registry
}

// Lnk is the registry of the sources of the graph: the graph resolver, the other resolvers of Graph.Resolvers, the
// sources the platform@Source nodes on main declare.
func (g *Graph) Lnk() *lnk.Registry {
	g.lnk.once.Do(func() {
		r := lnk.NewRegistry()
		for kind, res := range g.Resolvers {
			r.Register(kind, res)
		}
		r.Register(lnk.KindGraph, Resolver{G: g, Remote: g.Resolvers[lnk.KindGraph]})
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

// Resolve reads what a logical URL points to, with no project: the sources as their names say.
func (g *Graph) Resolve(ctx context.Context, u lnk.URL) (lnk.Target, error) {
	return g.Lnk().Resolve(ctx, u)
}

// ResolveIn reads what a logical URL points to for a project (ADR 0154 §8): through the source mapping in force on it
// (Graph.ProjectSources, the project's and its parents'), else as Resolve.
func (g *Graph) ResolveIn(ctx context.Context, project string, u lnk.URL) (lnk.Target, error) {
	m, err := g.mappingOf(ctx, project)
	if err != nil {
		return lnk.Target{}, err
	}
	return g.Lnk().ResolveIn(ctx, m, u)
}

// ResolveFrom reads what a logical URL points to for a node: in the project the node belongs to.
func (g *Graph) ResolveFrom(ctx context.Context, from domain.Node, u lnk.URL) (lnk.Target, error) {
	return g.ResolveIn(ctx, from.Project, u)
}

// mappingOf is the source mapping of a project, nil without one.
func (g *Graph) mappingOf(ctx context.Context, project string) (lnk.Mapping, error) {
	if g.ProjectSources == nil || project == "" {
		return nil, nil
	}
	m, err := g.ProjectSources(ctx, project)
	if err != nil || len(m) == 0 {
		return nil, err
	}
	return func(name string) (string, bool) {
		to, ok := m[name]
		return to, ok
	}, nil
}

// LinkURL is the logical URL of the target of a link (ADR 0154): the URL of the node it points to, whatever its version;
// the URL it holds for a target in another source.
func (g *Graph) LinkURL(ctx context.Context, l domain.Link) (lnk.URL, error) {
	if l.External() {
		return l.Target, nil
	}
	n, err := g.Node(ctx, l.To)
	if err != nil {
		return lnk.URL{}, err
	}
	return n.URL(), nil
}

// Resolver reads the graph sources. One with no location is this graph: a namespace (Source.NamespaceOf), on a branch
// (Source.Branch, else main); a URL names a node by its key, which is unique in its namespace; its type must be the
// node's type or one the node's type extends; a URL with a version reads that version of the node. One with a location
// is another graph, read by Remote (none: lnk.ErrNoResolver).
type Resolver struct {
	G      *Graph
	Remote lnk.Resolver
}

// Resolve reads the node a URL names.
func (r Resolver) Resolve(ctx context.Context, src lnk.Source, u lnk.URL) (lnk.Target, error) {
	if src.Location != "" {
		if r.Remote == nil {
			return lnk.Target{}, fmt.Errorf("source %s at %s: %w", src.Name, src.Location, lnk.ErrNoResolver)
		}
		return r.Remote.Resolve(ctx, src, u)
	}
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
	t.URL = lnk.New(u.Source, lnk.BareType(n.Type), n.Key).At(u.Version)
	return t, nil
}

// isA reports a node type that is the type a URL names, or extends it (with a type catalogue).
func (r Resolver) isA(ns, typ, name string) bool {
	if lnk.BareType(typ) == name {
		return true
	}
	if r.G.Types == nil {
		return false
	}
	c := r.G.Types()
	return c != nil && c.IsA(typ, domain.TypeRef{Namespace: ns, Name: name}.String())
}

// URLOf gives the URL of a node of the source (this graph): the node of Target.Node, else the type and key of the target.
func (r Resolver) URLOf(ctx context.Context, src lnk.Source, t lnk.Target) (lnk.URL, error) {
	if src.Location != "" {
		if r.Remote == nil {
			return lnk.URL{}, fmt.Errorf("source %s at %s: %w", src.Name, src.Location, lnk.ErrNoResolver)
		}
		return r.Remote.URLOf(ctx, src, t)
	}
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

// Links to logical URLs (ADR 0154 step 2): a link written with a URL is a link to a node of this graph when its source,
// as the project of the change maps it, is a namespace of this graph, else a link to a target in another source, kept as
// its URL.

// linkSources are the sources the target URLs of the links of a write name, by the source of the URL: read before the
// transaction of the write (the project's mapping and the declared sources are read in transactions of their own).
type linkSources map[string]lnk.Source

type linkSourcesKey struct{}

// withLinkSources reads the sources of the target URLs of links in the project of the change, for the write that
// follows; ctx as is when no link names a URL.
func (g *Graph) withLinkSources(ctx context.Context, id domain.ChangeID, links ...LinkWrite) (context.Context, error) {
	var names []string
	for _, l := range links {
		if l.To.ID == "" && !l.Target.IsZero() {
			if err := l.Target.Validate(); err != nil {
				return ctx, fmt.Errorf("%v: %w", err, ErrInvalid)
			}
			names = append(names, l.Target.Source)
		}
	}
	if len(names) == 0 {
		return ctx, nil
	}
	var project string
	if err := g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.ChangeHead(ctx, id)
		project = c.ProjectID
		return err
	}); err != nil {
		return ctx, err
	}
	m, err := g.mappingOf(ctx, project)
	if err != nil {
		return ctx, err
	}
	srcs := linkSources{}
	for _, name := range names {
		to := name
		if m != nil {
			if mapped, ok := m(name); ok && mapped != "" {
				to = mapped
			}
		}
		src, err := g.Lnk().Source(ctx, to)
		if err != nil {
			return ctx, fmt.Errorf("%v: %w", err, ErrInvalid)
		}
		srcs[name] = src
	}
	return context.WithValue(ctx, linkSourcesKey{}, srcs), nil
}

// local reports a source this graph holds: a graph source with no location.
func local(src lnk.Source) bool { return src.Kind == lnk.KindGraph && src.Location == "" }

// targetOf turns the target URL of a link written to a draft into its target: a node of this graph sets l.To (the
// version the URL names, else the draft of the change on the flow, else the version on the change's branch), and the
// link is checked as any; a target in another source is the link returned, checked against the type its URL names. A
// node of another graph must name its version (graph links are versioned).
func (g *Graph) targetOf(ctx context.Context, tx Tx, w *work, from domain.Draft, l *LinkWrite) (*domain.DraftLink, error) {
	srcs, _ := ctx.Value(linkSourcesKey{}).(linkSources)
	src, ok := srcs[l.Target.Source]
	if !ok {
		return nil, invalidf("the source of %s was not read before the write", l.Target)
	}
	if local(src) {
		to, err := g.localTarget(ctx, tx, w, src, l.Target)
		if err != nil {
			return nil, err
		}
		l.To, l.Target = to, lnk.URL{}
		return nil, nil
	}
	toType := l.Target.Type
	if src.Kind == lnk.KindGraph {
		if l.Target.Version == 0 {
			return nil, invalidf("a link to %s, a node of graph source %s, names its version (?v=)", l.Target, src.Name)
		}
		toType = domain.TypeRef{Namespace: src.NamespaceOf(), Name: l.Target.Type}.String()
	}
	if err := w.ix.checkLink(l.Type, from.Type, toType); err != nil {
		return nil, err
	}
	if err := w.ix.checkLinkAttributes(l.Type, l.Properties); err != nil {
		return nil, err
	}
	return &domain.DraftLink{ID: domain.LinkID(g.newID()), Type: l.Type, Target: l.Target, Properties: l.Properties}, nil
}

// localTarget is the node of this graph a URL names, as the change sees it: the version the URL names, else the draft of
// the node on the flow, else its version on the change's branch.
func (g *Graph) localTarget(ctx context.Context, tx Tx, w *work, src lnk.Source, u lnk.URL) (domain.NodeRef, error) {
	ns := src.NamespaceOf()
	r := Resolver{G: g}
	notFound := func() (domain.NodeRef, error) {
		return domain.NodeRef{}, fmt.Errorf("%s: no %s %s in namespace %s: %w", u, u.Type, u.Key, ns, ErrInvalid)
	}
	if u.Version == 0 && domain.NamespaceOf(w.ns) == domain.NamespaceOf(ns) {
		for _, cn := range w.seen {
			if cn.Key == u.Key && cn.Post != nil && cn.Post.IsDraft() {
				if !r.isA(ns, cn.Type, u.Type) {
					return notFound()
				}
				return *cn.Post, nil
			}
		}
	}
	id, err := tx.NodeIDByKey(ctx, ns, u.Key)
	if errors.Is(err, ErrNotFound) {
		return notFound()
	}
	if err != nil {
		return domain.NodeRef{}, err
	}
	var n domain.Node
	if u.Version > 0 {
		n, err = tx.Node(ctx, domain.NodeRef{ID: id, Version: domain.Version(u.Version)})
	} else {
		branch := src.Branch
		if branch == "" && domain.NamespaceOf(w.ns) == domain.NamespaceOf(ns) {
			branch = w.branch
		}
		n, err = latestOnTx(ctx, tx, ns, branch, id)
	}
	if errors.Is(err, ErrNotFound) {
		return notFound()
	}
	if err != nil {
		return domain.NodeRef{}, err
	}
	if n.Deleted || !r.isA(ns, n.Type, u.Type) {
		return notFound()
	}
	return w.normalizeTo(n.Ref()), nil
}
