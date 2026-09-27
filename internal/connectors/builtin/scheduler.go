package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	connectorv1 "github.com/zimwip/goap/gen/goap/connector/v1"
	enginev1 "github.com/zimwip/goap/gen/goap/engine/v1"
	"github.com/zimwip/goap/internal/connectorkit"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/mcp"
)

// EngineAPI is the part of the engine service goap-scheduler calls. The engine service handler (one
// process) and its Connect client (distributed platform) both implement it: the engine authorizes
// the caller whose identity the request carries.
type EngineAPI interface {
	StartProcess(context.Context, *connect.Request[enginev1.StartProcessRequest]) (*connect.Response[enginev1.StartProcessResponse], error)
	GetProcess(context.Context, *connect.Request[enginev1.GetProcessRequest]) (*connect.Response[enginev1.GetProcessResponse], error)
	ListProcesses(context.Context, *connect.Request[enginev1.ListProcessesRequest]) (*connect.Response[enginev1.ListProcessesResponse], error)
	ListTriggers(context.Context, *connect.Request[enginev1.ListTriggersRequest]) (*connect.Response[enginev1.ListTriggersResponse], error)
	FireTrigger(context.Context, *connect.Request[enginev1.FireTriggerRequest]) (*connect.Response[enginev1.FireTriggerResponse], error)
}

// Scheduler is the goap-scheduler connector: it starts processes (an agent working on a change for an
// intent, like a harness hands a task to a sub-agent), follows them and fires triggers.
type Scheduler struct{ p Ports }

var _ connectorkit.Connector = Scheduler{}

var schedulerOps = []op{
	{"start", "Start a process: {process}", schema(map[string]string{"intent": "string", "methodology": "string", "agent": "string", "goal": "string", "title": "string", "change": "string", "namespace": "string", "unit": "string", "vars": "object"}, "intent")},
	{"list", "List processes, the latest first: {processes, truncated}", schema(map[string]string{"mine": "boolean", "status": "string", "limit": "integer"})},
	{"get", "Read a process: {process}", schema(map[string]string{"id": "string"}, "id")},
	{"triggers", "List the triggers: {triggers}", schema(map[string]string{})},
	{"fire", "Fire a trigger: {process}", schema(map[string]string{"methodology": "string", "agent": "string", "trigger": "string"}, "methodology", "agent", "trigger")},
}

// Info implements connectorkit.Connector.
func (Scheduler) Info() *connectorv1.ConnectorInfo {
	return info(mcp.BuiltinScheduler, "Processes and triggers of the engine, run for the caller (built in).", schedulerOps)
}

// request makes a request carrying the identity of the caller.
func request[T any](who authz.Principal, msg *T) *connect.Request[T] {
	r := connect.NewRequest(msg)
	headers(who, r.Header())
	return r
}

// Invoke implements connectorkit.Connector.
func (s Scheduler) Invoke(ctx context.Context, op string, raw, _ map[string]any, _ map[string]string) (map[string]any, error) {
	who, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	a := args(raw)
	switch op {
	case "start":
		intent, err := a.required("intent")
		if err != nil {
			return nil, err
		}
		req := &enginev1.StartProcessRequest{Intent: intent, Methodology: a.str("methodology"), Agent: a.str("agent"), Goal: a.str("goal"),
			Title: a.str("title"), ChangeId: a.str("change"), OwnerOrg: a.unit(ctx, who), Vars: pbconv.Struct(a.object("vars"))}
		if req.Title == "" {
			req.Title = intent
		}
		if req.ChangeId == "" {
			if req.BaselineId, err = s.baseline(ctx, a); err != nil {
				return nil, err
			}
		}
		r, err := s.p.Engine.StartProcess(ctx, request(who, req))
		if err != nil {
			return nil, err
		}
		return protoResult("process", processView(r.Msg.Process, false))
	case "get":
		id, err := a.required("id")
		if err != nil {
			return nil, err
		}
		r, err := s.p.Engine.GetProcess(ctx, request(who, &enginev1.GetProcessRequest{Id: id}))
		if err != nil {
			return nil, err
		}
		return protoResult("process", processView(r.Msg.Process, true))
	case "list":
		req := &enginev1.ListProcessesRequest{Mine: a.boolean("mine")}
		if st := a.str("status"); st != "" {
			req.Statuses = []string{st}
		}
		r, err := s.p.Engine.ListProcesses(ctx, request(who, req))
		if err != nil {
			return nil, err
		}
		ps := slices.Clone(r.Msg.Processes)
		slices.SortFunc(ps, func(x, y *enginev1.Process) int { return y.GetCreatedAt().AsTime().Compare(x.GetCreatedAt().AsTime()) })
		truncated := len(ps) > a.limit()
		if truncated {
			ps = ps[:a.limit()]
		}
		list := []any{}
		for _, p := range ps {
			m, err := protoMap(processView(p, false))
			if err != nil {
				return nil, err
			}
			list = append(list, m)
		}
		return map[string]any{"processes": list, "truncated": truncated}, nil
	case "triggers":
		r, err := s.p.Engine.ListTriggers(ctx, request(who, &enginev1.ListTriggersRequest{}))
		if err != nil {
			return nil, err
		}
		return protoResult("", r.Msg)
	case "fire":
		m, ag, tr := a.str("methodology"), a.str("agent"), a.str("trigger")
		if m == "" || ag == "" || tr == "" {
			return nil, errors.New(`arguments "methodology", "agent" and "trigger" are required`)
		}
		r, err := s.p.Engine.FireTrigger(ctx, request(who, &enginev1.FireTriggerRequest{Methodology: m, Agent: ag, Trigger: tr}))
		if err != nil {
			return nil, err
		}
		return protoResult("process", processView(r.Msg.Process, false))
	}
	return nil, unknown(op)
}

// baseline is the head of main of the namespace a new process works on: the one the call names, else
// the one of its methodology, else the one of the calling change.
func (s Scheduler) baseline(ctx context.Context, a args) (string, error) {
	ns := a.str("namespace")
	if m := a.str("methodology"); ns == "" && m != "" && s.p.Registry != nil {
		ms, err := s.p.Registry.List(ctx)
		if err != nil {
			return "", err
		}
		for _, c := range ms {
			if c.Name == m {
				ns = c.Namespace
			}
		}
	}
	if id := mcp.CallFrom(ctx).Change; ns == "" && id != "" && s.p.Graph != nil {
		bb, err := s.p.Graph.Blackboard(ctx, domain.ChangeID(id))
		if err != nil {
			return "", err
		}
		ns = bb.Change.Namespace
	}
	if ns == "" || s.p.Graph == nil {
		return "", errors.New(`name the namespace (or the methodology, or the change) the process works on`)
	}
	head, err := s.p.Graph.BranchHead(ctx, ns, domain.MainBranch)
	if err != nil {
		return "", fmt.Errorf("head of main of %s: %w", ns, err)
	}
	return string(head.ID), nil
}

// processView keeps what a tool caller needs of a process; the steps only in detail.
func processView(p *enginev1.Process, detail bool) *enginev1.Process {
	if p == nil {
		return nil
	}
	v := proto.Clone(p).(*enginev1.Process)
	v.World, v.Unknown, v.Candidates = nil, nil, nil
	if !detail {
		v.Steps, v.Turns = nil, nil
	}
	return v
}

func protoMap(m proto.Message) (map[string]any, error) {
	b, err := protojson.MarshalOptions{UseProtoNames: false, EmitUnpopulated: false}.Marshal(m)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// protoResult returns a message as the result, under key when not empty.
func protoResult(key string, m proto.Message) (map[string]any, error) {
	if m == nil || !m.ProtoReflect().IsValid() {
		return nil, fmt.Errorf("the engine returned no %s", key)
	}
	v, err := protoMap(m)
	if err != nil || key == "" {
		return v, err
	}
	return map[string]any{key: v}, nil
}
