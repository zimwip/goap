package telemetry

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"connectrpc.com/connect"
)

// A unary call is logged with its procedure, duration and code at debug level, nothing at info.
func TestTimingLogsEveryUnaryCallAtDebug(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	next := connect.UnaryFunc(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no"))
	})
	_, _ = timing(nil)(next)(context.Background(), connect.NewRequest(&struct{}{}))
	out := buf.String()
	for _, want := range []string{"msg=rpc", "duration_ms=", "code=not_found", "level=DEBUG"} {
		if !strings.Contains(out, want) {
			t.Fatalf("%q missing from %q", want, out)
		}
	}
	buf.Reset()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	_, _ = timing(nil)(next)(context.Background(), connect.NewRequest(&struct{}{}))
	if buf.Len() != 0 {
		t.Fatalf("logged at info: %q", buf.String())
	}
}

// specRequest is a request of a named procedure (connect sets the spec of a request only when serving it).
type specRequest struct {
	*connect.Request[struct{}]
	procedure string
}

func (r specRequest) Spec() connect.Spec { return connect.Spec{Procedure: r.procedure} }

// A slow call is logged at warn level, unless its procedure waits by design (a long poll).
func TestTimingWarnsOfSlowCallsButLongPolls(t *testing.T) {
	prev, prevSlow := slog.Default(), slowRPC
	t.Cleanup(func() { slog.SetDefault(prev); slowRPC = prevSlow })
	slowRPC = 0
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	next := connect.UnaryFunc(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) { return nil, nil })
	longPolls := map[string]bool{}
	LongPoll("/svc/Claim")(longPolls)
	_, _ = timing(longPolls)(next)(context.Background(), specRequest{connect.NewRequest(&struct{}{}), "/svc/Read"})
	if out := buf.String(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, "procedure=/svc/Read") {
		t.Fatalf("a slow call not warned of: %q", out)
	}
	buf.Reset()
	_, _ = timing(longPolls)(next)(context.Background(), specRequest{connect.NewRequest(&struct{}{}), "/svc/Claim"})
	if buf.Len() != 0 {
		t.Fatalf("a long poll logged at info: %q", buf.String())
	}
}
