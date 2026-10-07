package modelgw

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/llmcfg"
)

// The measured cost of a behaviour (ADR 0093, "Measured cost"): the input tokens a model reports for a minimal request
// carrying the instruction as its system text, minus those it reports for the same request with no system text (the
// baseline of that model). The estimate (four bytes a token) serves until a measure exists.

// Sources of a cost.
const (
	// CostMeasured: the difference of the input tokens the provider reported.
	CostMeasured = "measured"
	// CostEstimated: the provider reported no usable difference (no usage numbers, or none between the two requests), so the
	// byte estimate is kept; it is not measured again before the TTL.
	CostEstimated = "estimated"
)

// Defaults of the calibration.
const (
	DefaultCostTTL     = 30 * 24 * time.Hour
	DefaultCostBackoff = 10 * time.Minute
	// costWorkers bounds the calibration calls in flight.
	costWorkers = 2
	// costReload is how long the cache of the store is trusted (another instance may have measured).
	costReload = time.Minute
	// calibrationTimeout bounds one calibration call.
	calibrationTimeout = 60 * time.Second
	// calibrationText is the user message of a calibration request.
	calibrationText = "."
)

// CostKey identifies a cost: valid for one instruction text on one model.
type CostKey struct {
	Behavior, Hash, Provider, Model string
}

// BehaviorCost is the stored cost of a behaviour on a model.
type BehaviorCost struct {
	CostKey
	Tokens, Baseline int64
	MeasuredAt       time.Time
	// Source is CostMeasured or CostEstimated.
	Source string
}

// ModelBaseline is the input tokens of the calibration request with no instruction, on a model.
type ModelBaseline struct {
	Provider, Model string
	Tokens          int64
	MeasuredAt      time.Time
}

// costBook is the in-memory side of the cost: a cache of the store, the calibrations in flight and the failures to wait out.
type costBook struct {
	mu       sync.Mutex
	loaded   time.Time
	costs    map[CostKey]BehaviorCost
	bases    map[[2]string]ModelBaseline
	inflight map[CostKey]bool
	failed   map[CostKey]time.Time // not before
	semOnce  sync.Once
	sem      chan struct{}
	baseMu   sync.Mutex // one baseline call at a time
	wg       sync.WaitGroup
}

func (b *costBook) semaphore() chan struct{} {
	b.semOnce.Do(func() { b.sem = make(chan struct{}, costWorkers) })
	return b.sem
}

func (s *Service) costTTL() time.Duration {
	if s.CostTTL > 0 {
		return s.CostTTL
	}
	return DefaultCostTTL
}

func (s *Service) costBackoff() time.Duration {
	if s.CostBackoff > 0 {
		return s.CostBackoff
	}
	return DefaultCostBackoff
}

// WaitCosts blocks until the calibrations in flight are over (tests, shutdown).
func (s *Service) WaitCosts() { s.cost.wg.Wait() }

// loadCosts refreshes the cache from the store when it is older than costReload. The caller holds cost.mu.
func (s *Service) loadCosts(ctx context.Context) {
	b := &s.cost
	if b.costs != nil && s.now().Sub(b.loaded) < costReload {
		return
	}
	costs, err := s.Store.BehaviorCosts(ctx)
	if err != nil {
		s.Log.Warn("behavior costs not read", "err", err)
		if b.costs == nil {
			b.costs, b.bases = map[CostKey]BehaviorCost{}, map[[2]string]ModelBaseline{}
		}
		return
	}
	bases, err := s.Store.ModelBaselines(ctx)
	if err != nil {
		s.Log.Warn("model baselines not read", "err", err)
		bases = nil
	}
	b.costs, b.bases = make(map[CostKey]BehaviorCost, len(costs)), make(map[[2]string]ModelBaseline, len(bases))
	for _, c := range costs {
		b.costs[c.CostKey] = c
	}
	for _, m := range bases {
		b.bases[[2]string{m.Provider, m.Model}] = m
	}
	b.loaded = s.now()
}

// cachedCost returns the cost stored for a key.
func (s *Service) cachedCost(ctx context.Context, k CostKey) (BehaviorCost, bool) {
	s.cost.mu.Lock()
	defer s.cost.mu.Unlock()
	s.loadCosts(ctx)
	c, ok := s.cost.costs[k]
	return c, ok
}

func (s *Service) fresh(c BehaviorCost) bool { return s.now().Sub(c.MeasuredAt) < s.costTTL() }

func costKeyOf(u llmcfg.Use, t Target) CostKey {
	return CostKey{Behavior: u.Name, Hash: llmcfg.InstructionHash(u.Instruction), Provider: t.Provider, Model: t.Model}
}

// Price is the tokens the behaviours added to a call: the measured cost of each where there is one, its byte estimate
// otherwise (Estimated is then true).
type Price struct {
	Tokens    int
	Estimated bool
}

// price prices the behaviours applied to a call served by t and, for each one without a fresh cost, asks for a calibration
// in the background. It reads the cache, never calls a model, and never fails.
func (s *Service) price(ctx context.Context, ap llmcfg.Applied, t Target) Price {
	var p Price
	for _, u := range ap.Uses {
		k := costKeyOf(u, t)
		c, ok := s.cachedCost(ctx, k)
		switch {
		case ok:
			p.Tokens += int(c.Tokens)
			p.Estimated = p.Estimated || c.Source != CostMeasured
		default:
			p.Tokens += llmcfg.EstimateTokens(u.Bytes)
			p.Estimated = true
		}
		if !ok || !s.fresh(c) {
			s.enqueueCost(k, u.Instruction)
		}
	}
	return p
}

// enqueueCost measures a cost in the background: once per key at a time, never again before the backoff of a failure
// is over, with at most costWorkers calls in flight. The caller does not wait and never sees an error.
func (s *Service) enqueueCost(k CostKey, instruction string) {
	b := &s.cost
	b.mu.Lock()
	if b.inflight == nil {
		b.inflight, b.failed = map[CostKey]bool{}, map[CostKey]time.Time{}
	}
	if b.inflight[k] || s.now().Before(b.failed[k]) {
		b.mu.Unlock()
		return
	}
	b.inflight[k] = true
	b.wg.Add(1)
	b.mu.Unlock()
	go func() {
		defer b.wg.Done()
		sem := b.semaphore()
		sem <- struct{}{}
		ctx, cancel := context.WithTimeout(context.Background(), calibrationTimeout)
		_, err := s.measure(ctx, k, instruction, false)
		cancel()
		<-sem
		b.mu.Lock()
		delete(b.inflight, k)
		if err != nil {
			b.failed[k] = s.now().Add(s.costBackoff())
		}
		b.mu.Unlock()
		if err != nil {
			s.Log.Info("behavior cost not measured", "behavior", k.Behavior, "model", k.Provider+"/"+k.Model, "retryAfter", s.costBackoff(), "err", err)
			return
		}
		s.pruneCosts(context.Background())
	}()
}

// calibrate makes one calibration call and returns the input tokens the provider reports. It goes through the normal path
// of the gateway (admission, quota, ledger: source "calibration", subject system:modelgw, the quota of the model counts
// its few tokens) but no behaviour applies to it and the roles of the model do not (it is the gateway's own call).
func (s *Service) calibrate(ctx context.Context, t Target, system string) (int, error) {
	ctx = authz.With(ctx, authz.System("modelgw"))
	ctx = llm.WithMeta(ctx, llm.CallMeta{Source: llm.SourceCalibration})
	resp, err := s.complete(ctx, llm.Request{Model: t.Provider + "/" + t.Model, System: system, MaxTokens: 1,
		Messages: []llm.Message{{Role: "user", Content: calibrationText}}}, true)
	if err != nil {
		return 0, err
	}
	return resp.Usage.InputTokens, nil
}

// baseline returns the input tokens of the request with no instruction on a model: stored, measured once per TTL (force:
// now).
func (s *Service) baseline(ctx context.Context, t Target, force bool) (int64, error) {
	s.cost.baseMu.Lock()
	defer s.cost.baseMu.Unlock()
	key := [2]string{t.Provider, t.Model}
	if !force {
		s.cost.mu.Lock()
		s.loadCosts(ctx)
		b, ok := s.cost.bases[key]
		s.cost.mu.Unlock()
		if ok && s.now().Sub(b.MeasuredAt) < s.costTTL() {
			return b.Tokens, nil
		}
	}
	n, err := s.calibrate(ctx, t, "")
	if err != nil {
		return 0, err
	}
	b := ModelBaseline{Provider: t.Provider, Model: t.Model, Tokens: int64(n), MeasuredAt: s.now().UTC()}
	if err := s.Store.PutModelBaseline(ctx, b); err != nil {
		s.Log.Warn("model baseline not stored", "model", t.Provider+"/"+t.Model, "err", err)
	}
	s.cost.mu.Lock()
	if s.cost.bases == nil {
		s.cost.bases = map[[2]string]ModelBaseline{}
	}
	s.cost.bases[key] = b
	s.cost.mu.Unlock()
	return b.Tokens, nil
}

// costOf is the arithmetic of a measure: the tokens of the instruction request minus the baseline, never negative. A
// provider that reported no usage (a zero), or none between the two requests, gives no measure: the byte estimate is kept
// and the source says so.
func costOf(withInstruction, baseline int64, instruction string) (tokens int64, source string) {
	if d := withInstruction - baseline; baseline > 0 && withInstruction > 0 && d > 0 {
		return d, CostMeasured
	}
	return int64(llmcfg.EstimateTokens(len(instruction) + 2)), CostEstimated
}

// measure calibrates one cost and stores it (force: measures the baseline again too). Nothing is stored when a call fails
// (the quota of the model is spent, the provider is down): the estimate stays.
func (s *Service) measure(ctx context.Context, k CostKey, instruction string, force bool) (BehaviorCost, error) {
	t := Target{Provider: k.Provider, Model: k.Model}
	base, err := s.baseline(ctx, t, force)
	if err != nil {
		return BehaviorCost{}, fmt.Errorf("baseline: %w", err)
	}
	in, err := s.calibrate(ctx, t, instruction)
	if err != nil {
		return BehaviorCost{}, err
	}
	tokens, source := costOf(int64(in), base, instruction)
	c := BehaviorCost{CostKey: k, Tokens: tokens, Baseline: base, MeasuredAt: s.now().UTC(), Source: source}
	if err := s.Store.PutBehaviorCost(ctx, c); err != nil {
		return c, fmt.Errorf("store: %w", err)
	}
	s.cost.mu.Lock()
	if s.cost.costs == nil {
		s.cost.costs = map[CostKey]BehaviorCost{}
	}
	s.cost.costs[k] = c
	delete(s.cost.failed, k)
	s.cost.mu.Unlock()
	return c, nil
}

// pruneCosts deletes, lazily after a measure, the costs of an instruction that changed, of a behaviour that left, and of a
// model that left the catalog, and the baselines of those models.
func (s *Service) pruneCosts(ctx context.Context) {
	snap, err := s.sync(ctx)
	if err != nil || snap == nil {
		return
	}
	hashes := map[string]map[string]bool{}
	for _, b := range append(slices.Clone(snap.Behaviors), snap.Off...) {
		if hashes[b.Name] == nil {
			hashes[b.Name] = map[string]bool{}
		}
		hashes[b.Name][llmcfg.InstructionHash(b.Instruction)] = true
	}
	models := map[string]bool{}
	for _, m := range snap.Models {
		models[m.Provider+"/"+m.Model] = true
	}
	s.cost.mu.Lock()
	s.loadCosts(ctx)
	var stale []CostKey
	for k := range s.cost.costs {
		if !hashes[k.Behavior][k.Hash] || !models[k.Provider+"/"+k.Model] {
			stale = append(stale, k)
		}
	}
	var staleBases [][2]string
	for k := range s.cost.bases {
		if !models[k[0]+"/"+k[1]] {
			staleBases = append(staleBases, k)
		}
	}
	for _, k := range stale {
		delete(s.cost.costs, k)
	}
	for _, k := range staleBases {
		delete(s.cost.bases, k)
	}
	s.cost.mu.Unlock()
	if len(stale) > 0 {
		if err := s.Store.DeleteBehaviorCosts(ctx, stale); err != nil {
			s.Log.Warn("behavior costs not purged", "err", err)
		}
	}
	if len(staleBases) > 0 {
		if err := s.Store.DeleteModelBaselines(ctx, staleBases); err != nil {
			s.Log.Warn("model baselines not purged", "err", err)
		}
	}
}

// ---- read side --------------------------------------------------------------

// CostView is the cost of a behaviour on a model, as the administrators see it.
type CostView struct {
	Behavior         string
	Provider, Model  string
	Aliases          []string // the aliases of the platform resolving to the model that the behaviour applies to
	Tokens, Baseline int64
	Source           string
	MeasuredAt       time.Time // zero: never measured (the estimate)
	// Error is why a measure asked for failed.
	Error string
}

// BehaviorView is a behaviour with its costs.
type BehaviorView struct {
	llmcfg.Behavior
	Costs []CostView
}

type costTarget struct {
	t       Target
	aliases []string
}

// behaviorTargets lists the models a behaviour can apply to: the enabled catalog models reached by an alias that the
// behaviour's alias and model selectors accept, and the models its selector names without an alias.
func behaviorTargets(snap *llmcfg.Snapshot, b llmcfg.Behavior) []costTarget {
	enabled := map[string]bool{}
	for _, m := range snap.Models {
		if m.Enabled {
			enabled[m.Provider+"/"+m.Model] = true
		}
	}
	by := map[string]*costTarget{}
	add := func(target, alias string) {
		t, ok := parseTarget(target)
		if !ok || !enabled[target] {
			return
		}
		ct := by[target]
		if ct == nil {
			ct = &costTarget{t: t}
			by[target] = ct
		}
		if alias != "" && !slices.Contains(ct.aliases, alias) {
			ct.aliases = append(ct.aliases, alias)
		}
	}
	in := func(list []string, v string) bool { return len(list) == 0 || slices.Contains(list, v) }
	for _, a := range snap.Aliases {
		if in(b.Aliases, a.Alias) && in(b.Models, a.Target) {
			add(a.Target, a.Alias)
		}
	}
	for _, m := range b.Models {
		if len(b.Aliases) == 0 {
			add(m, "")
		}
	}
	out := make([]costTarget, 0, len(by))
	for _, ct := range by {
		sort.Strings(ct.aliases)
		out = append(out, *ct)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].t.Provider+"/"+out[i].t.Model < out[j].t.Provider+"/"+out[j].t.Model
	})
	return out
}

// costView reads the cost of a behaviour on a model: the stored one, else the byte estimate.
func (s *Service) costView(ctx context.Context, b llmcfg.Behavior, ct costTarget) CostView {
	v := CostView{Behavior: b.Name, Provider: ct.t.Provider, Model: ct.t.Model, Aliases: ct.aliases}
	k := CostKey{Behavior: b.Name, Hash: llmcfg.InstructionHash(b.Instruction), Provider: ct.t.Provider, Model: ct.t.Model}
	if c, ok := s.cachedCost(ctx, k); ok {
		v.Tokens, v.Baseline, v.Source, v.MeasuredAt = c.Tokens, c.Baseline, c.Source, c.MeasuredAt
		return v
	}
	v.Tokens, v.Source = int64(llmcfg.EstimateTokens(len(strings.TrimSpace(b.Instruction))+2)), CostEstimated
	return v
}

// BehaviorViews lists the behaviours with their cost on each model they apply to.
func (s *Service) BehaviorViews(ctx context.Context) ([]BehaviorView, error) {
	list, err := s.Behaviors(ctx)
	if err != nil {
		return nil, err
	}
	snap, err := s.sync(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]BehaviorView, len(list))
	for i, b := range list {
		out[i] = BehaviorView{Behavior: b}
		for _, ct := range behaviorTargets(snap, b) {
			out[i].Costs = append(out[i].Costs, s.costView(ctx, b, ct))
		}
	}
	return out, nil
}

// MeasureBehaviors measures now the cost of the named behaviours (none named: all, disabled ones included, so an
// administrator sees the cost before enabling) on the named provider/models (none: every model they apply to), whatever
// the age of the stored costs and the backoff of an earlier failure. A call that fails is reported on its row (the
// estimate is kept), not as an error.
func (s *Service) MeasureBehaviors(ctx context.Context, names, models []string) ([]CostView, error) {
	list, err := s.Behaviors(ctx)
	if err != nil {
		return nil, err
	}
	snap, err := s.sync(ctx)
	if err != nil {
		return nil, err
	}
	var out []CostView
	for _, b := range list {
		if len(names) > 0 && !slices.Contains(names, b.Name) {
			continue
		}
		for _, ct := range behaviorTargets(snap, b) {
			if len(models) > 0 && !slices.Contains(models, ct.t.Provider+"/"+ct.t.Model) {
				continue
			}
			k := CostKey{Behavior: b.Name, Hash: llmcfg.InstructionHash(b.Instruction), Provider: ct.t.Provider, Model: ct.t.Model}
			v := s.costView(ctx, b, ct)
			if c, err := s.measure(ctx, k, strings.TrimSpace(b.Instruction), true); err != nil {
				v.Error = err.Error()
				if errors.Is(err, ErrQuotaExceeded) {
					v.Error = "quota exhausted: " + v.Error
				}
			} else {
				v.Tokens, v.Baseline, v.Source, v.MeasuredAt = c.Tokens, c.Baseline, c.Source, c.MeasuredAt
			}
			out = append(out, v)
		}
	}
	s.pruneCosts(ctx)
	return out, nil
}
