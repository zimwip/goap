// Package authz carries the identity of callers and decides access with
// attribute-based rules (ABAC) evaluated by Casbin. Policies are stored and
// administered by the IAM service; this package holds the model, the
// enforcer and the request types shared by every service.
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
	Subject string   `json:"subject,omitempty"`
	Org     string   `json:"org,omitempty"`
	Roles   []string `json:"roles,omitempty"`
}

// Anonymous reports whether the principal is unauthenticated.
func (p Principal) Anonymous() bool { return p.Subject == "" }

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
}

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

// Check returns ErrForbidden when the request is denied. A nil authorizer
// grants everything.
func Check(ctx context.Context, a Authorizer, req Request) error {
	if a == nil {
		return nil
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
