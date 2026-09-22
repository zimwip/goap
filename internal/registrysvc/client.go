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

// Client adapts the registry to engine.MethodologyPort. Compiled
// methodologies are cached by name and version.
type Client struct {
	rpc   registryv1connect.RegistryServiceClient
	mu    sync.Mutex
	cache map[string]*methodology.Compiled
}

var _ engine.MethodologyPort = (*Client)(nil)

// NewClient returns a registry client.
func NewClient(hc *http.Client, baseURL string) *Client {
	return &Client{rpc: registryv1connect.NewRegistryServiceClient(hc, baseURL), cache: map[string]*methodology.Compiled{}}
}

// Methodology implements engine.MethodologyPort (latest version).
func (c *Client) Methodology(ctx context.Context, name string) (*methodology.Compiled, error) {
	r, err := c.rpc.GetMethodology(ctx, connect.NewRequest(&registryv1.GetMethodologyRequest{Name: name}))
	if err != nil {
		return nil, err
	}
	key := r.Msg.Methodology.Name + "@" + r.Msg.Methodology.Version
	c.mu.Lock()
	defer c.mu.Unlock()
	if m, ok := c.cache[key]; ok {
		return m, nil
	}
	m, err := methodology.Parse([]byte(r.Msg.Methodology.Source))
	if err != nil {
		return nil, err
	}
	cm, err := m.Compile()
	if err != nil {
		return nil, err
	}
	c.cache[key] = cm
	return cm, nil
}
