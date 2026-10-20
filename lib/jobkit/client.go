package jobkit

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/contracts/authz"
	jobsv1 "github.com/zimwip/goap/contracts/gen/goap/jobs/v1"
	"github.com/zimwip/goap/contracts/gen/goap/jobs/v1/jobsv1connect"
	"github.com/zimwip/goap/lib/pbconv"
)

// Client is the Queue of a job service reached over RPC, with the services' token.
type Client struct {
	RPC jobsv1connect.JobServiceClient
}

var _ Queue = Client{}

// NewClient returns the client of the job service at url, presenting token on every call.
func NewClient(hc connect.HTTPClient, url, token string, opts ...connect.ClientOption) Client {
	return Client{RPC: jobsv1connect.NewJobServiceClient(hc, url, append(opts, connect.WithInterceptors(TokenInterceptor(token)))...)}
}

// TokenInterceptor sets the services' token on the calls of a client.
func TokenInterceptor(token string) connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if token != "" {
				req.Header().Set(TokenHeader, token)
			}
			return next(ctx, req)
		}
	})
}

// fromRPC maps the codes of the service back to the errors of the kit.
func fromRPC(err error) error {
	switch connect.CodeOf(err) {
	case connect.CodeNotFound:
		return errors.Join(ErrNotFound, err)
	case connect.CodeInvalidArgument:
		return errors.Join(ErrInvalid, err)
	case connect.CodePermissionDenied:
		return errors.Join(ErrForbidden, err)
	case connect.CodeFailedPrecondition:
		return errors.Join(ErrLost, err)
	}
	return err
}

// EncodePrincipal is the JSON form of a principal the job service keeps ("" for none).
func EncodePrincipal(p authz.Principal) string {
	if p.Anonymous() {
		return ""
	}
	b, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	return string(b)
}

// DecodePrincipal reads EncodePrincipal back (anonymous when empty or unreadable).
func DecodePrincipal(s string) authz.Principal {
	var p authz.Principal
	if s != "" {
		_ = json.Unmarshal([]byte(s), &p)
	}
	return p
}

// Create implements Queue.
func (c Client) Create(ctx context.Context, s Spec) (*jobsv1.Job, error) {
	r, err := c.RPC.CreateJob(ctx, connect.NewRequest(&jobsv1.CreateJobRequest{Kind: s.Kind, Label: s.Label, Target: s.Target,
		Params: pbconv.Struct(s.Params), ProjectId: s.ProjectID, OwnerOrg: s.OwnerOrg, By: s.By, Principal: EncodePrincipal(s.Principal), Input: s.Input}))
	if err != nil {
		return nil, fromRPC(err)
	}
	return r.Msg.Job, nil
}

// Workers implements Queue.
func (c Client) Workers(ctx context.Context) ([]*jobsv1.Worker, error) {
	r, err := c.RPC.ListWorkers(ctx, connect.NewRequest(&jobsv1.ListWorkersRequest{}))
	if err != nil {
		return nil, fromRPC(err)
	}
	return r.Msg.Workers, nil
}

// Claim implements Queue.
func (c Client) Claim(ctx context.Context, worker string, kinds []string, wait time.Duration) (*Claim, error) {
	r, err := c.RPC.ClaimJob(ctx, connect.NewRequest(&jobsv1.ClaimJobRequest{Worker: worker, Kinds: kinds, WaitMs: int32(wait / time.Millisecond)}))
	if err != nil {
		return nil, fromRPC(err)
	}
	if r.Msg.Job == nil {
		return nil, nil
	}
	return &Claim{Job: r.Msg.Job, Principal: DecodePrincipal(r.Msg.Principal), Lease: time.Duration(r.Msg.LeaseSeconds) * time.Second}, nil
}

// Report implements Queue.
func (c Client) Report(ctx context.Context, id, worker string, rep Report) (bool, error) {
	r, err := c.RPC.ReportJob(ctx, connect.NewRequest(&jobsv1.ReportJobRequest{Id: id, Worker: worker, Progress: rep.Progress, Done: rep.Done,
		Total: rep.Total, Stats: rep.Stats, Output: pbconv.Struct(rep.Output), Status: rep.Status, Error: rep.Error}))
	if err != nil {
		return false, fromRPC(err)
	}
	return r.Msg.Cancel, nil
}

// ReadInput implements Queue.
func (c Client) ReadInput(ctx context.Context, id, worker string, offset int64) ([]byte, bool, error) {
	r, err := c.RPC.ReadJobInput(ctx, connect.NewRequest(&jobsv1.ReadJobInputRequest{Id: id, Worker: worker, Offset: offset}))
	if err != nil {
		return nil, false, fromRPC(err)
	}
	return r.Msg.Data, r.Msg.Eof, nil
}

// WriteResult implements Queue.
func (c Client) WriteResult(ctx context.Context, id, worker, name, mediaType string, data []byte) (int64, error) {
	r, err := c.RPC.WriteJobResult(ctx, connect.NewRequest(&jobsv1.WriteJobResultRequest{Id: id, Worker: worker, Name: name, MediaType: mediaType, Data: data}))
	if err != nil {
		return 0, fromRPC(err)
	}
	return r.Msg.Size, nil
}
