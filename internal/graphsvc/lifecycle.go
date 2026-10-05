package graphsvc

import (
	"context"
	"fmt"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/criticality"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/risk"
)

// TransitionAuthorizer authorizes the lifecycle transitions of a change for
// the caller (ADR 0014). A transition declares the permission it needs
// ("type:action"); by default it is node:transition. A nil authorizer grants
// everything; an anonymous caller is refused by it, internal services act as
// a named system principal (SystemPrincipal).
func TransitionAuthorizer(a authz.Authorizer) graph.TransitionAuthorizer {
	return func(ctx context.Context, n domain.Node, t domain.Transition) error {
		who := authz.From(ctx)
		if a == nil {
			return nil
		}
		typ, action := "node", "transition"
		if t.Permission != "" {
			var err error
			if typ, action, err = authz.ParsePermission(t.Permission); err != nil {
				return err
			}
		}
		return authz.Check(ctx, a, authz.Request{Subject: who, Action: action,
			Resource: authz.Resource{Type: typ, ID: string(n.ID), Name: n.Key, Org: who.Org, ProjectID: who.Project}})
	}
}

// ChangeTransitionAuthorizer authorizes the transitions of the lifecycle of a change for the caller (ADR 0058): the
// permission a transition declares, by default change:transition, checked on the project of the change.
func ChangeTransitionAuthorizer(a authz.Authorizer) graph.ChangeTransitionAuthorizer {
	return func(ctx context.Context, c domain.Change, t domain.Transition) error {
		who := authz.From(ctx)
		if a == nil {
			return nil
		}
		typ, action := "change", "transition"
		if t.Permission != "" {
			var err error
			if typ, action, err = authz.ParsePermission(t.Permission); err != nil {
				return err
			}
		}
		return authz.Check(ctx, a, authz.Request{Subject: who, Action: action,
			Resource: authz.Resource{Type: typ, ID: string(c.ID), Name: c.Title, Org: c.OwnerOrg, ProjectID: c.ProjectID}})
	}
}

// ItemAuthorizer authorizes the write of the items whose kind asks a permission of its writer (domain.RequireItemPermission,
// ADR 0075: a derogation asks derogation:sign), checked on the project of the change. When the kind names the field that
// holds who answers for the item, that must be the writer: one signs for oneself. A platform service acting by itself
// (authz.Principal.System) writes on behalf of the rules it applies (the expiry of a derogation) and is not asked. A nil
// authorizer grants everything. A derogation also needs the role its criticality level asks of a signatory (ADR 0075 §3,
// criticality.Policy.SignatoryRole, resolved by resolve; nil: the compiled-in table), checked as derogation:sign-role.
func ItemAuthorizer(a authz.Authorizer, resolve criticality.Resolver) graph.ItemAuthorizer {
	return func(ctx context.Context, c domain.Change, it domain.ChangeItem, p domain.ItemPermission) error {
		if a == nil {
			return nil
		}
		who := authz.From(ctx)
		if who.System() {
			return nil
		}
		typ, action, err := authz.ParsePermission(p.Permission)
		if err != nil {
			return err
		}
		if err := authz.Check(ctx, a, authz.Request{Subject: who, Action: action,
			Resource: authz.Resource{Type: typ, ID: string(c.ID), Name: c.Title, Org: c.OwnerOrg, ProjectID: c.ProjectID}}); err != nil {
			return err
		}
		if p.SubjectField != "" {
			if s, _ := it.Data[p.SubjectField].(string); s != who.Subject {
				return fmt.Errorf("%s %q must be the one who writes it (%s): %w", p.SubjectField, s, who.Subject, authz.ErrForbidden)
			}
		}
		if it.Kind == risk.KindDerogation {
			if st, _ := it.Data["status"].(string); st != risk.DerogationClosed {
				if resolve == nil {
					resolve = criticality.DefaultResolver
				}
				lvl := criticality.Of(c.Data)
				if role := resolve(ctx, c, lvl).SignatoryRole; role != "" {
					if err := authz.Check(ctx, a, authz.Request{Subject: who, Action: "sign-role",
						Resource: authz.Resource{Type: "derogation", ID: string(c.ID), Name: c.Title, Org: c.OwnerOrg, ProjectID: c.ProjectID, Roles: []string{role}}}); err != nil {
						return fmt.Errorf("a derogation on a %s change is signed by the role %s: %w", lvl, role, err)
					}
				}
			}
		}
		return nil
	}
}
