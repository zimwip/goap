package importsvc_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zimwip/goap/contracts/authz"
	importerv1 "github.com/zimwip/goap/contracts/gen/goap/importer/v1"
	jobsv1 "github.com/zimwip/goap/contracts/gen/goap/jobs/v1"
	"github.com/zimwip/goap/graph/importsvc"
	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/importerkit"
	"github.com/zimwip/goap/lib/jobkit"
	"github.com/zimwip/goap/lib/pbconv"
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

// queue is a job queue recording the jobs it is given, with a live worker of import.code.git.
type queue struct {
	jobkit.Queue
	created []jobkit.Spec
}

func (q *queue) Workers(context.Context) ([]*jobsv1.Worker, error) {
	return []*jobsv1.Worker{{Id: "importer-fake@host:1", Kinds: []string{importerkit.Kind("code", "git")}}}, nil
}

func (q *queue) Create(_ context.Context, s jobkit.Spec) (*jobsv1.Job, error) {
	q.created = append(q.created, s)
	return &jobsv1.Job{Id: "job-1", Kind: s.Kind, Label: s.Label, Target: s.Target, Status: jobkit.StatusQueued, By: s.By, ProjectId: s.ProjectID,
		Params: pbconv.Struct(s.Params)}, nil
}

func person(subject string, roles ...string) context.Context {
	return authz.With(context.Background(), authz.Principal{Subject: subject, Project: "PROJ-ROOT", Roles: roles})
}

func registry(a authz.Authorizer) (*importsvc.Service, *queue) {
	q := &queue{}
	return &importsvc.Service{Domains: domains{codeDomain}, Authz: a, Jobs: q}, q
}

func TestImportersFollowTheDomains(t *testing.T) {
	reg, _ := registry(authz.AllowAll{})
	ims, err := reg.Importers(person("ann"), "code")
	if err != nil {
		t.Fatal(err)
	}
	if len(ims) != 2 || ims[0].Def.Name != "git" || ims[0].Service != "importer-fake@host:1" || ims[1].Def.Name != "svn" || ims[1].Service != "" {
		t.Fatalf("importers = %+v", ims)
	}
	if ims, _ := reg.Importers(person("ann"), "alm"); len(ims) != 0 {
		t.Fatalf("alm declares no importer: %+v", ims)
	}
}

func TestStartChecksAndQueues(t *testing.T) {
	reg, q := registry(authz.AllowAll{})
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
	if job.Kind != "import.code.git" || job.Label != "Git repository" || job.Target != "code" || job.By != "ann" || job.ProjectId != "PROJ-ROOT" {
		t.Fatalf("job = %+v", job)
	}
	if len(q.created) != 1 {
		t.Fatalf("%d jobs created", len(q.created))
	}
	sp := q.created[0]
	if sp.Params["rev"] != "HEAD" || sp.Params["max"] != 2.0 || sp.Params["repository"] != "/r" || sp.Principal.Subject != "ann" {
		t.Fatalf("spec = %+v (the default of rev given)", sp)
	}
}

func TestStartNeedsThePermission(t *testing.T) {
	reg, q := registry(nil)
	_, err := reg.Start(person("ann"), importsvc.StartRequest{Domain: "code", Importer: "git"})
	if !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("err = %v, want forbidden (no authorizer: everything refused)", err)
	}
	if _, err := reg.Start(context.Background(), importsvc.StartRequest{Domain: "code", Importer: "git"}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("anonymous: err = %v", err)
	}
	if len(q.created) != 0 {
		t.Fatalf("%d jobs created", len(q.created))
	}
}

// An importer's runners act as its service on behalf of the person, one kind per importer it implements.
func TestRunnersActAsTheService(t *testing.T) {
	var who authz.Principal
	run := importerkit.Runners(fake{func(ctx context.Context, j *importerkit.Job) error { who = authz.From(ctx); return nil }})
	r := run["import.code.git"]
	if r == nil || len(run) != 1 {
		t.Fatalf("runners = %v", run)
	}
	if err := r(person("ann"), &jobkit.Job{By: "ann"}); err != nil {
		t.Fatal(err)
	}
	if who.Subject != "system:importer-fake" || who.EndUser() != "ann" || !who.HasRole("admin") {
		t.Fatalf("principal = %+v", who)
	}
	if d, im, ok := importerkit.SplitKind("import.code.git"); !ok || d != "code" || im != "git" {
		t.Fatalf("split = %s %s %v", d, im, ok)
	}
	if _, _, ok := importerkit.SplitKind("graph.export"); ok {
		t.Fatal("graph.export is no import")
	}
}

type fake struct {
	run func(context.Context, *importerkit.Job) error
}

func (f fake) Info() *importerv1.ImporterInfo {
	return &importerv1.ImporterInfo{Id: "importer-fake", Implements: []*importerv1.Implements{{Domain: "code", Importer: "git"}}}
}

func (f fake) Run(ctx context.Context, j *importerkit.Job) error { return f.run(ctx, j) }
