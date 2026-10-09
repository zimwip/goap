package enginesvc

import (
	"context"

	"connectrpc.com/connect"

	enginev1 "github.com/zimwip/goap/gen/goap/engine/v1"
	"github.com/zimwip/goap/gen/goap/engine/v1/enginev1connect"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/changeapi"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/engine"
)

// TransitionChange moves a change along a transition of the lifecycle of its methodology (ADR 0058, ADR 0098), for
// the caller: the engine checks the permission of the transition (Engine.TransitionAuthorizer), its gate, and records
// the move.
func (h *Handler) TransitionChange(ctx context.Context, r *connect.Request[enginev1.TransitionChangeRequest]) (*connect.Response[enginev1.TransitionChangeResponse], error) {
	ctx = h.principal(ctx, r.Header())
	out, err := h.Engine.TransitionChange(ctx, domain.ChangeID(r.Msg.ChangeId), engine.TransitionRequest{Transition: r.Msg.Transition, Decision: r.Msg.Decision,
		By: authz.From(ctx).Subject})
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&enginev1.TransitionChangeResponse{Lifecycle: out.State.Lifecycle, State: out.State.State, TransitionKey: out.Move}), nil
}

// GuardianHandler serves the guardian of the changes governed by a methodology (engine.Guardian, ADR 0098) to the
// graph, which asks it before it lands a change, takes a sub-change, moves a change or edits an impact, forwarding the
// identity of its caller. It is an internal service: the gateway routes no call to it, and its answers decide nothing
// by themselves (the graph acts on them).
type GuardianHandler struct {
	Guardian changeapi.Guardian
	Handler  *Handler
}

var _ enginev1connect.GuardianServiceHandler = (*GuardianHandler)(nil)

func (g *GuardianHandler) ctx(ctx context.Context, r connect.AnyRequest) context.Context {
	if g.Handler != nil {
		return g.Handler.principal(ctx, r.Header())
	}
	return ctx
}

// MayCommit implements enginev1connect.GuardianServiceHandler.
func (g *GuardianHandler) MayCommit(ctx context.Context, r *connect.Request[enginev1.MayCommitRequest]) (*connect.Response[enginev1.MayCommitResponse], error) {
	bb := pbconv.BlackboardFromPB(r.Msg.Blackboard)
	if err := g.Guardian.MayCommit(g.ctx(ctx, r), bb.Change, bb); err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&enginev1.MayCommitResponse{}), nil
}

// MayCreateChild implements enginev1connect.GuardianServiceHandler.
func (g *GuardianHandler) MayCreateChild(ctx context.Context, r *connect.Request[enginev1.MayCreateChildRequest]) (*connect.Response[enginev1.MayCreateChildResponse], error) {
	if err := g.Guardian.MayCreateChild(g.ctx(ctx, r), pbconv.ChangeFromPB(r.Msg.Parent), pbconv.ChangeFromPB(r.Msg.Child)); err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&enginev1.MayCreateChildResponse{}), nil
}

// MayMove implements enginev1connect.GuardianServiceHandler.
func (g *GuardianHandler) MayMove(ctx context.Context, r *connect.Request[enginev1.MayMoveRequest]) (*connect.Response[enginev1.MayMoveResponse], error) {
	family := make([]domain.Change, 0, len(r.Msg.Family))
	for _, c := range r.Msg.Family {
		family = append(family, pbconv.ChangeFromPB(c))
	}
	if err := g.Guardian.MayMove(g.ctx(ctx, r), family, r.Msg.To); err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&enginev1.MayMoveResponse{}), nil
}

// MayEdit implements enginev1connect.GuardianServiceHandler.
func (g *GuardianHandler) MayEdit(ctx context.Context, r *connect.Request[enginev1.MayEditRequest]) (*connect.Response[enginev1.MayEditResponse], error) {
	if err := g.Guardian.MayEdit(g.ctx(ctx, r), pbconv.ChangeFromPB(r.Msg.Change), domain.ChangeImpactID(r.Msg.ImpactId)); err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&enginev1.MayEditResponse{}), nil
}
