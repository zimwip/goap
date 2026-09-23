package registrysvc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/methodology"
)

// Service holds the registry rules: drafts are editable, published versions
// are immutable and must be valid, writes are authorized by ABAC rules on
// the "methodology" resource (actions write, publish, delete).
type Service struct {
	Store  Store
	Authz  authz.Authorizer
	Events engine.Publisher
	now    func() time.Time
}

// ErrInvalid wraps validation issues.
var ErrInvalid = errors.New("invalid methodology")

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now().UTC()
}

func (s *Service) authorize(ctx context.Context, action string, m *methodology.Methodology) error {
	return authz.Check(ctx, s.Authz, authz.Request{Subject: authz.From(ctx), Action: action,
		Resource: authz.Resource{Type: "methodology", ID: m.Name + "@" + m.Version, Name: m.Name, Org: authz.From(ctx).Org}})
}

func (s *Service) publish(ctx context.Context, event string, r Record) {
	if s.Events != nil {
		_ = s.Events.Publish(ctx, "goap.registry.methodology."+event, map[string]string{"name": r.Methodology.Name, "version": r.Methodology.Version, "status": string(r.Status)})
	}
}

// Get returns a version (empty version: latest published).
func (s *Service) Get(ctx context.Context, name, version string) (Record, error) {
	return s.Store.Get(ctx, name, version)
}

// Versions returns every version, or the latest version of each methodology
// (latest published if any, latest draft otherwise).
func (s *Service) Versions(ctx context.Context, all bool) ([]Record, error) {
	rs, err := s.Store.List(ctx)
	if err != nil || all {
		return rs, err
	}
	latest := map[string]Record{}
	var order []string
	for _, r := range rs {
		name := r.Methodology.Name
		cur, ok := latest[name]
		if !ok {
			order = append(order, name)
		}
		better := !ok ||
			(r.Status == StatusPublished && (cur.Status != StatusPublished || r.PublishedAt.After(cur.PublishedAt))) ||
			(cur.Status != StatusPublished && r.Status != StatusArchived && r.CreatedAt.After(cur.CreatedAt))
		if better {
			latest[name] = r
		}
	}
	out := make([]Record, 0, len(order))
	for _, n := range order {
		out = append(out, latest[n])
	}
	return out, nil
}

// Save creates or replaces a draft; invalid drafts are stored and their
// issues returned.
func (s *Service) Save(ctx context.Context, m methodology.Methodology) (Record, methodology.Issues, error) {
	if m.Name == "" || m.Version == "" {
		return Record{}, nil, fmt.Errorf("name and version are required: %w", ErrInvalid)
	}
	if err := s.authorize(ctx, "write", &m); err != nil {
		return Record{}, nil, err
	}
	now := s.clock()
	r := Record{Methodology: m, Status: StatusDraft, CreatedAt: now, UpdatedAt: now, UpdatedBy: authz.From(ctx).Subject}
	if err := s.Store.Save(ctx, r); err != nil {
		return Record{}, nil, err
	}
	saved, err := s.Store.Get(ctx, m.Name, m.Version)
	if err != nil {
		return Record{}, nil, err
	}
	s.publish(ctx, "saved", saved)
	return saved, m.Validate(), nil
}

// Publish freezes a valid draft.
func (s *Service) Publish(ctx context.Context, name, version string) (Record, error) {
	r, err := s.Store.Get(ctx, name, version)
	if err != nil {
		return Record{}, err
	}
	if err := s.authorize(ctx, "publish", &r.Methodology); err != nil {
		return Record{}, err
	}
	if r.Status != StatusDraft {
		return Record{}, fmt.Errorf("%s@%s: %w", name, version, ErrImmutable)
	}
	if issues := r.Methodology.Validate(); len(issues) > 0 {
		return Record{}, fmt.Errorf("%w: %v", ErrInvalid, issues)
	}
	if err := s.Store.SetStatus(ctx, name, version, StatusPublished, s.clock()); err != nil {
		return Record{}, err
	}
	r, err = s.Store.Get(ctx, name, version)
	if err == nil {
		s.publish(ctx, "published", r)
	}
	return r, err
}

// CreateVersion copies a version into a new draft.
func (s *Service) CreateVersion(ctx context.Context, name, from, to string) (Record, error) {
	src, err := s.Store.Get(ctx, name, from)
	if err != nil {
		return Record{}, err
	}
	if to == "" || to == src.Methodology.Version {
		return Record{}, fmt.Errorf("a new version number is required: %w", ErrInvalid)
	}
	if _, err := s.Store.Get(ctx, name, to); err == nil {
		return Record{}, fmt.Errorf("%s@%s already exists: %w", name, to, ErrImmutable)
	}
	m := src.Methodology
	m.Version = to
	r, _, err := s.Save(ctx, m)
	return r, err
}

// Delete removes a draft or archives a published version.
func (s *Service) Delete(ctx context.Context, name, version string) error {
	r, err := s.Store.Get(ctx, name, version)
	if err != nil {
		return err
	}
	if err := s.authorize(ctx, "delete", &r.Methodology); err != nil {
		return err
	}
	switch r.Status {
	case StatusDraft:
		err = s.Store.Delete(ctx, name, version)
	case StatusPublished:
		err = s.Store.SetStatus(ctx, name, version, StatusArchived, s.clock())
	default:
		return nil
	}
	if err == nil {
		s.publish(ctx, "deleted", r)
	}
	return err
}

// Import stores a YAML definition as a draft, and publishes it on request.
func (s *Service) Import(ctx context.Context, yamlSrc []byte, publish bool) (Record, methodology.Issues, error) {
	m, err := methodology.Parse(yamlSrc)
	if err != nil {
		return Record{}, nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	r, issues, err := s.Save(ctx, *m)
	if err != nil || !publish {
		return r, issues, err
	}
	if len(issues) > 0 {
		return r, issues, fmt.Errorf("%w: cannot publish: %v", ErrInvalid, issues)
	}
	r, err = s.Publish(ctx, m.Name, m.Version)
	return r, nil, err
}

// Export renders a version as YAML.
func (s *Service) Export(ctx context.Context, name, version string) ([]byte, string, error) {
	r, err := s.Store.Get(ctx, name, version)
	if err != nil {
		return nil, "", err
	}
	out, err := r.Methodology.YAML()
	return out, fmt.Sprintf("%s-%s.yaml", r.Methodology.Name, r.Methodology.Version), err
}

// Seed imports and publishes the YAML files of dir whose version is not
// stored yet (dev bootstrap). It runs with the given principal.
func (s *Service) Seed(ctx context.Context, dir string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.y*ml"))
	if err != nil {
		return nil, err
	}
	var loaded []string
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			return loaded, err
		}
		m, err := methodology.Parse(src)
		if err != nil {
			return loaded, fmt.Errorf("%s: %w", f, err)
		}
		if _, err := s.Store.Get(ctx, m.Name, m.Version); err == nil {
			continue
		}
		if _, _, err := s.Import(ctx, src, true); err != nil {
			return loaded, fmt.Errorf("%s: %w", f, err)
		}
		loaded = append(loaded, m.Name+"@"+m.Version)
	}
	return loaded, nil
}

// List implements engine.MethodologyPort: the latest published version of
// every methodology.
func (s *Service) List(ctx context.Context) ([]*methodology.Compiled, error) {
	rs, err := s.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []*methodology.Compiled
	for _, r := range rs {
		if r.Status != StatusPublished || seen[r.Methodology.Name] {
			continue
		}
		seen[r.Methodology.Name] = true
		c, err := s.Methodology(ctx, r.Methodology.Name)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// Methodology implements engine.MethodologyPort for in-process use.
func (s *Service) Methodology(ctx context.Context, name string) (*methodology.Compiled, error) {
	r, err := s.Store.Get(ctx, name, "")
	if err != nil {
		return nil, engine.ErrUnknownMethodology{Name: name}
	}
	m := r.Methodology
	return m.Compile()
}
