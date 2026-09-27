// Package identity reads the caller identity propagated by the gateway.
package identity

import (
	"context"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"

	"github.com/zimwip/goap/pkg/authz"
)

// Headers set by the gateway. Services must only be reachable through the
// gateway, which always overwrites them.
const (
	HeaderSubject = "X-Goap-Subject"
	HeaderOrg     = "X-Goap-Org"
	HeaderRoles   = "X-Goap-Roles"
)

// FromHeaders reads the principal of a request.
func FromHeaders(h http.Header) authz.Principal {
	p := authz.Principal{Subject: h.Get(HeaderSubject), Org: h.Get(HeaderOrg)}
	if roles := h.Get(HeaderRoles); roles != "" {
		p.Roles = strings.Split(roles, ",")
	}
	return p
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

// WhoAmI serves the caller as the platform sees it: the principal of the request, completed by enrich
// (its User node in the graph). Nil enrich returns the principal as it is.
func WhoAmI(x Extractor, enrich func(context.Context, authz.Principal) authz.Principal) echo.HandlerFunc {
	return func(c *echo.Context) error {
		ctx := x.Context(c.Request().Context(), c.Request().Header)
		p := authz.From(ctx)
		if enrich != nil {
			p = enrich(ctx, p)
		}
		return c.JSON(http.StatusOK, p)
	}
}
