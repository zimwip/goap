package registrysvc

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/contracts/domain"
	registryv1 "github.com/zimwip/goap/contracts/gen/goap/registry/v1"
	"github.com/zimwip/goap/contracts/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/engine/engine"
	"github.com/zimwip/goap/engine/methodology"
	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/domainpb"
)

// Client adapts the registry to engine.MethodologyPort. It builds the activable form of a version as the registry does
// (Activate over the definition and the domains the registry says it compiles against); published versions are
// immutable, so compiled methodologies are cached by name, version and those domains.
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

// List implements engine.MethodologyPort (latest published versions, the suspended ones left out).
func (c *Client) List(ctx context.Context) ([]*methodology.Compiled, error) {
	r, err := c.rpc.ListMethodologies(ctx, connect.NewRequest(&registryv1.ListMethodologiesRequest{}))
	if err != nil {
		return nil, err
	}
	var out []*methodology.Compiled
	for _, m := range r.Msg.Methodologies {
		if m.Status != string(StatusPublished) || m.Suspended != "" {
			continue // a suspended version is not in force (ADR 0125 §3)
		}
		cm, err := c.Methodology(ctx, m.Name)
		if err != nil {
			return nil, err
		}
		out = append(out, cm)
	}
	return out, nil
}

// Methodology implements engine.MethodologyPort: the version a reference names (domain.MethodologyRef, ADR 0113), else
// the latest published one.
func (c *Client) Methodology(ctx context.Context, ref string) (*methodology.Compiled, error) {
	name, version := domain.SplitMethodologyRef(ref)
	r, err := c.rpc.GetMethodology(ctx, connect.NewRequest(&registryv1.GetMethodologyRequest{Name: name, Version: version, WithDomains: true}))
	if connect.CodeOf(err) == connect.CodeNotFound {
		return nil, engine.ErrUnknownMethodology{Name: ref}
	}
	if err != nil {
		return nil, err
	}
	ds := make([]*def.Domain, 0, len(r.Msg.Domains))
	for _, p := range r.Msg.Domains {
		d := domainpb.DomainFromPB(p)
		ds = append(ds, &d)
	}
	// the version and the domains it compiles against: a version that pins none follows the domains in force
	k := key(r.Msg.Methodology.Name, r.Msg.Methodology.Version) + "|" + typesStamp(ds)
	if why := r.Msg.Methodology.Suspended; why != "" {
		return nil, suspendedError{ref: key(r.Msg.Methodology.Name, r.Msg.Methodology.Version), cause: errors.New(why)}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if m, ok := c.cache[k]; ok {
		return m, nil
	}
	// the activable form the registry builds, by the same compilation of the same inputs (ADR 0126 step 6)
	cm, err := Activate(FromPB(r.Msg.Methodology), ds)
	if err != nil {
		return nil, err
	}
	c.cache[k] = cm
	return cm, nil
}
