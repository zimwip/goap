// Package builtin holds the connectors of the platform itself (ADR 0028): the drivers of the
// built-in MCPs goap-graph, goap-change, goap-scheduler and goap-admin. Like a harness that ships
// its own tools next to the ones users plug in, the hub serves them in-process (endpoint
// inproc://<id>) and they register like any connector; the default organisation holds an adapter
// instance of each, so that every unit can use them until it restricts them.
//
// They call the platform's own APIs for the caller: the principal of the context (the initiator
// of the process) is checked by the hub (tool:call), then by the APIs they reach (the engine for
// processes, the access gate for User and Policy nodes, read authorization per node type). An
// anonymous call is refused. The call context (mcp.CallFrom) gives the unit and the change the
// call runs for: goap-change works on the change of the calling process by default.
package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	connectorv1 "github.com/zimwip/goap/gen/goap/connector/v1"
	"github.com/zimwip/goap/internal/connectorkit"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/mcp"
	"github.com/zimwip/goap/pkg/mcpbuiltin"
)

// Version is the version the built-in connectors announce.
const Version = "1"

// DefaultLimit bounds the lists a tool returns when the call sets no limit.
const DefaultLimit = 50

// Ports are the platform APIs the built-in connectors reach. A nil port leaves its connector out.
type Ports struct {
	// Graph is the graph (in-process *graph.Graph or the graph service client): goap-graph, goap-change
	// and the organisation read by goap-admin.
	Graph engine.GraphPort
	// Engine starts and reads processes (goap-scheduler): the engine service handler in-process, its
	// client otherwise.
	Engine EngineAPI
	// Hub describes the MCPs and connectors (goap-admin): the hub service.
	Hub Hub
	// Registry lists the published domains and methodologies (goap-admin).
	Registry Registry
	// Authz authorizes reads per node type; Floor guards the access nodes (User, Policy: ADR 0020),
	// Authz when nil. Without Authz only the platform APIs authorize.
	Authz authz.Authorizer
	Floor authz.Authorizer
}

// Connectors returns the built-in connectors the ports allow, by id.
func Connectors(p Ports) map[string]connectorkit.Connector {
	out := map[string]connectorkit.Connector{}
	if p.Graph != nil {
		out[mcpbuiltin.Graph] = Graph{p}
		out[mcpbuiltin.Change] = Change{p}
	}
	if p.Engine != nil {
		out[mcpbuiltin.Scheduler] = Scheduler{p}
	}
	if p.Hub != nil && p.Registry != nil && p.Graph != nil {
		out[mcpbuiltin.Admin] = Admin{p}
	}
	return out
}

// op is an operation of a built-in connector.
type op struct {
	name, description string
	schema            map[string]any
}

func info(id, description string, ops []op) *connectorv1.ConnectorInfo {
	out := &connectorv1.ConnectorInfo{Id: id, Version: Version, Description: description}
	for _, o := range ops {
		s, _ := structpb.NewStruct(o.schema)
		out.Operations = append(out.Operations, &connectorv1.Operation{Name: o.name, Description: o.description, InputSchema: s})
	}
	return out
}

// schema is the input schema of an operation: its properties (all strings unless typed) and the required ones.
func schema(props map[string]string, required ...string) map[string]any {
	p := map[string]any{}
	for name, typ := range props {
		p[name] = map[string]any{"type": typ}
	}
	s := map[string]any{"type": "object", "properties": p}
	if len(required) > 0 {
		r := make([]any, len(required))
		for i, n := range required {
			r[i] = n
		}
		s["required"] = r
	}
	return s
}

// caller returns the principal the call runs for; the built-in connectors never act anonymously.
func caller(ctx context.Context) (authz.Principal, error) {
	p := authz.From(ctx)
	if p.Anonymous() {
		return p, errors.New("the built-in connectors act for a caller: the call carries no principal")
	}
	return p, nil
}

// headers are the identity headers of the caller, for the platform APIs reached as Connect services.
func headers(p authz.Principal, h http.Header) { identity.SetHeaders(p, h) }

// result converts a value to the JSON object an operation returns.
func result(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// args reads the arguments of a call.
type args map[string]any

func (a args) str(name string) string {
	s, _ := a[name].(string)
	return strings.TrimSpace(s)
}

func (a args) required(name string) (string, error) {
	if s := a.str(name); s != "" {
		return s, nil
	}
	return "", fmt.Errorf("argument %q is required", name)
}

func (a args) boolean(name string) bool {
	b, _ := a[name].(bool)
	return b
}

func (a args) object(name string) map[string]any {
	m, _ := a[name].(map[string]any)
	return m
}

func (a args) limit() int {
	if f, ok := a["limit"].(float64); ok && f > 0 {
		return int(f)
	}
	return DefaultLimit
}

// unit is the unit a call names, else the unit the call runs for, else the principal's.
func (a args) unit(ctx context.Context, p authz.Principal) string {
	if u := a.str("unit"); u != "" {
		return u
	}
	if u := mcp.CallFrom(ctx).Unit; u != "" {
		return u
	}
	return p.Org
}

// ForwardIdentity is the client option of the platform clients the built-in connectors use: the
// requests carry the identity of the caller of the tool.
func ForwardIdentity() connect.ClientOption { return identity.Forward() }

func unknown(op string) error { return fmt.Errorf("%s: %w", op, connectorkit.ErrUnknownOperation) }
