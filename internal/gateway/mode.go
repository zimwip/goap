package gateway

import (
	"errors"
	"fmt"
	"sort"

	"github.com/labstack/echo/v5"

	"github.com/zimwip/goap/pkg/authz"
)

// Mode is an authentication strategy (ADR 0068): how a caller proves who it is, which endpoints it adds to the
// gateway, and what it needs. The gateway holds one, found by the name of Config.AuthMode, and has no other
// knowledge of the names: a new way of signing in is a new Mode registered by name (RegisterMode).
type Mode interface {
	// Name is what Config.AuthMode, GOAP_AUTH_MODE and /api/auth/config call the mode.
	Name() string
	// Open reports a mode that checks no credentials (every caller is the fixed development principal): a
	// deployment with its own fixed identity (cmd/goap-dev) needs no gateway in front of its services.
	Open() bool
	// VerifiesTokens reports a mode whose callers carry a bearer token signed with Config.JWTSecret.
	VerifiesTokens() bool
	// Sessions reports a mode that signs its users in itself (register / login) and keeps their sign-in
	// server-side, in Config.Credentials: its tokens are refused once their session ended (ADR 0045).
	Sessions() bool
	// Validate checks that cfg holds what the mode needs (a secret, Credentials), at start.
	Validate(cfg Config) error
	// Authenticate turns the credentials of a request into the caller.
	Authenticate(c *echo.Context, cfg Config) (authz.Principal, error)
	// Routes adds the endpoints of the mode (sign-in, token reissue) to e.
	Routes(e *echo.Echo, cfg Config)
}

var modes = map[string]Mode{}

// RegisterMode makes a mode available under its name; a second mode of the same name is a programming error.
func RegisterMode(m Mode) {
	if _, dup := modes[m.Name()]; dup {
		panic("gateway: auth mode " + m.Name() + " registered twice")
	}
	modes[m.Name()] = m
}

// LookupMode returns the mode named name. An unset (empty) name is an error, never an open gateway.
func LookupMode(name string) (Mode, error) {
	if m, ok := modes[name]; ok {
		return m, nil
	}
	known := make([]string, 0, len(modes))
	for n := range modes {
		known = append(known, n)
	}
	sort.Strings(known)
	return nil, fmt.Errorf("unknown auth mode %q (one of %v)", name, known)
}

func init() {
	RegisterMode(noneMode{})
	RegisterMode(bearerMode{})
	RegisterMode(localMode{})
}

// noneMode (explicit only): every caller is "dev", an administrator.
type noneMode struct{}

func (noneMode) Name() string         { return "none" }
func (noneMode) Open() bool           { return true }
func (noneMode) VerifiesTokens() bool { return false }
func (noneMode) Sessions() bool       { return false }
func (noneMode) Validate(Config) error {
	return nil
}
func (noneMode) Authenticate(*echo.Context, Config) (authz.Principal, error) {
	return authz.Principal{Subject: "dev", Org: "dev", Roles: []string{"admin"}}, nil
}
func (noneMode) Routes(*echo.Echo, Config) {}

// bearerMode ("hs256"): bearer tokens minted out of band; POST /auth/dev-token mints one when DevTokens is set.
type bearerMode struct{}

func (bearerMode) Name() string         { return "hs256" }
func (bearerMode) Open() bool           { return false }
func (bearerMode) VerifiesTokens() bool { return true }
func (bearerMode) Sessions() bool       { return false }
func (m bearerMode) Validate(cfg Config) error {
	return requireSecret(m, cfg)
}
func (bearerMode) Authenticate(c *echo.Context, cfg Config) (authz.Principal, error) {
	return authenticateToken(c, cfg)
}
func (bearerMode) Routes(e *echo.Echo, cfg Config) {
	if cfg.DevTokens { // never in production
		e.POST("/auth/dev-token", devToken(cfg))
		e.POST("/auth/dev-token/project", switchProject(cfg))
	}
}

// localMode ("local", ADR 0040): signup / login against Credentials, for a deployment with no external identity
// provider. It verifies bearer tokens the way hs256 does: only how a token is first obtained differs, and the
// session it belongs to is checked (ADR 0045).
type localMode struct{ bearerMode }

func (localMode) Name() string   { return "local" }
func (localMode) Sessions() bool { return true }
func (m localMode) Validate(cfg Config) error {
	if err := requireSecret(m, cfg); err != nil {
		return err
	}
	if cfg.Credentials == nil {
		return errors.New("local auth requires Credentials")
	}
	return nil
}
func (localMode) Routes(e *echo.Echo, cfg Config) {
	e.POST("/auth/register", register(cfg))
	e.POST("/auth/login", login(cfg))
	e.POST("/auth/logout", logout(cfg))
	e.POST("/auth/refresh", refresh(cfg))
	// switching project reissues the token (ADR 0039): a signed-in user needs it as much as a dev token
	e.POST("/auth/dev-token/project", switchProject(cfg))
}

func requireSecret(m Mode, cfg Config) error {
	if len(cfg.JWTSecret) < 32 {
		return fmt.Errorf("%s auth requires a JWT secret of at least 32 bytes", m.Name())
	}
	return nil
}

// authenticateToken verifies the bearer token of a request (and its session, when the mode keeps them).
func authenticateToken(c *echo.Context, cfg Config) (authz.Principal, error) {
	claims, err := parseToken(cfg, c.Request().Header.Get("Authorization"))
	if err != nil {
		return authz.Principal{}, unauthorized(err)
	}
	if err := checkSession(c.Request().Context(), cfg, claims, false); err != nil {
		return authz.Principal{}, err
	}
	c.Request().Header.Del("Authorization")
	return authz.Principal{Subject: claims.Subject, Org: claims.Org, Project: claims.Project, Roles: claims.Roles}, nil
}
