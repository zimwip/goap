package gitimporte2e

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/zimwip/goap/contracts/authz"
	importerv1 "github.com/zimwip/goap/contracts/gen/goap/importer/v1"
	"github.com/zimwip/goap/graph/importsvc"
	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/importerkit"
	"github.com/zimwip/goap/platform/gitimport"
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
	svc := importerkit.New(&gitimport.Plugin{Graph: g, Roots: []string{r.dir}}, "")
	reg := &importsvc.Service{Domains: domains{code}, Authz: authz.AllowAll{}}
	reg.AddLocal(svc)
	ctx := authz.With(context.Background(), authz.Principal{Subject: "ann", Project: project})

	ims, err := reg.Importers(ctx, gitimport.Namespace)
	if err != nil || len(ims) != 1 || ims[0].Def.Name != gitimport.ImporterName || ims[0].Service != gitimport.ServiceID {
		t.Fatalf("importers = %+v, %v", ims, err)
	}
	run := func(params map[string]any) *importerv1.Job {
		t.Helper()
		job, err := reg.Start(ctx, importsvc.StartRequest{Domain: gitimport.Namespace, Importer: gitimport.ImporterName, Params: params})
		if err != nil {
			t.Fatal(err)
		}
		done, err := svc.Wait(context.Background(), job.Id)
		if err != nil {
			t.Fatal(err)
		}
		return done
	}

	j := run(map[string]any{"repository": r.dir, "name": "demo", "max": 1})
	if j.Status != importerkit.StatusDone || j.Stats["commits"] != 1 {
		t.Fatalf("first run: %+v", j)
	}
	j = run(map[string]any{"repository": r.dir, "name": "demo"})
	if j.Status != importerkit.StatusDone || j.Stats["commits"] != 1 || !strings.Contains(j.Progress, "demo: 1 commits") {
		t.Fatalf("second run (resumed): %+v", j)
	}
	got, root := graphTree(t, g, "demo")
	same(t, r.tree(), got)
	if root.Properties["commit"] != strings.TrimSpace(r.git("rev-parse", "HEAD")) {
		t.Fatalf("repository node at %v", root.Properties["commit"])
	}

	// a path outside the roots of the service, a URL of another scheme: refused
	for _, repo := range []string{t.TempDir(), "file:///etc"} {
		if j := run(map[string]any{"repository": repo, "name": "other"}); j.Status != importerkit.StatusFailed {
			t.Errorf("%s: %+v, want failed", repo, j)
		}
	}
}
