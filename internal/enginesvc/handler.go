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
		Title: r.Msg.Title, Intent: r.Msg.Intent, Goal: r.Msg.Goal, Vars: pbconv.Map(r.Msg.Vars),
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
	all, err := h.Engine.Store.List(ctx)
	// only the processes the caller may read
	var ps []*engine.Process
	for _, p := range all {
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

// ProcessToPB converts a process.
func ProcessToPB(p *engine.Process) *enginev1.Process {
	out := &enginev1.Process{
		Id: p.ID, Methodology: p.Methodology, ChangeId: string(p.ChangeID), Status: string(p.Status), Goal: p.Goal,
		Question: p.Question, Plan: p.Plan, World: p.World, Unknown: p.Unknown, Error: p.Error,
		CreatedAt: pbconv.Time(p.CreatedAt), UpdatedAt: pbconv.Time(p.UpdatedAt),
		Initiator: &enginev1.Principal{Subject: p.Initiator.Subject, Org: p.Initiator.Org, Roles: p.Initiator.Roles},
	}
	for _, t := range p.Intent.Turns {
		out.Turns = append(out.Turns, &enginev1.Turn{Role: t.Role, Text: t.Text})
	}
	for _, c := range p.Candidates {
		out.Candidates = append(out.Candidates, &enginev1.Candidate{Goal: c.Goal, Confidence: c.Confidence, Reason: c.Reason})
	}
	if t := p.Pending; t != nil {
		out.Pending = &enginev1.HumanTask{Kind: t.Kind, Permission: t.Permission, Action: t.Action, Description: t.Description, Instructions: t.Instructions, Step: int32(t.Step)}
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
		out.Steps = append(out.Steps, ps)
	}
	return out
}
