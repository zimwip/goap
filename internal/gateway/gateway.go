// Package gateway is the single entry point: authentication, CORS and
// routing of Connect calls to the services.
package gateway

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/zimwip/goap/internal/identity"
)

// Headers propagated to services. Incoming values are always overwritten.
const (
	HeaderSubject = identity.HeaderSubject
	HeaderOrg     = identity.HeaderOrg
	HeaderRoles   = identity.HeaderRoles
)

// Route maps a Connect service prefix to an upstream base URL.
type Route struct {
	Prefix   string // e.g. /goap.graph.v1.GraphService/
	Upstream string // e.g. http://graph:8080
}

// Config configures the gateway.
type Config struct {
	Routes []Route
	// AuthMode is "none" (dev: every caller is "dev") or "hs256".
	AuthMode string
	// JWTSecret signs and validates HS256 tokens.
	JWTSecret []byte
	// DevTokens enables POST /auth/dev-token (never in production).
	DevTokens bool
	// AllowOrigins for CORS.
	AllowOrigins []string
}

// Claims are the GOAP JWT claims.
type Claims struct {
	Org   string   `json:"org,omitempty"`
	Roles []string `json:"roles,omitempty"`
	jwt.RegisteredClaims
}

// Mount installs the gateway on e.
func Mount(e *echo.Echo, cfg Config) error {
	if len(cfg.AllowOrigins) > 0 {
		e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
			AllowOrigins:  cfg.AllowOrigins,
			AllowHeaders:  []string{"Authorization", "Content-Type", "Connect-Protocol-Version", "Connect-Timeout-Ms", "X-User-Agent"},
			ExposeHeaders: []string{"Grpc-Status", "Grpc-Message"},
			AllowMethods:  []string{http.MethodGet, http.MethodPost, http.MethodOptions},
		}))
	}
	// dev tokens only make sense with hs256; ignored otherwise
	if cfg.DevTokens && cfg.AuthMode == "hs256" {
		e.POST("/auth/dev-token", devToken(cfg))
	}
	auth, err := authenticator(cfg)
	if err != nil {
		return err
	}
	for _, r := range cfg.Routes {
		u, err := url.Parse(r.Upstream)
		if err != nil {
			return fmt.Errorf("route %s: %w", r.Prefix, err)
		}
		proxy := httputil.NewSingleHostReverseProxy(u)
		proxy.Transport = otelhttp.NewTransport(http.DefaultTransport)
		proxy.FlushInterval = -1 // streaming friendly
		e.Any(r.Prefix+"*", echo.WrapHandler(proxy), auth)
	}
	return nil
}

func authenticator(cfg Config) (echo.MiddlewareFunc, error) {
	switch cfg.AuthMode {
	case "", "none":
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error {
				h := c.Request().Header
				h.Set(HeaderSubject, "dev")
				h.Set(HeaderOrg, "dev")
				h.Set(HeaderRoles, "admin")
				return next(c)
			}
		}, nil
	case "hs256":
		if len(cfg.JWTSecret) < 32 {
			return nil, errors.New("hs256 auth requires a JWT secret of at least 32 bytes")
		}
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error {
				raw, ok := strings.CutPrefix(c.Request().Header.Get("Authorization"), "Bearer ")
				if !ok {
					return echo.NewHTTPError(http.StatusUnauthorized, "missing bearer token")
				}
				claims := &Claims{}
				_, err := jwt.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) { return cfg.JWTSecret, nil },
					jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithExpirationRequired())
				if err != nil {
					return echo.NewHTTPError(http.StatusUnauthorized, "invalid token")
				}
				h := c.Request().Header
				h.Del("Authorization")
				h.Set(HeaderSubject, claims.Subject)
				h.Set(HeaderOrg, claims.Org)
				h.Set(HeaderRoles, strings.Join(claims.Roles, ","))
				return next(c)
			}
		}, nil
	}
	return nil, fmt.Errorf("unknown auth mode %q", cfg.AuthMode)
}

func devToken(cfg Config) echo.HandlerFunc {
	return func(c echo.Context) error {
		var in struct {
			Subject string   `json:"subject"`
			Org     string   `json:"org"`
			Roles   []string `json:"roles"`
		}
		if err := c.Bind(&in); err != nil || in.Subject == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "subject required")
		}
		claims := Claims{Org: in.Org, Roles: in.Roles, RegisteredClaims: jwt.RegisteredClaims{
			Subject: in.Subject, Issuer: "goap-gateway", IssuedAt: jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(12 * time.Hour)),
		}}
		tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(cfg.JWTSecret)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]string{"token": tok})
	}
}
