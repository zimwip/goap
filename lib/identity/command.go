package identity

import (
	"context"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/lib/platform"
)

// CommandInterceptor stamps the events published while a request is served with the id its client gave it
// (X-Goap-Command) and the caller, so a client recognises the echo of its own writes (ADR 0053). Wrap the services
// that write.
func CommandInterceptor() connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if c := req.Header().Get(platform.HeaderCommand); c != "" {
				ctx = platform.WithStamp(ctx, platform.Stamp{Actor: FromHeaders(req.Header()).Subject, Command: c})
			}
			return next(ctx, req)
		}
	})
}
