package registrysvc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/methodology"
)

// ErrNoDomainStore is returned when the Store does not hold domains.
var ErrNoDomainStore = errors.New("the registry store does not support domains")

func (s *Service) domains() (DomainStore, error) {
	if ds, ok := s.Store.(DomainStore); ok {
		return ds, nil
	}
	return nil, ErrNoDomainStore
}

// resolver looks domains up for Methodology.Resolve. An unpinned reference
// resolves to the latest published version, a pinned one to that version,
// draft included (so that a draft methodology can be edited against a draft
// domain; publishing requires a published one).
func (s *Service) resolver(ctx context.Context) methodology.DomainResolver {
	return func(name, version string) (*methodology.Domain, error) {
		ds, err := s.domains()
		if err != nil {
			return nil, err
		}
		r, err := ds.GetDomain(ctx, name, version)
		if err != nil {
			return nil, err
		}
		if r.Status == StatusArchived {
			return nil, fmt.Errorf("%s@%s is archived", r.Domain.Name, r.Domain.Version)
		}
		return &r.Domain, nil
	}
}

// requirePublishedDomain refuses to publish a methodology on a draft domain.
func (s *Service) requirePublishedDomain(ctx context.Context, m *methodology.Methodology) error {
	if m.DomainRef == "" {
		return nil
	}
	name, version, err := methodology.SplitRef(m.DomainRef)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	ds, err := s.domains()
	if err != nil {
		return err
	}
	r, err := ds.GetDomain(ctx, name, version)
	if err != nil {
		return fmt.Errorf("%w: domain %s: %v", ErrInvalid, m.DomainRef, err)
	}
	if r.Status != StatusPublished {
		return fmt.Errorf("%w: domain %s is %s, publish it first", ErrInvalid, m.DomainRef, r.Status)
	}
	return nil
}

func (s *Service) authorizeDomain(ctx context.Context, action string, d *methodology.Domain) error {
	return authz.Check(ctx, s.Authz, authz.Request{Subject: authz.From(ctx), Action: action,
		Resource: authz.Resource{Type: "domain", ID: d.Name + "@" + d.Version, Name: d.Name, Org: authz.From(ctx).Org}})
}

func (s *Service) publishDomainEvent(ctx context.Context, event string, r DomainRecord) {
	if s.Events != nil {
		_ = s.Events.Publish(ctx, "goap.registry.domain."+event, map[string]string{"name": r.Domain.Name, "version": r.Domain.Version, "status": string(r.Status)})
	}
}

// GetDomain returns a version (empty version: latest published).
func (s *Service) GetDomain(ctx context.Context, name, version string) (DomainRecord, error) {
	ds, err := s.domains()
	if err != nil {
		return DomainRecord{}, err
	}
	return ds.GetDomain(ctx, name, version)
}

// DomainVersions returns every version, or the latest version of each domain
// (latest published if any, latest draft otherwise).
func (s *Service) DomainVersions(ctx context.Context, all bool) ([]DomainRecord, error) {
	ds, err := s.domains()
	if err != nil {
		return nil, err
	}
	rs, err := ds.ListDomains(ctx)
	if err != nil || all {
		return rs, err
	}
	latest := map[string]DomainRecord{}
	var order []string
	for _, r := range rs {
		cur, ok := latest[r.Domain.Name]
		if !ok {
			order = append(order, r.Domain.Name)
		}
		better := !ok ||
			(r.Status == StatusPublished && (cur.Status != StatusPublished || r.PublishedAt.After(cur.PublishedAt))) ||
			(cur.Status != StatusPublished && r.Status != StatusArchived && r.CreatedAt.After(cur.CreatedAt))
		if better {
			latest[r.Domain.Name] = r
		}
	}
	out := make([]DomainRecord, 0, len(order))
	for _, n := range order {
		out = append(out, latest[n])
	}
	return out, nil
}

// SaveDomain creates or replaces a draft; invalid drafts are stored and
// their issues returned.
func (s *Service) SaveDomain(ctx context.Context, d methodology.Domain) (DomainRecord, methodology.Issues, error) {
	if d.Name == "" || d.Version == "" {
		return DomainRecord{}, nil, fmt.Errorf("name and version are required: %w", ErrInvalid)
	}
	ds, err := s.domains()
	if err != nil {
		return DomainRecord{}, nil, err
	}
	if err := s.authorizeDomain(ctx, "write", &d); err != nil {
		return DomainRecord{}, nil, err
	}
	now := s.clock()
	r := DomainRecord{Domain: d, Status: StatusDraft, CreatedAt: now, UpdatedAt: now, UpdatedBy: authz.From(ctx).Subject}
	if err := ds.SaveDomain(ctx, r); err != nil {
		return DomainRecord{}, nil, err
	}
	saved, err := ds.GetDomain(ctx, d.Name, d.Version)
	if err != nil {
		return DomainRecord{}, nil, err
	}
	s.publishDomainEvent(ctx, "saved", saved)
	return saved, d.Validate(), nil
}

// methodologiesUsing returns the methodology versions referencing the
// domain version: pinned to it, or unpinned (following the latest published
// version) when floating is set.
func (s *Service) methodologiesUsing(ctx context.Context, name, version string, floating bool) ([]Record, error) {
	rs, err := s.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, r := range rs {
		if r.Status == StatusArchived || r.Methodology.DomainRef == "" {
			continue
		}
		n, v, err := methodology.SplitRef(r.Methodology.DomainRef)
		if err != nil || n != name {
			continue
		}
		if v == version || (floating && v == "") {
			out = append(out, r)
		}
	}
	return out, nil
}

// DomainUsage lists the methodology versions referencing a domain version
// (unpinned references included).
func (s *Service) DomainUsage(ctx context.Context, name, version string) ([]Record, error) {
	return s.methodologiesUsing(ctx, name, version, true)
}

// PublishDomain freezes a valid draft. Published methodologies that follow
// the latest published domain (unpinned reference) must stay valid against it.
func (s *Service) PublishDomain(ctx context.Context, name, version string) (DomainRecord, error) {
	ds, err := s.domains()
	if err != nil {
		return DomainRecord{}, err
	}
	r, err := ds.GetDomain(ctx, name, version)
	if err != nil {
		return DomainRecord{}, err
	}
	if err := s.authorizeDomain(ctx, "publish", &r.Domain); err != nil {
		return DomainRecord{}, err
	}
	if r.Status != StatusDraft {
		return DomainRecord{}, fmt.Errorf("domain %s@%s: %w", name, version, ErrImmutable)
	}
	if issues := r.Domain.Validate(); len(issues) > 0 {
		return DomainRecord{}, fmt.Errorf("%w: %v", ErrInvalid, issues)
	}
	users, err := s.methodologiesUsing(ctx, name, "", true)
	if err != nil {
		return DomainRecord{}, err
	}
	for _, u := range users {
		if u.Status != StatusPublished {
			continue
		}
		m := u.Methodology
		m.Domain = r.Domain.Schema
		if issues := m.Validate(); len(issues) > 0 {
			return DomainRecord{}, fmt.Errorf("%w: methodology %s@%s would break: %v", ErrInvalid, u.Methodology.Name, u.Methodology.Version, issues)
		}
	}
	if err := ds.SetDomainStatus(ctx, name, version, StatusPublished, s.clock()); err != nil {
		return DomainRecord{}, err
	}
	r, err = ds.GetDomain(ctx, name, version)
	if err == nil {
		s.publishDomainEvent(ctx, "published", r)
	}
	return r, err
}

// CreateDomainVersion copies a version into a new draft.
func (s *Service) CreateDomainVersion(ctx context.Context, name, from, to string) (DomainRecord, error) {
	ds, err := s.domains()
	if err != nil {
		return DomainRecord{}, err
	}
	src, err := ds.GetDomain(ctx, name, from)
	if err != nil {
		return DomainRecord{}, err
	}
	if to == "" || to == src.Domain.Version {
		return DomainRecord{}, fmt.Errorf("a new version number is required: %w", ErrInvalid)
	}
	if _, err := ds.GetDomain(ctx, name, to); err == nil {
		return DomainRecord{}, fmt.Errorf("domain %s@%s already exists: %w", name, to, ErrImmutable)
	}
	d := src.Domain
	d.Version = to
	r, _, err := s.SaveDomain(ctx, d)
	return r, err
}

// DeleteDomain removes a draft or archives a published version that no
// methodology is pinned to.
func (s *Service) DeleteDomain(ctx context.Context, name, version string) error {
	ds, err := s.domains()
	if err != nil {
		return err
	}
	r, err := ds.GetDomain(ctx, name, version)
	if err != nil {
		return err
	}
	if err := s.authorizeDomain(ctx, "delete", &r.Domain); err != nil {
		return err
	}
	switch r.Status {
	case StatusDraft, StatusPublished:
		// pinned methodologies block deletion; so do unpinned ones when this
		// is the version they resolve to (the latest published)
		latest, _ := ds.GetDomain(ctx, name, "")
		users, err := s.methodologiesUsing(ctx, name, version, r.Status == StatusPublished && latest.Domain.Version == version)
		if err != nil {
			return err
		}
		if len(users) > 0 {
			return fmt.Errorf("%w: domain %s@%s is used by %s@%s", ErrInvalid, name, version, users[0].Methodology.Name, users[0].Methodology.Version)
		}
	default:
		return nil
	}
	if r.Status == StatusDraft {
		err = ds.DeleteDomain(ctx, name, version)
	} else {
		err = ds.SetDomainStatus(ctx, name, version, StatusArchived, s.clock())
	}
	if err == nil {
		s.publishDomainEvent(ctx, "deleted", r)
	}
	return err
}

// ImportDomain stores a YAML definition as a draft, and publishes it on request.
func (s *Service) ImportDomain(ctx context.Context, yamlSrc []byte, publish bool) (DomainRecord, methodology.Issues, error) {
	d, err := methodology.ParseDomain(yamlSrc)
	if err != nil {
		return DomainRecord{}, nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	r, issues, err := s.SaveDomain(ctx, *d)
	if err != nil || !publish {
		return r, issues, err
	}
	if len(issues) > 0 {
		return r, issues, fmt.Errorf("%w: cannot publish: %v", ErrInvalid, issues)
	}
	r, err = s.PublishDomain(ctx, d.Name, d.Version)
	return r, nil, err
}

// ExportDomain renders a version as YAML.
func (s *Service) ExportDomain(ctx context.Context, name, version string) ([]byte, string, error) {
	r, err := s.GetDomain(ctx, name, version)
	if err != nil {
		return nil, "", err
	}
	out, err := r.Domain.YAML()
	return out, fmt.Sprintf("domain-%s-%s.yaml", r.Domain.Name, r.Domain.Version), err
}

// SeedDomains imports and publishes the YAML files of dir whose domain version
// is not stored yet (dev bootstrap, before the methodologies that reference
// them). It runs with the given principal.
func (s *Service) SeedDomains(ctx context.Context, dir string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.y*ml"))
	if err != nil {
		return nil, err
	}
	ds, err := s.domains()
	if err != nil {
		return nil, err
	}
	var loaded []string
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			return loaded, err
		}
		d, err := methodology.ParseDomain(src)
		if err != nil {
			return loaded, fmt.Errorf("%s: %w", f, err)
		}
		if _, err := ds.GetDomain(ctx, d.Name, d.Version); err == nil {
			continue
		}
		if _, _, err := s.ImportDomain(ctx, src, true); err != nil {
			return loaded, fmt.Errorf("%s: %w", f, err)
		}
		loaded = append(loaded, d.Name+"@"+d.Version)
	}
	return loaded, nil
}
