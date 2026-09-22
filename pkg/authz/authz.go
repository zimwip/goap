// Package authz carries the identity of callers and decides whether they hold
// a permission. The role policy is the interim implementation until the IAM
// service (milestone M2) answers IamService.CheckPermission.
package authz

import (
	"context"
	"errors"
	"slices"
	"strings"
)

// Well-known permissions.
const (
	// PermChangeApply allows materializing a change into a new baseline.
	PermChangeApply = "change:apply"
)

// Principal is an authenticated caller.
type Principal struct {
	Subject string   `json:"subject,omitempty"`
	Org     string   `json:"org,omitempty"`
	Roles   []string `json:"roles,omitempty"`
}

// Anonymous reports whether the principal is unauthenticated.
func (p Principal) Anonymous() bool { return p.Subject == "" }

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

// ErrForbidden is returned when a principal lacks a permission.
var ErrForbidden = errors.New("permission denied")

// Authorizer decides permissions.
type Authorizer interface {
	Allowed(ctx context.Context, p Principal, permission string) (bool, error)
}

// AllowAll grants everything (tests, single-user dev).
type AllowAll struct{}

// Allowed implements Authorizer.
func (AllowAll) Allowed(context.Context, Principal, string) (bool, error) { return true, nil }

// RolePolicy maps roles to permissions. "*" grants every permission and
// "<resource>:*" every action on a resource.
type RolePolicy map[string][]string

// DefaultRoles is the platform role model.
var DefaultRoles = RolePolicy{
	"viewer":        {"process:read", "change:read"},
	"contributor":   {"process:read", "change:read", "process:start", "process:submit"},
	"methodologist": {"process:read", "change:read", "process:start", "process:submit", "methodology:publish"},
	"approver":      {"process:read", "change:read", "process:submit", PermChangeApply},
	"admin":         {"*"},
}

// Allowed implements Authorizer. Anonymous principals hold no permission.
func (r RolePolicy) Allowed(_ context.Context, p Principal, permission string) (bool, error) {
	if p.Anonymous() {
		return false, nil
	}
	resource, _, _ := strings.Cut(permission, ":")
	for _, role := range p.Roles {
		perms := r[role]
		if slices.Contains(perms, "*") || slices.Contains(perms, permission) || slices.Contains(perms, resource+":*") {
			return true, nil
		}
	}
	return false, nil
}
