// Package domainsvc is the domain registry (ADR 0125 §3): it stores the domains, the definition of the graph (node
// types, link types, lifecycles, algorithms, one per namespace, ADR 0013), in its own database, and serves them over
// Connect (registry.v1.DomainService). It goes with the graph, which loads its type catalogue from it, and names no
// methodology: publishing a domain emits an event, and the methodology registry checks its methodologies again.
package domainsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/typecat"
)

// Publisher publishes the events of the domains (goap.registry.domain.saved | published | deleted, payload {name,
// version, status}).
type Publisher interface {
	Publish(ctx context.Context, subject string, v any) error
}

// Service holds the rules of the domain registry: drafts are editable, published versions are immutable and must be
// valid, writes are authorized by ABAC rules on the "domain" resource (actions write, publish, delete).
type Service struct {
	Store  Store
	Authz  authz.Authorizer
	Events Publisher
	// Changed is called after a version is published or archived, once stored: the types in force may have changed (the
	// graph in the same process reloads its catalogue). Nil: nothing is called.
	Changed func(ctx context.Context, r def.Record)
	now     func() time.Time
}

// ErrInvalid wraps validation issues.
var ErrInvalid = errors.New("invalid domain")

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now().UTC()
}

// Types is the type catalogue in force (ADR 0012 §2): the latest published version of every domain and the built-in
// domains. over replaces the published version of a domain (to check a candidate version).
func (s *Service) Types(ctx context.Context, over ...*def.Domain) (*typecat.Catalog, error) {
	ds, err := s.Domains(ctx)
	if err != nil {
		return nil, err
	}
	byName := map[string]*def.Domain{}
	var order []string
	for _, d := range append(ds, over...) {
		if _, ok := byName[d.Name]; !ok {
			order = append(order, d.Name)
		}
		byName[d.Name] = d
	}
	list := make([]*def.Domain, 0, len(order))
	for _, n := range order {
		list = append(list, byName[n])
	}
	return typecat.New(list...)
}

func (s *Service) authorizeDomain(ctx context.Context, action string, d *def.Domain) error {
	return authz.Check(ctx, s.Authz, authz.Request{Subject: authz.From(ctx), Action: action,
		Resource: authz.Resource{Type: "domain", ID: d.Name + "@" + d.Version, Name: d.Name, Org: authz.From(ctx).Org}})
}

func (s *Service) publishDomainEvent(ctx context.Context, event string, r def.Record) {
	if s.Events != nil {
		_ = s.Events.Publish(ctx, "goap.registry.domain."+event, map[string]string{"name": r.Domain.Name, "version": r.Domain.Version, "status": string(r.Status)})
	}
	if event != "saved" && s.Changed != nil {
		s.Changed(context.WithoutCancel(ctx), r)
	}
}

// builtinDomains are the records of the domains shipped with the platform: published, frozen, never stored.
func builtinDomains() []def.Record {
	var out []def.Record
	for _, d := range typecat.Builtins() {
		out = append(out, def.Record{Domain: *d, Status: def.StatusPublished, UpdatedBy: "platform", Builtin: true})
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
func (s *Service) GetDomain(ctx context.Context, name, version string) (def.Record, error) {
	for _, r := range builtinDomains() {
		if r.Domain.Name == name && (version == "" || version == r.Domain.Version) {
			return r, nil
		}
	}
	ds := s.Store
	return ds.GetDomain(ctx, name, version)
}

// Domains returns the latest published version of every stored domain (the source of the type catalogue, which
// adds the built-in ones itself).
func (s *Service) Domains(ctx context.Context) ([]*def.Domain, error) {
	rs, err := s.DomainVersions(ctx, false)
	if err != nil {
		return nil, err
	}
	return def.InForce(rs), nil
}

// DomainVersions returns every version, or the latest version of each domain
// (latest published if any, latest draft otherwise); the built-in domains come first.
func (s *Service) DomainVersions(ctx context.Context, all bool) ([]def.Record, error) {
	ds := s.Store
	rs, err := ds.ListDomains(ctx)
	if err != nil {
		return nil, err
	}
	rs = append(builtinDomains(), rs...)
	if all {
		return rs, nil
	}
	return def.Latest(rs), nil
}

// SaveDomain creates or replaces a draft; invalid drafts are stored and
// their issues returned.
func (s *Service) SaveDomain(ctx context.Context, d def.Domain) (def.Record, def.Issues, error) {
	if d.Name == "" || d.Version == "" {
		return def.Record{}, nil, fmt.Errorf("name and version are required: %w", ErrInvalid)
	}
	if err := errBuiltin(d.Name); err != nil {
		return def.Record{}, nil, err
	}
	ds := s.Store
	if err := s.authorizeDomain(ctx, "write", &d); err != nil {
		return def.Record{}, nil, err
	}
	now := s.clock()
	r := def.Record{Domain: d, Status: def.StatusDraft, CreatedAt: now, UpdatedAt: now, UpdatedBy: authz.From(ctx).Subject}
	if err := ds.SaveDomain(ctx, r); err != nil {
		return def.Record{}, nil, err
	}
	saved, err := ds.GetDomain(ctx, d.Name, d.Version)
	if err != nil {
		return def.Record{}, nil, err
	}
	s.publishDomainEvent(ctx, "saved", saved)
	return saved, s.validateDomain(ctx, &d), nil
}

// validateDomain checks a domain and its references to the types of the other domains in force.
func (s *Service) validateDomain(ctx context.Context, d *def.Domain) def.Issues {
	issues := d.Validate()
	if typecat.IsBuiltin(d.Name) {
		return append(issues, def.Issue{Path: "name", Message: d.Name + " is a domain built into the platform: it changes with the platform code"})
	}
	if len(issues) == 0 {
		if _, err := s.Types(ctx, d); err != nil {
			path := "nodeTypes"
			if strings.HasPrefix(err.Error(), "item type") {
				path = "itemTypes"
			}
			issues = append(issues, def.Issue{Path: path, Message: err.Error()})
		}
	}
	return issues
}

// PublishDomain freezes a valid draft; it becomes the version in force. The methodologies it breaks are the methodology
// registry's to suspend, on the event (ADR 0125 §3): a domain is never refused for them.
func (s *Service) PublishDomain(ctx context.Context, name, version string) (def.Record, error) {
	if err := errBuiltin(name); err != nil {
		return def.Record{}, err
	}
	ds := s.Store
	r, err := ds.GetDomain(ctx, name, version)
	if err != nil {
		return def.Record{}, err
	}
	if err := s.authorizeDomain(ctx, "publish", &r.Domain); err != nil {
		return def.Record{}, err
	}
	if r.Status != def.StatusDraft {
		return def.Record{}, fmt.Errorf("domain %s@%s: %w", name, version, ErrImmutable)
	}
	if err := s.checkPublishable(ctx, &r.Domain); err != nil {
		return def.Record{}, err
	}
	if err := ds.SetDomainStatus(ctx, name, version, def.StatusPublished, s.clock()); err != nil {
		return def.Record{}, err
	}
	r, err = ds.GetDomain(ctx, name, version)
	if err == nil {
		s.publishDomainEvent(ctx, "published", r)
	}
	return r, err
}

// checkPublishable refuses a domain with issues, or one whose types do not resolve with the other domains in force.
func (s *Service) checkPublishable(ctx context.Context, d *def.Domain) error {
	if issues := s.validateDomain(ctx, d); len(issues) > 0 {
		return fmt.Errorf("%w: %v", ErrInvalid, issues)
	}
	if _, err := s.Types(ctx, d); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return nil
}

// CreateDomainVersion copies a version into a new draft.
func (s *Service) CreateDomainVersion(ctx context.Context, name, from, to string) (def.Record, error) {
	if err := errBuiltin(name); err != nil {
		return def.Record{}, err
	}
	ds := s.Store
	src, err := s.GetDomain(ctx, name, from)
	if err != nil {
		return def.Record{}, err
	}
	if to == "" || to == src.Domain.Version {
		return def.Record{}, fmt.Errorf("a new version number is required: %w", ErrInvalid)
	}
	if _, err := ds.GetDomain(ctx, name, to); err == nil {
		return def.Record{}, fmt.Errorf("domain %s@%s already exists: %w", name, to, ErrImmutable)
	}
	d := src.Domain
	d.Version = to
	r, _, err := s.SaveDomain(ctx, d)
	return r, err
}

// DeleteDomain removes a draft, or archives a published version (the methodologies the archive breaks are suspended by
// the methodology registry, on the event).
func (s *Service) DeleteDomain(ctx context.Context, name, version string) error {
	if err := errBuiltin(name); err != nil {
		return err
	}
	ds := s.Store
	r, err := ds.GetDomain(ctx, name, version)
	if err != nil {
		return err
	}
	if err := s.authorizeDomain(ctx, "delete", &r.Domain); err != nil {
		return err
	}
	if r.Status == def.StatusArchived {
		return nil
	}
	if r.Status == def.StatusDraft {
		err = ds.DeleteDomain(ctx, name, version)
	} else {
		err = ds.SetDomainStatus(ctx, name, version, def.StatusArchived, s.clock())
	}
	if err == nil {
		s.publishDomainEvent(ctx, "deleted", r)
	}
	return err
}

// ImportDomain stores a YAML definition as a draft, and publishes it on request.
func (s *Service) ImportDomain(ctx context.Context, yamlSrc []byte, publish bool) (def.Record, def.Issues, error) {
	d, err := def.ParseDomain(yamlSrc)
	if err != nil {
		return def.Record{}, nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if !publish {
		return s.SaveDomain(ctx, *d)
	}
	return s.importPublishedDomain(ctx, *d)
}

// importPublishedDomain stores a domain version as published in one write, with one event (SaveDomain then
// PublishDomain would be two). A domain with issues cannot be published: it is kept as a draft, and the issues
// returned with the error.
func (s *Service) importPublishedDomain(ctx context.Context, d def.Domain) (def.Record, def.Issues, error) {
	if d.Name == "" || d.Version == "" {
		return def.Record{}, nil, fmt.Errorf("name and version are required: %w", ErrInvalid)
	}
	if err := errBuiltin(d.Name); err != nil {
		return def.Record{}, nil, err
	}
	ds := s.Store
	if err := s.authorizeDomain(ctx, "write", &d); err != nil {
		return def.Record{}, nil, err
	}
	if err := s.authorizeDomain(ctx, "publish", &d); err != nil {
		return def.Record{}, nil, err
	}
	if issues := s.validateDomain(ctx, &d); len(issues) > 0 {
		r, _, err := s.SaveDomain(ctx, d)
		if err != nil {
			return r, issues, err
		}
		return r, issues, fmt.Errorf("%w: cannot publish: %v", ErrInvalid, issues)
	}
	if err := s.checkPublishable(ctx, &d); err != nil {
		return def.Record{}, nil, err
	}
	now := s.clock()
	r := def.Record{Domain: d, Status: def.StatusPublished, CreatedAt: now, UpdatedAt: now, PublishedAt: now, UpdatedBy: authz.From(ctx).Subject}
	if err := ds.SaveDomain(ctx, r); err != nil {
		return def.Record{}, nil, err
	}
	saved, err := ds.GetDomain(ctx, d.Name, d.Version)
	if err != nil {
		return def.Record{}, nil, err
	}
	s.publishDomainEvent(ctx, "published", saved)
	return saved, nil, nil
}

// ExportDomain renders a version as YAML, or as JSON with format "json" (the same document: ImportDomain reads both).
func (s *Service) ExportDomain(ctx context.Context, name, version, format string) ([]byte, string, error) {
	r, err := s.GetDomain(ctx, name, version)
	if err != nil {
		return nil, "", err
	}
	out, err := r.Domain.YAML()
	if err != nil {
		return nil, "", err
	}
	base := fmt.Sprintf("domain-%s-%s", r.Domain.Name, r.Domain.Version)
	switch format {
	case "", "yaml":
		return out, base + ".yaml", nil
	case "json":
		// through the YAML document, so that the JSON carries the same names and omissions
		var doc any
		if err := yaml.Unmarshal(out, &doc); err != nil {
			return nil, "", err
		}
		js, err := json.MarshalIndent(doc, "", "  ")
		return js, base + ".json", err
	default:
		return nil, "", fmt.Errorf("unknown export format %q (yaml or json): %w", format, ErrInvalid)
	}
}

// SeedDomains imports and publishes the YAML files of dir whose domain version
// is not stored yet (dev bootstrap, before the methodologies that reference
// them). It runs with the given principal.
func (s *Service) SeedDomains(ctx context.Context, dir string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.y*ml"))
	if err != nil {
		return nil, err
	}
	ds := s.Store
	var loaded []string
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			return loaded, err
		}
		d, err := def.ParseDomain(src)
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
