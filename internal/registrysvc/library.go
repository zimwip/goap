package registrysvc

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	registryv1 "github.com/zimwip/goap/gen/goap/registry/v1"
	"github.com/zimwip/goap/pkg/algo"
)

// Algorithm returns an algorithm of a published domain (the domain library): the adapters of the
// organisational units are instances of the adapter algorithms it holds. An empty version is the
// latest published one; the version found is returned. Drafts and archived versions are not served.
func (s *Service) Algorithm(ctx context.Context, domain, version, name string) (algo.Algorithm, string, error) {
	ds, err := s.domains()
	if err != nil {
		return algo.Algorithm{}, "", err
	}
	r, err := ds.GetDomain(ctx, domain, version)
	if err != nil {
		return algo.Algorithm{}, "", err
	}
	if r.Status != StatusPublished {
		return algo.Algorithm{}, "", fmt.Errorf("domain %s@%s is %s, not published: %w", r.Domain.Name, r.Domain.Version, r.Status, ErrNotFound)
	}
	for _, a := range r.Domain.Algorithms {
		if a.Name == name {
			return a, r.Domain.Version, nil
		}
	}
	return algo.Algorithm{}, "", fmt.Errorf("domain %s@%s has no algorithm %s: %w", domain, r.Domain.Version, name, ErrNotFound)
}

// Algorithm asks the registry for an algorithm of a published domain (see Service.Algorithm).
func (c *Client) Algorithm(ctx context.Context, domain, version, name string) (algo.Algorithm, string, error) {
	r, err := c.rpc.GetDomain(ctx, connect.NewRequest(&registryv1.GetDomainRequest{Name: domain, Version: version}))
	if err != nil {
		return algo.Algorithm{}, "", err
	}
	d := r.Msg.Domain
	if d == nil {
		return algo.Algorithm{}, "", fmt.Errorf("domain %s: %w", domain, ErrNotFound)
	}
	for _, a := range algorithmsFromPB(d.Algorithms) {
		if a.Name == name {
			return a, d.Version, nil
		}
	}
	return algo.Algorithm{}, "", fmt.Errorf("domain %s@%s has no algorithm %s: %w", domain, d.Version, name, ErrNotFound)
}
