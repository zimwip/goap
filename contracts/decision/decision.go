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
//	board        the roles that sit on the point (ADR 0132 §3): each rules once, in its seat; a point of a board is
//	             ruled by people only
//	rule         how the rulings of a board settle the point: unanimity (default; every seat rules the go option, a
//	             ruling of the no-go option settles it at once), majority (more than half of the seats rule the same
//	             option), consent (every seat rules; it goes unless a seat objects, which settles it at once)
//	chair        the role that settles a board the rule left undecided (every seat ruled, no settlement: a split
//	             majority): its further ruling decides (ADR 0132 §3, the accountable role of the gate)
//	gate         the step of the gate the point decides, transition the transition it gates (stored for the readers)
//
// and, kept by the policy as it folds the events, rounds (the rulings that failed to settle the point), deadline
// (RFC 3339) and votes (the option ruled in each seat of a board).
package decision

import (
	"fmt"
	"slices"
	"time"

	"github.com/zimwip/goap/contracts/domain"
)

// Keys of the policy values of a decision point.
const (
	KeyDecider     = "decider"
	KeyThreshold   = "threshold"
	KeyMaxRounds   = "maxRounds"
	KeyMaxDuration = "maxDuration"
	KeyDeadline    = "deadline"
	KeyRounds      = "rounds"
	KeyBoard       = "board"
	KeyRule        = "rule"
	KeyVotes       = "votes"
	KeyChair       = "chair"
	KeyGate        = "gate"
	KeyTransition  = "transition"
)

// Rules of a board (ADR 0132 §3).
const (
	RuleUnanimity = "unanimity"
	RuleMajority  = "majority"
	RuleConsent   = "consent"
)

// Options of a gate (ADR 0132 §3): the first one goes, the second one stops; abstain is a ruling of a seat that
// neither goes nor stops (consent).
const (
	OptionGo      = "go"
	OptionNoGo    = "no_go"
	OptionAbstain = "abstain"
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

var (
	_ domain.DecisionPolicy = Policy{}
	_ domain.SeatedPolicy   = Policy{}
)

// Open resolves the values the opener gave: the defaults, their validation, the deadline from maxDuration.
func (Policy) Open(given map[string]any, now time.Time) (map[string]any, error) {
	for k := range given {
		switch k {
		case KeyDecider, KeyThreshold, KeyMaxRounds, KeyMaxDuration, KeyBoard, KeyRule, KeyChair, KeyGate, KeyTransition:
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
	for _, k := range []string{KeyGate, KeyTransition, KeyChair} {
		if v, ok := given[k].(string); ok && v != "" {
			out[k] = v
		}
	}
	board, err := strings(given, KeyBoard)
	if err != nil {
		return nil, err
	}
	if len(board) > 0 {
		out[KeyBoard] = board
		out[KeyDecider] = DeciderHuman // a board is people
		switch r := given[KeyRule]; r {
		case nil, "":
			out[KeyRule] = RuleUnanimity
		case RuleUnanimity, RuleMajority, RuleConsent:
			out[KeyRule] = r
		default:
			return nil, fmt.Errorf("rule %v: unanimity, majority or consent", r)
		}
	} else if r, _ := given[KeyRule].(string); r != "" {
		return nil, fmt.Errorf("rule %s applies to a board: name its roles", r)
	}
	return out, nil
}

// Seats are the roles of the board of a point (nil: no board, anyone rules it alone).
func (Policy) Seats(p domain.DecisionPoint) []string { return Board(p) }

// Board is the roles that sit on the point (nil: none).
func Board(p domain.DecisionPoint) []string {
	b, _ := strings(p.Policy, KeyBoard)
	return b
}

// Rule is how the rulings of the board settle the point.
func Rule(p domain.DecisionPoint) string {
	if r, _ := p.Policy[KeyRule].(string); r != "" {
		return r
	}
	return RuleUnanimity
}

// Votes are the options ruled in the seats of a board, by role.
func Votes(p domain.DecisionPoint) map[string]string {
	out := map[string]string{}
	switch v := p.Policy[KeyVotes].(type) {
	case map[string]string:
		for k, o := range v {
			out[k] = o
		}
	case map[string]any:
		for k, o := range v {
			if s, ok := o.(string); ok {
				out[k] = s
			}
		}
	}
	return out
}

// foldBoard records the ruling of a seat and says whether the board settles the point, setting the option it settles
// on. A ruling outside a seat, or of a seat already ruled, changes nothing. An unsettling ruling is a vote, not a
// ruling waiting for its ratification: the point keeps no ruling then.
func foldBoard(p *domain.DecisionPoint, board []string, ev domain.DecisionEvent) bool {
	option, settled := boardRuling(p, board, ev)
	if !settled {
		p.Ruling = nil
		return false
	}
	if p.Ruling != nil {
		p.Ruling.Option = option
	}
	return true
}

func boardRuling(p *domain.DecisionPoint, board []string, ev domain.DecisionEvent) (string, bool) {
	votes := Votes(*p)
	if chair, _ := p.Policy[KeyChair].(string); chair != "" && ev.Role == chair && len(votes) >= len(board) {
		return ev.Option, true // every seat ruled and the rule settled nothing: the chair decides
	}
	if !slices.Contains(board, ev.Role) || votes[ev.Role] != "" {
		return "", false
	}
	votes[ev.Role] = ev.Option
	if p.Policy == nil {
		p.Policy = map[string]any{}
	}
	p.Policy[KeyVotes] = votes
	count := map[string]int{}
	for _, role := range board {
		if o := votes[role]; o != "" {
			count[o]++
		}
	}
	switch Rule(*p) {
	case RuleMajority:
		for o, n := range count {
			if 2*n > len(board) && o != OptionAbstain {
				return o, true
			}
		}
	case RuleConsent:
		switch {
		case ev.Option == OptionNoGo:
			return OptionNoGo, true
		case len(votes) >= len(board):
			return OptionGo, true
		}
	default:
		switch {
		case ev.Option != OptionGo:
			return ev.Option, true
		case count[OptionGo] == len(board):
			return OptionGo, true
		}
	}
	return "", false
}

// strings reads a list of names of the policy values (a []string in process, a []any once read back as JSON).
func strings(m map[string]any, key string) ([]string, error) {
	switch v := m[key].(type) {
	case nil:
		return nil, nil
	case []string:
		return slices.Clone(v), nil
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			s, ok := x.(string)
			if !ok || s == "" {
				return nil, fmt.Errorf("%s: a list of role names", key)
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s %v: a list of role names", key, m[key])
}

// Fold takes an event into account. A decided ruling settles the point when it comes from a person or reaches the
// threshold; an accepted ratification settles it; an undecidable ruling or a refused ratification is a failed round.
func (Policy) Fold(p *domain.DecisionPoint, ev domain.DecisionEvent) bool {
	if board := Board(*p); len(board) > 0 && ev.Op == domain.DecisionRuleOp && ev.Outcome == domain.OutcomeDecided {
		return foldBoard(p, board, ev)
	}
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
