package registrysvc

import (
	"context"
	"net/http"
	"sync"

	"connectrpc.com/connect"

	registryv1 "github.com/zimwip/goap/gen/goap/registry/v1"
	"github.com/zimwip/goap/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/methodology"
)

// Client adapts the registry to engine.MethodologyPort. Published versions
// are immutable, so compiled methodologies are cached by name and version.
type Client struct {
	rpc   registryv1connect.RegistryServiceClient
	mu    sync.Mutex
	cache map[string]*methodology.Compiled
}

var _ engine.MethodologyPort = (*Client)(nil)

// NewClient returns a registry client.
func NewClient(hc *http.Client, baseURL string, opts ...connect.ClientOption) *Client {
	return &Client{rpc: registryv1connect.NewRegistryServiceClient(hc, baseURL, opts...), cache: map[string]*methodology.Compiled{}}
}

// List implements engine.MethodologyPort (latest published versions).
func (c *Client) List(ctx context.Context) ([]*methodology.Compiled, error) {
	r, err := c.rpc.ListMethodologies(ctx, connect.NewRequest(&registryv1.ListMethodologiesRequest{}))
	if err != nil {
		return nil, err
	}
	var out []*methodology.Compiled
	for _, m := range r.Msg.Methodologies {
		if m.Status != string(StatusPublished) {
			continue
		}
		cm, err := c.Methodology(ctx, m.Name)
		if err != nil {
			return nil, err
		}
		out = append(out, cm)
	}
	return out, nil
}

// Domains returns the latest published version of every domain (the source of a typecat.Live catalogue).
func (c *Client) Domains(ctx context.Context) ([]*methodology.Domain, error) {
	r, err := c.rpc.ListDomains(ctx, connect.NewRequest(&registryv1.ListDomainsRequest{}))
	if err != nil {
		return nil, err
	}
	var out []*methodology.Domain
	for _, s := range r.Msg.Domains {
		if s.Status != string(StatusPublished) {
			continue
		}
		d, err := c.rpc.GetDomain(ctx, connect.NewRequest(&registryv1.GetDomainRequest{Name: s.Name, Version: s.Version}))
		if err != nil {
			return nil, err
		}
		dom := DomainFromPB(d.Msg.Domain)
		out = append(out, &dom)
	}
	return out, nil
}

// Methodology implements engine.MethodologyPort (latest published version).
func (c *Client) Methodology(ctx context.Context, name string) (*methodology.Compiled, error) {
	r, err := c.rpc.GetMethodology(ctx, connect.NewRequest(&registryv1.GetMethodologyRequest{Name: name}))
	if connect.CodeOf(err) == connect.CodeNotFound {
		return nil, engine.ErrUnknownMethodology{Name: name}
	}
	if err != nil {
		return nil, err
	}
	k := key(r.Msg.Methodology.Name, r.Msg.Methodology.Version)
	c.mu.Lock()
	defer c.mu.Unlock()
	if m, ok := c.cache[k]; ok {
		return m, nil
	}
	m := FromPB(r.Msg.Methodology)
	cm, err := m.Compile()
	if err != nil {
		return nil, err
	}
	c.cache[k] = cm
	return cm, nil
}
