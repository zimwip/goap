// Package gateway is the single entry point: authentication, CORS and
// routing of Connect calls to the services.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/pkg/authz"
)

// Headers propagated to services. Incoming values are always overwritten.
const (
	HeaderSubject = identity.HeaderSubject
	HeaderOrg     = identity.HeaderOrg
	HeaderRoles   = identity.HeaderRoles
	HeaderProject = identity.HeaderProject
)

// Route maps a Connect service prefix to an upstream base URL.
type Route struct {
	Prefix   string // e.g. /goap.graph.v1.GraphService/
	Upstream string // e.g. http://graph:8080
}

// Config configures the gateway.
type Config struct {
	Routes []Route
	// AuthMode is "none" (dev: every caller is "dev"), "hs256" (bearer tokens minted out of band, dev-token
	// only) or "local" (ADR 0040: signup/login against Credentials, backed by internal/credsvc, for a
	// deployment with no external identity provider). "local" verifies bearer tokens the same way "hs256"
	// does: only how a token is first obtained differs. It is the default (DefaultAuthMode, ADR 0042): a
	// deployment signs its users in itself unless an external identity provider issues their tokens.
	AuthMode string
	// JWTSecret signs and validates HS256 tokens.
	JWTSecret []byte
	// DevTokens enables POST /auth/dev-token (never in production).
	DevTokens bool
	// Credentials backs the local AuthMode's register/login endpoints (ADR 0040). Required when
	// AuthMode == "local".
	Credentials Credentials
	// OnSignIn runs after a successful local register/login, before the token is signed (ADR 0042): it
	// creates the subject's User node when the graph has none yet (graphsvc.EnsureUser, member of the unit
	// new users join), so a user exists from the moment they sign in rather than from their first call.
	// A failure refuses the sign-in. Nil skips it (the node is then created on the first call, EnsureCaller).
	OnSignIn func(ctx context.Context, subject string) error
	// AllowOrigins for CORS.
	AllowOrigins []string
	// Enrich completes the authenticated principal with what the graph knows of its subject (roles and unit
	// of its User node). Nil leaves the principal as the token gives it.
	Enrich func(ctx context.Context, p authz.Principal) authz.Principal
}

// DefaultAuthMode is the AuthMode of a deployment that names none (GOAP_AUTH_MODE unset, ADR 0042): local
// sign-in, since no external identity provider (SSO) is configured. "none" (no sign-in at all) is for local
// development only and must be asked for explicitly.
const DefaultAuthMode = "local"

// Credentials is what the local AuthMode needs of the credentials service (internal/credsvc, ADR 0040).
type Credentials interface {
	Register(ctx context.Context, subject, password string) error
	Verify(ctx context.Context, subject, password string) (bool, error)
}

// Claims are the GOAP JWT claims.
type Claims struct {
	Org string `json:"org,omitempty"`
	// Project is the caller's active project (ADR 0039): re-issued by /auth/dev-token/project each time
	// the user switches, so every call carries it without the caller having to pass it explicitly.
	Project string   `json:"project,omitempty"`
	Roles   []string `json:"roles,omitempty"`
	jwt.RegisteredClaims
}

// MountAuthEndpoints registers the sign-in HTTP endpoints alone (no CORS, no proxying, no /api/whoami or
// /api/status): the dev-token endpoints (hs256, DevTokens), local register/login/logout (AuthMode "local")
// and /api/auth/config (always). Split out of Mount so a single-process deployment (cmd/goap-dev) can offer
// the same sign-in UI as the distributed gateway without also wanting a reverse proxy.
func MountAuthEndpoints(e *echo.Echo, cfg Config) error {
	// dev tokens only make sense with hs256; ignored otherwise
	if cfg.DevTokens && cfg.AuthMode == "hs256" {
		e.POST("/auth/dev-token", devToken(cfg))
		e.POST("/auth/dev-token/project", switchProject(cfg))
	}
	if cfg.AuthMode == "local" {
		if len(cfg.JWTSecret) < 32 {
			return errors.New("local auth requires a JWT secret of at least 32 bytes")
		}
		if cfg.Credentials == nil {
			return errors.New("local auth requires Credentials")
		}
		e.POST("/auth/register", register(cfg))
		e.POST("/auth/login", login(cfg))
		e.POST("/auth/logout", logout())
		// switching project reissues the token (ADR 0039): a signed-in user needs it as much as a dev token
		e.POST("/auth/dev-token/project", switchProject(cfg))
	}
	e.GET("/api/auth/config", authConfig(cfg))
	return nil
}

// Authenticator returns the middleware that turns a request's credentials (a bearer token, or nothing in
// AuthMode "none") into the propagated identity headers (setPrincipal): exported so a single-process
// deployment (cmd/goap-dev) can apply it globally instead of per-route.
func Authenticator(cfg Config) (echo.MiddlewareFunc, error) { return authenticator(cfg) }

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
	if err := MountAuthEndpoints(e, cfg); err != nil {
		return err
	}
	auth, err := authenticator(cfg)
	if err != nil {
		return err
	}
	e.GET("/api/status", statusHandler(cfg.Routes))
	e.GET("/api/whoami", identity.WhoAmI(identity.Extractor{}, nil), auth)
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
			return func(c *echo.Context) error {
				setPrincipal(c, cfg, authz.Principal{Subject: "dev", Org: "dev", Roles: []string{"admin"}})
				return next(c)
			}
		}, nil
	case "hs256", "local":
		if len(cfg.JWTSecret) < 32 {
			return nil, fmt.Errorf("%s auth requires a JWT secret of at least 32 bytes", cfg.AuthMode)
		}
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c *echo.Context) error {
				claims, err := parseToken(cfg, c.Request().Header.Get("Authorization"))
				if err != nil {
					return echo.NewHTTPError(http.StatusUnauthorized, "invalid token")
				}
				c.Request().Header.Del("Authorization")
				setPrincipal(c, cfg, authz.Principal{Subject: claims.Subject, Org: claims.Org, Project: claims.Project, Roles: claims.Roles})
				return next(c)
			}
		}, nil
	}
	return nil, fmt.Errorf("unknown auth mode %q", cfg.AuthMode)
}

// setPrincipal propagates the caller to the services, completed by its User node when there is one.
func setPrincipal(c *echo.Context, cfg Config, p authz.Principal) {
	if cfg.Enrich != nil {
		p = cfg.Enrich(c.Request().Context(), p)
	}
	h := c.Request().Header
	h.Set(HeaderSubject, p.Subject)
	h.Set(HeaderOrg, p.Org)
	h.Set(HeaderProject, p.Project)
	h.Set(HeaderRoles, strings.Join(p.Roles, ","))
}

// parseToken validates a "Bearer <token>" Authorization header value and returns its claims.
func parseToken(cfg Config, authorization string) (*Claims, error) {
	raw, ok := strings.CutPrefix(authorization, "Bearer ")
	if !ok {
		return nil, errors.New("missing bearer token")
	}
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) { return cfg.JWTSecret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithExpirationRequired())
	return claims, err
}

func sign(cfg Config, subject, org, project string, roles []string) (string, error) {
	claims := Claims{Org: org, Project: project, Roles: roles, RegisteredClaims: jwt.RegisteredClaims{
		Subject: subject, Issuer: "goap-gateway", IssuedAt: jwt.NewNumericDate(time.Now()),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(12 * time.Hour)),
	}}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(cfg.JWTSecret)
}

func devToken(cfg Config) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var in struct {
			Subject string   `json:"subject"`
			Org     string   `json:"org"`
			Project string   `json:"project"`
			Roles   []string `json:"roles"`
		}
		if err := c.Bind(&in); err != nil || in.Subject == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "subject required")
		}
		tok, err := sign(cfg, in.Subject, in.Org, in.Project, in.Roles)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]string{"token": tok})
	}
}

// authConfig tells the web which sign-in UI to show: unauthenticated, so it can be called before any token
// exists (ADR 0040 — the signin/signup screens, and the Logout action's own SSO seam, both read it).
func authConfig(cfg Config) echo.HandlerFunc {
	return func(c *echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"authMode": cfg.AuthMode})
	}
}

// register creates the credential of a new subject and signs them straight in (ADR 0040): the returned
// token carries no org/roles of its own, filled in by Enrich from the subject's User node — created, with
// the org-membership/first-admin bootstrap, by OnSignIn (ADR 0042), or else the moment this token is first
// used (EnsureCaller).
func register(cfg Config) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var in struct{ Subject, Password string }
		if err := c.Bind(&in); err != nil || in.Subject == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "subject required")
		}
		if err := cfg.Credentials.Register(c.Request().Context(), in.Subject, in.Password); err != nil {
			status := http.StatusBadRequest
			if connect.CodeOf(err) == connect.CodeAlreadyExists {
				status = http.StatusConflict
			}
			return echo.NewHTTPError(status, err.Error())
		}
		if err := signedIn(c, cfg, in.Subject); err != nil {
			return err
		}
		tok, err := sign(cfg, in.Subject, "", "", nil)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]string{"token": tok})
	}
}

// login verifies a subject's password and signs a token the same shape register's is.
func login(cfg Config) echo.HandlerFunc {
	return func(c *echo.Context) error {
		var in struct{ Subject, Password string }
		if err := c.Bind(&in); err != nil || in.Subject == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "subject required")
		}
		ok, err := cfg.Credentials.Verify(c.Request().Context(), in.Subject, in.Password)
		if err != nil {
			return err
		}
		if !ok {
			return echo.NewHTTPError(http.StatusUnauthorized, "wrong subject or password")
		}
		if err := signedIn(c, cfg, in.Subject); err != nil {
			return err
		}
		tok, err := sign(cfg, in.Subject, "", "", nil)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]string{"token": tok})
	}
}

// signedIn runs OnSignIn for a subject whose credentials were just accepted: the user is declared in the
// graph (created, member of the unit new users join) before they get a token.
func signedIn(c *echo.Context, cfg Config, subject string) error {
	if cfg.OnSignIn == nil {
		return nil
	}
	if err := cfg.OnSignIn(c.Request().Context(), subject); err != nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "cannot declare the user: "+err.Error())
	}
	return nil
}

// logout has nothing to revoke (HS256 tokens are stateless, ADR 0040's known limitation): it exists so the
// web has one endpoint to call, and so a real revocation list is a change to this function alone, later.
func logout() echo.HandlerFunc {
	return func(c *echo.Context) error { return c.NoContent(http.StatusNoContent) }
}

// switchProject reissues the caller's token with a new active project (ADR 0039: "a token regenerated each
// time the user changes project"), keeping its subject, org and roles: the identity a caller already
// proved, unchanged, just pointed at a different project from here on.
func switchProject(cfg Config) echo.HandlerFunc {
	return func(c *echo.Context) error {
		claims, err := parseToken(cfg, c.Request().Header.Get("Authorization"))
		if err != nil {
			return echo.NewHTTPError(http.StatusUnauthorized, "invalid token")
		}
		var in struct {
			Project string `json:"project"`
		}
		if err := c.Bind(&in); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "project required")
		}
		tok, err := sign(cfg, claims.Subject, claims.Org, in.Project, claims.Roles)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]string{"token": tok})
	}
}
