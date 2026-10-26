package registrysvc

import (
	"context"
	"encoding/json"
	"sort"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/contracts/domain"
	registryv1 "github.com/zimwip/goap/contracts/gen/goap/registry/v1"
	"github.com/zimwip/goap/contracts/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/lib/pbconv"
	"github.com/zimwip/goap/lib/rpcerr"
)

// GuardianHandler serves what the graphs ask of the methodologies of their changes (ADR 0168): the hooks a graph
// offers, answered by the methodologies this registry compiles. The graph sends the blackboard it judges; the answers
// are the ones the Service gives in process.
type GuardianHandler struct {
	Service *Service
}

var _ registryv1connect.ChangeGuardianServiceHandler = (*GuardianHandler)(nil)

func blackboardOf(in *registryv1.Blackboard) (domain.Blackboard, error) {
	bb, err := pbconv.BlackboardFromPB(in)
	if err != nil {
		return domain.Blackboard{}, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return bb, nil
}

func (h *GuardianHandler) ResolveMethodologyVersion(ctx context.Context, r *connect.Request[registryv1.ResolveMethodologyVersionRequest]) (*connect.Response[registryv1.ResolveMethodologyVersionResponse], error) {
	v, err := h.Service.MethodologyVersion(ctx, r.Msg.Methodology)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	return connect.NewResponse(&registryv1.ResolveMethodologyVersionResponse{Version: v}), nil
}

func (h *GuardianHandler) GetChangeLifecycle(ctx context.Context, r *connect.Request[registryv1.GetChangeLifecycleRequest]) (*connect.Response[registryv1.GetChangeLifecycleResponse], error) {
	lc, err := h.Service.Lifecycle(ctx, r.Msg.Methodology)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	out := &registryv1.GetChangeLifecycleResponse{}
	if lc != nil {
		if out.Lifecycle, err = json.Marshal(lc); err != nil {
			return nil, rpcerr.ToConnect(err)
		}
	}
	return connect.NewResponse(out), nil
}

func (h *GuardianHandler) GetDefaultGoal(ctx context.Context, r *connect.Request[registryv1.GetDefaultGoalRequest]) (*connect.Response[registryv1.GetDefaultGoalResponse], error) {
	g, err := h.Service.DefaultGoal(ctx, r.Msg.Methodology)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	return connect.NewResponse(&registryv1.GetDefaultGoalResponse{Goal: g}), nil
}

func (h *GuardianHandler) CheckGuard(ctx context.Context, r *connect.Request[registryv1.CheckGuardRequest]) (*connect.Response[registryv1.CheckGuardResponse], error) {
	bb, err := blackboardOf(r.Msg.Blackboard)
	if err != nil {
		return nil, err
	}
	ok, err := h.Service.Guard(ctx, bb, r.Msg.Expr, r.Msg.Transition, r.Msg.Decision)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	return connect.NewResponse(&registryv1.CheckGuardResponse{Ok: ok}), nil
}

func (h *GuardianHandler) CheckGate(ctx context.Context, r *connect.Request[registryv1.CheckGateRequest]) (*connect.Response[registryv1.CheckGateResponse], error) {
	bb, err := blackboardOf(r.Msg.Blackboard)
	if err != nil {
		return nil, err
	}
	var t domain.Transition
	if err := json.Unmarshal(r.Msg.Transition, &t); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	res, err := h.Service.Gate(ctx, bb, t, r.Msg.Decision)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	return connect.NewResponse(&registryv1.CheckGateResponse{Result: raw}), nil
}

func (h *GuardianHandler) CheckCommit(ctx context.Context, r *connect.Request[registryv1.CheckCommitRequest]) (*connect.Response[registryv1.CheckCommitResponse], error) {
	bb, err := blackboardOf(r.Msg.Blackboard)
	if err != nil {
		return nil, err
	}
	decided, ok, err := h.Service.Commit(ctx, pbconv.ChangeFromPB(r.Msg.Change), bb)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	return connect.NewResponse(&registryv1.CheckCommitResponse{Decided: decided, Ok: ok}), nil
}

func (h *GuardianHandler) GetGuardians(ctx context.Context, r *connect.Request[registryv1.GetGuardiansRequest]) (*connect.Response[registryv1.GetGuardiansResponse], error) {
	gs, err := h.Service.Guardians(ctx, r.Msg.Methodology, r.Msg.Project)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	return connect.NewResponse(&registryv1.GetGuardiansResponse{Guardians: pbconv.GuardiansToPB(gs)}), nil
}

func (h *GuardianHandler) CheckIntegrates(ctx context.Context, r *connect.Request[registryv1.CheckIntegratesRequest]) (*connect.Response[registryv1.CheckIntegratesResponse], error) {
	ok, err := h.Service.Integrates(ctx, r.Msg.Integrator, r.Msg.Methodology)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	return connect.NewResponse(&registryv1.CheckIntegratesResponse{Integrates: ok}), nil
}

func (h *GuardianHandler) CheckGuardians(ctx context.Context, r *connect.Request[registryv1.CheckGuardiansRequest]) (*connect.Response[registryv1.CheckGuardiansResponse], error) {
	bb, err := blackboardOf(r.Msg.Blackboard)
	if err != nil {
		return nil, err
	}
	ancestors := make([]domain.Change, 0, len(r.Msg.Ancestors))
	for _, a := range r.Msg.Ancestors {
		ancestors = append(ancestors, pbconv.ChangeFromPB(a))
	}
	refusal, err := h.Service.CheckGuardians(ctx, bb, r.Msg.Transition, ancestors)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	return connect.NewResponse(&registryv1.CheckGuardiansResponse{Refusal: refusal}), nil
}

func (h *GuardianHandler) CheckSubChange(ctx context.Context, r *connect.Request[registryv1.CheckSubChangeRequest]) (*connect.Response[registryv1.CheckSubChangeResponse], error) {
	if err := h.Service.SubChangeValidator(ctx, pbconv.ChangeFromPB(r.Msg.Parent), pbconv.ChangeFromPB(r.Msg.Child)); err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	return connect.NewResponse(&registryv1.CheckSubChangeResponse{}), nil
}

func (h *GuardianHandler) GetOperationRoles(ctx context.Context, r *connect.Request[registryv1.GetOperationRolesRequest]) (*connect.Response[registryv1.GetOperationRolesResponse], error) {
	roles, restricted, err := h.Service.OperationRoles(ctx, r.Msg.Methodology, r.Msg.Operation)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	return connect.NewResponse(&registryv1.GetOperationRolesResponse{Roles: roles, Restricted: restricted}), nil
}

// GetStepDependencies answers no step for a methodology the registry does not know or cannot compile: its history is
// folded with no step relying on another.
func (h *GuardianHandler) GetStepDependencies(ctx context.Context, r *connect.Request[registryv1.GetStepDependenciesRequest]) (*connect.Response[registryv1.GetStepDependenciesResponse], error) {
	out := &registryv1.GetStepDependenciesResponse{}
	m, err := h.Service.Methodology(ctx, r.Msg.Methodology)
	if err != nil {
		return connect.NewResponse(out), nil
	}
	deps := m.StepDependencies()
	if len(deps) > 0 {
		out.Steps = make(map[string]*registryv1.Names, len(deps))
	}
	for step, on := range deps {
		names := make([]string, 0, len(on))
		for n := range on {
			names = append(names, n)
		}
		sort.Strings(names)
		out.Steps[step] = &registryv1.Names{Names: names}
	}
	return connect.NewResponse(out), nil
}
