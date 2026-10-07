package llmcfg

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/zimwip/goap/pkg/llm"
)

// NodeTypeBehavior is the node type of a global behaviour of the LLM calls (ADR 0093).
const NodeTypeBehavior = "platform@LlmBehavior"

// Positions of a behaviour in the system text of a call.
const (
	PositionPrepend = "prepend"
	PositionAppend  = "append"
)

// Caps of ADR 0093 (bytes).
const (
	// MaxInstruction caps the instruction of one behaviour.
	MaxInstruction = 4 << 10
	// MaxBehaviorBytes caps the text all the behaviours add to one call: the ones that do not fit are dropped (the lowest
	// priority last) and the call records them as skipped.
	MaxBehaviorBytes = 8 << 10
)

// KindComplete is the only kind of call a behaviour may apply to: an embedding has no instructions.
const KindComplete = "complete"

// JSONNotice wraps the instruction of a behaviour applied to a call that requires structured output, so that the style
// cannot change the format.
const JSONNotice = "These style rules apply to free-text fields only; the output format required by the other instructions is unchanged.\n"

// Sources a behaviour may be scoped to (the callers of llm.CallMeta the ledger knows).
func Sources() []string {
	return []string{llm.SourceEngine, llm.SourceAssistant, llm.SourceHelper, llm.SourceIndexer, llm.SourceIntent, llm.SourceOther}
}

// Behavior is an instruction the gateway adds to the system text of the calls that match its scope, so that a platform
// wide style (terse answers, a language, a tone) needs no change in any prompt. Every selector is optional and the
// selectors are ANDed; an empty one matches everything.
type Behavior struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Instruction is the text added to the instructions of the model (at most MaxInstruction bytes).
	Instruction string `json:"instruction"`
	Enabled     bool   `json:"enabled"`
	// Position is prepend or append (default append) relative to the system text of the request.
	Position string `json:"position,omitempty"`
	// Order sorts the behaviours, lower first, then by name.
	Order int `json:"order,omitempty"`
	// Aliases limits it to the calls requesting one of these aliases ("default" for a call naming none).
	Aliases []string `json:"aliases,omitempty"`
	// Models limits it to the calls served by one of these provider/model.
	Models []string `json:"models,omitempty"`
	// Sources limits it to the calls of these callers (llm.CallMeta.Source).
	Sources []string `json:"sources,omitempty"`
	// Kinds limits it to these kinds of call; only complete exists (a behaviour never applies to an embedding).
	Kinds []string `json:"kinds,omitempty"`
	// AppliesToJSON lets it apply to the calls that require a JSON answer (default: it does not, so it cannot break a protocol).
	AppliesToJSON bool `json:"appliesToJSON,omitempty"`
}

// BehaviorKey is the key of the node of a behaviour.
func BehaviorKey(name string) string { return "LLB:" + name }

// Validate checks a behaviour.
func (b Behavior) Validate() error {
	if !ValidName(b.Name) {
		return fmt.Errorf("behavior name must be 1-40 characters of a-z, 0-9, - or _")
	}
	if strings.TrimSpace(b.Instruction) == "" {
		return fmt.Errorf("behavior %s: instruction required", b.Name)
	}
	if len(b.Instruction) > MaxInstruction {
		return fmt.Errorf("behavior %s: instruction is %d bytes, at most %d", b.Name, len(b.Instruction), MaxInstruction)
	}
	switch b.Position {
	case "", PositionPrepend, PositionAppend:
	default:
		return fmt.Errorf("behavior %s: position must be prepend or append", b.Name)
	}
	for _, a := range b.Aliases {
		if !ValidName(a) {
			return fmt.Errorf("behavior %s: alias %q is not a valid alias name", b.Name, a)
		}
	}
	for _, m := range b.Models {
		if _, _, ok := SplitTarget(m); !ok {
			return fmt.Errorf("behavior %s: model %q must be provider/model", b.Name, m)
		}
	}
	for _, s := range b.Sources {
		if !slices.Contains(Sources(), s) {
			return fmt.Errorf("behavior %s: source %q must be one of %s", b.Name, s, strings.Join(Sources(), ", "))
		}
	}
	for _, k := range b.Kinds {
		if k != KindComplete {
			return fmt.Errorf("behavior %s: kind %q: a behavior applies to complete calls only, never to embeddings", b.Name, k)
		}
	}
	return nil
}

// Props returns the properties of the LlmBehavior node.
func (b Behavior) Props() map[string]any { var m map[string]any; _ = viaJSON(b, &m); return m }

// BehaviorFromProps reads a behavior from the properties of its node.
func BehaviorFromProps(props map[string]any) (b Behavior, err error) {
	if err = viaJSON(props, &b); err != nil {
		return b, fmt.Errorf("behavior node: %w", err)
	}
	return b, b.Validate()
}

// TerseBehavior is the built-in example, disabled until an administrator turns it on: the ultra-terse answer style.
func TerseBehavior() Behavior {
	return Behavior{
		Name:        "terse",
		Description: "Ultra-terse answers (\"caveman\" style): fewer output tokens, same technical content",
		Instruction: "Answer tersely: drop articles, filler, pleasantries and hedging; fragments are fine; keep technical terms, code, identifiers and errors exact; never shorten security warnings or irreversible-action confirmations.",
		Enabled:     false,
		Position:    PositionAppend,
		Order:       100,
	}
}

// CallInfo is what a behaviour is matched against: the call as the gateway sees it.
type CallInfo struct {
	// Alias is the name requested ("default" when none, "" for a literal provider/model).
	Alias string
	// Provider and Model are the resolved target.
	Provider, Model string
	// Source is llm.CallMeta.Source.
	Source string
	// Kind is complete or embed.
	Kind string
	// JSON: the call requires a JSON answer.
	JSON bool
}

// Matches reports whether the behavior applies to the call (Enabled aside).
func (b Behavior) Matches(c CallInfo) bool {
	if c.Kind != KindComplete {
		return false
	}
	if c.JSON && !b.AppliesToJSON {
		return false
	}
	in := func(list []string, v string) bool { return len(list) == 0 || slices.Contains(list, v) }
	return in(b.Aliases, c.Alias) && in(b.Models, c.Provider+"/"+c.Model) && in(b.Sources, c.Source) && in(b.Kinds, c.Kind)
}

// Applied is the result of Apply.
type Applied struct {
	// System is the system text to send.
	System string
	// Names are the behaviours added, in the order they were applied (prepended ones first as they appear in the text).
	Names []string
	// Skipped are the matching behaviours dropped because the text added would pass MaxBehaviorBytes.
	Skipped []string
	// Bytes is the length of the text added (separators included).
	Bytes int
}

// Tokens estimates the tokens the behaviours added (four bytes a token): an estimate for the ledger, not a measure.
func (a Applied) Tokens() int { return (a.Bytes + 3) / 4 }

// Apply returns the system text of a call with the enabled behaviours that match it: sorted by order then name,
// prepended ones before the system text, appended ones after, separated by blank lines. A call that requires JSON gets
// only the behaviours flagged AppliesToJSON, each wrapped with JSONNotice. Nothing is added to an embedding.
func (s *Snapshot) Apply(system string, c CallInfo) Applied {
	out := Applied{System: system}
	if s == nil || c.Kind != KindComplete {
		return out
	}
	var match []Behavior
	for _, b := range s.Behaviors {
		if b.Enabled && b.Matches(c) {
			match = append(match, b)
		}
	}
	sort.SliceStable(match, func(i, j int) bool {
		if match[i].Order != match[j].Order {
			return match[i].Order < match[j].Order
		}
		return match[i].Name < match[j].Name
	})
	var pre, post []string
	for _, b := range match {
		text := strings.TrimSpace(b.Instruction)
		if c.JSON {
			text = JSONNotice + text
		}
		if out.Bytes+len(text)+2 > MaxBehaviorBytes {
			out.Skipped = append(out.Skipped, b.Name)
			continue
		}
		out.Bytes += len(text) + 2
		out.Names = append(out.Names, b.Name)
		if b.Position == PositionPrepend {
			pre = append(pre, text)
		} else {
			post = append(post, text)
		}
	}
	parts := append(append(pre, system), post...)
	parts = slices.DeleteFunc(parts, func(p string) bool { return strings.TrimSpace(p) == "" })
	out.System = strings.Join(parts, "\n\n")
	if len(out.Names) == 0 {
		out.System = system
	}
	return out
}
