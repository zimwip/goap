package bench

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path"
	"time"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/contracts/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/lib/identity"
)

// Clients are the APIs the bench calls: the graph service and the domain registry (served by the graph service in the
// distributed stack, by goap-local without docker).
type Clients struct {
	Graph   graphv1connect.GraphServiceClient
	Domains registryv1connect.DomainServiceClient
}

// NewClients returns clients acting as the given principal (identity headers, the platform's internal trust: the bench
// talks to the services behind the gateway) or with a bearer token (through the gateway). Every answer's size is
// recorded on m under the name of its RPC.
func NewClients(graphURL, domainURL string, p authz.Principal, token string, m *Metrics) Clients {
	hc := &http.Client{Timeout: 10 * time.Minute, Transport: &transport{next: http.DefaultTransport, principal: p, token: token, m: m}}
	return Clients{
		Graph:   graphv1connect.NewGraphServiceClient(hc, graphURL),
		Domains: registryv1connect.NewDomainServiceClient(hc, domainURL),
	}
}

type transport struct {
	next      http.RoundTripper
	principal authz.Principal
	token     string
	m         *Metrics
}

func (t *transport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if t.token != "" {
		r.Header.Set("Authorization", "Bearer "+t.token)
	} else {
		identity.SetHeaders(t.principal, r.Header)
	}
	res, err := t.next.RoundTrip(r)
	if err == nil && t.m != nil {
		op, _ := r.Context().Value(opKey{}).(string)
		if op == "" {
			op = path.Base(r.URL.Path)
		}
		res.Body = &counted{ReadCloser: res.Body, ctx: r.Context(), op: op, m: t.m}
	}
	return res, err
}

// counted records the size of a body once read or closed.
type counted struct {
	io.ReadCloser
	ctx  context.Context
	op   string
	m    *Metrics
	n    int64
	done bool
}

func (c *counted) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	c.n += int64(n)
	if err == io.EOF {
		c.record()
	}
	return n, err
}

func (c *counted) Close() error {
	c.record()
	return c.ReadCloser.Close()
}

func (c *counted) record() {
	if !c.done {
		c.done = true
		c.m.Size(c.ctx, c.op, c.n)
	}
}

// outcome classifies the error of a call.
func outcome(err error) string {
	switch {
	case err == nil:
		return OutcomeOK
	case isConflict(err):
		return OutcomeConflict
	default:
		return OutcomeError
	}
}

// isConflict is a call refused because the graph moved meanwhile (graph.ErrConflict: Aborted or FailedPrecondition).
func isConflict(err error) bool {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return false
	}
	return ce.Code() == connect.CodeAborted || ce.Code() == connect.CodeFailedPrecondition
}

// opKey carries the name of the operation of a call to the transport, which records the size of its answer under it.
type opKey struct{}

// timed runs one call and records it under op; a call cut by the end of the run is not recorded.
// shaped is timed for a call whose cost follows the links it handles: also recorded by that size (Metrics.Shaped).
func shaped[T any](ctx context.Context, m *Metrics, op string, links int, call func(ctx context.Context) (T, error)) (T, error) {
	start := time.Now()
	out, err := timed(ctx, m, op, call)
	if err == nil {
		m.Shaped(ctx, op, time.Since(start), links)
	}
	return out, err
}

func timed[T any](ctx context.Context, m *Metrics, op string, call func(ctx context.Context) (T, error)) (T, error) {
	start := time.Now()
	out, err := call(context.WithValue(ctx, opKey{}, op))
	if err == nil || ctx.Err() == nil {
		m.Observe(ctx, op, time.Since(start), outcome(err))
	}
	return out, err
}
