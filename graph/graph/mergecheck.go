package graph

import (
	"context"
	"fmt"
	"slices"

	"github.com/zimwip/goap/contracts/domain"
)

// The merge of a change is mechanical (ADR 0146): it takes the work the change committed into what it was forked from,
// combining two views of the graph, and asks no methodology. It only checks what makes the work whole: the changes it
// depends on reached the branch it merges into (ADR 0108), and its lifecycle, when it follows one, is in a final state.
// What the methodology says about the work, its guardians, is asked at each commit (commitcheck.go).

// checkMerge refuses the merge of a change whose dependencies or lifecycle do not allow it yet.
func (g *Graph) checkMerge(ctx context.Context, tx Tx, c domain.Change, closing bool) error {
	deps, err := dependenciesOf(ctx, tx, c)
	if err != nil {
		return err
	}
	// first the changes it depends on (ADR 0108): their work has reached the branch it merges into
	if err := checkDependencies(c, deps); err != nil {
		return err
	}
	// the final state of its lifecycle is the close's (ADR 0166): a change merges what it committed in any state
	if c.Lifecycle != "" && closing {
		lc, err := g.changeLifecycle(ctx, c)
		if err != nil {
			return err
		}
		if s, ok := lc.State(c.State); !ok || !s.Final {
			return fmt.Errorf("change %s is in state %s of lifecycle %s: it is merged once it is in a final state: %w", c.ID, c.State, c.Lifecycle, ErrConflict)
		}
	}
	return nil
}

// mainImpactsOf are the impacts of the main flow of a change: the ones its commit and its merge look at (read them,
// never edit them: they may be the change's own).
func mainImpactsOf(c domain.Change) []domain.ChangeImpact {
	main := func(cn domain.ChangeImpact) bool { return cn.Flow == "" && !cn.Superseded }
	n := 0
	for _, cn := range c.Nodes {
		if main(cn) {
			n++
		}
	}
	if n == len(c.Nodes) {
		return slices.Clip(c.Nodes) // every impact is the main flow's: no copy (the readers never edit them)
	}
	out := make([]domain.ChangeImpact, 0, n)
	for _, cn := range c.Nodes {
		if main(cn) {
			out = append(out, cn)
		}
	}
	return out
}
