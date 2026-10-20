package importsvc_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/contracts/authz"
	importerv1 "github.com/zimwip/goap/contracts/gen/goap/importer/v1"
	"github.com/zimwip/goap/contracts/gen/goap/importer/v1/importerv1connect"
	"github.com/zimwip/goap/graph/importsvc"
	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/importerkit"
)

type domains []*def.Domain

func (d domains) Domains(context.Context) ([]*def.Domain, error) { return d, nil }

var codeDomain = &def.Domain{Name: "code", Version: "1.1.0", Schema: def.Schema{
	Enums: []def.Enum{{Name: "mode", Values: []def.EnumValue{{Value: "full"}, {Value: "fast"}}}},
	Importers: []def.Importer{
		{Name: "git", Label: "Git repository", Params: []def.Attribute{
			{Name: "repository"}, {Name: "rev", Default: "HEAD"}, {Name: "max", Type: def.AttrNumber},
			{Name: "symbols", Type: def.AttrBoolean}, {Name: "mode", Type: def.AttrEnum, Enum: "mode"}}},
		{Name: "svn"},
	}}}

// fake is an importer that records its runs and ends when told.
type fake struct {
	release chan struct{}
	seen    chan *importerkit.Job
}

func (f *fake) Info() *importerv1.ImporterInfo {
	return &importerv1.ImporterInfo{Id: "importer-fake", Implements: []*importerv1.Implements{{Domain: "code", Importer: "git"}}}
}

func (f *fake) Run(ctx context.Context, j *importerkit.Job) error {
	j.Progress("working")
	j.Set("commits", 3)
	if who := authz.From(ctx); who.Subject != "system:importer-fake" || who.EndUser() != j.By || !who.HasRole("admin") {
		return errors.New("unexpected principal " + who.Subject)
	}
	f.seen <- j
	select {
	case <-f.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func person(subject string, roles ...string) context.Context {
	return authz.With(context.Background(), authz.Principal{Subject: subject, Project: "PROJ-ROOT", Roles: roles})
}

func registry(t *testing.T, a authz.Authorizer) (*importsvc.Service, *fake, *importerkit.Service) {
	t.Helper()
	f := &fake{release: make(chan struct{}), seen: make(chan *importerkit.Job, 4)}
	svc := importerkit.New(f, "")
	reg := &importsvc.Service{Domains: domains{codeDomain}, Authz: a}
	reg.AddLocal(svc)
	return reg, f, svc
}

func TestImportersFollowTheDomains(t *testing.T) {
	reg, _, _ := registry(t, authz.AllowAll{})
	ims, err := reg.Importers(person("ann"), "code")
	if err != nil {
		t.Fatal(err)
	}
	if len(ims) != 2 || ims[0].Def.Name != "git" || ims[0].Service != "importer-fake" || ims[1].Def.Name != "svn" || ims[1].Service != "" {
		t.Fatalf("importers = %+v", ims)
	}
	if ims, _ := reg.Importers(person("ann"), "alm"); len(ims) != 0 {
		t.Fatalf("alm declares no importer: %+v", ims)
	}
}

func TestStartChecksAndRuns(t *testing.T) {
	reg, f, svc := registry(t, authz.AllowAll{})
	ctx := person("ann")
	for _, c := range []struct {
		params map[string]any
		err    string
	}{
		{map[string]any{"unknown": "x"}, "not a parameter"},
		{map[string]any{"max": "many"}, "a number is expected"},
		{map[string]any{"symbols": "yes"}, "a boolean is expected"},
		{map[string]any{"mode": "slow"}, "not a value of the enum"},
	} {
		if _, err := reg.Start(ctx, importsvc.StartRequest{Domain: "code", Importer: "git", Params: c.params}); !errors.Is(err, importsvc.ErrInvalid) || !strings.Contains(err.Error(), c.err) {
			t.Errorf("%v: err = %v, want %s", c.params, err, c.err)
		}
	}
	if _, err := reg.Start(ctx, importsvc.StartRequest{Domain: "code", Importer: "svn"}); !errors.Is(err, importsvc.ErrUnavailable) {
		t.Errorf("svn: err = %v, want unavailable", err)
	}
	if _, err := reg.Start(ctx, importsvc.StartRequest{Domain: "code", Importer: "hg"}); !errors.Is(err, importsvc.ErrNotFound) {
		t.Errorf("hg: err = %v, want not found", err)
	}
	job, err := reg.Start(ctx, importsvc.StartRequest{Domain: "code", Importer: "git", Params: map[string]any{"repository": "/r", "max": 2.0, "mode": "fast"}})
	if err != nil {
		t.Fatal(err)
	}
	if job.Service != "importer-fake" || job.Status != importerkit.StatusRunning || job.By != "ann" || job.ProjectId != "PROJ-ROOT" {
		t.Fatalf("job = %+v", job)
	}
	j := <-f.seen
	if j.String("rev") != "HEAD" || j.Int("max") != 2 || j.String("repository") != "/r" {
		t.Fatalf("params = %v (the default of rev given)", j.Params)
	}
	// the jobs: the caller's own, every one for an administrator
	if jobs, _ := reg.Jobs(person("bob"), "code"); len(jobs) != 0 {
		t.Fatalf("bob sees %d jobs", len(jobs))
	}
	jobs, err := reg.Jobs(person("root", "admin"), "")
	if err != nil || len(jobs) != 1 || jobs[0].Progress != "working" || jobs[0].Stats["commits"] != 3 {
		t.Fatalf("jobs = %+v, %v", jobs, err)
	}
	close(f.release)
	done, err := svc.Wait(context.Background(), job.Id)
	if err != nil || done.Status != importerkit.StatusDone {
		t.Fatalf("done = %+v, %v", done, err)
	}
}

func TestStartNeedsThePermission(t *testing.T) {
	reg, _, _ := registry(t, nil)
	_, err := reg.Start(person("ann"), importsvc.StartRequest{Domain: "code", Importer: "git"})
	if !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("err = %v, want forbidden (no authorizer: everything refused)", err)
	}
	if _, err := reg.Start(context.Background(), importsvc.StartRequest{Domain: "code", Importer: "git"}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("anonymous: err = %v", err)
	}
}

func TestCancel(t *testing.T) {
	reg, f, svc := registry(t, authz.AllowAll{})
	job, err := reg.Start(person("ann"), importsvc.StartRequest{Domain: "code", Importer: "git"})
	if err != nil {
		t.Fatal(err)
	}
	<-f.seen
	if _, err := reg.Cancel(person("ann"), "importer-fake", job.Id); err != nil {
		t.Fatal(err)
	}
	done, err := svc.Wait(context.Background(), job.Id)
	if err != nil || done.Status != importerkit.StatusCancelled {
		t.Fatalf("done = %+v, %v", done, err)
	}
}

// A service of another process registers with the token, over Connect, and is reached at its endpoint with it.
func TestRemoteImporter(t *testing.T) {
	f := &fake{release: make(chan struct{}), seen: make(chan *importerkit.Job, 1)}
	svc := importerkit.New(f, "secret")
	mux := http.NewServeMux()
	mux.Handle(svc.Handler())
	importer := httptest.NewServer(mux)
	defer importer.Close()

	reg := &importsvc.Service{Domains: domains{codeDomain}, Authz: authz.AllowAll{}, Token: "secret", Lease: time.Minute}
	rmux := http.NewServeMux()
	rmux.Handle(importerv1connect.NewImportServiceHandler(&importsvc.Handler{Service: reg}))
	registrySrv := httptest.NewServer(rmux)
	defer registrySrv.Close()
	client := importerv1connect.NewImportServiceClient(http.DefaultClient, registrySrv.URL)

	if _, err := client.RegisterImporter(context.Background(), connect.NewRequest(&importerv1.RegisterImporterRequest{Info: f.Info(), Endpoint: importer.URL})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("no token: err = %v", err)
	}
	info := f.Info()
	info.Implements = append(info.Implements, &importerv1.Implements{Domain: "code", Importer: "cvs"})
	req := connect.NewRequest(&importerv1.RegisterImporterRequest{Info: info, Endpoint: importer.URL})
	req.Header().Set(importerkit.TokenHeader, "secret")
	resp, err := client.RegisterImporter(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.LeaseSeconds != 60 || len(resp.Msg.Undeclared) != 1 || resp.Msg.Undeclared[0].Importer != "cvs" {
		t.Fatalf("register = %+v", resp.Msg)
	}
	job, err := reg.Start(person("ann"), importsvc.StartRequest{Domain: "code", Importer: "git"})
	if err != nil {
		t.Fatal(err)
	}
	<-f.seen
	close(f.release)
	done, err := svc.Wait(context.Background(), job.Id)
	if err != nil || done.Status != importerkit.StatusDone {
		t.Fatalf("done = %+v, %v", done, err)
	}
	// the service refuses a call without the token
	direct := importerv1connect.NewImporterServiceClient(http.DefaultClient, importer.URL)
	if _, err := direct.Jobs(context.Background(), connect.NewRequest(&importerv1.JobsRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("no token: err = %v", err)
	}
}
