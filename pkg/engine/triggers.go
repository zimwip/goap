package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/condition"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/methodology"
)

// TriggerEvent is an event that may start agents.
type TriggerEvent struct {
	Type        string            `json:"type"`
	Change      *domain.ChangeSet `json:"change,omitempty"`
	Process     *Process          `json:"process,omitempty"`
	Methodology string            `json:"methodology,omitempty"`
	Version     string            `json:"version,omitempty"`
}

func (ev TriggerEvent) activation() map[string]any {
	out := map[string]any{"type": ev.Type, "change": map[string]any{}, "process": map[string]any{}, "methodology": map[string]any{}}
	if c := ev.Change; c != nil {
		data := c.Data
		if data == nil {
			data = map[string]any{}
		}
		out["change"] = map[string]any{"id": string(c.ID), "title": c.Title, "intent": c.Intent, "status": string(c.Status),
			"methodology": c.Methodology, "goal": c.Goal, "baseline": string(c.BaselineID), "items": int64(len(c.Items)), "data": data}
	}
	if p := ev.Process; p != nil {
		out["process"] = map[string]any{"id": p.ID, "agent": p.Agent, "goal": p.Goal, "status": string(p.Status),
			"methodology": p.Methodology, "trigger": p.Trigger, "change": string(p.ChangeID), "parent": p.ParentID}
	}
	if ev.Methodology != "" {
		out["methodology"] = map[string]any{"name": ev.Methodology, "version": ev.Version}
	}
	return out
}

// TriggerState is the runtime state of a trigger.
type TriggerState struct {
	Methodology   string    `json:"methodology"`
	Agent         string    `json:"agent"`
	Name          string    `json:"name"`
	Description   string    `json:"description,omitempty"`
	Type          string    `json:"type"`
	Event         string    `json:"event,omitempty"`
	Schedule      string    `json:"schedule,omitempty"`
	Enabled       bool      `json:"enabled"`
	Fires         int       `json:"fires"`
	LastFired     time.Time `json:"lastFired"`
	NextFire      time.Time `json:"nextFire"`
	LastProcessID string    `json:"lastProcessId,omitempty"`
	LastError     string    `json:"lastError,omitempty"`
}

// Key identifies a trigger: "<methodology>/<agent>/<trigger>".
func (s TriggerState) Key() string { return s.Methodology + "/" + s.Agent + "/" + s.Name }

type triggerEntry struct {
	state  TriggerState
	def    methodology.Trigger
	filter *condition.EventFilter
	cronID cron.EntryID
}

// TriggerManager runs agents automatically on events and schedules. Runs
// use a service identity "system:trigger:<key>" with the roles declared by
// the trigger; changes they open are marked so that a trigger never fires
// on its own output (loop protection), and a trigger fires at most once per
// MinInterval.
//
// With several engine replicas, only one must run the manager (leader
// election: milestone M1).
type TriggerManager struct {
	Engine *Engine
	Log    *slog.Logger
	// MinInterval between two fires of the same trigger (default 2s).
	MinInterval time.Duration

	mu      sync.Mutex
	entries map[string]*triggerEntry
	cron    *cron.Cron
	now     func() time.Time
}

var errUnknownTrigger = errors.New("unknown trigger")

func (t *TriggerManager) clock() time.Time {
	if t.now != nil {
		return t.now()
	}
	return time.Now().UTC()
}

func (t *TriggerManager) log() *slog.Logger {
	if t.Log != nil {
		return t.Log
	}
	return slog.Default()
}

// Start loads the triggers, starts the scheduler and reloads periodically.
func (t *TriggerManager) Start(ctx context.Context) {
	t.mu.Lock()
	if t.cron == nil {
		t.cron = cron.New(cron.WithLocation(time.UTC))
		t.cron.Start()
	}
	t.mu.Unlock()
	if err := t.Reload(ctx); err != nil {
		t.log().Warn("triggers", "err", err)
	}
	go func() {
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-tick.C:
				if err := t.Reload(ctx); err != nil {
					t.log().Warn("triggers reload", "err", err)
				}
			}
		}
	}()
}

// Stop stops the scheduler.
func (t *TriggerManager) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cron != nil {
		t.cron.Stop()
	}
}

// Reload rebuilds the triggers from the published methodologies, keeping the
// counters of unchanged triggers.
func (t *TriggerManager) Reload(ctx context.Context) error {
	ms, err := t.Engine.Methodologies.List(ctx)
	if err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	old := t.entries
	t.entries = map[string]*triggerEntry{}
	for _, e := range old {
		if e.cronID != 0 && t.cron != nil {
			t.cron.Remove(e.cronID)
		}
	}
	for _, m := range ms {
		for _, ag := range m.AgentList() {
			for _, def := range ag.Triggers {
				e := &triggerEntry{def: def, state: TriggerState{Methodology: m.Name, Agent: ag.Name, Name: def.Name, Description: def.Description,
					Type: def.Type, Event: def.Event, Schedule: def.Schedule, Enabled: def.Enabled}}
				if prev, ok := old[e.state.Key()]; ok {
					e.state.Fires, e.state.LastFired, e.state.LastProcessID, e.state.LastError = prev.state.Fires, prev.state.LastFired, prev.state.LastProcessID, prev.state.LastError
				}
				if def.Type == methodology.TriggerEvent {
					f, err := condition.CompileEventFilter(def.Filter)
					if err != nil {
						e.state.LastError = err.Error()
						e.state.Enabled = false
					}
					e.filter = f
				}
				key := e.state.Key()
				if def.Type == methodology.TriggerSchedule && def.Enabled && t.cron != nil {
					id, err := t.cron.AddFunc(def.Schedule, func() {
						if _, err := t.fireKey(context.Background(), key, nil); err != nil {
							t.log().Warn("scheduled trigger", "trigger", key, "err", err)
						}
					})
					if err != nil {
						e.state.LastError = err.Error()
					}
					e.cronID = id
				}
				t.entries[key] = e
			}
		}
	}
	return nil
}

// States lists the triggers with their state.
func (t *TriggerManager) States() []TriggerState {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]TriggerState, 0, len(t.entries))
	for _, e := range t.entries {
		s := e.state
		if e.cronID != 0 && t.cron != nil {
			s.NextFire = t.cron.Entry(e.cronID).Next
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key() < out[j].Key() })
	return out
}

// Handle fires the event triggers matching ev.
func (t *TriggerManager) Handle(ctx context.Context, ev TriggerEvent) {
	if ev.Type == "methodology.published" {
		if err := t.Reload(ctx); err != nil {
			t.log().Warn("triggers reload", "err", err)
		}
	}
	t.mu.Lock()
	var keys []string
	act := ev.activation()
	for key, e := range t.entries {
		if e.state.Enabled && e.def.Type == methodology.TriggerEvent && e.def.Event == ev.Type && e.filter.Match(act) {
			keys = append(keys, key)
		}
	}
	t.mu.Unlock()
	sort.Strings(keys)
	for _, key := range keys {
		if _, err := t.fireKey(ctx, key, &ev); err != nil && !errors.Is(err, errSkipped) {
			t.log().Warn("event trigger", "trigger", key, "event", ev.Type, "err", err)
		}
	}
}

// Fire fires a trigger manually (the event of event triggers is absent).
func (t *TriggerManager) Fire(ctx context.Context, methodologyName, agent, name string) (*Process, error) {
	return t.fireKey(ctx, methodologyName+"/"+agent+"/"+name, nil)
}

var errSkipped = errors.New("trigger skipped")

func (t *TriggerManager) fireKey(ctx context.Context, key string, ev *TriggerEvent) (*Process, error) {
	t.mu.Lock()
	e, ok := t.entries[key]
	if !ok {
		t.mu.Unlock()
		return nil, fmt.Errorf("%s: %w", key, errUnknownTrigger)
	}
	def, state := e.def, e.state
	minInterval := t.MinInterval
	if minInterval == 0 {
		minInterval = 2 * time.Second
	}
	// loop protection: never react to the output of this trigger
	if ev != nil {
		if (ev.Process != nil && ev.Process.Trigger == key) || (ev.Change != nil && ev.Change.Data["trigger"] == key) {
			t.mu.Unlock()
			return nil, errSkipped
		}
		if !state.LastFired.IsZero() && t.clock().Sub(state.LastFired) < minInterval {
			t.mu.Unlock()
			t.log().Warn("trigger rate limited", "trigger", key)
			return nil, errSkipped
		}
	}
	e.state.LastFired = t.clock()
	e.state.Fires++
	t.mu.Unlock()

	p, err := t.start(ctx, key, state, def, ev)
	t.mu.Lock()
	if cur, ok := t.entries[key]; ok {
		cur.state.LastError = ""
		if err != nil {
			cur.state.LastError = err.Error()
		} else {
			cur.state.LastProcessID = p.ID
		}
	}
	t.mu.Unlock()
	return p, err
}

func (t *TriggerManager) start(ctx context.Context, key string, s TriggerState, def methodology.Trigger, ev *TriggerEvent) (*Process, error) {
	who := authz.Principal{Subject: "system:trigger:" + key, Org: "system", Roles: def.Roles}
	if ev != nil && ev.Process != nil && ev.Process.Initiator.Org != "" {
		who.Org = ev.Process.Initiator.Org
	}
	ctx = authz.With(ctx, who)
	req := StartRequest{Methodology: s.Methodology, Agent: s.Agent, Goal: def.Goal, Intent: def.Intent, Trigger: key,
		Title: fmt.Sprintf("%s (trigger %s)", s.Agent, s.Name)}
	if req.Intent == "" {
		req.Intent = "Automatic run: " + strings.TrimSpace(s.Name+" "+def.Description)
	}
	if ev != nil {
		// the event is available to the agent (conditions, scripts, builtins)
		req.Vars = map[string]any{"event": ev.activation()}
	}
	switch {
	case def.Target == methodology.TargetEventChange:
		switch {
		case ev != nil && ev.Change != nil:
			req.ChangeID = ev.Change.ID
		case ev != nil && ev.Process != nil && ev.Process.ChangeID != "":
			req.ChangeID = ev.Process.ChangeID
		default:
			return nil, errors.New("event_change target needs a change or process event")
		}
	default:
		b, err := t.Engine.latestBaseline(ctx)
		if err != nil {
			return nil, err
		}
		req.BaselineID = b
	}
	p, err := t.Engine.Start(ctx, req)
	if err != nil {
		return nil, err
	}
	t.log().Info("trigger fired", "trigger", key, "process", p.ID, "status", p.Status)
	if p.Status == StatusRunning {
		t.Engine.schedule(p.ID)
	}
	return p, nil
}

func (e *Engine) latestBaseline(ctx context.Context) (domain.BaselineID, error) {
	bs, err := e.Graph.Baselines(ctx)
	if err != nil {
		return "", err
	}
	if len(bs) == 0 {
		return "", errors.New("no baseline")
	}
	latest := bs[0]
	for _, b := range bs[1:] {
		if b.CreatedAt.After(latest.CreatedAt) {
			latest = b
		}
	}
	return latest.ID, nil
}
