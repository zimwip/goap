package jobsvc

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/labstack/echo/v5"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/zimwip/goap/contracts/authz"
	jobsv1 "github.com/zimwip/goap/contracts/gen/goap/jobs/v1"
	"github.com/zimwip/goap/contracts/gen/goap/jobs/v1/jobsv1connect"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/identity"
	"github.com/zimwip/goap/lib/jobkit"
	"github.com/zimwip/goap/lib/pbconv"
)

// HTTPPrefix is where the stream and the downloads are served (routed by the gateway).
const HTTPPrefix = "/api/jobs/"

// HeartbeatEvery is how often an idle stream says it is alive.
const HeartbeatEvery = 15 * time.Second

// SnapshotSize is how many recent jobs a stream starts with.
const SnapshotSize = 50

// Handler implements jobsv1connect.JobServiceHandler: the people's calls act for the caller, the services' need the
// token (none set: the services' calls are refused).
type Handler struct {
	Service  *Service
	Identity identity.Extractor
	Token    string
	// Idle overrides HeartbeatEvery (tests).
	Idle time.Duration
}

var _ jobsv1connect.JobServiceHandler = (*Handler)(nil)

func rpcErr(err error) error {
	var ce *connect.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ce):
		return err
	case errors.Is(err, jobkit.ErrForbidden):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, jobkit.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, jobkit.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, jobkit.ErrLost):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeCanceled, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}

func (h *Handler) service(hd http.Header) error {
	if h.Token == "" {
		return connect.NewError(connect.CodePermissionDenied, errors.New("the job service takes no service call: it has no token (GOAP_JOB_TOKEN)"))
	}
	if subtle.ConstantTimeCompare([]byte(hd.Get(jobkit.TokenHeader)), []byte(h.Token)) != 1 {
		return connect.NewError(connect.CodePermissionDenied, errors.New("invalid job token"))
	}
	return nil
}

// GetJob implements the protocol.
func (h *Handler) GetJob(ctx context.Context, r *connect.Request[jobsv1.GetJobRequest]) (*connect.Response[jobsv1.GetJobResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	j, err := h.Service.Get(ctx, r.Msg.Id)
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&jobsv1.GetJobResponse{Job: ToPB(j)}), nil
}

// ListJobs implements the protocol.
func (h *Handler) ListJobs(ctx context.Context, r *connect.Request[jobsv1.ListJobsRequest]) (*connect.Response[jobsv1.ListJobsResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	m := r.Msg
	js, err := h.Service.List(ctx, ListRequest{KindPrefix: m.KindPrefix, Target: m.Target, All: m.All, Limit: int(m.Limit)})
	if err != nil {
		return nil, rpcErr(err)
	}
	out := &jobsv1.ListJobsResponse{}
	for _, j := range js {
		out.Jobs = append(out.Jobs, ToPB(j))
	}
	return connect.NewResponse(out), nil
}

// CancelJob implements the protocol.
func (h *Handler) CancelJob(ctx context.Context, r *connect.Request[jobsv1.CancelJobRequest]) (*connect.Response[jobsv1.CancelJobResponse], error) {
	ctx = h.Identity.Context(ctx, r.Header())
	j, err := h.Service.Cancel(ctx, r.Msg.Id)
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&jobsv1.CancelJobResponse{Job: ToPB(j)}), nil
}

// CreateJob implements the protocol.
func (h *Handler) CreateJob(ctx context.Context, r *connect.Request[jobsv1.CreateJobRequest]) (*connect.Response[jobsv1.CreateJobResponse], error) {
	if err := h.service(r.Header()); err != nil {
		return nil, err
	}
	m := r.Msg
	j, err := h.Service.Create(ctx, jobkit.Spec{Kind: m.Kind, Label: m.Label, Target: m.Target, Params: pbconv.Map(m.Params), ProjectID: m.ProjectId,
		OwnerOrg: m.OwnerOrg, By: m.By, Principal: jobkit.DecodePrincipal(m.Principal), Input: m.Input})
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&jobsv1.CreateJobResponse{Job: j}), nil
}

// ListWorkers implements the protocol.
func (h *Handler) ListWorkers(ctx context.Context, r *connect.Request[jobsv1.ListWorkersRequest]) (*connect.Response[jobsv1.ListWorkersResponse], error) {
	if err := h.service(r.Header()); err != nil {
		return nil, err
	}
	ws, err := h.Service.Workers(ctx)
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&jobsv1.ListWorkersResponse{Workers: ws}), nil
}

// ClaimJob implements the protocol.
func (h *Handler) ClaimJob(ctx context.Context, r *connect.Request[jobsv1.ClaimJobRequest]) (*connect.Response[jobsv1.ClaimJobResponse], error) {
	if err := h.service(r.Header()); err != nil {
		return nil, err
	}
	c, err := h.Service.Claim(ctx, r.Msg.Worker, r.Msg.Kinds, time.Duration(r.Msg.WaitMs)*time.Millisecond)
	if err != nil {
		return nil, rpcErr(err)
	}
	out := &jobsv1.ClaimJobResponse{}
	if c != nil {
		out.Job, out.Principal, out.LeaseSeconds = c.Job, jobkit.EncodePrincipal(c.Principal), int32(c.Lease/time.Second)
	}
	return connect.NewResponse(out), nil
}

// ReportJob implements the protocol.
func (h *Handler) ReportJob(ctx context.Context, r *connect.Request[jobsv1.ReportJobRequest]) (*connect.Response[jobsv1.ReportJobResponse], error) {
	if err := h.service(r.Header()); err != nil {
		return nil, err
	}
	m := r.Msg
	cancel, err := h.Service.Report(ctx, m.Id, m.Worker, jobkit.Report{Progress: m.Progress, Done: m.Done, Total: m.Total, Stats: m.Stats,
		Output: pbconv.Map(m.Output), Status: m.Status, Error: m.Error})
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&jobsv1.ReportJobResponse{Cancel: cancel}), nil
}

// ReadJobInput implements the protocol.
func (h *Handler) ReadJobInput(ctx context.Context, r *connect.Request[jobsv1.ReadJobInputRequest]) (*connect.Response[jobsv1.ReadJobInputResponse], error) {
	if err := h.service(r.Header()); err != nil {
		return nil, err
	}
	data, eof, err := h.Service.ReadInput(ctx, r.Msg.Id, r.Msg.Worker, r.Msg.Offset)
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&jobsv1.ReadJobInputResponse{Data: data, Eof: eof}), nil
}

// WriteJobResult implements the protocol.
func (h *Handler) WriteJobResult(ctx context.Context, r *connect.Request[jobsv1.WriteJobResultRequest]) (*connect.Response[jobsv1.WriteJobResultResponse], error) {
	if err := h.service(r.Header()); err != nil {
		return nil, err
	}
	m := r.Msg
	size, err := h.Service.WriteResult(ctx, m.Id, m.Worker, m.Name, m.MediaType, m.Data)
	if err != nil {
		return nil, rpcErr(err)
	}
	return connect.NewResponse(&jobsv1.WriteJobResultResponse{Size: size}), nil
}

// Mount serves the stream (GET /api/jobs/events) and the downloads (GET /api/jobs/<id>/result) on e, behind mw (the
// sign-in of a composition serving people directly; none behind the gateway).
func (h *Handler) Mount(e *echo.Echo, mw ...echo.MiddlewareFunc) {
	e.GET(HTTPPrefix+"events", h.Events, mw...)
	e.GET(HTTPPrefix+":id/result", h.Download, mw...)
}

// jobJSON is the JSON of a job the web reads: the Connect JSON of the protocol message, so the stream and the RPCs
// answer the same shape.
func jobJSON(r Record) ([]byte, error) { return protojson.Marshal(ToPB(r)) }

// Events streams the caller's jobs as Server-Sent Events: `snapshot` (the recent jobs, newest first), then `job` at each
// write of one. `?job=<id>` follows one job; `?all=1` every person's (administrators).
func (h *Handler) Events(c *echo.Context) error {
	req := c.Request()
	ctx := h.Identity.Context(req.Context(), req.Header)
	p := authz.From(ctx)
	if p.Anonymous() {
		return echo.NewHTTPError(http.StatusUnauthorized, "the caller is not identified")
	}
	id := c.QueryParam("job")
	all := c.QueryParam("all") == "1"
	if all && !p.HasRole(access.RoleAdmin) {
		return echo.NewHTTPError(http.StatusForbidden, "every person's jobs: administrators only")
	}
	// subscribe first: a write between the snapshot and the subscription is sent twice rather than lost
	ch, cancel := h.Service.Subscribe(p, id, all)
	defer cancel()
	var snapshot []Record
	if id != "" {
		r, err := h.Service.Get(ctx, id)
		if err != nil {
			return echo.NewHTTPError(http.StatusNotFound, err.Error())
		}
		snapshot = []Record{r}
	} else {
		var err error
		if snapshot, err = h.Service.List(ctx, ListRequest{All: all, Limit: SnapshotSize}); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
	}
	w := c.Response()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flush := func() { http.NewResponseController(w).Flush() }
	send := func(event string, data []byte) error {
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
			return err
		}
		flush()
		return nil
	}
	list := make([]json.RawMessage, 0, len(snapshot))
	for _, r := range snapshot {
		b, err := jobJSON(r)
		if err != nil {
			return err
		}
		list = append(list, b)
	}
	b, _ := json.Marshal(map[string]any{"jobs": list})
	if _, err := fmt.Fprint(w, "retry: 3000\n"); err != nil {
		return nil
	}
	if err := send("snapshot", b); err != nil {
		return nil
	}
	every := h.Idle
	if every <= 0 {
		every = HeartbeatEvery
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return nil
			}
			flush()
		case r, ok := <-ch:
			if !ok {
				// too slow to follow: the client reconnects and starts from a snapshot
				return nil
			}
			b, err := jobJSON(r)
			if err != nil {
				return err
			}
			if err := send("job", b); err != nil {
				return nil
			}
		}
	}
}

// Download serves the result of a job of the caller.
func (h *Handler) Download(c *echo.Context) error {
	req := c.Request()
	ctx := h.Identity.Context(req.Context(), req.Header)
	if authz.From(ctx).Anonymous() {
		return echo.NewHTTPError(http.StatusUnauthorized, "the caller is not identified")
	}
	r, read, err := h.Service.Result(ctx, c.Param("id"))
	if err != nil {
		if errors.Is(err, jobkit.ErrNotFound) {
			return echo.NewHTTPError(http.StatusNotFound, err.Error())
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	w := c.Response()
	w.Header().Set("Content-Type", r.ResultType)
	w.Header().Set("Content-Length", strconv.FormatInt(r.ResultSize, 10))
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.NewReplacer(`"`, "", "\\", "", "\n", "", "\r", "").Replace(r.ResultName)+`"`)
	w.WriteHeader(http.StatusOK)
	var off int64
	for {
		data, eof, err := read(off)
		if err != nil {
			return nil // the headers are sent: the client sees a short body
		}
		if _, err := w.Write(data); err != nil {
			return nil
		}
		off += int64(len(data))
		if eof || len(data) == 0 {
			return nil
		}
	}
}
