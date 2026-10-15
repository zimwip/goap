package methodology

import (
	"fmt"
	"time"
)

// Loop is the budget of the tool loop of an llm action (ADR 0118): an action that works on a workspace (reads,
// edits, builds, tests) needs hundreds of tool rounds where an analysis needs a few. The platform bounds every value.
type Loop struct {
	// MaxRounds bounds the tool rounds (0: DefaultToolRounds, at most MaxLoopRounds).
	MaxRounds int `yaml:"maxRounds,omitempty" json:"maxRounds,omitempty"`
	// MaxToolResultBytes bounds a tool result given back to the model (0: the engine's bound, at most
	// MaxLoopToolResult); a longer one keeps its head and its tail.
	MaxToolResultBytes int `yaml:"maxToolResultBytes,omitempty" json:"maxToolResultBytes,omitempty"`
	// MaxTokens bounds the tokens of the whole loop, input and output of every call (0: no bound but the rounds).
	MaxTokens int `yaml:"maxTokens,omitempty" json:"maxTokens,omitempty"`
	// Timeout bounds the duration of the loop, a Go duration ("45m", "2h"; empty: no bound, at most MaxLoopTimeout).
	Timeout string `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	// CompactBytes is the size of the conversation past which the older tool results are elided (0: the engine's
	// threshold); the latest rounds stay whole.
	CompactBytes int `yaml:"compactBytes,omitempty" json:"compactBytes,omitempty"`
}

// Bounds of a loop.
const (
	DefaultToolRounds = 6
	MaxLoopRounds     = 500
	MaxLoopToolResult = 200_000
	MaxLoopTimeout    = 4 * time.Hour
	MaxLoopCompact    = 4 << 20
)

// Rounds is the number of tool rounds the loop allows.
func (l *Loop) Rounds() int {
	if l == nil || l.MaxRounds <= 0 {
		return DefaultToolRounds
	}
	return min(l.MaxRounds, MaxLoopRounds)
}

// Duration is the bound of the duration of the loop (0: none).
func (l *Loop) Duration() time.Duration {
	if l == nil || l.Timeout == "" {
		return 0
	}
	d, err := time.ParseDuration(l.Timeout)
	if err != nil || d <= 0 {
		return 0
	}
	return min(d, MaxLoopTimeout)
}

// issues are the problems of the loop of an action of the given kind, as messages.
func (l *Loop) issues(kind string) []string {
	if l == nil {
		return nil
	}
	var out []string
	if kind != KindLLM {
		out = append(out, "loop is the budget of the tool loop of an llm action")
	}
	check := func(name string, v, max int) {
		if v < 0 || v > max {
			out = append(out, fmt.Sprintf("loop.%s must be between 0 and %d", name, max))
		}
	}
	check("maxRounds", l.MaxRounds, MaxLoopRounds)
	check("maxToolResultBytes", l.MaxToolResultBytes, MaxLoopToolResult)
	check("compactBytes", l.CompactBytes, MaxLoopCompact)
	if l.MaxTokens < 0 {
		out = append(out, "loop.maxTokens must not be negative")
	}
	if l.Timeout != "" {
		d, err := time.ParseDuration(l.Timeout)
		switch {
		case err != nil:
			out = append(out, fmt.Sprintf("loop.timeout %q is not a duration (45m, 2h)", l.Timeout))
		case d <= 0 || d > MaxLoopTimeout:
			out = append(out, fmt.Sprintf("loop.timeout must be positive and at most %s", MaxLoopTimeout))
		}
	}
	return out
}
