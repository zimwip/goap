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
}

// StateMoves are the moves of the state of the change, in order.
func (c *Change) StateMoves() []StateMove {
	var out []StateMove
	for _, it := range c.Items {
		if it.Kind != KindTransition || it.Flow != "" {
			continue
		}
		str := func(k string) string { s, _ := it.Data[k].(string); return s }
		out = append(out, StateMove{Item: it.ID, Transition: str("transition"), From: str("from"), To: str("to"), Decision: str("decision"), By: it.ProducedBy})
	}
	return out
}
