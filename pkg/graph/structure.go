package graph

import (
	"context"
	"errors"
	"fmt"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/typecat"
)

// This file places every change and every node version in the two structures of the graph (ADR 0054): the
// organisation (who is responsible) and the project (where the work happens). They are the node types the type
// catalogue tags (`structure:` on a node type of a domain), with their parent link, root and default flag; an
// untyped graph uses the catalogue of the built-in domains.

// builtinTypes is the catalogue of the built-in domains alone: what an untyped graph (tests, tools) and a catalogue
// that does not resolve a question fall back to, so that the built-in definitions have one source (their yaml).
func builtinTypes() TypeCatalog { return typecat.Builtin() }

// Structure returns the hierarchy of a kind (domain.StructureOrganisation, domain.StructureProject) in force.
func (g *Graph) Structure(kind string) domain.Structure {
	if g.Types != nil {
		if c := g.catalog(); c != nil {
			if s, ok := c.Structure(kind); ok {
				return s
			}
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
			if rs := c.Requires(typ); rs != nil {
				return rs
			}
		}
	}
	return builtinTypes().Requires(typ)
}

// AdminOnlyType reports a node type whose nodes only platform administrators write (ADR 0068): the type
// catalogue's `adminOnly:` flag, plus the built-in ones for an untyped graph. The one
// question every write path asks.
func (g *Graph) AdminOnlyType(_ context.Context, typ string) (bool, error) {
	if g.Types != nil {
		if c := g.catalog(); c != nil && c.AdminOnly(typ) {
			return true, nil
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

// structureNode resolves the key of a node of a structure: it must exist, live on main and be of the structure.
func (g *Graph) structureNode(ctx context.Context, tx Tx, kind, key string) (domain.Node, error) {
	st := g.Structure(kind)
	what := map[string]string{domain.StructureOrganisation: "organisational unit", domain.StructureProject: "project"}[kind]
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

// within reports whether the node of a structure with key `key` is `ancestor` or below it, following the parent links
// (child -> parent) of the latest versions on main.
func (g *Graph) within(ctx context.Context, tx Tx, kind, key, ancestor string) (bool, error) {
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
	if c.OwnerOrg == "" {
		c.OwnerOrg = g.Structure(domain.StructureOrganisation).Root
	}
	if c.ProjectID == "" {
		return fmt.Errorf("a change names the project it acts in: %w", ErrInvalid)
	}
	if _, err := g.structureNode(ctx, tx, domain.StructureOrganisation, c.OwnerOrg); err != nil {
		return fmt.Errorf("owner of the change: %w", err)
	}
	if _, err := g.structureNode(ctx, tx, domain.StructureProject, c.ProjectID); err != nil {
		return fmt.Errorf("project of the change: %w", err)
	}
	return nil
}
