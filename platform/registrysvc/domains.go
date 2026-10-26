package registrysvc

import (
	"context"
	"fmt"
	"slices"

	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/typecat"
)

// Domains is the port of the methodology registry on the domain registry (ADR 0125 §3): the methodologies are written
// against the domains, the domains know no methodology. *domainsvc.Service implements it in process, *domainsvc.Client
// over RPC.
type Domains interface {
	// DomainVersions returns every version, or the latest version of each domain (latest published if any, latest draft
	// otherwise); the built-in domains come first.
	DomainVersions(ctx context.Context, all bool) ([]def.Record, error)
	// GetDomain returns a version (empty version: the latest published one), the built-in domains included.
	GetDomain(ctx context.Context, name, version string) (def.Record, error)
}

// builtinRecords are the records of the domains shipped with the platform, as the library embeds them: the domains of a
// registry with no domain registry (tests); a domain registry stores them (ADR 0167).
func builtinRecords() []def.Record {
	var out []def.Record
	for _, d := range typecat.Builtins() {
		out = append(out, def.Record{Domain: *d, Status: StatusPublished, UpdatedBy: "platform", Protected: true})
	}
	return out
}

// domainVersions reads the domain registry; with none, the built-in domains only.
func (s *Service) domainVersions(ctx context.Context, all bool) ([]def.Record, error) {
	if s.Domains == nil {
		return builtinRecords(), nil
	}
	return s.Domains.DomainVersions(ctx, all)
}

// getDomain reads a version from the domain registry; with none, a built-in domain only.
func (s *Service) getDomain(ctx context.Context, name, version string) (def.Record, error) {
	if s.Domains != nil {
		return s.Domains.GetDomain(ctx, name, version)
	}
	for _, r := range builtinRecords() {
		if r.Domain.Name == name && (version == "" || version == r.Domain.Version) {
			return r, nil
		}
	}
	return def.Record{}, fmt.Errorf("domain %s@%s: %w", name, version, ErrNotFound)
}

// domainsInForce are the latest published version of every stored domain, the built-in ones included (ADR 0167).
func (s *Service) domainsInForce(ctx context.Context) ([]*def.Domain, error) {
	rs, err := s.domainVersions(ctx, false)
	if err != nil {
		return nil, err
	}
	var out []*def.Domain
	for _, r := range rs {
		if r.Status == StatusPublished {
			d := r.Domain
			out = append(out, &d)
		}
	}
	return out, nil
}

// DomainUsage lists the methodology versions (not archived) that act on the namespace of a domain or reference its
// types; each follows the version it pins (methodology.Methodology.Domains, ADR 0113), a draft the version in force.
func (s *Service) DomainUsage(ctx context.Context, name, _ string) ([]Record, error) {
	rs, err := s.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, r := range rs {
		if r.Status != StatusArchived && slices.Contains(r.Methodology.Namespaces(), name) {
			out = append(out, r)
		}
	}
	return out, nil
}
