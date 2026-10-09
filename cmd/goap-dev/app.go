package main

import (
	"context"
	"log/slog"
	"sync/atomic"

	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/registrysvc"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/changeapi"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/review"
	"github.com/zimwip/goap/pkg/risk"
	"github.com/zimwip/goap/pkg/verify"
)

// defaultAuthMode is the sign-in mode when GOAP_AUTH_MODE is unset (gateway.DefaultAuthMode).
const defaultAuthMode = "local"

// app is the platform in one process: its HTTP server and what to release when it stops.
type app struct {
	Server *platform.Server
	ctx    context.Context // ends with Close: the background loops of every part follow it
	cancel context.CancelFunc
	st     stores
}

// env is what every builder shares: the context of the background loops, the configuration, the logger.
type env struct {
	ctx     context.Context
	cfg     config
	log     *slog.Logger
	secrets *platform.Secrets
	dev     *authz.Principal
	// triggers is the late binding of the trigger manager, which needs the engine, while the change and registry
	// hooks it serves are wired before.
	triggers *triggerRef
	guardian *guardianRef
}

// newApp wires the whole platform in the order the composition needs (graphsvc/boot.go doc, docs/architecture.md
// "Startup sequence"): stores, graph, registry (domains, type catalogue, Boot, methodologies), model gateway and node
// index, MCP hub, engine and triggers, then the HTTP server. Nothing listens until Server runs.
func newApp(parent context.Context, cfg config, log *slog.Logger) (*app, error) {
	// the facts of the risk register are items of a change (ADR 0065)
	risk.Register()
	verify.Register()
	review.Register()
	ctx, cancel := context.WithCancel(parent)
	dev := cfg.Dev
	e := &env{ctx: ctx, cfg: cfg, log: log, secrets: platform.NewSecrets(), dev: &dev, triggers: &triggerRef{}, guardian: &guardianRef{}}
	st, err := openStores(ctx, log)
	if err != nil {
		cancel()
		return nil, wrap("store", err)
	}
	fail := func(err error) (*app, error) {
		cancel()
		st.close()
		return nil, err
	}
	gp, err := buildGraph(e, st)
	if err != nil {
		return fail(wrap("authorizer", err))
	}
	rp, err := buildRegistry(e, gp, st)
	if err != nil {
		return fail(err)
	}
	pp, err := buildPlatform(e, st, gp, rp)
	if err != nil {
		return fail(err)
	}
	ep, err := buildEngine(e, st, gp, rp, pp)
	if err != nil {
		return fail(err)
	}
	srv, err := buildServer(e, st, gp, rp, pp, ep)
	if err != nil {
		return fail(err)
	}
	return &app{Server: srv, ctx: ctx, cancel: cancel, st: st}, nil
}

// Close stops the background loops and closes the stores.
func (a *app) Close() {
	a.cancel()
	a.st.close()
}

type wrapped struct {
	what string
	err  error
}

func (w wrapped) Error() string { return w.what + ": " + w.err.Error() }
func (w wrapped) Unwrap() error { return w.err }

func wrap(what string, err error) error { return wrapped{what, err} }

// triggerRef holds the trigger manager once the engine exists; the hooks that fire before it ignore events.
type triggerRef struct {
	m atomic.Pointer[engine.TriggerManager]
}

func (t *triggerRef) set(m *engine.TriggerManager) { t.m.Store(m) }

func (t *triggerRef) handle(ctx context.Context, ev engine.TriggerEvent) {
	if m := t.m.Load(); m != nil {
		m.Handle(ctx, ev)
	}
}

// onChange forwards a change event of the graph to the triggers.
func (t *triggerRef) onChange(ctx context.Context, ev domain.ChangeEvent) {
	t.handle(ctx, engine.TriggerEventOf(ev))
}

// changePublisher forwards the change events of the graph handler (calls from
// the IDE) to the triggers.
type changePublisher func(ctx context.Context, ev domain.ChangeEvent)

func (f changePublisher) Publish(ctx context.Context, _ string, v any) error {
	if ev, ok := v.(domain.ChangeEvent); ok {
		f(ctx, ev)
	}
	return nil
}

// guardianRef is the guardian of the changes (ADR 0098), bound late: the graph asks it from its bootstrap on, the engine
// that holds the lifecycles of the methodologies exists after the registry. Until then the rules of the registry
// answer alone.
type guardianRef struct {
	g atomic.Pointer[changeapi.Guardian]
}

func (r *guardianRef) set(g changeapi.Guardian) { r.g.Store(&g) }

func (r *guardianRef) get() changeapi.Guardian {
	if g := r.g.Load(); g != nil {
		return *g
	}
	return registrysvc.Guardian{}
}

// MayCommit implements changeapi.Guardian.
func (r *guardianRef) MayCommit(ctx context.Context, c domain.Change, bb domain.Blackboard) (bool, bool, error) {
	return r.get().MayCommit(ctx, c, bb)
}

// MayCreateChild implements changeapi.Guardian.
func (r *guardianRef) MayCreateChild(ctx context.Context, parent, child domain.Change) error {
	return r.get().MayCreateChild(ctx, parent, child)
}

// MayMove implements changeapi.Guardian.
func (r *guardianRef) MayMove(ctx context.Context, family []domain.Change, to string) error {
	return r.get().MayMove(ctx, family, to)
}

// MayEdit implements changeapi.Guardian.
func (r *guardianRef) MayEdit(ctx context.Context, c domain.Change, impact domain.ChangeImpactID) error {
	return r.get().MayEdit(ctx, c, impact)
}
