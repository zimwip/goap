package gitimporte2e

import (
	"context"
	"maps"
	"os"
	"slices"
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

// without is a tree without the paths a list of patterns leaves out.
func without(tree map[string]string, patterns ...string) map[string]string {
	out := map[string]string{}
	for p, id := range tree {
		if !gitimport.Excluded(patterns, strings.TrimSuffix(p, "/")) {
			out[p] = id
		}
	}
	return out
}

// The build artifacts found at the start of a run are asked about: what is left out is never imported, through the
// whole history, and the decision recorded on the repository node; the next run asks only about what it finds anew,
// and a path left out that the graph holds is deleted when a commit touches it.
func TestImportLeavesTheArtifactsOut(t *testing.T) {
	ctx := context.Background()
	g := newGraph(t)
	r := newRepo(t)
	r.write("src/main.c", "int main(){}\n")
	r.write("web/node_modules/x/index.js", "module.exports = 1\n")
	r.commit("first")
	r.write("web/dist/app.min.js", "a()\n")
	r.write("web/static/lib.min.js", "b()\n")
	r.write("build/Dockerfile", "FROM scratch\n")
	r.write("src/main.c", "int main(){return 0;}\n")
	r.commit("second")

	var asked [][]string
	decide := func(include ...string) func(context.Context, []gitimport.Artifact) ([]string, error) {
		return func(_ context.Context, found []gitimport.Artifact) ([]string, error) {
			var ids []string
			for _, a := range found {
				ids = append(ids, a.ID)
			}
			asked = append(asked, ids)
			return include, nil
		}
	}
	im := &gitimport.Importer{Graph: g, Dir: r.dir, Name: "demo", ProjectID: project, Decide: decide("minified")}
	if _, err := im.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || strings.Join(asked[0], ",") != "dist,minified,node_modules" {
		t.Fatalf("asked %v", asked)
	}
	got, root := graphTree(t, g, "demo")
	same(t, without(r.tree(), "node_modules", "dist"), got)
	art, _ := root.Properties["artifacts"].(map[string]any)
	if ex, _ := art["exclude"].([]any); len(ex) != 2 || ex[0] != "dist" || ex[1] != "node_modules" || len(art["include"].([]any)) != 1 {
		t.Fatalf("recorded %v", root.Properties["artifacts"])
	}

	// the next run asks about the new kind only, and leaves out what was decided
	r.write("web/node_modules/y/index.js", "module.exports = 2\n")
	r.write("tools/__pycache__/x.pyc", "\x00compiled")
	r.write("web/static/other.min.js", "c()\n")
	r.commit("third")
	im = &gitimport.Importer{Graph: g, Dir: r.dir, Name: "demo", ProjectID: project, Decide: decide()}
	if _, err := im.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 2 || strings.Join(asked[1], ",") != "__pycache__" {
		t.Fatalf("asked %v", asked)
	}
	got, _ = graphTree(t, g, "demo")
	same(t, without(r.tree(), "node_modules", "dist", "__pycache__"), got)

	// a pattern given now leaves out what the graph holds as soon as a commit touches it
	r.write("web/static/lib.min.js", "b2()\n")
	r.commit("fourth")
	im = &gitimport.Importer{Graph: g, Dir: r.dir, Name: "demo", ProjectID: project, Exclude: []string{"web/static/lib.min.js"}, Decide: decide()}
	if _, err := im.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 2 {
		t.Fatalf("asked again %v", asked)
	}
	got, _ = graphTree(t, g, "demo")
	same(t, without(r.tree(), "node_modules", "dist", "__pycache__", "web/static/lib.min.js"), got)
}

// Through the registry, a run that finds build artifacts asks the person who started it in its job: the job waits with
// the form, the person answers, the run goes on with the answer.
func TestImportAsksThePerson(t *testing.T) {
	g := newGraph(t)
	r := newRepo(t)
	r.write("src/main.c", "int main(){}\n")
	r.write("node_modules/x/index.js", "module.exports = 1\n")
	r.write("app.min.js", "a()\n")
	r.commit("first")
	code := codeDomain(t)
	jobs := &jobsvc.Service{Store: jobsvc.NewMemoryStore()}
	wctx, stop := context.WithCancel(context.Background())
	defer stop()
	w := importerkit.Worker(jobs, &gitimport.Plugin{Graph: g, Roots: []string{r.dir}})
	w.Wait = 100 * time.Millisecond
	go w.Run(wctx)
	reg := &importsvc.Service{Domains: domains{code}, Authz: authz.AllowAll{}, Jobs: jobs}
	ctx := authz.With(context.Background(), authz.Principal{Subject: "ann", Project: project})
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if ws, _ := jobs.Workers(ctx); len(ws) > 0 {
			break
		}
	}
	job, err := reg.Start(ctx, importsvc.StartRequest{Domain: gitimport.Namespace, Importer: gitimport.ImporterName,
		Params: map[string]any{"directory": r.dir, "name": "demo"}})
	if err != nil {
		t.Fatal(err)
	}
	var j jobsvc.Record
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline) && !j.Request.Pending(); time.Sleep(10 * time.Millisecond) {
		if j, err = jobs.Get(ctx, job.Id); err != nil {
			t.Fatal(err)
		}
		if jobkit.Final(j.Status) {
			t.Fatalf("ended without asking: %+v", j)
		}
	}
	f := j.Request.Form
	if len(f.Fields) != 1 || f.Fields[0].Name != "include" || f.Fields[0].Widget != "multiselect" {
		t.Fatalf("form %+v", f)
	}
	opts, _ := f.Fields[0].Constraints["options"].([]any)
	labels, _ := f.Fields[0].Constraints["labels"].([]any)
	if len(opts) != 2 || opts[0] != "minified" || opts[1] != "node_modules" || len(labels) != 2 || !strings.Contains(labels[1].(string), "node_modules") {
		t.Fatalf("options %v %v", opts, labels)
	}
	if _, err := jobs.Answer(ctx, job.Id, j.Request.ID, map[string]any{"include": []any{"minified"}}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline) && !jobkit.Final(j.Status); time.Sleep(10 * time.Millisecond) {
		if j, err = jobs.Get(ctx, job.Id); err != nil {
			t.Fatal(err)
		}
	}
	if j.Status != jobkit.StatusDone {
		t.Fatalf("ended %s: %s", j.Status, j.Error)
	}
	got, _ := graphTree(t, g, "demo")
	same(t, without(r.tree(), "node_modules"), got)
	if !slices.Contains(slices.Collect(maps.Keys(got)), "app.min.js") {
		t.Fatalf("the minified file imported anyway is missing: %v", got)
	}
}

func codeDomain(t *testing.T) *def.Domain {
	t.Helper()
	b, err := readCode()
	if err != nil {
		t.Fatal(err)
	}
	d, err := def.ParseDomain(b)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func readCode() ([]byte, error) { return os.ReadFile("../../../domains/code.yaml") }
