package registrysvc

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"

	registryv1 "github.com/zimwip/goap/gen/goap/registry/v1"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/methodology"
)

// Drafts adapts an in-process registry to engine.MethodologyDrafts (the
// self-observation agent saves improved versions as drafts).
type Drafts struct{ Service *Service }

var _ engine.MethodologyDrafts = Drafts{}

// Definition implements engine.MethodologyDrafts.
func (d Drafts) Definition(ctx context.Context, name, version string) (methodology.Methodology, bool, error) {
	r, err := d.Service.Store.Get(ctx, name, version)
	if errors.Is(err, ErrNotFound) {
		return methodology.Methodology{}, false, nil
	}
	return r.Methodology, err == nil, err
}

// SaveDraft implements engine.MethodologyDrafts.
func (d Drafts) SaveDraft(ctx context.Context, m methodology.Methodology) (methodology.Issues, error) {
	_, issues, err := d.Service.Save(ctx, m)
	return issues, err
}

var _ engine.MethodologyDrafts = (*Client)(nil)

// withIdentity forwards the principal of ctx to the registry (service to
// service calls inside the platform network, as the gateway does).
func withIdentity[T any](ctx context.Context, req *connect.Request[T]) *connect.Request[T] {
	p := authz.From(ctx)
	req.Header().Set(identity.HeaderSubject, p.Subject)
	req.Header().Set(identity.HeaderOrg, p.Org)
	req.Header().Set(identity.HeaderRoles, strings.Join(p.Roles, ","))
	return req
}

// Definition implements engine.MethodologyDrafts.
func (c *Client) Definition(ctx context.Context, name, version string) (methodology.Methodology, bool, error) {
	r, err := c.rpc.GetMethodology(ctx, withIdentity(ctx, connect.NewRequest(&registryv1.GetMethodologyRequest{Name: name, Version: version})))
	if connect.CodeOf(err) == connect.CodeNotFound {
		return methodology.Methodology{}, false, nil
	}
	if err != nil {
		return methodology.Methodology{}, false, err
	}
	return FromPB(r.Msg.Methodology), true, nil
}

// SaveDraft implements engine.MethodologyDrafts.
func (c *Client) SaveDraft(ctx context.Context, m methodology.Methodology) (methodology.Issues, error) {
	r, err := c.rpc.SaveMethodology(ctx, withIdentity(ctx, connect.NewRequest(&registryv1.SaveMethodologyRequest{Methodology: ToPB(Record{Methodology: m, Status: StatusDraft})})))
	if err != nil {
		return nil, err
	}
	var out methodology.Issues
	for _, i := range r.Msg.Issues {
		out = append(out, methodology.Issue{Path: i.Path, Message: i.Message})
	}
	return out, nil
}
