package llmcfg

import (
	"context"
	"fmt"

	"github.com/zimwip/goap/pkg/graph"
)

// ProtectedAliasValidator keeps the protected aliases of the platform (ADR 0084) in place: a change may retarget one but
// never retire it, rename it or drop its protected flag, and the aliases the platform uses by name are always created
// protected. Wired onto Graph.Validators (cmd/goap-dev, cmd/graph); pkg/graph knows no alias name.
type ProtectedAliasValidator struct{}

// Types runs the validator whenever a change touches an alias.
func (ProtectedAliasValidator) Types() []string { return []string{NodeTypeAlias} }

// Validate checks every alias version the change writes against the one it replaces.
func (ProtectedAliasValidator) Validate(_ context.Context, _ graph.ValidatorQuery, impacted []graph.ValidatedNode) error {
	for _, v := range impacted {
		post, err := aliasOfProps(v.Post.Properties)
		if err != nil {
			continue // a malformed alias is the attribute check's to refuse, not this invariant's
		}
		if v.Pre == nil {
			if isProtectedName(post.Alias) && !post.Protected {
				return fmt.Errorf("alias %s is protected: it must be created with protected set: %w", post.Alias, graph.ErrInvalid)
			}
			continue
		}
		pre, err := aliasOfProps(v.Pre.Properties)
		if err != nil || !pre.Protected {
			continue
		}
		switch {
		case v.Post.State == StateRetired:
			return fmt.Errorf("alias %s is protected: it cannot be retired, only retargeted: %w", pre.Alias, graph.ErrInvalid)
		case !post.Protected:
			return fmt.Errorf("alias %s is protected: its protected flag cannot be removed: %w", pre.Alias, graph.ErrInvalid)
		case post.Alias != pre.Alias:
			return fmt.Errorf("alias %s is protected: it cannot be renamed to %q: %w", pre.Alias, post.Alias, graph.ErrInvalid)
		}
	}
	return nil
}

func isProtectedName(name string) bool {
	for _, p := range ProtectedAliases() {
		if p == name {
			return true
		}
	}
	return false
}

// aliasOfProps reads an alias without checking its target (the protected ones may have none).
func aliasOfProps(props map[string]any) (a Alias, err error) {
	err = viaJSON(props, &a)
	return a, err
}
