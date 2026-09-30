package graphsvc

import (
	"context"
	"encoding/json"
	"errors"
	"maps"

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

// applyOn commits node edits on main as one change of a namespace, so that the head of main
// moves (baselines made outside a change do not once main has a head). The graph gets an
// empty initial baseline when it has none.
func applyOn(ctx context.Context, g *graph.Graph, namespace, title string, edits []graph.NodeEdit) error {
	head, err := g.BranchHead(ctx, namespace, domain.MainBranch)
	if errors.Is(err, graph.ErrNotFound) {
		head, err = g.CreateBaseline(ctx, namespace, "Initial baseline", nil)
	}
	if err != nil {
		return err
	}
	_, err = g.Commit(ctx, graph.Commit{Namespace: namespace, Title: title, Intent: title, Baseline: head.ID, By: "graphsvc.seed", BaselineName: title, Edits: edits})
	return err
}

func createNode(key, typ string, props map[string]any) graph.NodeEdit {
	return graph.NodeEdit{Key: key, Type: typ, Props: props, Rationale: "Seed " + key}
}

// linkTo links a created node to an existing node of the graph.
func linkTo(e graph.NodeEdit, typ string, to domain.NodeRef) graph.NodeEdit {
	e.Links = append(e.Links, graph.LinkEdit{Type: typ, To: &to})
	return e
}

// SeedDefaults makes sure, at every start, that the default organisation (the root of every
// unit's adapter resolution), the root project, the document-repository MCP and its localfs adapter
// definition exist. It is idempotent: once the default organisation exists nothing is touched, so that
// edited or deleted MCPs stay so.
func SeedDefaults(ctx context.Context, g *graph.Graph) (bool, error) {
	if _, err := g.NodeByKey(ctx, mcp.NamespaceOrganisation, domain.DefaultOrg); err == nil {
		return false, nil
	} else if !errors.Is(err, graph.ErrNotFound) {
		return false, err
	}
	if err := applyOn(ctx, g, mcp.NamespaceOrganisation, "Default organisation", []graph.NodeEdit{
		createNode(domain.DefaultOrg, mcp.NodeTypeOrgUnit, map[string]any{"name": "Default organisation", "kind": "company",
			"description": "Holds the changes that name no unit, and the adapters every unit inherits.", access.PropDefaultUnit: true}),
	}); err != nil {
		return false, err
	}
	if err := seedRootProject(ctx, g); err != nil {
		return false, err
	}
	d := documentRepository()
	a := localFSAdapterDef()
	err := applyOn(ctx, g, mcp.NamespacePlatform, "MCP "+d.Name, []graph.NodeEdit{
		createNode(mcp.MCPKey(d.Name), mcp.NodeTypeMCP, d.Props()),
		createNode(mcp.AdapterDefKey(a.Name), mcp.NodeTypeAdapterDef, a.Props()),
	})
	return err == nil, err
}

// seedRootProject creates the root project (ADR 0039), linked project_part_of to itself: every project not
// folded into another one resolves to it, the same way ORG-DEFAULT is the root of adapter resolution. A
// self-link cannot be made in the same commit as the node it targets (the commit orderer refuses a cycle
// among created nodes), so this writes the node, then a second edit adding the link to its own new version.
func seedRootProject(ctx context.Context, g *graph.Graph) error {
	if _, err := g.NodeByKey(ctx, mcp.NamespaceOrganisation, domain.DefaultProject); err == nil {
		return nil
	} else if !errors.Is(err, graph.ErrNotFound) {
		return err
	}
	if err := applyOn(ctx, g, mcp.NamespaceOrganisation, "Root project", []graph.NodeEdit{
		createNode(domain.DefaultProject, access.NodeTypeProjectUnit, access.ProjectUnit{
			Name: "Root project", Status: "active", Description: "Every project not folded into another one resolves to it.",
		}.Props()),
	}); err != nil {
		return err
	}
	root, err := g.NodeByKey(ctx, mcp.NamespaceOrganisation, domain.DefaultProject)
	if err != nil {
		return err
	}
	return applyOn(ctx, g, mcp.NamespaceOrganisation, "Root project is its own parent", []graph.NodeEdit{
		linkTo(graph.NodeEdit{Pre: ptr(root.Ref()), Rationale: "Root project is its own parent"}, access.LinkProjectPartOf, root.Ref()),
	})
}

func ptr[T any](v T) *T { return &v }

// SeedUnit creates an organisational unit, under parent when it is not empty.
// SeedUnit creates an OrgUnit, part_of parent (domain.DefaultOrg when parent is empty: every unit but the
// root itself needs one, ADR 0040).
func SeedUnit(ctx context.Context, g *graph.Graph, key, name, kind, parent string) error {
	unit := createNode(key, mcp.NodeTypeOrgUnit, map[string]any{"name": name, "kind": kind})
	p, err := g.NodeByKey(ctx, mcp.NamespaceOrganisation, domain.OrgOf(parent))
	if err != nil {
		return err
	}
	unit = linkTo(unit, mcp.LinkPartOf, p.Ref())
	return applyOn(ctx, g, mcp.NamespaceOrganisation, "Unit "+key, []graph.NodeEdit{unit})
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
	return applyOn(ctx, g, mcp.NamespaceOrganisation, "Adapter "+a.MCP+" of "+a.Unit, []graph.NodeEdit{
		linkTo(createNode(mcp.AdapterKey(a.Unit, a.MCP), mcp.NodeTypeAdapter, a.Props()), mcp.LinkOwner, unit.Ref()),
	})
}

// SeedAdapterDef creates the AdapterDef node of an adapter definition in the platform namespace.
func SeedAdapterDef(ctx context.Context, g *graph.Graph, d mcp.AdapterDef) error {
	return applyOn(ctx, g, mcp.NamespacePlatform, "Adapter "+d.Name, []graph.NodeEdit{createNode(mcp.AdapterDefKey(d.Name), mcp.NodeTypeAdapterDef, d.Props())})
}

// SeedAccess makes sure the default policies exist as Policy nodes of the organisation namespace. It is
// idempotent: once the floor policy exists nothing is touched, so that edited or deleted policies stay so.
func SeedAccess(ctx context.Context, g *graph.Graph) (bool, error) {
	if _, err := g.NodeByKey(ctx, mcp.NamespaceOrganisation, access.PolicyKey(authz.FloorPolicies[0])); err == nil {
		return false, nil
	} else if !errors.Is(err, graph.ErrNotFound) {
		return false, err
	}
	items := make([]graph.NodeEdit, len(authz.DefaultPolicies))
	for i, p := range authz.DefaultPolicies {
		items[i] = createNode(access.PolicyKey(p), access.NodeTypePolicy, access.PolicyProps(p))
	}
	return true, applyOn(ctx, g, mcp.NamespaceOrganisation, "Default policies", items)
}

// SeedUser creates the User node of a subject, member of a unit (DefaultUnit when u.Unit is empty:
// member_of is exactly one link, ADR 0040, never left unset).
func SeedUser(ctx context.Context, g *graph.Graph, u access.User) error {
	user := createNode(access.UserKey(u.Subject), access.NodeTypeUser, u.Props())
	var unit domain.Node
	var err error
	if u.Unit == "" {
		unit, err = DefaultUnit(ctx, g)
	} else {
		unit, err = g.NodeByKey(ctx, mcp.NamespaceOrganisation, u.Unit)
	}
	if err != nil {
		return err
	}
	user = linkTo(user, access.LinkMemberOf, unit.Ref())
	return applyOn(ctx, g, mcp.NamespaceOrganisation, "User "+u.Subject, []graph.NodeEdit{user})
}

// SeedPolicy creates a Policy node.
func SeedPolicy(ctx context.Context, g *graph.Graph, p authz.Policy) error {
	return applyOn(ctx, g, mcp.NamespaceOrganisation, "Policy "+p.Resource+"/"+p.Action, []graph.NodeEdit{
		createNode(access.PolicyKey(p), access.NodeTypePolicy, access.PolicyProps(p))})
}

// SeedModels creates the model gateway configuration (providers, models, aliases; nodes of the platform namespace)
// when the graph holds no provider yet, so that providers or models deleted on purpose stay so. It reports
// whether it seeded.
func SeedModels(ctx context.Context, g *graph.Graph, providers []llmcfg.Provider, models []llmcfg.Model, aliases []llmcfg.Alias) (bool, error) {
	head, err := g.BranchHead(ctx, llmcfg.NamespacePlatform, domain.MainBranch)
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
	var items []graph.NodeEdit
	for _, p := range providers {
		items = append(items, createNode(llmcfg.ProviderKey(p.Name), llmcfg.NodeTypeProvider, p.Props()))
	}
	for _, m := range models {
		items = append(items, createNode(m.Key(), llmcfg.NodeTypeModel, m.Props()))
	}
	for _, a := range aliases {
		items = append(items, createNode(llmcfg.AliasKey(a.Alias), llmcfg.NodeTypeAlias, a.Props()))
	}
	if len(items) == 0 {
		return false, nil
	}
	return true, applyOn(ctx, g, llmcfg.NamespacePlatform, "Model gateway configuration", items)
}

// SeedBuiltins makes sure, at every start, that the built-in MCPs and their adapter definitions exist
// and match the code (ADR 0028): like the built-in domains they ship with the platform. The first
// time a built-in MCP is seeded the default organisation gets an instance of its adapter, so that
// every unit can use it; the instance is never recreated afterwards, so that a unit restricting or an
// administrator removing it stays so. It reports whether it wrote anything; SeedDefaults runs first.
func SeedBuiltins(ctx context.Context, g *graph.Graph) (bool, error) {
	head, err := g.BranchHead(ctx, mcp.NamespacePlatform, domain.MainBranch)
	if err != nil && !errors.Is(err, graph.ErrNotFound) {
		return false, err
	}
	current := map[string]domain.Node{}
	if head.ID != "" {
		nodes, _, err := g.BaselineGraph(ctx, head.ID)
		if err != nil {
			return false, err
		}
		for _, n := range nodes {
			if !n.Deleted {
				current[n.Key] = n
			}
		}
	}
	var edits []graph.NodeEdit
	var fresh []string
	sync := func(key, typ string, props map[string]any) {
		n, ok := current[key]
		switch {
		case !ok:
			edits = append(edits, createNode(key, typ, props))
		case !sameProps(n.Properties, props):
			pre := n.Ref()
			set := maps.Clone(props)
			for k := range n.Properties {
				if _, keep := props[k]; !keep {
					set[k] = nil // properties are merged: clear the ones the code dropped
				}
			}
			edits = append(edits, graph.NodeEdit{Pre: &pre, Props: set, Rationale: "Built-in " + key + " follows the platform"})
		}
	}
	defs := mcp.BuiltinAdapterDefs()
	for i, d := range mcp.BuiltinDefs() {
		if _, ok := current[mcp.MCPKey(d.Name)]; !ok {
			fresh = append(fresh, d.Name)
		}
		sync(mcp.MCPKey(d.Name), mcp.NodeTypeMCP, d.Props())
		sync(mcp.AdapterDefKey(defs[i].Name), mcp.NodeTypeAdapterDef, defs[i].Props())
	}
	if len(edits) > 0 {
		if err := applyOn(ctx, g, mcp.NamespacePlatform, "Built-in MCPs", edits); err != nil {
			return false, err
		}
	}
	if len(fresh) == 0 {
		return len(edits) > 0, nil
	}
	org, err := g.NodeByKey(ctx, mcp.NamespaceOrganisation, domain.DefaultOrg)
	if err != nil {
		return false, err
	}
	var instances []graph.NodeEdit
	for _, name := range fresh {
		a := mcp.BuiltinAdapter(domain.DefaultOrg, name)
		if _, err := g.NodeByKey(ctx, mcp.NamespaceOrganisation, mcp.AdapterKey(a.Unit, a.MCP)); err == nil {
			continue
		} else if !errors.Is(err, graph.ErrNotFound) {
			return false, err
		}
		instances = append(instances, linkTo(createNode(mcp.AdapterKey(a.Unit, a.MCP), mcp.NodeTypeAdapter, a.Props()), mcp.LinkOwner, org.Ref()))
	}
	if len(instances) > 0 {
		if err := applyOn(ctx, g, mcp.NamespaceOrganisation, "Built-in adapters of the default organisation", instances); err != nil {
			return false, err
		}
	}
	return true, nil
}

// sameProps reports whether the properties of a node hold the wanted ones (compared as JSON).
func sameProps(have, want map[string]any) bool {
	a, _ := json.Marshal(have)
	b, _ := json.Marshal(want)
	return string(a) == string(b)
}
