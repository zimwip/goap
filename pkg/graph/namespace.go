package graph

import (
	"context"

	"github.com/zimwip/goap/pkg/domain"
)

// checkNamespace enforces that a change only creates and modifies nodes of its
// own namespace. Reading nodes of another namespace (impacts, link targets) is
// allowed: namespaces may reference each other.
func checkNamespace(ctx context.Context, tx Tx, c domain.ChangeSet) error {
	if _, isMerge := c.Data["merge"]; isMerge {
		return nil // a branch merge only moves versions between branches
	}
	ns := domain.NamespaceOf(c.Namespace)
	inNS := func(ref *domain.NodeRef, what string) error {
		if ref == nil {
			return nil
		}
		n, err := tx.Node(ctx, *ref)
		if err != nil {
			return err
		}
		if got := domain.NamespaceOf(n.Namespace); got != ns {
			return invalidf("cannot %s %s: it belongs to namespace %q, the change acts on %q", what, n.Key, got, ns)
		}
		return nil
	}
	for _, it := range c.Items {
		if it.Kind != domain.KindProposal || !c.InEffect(it.ID) {
			continue
		}
		p := it.Proposal
		switch p.Op {
		case domain.OpCreateNode:
			if got := domain.NamespaceOf(p.Node.Namespace); p.Node.Namespace != "" && got != ns {
				return invalidf("cannot create %s in namespace %q: the change acts on %q", p.Node.Key, got, ns)
			}
		case domain.OpUpdateNode, domain.OpDeleteNode, domain.OpTransitionNode, domain.OpMergeNode:
			if err := inNS(p.Node.Base, "modify"); err != nil {
				return err
			}
		case domain.OpAddLink:
			// instanceOf binds a data node to the metadata layer (code-managed), from any namespace.
			if p.Link.Type == domain.LinkInstanceOf {
				continue
			}
			if err := inNS(p.Link.From.Node, "link from"); err != nil {
				return err
			}
		}
	}
	return nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
