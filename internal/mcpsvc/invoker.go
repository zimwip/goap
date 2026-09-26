package mcpsvc

import (
	"context"
	"net/http"
	"sync"

	"connectrpc.com/connect"

	connectorv1 "github.com/zimwip/goap/gen/goap/connector/v1"
	"github.com/zimwip/goap/gen/goap/connector/v1/connectorv1connect"
	"github.com/zimwip/goap/internal/platform"
)

// ConnectorTokenHeader carries the shared token between the hub and the connectors.
const ConnectorTokenHeader = "X-Goap-Connector-Token"

// ConnectInvoker calls connectors over Connect. Token, when set, is sent on every call:
// connectors reject calls without it (GOAP_CONNECTOR_TOKEN).
type ConnectInvoker struct {
	HTTP  *http.Client
	Token string

	mu      sync.Mutex
	clients map[string]connectorv1connect.ConnectorServiceClient
}

var _ Invoker = (*ConnectInvoker)(nil)

func (i *ConnectInvoker) client(endpoint string) connectorv1connect.ConnectorServiceClient {
	i.mu.Lock()
	defer i.mu.Unlock()
	if c, ok := i.clients[endpoint]; ok {
		return c
	}
	if i.clients == nil {
		i.clients = map[string]connectorv1connect.ConnectorServiceClient{}
	}
	hc := i.HTTP
	if hc == nil {
		hc = platform.H2CClient()
	}
	c := connectorv1connect.NewConnectorServiceClient(hc, endpoint)
	i.clients[endpoint] = c
	return c
}

// Invoke implements Invoker.
func (i *ConnectInvoker) Invoke(ctx context.Context, endpoint string, req *connectorv1.InvokeRequest) (*connectorv1.InvokeResponse, error) {
	r := connect.NewRequest(req)
	if i.Token != "" {
		r.Header().Set(ConnectorTokenHeader, i.Token)
	}
	resp, err := i.client(endpoint).Invoke(ctx, r)
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// ResolveSecret resolves a secret reference of an adapter (see platform.Secrets.Resolve).
func ResolveSecret(s *platform.Secrets) func(ctx context.Context, ref string) (string, error) {
	return s.Resolve
}
