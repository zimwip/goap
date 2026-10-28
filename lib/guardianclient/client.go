// Package guardianclient asks the guardian service what the methodologies of the changes of a graph say of them
// (registry.v1.ChangeGuardianService, ADR 0168): the client a graph's composition plugs into its hooks. The graph knows
// no methodology; the guardian compiles those the registry stores and answers on the blackboard the graph sends.
package guardianclient

import (
	"context"
	"encoding/json"
	"net/http"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/contracts/domain"
	registryv1 "github.com/zimwip/goap/contracts/gen/goap/registry/v1"
	"github.com/zimwip/goap/contracts/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/lib/pbconv"
	"github.com/zimwip/goap/lib/rpcerr"
)

// Client implements the graph's ChangeLifecycles and its other methodology hooks over Connect.
type Client struct {
	rpc registryv1connect.ChangeGuardianServiceClient
}

// NewClient returns a client of the guardian service at baseURL.
func NewClient(hc *http.Client, baseURL string, opts ...connect.ClientOption) *Client {
	return &Client{rpc: registryv1connect.NewChangeGuardianServiceClient(hc, baseURL, opts...)}
}

// dataActivity is the key of Change.Data naming the Activity a change is scoped to (registrysvc.DataActivity): a change
// scoped to none is judged by the graph alone, so the graph asks nothing of the guardian for it, whose start waits on
// the graph's bootstrap.
const dataActivity = "activityRef"

func activityOf(c domain.Change) string {
	ref, _ := c.Data[dataActivity].(string)
	return ref
}

func board(bb domain.Blackboard) (*registryv1.Blackboard, error) {
	return pbconv.BlackboardToPB(bb)
}

// MethodologyVersion is the version a new change of a methodology follows ("" when no methodology of that name is published).
func (c *Client) MethodologyVersion(ctx context.Context, methodology string) (string, error) {
	r, err := c.rpc.ResolveMethodologyVersion(ctx, connect.NewRequest(&registryv1.ResolveMethodologyVersionRequest{Methodology: methodology}))
	if err != nil {
		return "", rpcerr.FromConnect(err)
	}
	return r.Msg.Version, nil
}

// Lifecycle is the lifecycle the changes of a methodology follow, gates merged in; nil when it names none.
func (c *Client) Lifecycle(ctx context.Context, methodology string) (*domain.Lifecycle, error) {
	r, err := c.rpc.GetChangeLifecycle(ctx, connect.NewRequest(&registryv1.GetChangeLifecycleRequest{Methodology: methodology}))
	if err != nil {
		return nil, rpcerr.FromConnect(err)
	}
	if len(r.Msg.Lifecycle) == 0 {
		return nil, nil
	}
	var lc domain.Lifecycle
	if err := json.Unmarshal(r.Msg.Lifecycle, &lc); err != nil {
		return nil, err
	}
	return &lc, nil
}

// DefaultGoal is the main goal the changes of a methodology start with.
func (c *Client) DefaultGoal(ctx context.Context, methodology string) (string, error) {
	r, err := c.rpc.GetDefaultGoal(ctx, connect.NewRequest(&registryv1.GetDefaultGoalRequest{Methodology: methodology}))
	if err != nil {
		return "", rpcerr.FromConnect(err)
	}
	return r.Msg.Goal, nil
}

// Guard evaluates the guard of a transition on the blackboard of the change.
func (c *Client) Guard(ctx context.Context, bb domain.Blackboard, expr, transition, decision string) (bool, error) {
	b, err := board(bb)
	if err != nil {
		return false, err
	}
	r, err := c.rpc.CheckGuard(ctx, connect.NewRequest(&registryv1.CheckGuardRequest{Blackboard: b, Expr: expr, Transition: transition, Decision: decision}))
	if err != nil {
		return false, rpcerr.FromConnect(err)
	}
	return r.Msg.Ok, nil
}

// Gate evaluates the vetos and the objectives of a transition on the blackboard of the change.
func (c *Client) Gate(ctx context.Context, bb domain.Blackboard, t domain.Transition, decision string) (domain.GateResult, error) {
	b, err := board(bb)
	if err != nil {
		return domain.GateResult{}, err
	}
	raw, err := json.Marshal(t)
	if err != nil {
		return domain.GateResult{}, err
	}
	r, err := c.rpc.CheckGate(ctx, connect.NewRequest(&registryv1.CheckGateRequest{Blackboard: b, Transition: raw, Decision: decision}))
	if err != nil {
		return domain.GateResult{}, rpcerr.FromConnect(err)
	}
	var res domain.GateResult
	if len(r.Msg.Result) > 0 {
		if err := json.Unmarshal(r.Msg.Result, &res); err != nil {
			return domain.GateResult{}, err
		}
	}
	return res, nil
}

// Commit is the say of the methodology on a commit of a change.
func (c *Client) Commit(ctx context.Context, ch domain.Change, bb domain.Blackboard) (decided, ok bool, err error) {
	if activityOf(ch) == "" {
		return false, false, nil // scoped to no Activity: the committable states decide
	}
	b, err := board(bb)
	if err != nil {
		return false, false, err
	}
	r, err := c.rpc.CheckCommit(ctx, connect.NewRequest(&registryv1.CheckCommitRequest{Change: pbconv.ChangeToPB(ch), Blackboard: b}))
	if err != nil {
		return false, false, rpcerr.FromConnect(err)
	}
	return r.Msg.Decided, r.Msg.Ok, nil
}

// JudgesCommit implements graph.CommitJudge: only a change scoped to an Activity is judged at its commit.
func (c *Client) JudgesCommit(ch domain.Change) bool { return activityOf(ch) != "" }

// Guardians is the guardian chain of a new root change of a methodology in a project.
func (c *Client) Guardians(ctx context.Context, methodology, project string) ([]domain.GuardianRef, error) {
	r, err := c.rpc.GetGuardians(ctx, connect.NewRequest(&registryv1.GetGuardiansRequest{Methodology: methodology, Project: project}))
	if err != nil {
		return nil, rpcerr.FromConnect(err)
	}
	return pbconv.GuardiansFromPB(r.Msg.Guardians), nil
}

// Integrates reports whether the changes of integrator integrate the changes of methodology.
func (c *Client) Integrates(ctx context.Context, integrator, methodology string) (bool, error) {
	r, err := c.rpc.CheckIntegrates(ctx, connect.NewRequest(&registryv1.CheckIntegratesRequest{Integrator: integrator, Methodology: methodology}))
	if err != nil {
		return false, rpcerr.FromConnect(err)
	}
	return r.Msg.Integrates, nil
}

// CheckGuardians asks the outer guardians of the chain of a change about a transition, or its commit when transition
// is "".
func (c *Client) CheckGuardians(ctx context.Context, bb domain.Blackboard, transition string, ancestors []domain.Change) (string, error) {
	b, err := board(bb)
	if err != nil {
		return "", err
	}
	in := &registryv1.CheckGuardiansRequest{Blackboard: b, Transition: transition}
	for _, a := range ancestors {
		in.Ancestors = append(in.Ancestors, pbconv.ChangeToPB(a))
	}
	r, err := c.rpc.CheckGuardians(ctx, connect.NewRequest(in))
	if err != nil {
		return "", rpcerr.FromConnect(err)
	}
	return r.Msg.Refusal, nil
}

// SubChangeValidator checks the activity of a sub-change against its parent's (the graph's SubChangeValidator).
func (c *Client) SubChangeValidator(ctx context.Context, parent, child domain.Change) error {
	if ref, ancestor := activityOf(child), activityOf(parent); ref == "" || ancestor == "" || ref == ancestor {
		return nil
	}
	_, err := c.rpc.CheckSubChange(ctx, connect.NewRequest(&registryv1.CheckSubChangeRequest{Parent: pbconv.ChangeToPB(parent), Child: pbconv.ChangeToPB(child)}))
	return rpcerr.FromConnect(err)
}

// OperationRoles are the roles of a methodology allowed an operation on its changes (restricted false: every one).
func (c *Client) OperationRoles(ctx context.Context, methodology, op string) (roles []string, restricted bool, err error) {
	if methodology == "" {
		return nil, false, nil
	}
	r, err := c.rpc.GetOperationRoles(ctx, connect.NewRequest(&registryv1.GetOperationRolesRequest{Methodology: methodology, Operation: op}))
	if err != nil {
		return nil, false, rpcerr.FromConnect(err)
	}
	return r.Msg.Roles, r.Msg.Restricted, nil
}

// StepDependencies are the steps each step of a methodology relies on, to weigh the history of a change; nil when the
// guardian cannot say.
func (c *Client) StepDependencies(ctx context.Context, methodology string) map[string]map[string]bool {
	r, err := c.rpc.GetStepDependencies(ctx, connect.NewRequest(&registryv1.GetStepDependenciesRequest{Methodology: methodology}))
	if err != nil || len(r.Msg.Steps) == 0 {
		return nil
	}
	out := make(map[string]map[string]bool, len(r.Msg.Steps))
	for step, on := range r.Msg.Steps {
		set := make(map[string]bool, len(on.Names))
		for _, n := range on.Names {
			set[n] = true
		}
		out[step] = set
	}
	return out
}
