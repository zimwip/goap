package registrysvc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"connectrpc.com/connect"

	registryv1 "github.com/zimwip/goap/gen/goap/registry/v1"
	"github.com/zimwip/goap/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/internal/rpcerr"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/methodology"
)

// Handler implements registryv1connect.RegistryServiceHandler.
type Handler struct {
	Store  Store
	Events engine.Publisher
}

var _ registryv1connect.RegistryServiceHandler = (*Handler)(nil)

// Publish validates and stores a methodology source.
func (h *Handler) Publish(ctx context.Context, source string) (Record, error) {
	m, err := methodology.Parse([]byte(source))
	if err != nil {
		return Record{}, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if _, err := m.Compile(); err != nil {
		return Record{}, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if m.Version == "" {
		return Record{}, connect.NewError(connect.CodeInvalidArgument, errors.New("methodology version required"))
	}
	r := Record{Name: m.Name, Version: m.Version, Description: m.Description, Source: source, PublishedAt: time.Now().UTC()}
	for _, g := range m.Goals {
		r.Goals = append(r.Goals, Goal{Name: g.Name, Description: g.Description})
	}
	if err := h.Store.Put(ctx, r); err != nil {
		if errors.Is(err, ErrExists) {
			return Record{}, connect.NewError(connect.CodeAlreadyExists, err)
		}
		return Record{}, err
	}
	if h.Events != nil {
		_ = h.Events.Publish(ctx, "goap.registry.methodology.published", map[string]string{"name": r.Name, "version": r.Version})
	}
	return r, nil
}

// LoadDir publishes every *.yaml file of dir (used at startup in dev).
func (h *Handler) LoadDir(ctx context.Context, dir string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.y*ml"))
	if err != nil {
		return nil, err
	}
	var loaded []string
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			return loaded, err
		}
		r, err := h.Publish(ctx, string(src))
		if err != nil {
			return loaded, err
		}
		loaded = append(loaded, r.Name+"@"+r.Version)
	}
	return loaded, nil
}

func toPB(r Record) *registryv1.Methodology {
	out := &registryv1.Methodology{Name: r.Name, Version: r.Version, Description: r.Description, Source: r.Source, PublishedAt: pbconv.Time(r.PublishedAt)}
	for _, g := range r.Goals {
		out.Goals = append(out.Goals, &registryv1.GoalSummary{Name: g.Name, Description: g.Description})
	}
	return out
}

func (h *Handler) PublishMethodology(ctx context.Context, req *connect.Request[registryv1.PublishMethodologyRequest]) (*connect.Response[registryv1.PublishMethodologyResponse], error) {
	r, err := h.Publish(ctx, req.Msg.Source)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	return connect.NewResponse(&registryv1.PublishMethodologyResponse{Methodology: toPB(r)}), nil
}

func (h *Handler) GetMethodology(ctx context.Context, req *connect.Request[registryv1.GetMethodologyRequest]) (*connect.Response[registryv1.GetMethodologyResponse], error) {
	r, err := h.Store.Get(ctx, req.Msg.Name, req.Msg.Version)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	return connect.NewResponse(&registryv1.GetMethodologyResponse{Methodology: toPB(r)}), nil
}

func (h *Handler) ListMethodologies(ctx context.Context, _ *connect.Request[registryv1.ListMethodologiesRequest]) (*connect.Response[registryv1.ListMethodologiesResponse], error) {
	rs, err := h.Store.List(ctx)
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	out := &registryv1.ListMethodologiesResponse{}
	for _, r := range rs {
		m := toPB(r)
		m.Source = ""
		out.Methodologies = append(out.Methodologies, m)
	}
	return connect.NewResponse(out), nil
}
