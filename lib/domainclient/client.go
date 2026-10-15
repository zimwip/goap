// Package domainclient reads the domain registry (registry.v1.DomainService, served by the graph) over Connect: the
// client the engine, the methodology registry and the MCP hub share (ADR 0125 §3).
package domainclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"connectrpc.com/connect"

	registryv1 "github.com/zimwip/goap/contracts/gen/goap/registry/v1"
	"github.com/zimwip/goap/contracts/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/domainpb"
	"github.com/zimwip/goap/lib/pbconv"
)

// ErrNotFound is returned for an unknown domain version.
var ErrNotFound = errors.New("domain not found")

// DefaultTTL is how long a Client keeps the domains it read when nothing invalidates them.
const DefaultTTL = 30 * time.Second

// Client reads the domain registry over Connect: the source of a typecat.Live catalogue (Domains) and the domains a
// methodology registry compiles against (DomainVersions, GetDomain). A published version is immutable and the domains
// are few, so it keeps every version it read, until Invalidate (on a domain event) or for TTL.
type Client struct {
	rpc registryv1connect.DomainServiceClient
	// TTL bounds how long the versions read are kept (DefaultTTL when zero).
	TTL time.Duration

	mu     sync.Mutex
	all    []def.Record // every version, the built-in domains first; nil: to read
	loaded time.Time
}

// NewClient returns a domain registry client.
func NewClient(hc *http.Client, baseURL string, opts ...connect.ClientOption) *Client {
	return &Client{rpc: registryv1connect.NewDomainServiceClient(hc, baseURL, opts...)}
}

// Invalidate drops what the client read: the next call reads the registry again.
func (c *Client) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.all = nil
}

// versions returns every version of every domain, read once per TTL.
func (c *Client) versions(ctx context.Context) ([]def.Record, error) {
	ttl := c.TTL
	if ttl == 0 {
		ttl = DefaultTTL
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.all != nil && time.Since(c.loaded) < ttl {
		return c.all, nil
	}
	list, err := c.rpc.ListDomains(ctx, connect.NewRequest(&registryv1.ListDomainsRequest{AllVersions: true}))
	if err != nil {
		return nil, err
	}
	all := make([]def.Record, 0, len(list.Msg.Domains))
	for _, s := range list.Msg.Domains {
		d, err := c.rpc.GetDomain(ctx, connect.NewRequest(&registryv1.GetDomainRequest{Name: s.Name, Version: s.Version}))
		if err != nil {
			return nil, err
		}
		p := d.Msg.Domain
		all = append(all, def.Record{Domain: domainpb.DomainFromPB(p), Status: def.Status(p.Status), CreatedAt: pbconv.FromTime(p.CreatedAt),
			UpdatedAt: pbconv.FromTime(p.UpdatedAt), PublishedAt: pbconv.FromTime(p.PublishedAt), UpdatedBy: p.UpdatedBy, Builtin: p.Builtin})
	}
	c.all, c.loaded = all, time.Now()
	return all, nil
}

// Domains returns the latest published version of every stored domain (the source of a typecat.Live catalogue, which
// adds the built-in domains itself).
func (c *Client) Domains(ctx context.Context) ([]*def.Domain, error) {
	rs, err := c.versions(ctx)
	if err != nil {
		return nil, err
	}
	return def.InForce(rs), nil
}

// DomainVersions returns every version, or the latest version of each domain; the built-in domains come first.
func (c *Client) DomainVersions(ctx context.Context, all bool) ([]def.Record, error) {
	rs, err := c.versions(ctx)
	if err != nil || all {
		return rs, err
	}
	return def.Latest(rs), nil
}

// GetDomain returns a version (empty version: the latest published one).
func (c *Client) GetDomain(ctx context.Context, name, version string) (def.Record, error) {
	rs, err := c.versions(ctx)
	if err != nil {
		return def.Record{}, err
	}
	var best def.Record
	found := false
	for _, r := range rs {
		switch {
		case r.Domain.Name != name:
		case version != "":
			if r.Domain.Version == version {
				return r, nil
			}
		case r.Status == def.StatusPublished && (!found || r.PublishedAt.After(best.PublishedAt)):
			best, found = r, true
		}
	}
	if !found {
		return def.Record{}, fmt.Errorf("%s@%s: %w", name, version, ErrNotFound)
	}
	return best, nil
}
