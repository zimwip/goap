package sandbox

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"connectrpc.com/connect"

	runtimev1 "github.com/zimwip/goap/gen/goap/runtime/v1"
	"github.com/zimwip/goap/gen/goap/runtime/v1/runtimev1connect"
	"github.com/zimwip/goap/pkg/dsl"
)

// Runner is the SandboxService served by goap-runner inside a sandbox.
type Runner struct {
	// HTTP is used to call back the engine RuntimeService.
	HTTP *http.Client
	// Options are applied to the RuntimeService client (tracing interceptors).
	Options []connect.ClientOption
}

var _ runtimev1connect.SandboxServiceHandler = (*Runner)(nil)

// Execute implements runtimev1connect.SandboxServiceHandler.
func (r *Runner) Execute(ctx context.Context, req *connect.Request[runtimev1.ExecuteRequest]) (*connect.Response[runtimev1.ExecuteResponse], error) {
	m := req.Msg
	job := dsl.Job{Language: m.Language, Code: m.Code, ProcessID: m.ProcessId, Agent: m.Agent, Action: m.Action,
		Timeout: time.Duration(m.TimeoutMs) * time.Millisecond}
	var snapshot struct {
		Items  []dsl.Item `json:"items"`
		Intent string     `json:"intent"`
		Goal   string     `json:"goal"`
	}
	if err := json.Unmarshal([]byte(m.BlackboardJson), &snapshot); err != nil && m.BlackboardJson != "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	job.Items, job.Intent, job.Goal = snapshot.Items, snapshot.Intent, snapshot.Goal
	_ = json.Unmarshal([]byte(m.ParamsJson), &job.Params)
	_ = json.Unmarshal([]byte(m.VarsJson), &job.Vars)
	hc := r.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	host := RemoteHost{Client: runtimev1connect.NewRuntimeServiceClient(hc, m.RuntimeUrl, r.Options...), JobID: m.JobId, Token: m.Token}
	res, err := dsl.Run(ctx, job, host)
	out := &runtimev1.ExecuteResponse{Output: res.Output, Suspended: res.Suspended}
	for _, l := range res.Logs {
		out.Logs = append(out.Logs, &runtimev1.LogLine{Time: timestamp(l.Time), Level: l.Level, Message: l.Message})
	}
	if err != nil {
		out.Error = err.Error()
	} else {
		b, _ := json.Marshal(res.Items)
		out.ItemsJson = string(b)
	}
	return connect.NewResponse(out), nil
}
