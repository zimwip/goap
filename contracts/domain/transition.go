package domain

// A change that follows a lifecycle (ADR 0058) records every move of it as a KindTransition item of the main flow:
// the journal of its state, from which the phase an impact was written in is derived.

// StateMove is one entry of the journal of the state of a change.
type StateMove struct {
	// Item is the item that records the move.
	Item       ItemID
	Transition string
	From, To   string
	// Decision is the decision point that gated the move, when there was one.
	Decision string
	By       string
	// Freezes is what the move froze beyond its phase (ADR 0132 §4), recorded on the move.
	Freezes *Freezes
}

// StateMoves are the moves of the state of the change, in order.
func (c *Change) StateMoves() []StateMove {
	var out []StateMove
	for _, it := range c.Items {
		if it.Type != TypeTransition || it.Flow != "" {
			continue
		}
		str := func(k string) string { s, _ := it.Data[k].(string); return s }
		out = append(out, StateMove{Item: it.ID, Transition: str("transition"), From: str("from"), To: str("to"), Decision: str("decision"), By: it.ProducedBy,
			Freezes: freezesOf(it.Data["freezes"])})
	}
	return out
}

// freezesOf reads the freezes recorded on a move (a Freezes written in process, a map once read back as JSON).
func freezesOf(v any) *Freezes {
	switch f := v.(type) {
	case nil:
		return nil
	case *Freezes:
		return f
	case Freezes:
		return &f
	case map[string]any:
		out := &Freezes{Fields: stringList(f["fields"]), Types: stringList(f["types"])}
		out.Transitions, _ = f["transitions"].(bool)
		out.KeepPhase, _ = f["keepPhase"].(bool)
		return out
	}
	return nil
}

func stringList(v any) []string {
	switch l := v.(type) {
	case []string:
		return l
	case []any:
		out := make([]string, 0, len(l))
		for _, x := range l {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// Passed are the transitions of the lifecycle of the change that stand (ADR 0132 §1): taken, and the change has not
// gone back to the state they leave since. A gate of a methodology is passed while its transition stands.
func (c *Change) Passed() []string {
	var out []string
	for _, m := range c.StateMoves() {
		kept := out[:0]
		for _, t := range out {
			if from := c.moveFrom(t); from != m.To && t != m.Transition {
				kept = append(kept, t)
			}
		}
		out = append(kept, m.Transition)
	}
	return out
}

// ConsumedDecisions are the decision points a move of the lifecycle consumed: each gates one transition only.
func (c *Change) ConsumedDecisions() []string {
	var out []string
	for _, m := range c.StateMoves() {
		if m.Decision != "" {
			out = append(out, m.Decision)
		}
	}
	return out
}

// moveFrom is the state the last move of a transition left.
func (c *Change) moveFrom(transition string) string {
	from := ""
	for _, m := range c.StateMoves() {
		if m.Transition == transition {
			from = m.From
		}
	}
	return from
}

// InForceFreezes are the freezes of the moves that stand (Passed), in order.
func (c *Change) InForceFreezes() []Freezes {
	passed := map[string]bool{}
	for _, t := range c.Passed() {
		passed[t] = true
	}
	var out []Freezes
	seen := map[string]bool{}
	moves := c.StateMoves()
	for i := len(moves) - 1; i >= 0; i-- {
		m := moves[i]
		if !passed[m.Transition] || seen[m.Transition] || m.Freezes == nil {
			continue
		}
		seen[m.Transition] = true
		out = append([]Freezes{*m.Freezes}, out...)
	}
	return out
}
