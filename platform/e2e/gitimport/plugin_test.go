package gitimporte2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/graph/importsvc"
	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/importerkit"
	"github.com/zimwip/goap/lib/jobkit"
	"github.com/zimwip/goap/platform/gitimport"
	"github.com/zimwip/goap/platform/jobsvc"
)

type domains []*def.Domain

func (d domains) Domains(context.Context) ([]*def.Domain, error) { return d, nil }

// The git importer as an importer service (ADR 0148): run through the import registry from the declaration of the code
// domain, as a person, it imports a repository under its roots and resumes at the next run.
func TestImportThroughTheRegistry(t *testing.T) {
	g := newGraph(t)
	r := newRepo(t)
	r.write("README", "hello\n")
	r.write("src/main.c", "int main(){}\n")
	r.commit("first")
	r.write("src/main.c", "int main(){return 0;}\n")
	r.commit("second")

	b, err := os.ReadFile("../../../domains/code.yaml")
	if err != nil {
		t.Fatal(err)
	}
	code, err := def.ParseDomain(b)
	if err != nil {
		t.Fatal(err)
	}
	if issues := code.Validate(); issues.HasErrors() {
		t.Fatalf("code domain: %v", issues)
	}
	plain := t.TempDir() // a directory the service reads that holds no repository
	// the runs are jobs (ADR 0149): the git importer is a worker of the job service
	jobs := &jobsvc.Service{Store: jobsvc.NewMemoryStore()}
	wctx, stop := context.WithCancel(context.Background())
	defer stop()
	w := importerkit.Worker(jobs, &gitimport.Plugin{Graph: g, Roots: []string{r.dir, plain}})
	w.Wait = 100 * time.Millisecond
	go w.Run(wctx)
	reg := &importsvc.Service{Domains: domains{code}, Authz: authz.AllowAll{}, Jobs: jobs}
	ctx := authz.With(context.Background(), authz.Principal{Subject: "ann", Project: project})
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if ws, _ := jobs.Workers(ctx); len(ws) > 0 {
			break
		}
	}

	ims, err := reg.Importers(ctx, gitimport.Namespace)
	if err != nil || len(ims) != 1 || ims[0].Def.Name != gitimport.ImporterName || !strings.HasPrefix(ims[0].Service, gitimport.ServiceID+"@") {
		t.Fatalf("importers = %+v, %v", ims, err)
	}
	run := func(params map[string]any) jobsvc.Record {
		t.Helper()
		job, err := reg.Start(ctx, importsvc.StartRequest{Domain: gitimport.Namespace, Importer: gitimport.ImporterName, Params: params})
		if err != nil {
			t.Fatal(err)
		}
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			j, err := jobs.Get(ctx, job.Id)
			if err != nil {
				t.Fatal(err)
			}
			if jobkit.Final(j.Status) {
				return j
			}
		}
		t.Fatalf("job %s did not end", job.Id)
		return jobsvc.Record{}
	}

	j := run(map[string]any{"directory": r.dir, "name": "demo", "max": 1})
	if j.Status != jobkit.StatusDone || j.Stats["commits"] != 1 {
		t.Fatalf("first run: %+v", j)
	}
	j = run(map[string]any{"directory": r.dir, "name": "demo"})
	if j.Status != jobkit.StatusDone || j.Stats["commits"] != 1 || !strings.Contains(j.Progress, "demo: 1 commits") {
		t.Fatalf("second run (resumed): %+v", j)
	}
	got, root := graphTree(t, g, "demo")
	same(t, r.tree(), got)
	if root.Properties["commit"] != strings.TrimSpace(r.git("rev-parse", "HEAD")) {
		t.Fatalf("repository node at %v", root.Properties["commit"])
	}

	// a directory outside the roots of the service, a directory under one that holds no repository, a URL of another
	// scheme, a path given as the URL, both or neither: refused
	for _, params := range []map[string]any{{"directory": t.TempDir()}, {"directory": plain}, {"url": "file:///etc"},
		{"url": r.dir}, {"url": "https://example.com/r.git", "directory": r.dir}, {}} {
		params["name"] = "other"
		if j := run(params); j.Status != jobkit.StatusFailed {
			t.Errorf("%v: %+v, want failed", params, j)
		}
	}
}
