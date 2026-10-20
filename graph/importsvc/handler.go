package importsvc

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	importerv1 "github.com/zimwip/goap/contracts/gen/goap/importer/v1"
	"github.com/zimwip/goap/contracts/gen/goap/importer/v1/importerv1connect"
	registryv1 "github.com/zimwip/goap/contracts/gen/goap/registry/v1"
	"github.com/zimwip/goap/lib/domainpb"
	"github.com/zimwip/goap/lib/identity"
	"github.com/zimwip/goap/lib/pbconv"
)

// Handler implements importerv1connect.ImportServiceHandler: every request acts for its caller.
type Handler struct {
	Service  *Service
	Identity identity.Extractor
}

var _ importerv1connect.ImportServiceHandler = (*Handler)(nil)

func rpcErr(err error) error {
	var ce *connect.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ce):
		return err
	case errors.Is(err, ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, ErrUnavailable):
		return connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

// ListImporters implements the protocol.
func (h *Handler) ListImporters(ctx context.Context, r *connect.Request[importerv1.ListImportersRequest]) (*connect.Response[importerv1.ListImportersResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	ims, err := h.Service.Importers(ctx, r.Msg.Namespace)
	if err != nil {
		return nil, rpcErr(err)
	}
	out := &importerv1.ListImportersResponse{}
	for _, im := range ims {
		out.Importers = append(out.Importers, &importerv1.Importer{Domain: im.Domain, DomainVersion: im.DomainVersion, Name: im.Def.Name,
			Label: im.Def.Label, Description: im.Def.Description, Params: params(im),
			Available: im.Service != "", Service: im.Service})
	}
	return connect.NewResponse(out), nil
}

// StartImport implements the protocol.
func (h *Handler) StartImport(ctx context.Context, r *connect.Request[importerv1.StartImportRequest]) (*connect.Response[importerv1.StartImportResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	m := r.Msg
	job, err := h.Service.Start(ctx, StartRequest{Domain: m.Domain, Importer: m.Importer, Params: pbconv.Map(m.Params), ProjectID: m.ProjectId, OwnerOrg: m.OwnerOrg})
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&importerv1.StartImportResponse{Job: job}), nil
}

// params are the parameters of an importer, an enum one with its values.
func params(im Importer) []*registryv1.AttributeInfo {
	var out []*registryv1.AttributeInfo
	for i, a := range domainpb.AttributesToPB(im.Def.Params) {
		info := &registryv1.AttributeInfo{Attribute: a}
		for _, v := range im.Enums[im.Def.Params[i].Enum] {
			info.Values = append(info.Values, &registryv1.EnumValue{Value: v.Value, Label: v.Label})
		}
		out = append(out, info)
	}
	return out
}
