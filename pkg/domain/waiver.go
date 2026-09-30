package domain

import (
	"fmt"
	"strings"
)

// KindWaiver is a person's decision to unblock a run (ADR 0036 §3): a process waiting for conditions established
// outside it, or stuck, is never left without a way out; whoever answers for it (the initiator of the run, the
// accountable role of its step, an administrator) may declare conditions established for that run. The waiver is a
// fact of the change like any other: who decided, why, for which run.
//
// Data: process (the run it applies to), conditions ("name" established true, "!name" established false), reason.
const KindWaiver ItemKind = "waiver"

func validateWaiver(it ChangeItem) error {
	if s, _ := it.Data["process"].(string); s == "" {
		return fmt.Errorf("waiver item requires data.process")
	}
	if s, _ := it.Data["reason"].(string); strings.TrimSpace(s) == "" {
		return fmt.Errorf("waiver item requires data.reason")
	}
	if len(waivedConditions(it)) == 0 {
		return fmt.Errorf("waiver item requires data.conditions")
	}
	return nil
}

func waivedConditions(it ChangeItem) []string {
	var out []string
	switch cs := it.Data["conditions"].(type) {
	case []string:
		out = cs
	case []any:
		for _, c := range cs {
			if s, ok := c.(string); ok && strings.TrimPrefix(s, "!") != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// Waivers returns the conditions people declared established for a run, the latest waiver of a condition winning.
func (c *Change) Waivers(process string) map[string]bool {
	var out map[string]bool
	for _, it := range c.Items {
		if it.Kind != KindWaiver || it.Data["process"] != process {
			continue
		}
		for _, cond := range waivedConditions(it) {
			if out == nil {
				out = map[string]bool{}
			}
			name, negated := strings.CutPrefix(cond, "!")
			out[name] = !negated
		}
	}
	return out
}
