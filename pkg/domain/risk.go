package domain

import (
	"fmt"
	"slices"
	"strings"
)

// Risks and actions are facts of the change (ADR 0036 §1): an item of kind risk or action is a version of a record
// identified by its key; the register is the last version of each key among the items in effect, and the change log
// keeps every version with who wrote it.
const (
	KindRisk   ItemKind = "risk"
	KindAction ItemKind = "action"
)

// Risk statuses.
const (
	RiskOpen       = "open"
	RiskMitigating = "mitigating"
	RiskAccepted   = "accepted"
	RiskOccurred   = "occurred"
	RiskClosed     = "closed"
)

// Action statuses.
const (
	ActionOpen      = "open"
	ActionDone      = "done"
	ActionCancelled = "cancelled"
)

// RiskStatuses and ActionStatuses list the statuses a record may have.
var (
	RiskStatuses   = []string{RiskOpen, RiskMitigating, RiskAccepted, RiskOccurred, RiskClosed}
	ActionStatuses = []string{ActionOpen, ActionDone, ActionCancelled}
)

// Risk is the current version of a risk of the change.
type Risk struct {
	Key         string   `json:"key"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Probability int      `json:"probability"`
	Impact      int      `json:"impact"`
	Status      string   `json:"status"`
	Owner       string   `json:"owner,omitempty"`
	Actions     []string `json:"actions,omitempty"`
	Step        string   `json:"step,omitempty"`
	// Item is the item holding this version, By what produced it, Versions how many versions the key has.
	Item     ItemID `json:"item"`
	By       string `json:"by,omitempty"`
	Versions int    `json:"versions"`
}

// Score is probability × impact.
func (r Risk) Score() int { return r.Probability * r.Impact }

// Live reports whether the risk still needs attention (open or being mitigated).
func (r Risk) Live() bool { return r.Status == RiskOpen || r.Status == RiskMitigating }

// ActionItem is the current version of an action of the change.
type ActionItem struct {
	Key    string `json:"key"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Owner  string `json:"owner,omitempty"`
	Due    string `json:"due,omitempty"`
	// For is the risk key or the decision point the action answers.
	For      string `json:"for,omitempty"`
	Result   string `json:"result,omitempty"`
	Item     ItemID `json:"item"`
	By       string `json:"by,omitempty"`
	Versions int    `json:"versions"`
}

func dataString(d map[string]any, k string) string {
	s, _ := d[k].(string)
	return strings.TrimSpace(s)
}

func dataInt(d map[string]any, k string) int {
	switch v := d[k].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}

func dataStrings(d map[string]any, k string) []string {
	var out []string
	switch v := d[k].(type) {
	case []string:
		out = slices.Clone(v)
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// validateRecord checks the data of a risk or an action item.
func validateRecord(it ChangeItem) error {
	d := it.Data
	if dataString(d, "key") == "" || dataString(d, "title") == "" {
		return fmt.Errorf("%s item requires data.key and data.title", it.Kind)
	}
	status := dataString(d, "status")
	switch it.Kind {
	case KindRisk:
		if status != "" && !slices.Contains(RiskStatuses, status) {
			return fmt.Errorf("risk status must be one of %s", strings.Join(RiskStatuses, ", "))
		}
		for _, k := range []string{"probability", "impact"} {
			// absent: a version of the risk that does not restate it (ADR 0036); given, it is 1 to 5
			if raw, ok := d[k]; ok && raw != nil {
				if v := dataInt(d, k); v < 1 || v > 5 {
					return fmt.Errorf("risk %s must be between 1 and 5", k)
				}
			}
		}
	case KindAction:
		if status != "" && !slices.Contains(ActionStatuses, status) {
			return fmt.Errorf("action status must be one of %s", strings.Join(ActionStatuses, ", "))
		}
	}
	return nil
}

// registerOf folds the items of a kind in effect into the last version of each key, in the order the keys appeared.
func (c *Change) registerOf(kind ItemKind) (order []string, last map[string]ChangeItem, versions map[string]int) {
	last, versions = map[string]ChangeItem{}, map[string]int{}
	for _, it := range c.Items {
		if it.Kind != kind || !c.Active(it.ID) || it.Status == ItemRejected {
			continue
		}
		key := dataString(it.Data, "key")
		if key == "" {
			continue
		}
		if _, seen := last[key]; !seen {
			order = append(order, key)
		}
		prev := last[key]
		// a new version keeps what it does not restate
		merged := map[string]any{}
		for k, v := range prev.Data {
			merged[k] = v
		}
		for k, v := range it.Data {
			merged[k] = v
		}
		it.Data = merged
		last[key] = it
		versions[key]++
	}
	return order, last, versions
}

// Risks returns the risk register of the change: the current version of each risk, in the order they were raised.
func (c *Change) Risks() []Risk {
	order, last, versions := c.registerOf(KindRisk)
	out := make([]Risk, 0, len(order))
	for _, key := range order {
		it := last[key]
		d := it.Data
		r := Risk{Key: key, Title: dataString(d, "title"), Description: dataString(d, "description"), Probability: dataInt(d, "probability"),
			Impact: dataInt(d, "impact"), Status: dataString(d, "status"), Owner: dataString(d, "owner"), Actions: dataStrings(d, "actions"),
			Step: dataString(d, "step"), Item: it.ID, By: it.ProducedBy, Versions: versions[key]}
		if r.Status == "" {
			r.Status = RiskOpen
		}
		out = append(out, r)
	}
	return out
}

// ActionItems returns the actions of the change: the current version of each, in the order they were created.
func (c *Change) ActionItems() []ActionItem {
	order, last, versions := c.registerOf(KindAction)
	out := make([]ActionItem, 0, len(order))
	for _, key := range order {
		it := last[key]
		d := it.Data
		a := ActionItem{Key: key, Title: dataString(d, "title"), Status: dataString(d, "status"), Owner: dataString(d, "owner"),
			Due: dataString(d, "due"), For: dataString(d, "for"), Result: dataString(d, "result"), Item: it.ID, By: it.ProducedBy, Versions: versions[key]}
		if a.Status == "" {
			a.Status = ActionOpen
		}
		out = append(out, a)
	}
	return out
}
