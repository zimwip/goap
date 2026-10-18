// Package authz carries the identity of callers and decides access with
// attribute-based rules (ABAC) evaluated by Casbin. Policies are Policy nodes of the
// graph (ADR 0020, read by pkg/access); this package holds the model, the enforcer
// and the request types shared by every service.
package authz

import (
	"context"
	"errors"
	"slices"
	"strings"
)

// Principal is an authenticated caller (subject attributes of ABAC rules:
// r.sub.Subject, r.sub.Org, r.sub.Roles).
type Principal struct {
	Subject string `json:"subject,omitempty"`
	Org     string `json:"org,omitempty"`
	// Project is the caller's active project (ADR 0039), from their token; a call that names no project
	// of its own (Change.ProjectID, StartProcessRequest.project_id, ...) defaults to it.
	Project string   `json:"project,omitempty"`
	Roles   []string `json:"roles,omitempty"`
	// Global are the platform roles (ADR 0046, 0047: admin, reader) the principal holds everywhere, kept apart from
	// Roles so that holding one is never mistaken for working on a project (OnProject). hasRole sees both.
	Global []string `json:"global,omitempty"`
	// ProjectAdmin and ProjectOwner are the flags of the project Assignments of the subject's chain covering the
	// project of the resource being checked (ADR 0135): set by the authorizer that knows the organisation, for that one
	// request. The owner possesses the project (ProjectOwner implies ProjectAdmin), the administrator administers it.
	ProjectAdmin bool `json:"projectAdmin,omitempty"`
	ProjectOwner bool `json:"projectOwner,omitempty"`
	// OnBehalfOf is the person a platform service acts for (System): the end user whose action led, from one
	// consequence to the next (an event, a trigger, a run, a sub-agent), to what the service does. It attributes the
	// work (the LLM call ledger, ADR 0114) and never grants anything: authorization reads the Subject. Ignored on a
	// person, who acts for themselves.
	OnBehalfOf string `json:"onBehalfOf,omitempty"`
}

// Anonymous reports whether the principal is unauthenticated.
func (p Principal) Anonymous() bool { return p.Subject == "" }

// SystemPrefix starts the subject of a platform service acting by itself (system:registry, system:trigger:<key>,
// system:graph): never a person, so never a user of the organisation.
const SystemPrefix = "system:"

// System is the identity of the platform service name acting by itself ("registry", "graph", "trigger:<key>"): the
// one constructor of such principals, so that no caller spells the prefix by hand.
func System(name string, roles ...string) Principal {
	return Principal{Subject: SystemPrefix + name, Roles: roles}
}

// System reports whether the principal is a platform service rather than a person.
func (p Principal) System() bool { return strings.HasPrefix(p.Subject, SystemPrefix) }

// EndUser is the person the principal's work is attributed to (ADR 0114): a person themselves, a platform service the
// person it acts for (OnBehalfOf, "" when it acts for nobody).
func (p Principal) EndUser() string {
	if p.System() {
		return p.OnBehalfOf
	}
	return p.Subject
}

// For returns the principal acting for the end user user (ADR 0114): a platform service takes it as OnBehalfOf (kept
// when user is ""); a person is unchanged, acting for themselves.
func (p Principal) For(user string) Principal {
	if user != "" && p.System() {
		p.OnBehalfOf = user
	}
	return p
}

// Resource is the object of an access request (r.obj.Type, r.obj.ID,
// r.obj.Org, r.obj.Owner, r.obj.Name).
type Resource struct {
	Type      string `json:"type"`                // change, process, methodology, policy…
	ID        string `json:"id,omitempty"`        // resource identifier
	Org       string `json:"org,omitempty"`       // owning organization
	Namespace string `json:"namespace,omitempty"` // graph namespace of the object
	Owner     string `json:"owner,omitempty"`     // subject who created / owns it
	Name      string `json:"name,omitempty"`      // human name (methodology name, goal…)
	// Role and Accountable are the responsible and accountable roles of a step of a process (ADR 0035 §2).
	Role        string `json:"role,omitempty"`
	Accountable string `json:"accountable,omitempty"`
	// OrgChain is Org followed by its ancestor units (part_of), filled by the authorizer that knows the
	// organisation: a role held in a unit holds in the units below it.
	OrgChain []string `json:"orgChain,omitempty"`
	// ProjectID is the project the resource's change belongs to (ADR 0039); when set, the authorizer that
	// knows the organisation merges the roles an Assignment grants the subject's org chain on this
	// project's chain into the subject's roles before evaluating the request.
	ProjectID string `json:"projectId,omitempty"`
	// Roles are the roles allowed to run an agent or an action (ADR 0043): one of them is required, held on
	// the project; none means any member of the project.
	Roles []string `json:"roles,omitempty"`
}

// HasRole reports whether the principal holds a role, on the project of the request (Roles) or platform-wide (Global).
// AllRoles are the roles held on the project and the platform roles, once each: what the identity headers carry
// (ADR 0135), the receiving side telling the platform roles apart again.
func (p Principal) AllRoles() []string {
	out := slices.Clone(p.Roles)
	for _, r := range p.Global {
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}

func (p Principal) HasRole(role string) bool {
	return slices.Contains(p.Roles, role) || slices.Contains(p.Global, role)
}

// AnyRole is the role an agent, an action, an operation or a model lists to be open to every member of the project
// (ADR 0135): an empty list grants nothing.
const AnyRole = "*"

// RoleScope separates a role from the unit it is held in: "developer@TEAM-PAY".
const RoleScope = "@"

// HasRoleIn reports whether the principal holds the role for a resource: unscoped ("developer"), or held in the
// resource's unit or one of its ancestors ("developer@DEP-IT" holds for every unit below DEP-IT).
func (p Principal) HasRoleIn(role string, res Resource) bool {
	if role == "" {
		return false
	}
	chain := res.OrgChain
	if len(chain) == 0 && res.Org != "" {
		chain = []string{res.Org}
	}
	for _, r := range p.Roles {
		name, unit, scoped := strings.Cut(r, RoleScope)
		if name != role {
			continue
		}
		if !scoped || slices.Contains(chain, unit) {
			return true
		}
	}
	return false
}

// OnProject reports whether the principal works on the project of the resource it is checked against: it holds a role
// an Assignment grants there (merged by the authorizer that knows the organisation, ADR 0043), one its token carries
// (an external identity provider, a service identity), or administers the project (ADR 0135). A platform role
// (Global) is not a role on a project.
func (p Principal) OnProject() bool { return len(p.Roles) > 0 || p.ProjectAdmin || p.ProjectOwner }

// MayRun reports whether the principal may run an agent or an action allowed to res.Roles (ADR 0135): one of them held
// on the project, any role there when the list holds AnyRole, and an administrator of the project whatever the list. An
// empty list grants nothing else: it is explicit or it is closed.
func (p Principal) MayRun(res Resource) bool {
	if p.ProjectAdmin || p.ProjectOwner {
		return true
	}
	for _, r := range res.Roles {
		if r == AnyRole {
			if len(p.Roles) > 0 {
				return true
			}
			continue
		}
		if p.HasRoleIn(r, res) {
			return true
		}
	}
	return false
}

// Request is an ABAC access request.
type Request struct {
	Subject  Principal
	Action   string
	Resource Resource
}

// ParsePermission splits "type:action" (e.g. "change:apply").
func ParsePermission(p string) (resourceType, action string, err error) {
	t, a, ok := strings.Cut(p, ":")
	if !ok || t == "" || a == "" {
		return "", "", errors.New("permission must be <resource>:<action>")
	}
	return t, a, nil
}

type ctxKey struct{}

// With returns a context carrying the principal.
func With(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// From returns the principal of the context (anonymous when absent).
func From(ctx context.Context) Principal {
	p, _ := ctx.Value(ctxKey{}).(Principal)
	return p
}

// ErrForbidden is returned when access is denied.
var ErrForbidden = errors.New("permission denied")

// Authorizer decides access requests.
type Authorizer interface {
	Authorize(ctx context.Context, req Request) (bool, error)
}

// AllowAll grants every request: the explicit opening of a test or of the gateway-less dev mode (ADR 0135), never a
// default.
type AllowAll struct{}

// Authorize implements Authorizer.
func (AllowAll) Authorize(context.Context, Request) (bool, error) { return true, nil }

// Check returns ErrForbidden when the request is denied. A nil authorizer denies everything (ADR 0135: without an
// explicit grant, everything is refused); AllowAll is the explicit opening.
func Check(ctx context.Context, a Authorizer, req Request) error {
	if a == nil {
		return fmtForbidden(req)
	}
	ok, err := a.Authorize(ctx, req)
	if err != nil {
		return err
	}
	if !ok {
		return fmtForbidden(req)
	}
	return nil
}

func fmtForbidden(req Request) error {
	who := req.Subject.Subject
	if who == "" {
		who = "anonymous"
	}
	return errors.Join(ErrForbidden, errors.New(who+" may not "+req.Action+" "+req.Resource.Type+" "+req.Resource.ID))
}
