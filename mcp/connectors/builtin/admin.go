package builtin

import (
	"context"
	"sort"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	connectorv1 "github.com/zimwip/goap/contracts/gen/goap/connector/v1"
	registryv1 "github.com/zimwip/goap/contracts/gen/goap/registry/v1"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/adapter"
	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/mcpbuiltin"
	"github.com/zimwip/goap/mcp/connectorkit"
	"github.com/zimwip/goap/mcp/mcpsvc"
)

// Hub is the part of the MCP hub goap-admin reads (*mcpsvc.Service).
type Hub interface {
	Effective(ctx context.Context, unit string) (chain []string, out []mcpsvc.Effective, err error)
	Connectors(ctx context.Context) ([]mcpsvc.ConnectorView, error)
	ConnectorOf(ctx context.Context, a adapter.Instance) string
}

// Domains lists the domains in force: the domain registry (*domainsvc.Service in process, *domainsvc.Client).
type Domains interface {
	Domains(ctx context.Context) ([]*def.Domain, error)
}

// Methodologies lists the methodologies of the methodology registry and gives the compiled view of a version: its
// client (registryv1connect), or its handler in process. The hub reads them through the contract, never compiled (ADR
// 0125 §4, ADR 0126 step 5).
type Methodologies interface {
	ListMethodologies(ctx context.Context, r *connect.Request[registryv1.ListMethodologiesRequest]) (*connect.Response[registryv1.ListMethodologiesResponse], error)
	GetMethodologyView(ctx context.Context, r *connect.Request[registryv1.GetMethodologyViewRequest]) (*connect.Response[registryv1.GetMethodologyViewResponse], error)
}

// inForce lists the published methodologies the registry does not suspend (ADR 0125 §3).
func inForce(ctx context.Context, m Methodologies) ([]*registryv1.MethodologySummary, error) {
	r, err := m.ListMethodologies(ctx, connect.NewRequest(&registryv1.ListMethodologiesRequest{}))
	if err != nil {
		return nil, err
	}
	var out []*registryv1.MethodologySummary
	for _, s := range r.Msg.Methodologies {
		if s.Status == string(def.StatusPublished) && s.Suspended == "" {
			out = append(out, s)
		}
	}
	return out, nil
}

// Admin is the goap-admin connector: it describes the platform (who, with what, what, how). It
// changes nothing: the organisation, the adapters and the methodologies are graph data, changed
// through changes (goap-change).
type Admin struct{ p Ports }

var _ connectorkit.Connector = Admin{}

var adminOps = []op{
	{"units", "List the organisational units: {units}", schema(map[string]string{})},
	{"users", "List the users: {users}", schema(map[string]string{"unit": "string"})},
	{"mcps", "List the MCPs a unit can use: {unit, chain, mcps}", schema(map[string]string{"unit": "string"})},
	{"connectors", "List the registered connectors: {connectors}", schema(map[string]string{})},
	{"domains", "List the published domains: {domains}", schema(map[string]string{})},
	{"methodologies", "List the published methodologies: {methodologies}", schema(map[string]string{})},
}

// Info implements connectorkit.Connector.
func (Admin) Info() *connectorv1.ConnectorInfo {
	return info(mcpbuiltin.Admin, "Description of the platform: organisation, users, MCPs, connectors, domains, methodologies (built in).", adminOps)
}

// resource of each operation, read-authorized for the caller
var adminResource = map[string]string{"units": "unit", "users": access.ResourcePolicy, "mcps": "adapter", "connectors": "connector",
	"domains": "domain", "methodologies": "methodology"}

// Invoke implements connectorkit.Connector.
func (c Admin) Invoke(ctx context.Context, op string, raw, _ map[string]any, _ map[string]string) (map[string]any, error) {
	who, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	res, ok := adminResource[op]
	if !ok {
		return nil, unknown(op)
	}
	if err := authz.Check(ctx, c.p.Authz, authz.Request{Subject: who, Action: "read", Resource: authz.Resource{Type: res, Org: who.Org}}); err != nil {
		return nil, err
	}
	a := args(raw)
	switch op {
	case "units", "users":
		return c.organisation(ctx, op, a.str("unit"))
	case "mcps":
		unit := a.unit(ctx, who)
		if unit == "" { // the root of the organisation, as the structures name it
			st, err := c.p.Graph.Structures(ctx)
			if err != nil {
				return nil, err
			}
			unit = st.Organisation().Root
		}
		chain, eff, err := c.p.Hub.Effective(ctx, unit)
		if err != nil {
			return nil, err
		}
		list := []map[string]any{}
		for _, e := range eff {
			tools := []string{}
			for _, t := range e.Allowed().Tools {
				tools = append(tools, t.Name)
			}
			list = append(list, map[string]any{"mcp": e.MCP.Name, "description": e.MCP.Description, "builtin": mcpbuiltin.Is(e.MCP.Name),
				"adapter": e.Adapter.Adapter, "definedIn": e.Adapter.Unit, "inherited": e.Inherited, "connector": c.p.Hub.ConnectorOf(ctx, e.Adapter),
				"tools": tools, "restrictedBy": e.Restriction.By, "disabled": e.Restriction.Disabled})
		}
		return result(map[string]any{"unit": unit, "chain": chain, "mcps": list})
	case "connectors":
		cs, err := c.p.Hub.Connectors(ctx)
		if err != nil {
			return nil, err
		}
		list := []map[string]any{}
		for _, r := range cs {
			ops := []string{}
			for _, o := range r.Info.GetOperations() {
				ops = append(ops, o.Name)
			}
			list = append(list, map[string]any{"id": r.Info.GetId(), "version": r.Info.GetVersion(), "description": r.Info.GetDescription(),
				"operations": ops, "live": r.Live, "lastSeen": r.LastSeen, "builtin": mcpbuiltin.Is(r.Info.GetId())})
		}
		return result(map[string]any{"connectors": list})
	case "domains":
		ds, err := c.p.Domains.Domains(ctx)
		if err != nil {
			return nil, err
		}
		list := []map[string]any{}
		for _, d := range ds {
			types := []string{}
			for _, t := range d.NodeTypes {
				types = append(types, d.Name+"@"+t.Name)
			}
			list = append(list, map[string]any{"name": d.Name, "version": d.Version, "description": d.Description, "nodeTypes": types})
		}
		return result(map[string]any{"domains": list})
	case "methodologies":
		ms, err := inForce(ctx, c.p.Methodologies)
		if err != nil {
			return nil, err
		}
		list := []map[string]any{}
		for _, m := range ms {
			agents := []map[string]any{}
			for _, ag := range m.GetAgents() {
				agents = append(agents, map[string]any{"name": ag.Name, "description": ag.Description, "planner": ag.Planner})
			}
			list = append(list, map[string]any{"name": m.Name, "version": m.Version, "description": m.Description, "namespace": m.Namespace, "agents": agents})
		}
		return result(map[string]any{"methodologies": list})
	}
	return nil, unknown(op)
}

// organisation lists the units or the users of the head of the organisation namespace: the units are the nodes of the
// organisation structure the graph names (ADR 0054), their hierarchy and the root every unit hangs from are read
// through domain.Hierarchy, the one resolver.
func (c Admin) organisation(ctx context.Context, op, unit string) (map[string]any, error) {
	st, err := c.p.Graph.Structures(ctx)
	if err != nil {
		return nil, err
	}
	head, err := c.p.Graph.BranchHead(ctx, st.Organisation().Namespace, domain.MainBranch)
	if err != nil {
		return nil, err
	}
	nodes, links, err := c.p.Graph.CommitGraph(ctx, head.ID)
	if err != nil {
		return nil, err
	}
	units := st.Hierarchy(domain.StructureOrganisation, nodes, links)
	byRef := map[domain.NodeRef]domain.Node{}
	for _, n := range nodes {
		byRef[n.Ref()] = n
	}
	member := map[string]string{}
	for _, l := range links {
		if from := byRef[l.From]; l.Type == access.LinkMemberOf && from.Type == access.NodeTypeUser {
			member[from.Key] = byRef[l.To].Key
		}
	}
	list := []map[string]any{}
	for _, n := range nodes {
		if n.Deleted {
			continue
		}
		switch {
		case op == "units" && n.Type == st.Organisation().Type:
			u := map[string]any{"key": n.Key, "name": n.Properties["name"], "kind": n.Properties["kind"]}
			if p := units.Parent(n.Key); p != "" {
				u["parent"] = p
			} else if n.Key != units.Root() {
				u["parent"] = units.Root() // the implicit root of every unit
			}
			list = append(list, u)
		case op == "users" && n.Type == access.NodeTypeUser:
			if unit != "" && member[n.Key] != unit {
				continue
			}
			usr, err := access.UserFromProps(n.Properties)
			if err != nil {
				continue
			}
			list = append(list, map[string]any{"subject": usr.Subject, "displayName": usr.DisplayName, "email": usr.Email, "unit": member[n.Key]})
		}
	}
	sort.Slice(list, func(i, j int) bool { return sortKey(list[i]) < sortKey(list[j]) })
	return result(map[string]any{op: list})
}

func sortKey(m map[string]any) string {
	if k, ok := m["key"].(string); ok {
		return k
	}
	s, _ := m["subject"].(string)
	return s
}
