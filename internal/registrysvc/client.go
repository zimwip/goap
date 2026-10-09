package registrysvc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"connectrpc.com/connect"

	registryv1 "github.com/zimwip/goap/gen/goap/registry/v1"
	"github.com/zimwip/goap/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/methodology"
)

// Client adapts the registry to methodology.Source. Published versions
// are immutable, so compiled methodologies are cached by name and version.
type Client struct {
	rpc   registryv1connect.RegistryServiceClient
	mu    sync.Mutex
	cache map[string]*methodology.Compiled
}

var _ methodology.Source = (*Client)(nil)

// NewClient returns a registry client.
func NewClient(hc *http.Client, baseURL string, opts ...connect.ClientOption) *Client {
	return &Client{rpc: registryv1connect.NewRegistryServiceClient(hc, baseURL, opts...), cache: map[string]*methodology.Compiled{}}
}

// List implements methodology.Source (latest published versions).
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
func (c *Client) Domains(ctx context.Context) ([]*def.Domain, error) {
	r, err := c.rpc.ListDomains(ctx, connect.NewRequest(&registryv1.ListDomainsRequest{}))
	if err != nil {
		return nil, err
	}
	var out []*def.Domain
	for _, s := range r.Msg.Domains {
		if s.Status != string(StatusPublished) || s.Builtin {
			continue // the catalogue adds the built-in domains itself
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

// Methodology implements methodology.Source (latest published version).
func (c *Client) Methodology(ctx context.Context, name string) (*methodology.Compiled, error) {
	r, err := c.rpc.GetMethodology(ctx, connect.NewRequest(&registryv1.GetMethodologyRequest{Name: name}))
	if connect.CodeOf(err) == connect.CodeNotFound {
		return nil, methodology.ErrUnknown{Name: name}
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

// Lifecycle implements engine.LifecyclePort for a remote engine: the lifecycle the latest published version of a
// methodology names, defined by a domain (the one of its namespace first); nil when it names none.
func (c *Client) Lifecycle(ctx context.Context, name string) (*domain.Lifecycle, error) {
	m, err := c.Methodology(ctx, name)
	if err != nil {
		var unknown methodology.ErrUnknown
		if errors.As(err, &unknown) {
			return nil, nil
		}
		return nil, err
	}
	if m.Lifecycle == "" {
		return nil, nil
	}
	ds, err := c.Domains(ctx)
	if err != nil {
		return nil, err
	}
	var found *domain.Lifecycle
	for _, d := range append(def.BuiltinDomains(), ds...) {
		if lc := d.Lifecycle(m.Lifecycle); lc != nil && (found == nil || d.Name == m.Namespace) {
			found = lc
		}
	}
	if found == nil {
		return nil, fmt.Errorf("methodology %s names lifecycle %q, which no domain defines: %w", m.Name, m.Lifecycle, ErrInvalid)
	}
	return found, nil
}
