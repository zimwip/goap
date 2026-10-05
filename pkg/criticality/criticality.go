// Package criticality is the use case of the criticality of a change (ADR 0075 §3): the weight of its effect, C1 (low),
// C2 (current) or C3 (critical), kept in Change.Data (domain.DataCriticality; the graph never reads it) and the policy
// of the organisation that says what each level requires: the kinds of oracle that may verify an effect, whether a
// review may be sampled, the role of whoever signs a derogation and how long a derogation may last.
//
// The methodology names a default level (methodology.Methodology.Criticality), the requester may raise it, only a
// permission lowers it (ABAC change:lower-criticality, administrators by default). What a level requires is organisation
// data, resolved along the unit chain with the nearest unit winning (pkg/access, design rule 3); Defaults is the table
// that applies where the organisation gives none. The graph and the domain model import nothing from here
// (pkg/layering).
package criticality

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/risk"
	"github.com/zimwip/goap/pkg/verify"
)

// Level is the criticality of a change.
type Level string

// The levels, from the lowest.
const (
	C1 Level = "C1"
	C2 Level = "C2"
	C3 Level = "C3"
)

// Default is the level of a change that names none: current.
const Default = C2

// Levels lists the levels, lowest first.
var Levels = []Level{C1, C2, C3}

// Valid reports whether l is a level.
func Valid(l Level) bool { return slices.Contains(Levels, l) }

// Rank orders the levels (C1 < C2 < C3); an unknown level ranks 0.
func Rank(l Level) int { return slices.Index(Levels, l) + 1 }

// Of returns the level held by the data of a change (Default when it names none or an unknown one).
func Of(data map[string]any) Level {
	if s, _ := data[domain.DataCriticality].(string); Valid(Level(s)) {
		return Level(s)
	}
	return Default
}

// Lowers reports whether moving from one level to another lowers the criticality.
func Lowers(from, to Level) bool { return Rank(to) < Rank(from) }

// Policy is what the organisation requires of the changes of one level.
type Policy struct {
	// Oracles are the kinds of oracle (tool, human, model) accepted to verify an effect; empty: any.
	Oracles []string `json:"oracles"`
	// Sampling says a human review may be sampled instead of covering every effect.
	Sampling bool `json:"sampling"`
	// SignatoryRole is the role whoever signs a derogation holds on the project of the change; empty: any signatory
	// (the permission derogation:sign still holds).
	SignatoryRole string `json:"signatoryRole,omitempty"`
	// MaxDerogation is the longest a derogation may last from its signature; zero: unbounded.
	MaxDerogation time.Duration `json:"maxDerogation,omitempty"`
}

// Accepts reports whether the oracle kind may verify an effect.
func (p Policy) Accepts(oracle string) bool {
	return len(p.Oracles) == 0 || slices.Contains(p.Oracles, oracle)
}

// Map is the policy as the plain values CEL reads (criticalityPolicy): oracles, sampling, signatoryRole and
// maxDerogationHours.
func (p Policy) Map() map[string]any {
	oracles := make([]any, len(p.Oracles))
	for i, o := range p.Oracles {
		oracles[i] = o
	}
	return map[string]any{"oracles": oracles, "sampling": p.Sampling, "signatoryRole": p.SignatoryRole,
		"maxDerogationHours": int64(p.MaxDerogation / time.Hour)}
}

// Defaults is the compiled-in table, used where the units of the organisation hold none for a level: C1 is loose (any
// oracle, sampling, a month), C2 asks a tool or a person (sampling, two weeks), C3 asks a person to verify and a
// signatory of the role "derogation_signatory" to waive, for a week at most.
func Defaults() map[Level]Policy {
	return map[Level]Policy{
		C1: {Oracles: []string{"tool", "model", "human"}, Sampling: true, MaxDerogation: 30 * 24 * time.Hour},
		C2: {Oracles: []string{"tool", "human"}, Sampling: true, MaxDerogation: 14 * 24 * time.Hour},
		C3: {Oracles: []string{"human"}, SignatoryRole: "derogation_signatory", MaxDerogation: 7 * 24 * time.Hour},
	}
}

// Resolver gives the policy of a level for a change: the organisation of the change resolves it (pkg/access).
type Resolver func(ctx context.Context, c domain.Change, l Level) Policy

// DefaultResolver resolves every level from the compiled-in table.
func DefaultResolver(_ context.Context, _ domain.Change, l Level) Policy { return Defaults()[l] }

// ItemPolicy is the check the graph asks of the items about to be written (Graph.ItemPolicy): a verification of an
// effect names an oracle the policy of the change accepts, and a derogation lasts no longer than it allows. Items of
// other kinds are not looked at. The signatory role is checked by the services that know the roles (graphsvc).
func ItemPolicy(resolve Resolver) func(ctx context.Context, c domain.Change, it domain.ChangeItem) error {
	if resolve == nil {
		resolve = DefaultResolver
	}
	return func(ctx context.Context, c domain.Change, it domain.ChangeItem) error {
		switch it.Kind {
		case verify.KindVerification:
			if state, _ := it.Data[verify.KeyState].(string); state != verify.Produced {
				return nil
			}
			oracle, _ := it.Data[verify.KeyOracle].(string)
			if oracle == "" {
				return nil
			}
			lvl := Of(c.Data)
			if p := resolve(ctx, c, lvl); !p.Accepts(oracle) {
				return fmt.Errorf("a %s oracle cannot verify the effects of a %s change (accepted: %v)", oracle, lvl, p.Oracles)
			}
		case risk.KindDerogation:
			if st, _ := it.Data["status"].(string); st == risk.DerogationClosed {
				return nil
			}
			lvl := Of(c.Data)
			p := resolve(ctx, c, lvl)
			if p.MaxDerogation <= 0 {
				return nil
			}
			exp, _ := it.Data["expires"].(string)
			t, err := risk.ParseExpiry(exp)
			if err != nil {
				return nil // validateDerogation says so
			}
			at := it.CreatedAt
			if at.IsZero() {
				at = time.Now()
			}
			if t.Sub(at) > p.MaxDerogation {
				return fmt.Errorf("a derogation on a %s change lasts %s at most (policy of the organisation), not until %s", lvl, p.MaxDerogation, t.Format(time.RFC3339))
			}
		}
		return nil
	}
}
