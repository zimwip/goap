// Package decision is the policy of the decision points of a change (ADR 0067): who decides, how sure an agent must be
// to settle a point alone, how many rulings may fail before a person takes over, and by when. The graph core keeps the
// mechanism (open, rule, answer, ratify, the status machine); it stores the opaque policy values the opener gives and
// asks a domain.DecisionPolicy. The composition roots plug Policy on the graph (Graph.DecisionPolicy), explicitly.
//
// The values of a point, by key (the opener gives some, Open resolves them):
//
//	decider      "agent" (default) or "human": a point of a human decider is ruled by a person only
//	threshold    confidence under which an agent ruling waits for a person's ratification (default 0.7)
//	maxRounds    rulings that may fail to settle the point (undecidable, or a ratification refused) before it is
//	             escalated (default 3; negative: no limit)
//	maxDuration  the opener's duration ("24h", a Go duration or a time.Duration): becomes the deadline, past which
//	             the point is escalated (none: no deadline)
//
// and, kept by the policy as it folds the events, rounds (the rulings that failed to settle the point) and deadline
// (RFC 3339).
package decision

import (
	"fmt"
	"time"

	"github.com/zimwip/goap/pkg/domain"
)

// Keys of the policy values of a decision point.
const (
	KeyDecider     = "decider"
	KeyThreshold   = "threshold"
	KeyMaxRounds   = "maxRounds"
	KeyMaxDuration = "maxDuration"
	KeyDeadline    = "deadline"
	KeyRounds      = "rounds"
)

// Deciders.
const (
	DeciderAgent = "agent"
	DeciderHuman = "human"
)

// Defaults of a decision point.
const (
	DefaultThreshold = 0.7
	DefaultRounds    = 3
)

// Policy is the confidence / rounds / deadline policy (domain.DecisionPolicy). It holds no state.
type Policy struct{}

var _ domain.DecisionPolicy = Policy{}

// Open resolves the values the opener gave: the defaults, their validation, the deadline from maxDuration.
func (Policy) Open(given map[string]any, now time.Time) (map[string]any, error) {
	for k := range given {
		switch k {
		case KeyDecider, KeyThreshold, KeyMaxRounds, KeyMaxDuration:
		default:
			return nil, fmt.Errorf("unknown decision policy value %q", k)
		}
	}
	out := map[string]any{}
	switch d := given[KeyDecider]; d {
	case nil, "", DeciderAgent:
		out[KeyDecider] = DeciderAgent
	case DeciderHuman:
		out[KeyDecider] = DeciderHuman
	default:
		return nil, fmt.Errorf("decider %v: agent or human", d)
	}
	threshold, err := number(given, KeyThreshold)
	if err != nil {
		return nil, err
	}
	if threshold == 0 {
		threshold = DefaultThreshold
	}
	if threshold < 0 || threshold > 1 {
		return nil, fmt.Errorf("a confidence threshold is between 0 and 1, not %v", threshold)
	}
	out[KeyThreshold] = threshold
	rounds, err := number(given, KeyMaxRounds)
	if err != nil {
		return nil, err
	}
	if rounds == 0 {
		rounds = DefaultRounds
	}
	out[KeyMaxRounds] = int64(rounds)
	switch v := given[KeyMaxDuration].(type) {
	case nil:
	case time.Duration:
		if v > 0 {
			out[KeyDeadline] = now.Add(v).Format(time.RFC3339Nano)
		}
	case string:
		if v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				return nil, fmt.Errorf("max duration: %w", err)
			}
			if d > 0 {
				out[KeyDeadline] = now.Add(d).Format(time.RFC3339Nano)
			}
		}
	default:
		return nil, fmt.Errorf("max duration %v: a duration such as \"24h\"", v)
	}
	return out, nil
}

// Fold takes an event into account. A decided ruling settles the point when it comes from a person or reaches the
// threshold; an accepted ratification settles it; an undecidable ruling or a refused ratification is a failed round.
func (Policy) Fold(p *domain.DecisionPoint, ev domain.DecisionEvent) bool {
	switch ev.Op {
	case domain.DecisionRuleOp:
		if ev.Outcome == domain.OutcomeUndecidable {
			failRound(p)
			return false
		}
		t, _ := number(p.Policy, KeyThreshold)
		return ev.Human || ev.Confidence >= t
	case domain.DecisionRatifyOp:
		if ev.Accept {
			return true
		}
		failRound(p)
	}
	return false
}

// Reserve reserves the point to a person when its decider is one, or escalates it: too many failed rounds, or past the
// deadline.
func (Policy) Reserve(p domain.DecisionPoint, now time.Time) (bool, string) {
	human := false
	if d, _ := p.Policy[KeyDecider].(string); d == DeciderHuman {
		human = true
	}
	rounds, max := Rounds(p), MaxRounds(p)
	if max > 0 && rounds >= max {
		return human, fmt.Sprintf("%d rounds without a decision", rounds)
	}
	if s, _ := p.Policy[KeyDeadline].(string); s != "" {
		if deadline, err := time.Parse(time.RFC3339Nano, s); err == nil && now.After(deadline) {
			return human, "past the deadline " + deadline.UTC().Format(time.RFC3339)
		}
	}
	return human, ""
}

// Rounds is the number of rulings that failed to settle the point: undecidable, or a ratification refused.
func Rounds(p domain.DecisionPoint) int {
	n, _ := number(p.Policy, KeyRounds)
	return int(n)
}

// Decider is who rules the point: agent or human.
func Decider(p domain.DecisionPoint) string {
	if d, _ := p.Policy[KeyDecider].(string); d != "" {
		return d
	}
	return DeciderAgent
}

// Threshold is the confidence an agent ruling must reach to settle the point.
func Threshold(p domain.DecisionPoint) float64 {
	n, _ := number(p.Policy, KeyThreshold)
	return n
}

// MaxRounds is the number of failed rounds after which the point is escalated (0 or less: no limit).
func MaxRounds(p domain.DecisionPoint) int {
	n, _ := number(p.Policy, KeyMaxRounds)
	return int(n)
}

func failRound(p *domain.DecisionPoint) {
	if p.Policy == nil {
		p.Policy = map[string]any{}
	}
	p.Policy[KeyRounds] = int64(Rounds(*p) + 1)
}

// number reads a numeric policy value, whatever its encoding (the values travel as JSON): absent is 0.
func number(m map[string]any, key string) (float64, error) {
	switch v := m[key].(type) {
	case nil:
		return 0, nil
	case float64:
		return v, nil
	case float32:
		return float64(v), nil
	case int:
		return float64(v), nil
	case int32:
		return float64(v), nil
	case int64:
		return float64(v), nil
	}
	return 0, fmt.Errorf("%s %v: a number", key, m[key])
}
