package graphsvc

import (
	"context"
	"errors"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/mcp"
)

// documentRepository is the generic MCP to manipulate documents.
func documentRepository() mcp.Def {
	obj := func(props map[string]any, required ...string) map[string]any {
		s := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			r := make([]any, len(required))
			for i, n := range required {
				r[i] = n
			}
			s["required"] = r
		}
		return s
	}
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	return mcp.Def{Name: "document-repository", Description: "Documents organised in folders.", Tools: []mcp.Tool{
		{Name: "list", Description: "List the documents and folders under a path.", InputSchema: obj(map[string]any{"path": str("folder path, empty for the root")})},
		{Name: "read", Description: "Read the text of a document.", InputSchema: obj(map[string]any{"path": str("document path")}, "path")},
		{Name: "write", Description: "Create or replace a document.", InputSchema: obj(map[string]any{"path": str("document path"), "content": str("text of the document")}, "path", "content")},
	}}
}

// applyOn applies proposals on main as one change of a namespace, so that the head of main
// moves (baselines made outside a change do not once main has a head). The graph gets an
// empty initial baseline when it has none.
func applyOn(ctx context.Context, g *graph.Graph, namespace, title string, items []domain.ChangeItem) error {
	head, err := g.BranchHead(ctx, domain.MainBranch)
	if errors.Is(err, graph.ErrNotFound) {
		head, err = g.CreateBaseline(ctx, "Initial baseline", nil)
	}
	if err != nil {
		return err
	}
	c, err := g.CreateChange(ctx, graph.NewChange{Namespace: namespace, Title: title, Intent: title, BaselineID: head.ID})
	if err != nil {
		return err
	}
	if _, err := g.AddItems(ctx, c.ID, items); err != nil {
		return err
	}
	_, err = g.Apply(ctx, c.ID, title)
	return err
}

func createNode(id domain.ItemID, key, typ string, props map[string]any) domain.ChangeItem {
	return domain.ChangeItem{ID: id, Kind: domain.KindProposal, Type: "object", ProducedBy: "graphsvc.seed",
		Proposal: &domain.Proposal{Op: domain.OpCreateNode, Node: &domain.NodeDraft{Key: key, Type: typ, Properties: props}}}
}

// linkTo links the node of an item to an existing node of the graph.
func linkTo(from domain.ItemID, typ string, to domain.NodeRef) domain.ChangeItem {
	return domain.ChangeItem{Kind: domain.KindProposal, Type: "object", ProducedBy: "graphsvc.seed", DerivedFrom: []domain.ItemID{from},
		Proposal: &domain.Proposal{Op: domain.OpAddLink, Link: &domain.LinkDraft{Type: typ, From: domain.Endpoint{Item: from}, To: domain.Endpoint{Node: &to}}}}
}

// SeedDefaults makes sure, at every start, that the default organisation (the root of every
// unit's adapter resolution) and the document-repository MCP exist. It is idempotent: once the
// default organisation exists nothing is touched, so that edited or deleted MCPs stay so.
func SeedDefaults(ctx context.Context, g *graph.Graph) (bool, error) {
	if _, err := g.NodeByKey(ctx, mcp.NamespaceOrganisation, domain.DefaultOrg); err == nil {
		return false, nil
	} else if !errors.Is(err, graph.ErrNotFound) {
		return false, err
	}
	if err := applyOn(ctx, g, mcp.NamespaceOrganisation, "Default organisation", []domain.ChangeItem{
		createNode("default-org", domain.DefaultOrg, mcp.NodeTypeOrgUnit, map[string]any{"name": "Default organisation", "kind": "company",
			"description": "Holds the changes that name no unit, and the adapters every unit inherits."}),
	}); err != nil {
		return false, err
	}
	d := documentRepository()
	err := applyOn(ctx, g, mcp.NamespacePlatform, "MCP "+d.Name, []domain.ChangeItem{createNode("mcp", mcp.MCPKey(d.Name), mcp.NodeTypeMCP, d.Props())})
	return err == nil, err
}

// SeedUnit creates an organisational unit, under parent when it is not empty.
func SeedUnit(ctx context.Context, g *graph.Graph, key, name, kind, parent string) error {
	items := []domain.ChangeItem{createNode("unit", key, mcp.NodeTypeOrgUnit, map[string]any{"name": name, "kind": kind})}
	if parent != "" {
		p, err := g.NodeByKey(ctx, mcp.NamespaceOrganisation, parent)
		if err != nil {
			return err
		}
		items = append(items, linkTo("unit", mcp.LinkPartOf, p.Ref()))
	}
	return applyOn(ctx, g, mcp.NamespaceOrganisation, "Unit "+key, items)
}

// LocalFSAdapter is the instance of the localfs adapter of the platform library for a unit, exposing a
// directory as its document repository (demos and tests).
func LocalFSAdapter(unit, root string) mcp.Adapter {
	return mcp.Adapter{Unit: unit, MCP: "document-repository", Domain: "platform", Algorithm: "localfs-document-repository", Params: map[string]any{"root": root}}
}

// SeedAdapter creates the Adapter node of a unit, owned by it.
func SeedAdapter(ctx context.Context, g *graph.Graph, a mcp.Adapter) error {
	unit, err := g.NodeByKey(ctx, mcp.NamespaceOrganisation, a.Unit)
	if err != nil {
		return err
	}
	return applyOn(ctx, g, mcp.NamespaceOrganisation, "Adapter "+a.MCP+" of "+a.Unit, []domain.ChangeItem{
		createNode("adapter", mcp.AdapterKey(a.Unit, a.MCP), mcp.NodeTypeAdapter, a.Props()),
		linkTo("adapter", mcp.LinkOwner, unit.Ref()),
	})
}
