package graph

import (
	"context"
	"fmt"
	"slices"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/graphapi"
)

// Reach is what a caller reads of the current graph (ADR 0161 §4-5): the versions whose scope (Node.Visibility)
// reaches it. A nil *Reach reads everything (a service acting by itself, a graph with no ReachOf).
type Reach struct {
	// Projects are the keys of the projects the caller works on, with the projects below them: it reads their versions
	// of scope project, and of scope private or personal when it is their responsible unit or their creator.
	Projects map[string]bool
	// Trees are the keys of the projects whose versions of scope tree the caller reads: the projects of the parent tree
	// of a project it works on (the children of every project above it, siblings included).
	Trees map[string]bool
	// Units are the keys of the caller's units (its own personal unit, its unit and the units above it): it reads the
	// private versions they answer for.
	Units map[string]bool
	// Subject is the caller: it reads the personal versions of the nodes it created.
	Subject string
	// Namespaces are the namespaces read whole (the built-in organisation, platform and methodology).
	Namespaces map[string]bool
}

// ReachOf is the reach of the caller of a read, plugged by the compositions from the organisation they read
// (graphsvc.ReachOf). It is asked before the transaction of the read: it may read the organisation.
type ReachOf func(ctx context.Context) (*Reach, error)

// reachOf is the reach of the caller (nil: everything).
func (g *Graph) reachOf(ctx context.Context) (*Reach, error) {
	if g.ReachOf == nil {
		return nil, nil
	}
	return g.ReachOf(ctx)
}

// in is the reach of a namespace: nil when it is read whole.
func (r *Reach) in(namespace string) *Reach {
	if r == nil || r.Namespaces[domain.NamespaceOf(namespace)] {
		return nil
	}
	return r
}

// sees reports whether the reach holds a version: its scope reaches the caller (ADR 0161 §4).
func (r *Reach) sees(n domain.Node) bool {
	if r == nil {
		return true
	}
	p := n.Project.Key
	switch domain.VisibilityOf(n.Visibility) {
	case domain.VisibilityPublic:
		return true
	case domain.VisibilityTree:
		return r.Trees[p] || r.Projects[p]
	case domain.VisibilityPrivate:
		return r.Projects[p] && r.Units[n.Responsible.Key]
	case domain.VisibilityPersonal:
		return r.Projects[p] && r.Subject != "" && n.Creator.Subject == r.Subject
	default:
		return r.Projects[p]
	}
}

// Reach is the reach of the caller of a read (nil: everything), for the read paths that return a state whole (a
// baseline's graph, a delta, a diff, an export): they keep what Reach.Keep keeps (ADR 0161 §5).
func (g *Graph) Reach(ctx context.Context) (*Reach, error) { return g.reachOf(ctx) }

// Sees reports whether the reach holds a version of any namespace.
func (r *Reach) Sees(n domain.Node) bool { return r.in(n.Namespace).sees(n) }

// Keep keeps the nodes the reach holds and the links none of whose ends is a node it leaves out (no stub: a link to a
// node out of reach tells the node exists).
func (r *Reach) Keep(nodes []domain.Node, links []domain.Link) ([]domain.Node, []domain.Link) {
	if r == nil {
		return nodes, links
	}
	hidden := map[domain.NodeID]bool{}
	nodes = slices.DeleteFunc(nodes, func(n domain.Node) bool {
		hidden[n.ID] = !r.Sees(n)
		return hidden[n.ID]
	})
	links = slices.DeleteFunc(links, func(l domain.Link) bool { return hidden[l.From.ID] || (!l.External() && hidden[l.To.ID]) })
	return nodes, links
}

// filterOf is the reach as the stores filter it: the keys as the nodes store them (their logical URLs, and the bare
// keys), sorted; nil reads everything.
func (g *Graph) filterOf(r *Reach) *graphapi.ReachFilter {
	if r == nil {
		return nil
	}
	keys := func(kind string, set map[string]bool) []string {
		out := []string{}
		for k, ok := range set {
			if ok {
				out = append(out, k, g.StructureURL(kind, k).String())
			}
		}
		slices.Sort(out)
		return slices.Compact(out)
	}
	return &graphapi.ReachFilter{Projects: keys(domain.StructureProject, r.Projects), Trees: keys(domain.StructureProject, r.Trees),
		Units: keys(domain.StructureOrganisation, r.Units), Subject: r.Subject}
}

// sees reports whether the caller reads a node of the world: in its reach, or a draft of the change it reads (a draft
// is the change's, read through it, ADR 0161 §4).
func (w *world) sees(n domain.Node) bool {
	if w.reach == nil || (w.change != nil && n.Version == 0) {
		return true
	}
	return w.reach.sees(n)
}

// hidden tells which of the nodes some refs name the caller does not read, by the scope of the version named: one read
// for the stored ones, the drafts of the change seen.
func (w *world) hidden(ctx context.Context, tx Tx, refs []domain.NodeRef) (map[domain.NodeID]bool, error) {
	out := map[domain.NodeID]bool{}
	if w.reach == nil {
		return out, nil
	}
	var stored []domain.NodeRef
	for _, r := range refs {
		if _, ok := w.vis[r]; ok || r.Version == 0 {
			continue
		}
		stored = append(stored, r)
	}
	if len(stored) > 0 {
		nodes, err := tx.NodesAt(ctx, stored, "")
		if err != nil {
			return nil, err
		}
		for _, n := range nodes {
			w.vis[n.Ref()] = w.reach.sees(n)
		}
	}
	for _, r := range refs {
		if v, ok := w.vis[r]; ok && !v {
			out[r.ID] = true
		}
	}
	return out, nil
}

// keepLinks are the links whose other end (from the node's side: the target of an outgoing link, the source of an
// entering one) the caller reads; a link to another source is kept.
func (w *world) keepLinks(ctx context.Context, tx Tx, out, in []domain.Link) ([]domain.Link, []domain.Link, error) {
	if w.reach == nil {
		return out, in, nil
	}
	var refs []domain.NodeRef
	for _, l := range out {
		if !l.External() {
			refs = append(refs, l.To)
		}
	}
	for _, l := range in {
		refs = append(refs, l.From)
	}
	hide, err := w.hidden(ctx, tx, refs)
	if err != nil || len(hide) == 0 {
		return out, in, err
	}
	out = slices.DeleteFunc(slices.Clone(out), func(l domain.Link) bool { return !l.External() && hide[l.To.ID] })
	in = slices.DeleteFunc(slices.Clone(in), func(l domain.Link) bool { return hide[l.From.ID] })
	return out, in, nil
}

// OwnedWrites tells the namespaces whose nodes a change writes only when its project owns them (ADR 0161 §3): the
// node's project is the change's or one below it. Plugged by the compositions (graphsvc.OwnedWrites: every namespace
// but the built-in ones the platform administers). Unset: a change writes the nodes of any project.
type OwnedWrites func(namespace string) bool

// checkOwned refuses the check out of a stored node by a change whose project does not own it (ADR 0161 §3): seeing a
// node of another project gives no action on it. A node with no project, or a change with none, is not judged.
func (g *Graph) checkOwned(ctx context.Context, tx Tx, c domain.Change, n domain.Node) error {
	if g.OwnedWrites == nil || !g.OwnedWrites(domain.NamespaceOf(n.Namespace)) || n.Project.IsZero() || c.ProjectID.IsZero() {
		return nil
	}
	if n.Project.Key == c.ProjectID.Key {
		return nil
	}
	ok, err := g.within(ctx, tx, domain.StructureProject, n.Project, c.ProjectID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("node %s belongs to project %s, which change %s (project %s) does not own: %w", n.Key, n.Project.Key, c.ID, c.ProjectID.Key, authz.ErrForbidden)
	}
	return nil
}

// checkShare refuses a move out of a personal state to a wider scope by anyone but the node's creator (ADR 0161 §4,
// node:share): whatever the lifecycle says, neither an administrator nor an agent acting for the creator widens a
// personal node; the creator does it themselves.
func (g *Graph) checkShare(ctx context.Context, tx Tx, d domain.Draft, from, to string) error {
	if g.visibilityOf(d.Type, from) != domain.VisibilityPersonal || g.visibilityOf(d.Type, to) == domain.VisibilityPersonal {
		return nil
	}
	creator := d.Creator
	if d.Base != nil {
		n, err := tx.Node(ctx, *d.Base)
		if err != nil {
			return err
		}
		creator = n.Creator.Subject
	}
	if caller := g.caller(ctx); caller != creator {
		return fmt.Errorf("%s leaves the personal scope for %s: only its creator %s shares it (node:share): %w", d.Key, g.visibilityOf(d.Type, to), creator, authz.ErrForbidden)
	}
	return nil
}
