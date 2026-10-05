package engine

import (
	"context"
	"errors"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/mcp"
)

// ProcessRef is what the Scope needs to know of a process: who started it and where it runs. A snapshot, so
// the port does not depend on *Process. Org and Project are the scope of its change; empty (a process with no change), the services behind the
// scope resolve them to the roots of the structures (the engine names none, ADR 0054).
type ProcessRef struct {
	ID          string
	Methodology string
	ChangeID    domain.ChangeID
	Initiator   authz.Principal
	// Org is the organisation holding the change (a unit key): its adapters decide which MCPs are bound.
	Org string
	// Project is the project of the change: the roles its Assignments grant are the ones that count (ADR 0043).
	Project string
}

// Actor is who the process acts as: its initiator on the project of the process (what the initiator may do there
// depends on the roles they hold on that project), whatever project their token had active.
func (r ProcessRef) Actor() authz.Principal {
	a := r.Initiator
	a.Project = r.Project
	return a
}

// StepRef is the step of a process a gate is about (ADR 0035 §2): its path and the roles that carry it out.
type StepRef struct {
	Path        string
	Responsible string
	Accountable string
}

// Scope is the engine's one meeting point with who and where: the organisation and project a process works in,
// the authorization model that says what a principal may do there, and the MCPs the organisation binds. The
// engine reads its methodology and decides which gate applies; the scope answers it with plain data (ADR 0063).
type Scope interface {
	// As returns ctx acting as the actor of the process (ProcessRef.Actor).
	As(ctx context.Context, p ProcessRef) context.Context
	// HasTools reports whether a hub of MCPs is available at all.
	HasTools() bool
	// Tools lists the tools the organisation of the process binds and the MCPs it binds, as the actor.
	Tools(ctx context.Context, p ProcessRef) ([]mcp.ToolInfo, []string, error)
	// CallTool calls "<mcp>/<tool>" for the organisation of the process, as the actor, on the change of the process.
	CallTool(ctx context.Context, p ProcessRef, name string, args map[string]any) (any, error)
	// Allowed reports whether who holds a permission "<type>:<action>" on the process (or its change).
	Allowed(ctx context.Context, p ProcessRef, who authz.Principal, permission string) (bool, error)
	// MayRun reports whether who holds, on the project of the process, one of the roles allowed to run an agent
	// or an action (kind "agent" or "action"); none listed: any member of the project.
	MayRun(ctx context.Context, p ProcessRef, who authz.Principal, kind, name string, roles []string) (bool, error)
	// MayStep reports whether who may act ("perform": the responsible role, "approve": the accountable one) on
	// a step of the process.
	MayStep(ctx context.Context, p ProcessRef, who authz.Principal, s StepRef, act string) (bool, error)
}

// ToolPort is the MCP hub seen by AuthzScope (ADR 0019). Tools are those of the MCPs the organization of the
// change binds; the call runs with the principal of ctx.
type ToolPort interface {
	// CallTool calls "<mcp>/<tool>" for an organization.
	CallTool(ctx context.Context, org, name string, args map[string]any) (any, error)
	// Tools lists the tools available to an organization and the MCPs it binds.
	Tools(ctx context.Context, org string) ([]mcp.ToolInfo, []string, error)
}

// AuthzScope is the Scope of the platform: an ABAC authorizer (pkg/authz) and the MCP hub.
//
// Convenience for tests and goap-dev paths: a nil Authz grants every permission and a nil Hub binds no MCP. This
// is the only place that behaviour lives; the engine itself never tests for them.
type AuthzScope struct {
	Authz authz.Authorizer
	Hub   ToolPort
}

var _ Scope = AuthzScope{}

// As implements Scope.
func (AuthzScope) As(ctx context.Context, p ProcessRef) context.Context {
	return authz.With(ctx, p.Actor())
}

// HasTools implements Scope.
func (s AuthzScope) HasTools() bool { return s.Hub != nil }

// Tools implements Scope; without a hub nothing is bound.
func (s AuthzScope) Tools(ctx context.Context, p ProcessRef) ([]mcp.ToolInfo, []string, error) {
	if s.Hub == nil {
		return nil, nil, nil
	}
	return s.Hub.Tools(s.As(ctx, p), p.Org)
}

// CallTool implements Scope.
func (s AuthzScope) CallTool(ctx context.Context, p ProcessRef, name string, args map[string]any) (any, error) {
	if s.Hub == nil {
		return nil, errors.New("no MCP hub configured")
	}
	// the built-in connectors act on the change of the process by default (ADR 0028)
	call := mcp.WithCall(s.As(ctx, p), mcp.CallContext{Change: string(p.ChangeID), Process: p.ID})
	return s.Hub.CallTool(call, p.Org, name, args)
}

// Allowed implements Scope.
func (s AuthzScope) Allowed(ctx context.Context, p ProcessRef, who authz.Principal, permission string) (bool, error) {
	if s.Authz == nil {
		return true, nil
	}
	typ, act, err := authz.ParsePermission(permission)
	if err != nil {
		return false, err
	}
	res := authz.Resource{Type: typ, Org: p.Initiator.Org, Owner: p.Initiator.Subject, Name: p.Methodology, ProjectID: p.Project}
	if typ == "change" {
		res.ID = string(p.ChangeID)
	} else {
		res.ID = p.ID
	}
	return s.Authz.Authorize(ctx, authz.Request{Subject: who, Action: act, Resource: res})
}

// MayRun implements Scope.
func (s AuthzScope) MayRun(ctx context.Context, p ProcessRef, who authz.Principal, kind, name string, roles []string) (bool, error) {
	if s.Authz == nil {
		return true, nil
	}
	return s.Authz.Authorize(ctx, authz.Request{Subject: who, Action: "run", Resource: authz.Resource{Type: kind, ID: p.ID, Name: name,
		Org: p.Org, ProjectID: p.Project, Owner: p.Initiator.Subject, Roles: roles}})
}

// MayStep implements Scope.
func (s AuthzScope) MayStep(ctx context.Context, p ProcessRef, who authz.Principal, st StepRef, act string) (bool, error) {
	if s.Authz == nil {
		return true, nil
	}
	return s.Authz.Authorize(ctx, authz.Request{Subject: who, Action: act, Resource: authz.Resource{Type: "step", ID: p.ID, Name: st.Path,
		Org: p.Org, ProjectID: p.Project, Owner: p.Initiator.Subject, Role: st.Responsible, Accountable: st.Accountable}})
}

// scope is the engine's Scope. Unset, an AuthzScope with neither authorizer nor hub: everything permitted, no MCP
// bound (tests and bare embeddings).
func (e *Engine) scope() Scope {
	if e.Scope == nil {
		return AuthzScope{}
	}
	return e.Scope
}

// ref is the snapshot of p the scope works with.
func (e *Engine) ref(p *Process) ProcessRef {
	return ProcessRef{ID: p.ID, Methodology: p.Methodology, ChangeID: p.ChangeID, Initiator: p.Initiator,
		Org: p.Org, Project: p.Project}
}

// as is ctx acting as the actor of p.
func (e *Engine) as(ctx context.Context, p *Process) context.Context {
	return e.scope().As(ctx, e.ref(p))
}
