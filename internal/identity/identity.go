// Package identity reads the caller identity propagated by the gateway.
package identity

import (
	"context"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"github.com/labstack/echo/v5"

	"github.com/zimwip/goap/pkg/authz"
)

// Headers set by the gateway. Services must only be reachable through the
// gateway, which always overwrites them.
const (
	HeaderSubject = "X-Goap-Subject"
	HeaderOrg     = "X-Goap-Org"
	HeaderRoles   = "X-Goap-Roles"
	HeaderProject = "X-Goap-Project"
)

// FromHeaders reads the principal of a request.
func FromHeaders(h http.Header) authz.Principal {
	p := authz.Principal{Subject: h.Get(HeaderSubject), Org: h.Get(HeaderOrg), Project: h.Get(HeaderProject)}
	if roles := h.Get(HeaderRoles); roles != "" {
		p.Roles = strings.Split(roles, ",")
	}
	return p
}

// SetHeaders writes the identity headers of a principal, for a platform service reached as a Connect service.
func SetHeaders(p authz.Principal, h http.Header) {
	h.Set(HeaderSubject, p.Subject)
	h.Set(HeaderOrg, p.Org)
	h.Set(HeaderProject, p.Project)
	h.Set(HeaderRoles, strings.Join(p.Roles, ","))
}

// Forward is the client option of a platform client that acts for its caller: the requests carry the principal of
// their context (unless they already name one).
func Forward() connect.ClientOption {
	return connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if p := authz.From(ctx); !p.Anonymous() && req.Spec().IsClient && req.Header().Get(HeaderSubject) == "" {
				SetHeaders(p, req.Header())
			}
			return next(ctx, req)
		}
	}))
}

// Extractor puts the caller identity in the context. Default is used when
// the request carries none (single-process dev without gateway).
type Extractor struct {
	Default *authz.Principal
}

// Context returns ctx carrying the caller principal.
func (x Extractor) Context(ctx context.Context, h http.Header) context.Context {
	p := FromHeaders(h)
	if p.Anonymous() && x.Default != nil {
		p = *x.Default
	}
	return authz.With(ctx, p)
}

// WhoAmI serves the caller as the platform sees it: what session makes of the principal of the request (the principal
// completed by its User node, and what the client derives its UI from, access.Session). Nil session serves the
// principal as it is; an error is a 503 (the organisation cannot be read yet).
func WhoAmI(x Extractor, session func(context.Context, authz.Principal) (any, error)) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := x.Context(c.Request().Context(), c.Request().Header)
		p := authz.From(ctx)
		if session == nil {
			return c.JSON(http.StatusOK, p)
		}
		s, err := session(ctx, p)
		if err != nil {
			return echo.NewHTTPError(http.StatusServiceUnavailable, err.Error())
		}
		return c.JSON(http.StatusOK, s)
	}
}
