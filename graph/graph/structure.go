package graph

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/lnk"
	"github.com/zimwip/goap/lib/typecat"
)

// This file places every change and every node version in the two structures of the graph (ADR 0054): the
// organisation (who is responsible) and the project (where the work happens). They are the node types the type
// catalogue tags (`structure:` on a node type of a domain), with their parent link, root and default flag; an
// untyped graph uses the catalogue of the built-in domains.

// builtinTypes is the catalogue of the built-in domains the library embeds: what an untyped graph (tests, tools) falls
// back to. A typed graph asks its catalogue alone, the registry's domains in force (ADR 0167).
func builtinTypes() TypeCatalog { return typecat.Builtin() }

// Structure returns the hierarchy of a kind (domain.StructureOrganisation, domain.StructureProject) in force.
func (g *Graph) Structure(kind string) domain.Structure {
	if g.Types != nil {
		if c := g.catalog(); c != nil {
			s, _ := c.Structure(kind)
			return s
		}
	}
	s, _ := builtinTypes().Structure(kind)
	return s
}

// Structures returns the structures in force with the types belonging to each (ADR 0054): what the services reading
// the organisation learn from the graph service (GetStructures) instead of naming the types themselves.
func (g *Graph) Structures(context.Context) (domain.Structures, error) {
	if g.Types != nil {
		if c := g.catalog(); c != nil {
			return c.Structures(), nil
		}
	}
	return builtinTypes().Structures(), nil
}

// structures returns the structures in force, in bootstrap order: the organisation first, the project next, any
// other declared structure after.
func (g *Graph) structures() []domain.Structure {
	all, _ := g.Structures(context.Background())
	out := []domain.Structure{g.Structure(domain.StructureOrganisation), g.Structure(domain.StructureProject)}
	for _, x := range all {
		if x.Kind != domain.StructureOrganisation && x.Kind != domain.StructureProject {
			out = append(out, x.Structure)
		}
	}
	return out
}

// requires returns the links a node of the type must carry (ADR 0065), in force: the type catalogue's, the built-in
// ones for an untyped graph.
func (g *Graph) requires(typ string) []domain.RequiredLink {
	if g.Types != nil {
		if c := g.catalog(); c != nil {
			return c.Requires(typ)
		}
	}
	return builtinTypes().Requires(typ)
}

// AdminOnlyType reports a node type whose nodes only platform administrators write (ADR 0068): the type
// catalogue's `adminOnly:` flag, the built-in domains' for an untyped graph. The one question every write path asks.
func (g *Graph) AdminOnlyType(_ context.Context, typ string) (bool, error) {
	if g.Types != nil {
		if c := g.catalog(); c != nil {
			return c.AdminOnly(typ), nil
		}
	}
	return builtinTypes().AdminOnly(typ), nil
}

// isA reports whether the node type typ is base or a subtype of it.
func (g *Graph) isA(typ, base string) bool {
	if typ == base {
		return true
	}
	if g.Types != nil {
		if c := g.catalog(); c != nil {
			return c.IsA(typ, base)
		}
	}
	return builtinTypes().IsA(typ, base)
}

// inStructure reports whether a node belongs to a structure: of its namespace and of its type or a subtype.
func (g *Graph) inStructure(n domain.Node, st domain.Structure) bool {
	return domain.NamespaceOf(n.Namespace) == st.Namespace && g.isA(n.Type, st.Type)
}

// StructureURL is the logical URL of the node of a structure by its key (ADR 0154 §9): the structure's namespace as
// its source, the structure's type. What an owner or a project is stored as: a reference to a node is a link.
func (g *Graph) StructureURL(kind, key string) lnk.URL {
	st := g.Structure(kind)
	return lnk.New(st.Namespace, lnk.BareType(st.Type), key)
}

// StructureRef reads what an input names as a node of a structure: its logical URL, or its bare key (the structure's
// URL of that key). A reference names the node whatever its version: the version of a URL is dropped. "": none.
func (g *Graph) StructureRef(kind, s string) (lnk.URL, error) {
	if s == "" {
		return lnk.URL{}, nil
	}
	if !lnk.IsURL(s) {
		return g.StructureURL(kind, s), nil
	}
	u, err := lnk.Parse(s)
	if err != nil {
		return lnk.URL{}, fmt.Errorf("%v: %w", err, ErrInvalid)
	}
	return u.Logical(), nil
}

// structureNode resolves the URL of a node of a structure: it must name the structure's source (its namespace, held
// here or by Graph.Directory) and a type of the structure, and the node must exist, live on main and be of that type.
func (g *Graph) structureNode(ctx context.Context, tx Tx, kind string, u lnk.URL) (domain.Node, error) {
	st := g.Structure(kind)
	what := map[string]string{domain.StructureOrganisation: "organisational unit", domain.StructureProject: "project"}[kind]
	switch {
	case u.IsZero():
		return domain.Node{}, fmt.Errorf("no %s named: %w", what, ErrInvalid)
	case u.Source != st.Namespace:
		return domain.Node{}, fmt.Errorf("%s %s: a %s is read in the source %s: %w", what, u, kind, st.Namespace, ErrInvalid)
	case !g.isA(st.Namespace+"@"+u.Type, st.Type):
		return domain.Node{}, fmt.Errorf("%s %s: %s is not a type of the %s: %w", what, u, u.Type, kind, ErrInvalid)
	}
	n, err := g.structureByKey(ctx, tx, kind, u.Key)
	if err != nil {
		return n, err
	}
	if !g.isA(n.Type, st.Namespace+"@"+u.Type) {
		return domain.Node{}, fmt.Errorf("%s %s is a %s: %w", what, u, n.Type, ErrInvalid)
	}
	return n, nil
}

// resolveStructure reads what an input names as a node of a structure (its URL or its key, StructureRef) and resolves
// it (structureNode): the node, and the URL a reference to it is stored as, the structure's own (one spelling per node,
// so that two references to it compare equal).
func (g *Graph) resolveStructure(ctx context.Context, tx Tx, kind, s string) (lnk.URL, domain.Node, error) {
	u, err := g.StructureRef(kind, s)
	if err != nil {
		return lnk.URL{}, domain.Node{}, err
	}
	n, err := g.structureNode(ctx, tx, kind, u)
	if err != nil {
		return lnk.URL{}, n, err
	}
	return g.StructureURL(kind, n.Key), n, nil
}

// structureByKey reads the node of a structure by its key.
func (g *Graph) structureByKey(ctx context.Context, tx Tx, kind, key string) (domain.Node, error) {
	st := g.Structure(kind)
	what := map[string]string{domain.StructureOrganisation: "organisational unit", domain.StructureProject: "project"}[kind]
	if g.Directory != nil {
		n, ok, err := g.Directory.StructureNode(ctx, kind, key)
		if err != nil {
			return n, err
		}
		if !ok {
			return n, fmt.Errorf("%s %q is not a %s of the platform: %w", what, key, kind, ErrInvalid)
		}
		return n, nil
	}
	id, err := tx.NodeIDByKey(ctx, st.Namespace, key)
	if errors.Is(err, ErrNotFound) {
		return domain.Node{}, fmt.Errorf("%s %q is not a node of namespace %s: %w", what, key, st.Namespace, ErrInvalid)
	}
	if err != nil {
		return domain.Node{}, err
	}
	n, err := tx.LatestOn(ctx, id, domain.MainBranch)
	if errors.Is(err, ErrNotFound) {
		return domain.Node{}, fmt.Errorf("%s %q is not on main yet: %w", what, key, ErrInvalid)
	}
	if err != nil {
		return domain.Node{}, err
	}
	if n.Deleted {
		return domain.Node{}, fmt.Errorf("%s %q is deleted: %w", what, key, ErrInvalid)
	}
	if !g.inStructure(n, st) {
		return domain.Node{}, fmt.Errorf("%s %q is a %s, not a %s: %w", what, key, n.Type, st.Type, ErrInvalid)
	}
	return n, nil
}

// StructureDirectory reads the structures when another graph holds them (Graph.Directory, ADR 0154 §9).
type StructureDirectory interface {
	// StructureNode is the node of a structure of a kind by its key, retired or not; found false when there is none.
	StructureNode(ctx context.Context, kind, key string) (n domain.Node, found bool, err error)
	// StructureChain is a node of a structure followed by its ancestors, nearest first, the root last.
	StructureChain(ctx context.Context, kind, key string) ([]string, error)
}

// within reports whether the node of a structure `u` is `of` or below it, following the parent links (child -> parent)
// of the latest versions on main. Both name nodes of the structure (structureNode checked them).
func (g *Graph) within(ctx context.Context, tx Tx, kind string, u, of lnk.URL) (bool, error) {
	key, ancestor := u.Key, of.Key
	if g.Directory != nil {
		chain, err := g.Directory.StructureChain(ctx, kind, key)
		return slices.Contains(chain, ancestor), err
	}
	st := g.Structure(kind)
	seen := map[string]bool{}
	for cur := key; cur != "" && !seen[cur]; {
		if cur == ancestor {
			return true, nil
		}
		seen[cur] = true
		id, err := tx.NodeIDByKey(ctx, st.Namespace, cur)
		if err != nil {
			return false, err
		}
		n, err := tx.LatestOn(ctx, id, domain.MainBranch)
		if err != nil {
			return false, err
		}
		links, err := tx.OutLinks(ctx, n.Ref())
		if err != nil {
			return false, err
		}
		cur = ""
		for _, l := range links {
			if l.Type != st.Parent {
				continue
			}
			p, err := tx.Node(ctx, l.To)
			if err != nil {
				return false, err
			}
			cur = p.Key
			break
		}
	}
	return false, nil
}

// scopeChange resolves and checks who holds a change and where it acts (ADR 0054, 0091): an unset owner is the root
// unit; a project is never defaulted (a sub-change inherited its parent's already, a root change names its own, ADR
// 0091). Both must designate live nodes of their structures; nothing is left empty.
func (g *Graph) scopeChange(ctx context.Context, tx Tx, c *domain.Change) error {
	if c.OwnerOrg.IsZero() {
		c.OwnerOrg = g.StructureURL(domain.StructureOrganisation, g.Structure(domain.StructureOrganisation).Root)
	}
	if c.ProjectID.IsZero() {
		return fmt.Errorf("a change names the project it acts in: %w", ErrInvalid)
	}
	unit, err := g.structureNode(ctx, tx, domain.StructureOrganisation, c.OwnerOrg)
	if err != nil {
		return fmt.Errorf("owner of the change: %w", err)
	}
	project, err := g.structureNode(ctx, tx, domain.StructureProject, c.ProjectID)
	if err != nil {
		return fmt.Errorf("project of the change: %w", err)
	}
	// one spelling per node (resolveStructure)
	c.OwnerOrg, c.ProjectID = g.StructureURL(domain.StructureOrganisation, unit.Key), g.StructureURL(domain.StructureProject, project.Key)
	return nil
}
