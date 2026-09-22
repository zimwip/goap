// Package identity reads the caller identity propagated by the gateway.
package identity

import (
	"context"
	"net/http"
	"strings"

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
