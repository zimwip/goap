package registrysvc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/methodology"
	"github.com/zimwip/goap/pkg/typecat"
)

// ErrNoDomainStore is returned when the registry has no store for the domains.
var ErrNoDomainStore = errors.New("the registry has no domain store")

// domains is the store of the domain versions: DomainStore, else the Store when it holds domains too (MemoryStore).
func (s *Service) domains() (DomainStore, error) {
	if s.DomainStore != nil {
		return s.DomainStore, nil
	}
	if ds, ok := s.Store.(DomainStore); ok {
		return ds, nil
	}
	return nil, ErrNoDomainStore
}

// Types is the type catalogue in force (ADR 0012 §2): the latest published version of every domain and the built-in
// domains. over replaces the published version of a domain (to check a candidate version).
func (s *Service) Types(ctx context.Context, over ...*methodology.Domain) (*typecat.Catalog, error) {
	ds, err := s.Domains(ctx)
	if err != nil {
		return nil, err
	}
	byName := map[string]*methodology.Domain{}
	var order []string
	for _, d := range append(ds, over...) {
		if _, ok := byName[d.Name]; !ok {
			order = append(order, d.Name)
		}
		byName[d.Name] = d
	}
	list := make([]*methodology.Domain, 0, len(order))
	for _, n := range order {
		list = append(list, byName[n])
	}
	return typecat.New(list...)
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

// builtinDomains are the records of the domains shipped with the platform: published, frozen, never stored.
func builtinDomains() []DomainRecord {
	var out []DomainRecord
	for _, d := range typecat.Builtins() {
		out = append(out, DomainRecord{Domain: *d, Status: StatusPublished, UpdatedBy: "platform", Builtin: true})
	}
	return out
}

// builtinError refuses a write to a built-in domain; it is an ErrImmutable.
type builtinError string

func (e builtinError) Error() string {
	return "domain " + string(e) + " is built into the platform: it changes with the platform code"
}
func (builtinError) Is(target error) bool { return target == ErrImmutable }

// errBuiltin refuses a write to a built-in domain (nil for another name).
func errBuiltin(name string) error {
	if typecat.IsBuiltin(name) {
		return builtinError(name)
	}
	return nil
}

// GetDomain returns a version (empty version: latest published), the built-in domains included.
func (s *Service) GetDomain(ctx context.Context, name, version string) (DomainRecord, error) {
	for _, r := range builtinDomains() {
		if r.Domain.Name == name && (version == "" || version == r.Domain.Version) {
			return r, nil
		}
	}
	ds, err := s.domains()
	if err != nil {
		return DomainRecord{}, err
	}
	return ds.GetDomain(ctx, name, version)
}

// Domains returns the latest published version of every stored domain (the source of the type catalogue, which
// adds the built-in ones itself).
func (s *Service) Domains(ctx context.Context) ([]*methodology.Domain, error) {
	rs, err := s.DomainVersions(ctx, false)
	if err != nil {
		return nil, err
	}
	var out []*methodology.Domain
	for _, r := range rs {
		if r.Status == StatusPublished && !r.Builtin {
			out = append(out, &r.Domain)
		}
	}
	return out, nil
}

// DomainVersions returns every version, or the latest version of each domain
// (latest published if any, latest draft otherwise); the built-in domains come first.
func (s *Service) DomainVersions(ctx context.Context, all bool) ([]DomainRecord, error) {
	ds, err := s.domains()
	if err != nil {
		return nil, err
	}
	rs, err := ds.ListDomains(ctx)
	if err != nil {
		return nil, err
	}
	rs = append(builtinDomains(), rs...)
	if all {
		return rs, nil
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
	if err := errBuiltin(d.Name); err != nil {
		return DomainRecord{}, nil, err
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
	return saved, s.validateDomain(ctx, &d), nil
}

// validateDomain checks a domain and its references to the types of the other domains in force.
func (s *Service) validateDomain(ctx context.Context, d *methodology.Domain) methodology.Issues {
	issues := d.Validate()
	if typecat.IsBuiltin(d.Name) {
		return append(issues, methodology.Issue{Path: "name", Message: d.Name + " is a domain built into the platform: it changes with the platform code"})
	}
	if len(issues) == 0 {
		if _, err := s.Types(ctx, d); err != nil {
			issues = append(issues, methodology.Issue{Path: "nodeTypes", Message: err.Error()})
		}
	}
	return issues
}

// methodologiesUsing returns the methodology versions (not archived) that act on the namespace of a domain or
// reference its types.
func (s *Service) methodologiesUsing(ctx context.Context, name string) ([]Record, error) {
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

// DomainUsage lists the methodology versions using a domain (its namespace or its types); they follow the version
// in force.
func (s *Service) DomainUsage(ctx context.Context, name, version string) ([]Record, error) {
	return s.methodologiesUsing(ctx, name)
}

// PublishDomain freezes a valid draft; it becomes the version in force. The published methodologies must stay
// valid against it.
func (s *Service) PublishDomain(ctx context.Context, name, version string) (DomainRecord, error) {
	if err := errBuiltin(name); err != nil {
		return DomainRecord{}, err
	}
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
	if err := s.checkPublishable(ctx, &r.Domain); err != nil {
		return DomainRecord{}, err
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

// checkPublishable refuses a domain with issues, or one that would break a published methodology using it.
func (s *Service) checkPublishable(ctx context.Context, d *methodology.Domain) error {
	if issues := s.validateDomain(ctx, d); len(issues) > 0 {
		return fmt.Errorf("%w: %v", ErrInvalid, issues)
	}
	cat, err := s.Types(ctx, d)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	users, err := s.methodologiesUsing(ctx, d.Name)
	if err != nil {
		return err
	}
	for _, u := range users {
		if u.Status != StatusPublished {
			continue
		}
		if issues := u.Methodology.Resolve(cat).Validate(); len(issues) > 0 {
			return fmt.Errorf("%w: methodology %s@%s would break: %v", ErrInvalid, u.Methodology.Name, u.Methodology.Version, issues)
		}
	}
	return nil
}

// CreateDomainVersion copies a version into a new draft.
func (s *Service) CreateDomainVersion(ctx context.Context, name, from, to string) (DomainRecord, error) {
	if err := errBuiltin(name); err != nil {
		return DomainRecord{}, err
	}
	ds, err := s.domains()
	if err != nil {
		return DomainRecord{}, err
	}
	src, err := s.GetDomain(ctx, name, from)
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

// DeleteDomain removes a draft, or archives a published version; the version in force cannot be archived while
// methodologies use it.
func (s *Service) DeleteDomain(ctx context.Context, name, version string) error {
	if err := errBuiltin(name); err != nil {
		return err
	}
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
	case StatusDraft:
	case StatusPublished:
		if latest, _ := ds.GetDomain(ctx, name, ""); latest.Domain.Version == version {
			users, err := s.methodologiesUsing(ctx, name)
			if err != nil {
				return err
			}
			if len(users) > 0 {
				return fmt.Errorf("%w: domain %s@%s is used by %s@%s", ErrInvalid, name, version, users[0].Methodology.Name, users[0].Methodology.Version)
			}
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
	if !publish {
		return s.SaveDomain(ctx, *d)
	}
	return s.importPublishedDomain(ctx, *d)
}

// importPublishedDomain stores a domain version as published in one write, with one event (SaveDomain then
// PublishDomain would be two). A domain with issues cannot be published: it is kept as a draft, and the issues
// returned with the error.
func (s *Service) importPublishedDomain(ctx context.Context, d methodology.Domain) (DomainRecord, methodology.Issues, error) {
	if d.Name == "" || d.Version == "" {
		return DomainRecord{}, nil, fmt.Errorf("name and version are required: %w", ErrInvalid)
	}
	if err := errBuiltin(d.Name); err != nil {
		return DomainRecord{}, nil, err
	}
	ds, err := s.domains()
	if err != nil {
		return DomainRecord{}, nil, err
	}
	if err := s.authorizeDomain(ctx, "write", &d); err != nil {
		return DomainRecord{}, nil, err
	}
	if err := s.authorizeDomain(ctx, "publish", &d); err != nil {
		return DomainRecord{}, nil, err
	}
	if issues := s.validateDomain(ctx, &d); len(issues) > 0 {
		r, _, err := s.SaveDomain(ctx, d)
		if err != nil {
			return r, issues, err
		}
		return r, issues, fmt.Errorf("%w: cannot publish: %v", ErrInvalid, issues)
	}
	if err := s.checkPublishable(ctx, &d); err != nil {
		return DomainRecord{}, nil, err
	}
	now := s.clock()
	r := DomainRecord{Domain: d, Status: StatusPublished, CreatedAt: now, UpdatedAt: now, PublishedAt: now, UpdatedBy: authz.From(ctx).Subject}
	if err := ds.SaveDomain(ctx, r); err != nil {
		return DomainRecord{}, nil, err
	}
	saved, err := ds.GetDomain(ctx, d.Name, d.Version)
	if err != nil {
		return DomainRecord{}, nil, err
	}
	s.publishDomainEvent(ctx, "published", saved)
	return saved, nil, nil
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
