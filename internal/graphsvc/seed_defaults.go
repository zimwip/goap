package graphsvc

import (
	"context"
	"encoding/json"
	"errors"
	"maps"

	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/adapter"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/criticality"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/llmcfg"
	"github.com/zimwip/goap/pkg/mcp"
	"github.com/zimwip/goap/pkg/mcpbuiltin"
)

// SeedChange commits node edits on main as one change of a namespace, so that the head of main moves: the first change
// of a namespace starts from the empty state (ADR 0056).
func SeedChange(ctx context.Context, g *graph.Graph, namespace, title string, edits []graph.NodeEdit) error {
	if authz.From(ctx).Anonymous() { // a seed acts as the graph service itself
		ctx = System(ctx)
	}
	_, err := g.Commit(ctx, graph.Commit{Namespace: namespace, Title: title, Intent: title, By: "graphsvc.seed", BaselineName: title, Edits: edits,
		ProjectID: g.Structure(domain.StructureProject).Root}) // a seed acts in the root project (ADR 0091)
	return err
}

func SeedNode(key, typ string, props map[string]any) graph.NodeEdit {
	return graph.NodeEdit{Key: key, Type: typ, Props: props, Rationale: "Seed " + key}
}

// linkTo links a created node to an existing node of the graph.
func linkTo(e graph.NodeEdit, typ string, to domain.NodeRef) graph.NodeEdit {
	e.Links = append(e.Links, graph.LinkEdit{Type: typ, To: &to})
	return e
}

// SeedUnit creates a unit of the organisation, a child of parent (the root unit when parent is empty: every unit but the
// root itself needs one, ADR 0040).
func SeedUnit(ctx context.Context, g *graph.Graph, key, name, kind, parent string) error {
	unit := SeedNode(key, access.NodeTypeOrgUnit, map[string]any{"name": name, "kind": kind})
	if parent == "" {
		parent = g.Structure(domain.StructureOrganisation).Root
	}
	p, err := g.NodeByKey(ctx, access.NamespaceOrganisation, parent)
	if err != nil {
		return err
	}
	unit = linkTo(unit, access.LinkPartOf, p.Ref())
	return SeedChange(ctx, g, access.NamespaceOrganisation, "Unit "+key, []graph.NodeEdit{unit})
}

// SeedAdapter creates the Adapter node of a unit, owned by it (ADR 0054: the owner of its versions).
func SeedAdapter(ctx context.Context, g *graph.Graph, a adapter.Instance) error {
	return SeedChange(ctx, g, access.NamespaceOrganisation, "Adapter "+a.MCP+" of "+a.Unit, []graph.NodeEdit{ownedBy(SeedNode(adapter.Key(a.Unit, a.MCP), domain.TypeAdapter, a.Props()), a.Unit)})
}

// SeedCriticalityPolicy creates what a criticality level requires of the changes of a unit (ADR 0075 §3), owned by it.
func SeedCriticalityPolicy(ctx context.Context, g *graph.Graph, unit string, l criticality.Level, p criticality.Policy) error {
	return SeedChange(ctx, g, access.NamespaceOrganisation, "Criticality policy "+string(l)+" of "+unit, []graph.NodeEdit{
		ownedBy(SeedNode(access.CriticalityPolicyKey(unit, l), access.NodeTypeCriticalityPolicy, access.CriticalityProps(l, p)), unit)})
}

// ownedBy makes the unit with key unit the owner of a node an edit creates or modifies (ADR 0054).
func ownedBy(e graph.NodeEdit, unit string) graph.NodeEdit {
	e.Owner = unit
	return e
}

// SeedAdapterDef creates the AdapterDef node of an adapter definition in the platform namespace.
func SeedAdapterDef(ctx context.Context, g *graph.Graph, d adapter.Def) error {
	return SeedChange(ctx, g, domain.NamespacePlatform, "Adapter "+d.Name, []graph.NodeEdit{SeedNode(adapter.DefKey(d.Name), domain.TypeAdapterDef, d.Props())})
}

// SeedAccess makes sure the default policies exist as Policy nodes of the organisation namespace. It is
// idempotent: once the floor policy exists nothing is touched, so that edited or deleted policies stay so.
func SeedAccess(ctx context.Context, g *graph.Graph) (bool, error) {
	if _, err := g.NodeByKey(ctx, access.NamespaceOrganisation, access.PolicyKey(authz.FloorPolicies[0])); err == nil {
		return false, nil
	} else if !errors.Is(err, graph.ErrNotFound) {
		return false, err
	}
	items := make([]graph.NodeEdit, len(authz.DefaultPolicies))
	for i, p := range authz.DefaultPolicies {
		items[i] = SeedNode(access.PolicyKey(p), access.NodeTypePolicy, access.PolicyProps(p))
	}
	return true, SeedChange(ctx, g, access.NamespaceOrganisation, "Default policies", items)
}

// SeedUser creates the User node of a subject, member of a unit (NewUserUnit when u.Unit is empty:
// member_of is exactly one link, ADR 0040, never left unset).
func SeedUser(ctx context.Context, g *graph.Graph, u access.User) error {
	user := SeedNode(access.UserKey(u.Subject), access.NodeTypeUser, u.Props())
	// land it active (ADR 0048's user lifecycle): a User seeded this way (tools, tests) must be usable right
	// away, the same as one created by createUser's sign-in flow — left "proposed" it would silently never
	// count toward the admin floor until some unrelated later commit happened to touch User/Assignment.
	user.State = "active"
	var unit domain.Node
	var err error
	if u.Unit == "" {
		unit, err = NewUserUnit(ctx, g)
	} else {
		unit, err = g.NodeByKey(ctx, access.NamespaceOrganisation, u.Unit)
	}
	if err != nil {
		return err
	}
	user = linkTo(user, access.LinkMemberOf, unit.Ref())
	return SeedChange(ctx, g, access.NamespaceOrganisation, "User "+u.Subject, []graph.NodeEdit{user})
}

// SeedPolicy creates a Policy node.
func SeedPolicy(ctx context.Context, g *graph.Graph, p authz.Policy) error {
	return SeedChange(ctx, g, access.NamespaceOrganisation, "Policy "+p.Resource+"/"+p.Action, []graph.NodeEdit{
		SeedNode(access.PolicyKey(p), access.NodeTypePolicy, access.PolicyProps(p))})
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
		items = append(items, SeedNode(llmcfg.ProviderKey(p.Name), llmcfg.NodeTypeProvider, p.Props()))
	}
	for _, m := range models {
		items = append(items, SeedNode(m.Key(), llmcfg.NodeTypeModel, m.Props()))
	}
	for _, a := range aliases {
		items = append(items, SeedNode(llmcfg.AliasKey(a.Alias), llmcfg.NodeTypeAlias, a.Props()))
	}
	if len(items) == 0 {
		return false, nil
	}
	return true, SeedChange(ctx, g, llmcfg.NamespacePlatform, "Model gateway configuration", items)
}

// SeedProtectedAliases makes sure, at every start, that the protected aliases of the platform exist (ADR 0084:
// llmcfg.ProtectedAliases): SeedModels only runs on a graph holding no provider, so a graph that predates them gets them
// here. A missing one is created targeting what the default alias targets (the helper: what the fast alias targets
// when it exists), or nothing while no model is configured, in which case it resolves to nothing and is not available;
// a node of that name without the protected flag, or retired, is flagged and restored. One that exists is never
// retargeted: the target belongs to the administrators. It reports whether it wrote anything.
func SeedProtectedAliases(ctx context.Context, g *graph.Graph) (bool, error) {
	head, err := g.BranchHead(ctx, llmcfg.NamespacePlatform, domain.MainBranch)
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
			if n.Namespace == llmcfg.NamespacePlatform && n.Type == llmcfg.NodeTypeAlias && !n.Deleted {
				current[n.Key] = n
			}
		}
	}
	targetOf := func(alias string) string {
		n, ok := current[llmcfg.AliasKey(alias)]
		if !ok || n.State == llmcfg.StateRetired {
			return ""
		}
		a, err := llmcfg.AliasFromProps(n.Properties)
		if err != nil {
			return ""
		}
		return a.Target
	}
	var edits []graph.NodeEdit
	for _, name := range llmcfg.ProtectedAliases() {
		key := llmcfg.AliasKey(name)
		n, ok := current[key]
		if !ok {
			target := targetOf("default")
			if name == llmcfg.HelperAlias {
				if fast := targetOf("fast"); fast != "" {
					target = fast
				}
			}
			edits = append(edits, SeedNode(key, llmcfg.NodeTypeAlias, llmcfg.Alias{Alias: name, Target: target, Protected: true}.Props()))
			continue
		}
		if a, err := llmcfg.AliasFromProps(n.Properties); err == nil && a.Protected && n.State != llmcfg.StateRetired {
			continue
		}
		pre := n.Ref()
		e := graph.NodeEdit{Pre: &pre, Props: map[string]any{"protected": true}, Rationale: "Alias " + name + " is protected"}
		if n.State == llmcfg.StateRetired {
			e.State = "active"
		}
		edits = append(edits, e)
	}
	if len(edits) == 0 {
		return false, nil
	}
	return true, SeedChange(ctx, g, llmcfg.NamespacePlatform, "Protected model aliases", edits)
}

// SeedBehaviors gives every install the built-in example behaviour of the gateway (ADR 0093: llmcfg.TerseBehavior,
// disabled): created when no node of its key exists, never touched afterwards, so an administrator who edited, enabled
// or retired it keeps it so. It reports whether it wrote anything.
func SeedBehaviors(ctx context.Context, g *graph.Graph) (bool, error) {
	b := llmcfg.TerseBehavior()
	if _, err := g.NodeByKey(ctx, llmcfg.NamespacePlatform, llmcfg.BehaviorKey(b.Name)); err == nil {
		return false, nil
	} else if !errors.Is(err, graph.ErrNotFound) {
		return false, err
	}
	return true, SeedChange(ctx, g, llmcfg.NamespacePlatform, "Built-in LLM behaviours", []graph.NodeEdit{SeedNode(llmcfg.BehaviorKey(b.Name), llmcfg.NodeTypeBehavior, b.Props())})
}

// SeedBuiltins makes sure, at every start, that the built-in MCPs and their adapter definitions exist
// and match the code (ADR 0028): like the built-in domains they ship with the platform. The first
// time a built-in MCP is seeded the default organisation gets an instance of its adapter, so that
// every unit can use it; the instance is never recreated afterwards, so that a unit restricting or an
// administrator removing it stays so. It reports whether it wrote anything; Graph.Bootstrap runs first (Boot).
func SeedBuiltins(ctx context.Context, g *graph.Graph) (bool, error) {
	head, err := g.BranchHead(ctx, domain.NamespacePlatform, domain.MainBranch)
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
			edits = append(edits, SeedNode(key, typ, props))
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
	defs := mcpbuiltin.AdapterDefs()
	for i, d := range mcpbuiltin.Defs() {
		if _, ok := current[mcp.MCPKey(d.Name)]; !ok {
			fresh = append(fresh, d.Name)
		}
		sync(mcp.MCPKey(d.Name), mcp.NodeTypeMCP, d.Props())
		sync(adapter.DefKey(defs[i].Name), domain.TypeAdapterDef, defs[i].Props())
	}
	for _, r := range access.BuiltinRoles() {
		sync(access.RoleKey(r.Name), access.NodeTypeRole, r.Props())
	}
	if len(edits) > 0 {
		if err := SeedChange(ctx, g, domain.NamespacePlatform, "Built-in MCPs", edits); err != nil {
			return false, err
		}
	}
	if len(fresh) == 0 {
		return len(edits) > 0, nil
	}
	var instances []graph.NodeEdit
	for _, name := range fresh {
		a := mcpbuiltin.Adapter(g.Structure(domain.StructureOrganisation).Root, name)
		if _, err := g.NodeByKey(ctx, access.NamespaceOrganisation, adapter.Key(a.Unit, a.MCP)); err == nil {
			continue
		} else if !errors.Is(err, graph.ErrNotFound) {
			return false, err
		}
		instances = append(instances, ownedBy(SeedNode(adapter.Key(a.Unit, a.MCP), domain.TypeAdapter, a.Props()), a.Unit))
	}
	if len(instances) > 0 {
		if err := SeedChange(ctx, g, access.NamespaceOrganisation, "Built-in adapters of the default organisation", instances); err != nil {
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
