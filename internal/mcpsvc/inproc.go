package mcpsvc

import (
	"context"
	"strings"
	"time"

	connectorv1 "github.com/zimwip/goap/gen/goap/connector/v1"
	"github.com/zimwip/goap/internal/connectorkit"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/pkg/mcp"
)

// InprocScheme prefixes the endpoint of connectors running inside the process of the hub
// (single-process platform, tests).
const InprocScheme = "inproc://"

// InprocInvoker calls connectors registered in-process (endpoint "inproc://<id>") directly and
// delegates the other endpoints to Remote.
type InprocInvoker struct {
	Connectors map[string]connectorkit.Connector
	Remote     Invoker
}

var _ Invoker = InprocInvoker{}

// Invoke implements Invoker.
func (i InprocInvoker) Invoke(ctx context.Context, endpoint string, req *connectorv1.InvokeRequest) (*connectorv1.InvokeResponse, error) {
	id, ok := strings.CutPrefix(endpoint, InprocScheme)
	if !ok {
		return i.Remote.Invoke(ctx, endpoint, req)
	}
	c, ok := i.Connectors[id]
	if !ok {
		return &connectorv1.InvokeResponse{IsError: true, Error: "no in-process connector " + id}, nil
	}
	res, err := c.Invoke(ctx, req.Operation, pbconv.Map(req.Arguments), pbconv.Map(req.Config), req.Secrets)
	if err != nil {
		return &connectorv1.InvokeResponse{IsError: true, Error: err.Error()}, nil
	}
	return &connectorv1.InvokeResponse{Result: pbconv.Struct(res)}, nil
}

// KeepRegistered registers in-process connectors with the hub and renews them until ctx is done.
func (s *Service) KeepRegistered(ctx context.Context, connectors map[string]connectorkit.Connector) {
	register := func() {
		for id, c := range connectors {
			_, _ = s.RegisterConnector(ctx, c.Info(), InprocScheme+id)
		}
	}
	register()
	go func() {
		t := time.NewTicker(s.lease() / 3)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				register()
			}
		}
	}()
}

// HubPort adapts the service to the engine when both run in the same process: no
// network hop, the caller has already been authorized by the engine.
type HubPort struct{ Service *Service }

// CallTool calls a tool of an organization.
func (h HubPort) CallTool(ctx context.Context, org, name string, args map[string]any) (any, error) {
	return h.Service.Call(ctx, org, name, args)
}

// Tools lists the tools of an organization and the MCPs it binds.
func (h HubPort) Tools(ctx context.Context, org string) ([]mcp.ToolInfo, []string, error) {
	return h.Service.Tools(ctx, org)
}
