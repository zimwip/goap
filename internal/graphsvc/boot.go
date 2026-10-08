package graphsvc

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/llmcfg"
)

// Options says what differs between the compositions that boot the platform (goap-dev, cmd/graph).
type Options struct {
	// Models is the model gateway configuration seeded when the graph holds no provider yet (SeedModels); empty: no
	// model is seeded.
	Models ModelsConfig
	// RequireHooks makes Boot panic when the guard-relevant hooks of the graph (Authorizer, ChangeAuthorizer, Validators)
	// are not set: a composition that serves callers sets it, so that a seed never runs ahead of the access gating.
	RequireHooks bool
	// Dev, when set, runs last: the development or demo data of the composition (internal/devseed). The platform
	// bootstrap never carries it, a production composition leaves it nil.
	Dev func(ctx context.Context, g *graph.Graph) error
	// Log receives the one-line warning about the hooks the composition lacks; the default logger when nil.
	Log *slog.Logger
}

// ModelsConfig is the initial configuration of the model gateway.
type ModelsConfig struct {
	Providers []llmcfg.Provider
	Models    []llmcfg.Model
	Aliases   []llmcfg.Alias
}

// Report says what Boot wrote.
type Report struct {
	Access, Builtins, Models, Aliases, Behaviors bool
}

// Boot is the platform bootstrap every composition runs, the same steps in the same order (ADR 0071). Ordering contract:
//
//  1. Before Boot, the caller has assigned every hook of the graph (Authorizer, ChangeAuthorizer, Validators and, where it
//     holds the registry, its Guardians and DefaultGuardian (ADR 0098) and Lifecycles, plus DecisionPolicy and Types) and has a type
//     catalogue holding the domains Boot and Options.Dev write to (the registry seeds the domains: SeedDomains, then
//     Types reload). Boot writes through those hooks: it never runs ahead of them.
//  2. Boot: Graph.Bootstrap (the roots ORG-DEFAULT and PROJ-ROOT, ADR 0054), SeedAccess (the default policies),
//     SeedBuiltins (the built-in MCPs, adapter definitions, roles), SeedModels (the model gateway configuration), SeedProtectedAliases (the assistant and helper aliases, ADR 0084), SeedBehaviors (the built-in LLM behaviour, disabled, ADR 0093), then
//     Options.Dev if any. Every step is idempotent, so a second call writes nothing.
//  3. After Boot, the registry seeds the methodologies (Service.Seed): they need the model aliases of SeedModels.
//
// A composition without the registry in process cannot wire a guardian and Lifecycles: Boot logs one warning saying so.
func Boot(ctx context.Context, g *graph.Graph, o Options) (Report, error) {
	var r Report
	if o.RequireHooks && (g.Authorizer == nil || g.ChangeAuthorizer == nil || len(g.Validators) == 0) {
		panic("graphsvc.Boot: the graph hooks (Authorizer, ChangeAuthorizer, Validators) must be set before the bootstrap")
	}
	if len(g.Guardians) == 0 || g.Lifecycles == nil {
		log := o.Log
		if log == nil {
			log = slog.Default()
		}
		log.Warn("change lifecycle and activity gating are not available in this composition (no registry in process)")
	}
	// the seeds are the platform acting by itself: its transitions are authorized for it
	ctx = System(ctx)
	if err := g.Bootstrap(ctx); err != nil {
		return r, fmt.Errorf("bootstrap: %w", err)
	}
	var err error
	if r.Access, err = SeedAccess(ctx, g); err != nil {
		return r, fmt.Errorf("seed access: %w", err)
	}
	if r.Builtins, err = SeedBuiltins(ctx, g); err != nil {
		return r, fmt.Errorf("seed built-in MCPs: %w", err)
	}
	if r.Models, err = SeedModels(ctx, g, o.Models.Providers, o.Models.Models, o.Models.Aliases); err != nil {
		return r, fmt.Errorf("seed models: %w", err)
	}
	if r.Aliases, err = SeedProtectedAliases(ctx, g); err != nil {
		return r, fmt.Errorf("seed protected aliases: %w", err)
	}
	if r.Behaviors, err = SeedBehaviors(ctx, g); err != nil {
		return r, fmt.Errorf("seed LLM behaviors: %w", err)
	}
	if o.Dev != nil {
		if err := o.Dev(ctx, g); err != nil {
			return r, fmt.Errorf("dev seed: %w", err)
		}
	}
	return r, nil
}
