// Package enginesvc exposes the process engine over Connect.
package enginesvc

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"

	"connectrpc.com/connect"

	enginev1 "github.com/zimwip/goap/gen/goap/engine/v1"
	"github.com/zimwip/goap/gen/goap/engine/v1/enginev1connect"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/internal/rpcerr"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/engine"
)

// Handler implements enginev1connect.EngineServiceHandler. Processes run in
// background goroutines; clients poll GetProcess or listen to NATS events.
type Handler struct {
	Engine *engine.Engine
	Log    *slog.Logger
	// Run executes a process in the background (defaults to a goroutine).
	Run func(id string)
	// DefaultPrincipal is used when the request carries no identity headers
	// (single-process dev without gateway). Nil keeps such callers anonymous.
	DefaultPrincipal *authz.Principal
	// Authz authorizes process operations (resource "process"; actions
	// start, submit, read). Nil grants everything.
	Authz authz.Authorizer
	// Broker streams live events (WatchEvents).
	Broker *engine.Broker
	// Triggers runs agents automatically (nil: triggers disabled).
	Triggers *engine.TriggerManager
}

// authorize checks an operation on a process (p nil for start).
func (h *Handler) authorize(ctx context.Context, action, methodology string, p *engine.Process) error {
	who := authz.From(ctx)
	res := authz.Resource{Type: "process", Name: methodology, Org: who.Org, Owner: who.Subject}
	if p != nil {
		res = authz.Resource{Type: "process", ID: p.ID, Name: p.Methodology, Org: p.Initiator.Org, Owner: p.Initiator.Subject}
	}
	return authz.Check(ctx, h.Authz, authz.Request{Subject: who, Action: action, Resource: res})
}

// loadAuthorized loads a process and checks the operation on it.
func (h *Handler) loadAuthorized(ctx context.Context, id, action string) error {
	p, err := h.Engine.Store.Get(ctx, id)
	if err != nil {
		return err
	}
	return h.authorize(ctx, action, "", p)
}

func (h *Handler) principal(ctx context.Context, hdr http.Header) context.Context {
	return identity.Extractor{Default: h.DefaultPrincipal}.Context(ctx, hdr)
}

var _ enginev1connect.EngineServiceHandler = (*Handler)(nil)

func (h *Handler) run(p *engine.Process) {
	if p.Status != engine.StatusRunning {
		return
	}
	if h.Run != nil {
		h.Run(p.ID)
		return
	}
	go func() {
		if _, err := h.Engine.Run(context.Background(), p.ID); err != nil {
			h.Log.Error("run", "process", p.ID, "err", err)
		}
	}()
}

func toConnect(err error) error {
	var unknown engine.ErrUnknownMethodology
	switch {
	case errors.Is(err, engine.ErrNotFound), errors.As(err, &unknown):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, authz.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, engine.ErrInvalidState):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return rpcerr.ToConnect(err)
}

func (h *Handler) StartProcess(ctx context.Context, r *connect.Request[enginev1.StartProcessRequest]) (*connect.Response[enginev1.StartProcessResponse], error) {
	ctx = h.principal(ctx, r.Header())
	if err := h.authorize(ctx, "start", r.Msg.Methodology, nil); err != nil {
		return nil, toConnect(err)
	}
	p, err := h.Engine.Start(ctx, engine.StartRequest{
		Methodology: r.Msg.Methodology, ChangeID: domain.ChangeID(r.Msg.ChangeId), BaselineID: domain.BaselineID(r.Msg.BaselineId),
		Title: r.Msg.Title, Intent: r.Msg.Intent, Goal: r.Msg.Goal, Agent: r.Msg.Agent, OwnerOrg: r.Msg.OwnerOrg, Vars: pbconv.Map(r.Msg.Vars),
	})
	if err != nil {
		return nil, toConnect(err)
	}
	h.run(p)
	return connect.NewResponse(&enginev1.StartProcessResponse{Process: ProcessToPB(p)}), nil
}

func (h *Handler) AnswerIntent(ctx context.Context, r *connect.Request[enginev1.AnswerIntentRequest]) (*connect.Response[enginev1.AnswerIntentResponse], error) {
	ctx = h.principal(ctx, r.Header())
	if err := h.loadAuthorized(ctx, r.Msg.ProcessId, "submit"); err != nil {
		return nil, toConnect(err)
	}
	p, err := h.Engine.Answer(ctx, r.Msg.ProcessId, r.Msg.Answer)
	if err != nil {
		return nil, toConnect(err)
	}
	h.run(p)
	return connect.NewResponse(&enginev1.AnswerIntentResponse{Process: ProcessToPB(p)}), nil
}

func (h *Handler) SubmitHumanInput(ctx context.Context, r *connect.Request[enginev1.SubmitHumanInputRequest]) (*connect.Response[enginev1.SubmitHumanInputResponse], error) {
	items := make([]engine.ItemInput, 0, len(r.Msg.Items))
	for _, s := range r.Msg.Items {
		raw, err := json.Marshal(s.AsMap())
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		var it engine.ItemInput
		if err := json.Unmarshal(raw, &it); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		items = append(items, it)
	}
	ctx = h.principal(ctx, r.Header())
	if err := h.loadAuthorized(ctx, r.Msg.ProcessId, "submit"); err != nil {
		return nil, toConnect(err)
	}
	p, err := h.Engine.Submit(ctx, r.Msg.ProcessId, items)
	if err != nil {
		return nil, toConnect(err)
	}
	h.run(p)
	return connect.NewResponse(&enginev1.SubmitHumanInputResponse{Process: ProcessToPB(p)}), nil
}

func (h *Handler) ApproveAction(ctx context.Context, r *connect.Request[enginev1.ApproveActionRequest]) (*connect.Response[enginev1.ApproveActionResponse], error) {
	p, err := h.Engine.Approve(h.principal(ctx, r.Header()), r.Msg.ProcessId, r.Msg.Approve, r.Msg.Comment)
	if err != nil {
		return nil, toConnect(err)
	}
	h.run(p)
	return connect.NewResponse(&enginev1.ApproveActionResponse{Process: ProcessToPB(p)}), nil
}

func (h *Handler) RelaunchStep(ctx context.Context, r *connect.Request[enginev1.RelaunchStepRequest]) (*connect.Response[enginev1.RelaunchStepResponse], error) {
	ctx = h.principal(ctx, r.Header())
	if err := h.loadAuthorized(ctx, r.Msg.ProcessId, "relaunch"); err != nil {
		return nil, toConnect(err)
	}
	p, err := h.Engine.Relaunch(ctx, r.Msg.ProcessId, int(r.Msg.Step), r.Msg.Reason, r.Msg.Guidance)
	if err != nil {
		return nil, toConnect(err)
	}
	h.run(p)
	return connect.NewResponse(&enginev1.RelaunchStepResponse{Process: ProcessToPB(p)}), nil
}

func (h *Handler) DecideFlow(ctx context.Context, r *connect.Request[enginev1.DecideFlowRequest]) (*connect.Response[enginev1.DecideFlowResponse], error) {
	ctx = h.principal(ctx, r.Header())
	if err := h.loadAuthorized(ctx, r.Msg.ProcessId, "decide_flow"); err != nil {
		return nil, toConnect(err)
	}
	p, err := h.Engine.DecideFlow(ctx, r.Msg.ProcessId, r.Msg.Adopt, r.Msg.Comment)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&enginev1.DecideFlowResponse{Process: ProcessToPB(p)}), nil
}

func (h *Handler) ResolveBoard(ctx context.Context, r *connect.Request[enginev1.ResolveBoardRequest]) (*connect.Response[enginev1.ResolveBoardResponse], error) {
	ctx = h.principal(ctx, r.Header())
	if err := h.loadAuthorized(ctx, r.Msg.ProcessId, "relaunch"); err != nil {
		return nil, toConnect(err)
	}
	p, np, err := h.Engine.ResolveBoard(ctx, r.Msg.ProcessId, r.Msg.Relaunch, r.Msg.Comment)
	if err != nil {
		return nil, toConnect(err)
	}
	out := &enginev1.ResolveBoardResponse{Process: ProcessToPB(p)}
	if np != nil {
		out.Relaunched = ProcessToPB(np)
		h.run(np)
	}
	h.run(p)
	return connect.NewResponse(out), nil
}

func (h *Handler) GetProcess(ctx context.Context, r *connect.Request[enginev1.GetProcessRequest]) (*connect.Response[enginev1.GetProcessResponse], error) {
	ctx = h.principal(ctx, r.Header())
	p, err := h.Engine.Store.Get(ctx, r.Msg.Id)
	if err != nil {
		return nil, toConnect(err)
	}
	if err := h.authorize(ctx, "read", "", p); err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&enginev1.GetProcessResponse{Process: ProcessToPB(p)}), nil
}

func (h *Handler) ListProcesses(ctx context.Context, r *connect.Request[enginev1.ListProcessesRequest]) (*connect.Response[enginev1.ListProcessesResponse], error) {
	ctx = h.principal(ctx, r.Header())
	me := authz.From(ctx)
	all, err := h.Engine.Store.List(ctx)
	statuses := map[string]bool{}
	for _, s := range r.Msg.Statuses {
		statuses[s] = true
	}
	// only the processes the caller may read, filtered
	var ps []*engine.Process
	for _, p := range all {
		switch {
		case r.Msg.Mine && p.Initiator.Subject != me.Subject,
			len(statuses) > 0 && !statuses[string(p.Status)],
			r.Msg.RootsOnly && p.ParentID != "":
			continue
		}
		if h.authorize(ctx, "read", "", p) == nil {
			ps = append(ps, p)
		}
	}
	if err != nil {
		return nil, toConnect(err)
	}
	out := &enginev1.ListProcessesResponse{}
	for _, p := range ps {
		out.Processes = append(out.Processes, ProcessToPB(p))
	}
	return connect.NewResponse(out), nil
}

// WatchEvents streams live process events and logs.
func (h *Handler) WatchEvents(ctx context.Context, r *connect.Request[enginev1.WatchEventsRequest], stream *connect.ServerStream[enginev1.WatchEventsResponse]) error {
	if h.Broker == nil {
		return connect.NewError(connect.CodeUnimplemented, errors.New("event streaming is not configured"))
	}
	ctx = h.principal(ctx, r.Header())
	if id := r.Msg.ProcessId; id != "" {
		if err := h.loadAuthorized(ctx, id, "read"); err != nil {
			return toConnect(err)
		}
	}
	events, cancel := h.Broker.Subscribe(256)
	defer cancel()
	allowed := map[string]bool{} // read decision per process
	canRead := func(p *engine.Process) bool {
		ok, seen := allowed[p.ID]
		if !seen {
			ok = h.authorize(ctx, "read", "", p) == nil
			allowed[p.ID] = ok
		}
		return ok
	}
	related := func(id string) bool {
		return r.Msg.ProcessId == "" || id == r.Msg.ProcessId
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-events:
			if !ok {
				return nil
			}
			out := &enginev1.WatchEventsResponse{Type: ev.Event, Time: pbconv.Time(ev.Time)}
			switch {
			case ev.Process != nil:
				p := ev.Process
				if !(related(p.ID) || related(p.ParentID)) || !canRead(p) {
					continue
				}
				out.Process = ProcessToPB(p)
			case ev.Log != nil:
				if !related(ev.Log.ProcessID) {
					continue
				}
				if ok, seen := allowed[ev.Log.ProcessID]; seen && !ok {
					continue
				}
				if _, seen := allowed[ev.Log.ProcessID]; !seen {
					p, err := h.Engine.Store.Get(ctx, ev.Log.ProcessID)
					if err != nil || !canRead(p) {
						continue
					}
				}
				out.Log = logToPB(*ev.Log)
			default:
				continue
			}
			if err := stream.Send(out); err != nil {
				return err
			}
		}
	}
}

// ListTriggers lists the triggers of the published agents.
func (h *Handler) ListTriggers(ctx context.Context, r *connect.Request[enginev1.ListTriggersRequest]) (*connect.Response[enginev1.ListTriggersResponse], error) {
	out := &enginev1.ListTriggersResponse{}
	if h.Triggers == nil {
		return connect.NewResponse(out), nil
	}
	ctx = h.principal(ctx, r.Header())
	if err := authz.Check(ctx, h.Authz, authz.Request{Subject: authz.From(ctx), Action: "read", Resource: authz.Resource{Type: "trigger"}}); err != nil {
		return nil, toConnect(err)
	}
	for _, s := range h.Triggers.States() {
		out.Triggers = append(out.Triggers, &enginev1.TriggerState{Methodology: s.Methodology, Agent: s.Agent, Name: s.Name, Description: s.Description,
			Type: s.Type, Event: s.Event, Schedule: s.Schedule, Enabled: s.Enabled, Fires: int32(s.Fires), LastFired: pbconv.Time(s.LastFired),
			NextFire: pbconv.Time(s.NextFire), LastProcessId: s.LastProcessID, LastError: s.LastError})
	}
	return connect.NewResponse(out), nil
}

// FireTrigger fires a trigger now.
func (h *Handler) FireTrigger(ctx context.Context, r *connect.Request[enginev1.FireTriggerRequest]) (*connect.Response[enginev1.FireTriggerResponse], error) {
	if h.Triggers == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("triggers are disabled"))
	}
	ctx = h.principal(ctx, r.Header())
	who := authz.From(ctx)
	if err := authz.Check(ctx, h.Authz, authz.Request{Subject: who, Action: "fire",
		Resource: authz.Resource{Type: "trigger", ID: r.Msg.Methodology + "/" + r.Msg.Agent + "/" + r.Msg.Trigger, Name: r.Msg.Methodology, Org: who.Org}}); err != nil {
		return nil, toConnect(err)
	}
	p, err := h.Triggers.Fire(ctx, r.Msg.Methodology, r.Msg.Agent, r.Msg.Trigger)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewResponse(&enginev1.FireTriggerResponse{Process: ProcessToPB(p)}), nil
}

func logToPB(l engine.LogLine) *enginev1.LogLine {
	return &enginev1.LogLine{Time: pbconv.Time(l.Time), Level: l.Level, Message: l.Message, ProcessId: l.ProcessID, Action: l.Action, Step: int32(l.Step)}
}

func usageToPB(u engine.Usage) *enginev1.Usage {
	return &enginev1.Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, LlmCalls: int32(u.LLMCalls), ToolCalls: int32(u.ToolCalls)}
}

// ProcessToPB converts a process.
func ProcessToPB(p *engine.Process) *enginev1.Process {
	out := &enginev1.Process{
		Id: p.ID, Methodology: p.Methodology, ChangeId: string(p.ChangeID), Status: string(p.Status), Goal: p.Goal,
		Question: p.Question, Plan: p.Plan, World: p.World, Unknown: p.Unknown, Error: p.Error,
		CreatedAt: pbconv.Time(p.CreatedAt), UpdatedAt: pbconv.Time(p.UpdatedAt),
		Initiator: &enginev1.Principal{Subject: p.Initiator.Subject, Org: p.Initiator.Org, Roles: p.Initiator.Roles},
		Agent:     p.Agent, Planner: p.Planner, ParentId: p.ParentID, Usage: usageToPB(p.Usage),
		BaselineId: string(p.BaselineID), Title: p.Title, TraceId: p.TraceID, Trigger: p.Trigger, Flow: p.Flow, RelaunchOf: p.RelaunchOf, FromStep: int32(p.FromStep),
	}
	for _, t := range p.Intent.Turns {
		out.Turns = append(out.Turns, &enginev1.Turn{Role: t.Role, Text: t.Text})
	}
	for _, c := range p.Candidates {
		out.Candidates = append(out.Candidates, &enginev1.Candidate{Goal: c.Goal, Confidence: c.Confidence, Reason: c.Reason, Agent: c.Agent, Methodology: c.Methodology})
	}
	if t := p.Pending; t != nil {
		out.Pending = &enginev1.HumanTask{Kind: t.Kind, Permission: t.Permission, Action: t.Action, Description: t.Description,
			Instructions: t.Instructions, Step: int32(t.Step), ChildProcessId: t.ChildProcessID, FlowId: t.FlowID}
		for _, i := range t.Issues {
			out.Pending.Issues = append(out.Pending.Issues, &enginev1.BoardIssue{Item: string(i.Item), Culprit: string(i.Culprit), Code: i.Code, Message: i.Message, Severity: i.Severity})
		}
		if pr := t.Proposal; pr != nil {
			rp := &enginev1.RelaunchProposal{Process: pr.Process, Step: int32(pr.Step), Action: pr.Action, Reason: pr.Reason}
			for _, c := range pr.Culprits {
				rp.Culprits = append(rp.Culprits, string(c))
			}
			out.Pending.Proposal = rp
		}
	}
	for a := range p.Disabled {
		out.Disabled = append(out.Disabled, a)
	}
	sort.Strings(out.Disabled)
	for _, s := range p.Steps {
		ps := &enginev1.Step{Index: int32(s.Index), Action: s.Action, Plan: s.Plan, Before: s.Before, After: s.After,
			EffectsMet: s.EffectsMet, ApprovedBy: s.ApprovedBy, Output: s.Output, Error: s.Error, StartedAt: pbconv.Time(s.StartedAt), EndedAt: pbconv.Time(s.EndedAt)}
		for _, id := range s.Items {
			ps.Items = append(ps.Items, string(id))
		}
		ps.Usage, ps.ChildProcessIds, ps.Sandbox = usageToPB(s.Usage), s.Children, s.Sandbox
		for _, c := range s.LLMCalls {
			ps.LlmCalls = append(ps.LlmCalls, &enginev1.LlmCall{Provider: c.Provider, Model: c.Model, InputTokens: c.InputTokens,
				OutputTokens: c.OutputTokens, DurationMs: c.DurationMs, Error: c.Error})
		}
		for _, c := range s.ToolCalls {
			ps.ToolCalls = append(ps.ToolCalls, &enginev1.ToolCall{Name: c.Name, DurationMs: c.DurationMs, Error: c.Error})
		}
		for _, l := range s.Logs {
			ps.Logs = append(ps.Logs, logToPB(l))
		}
		out.Steps = append(out.Steps, ps)
	}
	return out
}
