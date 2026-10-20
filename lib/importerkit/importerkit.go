// Package importerkit is the toolkit to write an importer service (ADR 0148): a separately deployed service that loads
// a source into the graph (a git repository, a spreadsheet, another tool) for an importer a domain declares.
//
// An importer implements Importer and calls Run from its main:
//
//	func main() { importerkit.Run("importer-git", gitimport.Plugin{...}) }
//
// Run serves the goap.importer.v1.ImporterService protocol and registers the service with the import registry (served
// by the graph service), renewing the registration as a heartbeat; the registry needs no configuration to learn about a
// new importer. The kit holds the jobs: Start runs Importer.Run in the background for the person who asked, Jobs lists
// them, Cancel cancels the context of a run.
//
// Environment: GOAP_HTTP_ADDR (listen address), GOAP_IMPORT_URL (the import registry; default GOAP_GRAPH_URL, else
// http://localhost:8081), GOAP_IMPORTER_URL (URL of this service as the registry reaches it; default
// http://<hostname><listen address>), GOAP_IMPORTER_TOKEN (shared secret between the registry and the importers; the
// registry registers no importer without it, and the service refuses calls without it when it is set).
package importerkit

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/contracts/authz"
	importerv1 "github.com/zimwip/goap/contracts/gen/goap/importer/v1"
	"github.com/zimwip/goap/contracts/gen/goap/importer/v1/importerv1connect"
	"github.com/zimwip/goap/lib/pbconv"
	"github.com/zimwip/goap/lib/platform"
	"github.com/zimwip/goap/lib/telemetry"
)

// TokenHeader carries the shared token between the import registry and the importers.
const TokenHeader = "X-Goap-Importer-Token"

// Job statuses.
const (
	StatusRunning   = "running"
	StatusDone      = "done"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

// KeepFinished is the number of finished jobs a service remembers.
const KeepFinished = 50

// Importer loads a source into the graph.
type Importer interface {
	// Info describes the service and the importers it implements (domain and importer names, as the domains declare
	// them).
	Info() *importerv1.ImporterInfo
	// Run runs one import and returns when it is over. ctx carries the principal the run acts as (a system principal of
	// the service holding the admin role, on behalf of the person who started it: the registry authorized the run) and
	// is cancelled by Cancel; job carries the parameters and takes the progress.
	Run(ctx context.Context, job *Job) error
}

// Job is a run of an importer, as the importer sees it.
type Job struct {
	ID        string
	Domain    string
	Importer  string
	Params    map[string]any
	ProjectID string
	OwnerOrg  string
	By        string

	mu       sync.Mutex
	status   string
	progress string
	stats    map[string]int64
	err      string
	started  time.Time
	ended    time.Time
	cancel   context.CancelFunc
}

// String is a parameter as a string ("" when unset).
func (j *Job) String(name string) string {
	switch v := j.Params[name].(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

// Int is a numeric parameter (0 when unset).
func (j *Job) Int(name string) int {
	switch v := j.Params[name].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case string:
		var n int
		_, _ = fmt.Sscan(v, &n)
		return n
	}
	return 0
}

// Bool is a boolean parameter (false when unset).
func (j *Job) Bool(name string) bool {
	v, _ := j.Params[name].(bool)
	return v
}

// Progress records the last progress line of the run.
func (j *Job) Progress(line string) {
	j.mu.Lock()
	j.progress = line
	j.mu.Unlock()
}

// Set records a counter of the run.
func (j *Job) Set(name string, n int64) {
	j.mu.Lock()
	if j.stats == nil {
		j.stats = map[string]int64{}
	}
	j.stats[name] = n
	j.mu.Unlock()
}

// Write takes the progress lines an importer writes (a Job is an io.Writer): the last non empty line is kept.
func (j *Job) Write(b []byte) (int, error) {
	s := string(b)
	s = strings.TrimRight(s, "\r\n")
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if s != "" {
		j.Progress(s)
	}
	return len(b), nil
}

func (j *Job) toPB() *importerv1.Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	return &importerv1.Job{Id: j.ID, Domain: j.Domain, Importer: j.Importer, Status: j.status, Params: pbconv.Struct(j.Params),
		ProjectId: j.ProjectID, By: j.By, StartedAt: pbconv.Time(j.started), EndedAt: pbconv.Time(j.ended),
		Progress: j.progress, Stats: maps.Clone(j.stats), Error: j.err}
}

// Service serves an importer: it implements importerv1connect.ImporterServiceHandler, and the same methods make it an
// importerv1connect.ImporterServiceClient, so a composition running it in process hands it to the registry as is.
type Service struct {
	Importer Importer
	// Token, when set, is required on every call but Describe.
	Token string
	// Log reports the runs (default: slog.Default()).
	Log *slog.Logger

	mu   sync.Mutex
	jobs []*Job
	now  func() time.Time
}

var (
	_ importerv1connect.ImporterServiceHandler = (*Service)(nil)
	_ importerv1connect.ImporterServiceClient  = (*Service)(nil)
)

// New returns the service of an importer.
func New(im Importer, token string) *Service { return &Service{Importer: im, Token: token} }

// Handler returns the Connect handler of the service.
func (s *Service) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return importerv1connect.NewImporterServiceHandler(s, opts...)
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now().UTC()
}

func (s *Service) check(h http.Header) error {
	if s.Token != "" && subtle.ConstantTimeCompare([]byte(h.Get(TokenHeader)), []byte(s.Token)) != 1 {
		return connect.NewError(connect.CodePermissionDenied, errors.New("invalid importer token"))
	}
	return nil
}

// Describe implements the protocol.
func (s *Service) Describe(context.Context, *connect.Request[importerv1.DescribeRequest]) (*connect.Response[importerv1.DescribeResponse], error) {
	return connect.NewResponse(&importerv1.DescribeResponse{Info: s.Importer.Info()}), nil
}

func (s *Service) implements(domain, importer string) bool {
	for _, im := range s.Importer.Info().GetImplements() {
		if im.Domain == domain && im.Importer == importer {
			return true
		}
	}
	return false
}

// Start implements the protocol: the run goes on in the background, on a context of its own.
func (s *Service) Start(ctx context.Context, r *connect.Request[importerv1.StartRequest]) (*connect.Response[importerv1.StartResponse], error) {
	if err := s.check(r.Header()); err != nil {
		return nil, err
	}
	m := r.Msg
	if !s.implements(m.Domain, m.Importer) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("%s/%s: not implemented by %s", m.Domain, m.Importer, s.Importer.Info().GetId()))
	}
	if m.By == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("by: the person the import acts for is required"))
	}
	id := make([]byte, 6)
	_, _ = rand.Read(id)
	run, cancel := context.WithCancel(context.WithoutCancel(ctx))
	j := &Job{ID: hex.EncodeToString(id), Domain: m.Domain, Importer: m.Importer, Params: pbconv.Map(m.Params), ProjectID: m.ProjectId,
		OwnerOrg: m.OwnerOrg, By: m.By, status: StatusRunning, started: s.clock(), cancel: cancel}
	if j.Params == nil {
		j.Params = map[string]any{}
	}
	s.mu.Lock()
	s.jobs = append(s.jobs, j)
	s.mu.Unlock()
	who := authz.System(s.Importer.Info().GetId(), "admin").For(m.By)
	run = authz.With(run, who)
	go s.run(run, j)
	return connect.NewResponse(&importerv1.StartResponse{Job: j.toPB()}), nil
}

func (s *Service) run(ctx context.Context, j *Job) {
	log := s.log().With("job", j.ID, "importer", j.Domain+"/"+j.Importer, "by", j.By)
	log.Info("import started")
	err := s.safeRun(ctx, j)
	j.mu.Lock()
	j.ended = s.clock()
	switch {
	case err == nil:
		j.status = StatusDone
	case ctx.Err() != nil:
		j.status, j.err = StatusCancelled, err.Error()
	default:
		j.status, j.err = StatusFailed, err.Error()
	}
	status := j.status
	j.mu.Unlock()
	j.cancel()
	if err != nil {
		log.Warn("import ended", "status", status, "err", err)
	} else {
		log.Info("import done")
	}
	s.prune()
}

func (s *Service) safeRun(ctx context.Context, j *Job) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("importer panicked: %v", r)
		}
	}()
	return s.Importer.Run(ctx, j)
}

// prune forgets the oldest finished jobs past KeepFinished.
func (s *Service) prune() {
	s.mu.Lock()
	defer s.mu.Unlock()
	finished := 0
	for _, j := range s.jobs {
		if j.toPB().Status != StatusRunning {
			finished++
		}
	}
	for i := 0; finished > KeepFinished && i < len(s.jobs); {
		if s.jobs[i].toPB().Status != StatusRunning {
			s.jobs = slices.Delete(s.jobs, i, i+1)
			finished--
			continue
		}
		i++
	}
}

// Jobs implements the protocol, newest first.
func (s *Service) Jobs(_ context.Context, r *connect.Request[importerv1.JobsRequest]) (*connect.Response[importerv1.JobsResponse], error) {
	if err := s.check(r.Header()); err != nil {
		return nil, err
	}
	s.mu.Lock()
	jobs := slices.Clone(s.jobs)
	s.mu.Unlock()
	out := &importerv1.JobsResponse{}
	for i := len(jobs) - 1; i >= 0; i-- {
		out.Jobs = append(out.Jobs, jobs[i].toPB())
	}
	return connect.NewResponse(out), nil
}

// Cancel implements the protocol.
func (s *Service) Cancel(_ context.Context, r *connect.Request[importerv1.CancelRequest]) (*connect.Response[importerv1.CancelResponse], error) {
	if err := s.check(r.Header()); err != nil {
		return nil, err
	}
	s.mu.Lock()
	var j *Job
	for _, x := range s.jobs {
		if x.ID == r.Msg.Id {
			j = x
		}
	}
	s.mu.Unlock()
	if j == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("job %s not found", r.Msg.Id))
	}
	j.cancel()
	return connect.NewResponse(&importerv1.CancelResponse{Job: j.toPB()}), nil
}

// Wait waits for a job to end (tests).
func (s *Service) Wait(ctx context.Context, id string) (*importerv1.Job, error) {
	for {
		s.mu.Lock()
		var j *Job
		for _, x := range s.jobs {
			if x.ID == id {
				j = x
			}
		}
		s.mu.Unlock()
		if j == nil {
			return nil, fmt.Errorf("job %s not found", id)
		}
		if pb := j.toPB(); pb.Status != StatusRunning {
			return pb, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// Registrar is the part of the import registry an importer service calls.
type Registrar interface {
	RegisterImporter(context.Context, *connect.Request[importerv1.RegisterImporterRequest]) (*connect.Response[importerv1.RegisterImporterResponse], error)
}

// Register announces the service to the registry and keeps the registration alive until ctx is done. It retries while
// the registry is unreachable, so start order does not matter.
func Register(ctx context.Context, log *slog.Logger, registry Registrar, endpoint, token string, info *importerv1.ImporterInfo) {
	registered := false
	for {
		req := connect.NewRequest(&importerv1.RegisterImporterRequest{Info: info, Endpoint: endpoint})
		if token != "" {
			req.Header().Set(TokenHeader, token)
		}
		delay := 2 * time.Second
		resp, err := registry.RegisterImporter(ctx, req)
		switch {
		case err == nil:
			if !registered {
				log.Info("importer registered", "id", info.GetId(), "endpoint", endpoint)
				for _, u := range resp.Msg.Undeclared {
					log.Warn("importer declared by no domain in force", "domain", u.Domain, "importer", u.Importer)
				}
			}
			registered = true
			if d := time.Duration(resp.Msg.LeaseSeconds) * time.Second / 3; d > time.Second {
				delay = d
			}
		case registered:
			log.Warn("importer registration lost", "err", err)
			registered = false
		default:
			log.Warn("importer registration failed, retrying", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

// Run serves the importer and registers it with the registry. It returns when the server stops.
func Run(name string, im Importer) {
	log := platform.Logger(name)
	defer telemetry.Setup(context.Background(), log, name)(context.Background())
	token := os.Getenv("GOAP_IMPORTER_TOKEN")
	addr := platform.Env("GOAP_HTTP_ADDR", ":8080")
	srv := platform.NewServer(log, addr)
	svc := New(im, token)
	svc.Log = log
	srv.Mount(svc.Handler(telemetry.HandlerOptions()...))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registry := importerv1connect.NewImportServiceClient(platform.H2CClient(),
		platform.Env("GOAP_IMPORT_URL", platform.Env("GOAP_GRAPH_URL", "http://localhost:8081")))
	go Register(ctx, log, registry, advertised(addr), token, im.Info())
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}

func advertised(addr string) string {
	if u := os.Getenv("GOAP_IMPORTER_URL"); u != "" {
		return u
	}
	host, _ := os.Hostname()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		port = "8080"
	}
	return "http://" + net.JoinHostPort(host, port)
}
