package mcpsvc

import (
	"context"
	"errors"
	"fmt"
	"time"

	connectorv1 "github.com/zimwip/goap/gen/goap/connector/v1"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/mcp"
)

// DefaultLease is how long a connector registration stays live without renewal.
const DefaultLease = 30 * time.Second

// Invoker calls the ConnectorService of a registered connector.
type Invoker interface {
	Invoke(ctx context.Context, endpoint string, req *connectorv1.InvokeRequest) (*connectorv1.InvokeResponse, error)
}

// Service holds the logic of the hub.
type Service struct {
	Store Store
	// Directory reads the unit hierarchy, the MCPs and the adapters from the graph.
	Directory *Directory
	Invoker   Invoker
	// Secrets resolves a secret reference of a binding ("<vault path>#<field>" or "env:<VAR>").
	Secrets func(ctx context.Context, ref string) (string, error)
	// Lease is the validity of a registration (default DefaultLease).
	Lease time.Duration
	Now   func() time.Time
}

func (s *Service) lease() time.Duration {
	if s.Lease > 0 {
		return s.Lease
	}
	return DefaultLease
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

// RegisterConnector records (or renews) a connector and returns its lease.
func (s *Service) RegisterConnector(ctx context.Context, info *connectorv1.ConnectorInfo, endpoint string) (time.Duration, error) {
	if info == nil || !mcp.ValidName(info.Id) {
		return 0, fmt.Errorf("connector id must be a lowercase name: %w", mcp.ErrInvalid)
	}
	if endpoint == "" {
		return 0, fmt.Errorf("connector %s: endpoint is required: %w", info.Id, mcp.ErrInvalid)
	}
	for _, op := range info.Operations {
		if op.Name == "" {
			return 0, fmt.Errorf("connector %s: an operation has no name: %w", info.Id, mcp.ErrInvalid)
		}
	}
	return s.lease(), s.Store.SaveConnector(ctx, ConnectorReg{Info: info, Endpoint: endpoint, LastSeen: s.now()})
}

// ConnectorView is a registration and whether it is live.
type ConnectorView struct {
	ConnectorReg
	Live bool
}

// Connectors lists the registrations.
func (s *Service) Connectors(ctx context.Context) ([]ConnectorView, error) {
	regs, err := s.Store.Connectors(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ConnectorView, len(regs))
	for i, r := range regs {
		out[i] = ConnectorView{ConnectorReg: r, Live: s.live(r)}
	}
	return out, nil
}

func (s *Service) live(r ConnectorReg) bool { return s.now().Sub(r.LastSeen) <= s.lease() }

// CheckAdapter validates an adapter against the MCP it implements and reports what does
// not match the connector currently registered. Errors are blocking, warnings are not.
func (s *Service) CheckAdapter(ctx context.Context, a mcp.Adapter) (warnings []string, err error) {
	snap, err := s.Directory.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	def, ok := snap.Def(a.MCP)
	if !ok {
		return nil, fmt.Errorf("unknown MCP %q (declare it in the platform namespace): %w", a.MCP, mcp.ErrInvalid)
	}
	if err := a.Validate(def); err != nil {
		return nil, err
	}
	reg, err := s.Store.Connector(ctx, a.Connector)
	if errors.Is(err, ErrNotFound) {
		return []string{fmt.Sprintf("connector %s is not registered: operations and parameters not checked", a.Connector)}, nil
	} else if err != nil {
		return nil, err
	}
	ops := map[string]bool{}
	for _, o := range reg.Info.Operations {
		ops[o.Name] = true
	}
	for _, m := range a.Tools {
		if !ops[m.Operation] {
			warnings = append(warnings, fmt.Sprintf("connector %s has no operation %s (tool %s)", a.Connector, m.Operation, m.Tool))
		}
	}
	if schema := pbconv.Map(reg.Info.ConfigSchema); schema != nil {
		if req, _ := schema["required"].([]any); req != nil {
			for _, r := range req {
				if name, _ := r.(string); name != "" {
					if _, ok := a.Config[name]; !ok {
						warnings = append(warnings, fmt.Sprintf("connector %s needs the parameter %q", a.Connector, name))
					}
				}
			}
		}
	}
	declared := map[string]bool{}
	for _, n := range reg.Info.SecretNames {
		declared[n] = true
		if _, ok := a.Secrets[n]; !ok {
			warnings = append(warnings, fmt.Sprintf("connector %s needs the secret %q", a.Connector, n))
		}
	}
	for n := range a.Secrets {
		if !declared[n] {
			warnings = append(warnings, fmt.Sprintf("connector %s declares no secret %q", a.Connector, n))
		}
	}
	return warnings, nil
}

// Tool is a tool available to a unit.
type Tool = mcp.ToolInfo

// Effective lists the MCPs a unit can use with their resolved adapters, and the unit chain
// the adapters were looked up along (nearest first).
func (s *Service) Effective(ctx context.Context, unit string) (chain []string, out []Effective, err error) {
	snap, err := s.Directory.Snapshot(ctx)
	if err != nil {
		return nil, nil, err
	}
	return snap.Chain(unit), snap.Effective(unit), nil
}

// MCPs lists the MCP definitions of the platform namespace.
func (s *Service) MCPs(ctx context.Context) ([]mcp.Def, error) {
	snap, err := s.Directory.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return snap.Defs(), nil
}

// Tools lists the tools a unit can use (the ones its resolved adapters map) and the MCPs.
func (s *Service) Tools(ctx context.Context, unit string) (tools []Tool, mcps []string, err error) {
	_, eff, err := s.Effective(ctx, unit)
	if err != nil {
		return nil, nil, err
	}
	for _, e := range eff {
		mcps = append(mcps, e.MCP.Name)
		for _, t := range e.MCP.Tools {
			if _, ok := e.Adapter.Mapping(t.Name); ok {
				tools = append(tools, Tool{Name: mcp.ToolName(e.MCP.Name, t.Name), Description: t.Description, InputSchema: t.InputSchema})
			}
		}
	}
	return tools, mcps, nil
}

// Call runs a tool ("<mcp>/<tool>") for the unit holding a change: the nearest adapter of the MCP,
// its mapping, the connector operation. A failure reported by the connector is a *ToolError.
func (s *Service) Call(ctx context.Context, org, name string, args map[string]any) (map[string]any, error) {
	mcpName, tool, err := mcp.SplitTool(name)
	if err != nil {
		return nil, err
	}
	snap, err := s.Directory.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	b, _, ok := snap.Resolve(org, mcpName)
	if !ok {
		return nil, fmt.Errorf("mcp %s has no adapter for %s or its ancestors: %w", mcpName, domain.OrgOf(org), ErrNotBound)
	}
	m, ok := b.Mapping(tool)
	if !ok {
		return nil, fmt.Errorf("tool %s is not mapped by the adapter of %s in %s: %w", name, b.MCP, b.Unit, ErrNotFound)
	}
	reg, err := s.Store.Connector(ctx, b.Connector)
	if errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("connector %s is not registered: %w", b.Connector, ErrUnavailable)
	} else if err != nil {
		return nil, err
	}
	if !s.live(reg) {
		return nil, fmt.Errorf("connector %s has not renewed its registration since %s: %w", b.Connector, reg.LastSeen.Format(time.RFC3339), ErrUnavailable)
	}
	secrets, err := s.resolveSecrets(ctx, b, reg.Info)
	if err != nil {
		return nil, err
	}
	resp, err := s.Invoker.Invoke(ctx, reg.Endpoint, &connectorv1.InvokeRequest{
		Operation: m.Operation, Arguments: pbconv.Struct(mcp.MapArguments(m.Arguments, args)),
		Config: pbconv.Struct(b.Config), Secrets: secrets, OrgId: org,
	})
	if err != nil {
		return nil, fmt.Errorf("connector %s: %w: %w", b.Connector, ErrUnavailable, err)
	}
	if resp.IsError {
		return nil, &ToolError{Msg: resp.Error}
	}
	out, err := mcp.Pick(pbconv.Map(resp.Result), m.ResultPath)
	if err != nil {
		return nil, fmt.Errorf("tool %s: %w", name, err)
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

// resolveSecrets resolves the bound secrets the connector declares (no other one leaves the hub).
func (s *Service) resolveSecrets(ctx context.Context, b mcp.Adapter, info *connectorv1.ConnectorInfo) (map[string]string, error) {
	out := map[string]string{}
	for _, name := range info.SecretNames {
		ref, ok := b.Secrets[name]
		if !ok || s.Secrets == nil {
			continue
		}
		v, err := s.Secrets(ctx, ref)
		if err != nil {
			return nil, fmt.Errorf("secret %s of the adapter of %s in %s: %w", name, b.MCP, b.Unit, err)
		}
		out[name] = v
	}
	return out, nil
}
