// Package connectorkit is the toolkit to write a connector: a separately
// deployed service wrapping the API of a real service and exposed to the
// platform through the MCP hub (ADR 0019).
//
// A connector implements Connector and calls Run from its main:
//
//	func main() { connectorkit.Run("localfs", localfs.New()) }
//
// Run serves the goap.connector.v1.ConnectorService protocol and registers the
// connector with the hub, renewing the registration as a heartbeat. The hub needs
// no configuration to learn about a new connector.
//
// Environment: GOAP_HTTP_ADDR (listen address), GOAP_MCP_URL (the hub),
// GOAP_CONNECTOR_URL (URL of this connector as the hub reaches it; default
// http://<hostname><listen address>), GOAP_CONNECTOR_TOKEN (shared secret between
// hub and connectors; when set, calls without it are refused).
package connectorkit

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"connectrpc.com/connect"

	connectorv1 "github.com/zimwip/goap/gen/goap/connector/v1"
	"github.com/zimwip/goap/gen/goap/connector/v1/connectorv1connect"
	mcpv1 "github.com/zimwip/goap/gen/goap/mcp/v1"
	"github.com/zimwip/goap/gen/goap/mcp/v1/mcpv1connect"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/telemetry"
)

// TokenHeader carries the shared token between the hub and the connectors.
const TokenHeader = "X-Goap-Connector-Token"

// ErrUnknownOperation is returned by Invoke for an operation the connector does not offer.
var ErrUnknownOperation = errors.New("unknown operation")

// Connector is a driver for one real service.
type Connector interface {
	// Info describes the connector: identity, configuration schema, secrets and operations.
	Info() *connectorv1.ConnectorInfo
	// Invoke runs an operation. config is the configuration of the organization that
	// binds the connector, secrets the resolved secrets it declared. A returned error
	// is reported to the caller as a failure of the operation; unknown operations are
	// answered by the kit before Invoke is called.
	Invoke(ctx context.Context, op string, args, config map[string]any, secrets map[string]string) (map[string]any, error)
}

type service struct {
	c     Connector
	token string
}

var _ connectorv1connect.ConnectorServiceHandler = service{}

// Handler returns the Connect handler of a connector, for services that mount it themselves
// (a single-process platform, tests). token, when not empty, is required on Invoke.
func Handler(c Connector, token string, opts ...connect.HandlerOption) (string, http.Handler) {
	return connectorv1connect.NewConnectorServiceHandler(service{c: c, token: token}, opts...)
}

func (s service) Describe(context.Context, *connect.Request[connectorv1.DescribeRequest]) (*connect.Response[connectorv1.DescribeResponse], error) {
	return connect.NewResponse(&connectorv1.DescribeResponse{Info: s.c.Info()}), nil
}

func (s service) Invoke(ctx context.Context, r *connect.Request[connectorv1.InvokeRequest]) (*connect.Response[connectorv1.InvokeResponse], error) {
	if s.token != "" && subtle.ConstantTimeCompare([]byte(r.Header().Get(TokenHeader)), []byte(s.token)) != 1 {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("invalid connector token"))
	}
	known := false
	for _, op := range s.c.Info().Operations {
		known = known || op.Name == r.Msg.Operation
	}
	if !known {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s: %w", r.Msg.Operation, ErrUnknownOperation))
	}
	res, err := s.c.Invoke(ctx, r.Msg.Operation, pbconv.Map(r.Msg.Arguments), pbconv.Map(r.Msg.Config), r.Msg.Secrets)
	if err != nil {
		return connect.NewResponse(&connectorv1.InvokeResponse{IsError: true, Error: err.Error()}), nil
	}
	return connect.NewResponse(&connectorv1.InvokeResponse{Result: pbconv.Struct(res)}), nil
}

// Register announces the connector to the hub and keeps the registration alive until ctx
// is done. It retries while the hub is unreachable, so start order does not matter.
func Register(ctx context.Context, log *slog.Logger, hc *http.Client, hubURL, endpoint, token string, info *connectorv1.ConnectorInfo) {
	hub := mcpv1connect.NewMcpServiceClient(hc, hubURL)
	delay := time.Second
	registered := false
	for {
		req := connect.NewRequest(&mcpv1.RegisterConnectorRequest{Info: info, Endpoint: endpoint})
		if token != "" {
			req.Header().Set(TokenHeader, token)
		}
		resp, err := hub.RegisterConnector(ctx, req)
		switch {
		case err == nil:
			if !registered {
				log.Info("connector registered", "id", info.Id, "hub", hubURL, "endpoint", endpoint)
			}
			registered = true
			delay = time.Duration(resp.Msg.LeaseSeconds) * time.Second / 3
			if delay < time.Second {
				delay = time.Second
			}
		default:
			if registered {
				log.Warn("connector registration lost", "err", err)
			} else {
				log.Warn("connector registration failed, retrying", "err", err)
			}
			registered = false
			delay = 2 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

// Run serves the connector and registers it with the hub. It returns when the server stops.
func Run(name string, c Connector) {
	log := platform.Logger(name)
	defer telemetry.Setup(context.Background(), log, name)(context.Background())
	token := os.Getenv("GOAP_CONNECTOR_TOKEN")
	addr := platform.Env("GOAP_HTTP_ADDR", ":8080")
	srv := platform.NewServer(log, addr)
	srv.Mount(Handler(c, token, telemetry.HandlerOptions()...))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Register(ctx, log, platform.H2CClient(), platform.Env("GOAP_MCP_URL", "http://localhost:8085"), advertised(addr), token, c.Info())
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}

func advertised(addr string) string {
	if u := os.Getenv("GOAP_CONNECTOR_URL"); u != "" {
		return u
	}
	host, _ := os.Hostname()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		port = "8080"
	}
	return "http://" + net.JoinHostPort(host, port)
}
