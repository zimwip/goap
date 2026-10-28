package graphsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	graphv1 "github.com/zimwip/goap/contracts/gen/goap/graph/v1"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/graph/okf"
	"github.com/zimwip/goap/lib/jobkit"
	"github.com/zimwip/goap/lib/rpcerr"
	"github.com/zimwip/goap/lib/typecat"
)

// The job kinds of the platform graph (ADR 0149), run by JobRunners; a content graph's carry its space (Kind).
const (
	KindGraphExport = "graph.export"
	KindGraphImport = "graph.import"
)

// The formats of an export.
const (
	FormatJSON = "json"
	FormatOKF  = "okf"
)

func (h *Handler) jobs() (jobkit.Queue, error) {
	if h.Jobs == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("no job service: this deployment runs no long job"))
	}
	return h.Jobs, nil
}

func (h *Handler) caller(ctx context.Context) (authz.Principal, error) {
	p := authz.From(ctx)
	if p.Anonymous() || p.EndUser() == "" {
		return p, connect.NewError(connect.CodeUnauthenticated, errors.New("a job runs for an identified person"))
	}
	return p, nil
}

// StartGraphExport exports the state of a namespace as a job: the branch head or the commit are checked now, the
// state is read and written as the result of the job by its run.
func (h *Handler) StartGraphExport(ctx context.Context, r *connect.Request[graphv1.StartGraphExportRequest]) (*connect.Response[graphv1.StartGraphExportResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	q, err := h.jobs()
	if err != nil {
		return nil, err
	}
	p, err := h.caller(ctx)
	if err != nil {
		return nil, err
	}
	m := r.Msg
	format := m.Format
	if format == "" {
		format = FormatJSON
	}
	if format != FormatJSON && format != FormatOKF {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("format %q: json or okf", format))
	}
	ns := domain.NamespaceOf(m.Namespace)
	at := domain.BranchOf(m.Branch)
	if m.CommitId != "" {
		b, err := h.Graph.Commit(ctx, domain.CommitID(m.CommitId))
		if err != nil {
			return nil, rpcerr.ToConnect(err)
		}
		if b.Namespace != ns {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("commit %s is a commit of %s, not %s", m.CommitId, b.Namespace, ns))
		}
		at = m.CommitId
	} else if _, err := h.Graph.BranchHead(ctx, ns, at); err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	label := fmt.Sprintf("Export %s at %s (%s)", ns, at, map[string]string{FormatJSON: "JSON", FormatOKF: "OKF"}[format])
	job, err := q.Create(ctx, jobkit.Spec{Kind: h.Kind(KindGraphExport), Label: label, Target: ns, By: p.EndUser(), Principal: p, ProjectID: p.Project,
		Params: map[string]any{"namespace": ns, "branch": m.Branch, "commitId": m.CommitId, "format": format}})
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	return connect.NewResponse(&graphv1.StartGraphExportResponse{Job: job}), nil
}

// StartGraphImport imports a JSON snapshot as a job: the snapshot is read and the unit resolved now, the change is
// written by the run, each type gated for the person like ImportGraph.
func (h *Handler) StartGraphImport(ctx context.Context, r *connect.Request[graphv1.StartGraphImportRequest]) (*connect.Response[graphv1.StartGraphImportResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	q, err := h.jobs()
	if err != nil {
		return nil, err
	}
	p, err := h.caller(ctx)
	if err != nil {
		return nil, err
	}
	m := r.Msg
	s, err := graph.ParseSnapshot([]byte(m.Document))
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	owner, err := h.resolveOwner(ctx, m.OwnerOrg)
	if err != nil {
		return nil, err
	}
	project := h.projectOf(ctx, m.ProjectId)
	job, err := q.Create(ctx, jobkit.Spec{Kind: h.Kind(KindGraphImport), Label: fmt.Sprintf("Import a snapshot of %s (%d nodes)", s.Namespace, len(s.Nodes)),
		Target: s.Namespace, By: p.EndUser(), Principal: p, ProjectID: project, OwnerOrg: owner,
		Params: map[string]any{"title": m.Title, "intent": m.Intent, "methodology": m.Methodology}, Input: []byte(m.Document)})
	if err != nil {
		return nil, rpcerr.ToConnect(err)
	}
	return connect.NewResponse(&graphv1.StartGraphImportResponse{Job: job}), nil
}

// Kind is the name of a job kind of this graph: as is for the platform graph, followed by its space for another
// (graph.export.content), so that a job reaches the worker of the graph holding its namespace.
func (h *Handler) Kind(kind string) string {
	if h.Space == "" {
		return kind
	}
	return kind + "." + h.Space
}

// JobRunners are the runners of the graph's job kinds, for a jobkit.Worker of the graph's process.
func (h *Handler) JobRunners() map[string]jobkit.Runner {
	return map[string]jobkit.Runner{h.Kind(KindGraphExport): h.runExport, h.Kind(KindGraphImport): h.runImport}
}

func (h *Handler) okfOptions() okf.Options {
	var o okf.Options
	if h.Graph.Types != nil {
		if c, ok := h.Graph.Types().(interface {
			Type(string) (*typecat.Type, bool)
		}); ok {
			o = okf.FromTypes(c.Type)
		}
	}
	return o
}

func (h *Handler) runExport(ctx context.Context, j *jobkit.Job) error {
	j.Progress("reading the state")
	s, err := h.Graph.ExportSnapshot(ctx, j.String("namespace"), j.String("branch"), domain.CommitID(j.String("commitId")))
	if err != nil {
		return err
	}
	total := int64(len(s.Nodes))
	j.Set("nodes", total)
	stamp := s.ExportedAt.Format("20060102-150405")
	if j.String("format") == FormatOKF {
		o := h.okfOptions()
		o.Progress = func(done, _ int) {
			if done%50 == 0 {
				j.Step(int64(done), total)
			}
		}
		j.Progress("writing the OKF tree")
		w := j.Result(ctx, fmt.Sprintf("graph-%s-%s.okf.zip", s.Namespace, stamp), "application/zip")
		if err := okf.Write(w, s, o, s.ExportedAt); err != nil {
			return err
		}
		if err := w.Close(); err != nil {
			return err
		}
	} else {
		doc, err := json.MarshalIndent(s, "", "  ")
		if err != nil {
			return err
		}
		w := j.Result(ctx, fmt.Sprintf("graph-%s-%s.json", s.Namespace, stamp), "application/json")
		if _, err := w.Write(doc); err != nil {
			return err
		}
		if err := w.Close(); err != nil {
			return err
		}
	}
	j.Step(total, total)
	j.Progress(fmt.Sprintf("%d node(s) exported", total))
	j.Output(map[string]any{"nodes": total, "commitId": s.Commit, "branch": s.Branch})
	return nil
}

func (h *Handler) runImport(ctx context.Context, j *jobkit.Job) error {
	doc, err := j.Input(ctx)
	if err != nil {
		return err
	}
	s, err := graph.ParseSnapshot(doc)
	if err != nil {
		return err
	}
	j.Progress(fmt.Sprintf("importing %d node(s) into %s", len(s.Nodes), s.Namespace))
	out, err := h.Graph.ImportSnapshot(ctx, graph.SnapshotImport{Snapshot: s, Title: j.String("title"), Intent: j.String("intent"),
		Methodology: j.String("methodology"), OwnerOrg: j.OwnerOrg, ProjectID: j.ProjectID, By: authz.From(ctx).Subject,
		Gate:     func(c domain.Change, typ string) error { return h.checkCreate(ctx, c, typ) },
		Progress: func(done, total int) { j.Step(int64(done), int64(total)) }})
	if err != nil {
		return err
	}
	j.Set("created", int64(out.Created))
	j.Set("updated", int64(out.Updated))
	j.Set("unchanged", int64(out.Unchanged))
	res := map[string]any{"created": out.Created, "updated": out.Updated, "unchanged": out.Unchanged, "warnings": toAny(out.Warnings)}
	if out.Change != "" {
		res["changeId"] = string(out.Change)
		if c, err := h.Graph.Change(ctx, out.Change); err == nil {
			res["changeTitle"] = c.Title
		}
		j.Progress("change created: " + string(out.Change))
	} else {
		j.Progress("the graph already holds this snapshot: nothing to import")
	}
	j.Output(res)
	return nil
}

func toAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
