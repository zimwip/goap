package graphsvc

import (
	"context"
	"errors"
	"fmt"

	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/algo"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/llmcfg"
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

// LocalFSAdapterName is the adapter definition of the document-repository MCP on the localfs connector.
const LocalFSAdapterName = "localfs-document-repository"

// localFSAdapterDef is the reference adapter: the document-repository MCP on the local file system connector.
func localFSAdapterDef() mcp.AdapterDef {
	return mcp.AdapterDef{
		Name:        LocalFSAdapterName,
		Description: "The document-repository MCP on the local file system connector (localfs)",
		MCP:         "document-repository",
		Connector:   "localfs",
		Language:    algo.JavaScript,
		Params:      []algo.Param{{Name: "root", Type: algo.ParamString, Required: true, Description: "Directory the unit exposes as its document repository"}},
		Code: `switch (ctx.tool()) {
  case "list":
    return ctx.call("list_dir", { path: ctx.args().path || "" });
  case "read":
    return ctx.call("read_file", { path: ctx.args().path });
  case "write":
    return ctx.call("write_file", { path: ctx.args().path, content: ctx.args().content });
}
ctx.fail("unknown tool " + ctx.tool());
`,
	}
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
// unit's adapter resolution), the document-repository MCP and its localfs adapter definition exist. It is idempotent: once the
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
	a := localFSAdapterDef()
	err := applyOn(ctx, g, mcp.NamespacePlatform, "MCP "+d.Name, []domain.ChangeItem{
		createNode("mcp", mcp.MCPKey(d.Name), mcp.NodeTypeMCP, d.Props()),
		createNode("adapter-def", mcp.AdapterDefKey(a.Name), mcp.NodeTypeAdapterDef, a.Props()),
	})
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
	return mcp.Adapter{Unit: unit, MCP: "document-repository", Adapter: LocalFSAdapterName, Params: map[string]any{"root": root}}
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

// SeedAdapterDef creates the AdapterDef node of an adapter definition in the platform namespace.
func SeedAdapterDef(ctx context.Context, g *graph.Graph, d mcp.AdapterDef) error {
	return applyOn(ctx, g, mcp.NamespacePlatform, "Adapter "+d.Name, []domain.ChangeItem{createNode("adapter-def", mcp.AdapterDefKey(d.Name), mcp.NodeTypeAdapterDef, d.Props())})
}

// SeedAccess makes sure the default policies exist as Policy nodes of the organisation namespace. It is
// idempotent: once the floor policy exists nothing is touched, so that edited or deleted policies stay so.
func SeedAccess(ctx context.Context, g *graph.Graph) (bool, error) {
	if _, err := g.NodeByKey(ctx, mcp.NamespaceOrganisation, access.PolicyKey(authz.FloorPolicies[0])); err == nil {
		return false, nil
	} else if !errors.Is(err, graph.ErrNotFound) {
		return false, err
	}
	items := make([]domain.ChangeItem, len(authz.DefaultPolicies))
	for i, p := range authz.DefaultPolicies {
		items[i] = createNode(domain.ItemID(fmt.Sprintf("policy-%d", i)), access.PolicyKey(p), access.NodeTypePolicy, access.PolicyProps(p))
	}
	return true, applyOn(ctx, g, mcp.NamespaceOrganisation, "Default policies", items)
}

// SeedUser creates the User node of a subject, member of a unit when unit is not empty.
func SeedUser(ctx context.Context, g *graph.Graph, u access.User) error {
	items := []domain.ChangeItem{createNode("user", access.UserKey(u.Subject), access.NodeTypeUser, u.Props())}
	if u.Unit != "" {
		unit, err := g.NodeByKey(ctx, mcp.NamespaceOrganisation, u.Unit)
		if err != nil {
			return err
		}
		items = append(items, linkTo("user", access.LinkMemberOf, unit.Ref()))
	}
	return applyOn(ctx, g, mcp.NamespaceOrganisation, "User "+u.Subject, items)
}

// SeedPolicy creates a Policy node.
func SeedPolicy(ctx context.Context, g *graph.Graph, p authz.Policy) error {
	return applyOn(ctx, g, mcp.NamespaceOrganisation, "Policy "+p.Resource+"/"+p.Action, []domain.ChangeItem{
		createNode("policy", access.PolicyKey(p), access.NodeTypePolicy, access.PolicyProps(p))})
}

// SeedModels creates the model gateway configuration (providers, models, aliases; nodes of the platform namespace)
// when the graph holds no provider yet, so that providers or models deleted on purpose stay so. It reports
// whether it seeded.
func SeedModels(ctx context.Context, g *graph.Graph, providers []llmcfg.Provider, models []llmcfg.Model, aliases []llmcfg.Alias) (bool, error) {
	head, err := g.BranchHead(ctx, domain.MainBranch)
	if err == nil {
		nodes, _, gerr := g.BaselineGraph(ctx, head.ID)
		if gerr != nil {
			return false, gerr
		}
		for _, n := range nodes {
			if n.Namespace == llmcfg.NamespacePlatform && n.Type == llmcfg.NodeTypeProvider {
				return false, nil
			}
		}
	} else if !errors.Is(err, graph.ErrNotFound) {
		return false, err
	}
	var items []domain.ChangeItem
	for _, p := range providers {
		items = append(items, createNode(domain.ItemID("provider-"+p.Name), llmcfg.ProviderKey(p.Name), llmcfg.NodeTypeProvider, p.Props()))
	}
	for _, m := range models {
		items = append(items, createNode(domain.ItemID("model-"+m.Provider+"/"+m.Model), m.Key(), llmcfg.NodeTypeModel, m.Props()))
	}
	for _, a := range aliases {
		items = append(items, createNode(domain.ItemID("alias-"+a.Alias), llmcfg.AliasKey(a.Alias), llmcfg.NodeTypeAlias, a.Props()))
	}
	if len(items) == 0 {
		return false, nil
	}
	return true, applyOn(ctx, g, llmcfg.NamespacePlatform, "Model gateway configuration", items)
}
