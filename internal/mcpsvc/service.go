package mcpsvc

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	connectorv1 "github.com/zimwip/goap/gen/goap/connector/v1"
	"github.com/zimwip/goap/internal/pbconv"
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
	Store   Store
	Invoker Invoker
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

// SaveMcp validates and stores a generic MCP definition.
func (s *Service) SaveMcp(ctx context.Context, d mcp.Def) error {
	if err := d.Validate(); err != nil {
		return err
	}
	return s.Store.SaveMcp(ctx, d)
}

// SaveAdapter validates an adapter against its MCP and stores it. Warnings report
// what cannot be checked or does not match the connector currently registered.
func (s *Service) SaveAdapter(ctx context.Context, a mcp.Adapter) (warnings []string, err error) {
	def, err := s.Store.Mcp(ctx, a.MCP)
	if err != nil {
		return nil, err
	}
	if err := a.Validate(def); err != nil {
		return nil, err
	}
	reg, err := s.Store.Connector(ctx, a.Connector)
	switch {
	case errors.Is(err, ErrNotFound):
		warnings = append(warnings, fmt.Sprintf("connector %s is not registered: operations not checked", a.Connector))
	case err != nil:
		return nil, err
	default:
		ops := map[string]bool{}
		for _, o := range reg.Info.Operations {
			ops[o.Name] = true
		}
		for _, m := range a.Tools {
			if !ops[m.Operation] {
				warnings = append(warnings, fmt.Sprintf("connector %s has no operation %s (tool %s)", a.Connector, m.Operation, m.Tool))
			}
		}
	}
	return warnings, s.Store.SaveAdapter(ctx, a)
}

// Bind attaches an MCP to a connector for an organization; the adapter must exist.
func (s *Service) Bind(ctx context.Context, b mcp.Binding) error {
	if b.OrgID == "" || !mcp.ValidName(b.MCP) || !mcp.ValidName(b.Connector) {
		return fmt.Errorf("binding needs an organization, an mcp and a connector: %w", mcp.ErrInvalid)
	}
	return s.Store.SaveBinding(ctx, b)
}

// Tool is a tool available to an organization.
type Tool = mcp.ToolInfo

// Tools lists the tools of the MCPs bound by the organization (only those its adapter maps)
// and the bound MCPs.
func (s *Service) Tools(ctx context.Context, org string) (tools []Tool, mcps []string, err error) {
	bs, err := s.Store.Bindings(ctx, org)
	if err != nil {
		return nil, nil, err
	}
	for _, b := range bs {
		def, err := s.Store.Mcp(ctx, b.MCP)
		if err != nil {
			return nil, nil, err
		}
		ad, err := s.Store.Adapter(ctx, b.MCP, b.Connector)
		if err != nil {
			return nil, nil, err
		}
		mcps = append(mcps, b.MCP)
		for _, t := range def.Tools {
			if _, ok := ad.Mapping(t.Name); ok {
				tools = append(tools, Tool{Name: mcp.ToolName(b.MCP, t.Name), Description: t.Description, InputSchema: t.InputSchema})
			}
		}
	}
	sort.Strings(mcps)
	return tools, mcps, nil
}

// BoundMCPs returns the names of the MCPs the organization binds.
func (s *Service) BoundMCPs(ctx context.Context, org string) ([]string, error) {
	bs, err := s.Store.Bindings(ctx, org)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = b.MCP
	}
	return out, nil
}

// Call runs a tool ("<mcp>/<tool>") for an organization: binding, adapter mapping,
// connector operation. A failure reported by the connector is a *ToolError.
func (s *Service) Call(ctx context.Context, org, name string, args map[string]any) (map[string]any, error) {
	mcpName, tool, err := mcp.SplitTool(name)
	if err != nil {
		return nil, err
	}
	b, err := s.Store.Binding(ctx, org, mcpName)
	if errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("mcp %s is not bound in organization %s: %w", mcpName, org, ErrNotBound)
	} else if err != nil {
		return nil, err
	}
	ad, err := s.Store.Adapter(ctx, b.MCP, b.Connector)
	if err != nil {
		return nil, err
	}
	m, ok := ad.Mapping(tool)
	if !ok {
		return nil, fmt.Errorf("tool %s is not mapped by the adapter %s/%s: %w", name, b.MCP, b.Connector, ErrNotFound)
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
func (s *Service) resolveSecrets(ctx context.Context, b mcp.Binding, info *connectorv1.ConnectorInfo) (map[string]string, error) {
	out := map[string]string{}
	for _, name := range info.SecretNames {
		ref, ok := b.Secrets[name]
		if !ok || s.Secrets == nil {
			continue
		}
		v, err := s.Secrets(ctx, ref)
		if err != nil {
			return nil, fmt.Errorf("secret %s of %s/%s: %w", name, b.OrgID, b.MCP, err)
		}
		out[name] = v
	}
	return out, nil
}
