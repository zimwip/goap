package mcpsvc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	connectorv1 "github.com/zimwip/goap/gen/goap/connector/v1"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/pkg/algo"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/dsl"
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
	// Directory reads the unit hierarchy, the MCPs and the adapter instances from the graph.
	Directory *Directory
	// Library gives the adapter algorithms (the code of the adapters).
	Library Library
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

// adapterAlgorithm loads the algorithm of an adapter instance and checks that it is an adapter of
// the MCP the instance says it implements.
func (s *Service) adapterAlgorithm(ctx context.Context, a mcp.Adapter) (algo.Algorithm, error) {
	if s.Library == nil {
		return algo.Algorithm{}, fmt.Errorf("no adapter library configured: %w", ErrLibrary)
	}
	alg, _, err := s.Library.Algorithm(ctx, a.Domain, a.Version, a.Algorithm)
	if err != nil {
		return alg, fmt.Errorf("adapter of %s in %s: %w: %w", a.MCP, a.Unit, ErrLibrary, err)
	}
	if alg.Type != algo.UsageAdapter || alg.MCP != a.MCP {
		return alg, fmt.Errorf("algorithm %s/%s is not an adapter of the MCP %s: %w", a.Domain, a.Algorithm, a.MCP, mcp.ErrInvalid)
	}
	return alg, nil
}

// CheckAdapter validates an adapter instance before it is saved: the MCP and the algorithm must
// exist and match, and the parameter values must fit the algorithm (errors); what does not match the
// connector currently registered is reported as warnings.
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
	alg, err := s.adapterAlgorithm(ctx, a)
	if errors.Is(err, ErrLibrary) {
		return nil, fmt.Errorf("%v: %w", err, mcp.ErrInvalid)
	} else if err != nil {
		return nil, err
	}
	vals, issues := alg.Resolve(a.Params)
	if len(issues) > 0 {
		return nil, fmt.Errorf("adapter %s/%s: %s: %w", a.Domain, a.Algorithm, issues[0], mcp.ErrInvalid)
	}
	_, secrets := alg.Split(vals)
	for name, ref := range secrets {
		if !strings.HasPrefix(ref, "env:") && !strings.Contains(ref, "#") {
			warnings = append(warnings, fmt.Sprintf("secret %s: %q is neither env:<VAR> nor <vault path>#<field>", name, ref))
		}
	}
	reg, err := s.Store.Connector(ctx, alg.Connector)
	if errors.Is(err, ErrNotFound) {
		return append(warnings, fmt.Sprintf("connector %s is not registered: not checked against its operations", alg.Connector)), nil
	} else if err != nil {
		return nil, err
	}
	for _, name := range reg.Info.SecretNames {
		if _, ok := secrets[name]; !ok {
			warnings = append(warnings, fmt.Sprintf("connector %s needs the secret %q", alg.Connector, name))
		}
	}
	return warnings, nil
}

// Template generates the skeleton of the code of an adapter between an MCP and a registered
// connector, and the parameters the connector needs.
func (s *Service) Template(ctx context.Context, mcpName, connector string) (code string, params []algo.Param, err error) {
	snap, err := s.Directory.Snapshot(ctx)
	if err != nil {
		return "", nil, err
	}
	def, ok := snap.Def(mcpName)
	if !ok {
		return "", nil, errNotFound("mcp " + mcpName)
	}
	reg, err := s.Store.Connector(ctx, connector)
	if err != nil {
		return "", nil, err
	}
	var ops []mcp.Operation
	for _, o := range reg.Info.Operations {
		ops = append(ops, mcp.Operation{Name: o.Name, Description: o.Description, InputSchema: pbconv.Map(o.InputSchema)})
	}
	return mcp.AdapterTemplate(def, connector, ops), mcp.AdapterParams(pbconv.Map(reg.Info.ConfigSchema), reg.Info.SecretNames), nil
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

// ConnectorOf returns the id of the connector an adapter instance calls (from its algorithm), or ""
// when the library cannot say.
func (s *Service) ConnectorOf(ctx context.Context, a mcp.Adapter) string {
	if alg, err := s.adapterAlgorithm(ctx, a); err == nil {
		return alg.Connector
	}
	return ""
}

// Tools lists the tools of the MCPs a unit can use, and the MCPs.
func (s *Service) Tools(ctx context.Context, unit string) (tools []Tool, mcps []string, err error) {
	_, eff, err := s.Effective(ctx, unit)
	if err != nil {
		return nil, nil, err
	}
	for _, e := range eff {
		mcps = append(mcps, e.MCP.Name)
		for _, t := range e.MCP.Tools {
			tools = append(tools, Tool{Name: mcp.ToolName(e.MCP.Name, t.Name), Description: t.Description, InputSchema: t.InputSchema})
		}
	}
	return tools, mcps, nil
}

// Call runs a tool ("<mcp>/<tool>") for the unit holding a change: it takes the adapter instance of the
// nearest unit, loads its algorithm from the library, and runs the code, which calls the operations the
// connector exposes (configured with the instance's parameters and secrets). A failure reported by
// the code or the connector is a *ToolError.
func (s *Service) Call(ctx context.Context, org, name string, args map[string]any) (map[string]any, error) {
	mcpName, tool, err := mcp.SplitTool(name)
	if err != nil {
		return nil, err
	}
	snap, err := s.Directory.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	def, ok := snap.Def(mcpName)
	if !ok {
		return nil, fmt.Errorf("unknown MCP %s: %w", mcpName, ErrNotBound)
	}
	t, ok := def.Tool(tool)
	if !ok {
		return nil, fmt.Errorf("mcp %s has no tool %s: %w", mcpName, tool, ErrNotFound)
	}
	if err := t.CheckArgs(args); err != nil {
		return nil, err
	}
	a, _, ok := snap.Resolve(org, mcpName)
	if !ok {
		return nil, fmt.Errorf("mcp %s has no adapter for %s or its ancestors: %w", mcpName, domain.OrgOf(org), ErrNotBound)
	}
	alg, err := s.adapterAlgorithm(ctx, a)
	if errors.Is(err, ErrLibrary) {
		return nil, fmt.Errorf("%v: %w", err, ErrUnavailable)
	} else if err != nil {
		return nil, err
	}
	vals, issues := alg.Resolve(a.Params)
	if len(issues) > 0 {
		return nil, fmt.Errorf("adapter of %s in %s: %s: %w", mcpName, a.Unit, issues[0], mcp.ErrInvalid)
	}
	config, secretRefs := alg.Split(vals)
	reg, err := s.Store.Connector(ctx, alg.Connector)
	if errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("connector %s is not registered: %w", alg.Connector, ErrUnavailable)
	} else if err != nil {
		return nil, err
	}
	if !s.live(reg) {
		return nil, fmt.Errorf("connector %s has not renewed its registration since %s: %w", alg.Connector, reg.LastSeen.Format(time.RFC3339), ErrUnavailable)
	}
	secrets, err := s.resolveSecrets(ctx, a, secretRefs, reg.Info)
	if err != nil {
		return nil, err
	}
	var ops []string
	for _, o := range reg.Info.Operations {
		ops = append(ops, o.Name)
	}
	var transport error
	call := func(ctx context.Context, op string, args map[string]any) (map[string]any, error) {
		resp, err := s.Invoker.Invoke(ctx, reg.Endpoint, &connectorv1.InvokeRequest{Operation: op, Arguments: pbconv.Struct(args),
			Config: pbconv.Struct(config), Secrets: secrets, OrgId: a.Unit})
		if err != nil {
			transport = fmt.Errorf("connector %s: %w: %w", alg.Connector, ErrUnavailable, err)
			return nil, transport
		}
		if resp.IsError {
			return nil, errors.New(resp.Error)
		}
		return pbconv.Map(resp.Result), nil
	}
	bound := algo.Bound{Instance: mcp.AdapterKey(a.Unit, a.MCP), Algorithm: alg.Name, Type: algo.UsageAdapter, Language: alg.Language, Code: alg.Code, Params: config}
	out, err := dsl.RunAdapter(ctx, bound, dsl.AdapterInput{Tool: tool, Args: args, Operations: ops, Call: call})
	switch {
	case transport != nil:
		return nil, transport
	case err != nil:
		return nil, &ToolError{Msg: err.Error()}
	case !out.OK():
		return nil, &ToolError{Msg: strings.Join(out.Failures, "; ")}
	}
	if out.Result == nil {
		return map[string]any{}, nil
	}
	return out.Result, nil
}

// resolveSecrets resolves the secret parameters of an adapter instance; only the secrets the
// connector declares leave the hub.
func (s *Service) resolveSecrets(ctx context.Context, a mcp.Adapter, refs map[string]string, info *connectorv1.ConnectorInfo) (map[string]string, error) {
	out := map[string]string{}
	for _, name := range info.SecretNames {
		ref, ok := refs[name]
		if !ok || s.Secrets == nil {
			continue
		}
		v, err := s.Secrets(ctx, ref)
		if err != nil {
			return nil, fmt.Errorf("secret %s of the adapter of %s in %s: %w", name, a.MCP, a.Unit, err)
		}
		out[name] = v
	}
	return out, nil
}
