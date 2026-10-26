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
	"github.com/zimwip/goap/lib/outbox"
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
	// relay delivers the events of the outbox (Emit, ADR 0136).
	relay *outbox.Relay
}

// OutboxOrigin names the domain registry in the ids of its events (ADR 0136).
const OutboxOrigin = "domains"

// Emit makes the registry write its events in the outbox of its store, in the transaction of the write they are about
// (ADR 0136), and returns the relay delivering them to sink after each write (synchronously: the writes are rare); the
// caller runs it (Relay.Run) for what a delivery missed. The store must keep an outbox (it panics otherwise); a nil sink
// keeps Events. Changed is still called in process once a version is published or archived.
func (s *Service) Emit(sink outbox.Sink) *outbox.Relay {
	if sink == nil {
		return nil
	}
	st, ok := s.Store.(outbox.Store)
	if !ok {
		panic(fmt.Sprintf("domainsvc: the store %T keeps no outbox", s.Store))
	}
	s.relay = &outbox.Relay{Origin: OutboxOrigin, Store: st, Sink: sink}
	return s.relay
}

// DomainEvent is the payload of goap.registry.domain.<event>.
type DomainEvent struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Status  string `json:"status"`
}

// events are the outbox messages of a write (none without Emit).
func (s *Service) events(ctx context.Context, event, name, version string, st def.Status) []outbox.Message {
	if s.relay == nil {
		return nil
	}
	m, err := outbox.New(ctx, "goap.registry.domain."+event, DomainEvent{Name: name, Version: version, Status: string(st)}, s.clock())
	if err != nil {
		return nil
	}
	return []outbox.Message{m}
}

// ErrInvalid wraps validation issues.
var ErrInvalid = errors.New("invalid domain")

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now().UTC()
}

// Types is the type catalogue in force (ADR 0012 §2): the latest published version of every stored domain, the
// built-in ones included (ADR 0167). over replaces the published version of a domain (to check a candidate version).
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
	return typecat.Of(list...)
}

func (s *Service) authorizeDomain(ctx context.Context, action string, d *def.Domain) error {
	if authz.From(ctx).System() {
		return nil // the platform acting by itself (the seeds) is not asked, an authorizer or not (ADR 0138)
	}
	return authz.Check(ctx, s.Authz, authz.Request{Subject: authz.From(ctx), Action: action,
		Resource: authz.Resource{Type: "domain", ID: d.Name + "@" + d.Version, Name: d.Name, Org: authz.From(ctx).Org}})
}

// publishDomainEvent delivers the event of a write that stored it (Emit), else publishes it, then tells Changed.
func (s *Service) publishDomainEvent(ctx context.Context, event string, r def.Record) {
	if s.relay != nil {
		_ = s.relay.Drain(ctx) // what it cannot deliver now, Relay.Run delivers later
	} else if s.Events != nil {
		_ = s.Events.Publish(ctx, "goap.registry.domain."+event, map[string]string{"name": r.Domain.Name, "version": r.Domain.Version, "status": string(r.Status)})
	}
	if event != "saved" && s.Changed != nil {
		s.Changed(context.WithoutCancel(ctx), r)
	}
}

// protectedError refuses a write over RPC to a protected domain (ADR 0167); it is an ErrImmutable.
type protectedError string

func (e protectedError) Error() string {
	return "domain " + string(e) + " is protected: it is written by the local command line only"
}
func (protectedError) Is(target error) bool { return target == ErrImmutable }

type localKey struct{}

// Local marks a context as the local command line of the process owning the registry's database (ADR 0167): its
// writes reach the protected domains. The RPC handler never sets it; the seeds of the shipped domains do.
func Local(ctx context.Context) context.Context { return context.WithValue(ctx, localKey{}, true) }

// IsLocal reports a context marked by Local.
func IsLocal(ctx context.Context) bool { v, _ := ctx.Value(localKey{}).(bool); return v }

// writable refuses a write to a protected domain unless the context is local (ADR 0167).
func (s *Service) writable(ctx context.Context, name string) error {
	if IsLocal(ctx) {
		return nil
	}
	ps, err := s.Store.Protected(ctx)
	if err != nil {
		return err
	}
	if ps[name] {
		return protectedError(name)
	}
	return nil
}

// Protect protects a domain (on) or lifts its protection (ADR 0167): from the local command line only (a context not
// marked Local is refused). A name with no version stored yet may be protected before its first version.
func (s *Service) Protect(ctx context.Context, name string, on bool) error {
	if !IsLocal(ctx) {
		return fmt.Errorf("domain %s: the protection is set by the local command line only: %w", name, ErrImmutable)
	}
	if name == "" {
		return fmt.Errorf("a domain name is required: %w", ErrInvalid)
	}
	return s.Store.SetProtected(ctx, name, on, authz.From(ctx).Subject, s.clock())
}

// stamp sets Protected on the stored records of rs.
func (s *Service) stamp(ctx context.Context, rs []def.Record) ([]def.Record, error) {
	ps, err := s.Store.Protected(ctx)
	if err != nil {
		return nil, err
	}
	for i := range rs {
		if ps[rs[i].Domain.Name] {
			rs[i].Protected = true
		}
	}
	return rs, nil
}

// GetDomain returns a version (empty version: latest published).
func (s *Service) GetDomain(ctx context.Context, name, version string) (def.Record, error) {
	r, err := s.Store.GetDomain(ctx, name, version)
	if err != nil {
		return r, err
	}
	rs, err := s.stamp(ctx, []def.Record{r})
	if err != nil {
		return def.Record{}, err
	}
	return rs[0], nil
}

// Domains returns the latest published version of every stored domain, the built-in ones included: the source of the
// type catalogue of the graph (ADR 0167).
func (s *Service) Domains(ctx context.Context) ([]*def.Domain, error) {
	rs, err := s.DomainVersions(ctx, false)
	if err != nil {
		return nil, err
	}
	return def.InForce(rs), nil
}

// DomainVersions returns every version, or the latest version of each domain
// (latest published if any, latest draft otherwise).
func (s *Service) DomainVersions(ctx context.Context, all bool) ([]def.Record, error) {
	ds := s.Store
	rs, err := ds.ListDomains(ctx)
	if err != nil {
		return nil, err
	}
	if rs, err = s.stamp(ctx, rs); err != nil {
		return nil, err
	}
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
	if err := s.writable(ctx, d.Name); err != nil {
		return def.Record{}, nil, err
	}
	ds := s.Store
	if err := s.authorizeDomain(ctx, "write", &d); err != nil {
		return def.Record{}, nil, err
	}
	now := s.clock()
	r := def.Record{Domain: d, Status: def.StatusDraft, CreatedAt: now, UpdatedAt: now, UpdatedBy: authz.From(ctx).Subject}
	if err := ds.SaveDomain(ctx, r, s.events(ctx, "saved", d.Name, d.Version, def.StatusDraft)...); err != nil {
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
	if err := s.writable(ctx, name); err != nil {
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
	if err := ds.SetDomainStatus(ctx, name, version, def.StatusPublished, s.clock(), s.events(ctx, "published", name, version, def.StatusPublished)...); err != nil {
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
	if err := s.writable(ctx, name); err != nil {
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
	if err := s.writable(ctx, name); err != nil {
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
	// the event names the status the version had (as it always did)
	ev := s.events(ctx, "deleted", name, version, r.Status)
	if r.Status == def.StatusDraft {
		err = ds.DeleteDomain(ctx, name, version, ev...)
	} else {
		err = ds.SetDomainStatus(ctx, name, version, def.StatusArchived, s.clock(), ev...)
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
	if err := s.writable(ctx, d.Name); err != nil {
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
	if err := ds.SaveDomain(ctx, r, s.events(ctx, "published", d.Name, d.Version, def.StatusPublished)...); err != nil {
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

// BuiltinDir is the directory of the built-in domains a registry seeds (ADR 0167): GOAP_BUILTIN_DOMAINS_DIR when set,
// else the builtin directory of the shipped domains (the images copy them there), else the library's sources of a
// checkout (lib/domains/builtin, from its root).
func BuiltinDir(domainsDir string) string {
	if d := os.Getenv("GOAP_BUILTIN_DOMAINS_DIR"); d != "" {
		return d
	}
	if d := filepath.Join(domainsDir, "builtin"); isDir(d) {
		return d
	}
	return filepath.Join("lib", "domains", "builtin")
}

func isDir(p string) bool { fi, err := os.Stat(p); return err == nil && fi.IsDir() }

// SeedBuiltinDomains publishes the built-in domains of dir (methodology, organisation, platform: the domains of the
// platform itself) whose version is not stored yet, and protects them (ADR 0167): stored domains like any other, the
// RPCs never write them. They reference each other, so a file is tried again once another one landed. With no file
// found it fails: a registry without them cannot type a graph.
func (s *Service) SeedBuiltinDomains(ctx context.Context, dir string) ([]string, error) {
	ctx = Local(ctx)
	files, err := filepath.Glob(filepath.Join(dir, "*.y*ml"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no built-in domain in %s (GOAP_BUILTIN_DOMAINS_DIR): %w", dir, ErrNotFound)
	}
	type pending struct {
		file string
		src  []byte
		d    *def.Domain
	}
	var todo []pending
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
		if err := s.Store.SetProtected(ctx, d.Name, true, authz.From(ctx).Subject, s.clock()); err != nil {
			return loaded, err
		}
		if _, err := s.Store.GetDomain(ctx, d.Name, d.Version); err == nil {
			continue
		}
		todo = append(todo, pending{f, src, d})
	}
	for len(todo) > 0 {
		var left []pending
		var last error
		for _, p := range todo {
			if _, _, err := s.importPublishedDomain(ctx, *p.d); err != nil {
				last = fmt.Errorf("%s: %w", p.file, err)
				if _, gerr := s.Store.GetDomain(ctx, p.d.Name, p.d.Version); gerr == nil {
					// kept as a draft: removed, to be tried again whole
					_ = s.Store.DeleteDomain(ctx, p.d.Name, p.d.Version)
				}
				left = append(left, p)
				continue
			}
			loaded = append(loaded, p.d.Name+"@"+p.d.Version)
		}
		if len(left) == len(todo) {
			return loaded, last
		}
		todo = left
	}
	return loaded, nil
}

// SeedDomains imports and publishes the YAML files of dir whose domain version
// is not stored yet (dev bootstrap, before the methodologies that reference
// them). It runs with the given principal, as the local command line: the shipped files are the process's own, a
// protected domain included (ADR 0167).
func (s *Service) SeedDomains(ctx context.Context, dir string) ([]string, error) {
	ctx = Local(ctx)
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
