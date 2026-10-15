package methodology

import (
	"fmt"
	"strings"
)

// Persistence makes an agent persistent (ADR 0112): it has one instance per scope, a run the engine keeps and runs
// again in place for every new execution on that scope, so that one instance, with its conversation, handles the
// work from start to end (the review of the description of a change, an improvement cycle).
type Persistence struct {
	// Scope is what one instance is for: "change" (the default, one instance per change), or a path into the event
	// that starts the run ("event.process.methodology": one instance per observed methodology).
	Scope string `yaml:"scope,omitempty" json:"scope,omitempty"`
	// Until closes the instance: once a run of it completes with these conditions holding, the next execution on its
	// scope starts a new instance. Empty: the instance lives as long as its change.
	Until map[string]bool `yaml:"until,omitempty" json:"until,omitempty"`
	// Memory is the number of exchanges (prompt, answer) of its llm actions the instance keeps and gives back to the
	// model at its next call (default DefaultMemory, at most MaxMemory; 0: the default).
	Memory int `yaml:"memory,omitempty" json:"memory,omitempty"`
}

// Scopes and bounds of a persistent agent.
const (
	ScopeChange   = "change"
	DefaultMemory = 6
	MaxMemory     = 20
)

// Turns is the number of exchanges the instance keeps.
func (p *Persistence) Turns() int {
	switch {
	case p == nil:
		return 0
	case p.Memory <= 0:
		return DefaultMemory
	case p.Memory > MaxMemory:
		return MaxMemory
	}
	return p.Memory
}

// ScopePath is the path of the scope ("change" when none is given).
func (p *Persistence) ScopePath() string {
	if p == nil || p.Scope == "" {
		return ScopeChange
	}
	return p.Scope
}

// Resolve gives the value of the scope of a run on a change, with the event that starts it ("" when the path
// leads to nothing: the run is then not persistent).
func (p *Persistence) Resolve(change string, event map[string]any) string {
	path := p.ScopePath()
	if path == ScopeChange {
		return change
	}
	rest, ok := strings.CutPrefix(path, "event.")
	if !ok {
		return ""
	}
	var cur any = event
	for _, part := range strings.Split(rest, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = m[part]
	}
	switch v := cur.(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

// checkPersistence checks the persistence of an agent or a method at path.
func checkPersistence(add func(path, format string, args ...any), path string, p *Persistence, known map[string]bool) {
	if p == nil {
		return
	}
	if s := p.ScopePath(); s != ScopeChange {
		rest, ok := strings.CutPrefix(s, "event.")
		if !ok || rest == "" || strings.Contains(rest, "..") || strings.HasSuffix(rest, ".") {
			add(path+".persistent.scope", "scope must be %q or a path into the event (event.<field>...), got %q", ScopeChange, s)
		}
	}
	for c := range p.Until {
		if !known[c] {
			add(path+".persistent.until."+c, "unknown condition %q", c)
		}
	}
	if p.Memory < 0 || p.Memory > MaxMemory {
		add(path+".persistent.memory", "memory is the number of exchanges kept, from 0 (the default, %d) to %d", DefaultMemory, MaxMemory)
	}
}
