package registrysvc

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/methodology"
	"github.com/zimwip/goap/pkg/typecat"
)

// Versions (ADR 0113): a change follows a version of its methodology (domain.Change.MethodologyVersion), a methodology
// version is written against versions of the domains (methodology.Methodology.Domains). The readers of a methodology
// take a reference, "<name>@<version>" (domain.MethodologyRef), a bare name being the latest published version.

// record is the stored version a reference names: the version it names, else the latest published one.
func (s *Service) record(ctx context.Context, ref string) (Record, error) {
	name, version := domain.SplitMethodologyRef(ref)
	return s.Store.Get(ctx, name, version)
}

// MethodologyVersion implements pkg/graph.ChangeLifecycles: the version a change of a methodology follows. A reference
// naming a version gives it when it is published or archived (a draft is refused: graph.ErrInvalid); a bare name gives
// the latest published version; "" for a methodology the registry does not know (its changes follow no version).
func (s *Service) MethodologyVersion(ctx context.Context, ref string) (string, error) {
	name, version := domain.SplitMethodologyRef(ref)
	r, err := s.Store.Get(ctx, name, version)
	switch {
	case errors.Is(err, ErrNotFound) && version == "":
		return "", nil
	case errors.Is(err, ErrNotFound):
		return "", fmt.Errorf("methodology %s has no version %s: %w", name, version, graph.ErrInvalid)
	case err != nil:
		return "", err
	case r.Status == StatusDraft:
		return "", fmt.Errorf("%s@%s is a draft: a change follows a published version: %w", name, version, graph.ErrInvalid)
	}
	return r.Methodology.Version, nil
}

// domainsFor are the domains a methodology version is validated and compiled against: the latest published version of
// every stored domain, replaced by the version the methodology pins (a pinned version since archived still serves
// it). A pin naming a version that does not exist, or a draft, is an error. A version published before the pins
// existed (publishedAt set, a domain it does not pin) follows the version of that domain in force when it was
// published, when there was one: what it was validated against.
func (s *Service) domainsFor(ctx context.Context, m *methodology.Methodology, publishedAt time.Time) ([]*def.Domain, error) {
	ds, err := s.Domains(ctx)
	if err != nil {
		return nil, err
	}
	var then map[string]*def.Domain
	if !publishedAt.IsZero() && len(m.Domains) < len(ds) {
		if then, err = s.domainsInForceAt(ctx, publishedAt); err != nil {
			return nil, err
		}
	}
	if len(m.Domains) == 0 && len(then) == 0 {
		return ds, nil
	}
	out := make([]*def.Domain, 0, len(ds)+len(m.Domains))
	seen := map[string]bool{}
	for _, d := range ds {
		seen[d.Name] = true
		if v, ok := m.Domains[d.Name]; ok && v != d.Version {
			pinned, err := s.pinnedDomain(ctx, d.Name, v)
			if err != nil {
				return nil, err
			}
			d = pinned
		} else if old := then[d.Name]; !ok && old != nil {
			d = old
		}
		out = append(out, d)
	}
	for _, name := range slices.Sorted(maps.Keys(m.Domains)) {
		if seen[name] || typecat.IsBuiltin(name) {
			continue
		}
		pinned, err := s.pinnedDomain(ctx, name, m.Domains[name])
		if err != nil {
			return nil, err
		}
		out = append(out, pinned)
	}
	return out, nil
}

// domainsInForceAt are the versions of the stored domains in force at a time: the one published last before it
// (archived since or not), by name; a domain first published after it is absent.
func (s *Service) domainsInForceAt(ctx context.Context, at time.Time) (map[string]*def.Domain, error) {
	store, err := s.domains()
	if err != nil {
		return nil, err
	}
	rs, err := store.ListDomains(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]*def.Domain{}
	when := map[string]time.Time{}
	for _, r := range rs {
		if r.Builtin || r.Status == StatusDraft || r.PublishedAt.IsZero() || r.PublishedAt.After(at) {
			continue
		}
		if t, ok := when[r.Domain.Name]; !ok || r.PublishedAt.After(t) {
			d := r.Domain
			out[r.Domain.Name], when[r.Domain.Name] = &d, r.PublishedAt
		}
	}
	return out, nil
}

// pinnedDomain is a version of a domain a methodology pins: published or archived.
func (s *Service) pinnedDomain(ctx context.Context, name, version string) (*def.Domain, error) {
	if version == "" {
		return nil, fmt.Errorf("domain %s is pinned with no version", name)
	}
	r, err := s.GetDomain(ctx, name, version)
	if err != nil {
		return nil, fmt.Errorf("pinned domain %s@%s: %w", name, version, err)
	}
	if r.Status == StatusDraft {
		return nil, fmt.Errorf("pinned domain %s@%s is a draft: a methodology is written against published domains", name, version)
	}
	return &r.Domain, nil
}

// typesFor is the type catalogue of a methodology version: the built-in domains and domainsFor.
func (s *Service) typesFor(ctx context.Context, m *methodology.Methodology) (*typecat.Catalog, []*def.Domain, error) {
	ds, err := s.domainsFor(ctx, m, time.Time{})
	if err != nil {
		return nil, nil, err
	}
	cat, err := typecat.New(ds...)
	return cat, ds, err
}

// domainPinIssues are the issues of the pins of a methodology: a built-in domain is never pinned, the others name a
// published (or archived) version.
func (s *Service) domainPinIssues(ctx context.Context, m *methodology.Methodology) def.Issues {
	var issues def.Issues
	for _, name := range slices.Sorted(maps.Keys(m.Domains)) {
		path := "domains." + name
		if typecat.IsBuiltin(name) {
			issues = append(issues, def.Issue{Path: path, Message: "domain " + name + " is built into the platform: it changes with the platform, it is not pinned"})
			continue
		}
		if _, err := s.pinnedDomain(ctx, name, m.Domains[name]); err != nil {
			issues = append(issues, def.Issue{Path: path, Message: err.Error()})
		}
	}
	return issues
}

// pinDomains pins, on a methodology being published, every published domain in force it does not pin yet: the
// published version keeps the types it was validated against (ADR 0113).
func (s *Service) pinDomains(ctx context.Context, m *methodology.Methodology) error {
	ds, err := s.Domains(ctx)
	if err != nil {
		return err
	}
	for _, d := range ds {
		if _, ok := m.Domains[d.Name]; ok {
			continue
		}
		if m.Domains == nil {
			m.Domains = map[string]string{}
		}
		m.Domains[d.Name] = d.Version
	}
	return nil
}
