package platform

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"go.opentelemetry.io/contrib/instrumentation/github.com/labstack/echo/otelecho"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// Server is an Echo server that also serves Connect handlers over h2c.
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
	e.HideBanner = true
	e.HidePort = true
	e.Use(middleware.Recover())
	e.Use(otelecho.Middleware(ServiceName(), otelecho.WithSkipper(func(c echo.Context) bool {
		p := c.Request().URL.Path
		return p == "/healthz" || p == "/readyz"
	})))
	e.Use(middleware.RequestID())
	e.Use(middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		LogURI: true, LogStatus: true, LogLatency: true, LogMethod: true,
		LogValuesFunc: func(c echo.Context, v middleware.RequestLoggerValues) error {
			if v.URI != "/healthz" && v.URI != "/readyz" {
				log.Debug("request", "method", v.Method, "uri", v.URI, "status", v.Status, "latency", v.Latency)
			}
			return nil
		},
	}))
	s := &Server{Echo: e, Addr: addr, log: log}
	e.GET("/healthz", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })
	e.GET("/readyz", func(c echo.Context) error {
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

// Run serves until SIGINT/SIGTERM, then shuts down gracefully.
func (s *Server) Run() error {
	srv := &http.Server{Addr: s.Addr, Handler: h2c.NewHandler(s.Echo, &http2.Server{}), ReadHeaderTimeout: 10 * time.Second}
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
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s.log.Info("shutting down")
	return srv.Shutdown(ctx)
}

// Fatal logs and exits.
func Fatal(log *slog.Logger, msg string, err error) {
	log.Error(msg, "err", err)
	os.Exit(1)
}
