package platform

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	echootel "github.com/labstack/echo-opentelemetry"
	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
)

// Server is an Echo server that also serves Connect handlers over HTTP/2 cleartext (h2c).
type Server struct {
	Echo  *echo.Echo
	Addr  string
	log   *slog.Logger
	ready []func(context.Context) error
}

// NewServer returns a server with health endpoints, request logging and
// panic recovery.
func NewServer(log *slog.Logger, addr string) *Server {
	e := echo.New()
	e.Use(middleware.Recover())
	e.Use(echootel.NewMiddlewareWithConfig(echootel.Config{ServerName: ServiceName(), Skipper: func(c *echo.Context) bool {
		p := c.Request().URL.Path
		return p == "/healthz" || p == "/readyz"
	}}))
	e.Use(middleware.RequestID())
	e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogURI: true, LogStatus: true, LogLatency: true, LogMethod: true,
		LogValuesFunc: func(c *echo.Context, v middleware.RequestLoggerValues) error {
			if v.URI != "/healthz" && v.URI != "/readyz" {
				log.Debug("request", "method", v.Method, "uri", v.URI, "status", v.Status, "latency", v.Latency)
			}
			return nil
		},
	}))
	s := &Server{Echo: e, Addr: addr, log: log}
	servePprof(log)
	e.GET("/healthz", func(c *echo.Context) error { return c.String(http.StatusOK, "ok") })
	e.GET("/readyz", func(c *echo.Context) error {
		for _, f := range s.ready {
			if err := f(c.Request().Context()); err != nil {
				return c.String(http.StatusServiceUnavailable, err.Error())
			}
		}
		return c.String(http.StatusOK, "ready")
	})
	return s
}

// Readiness registers a readiness check.
func (s *Server) Readiness(f func(context.Context) error) { s.ready = append(s.ready, f) }

// Mount mounts a Connect handler (path, handler) as returned by the generated
// New<Service>Handler functions.
func (s *Server) Mount(path string, h http.Handler) {
	s.Echo.Any(path+"*", echo.WrapHandler(h))
}

// httpServer serves the Echo handler over HTTP/1.1 (browsers) and HTTP/2 cleartext (Connect clients, H2CClient). The
// requests' contexts derive from one the returned function cancels: a shutdown ends the streams through it.
func (s *Server) httpServer() (*http.Server, context.CancelFunc) {
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	base, cancel := context.WithCancel(context.Background())
	return &http.Server{Addr: s.Addr, Handler: s.Echo, Protocols: &protocols, ReadHeaderTimeout: 10 * time.Second,
		BaseContext: func(net.Listener) context.Context { return base }}, cancel
}

// streamGrace is how long a shutdown lets the requests in flight end by themselves before the streams, which never do
// (the event stream, server-sent events), are told to end through their context.
const streamGrace = 2 * time.Second

// Run serves until SIGINT/SIGTERM, then shuts down gracefully.
func (s *Server) Run() error { return s.RunContext(context.Background()) }

// RunContext serves until ctx ends or SIGINT/SIGTERM, then shuts down gracefully.
func (s *Server) RunContext(ctx context.Context) error {
	srv, endStreams := s.httpServer()
	errc := make(chan error, 1)
	go func() {
		s.log.Info("listening", "addr", s.Addr)
		errc <- srv.ListenAndServe()
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-stop:
	case <-ctx.Done():
	}
	s.log.Info("shutting down")
	return shutdown(ctx, srv, endStreams)
}

// shutdown stops the server: no new connection, the requests in flight given streamGrace to end, then the streams
// ended, everything done within 15 s.
func shutdown(ctx context.Context, srv *http.Server, endStreams context.CancelFunc) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	defer endStreams()
	done := make(chan error, 1)
	go func() { done <- srv.Shutdown(ctx) }()
	select {
	case err := <-done:
		return err
	case <-time.After(streamGrace):
	}
	endStreams()
	return <-done
}

// Fatal logs and exits.
func Fatal(log *slog.Logger, msg string, err error) {
	log.Error(msg, "err", err)
	os.Exit(1)
}
