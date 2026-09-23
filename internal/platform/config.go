// Package platform holds the plumbing shared by every service: configuration,
// logging, HTTP/Connect server, PostgreSQL, NATS and Vault access.
package platform

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// Env returns the environment variable or a default.
func Env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

// EnvInt returns an integer environment variable or a default.
func EnvInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return def
}

// EnvDuration returns a duration environment variable or a default.
func EnvDuration(key string, def time.Duration) time.Duration {
	if v, err := time.ParseDuration(os.Getenv(key)); err == nil {
		return v
	}
	return def
}

var serviceName = "goap"

// ServiceName returns the name given to Logger.
func ServiceName() string { return serviceName }

// Logger returns the service logger (JSON unless GOAP_LOG_FORMAT=text).
func Logger(service string) *slog.Logger {
	level := slog.LevelInfo
	if strings.EqualFold(os.Getenv("GOAP_LOG_LEVEL"), "debug") {
		level = slog.LevelDebug
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler = slog.NewJSONHandler(os.Stdout, opts)
	if os.Getenv("GOAP_LOG_FORMAT") == "text" {
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	serviceName = service
	l := slog.New(h).With("service", service)
	slog.SetDefault(l)
	return l
}
