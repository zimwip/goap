// Package jobse2e runs the graph's long runs as jobs (ADR 0149): the graph service creates them, a worker of the
// graph's kinds runs them on the job service, the person reads them and downloads their result.
package jobse2e

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/contracts/authz"
	graphv1 "github.com/zimwip/goap/contracts/gen/goap/graph/v1"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/graph/graphsvc"
	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/identity"
	"github.com/zimwip/goap/lib/jobkit"
	"github.com/zimwip/goap/lib/typecat"
	"github.com/zimwip/goap/platform/jobsvc"
)

func TestGraphImportAndExportAreJobs(t *testing.T) {
	ds, err := def.LoadDomains("../../../domains")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := typecat.New(ds...)
	if err != nil {
		t.Fatal(err)
	}
	g := graph.New(graph.NewMemory())
	g.Types = func() graph.TypeCatalog { return cat }
	authorizer, err := authz.NewCasbin(nil)
	if err != nil {
		t.Fatal(err)
	}
	jobs := &jobsvc.Service{Store: jobsvc.NewMemoryStore()}
	h := &graphsvc.Handler{Projects: graphsvc.EveryProject, Graph: g, Authz: authorizer, Jobs: jobs}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go (&jobkit.Worker{Queue: jobs, ID: "graph", Kinds: h.JobRunners(), Wait: 100 * time.Millisecond}).Run(ctx)

	as := func(m interface{ Header() http.Header }, roles string) {
		identity.SetHeaders(authz.Principal{Subject: "u", Org: "acme", Roles: strings.Split(roles, ",")}, m.Header())
	}
	person := authz.With(ctx, authz.Principal{Subject: "u"})
	wait := func(id string) jobsvc.Record {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			r, err := jobs.Get(person, id)
			if err != nil {
				t.Fatal(err)
			}
			if jobkit.Final(r.Status) {
				return r
			}
		}
		t.Fatalf("job %s did not end", id)
		return jobsvc.Record{}
	}
	result := func(id string) []byte {
		t.Helper()
		_, read, err := jobs.Result(person, id)
		if err != nil {
			t.Fatal(err)
		}
		var out []byte
		for off := int64(0); ; {
			data, eof, err := read(off)
			if err != nil {
				t.Fatal(err)
			}
			out, off = append(out, data...), off+int64(len(data))
			if eof {
				return out
			}
		}
	}

	// the import: a change created by the run, each type gated for the person
	s := graph.Snapshot{Format: graph.SnapshotFormat, Namespace: "alm", Nodes: []graph.SnapshotNode{
		{Key: "NEED-1", Type: "alm@Need", Properties: map[string]any{"title": "Pay online"}},
	}}
	doc, _ := json.Marshal(s)
	start := func(roles string) jobsvc.Record {
		req := connect.NewRequest(&graphv1.StartGraphImportRequest{Document: string(doc), ProjectId: "PROJ-ROOT"})
		as(req, roles)
		resp, err := h.StartGraphImport(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.Msg.Job.Kind != graphsvc.KindGraphImport || resp.Msg.Job.Target != "alm" {
			t.Fatalf("job %+v", resp.Msg.Job)
		}
		return wait(resp.Msg.Job.Id)
	}
	if r := start(""); r.Status != jobkit.StatusFailed {
		t.Fatalf("a person who may not create the nodes: %+v", r)
	}
	r := start("admin")
	if r.Status != jobkit.StatusDone || r.Output["changeId"] == nil || r.Stats["created"] != 1 || r.Done != r.Total {
		t.Fatalf("import: %+v", r)
	}
	if _, err := h.StartGraphImport(ctx, connect.NewRequest(&graphv1.StartGraphImportRequest{Document: "{"})); err == nil {
		t.Fatal("a malformed snapshot starts no job")
	}
	export := func(format string) (jobsvc.Record, []byte) {
		req := connect.NewRequest(&graphv1.StartGraphExportRequest{Namespace: "alm", Format: format})
		as(req, "")
		resp, err := h.StartGraphExport(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		r := wait(resp.Msg.Job.Id)
		if r.Status != jobkit.StatusDone {
			t.Fatalf("export %s: %+v", format, r)
		}
		return r, result(r.ID)
	}
	r, data := export("json")
	if r.ResultType != "application/json" || !strings.HasSuffix(r.ResultName, ".json") {
		t.Fatalf("json export: %+v", r)
	}
	var got graph.Snapshot
	if err := json.Unmarshal(data, &got); err != nil || got.Namespace != "alm" {
		t.Fatalf("snapshot %v %+v", err, got)
	}
	r, data = export("okf")
	if r.ResultType != "application/zip" || !strings.HasSuffix(r.ResultName, ".okf.zip") {
		t.Fatalf("okf export: %+v", r)
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(z.File) < 1 || z.File[0].Name != "alm/index.md" {
		t.Fatalf("okf archive: %v %+v", err, z)
	}
	req := connect.NewRequest(&graphv1.StartGraphExportRequest{Namespace: "alm", Format: "pdf"})
	as(req, "")
	if _, err := h.StartGraphExport(ctx, req); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("an unknown format: %v", err)
	}
}
