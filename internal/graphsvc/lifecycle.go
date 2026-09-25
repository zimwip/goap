package graphsvc

import (
	"context"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
)

// TransitionAuthorizer authorizes the lifecycle transitions of a change for
// the caller (ADR 0014). A transition declares the permission it needs
// ("type:action"); by default it is node:transition. Callers without identity
// are trusted internal services.
func TransitionAuthorizer(a authz.Authorizer) graph.TransitionAuthorizer {
	return func(ctx context.Context, n domain.Node, t domain.Transition) error {
		who := authz.From(ctx)
		if a == nil || who.Anonymous() {
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
			Resource: authz.Resource{Type: typ, ID: string(n.ID), Name: n.Key, Org: who.Org}})
	}
}
