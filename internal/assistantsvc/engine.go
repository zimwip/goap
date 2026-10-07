package assistantsvc

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	enginev1 "github.com/zimwip/goap/gen/goap/engine/v1"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/pkg/authz"
)

// Engine is what the assistant asks of the engine, always as the caller (ADR 0090): which agents they may run on a
// project, to start a process and the processes running (EngineClient).
type Engine interface {
	// CheckAgents answers whether the caller may start a process of the methodology on the project and which of the
	// agents (all when none is named) they may run there.
	CheckAgents(ctx context.Context, methodology, project string, agents []string) (AgentChecks, error)
	StartProcess(ctx context.Context, in StartProcess) (ProcessInfo, error)
	// Active lists the processes not finished (clarifying, running, waiting, stuck) the caller may read.
	Active(ctx context.Context) ([]ProcessInfo, error)
}

// AgentChecks is the answer of Engine.CheckAgents: the agents the caller may run, by name.
type AgentChecks struct {
	MayStart bool
	MayRun   map[string]bool
}

// StartProcess is the start of an agent: its goal, or the intent the engine identifies the goal from.
type StartProcess struct {
	Methodology, Agent, Goal, Intent, Title string
	ChangeID, ProjectID                     string
	Vars                                    map[string]any
}

// ProcessInfo is what the assistant keeps of a process.
type ProcessInfo struct {
	ID, Methodology, Agent, ChangeID, Status string
}

// EngineAPI is the part of the engine service the assistant calls: the engine handler (one process) and its Connect
// client (distributed platform) both implement it.
type EngineAPI interface {
	StartProcess(context.Context, *connect.Request[enginev1.StartProcessRequest]) (*connect.Response[enginev1.StartProcessResponse], error)
	ListProcesses(context.Context, *connect.Request[enginev1.ListProcessesRequest]) (*connect.Response[enginev1.ListProcessesResponse], error)
	CheckAgents(context.Context, *connect.Request[enginev1.CheckAgentsRequest]) (*connect.Response[enginev1.CheckAgentsResponse], error)
}

// EngineClient is the Engine over the engine service, with the identity of the caller of the context on every
// request: the engine authorizes them.
type EngineClient struct{ API EngineAPI }

var _ Engine = EngineClient{}

func asCaller[T any](ctx context.Context, msg *T) *connect.Request[T] {
	r := connect.NewRequest(msg)
	identity.SetHeaders(authz.From(ctx), r.Header())
	return r
}

func infoOf(p *enginev1.Process) ProcessInfo {
	return ProcessInfo{ID: p.GetId(), Methodology: p.GetMethodology(), Agent: p.GetAgent(), ChangeID: p.GetChangeId(), Status: p.GetStatus()}
}

func (c EngineClient) CheckAgents(ctx context.Context, methodology, project string, agents []string) (AgentChecks, error) {
	r, err := c.API.CheckAgents(ctx, asCaller(ctx, &enginev1.CheckAgentsRequest{Methodology: methodology, ProjectId: project, Agents: agents}))
	if err != nil {
		return AgentChecks{}, err
	}
	out := AgentChecks{MayStart: r.Msg.GetMayStart(), MayRun: map[string]bool{}}
	for _, a := range r.Msg.GetAgents() {
		out.MayRun[a.GetAgent()] = a.GetMayRun()
	}
	return out, nil
}

func (c EngineClient) StartProcess(ctx context.Context, in StartProcess) (ProcessInfo, error) {
	r, err := c.API.StartProcess(ctx, asCaller(ctx, &enginev1.StartProcessRequest{Methodology: in.Methodology, Agent: in.Agent, Goal: in.Goal,
		Intent: in.Intent, Title: in.Title, ChangeId: in.ChangeID, ProjectId: in.ProjectID, Vars: pbconv.Struct(in.Vars)}))
	if err != nil {
		return ProcessInfo{}, err
	}
	if r.Msg.GetProcess() == nil {
		return ProcessInfo{}, fmt.Errorf("the engine started no process")
	}
	return infoOf(r.Msg.Process), nil
}

func (c EngineClient) Active(ctx context.Context) ([]ProcessInfo, error) {
	r, err := c.API.ListProcesses(ctx, asCaller(ctx, &enginev1.ListProcessesRequest{Statuses: []string{"clarifying", "running", "waiting", "stuck"}}))
	if err != nil {
		return nil, err
	}
	var out []ProcessInfo
	for _, p := range r.Msg.GetProcesses() {
		out = append(out, infoOf(p))
	}
	return out, nil
}
