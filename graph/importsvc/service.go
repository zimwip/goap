// Package importsvc is the import registry (ADR 0148): the importer services register with it as implementing the
// importers the domains declare (`importers:` of a domain), and the people list the sources of a namespace and run
// them through it. The registry checks the run (the permission import:run, the parameters against the declaration) and
// asks the service, which writes through the graph's change operations like any producer; it holds no data of its own:
// the registrations are leases kept in memory (a service renews its own), the jobs are the services'.
package importsvc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	importerv1 "github.com/zimwip/goap/contracts/gen/goap/importer/v1"
	"github.com/zimwip/goap/contracts/gen/goap/importer/v1/importerv1connect"
	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/importerkit"
	"github.com/zimwip/goap/lib/pbconv"
)

// DefaultLease is how long a registration holds without being renewed.
const DefaultLease = 30 * time.Second

// LocalScheme is the endpoint scheme of a service running in the registry's process (AddLocal).
const LocalScheme = "inproc://"

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

// Registration is a registered importer service.
type Registration struct {
	Info     *importerv1.ImporterInfo
	Endpoint string
	LastSeen time.Time
	// Local registrations (AddLocal) never expire.
	Local bool
}

// Service is the registry.
type Service struct {
	Domains Domains
	// Authz decides import:run (nil: everything is refused, ADR 0138).
	Authz authz.Authorizer
	// Client reaches the services registered at an http(s) endpoint (default: Connect over Dial's client).
	HTTPClient connect.HTTPClient
	// Token is the shared secret of the importers, sent to the services on every call.
	Token string
	Lease time.Duration

	mu    sync.Mutex
	regs  map[string]*Registration
	local map[string]importerv1connect.ImporterServiceClient
	now   func() time.Time
}

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now().UTC()
}

func (s *Service) lease() time.Duration {
	if s.Lease > 0 {
		return s.Lease
	}
	return DefaultLease
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

// Register records a service and renews its lease; it answers the lease and the importers it implements that no domain
// in force declares (not offered until one does).
func (s *Service) Register(ctx context.Context, info *importerv1.ImporterInfo, endpoint string) (time.Duration, []*importerv1.Implements, error) {
	if info == nil || !def.NameRE.MatchString(info.Id) {
		return 0, nil, fmt.Errorf("importer id %q: lowercase letters, digits, - and _: %w", info.GetId(), ErrInvalid)
	}
	if endpoint == "" {
		return 0, nil, fmt.Errorf("endpoint required: %w", ErrInvalid)
	}
	if len(info.Implements) == 0 {
		return 0, nil, fmt.Errorf("%s implements no importer: %w", info.Id, ErrInvalid)
	}
	decl, _, err := s.declared(ctx)
	if err != nil {
		return 0, nil, err
	}
	var undeclared []*importerv1.Implements
	for _, im := range info.Implements {
		if _, ok := decl[[2]string{im.Domain, im.Importer}]; !ok {
			undeclared = append(undeclared, im)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.regs == nil {
		s.regs = map[string]*Registration{}
	}
	if r := s.regs[info.Id]; r != nil && r.Local {
		return 0, nil, fmt.Errorf("%s runs in the registry's process: %w", info.Id, ErrInvalid)
	}
	s.regs[info.Id] = &Registration{Info: info, Endpoint: endpoint, LastSeen: s.clock()}
	return s.lease(), undeclared, nil
}

// AddLocal registers a service running in the registry's process (goap-dev): it is called directly and never expires.
func (s *Service) AddLocal(svc *importerkit.Service) {
	info := svc.Importer.Info()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.regs == nil {
		s.regs = map[string]*Registration{}
		s.local = map[string]importerv1connect.ImporterServiceClient{}
	}
	if s.local == nil {
		s.local = map[string]importerv1connect.ImporterServiceClient{}
	}
	ep := LocalScheme + info.Id
	s.local[ep] = svc
	s.regs[info.Id] = &Registration{Info: info, Endpoint: ep, LastSeen: s.clock(), Local: true}
}

// live are the registrations whose lease holds, by id.
func (s *Service) live() []*Registration {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	var out []*Registration
	for _, r := range s.regs {
		if r.Local || now.Sub(r.LastSeen) <= s.lease() {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Info.Id < out[j].Info.Id })
	return out
}

// serving is the live service implementing an importer (the first by id when several do).
func (s *Service) serving(domainName, importer string) *Registration {
	for _, r := range s.live() {
		for _, im := range r.Info.Implements {
			if im.Domain == domainName && im.Importer == importer {
				return r
			}
		}
	}
	return nil
}

func (s *Service) byID(id string) *Registration {
	for _, r := range s.live() {
		if r.Info.Id == id {
			return r
		}
	}
	return nil
}

func (s *Service) client(r *Registration) importerv1connect.ImporterServiceClient {
	if strings.HasPrefix(r.Endpoint, LocalScheme) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.local[r.Endpoint]
	}
	hc := s.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	return importerv1connect.NewImporterServiceClient(hc, r.Endpoint, connect.WithInterceptors(tokenInterceptor(s.Token)))
}

func tokenInterceptor(token string) connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if token != "" {
				req.Header().Set(importerkit.TokenHeader, token)
			}
			return next(ctx, req)
		}
	})
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
			if r := s.serving(d.Name, im.Name); r != nil {
				x.Service = r.Info.Id
			}
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
func (s *Service) Start(ctx context.Context, req StartRequest) (*importerv1.Job, error) {
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
	r := s.serving(req.Domain, req.Importer)
	if r == nil {
		return nil, fmt.Errorf("importer %s/%s: no service implements it now: %w", req.Domain, req.Importer, ErrUnavailable)
	}
	by := p.EndUser()
	resp, err := s.client(r).Start(ctx, connect.NewRequest(&importerv1.StartRequest{Domain: req.Domain, Importer: req.Importer,
		Params: pbconv.Struct(params), ProjectId: req.ProjectID, OwnerOrg: req.OwnerOrg, By: by}))
	if err != nil {
		return nil, err
	}
	job := resp.Msg.Job
	job.Service = r.Info.Id
	return job, nil
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

// Jobs lists the jobs of the live services (of the domain of namespace when set), newest first: every job for an
// administrator, the caller's own otherwise.
func (s *Service) Jobs(ctx context.Context, namespace string) ([]*importerv1.Job, error) {
	p := authz.From(ctx)
	if p.Anonymous() {
		return nil, fmt.Errorf("anonymous: %w", ErrForbidden)
	}
	admin := p.HasRole("admin")
	var out []*importerv1.Job
	for _, r := range s.live() {
		resp, err := s.client(r).Jobs(ctx, connect.NewRequest(&importerv1.JobsRequest{}))
		if err != nil {
			continue // a service that does not answer has no job to show
		}
		for _, j := range resp.Msg.Jobs {
			if (namespace != "" && j.Domain != namespace) || (!admin && j.By != p.EndUser()) {
				continue
			}
			j.Service = r.Info.Id
			out = append(out, j)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.AsTime().After(out[j].StartedAt.AsTime()) })
	return out, nil
}

// Cancel stops a running job: who started it, or a holder of import:run on its namespace and project.
func (s *Service) Cancel(ctx context.Context, service, id string) (*importerv1.Job, error) {
	p := authz.From(ctx)
	if p.Anonymous() {
		return nil, fmt.Errorf("anonymous: %w", ErrForbidden)
	}
	r := s.byID(service)
	if r == nil {
		return nil, fmt.Errorf("importer service %s: %w", service, ErrNotFound)
	}
	c := s.client(r)
	resp, err := c.Jobs(ctx, connect.NewRequest(&importerv1.JobsRequest{}))
	if err != nil {
		return nil, err
	}
	var job *importerv1.Job
	for _, j := range resp.Msg.Jobs {
		if j.Id == id {
			job = j
		}
	}
	if job == nil {
		return nil, fmt.Errorf("job %s: %w", id, ErrNotFound)
	}
	if job.By != p.EndUser() {
		if err := authz.Check(ctx, s.Authz, authz.Request{Subject: p, Action: "run",
			Resource: authz.Resource{Type: "import", Namespace: job.Domain, Name: job.Importer, ProjectID: job.ProjectId}}); err != nil {
			return nil, err
		}
	}
	cr, err := c.Cancel(ctx, connect.NewRequest(&importerv1.CancelRequest{Id: id}))
	if err != nil {
		return nil, err
	}
	cr.Msg.Job.Service = r.Info.Id
	return cr.Msg.Job, nil
}
