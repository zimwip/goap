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
	_, _ = timing()(next)(context.Background(), connect.NewRequest(&struct{}{}))
	out := buf.String()
	for _, want := range []string{"msg=rpc", "duration_ms=", "code=not_found", "level=DEBUG"} {
		if !strings.Contains(out, want) {
			t.Fatalf("%q missing from %q", want, out)
		}
	}
	buf.Reset()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	_, _ = timing()(next)(context.Background(), connect.NewRequest(&struct{}{}))
	if buf.Len() != 0 {
		t.Fatalf("logged at info: %q", buf.String())
	}
}
