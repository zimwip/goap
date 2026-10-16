package risk

import (
	"fmt"

	"github.com/zimwip/goap/contracts/domain"
)

// TypeHypothesis is a statement a proof of concept or a prototype checks (ADR 0131): the requirement of the sub-change
// carrying it. Its verdict is an assessment of the subject item:<key> (confirmed or refuted, ADR 0109), not a field of
// its own; each item is a version of the record of its key, like a risk.
const TypeHypothesis domain.ItemType = "hypothesis"

// Hypothesis is the current version of a hypothesis of the change.
type Hypothesis struct {
	Key       string `json:"key"`
	Statement string `json:"statement"`
	// Source is what the hypothesis comes from: the key of a risk of the parent change (derisk), or "framing" (explore).
	Source   string        `json:"source,omitempty"`
	Item     domain.ItemID `json:"item"`
	By       string        `json:"by,omitempty"`
	Versions int           `json:"versions"`
}

func validateHypothesis(it domain.ChangeItem) error {
	if dataString(it.Data, "key") == "" || dataString(it.Data, "statement") == "" {
		return fmt.Errorf("hypothesis item requires data.key and data.statement")
	}
	return nil
}

// Hypotheses returns the hypotheses of the change: the current version of each, in the order they were stated.
func Hypotheses(c domain.Change) []Hypothesis {
	order, last, versions := registerOf(&c, TypeHypothesis)
	out := make([]Hypothesis, 0, len(order))
	for _, key := range order {
		it := last[key]
		out = append(out, Hypothesis{Key: key, Statement: dataString(it.Data, "statement"), Source: dataString(it.Data, "source"),
			Item: it.ID, By: it.ProducedBy, Versions: versions[key]})
	}
	return out
}
