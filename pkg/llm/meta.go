package llm

import (
	"context"
	"strings"
	"unicode"
)

// CallMeta says who asked for an LLM call and for what, for the ledger of the gateway (ADR 0089). It is accounting
// only: declared by the caller, never an authorisation input (the subject and the project of a call are the
// principal's, taken by the gateway). The ledger stores no prompt.
type CallMeta struct {
	// Source is who asked: engine, assistant, helper, indexer, intent... (lower case, at most MaxSourceLen).
	Source         string
	ConversationID string
	ProcessID      string
	ChangeID       string
	// Step and Call locate the call of a process: the step of the process and the position of the call in the calls of
	// that step, as the journal numbers them (ADR 0059: the exchange of the call is the model.call entry of the change
	// log of that process, step and call). They mean something only with a ProcessID; otherwise they are NoStep.
	Step   int
	Call   int
	Action string
	Agent  string
}

// NoStep is the Step and the Call of a call that belongs to no process.
const NoStep = -1

// Caps of the declared values (bytes).
const (
	MaxSourceLen = 40
	MaxIDLen     = 128
	MaxNameLen   = 200
)

// The sources of the platform's own callers; a source is free-form (see Clean), these are the ones the ledger's views know.
const (
	SourceEngine    = "engine"
	SourceAssistant = "assistant"
	SourceHelper    = "helper"
	SourceIndexer   = "indexer"
	SourceIntent    = "intent"
)

// SourceCalibration is the source of the gateway's own calls that measure what a behaviour costs on a model (ADR 0093,
// "Measured cost"): set by the gateway only, and never a scope of a behaviour.
const SourceCalibration = "calibration"

// SourceOther is the source of a call that declares none, or an invalid one.
const SourceOther = "other"

type metaKey struct{}

// WithMeta returns a context carrying the meta of the calls made with it.
func WithMeta(ctx context.Context, m CallMeta) context.Context {
	return context.WithValue(ctx, metaKey{}, m)
}

// MetaFrom returns the meta of ctx (the zero CallMeta, cleaned, when there is none).
func MetaFrom(ctx context.Context) CallMeta {
	m, _ := ctx.Value(metaKey{}).(CallMeta)
	return m.Clean()
}

// Clean returns the meta with its values capped and sanitised: a source outside [a-z0-9._:-] (after lower-casing) is
// SourceOther, the identifiers lose their control characters, Step and Call are NoStep without a process.
func (m CallMeta) Clean() CallMeta {
	m.Source = strings.ToLower(strings.TrimSpace(m.Source))
	if m.Source == "" || len(m.Source) > MaxSourceLen || strings.ContainsFunc(m.Source, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == ':' || r == '-')
	}) {
		m.Source = SourceOther
	}
	m.ConversationID = clip(m.ConversationID, MaxIDLen)
	m.ProcessID = clip(m.ProcessID, MaxIDLen)
	m.ChangeID = clip(m.ChangeID, MaxIDLen)
	m.Action = clip(m.Action, MaxNameLen)
	m.Agent = clip(m.Agent, MaxNameLen)
	if m.ProcessID == "" || m.Step < 0 || m.Call < 0 {
		m.Step, m.Call = NoStep, NoStep
	}
	return m
}

// clip drops the control characters of s and cuts it to n bytes (on a character boundary).
func clip(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}
