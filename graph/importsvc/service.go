// Package importsvc is the import registry (ADR 0148): the people list the sources of a namespace the domains declare
// (`importers:` of a domain) and run them through it. The registry checks the run (the permission import:run, the
// parameters against the declaration) and creates its job (ADR 0149, kind import.<domain>.<importer>) for the person;
// the importer service implementing it is a worker of the job service, which writes through the graph's change
// operations like any producer. The registry holds no data of its own: an importer is available when a live worker
// claims its kind, and the runs are followed, cancelled and listed on the job service.
package importsvc

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	importerv1 "github.com/zimwip/goap/contracts/gen/goap/importer/v1"
	jobsv1 "github.com/zimwip/goap/contracts/gen/goap/jobs/v1"
	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/importerkit"
	"github.com/zimwip/goap/lib/jobkit"
)

// Errors of the registry.
var (
	ErrInvalid   = errors.New("invalid")
	ErrNotFound  = errors.New("not found")
	ErrForbidden = authz.ErrForbidden
	// ErrUnavailable: the importer is declared, but no live service implements it.
	ErrUnavailable = errors.New("unavailable")
)

// Domains is the domain registry as the import registry reads it: the latest published version of each domain
// (domainsvc.Service).
type Domains interface {
	Domains(ctx context.Context) ([]*def.Domain, error)
}

// Service is the registry.
type Service struct {
	Domains Domains
	// Authz decides import:run (nil: everything is refused, ADR 0138).
	Authz authz.Authorizer
	// Jobs is the job service: the runs are its jobs, and its live workers say which importers are available.
	Jobs jobkit.Queue
}

// declared are the importers the domains in force declare, by domain and name.
func (s *Service) declared(ctx context.Context) (map[[2]string]declaration, []*def.Domain, error) {
	if s.Domains == nil {
		return nil, nil, nil
	}
	ds, err := s.Domains.Domains(ctx)
	if err != nil {
		return nil, nil, err
	}
	out := map[[2]string]declaration{}
	for _, d := range ds {
		for _, im := range d.Importers {
			out[[2]string{d.Name, im.Name}] = declaration{domain: d, importer: im}
		}
	}
	return out, ds, nil
}

type declaration struct {
	domain   *def.Domain
	importer def.Importer
}

// serving are the kinds a live worker claims, with the worker (none without a job service).
func (s *Service) serving(ctx context.Context) (map[string]string, error) {
	if s.Jobs == nil {
		return map[string]string{}, nil
	}
	return jobkit.LiveKinds(ctx, s.Jobs)
}

// Importer is a declared importer as the registry answers it.
type Importer struct {
	Domain        string
	DomainVersion string
	Def           def.Importer
	Service       string
	// Enums are the values of the enums its parameters take, by enum.
	Enums map[string][]def.EnumValue
}

// Importers lists the importers the domains in force declare (of the domain of namespace when set), each with the live
// service implementing it, if any.
func (s *Service) Importers(ctx context.Context, namespace string) ([]Importer, error) {
	_, ds, err := s.declared(ctx)
	if err != nil {
		return nil, err
	}
	live, err := s.serving(ctx)
	if err != nil {
		return nil, err
	}
	var out []Importer
	for _, d := range ds {
		if namespace != "" && d.Name != namespace {
			continue
		}
		for _, im := range d.Importers {
			x := Importer{Domain: d.Name, DomainVersion: d.Version, Def: im, Enums: map[string][]def.EnumValue{}}
			for _, a := range im.Params {
				if e := d.Enum(a.Enum); e != nil {
					x.Enums[a.Enum] = e.Values
				}
			}
			x.Service = live[importerkit.Kind(d.Name, im.Name)]
			out = append(out, x)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Domain != out[j].Domain {
			return out[i].Domain < out[j].Domain
		}
		return out[i].Def.Name < out[j].Def.Name
	})
	return out, nil
}

// StartRequest is a run asked by a person.
type StartRequest struct {
	Domain, Importer string
	Params           map[string]any
	ProjectID        string
	OwnerOrg         string
}

// Start checks and starts a run for the caller of ctx: permission import:run on the namespace and the project, the
// parameters against the declaration (unknown ones refused, types and enums checked, defaults given).
func (s *Service) Start(ctx context.Context, req StartRequest) (*jobsv1.Job, error) {
	p := authz.From(ctx)
	if p.Anonymous() {
		return nil, fmt.Errorf("an import acts for a person: %w", ErrForbidden)
	}
	if req.ProjectID == "" {
		req.ProjectID = p.Project
	}
	if req.ProjectID == "" {
		return nil, fmt.Errorf("project required (the changes of an import act in a project): %w", ErrInvalid)
	}
	decl, _, err := s.declared(ctx)
	if err != nil {
		return nil, err
	}
	d, ok := decl[[2]string{req.Domain, req.Importer}]
	if !ok {
		return nil, fmt.Errorf("importer %s/%s: declared by no domain in force: %w", req.Domain, req.Importer, ErrNotFound)
	}
	if err := authz.Check(ctx, s.Authz, authz.Request{Subject: p, Action: "run",
		Resource: authz.Resource{Type: "import", Namespace: req.Domain, Name: req.Importer, ProjectID: req.ProjectID}}); err != nil {
		return nil, err
	}
	params, err := Params(d.domain, d.importer, req.Params)
	if err != nil {
		return nil, err
	}
	live, err := s.serving(ctx)
	if err != nil {
		return nil, err
	}
	kind := importerkit.Kind(req.Domain, req.Importer)
	if live[kind] == "" {
		return nil, fmt.Errorf("importer %s/%s: no service implements it now: %w", req.Domain, req.Importer, ErrUnavailable)
	}
	label := d.importer.Label
	if label == "" {
		label = d.importer.Name
	}
	return s.Jobs.Create(ctx, jobkit.Spec{Kind: kind, Label: label, Target: req.Domain, Params: params, ProjectID: req.ProjectID,
		OwnerOrg: req.OwnerOrg, By: p.EndUser(), Principal: p})
}

// BrowseRequest asks what a parameter of an importer may name, under path (empty: the roots).
type BrowseRequest struct {
	Domain, Importer, Param, Path string
	ProjectID                     string
}

// browseWait bounds the wait of a listing, browsePoll how often the job is read meanwhile.
const (
	browseWait = 15 * time.Second
	browsePoll = 50 * time.Millisecond
)

// Browse lists what a parameter whose widget is "directory" may name, as the service implementing the importer reads
// it: a short job of kind browse.<domain>.<importer> for the caller (permission import:run, as for a run), awaited.
func (s *Service) Browse(ctx context.Context, req BrowseRequest) (*importerv1.BrowseImportResponse, error) {
	p := authz.From(ctx)
	if p.Anonymous() {
		return nil, fmt.Errorf("a chooser acts for a person: %w", ErrForbidden)
	}
	if req.ProjectID == "" {
		req.ProjectID = p.Project
	}
	decl, _, err := s.declared(ctx)
	if err != nil {
		return nil, err
	}
	d, ok := decl[[2]string{req.Domain, req.Importer}]
	if !ok {
		return nil, fmt.Errorf("importer %s/%s: declared by no domain in force: %w", req.Domain, req.Importer, ErrNotFound)
	}
	i := slices.IndexFunc(d.importer.Params, func(a def.Attribute) bool { return a.Name == req.Param })
	if i < 0 || d.importer.Params[i].Widget != def.WidgetDirectory {
		return nil, fmt.Errorf("%s: not a directory parameter of %s: %w", req.Param, req.Importer, ErrInvalid)
	}
	if err := authz.Check(ctx, s.Authz, authz.Request{Subject: p, Action: "run",
		Resource: authz.Resource{Type: "import", Namespace: req.Domain, Name: req.Importer, ProjectID: req.ProjectID}}); err != nil {
		return nil, err
	}
	live, err := s.serving(ctx)
	if err != nil {
		return nil, err
	}
	kind := importerkit.BrowseKind(req.Domain, req.Importer)
	if live[kind] == "" {
		return nil, fmt.Errorf("importer %s/%s: no service lists its directories now: %w", req.Domain, req.Importer, ErrUnavailable)
	}
	job, err := s.Jobs.Create(ctx, jobkit.Spec{Kind: kind, Label: "Browse", Target: req.Domain,
		Params: map[string]any{"param": req.Param, "path": req.Path}, ProjectID: req.ProjectID, By: p.EndUser(), Principal: p})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, browseWait)
	defer cancel()
	tick := time.NewTicker(browsePoll)
	defer tick.Stop()
	for !jobkit.Final(job.GetStatus()) {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("importer %s/%s: no listing in %s: %w", req.Domain, req.Importer, browseWait, ErrUnavailable)
		case <-tick.C:
		}
		if job, err = s.Jobs.Job(ctx, job.GetId()); err != nil {
			return nil, err
		}
	}
	if job.GetStatus() != jobkit.StatusDone {
		return nil, fmt.Errorf("%s: %w", job.GetError(), ErrInvalid)
	}
	return importerkit.ListingOf(job.GetOutput().AsMap())
}

// Params checks the values of a run against the parameters an importer declares and gives the defaults.
func Params(d *def.Domain, im def.Importer, values map[string]any) (map[string]any, error) {
	out := map[string]any{}
	byName := map[string]def.Attribute{}
	for _, a := range im.Params {
		byName[a.Name] = a
		if a.Default != "" {
			out[a.Name] = a.Default
		}
	}
	var problems []string
	for k, v := range values {
		a, ok := byName[k]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: not a parameter of %s", k, im.Name))
			continue
		}
		if v == nil || v == "" {
			continue
		}
		check := domain.AttributeCheck{Name: a.Name, Type: a.Type, Enum: a.Enum}
		if e := d.Enum(a.Enum); e != nil {
			for _, ev := range e.Values {
				check.Values = append(check.Values, ev.Value)
			}
		}
		if err := check.Check(v); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", k, err))
			continue
		}
		out[k] = v
	}
	if len(problems) > 0 {
		slices.Sort(problems)
		return nil, fmt.Errorf("%s: %w", strings.Join(problems, "; "), ErrInvalid)
	}
	return out, nil
}
