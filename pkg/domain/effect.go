package domain

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Effects on the blackboard (ADR 0107): a named value attached to a change impact or to an item of the change, such as
// "formulated" on an impact or "assessed" on a risk, so what the work established about a subject is a fact of the
// blackboard rather than a convention of the item data. Each is set by an item of kind KindEffect (an EffectMark) and
// the latest mark of a name wins; a null value clears it. An item that is a version of a keyed record (data.key: a
// risk, an action, a waiver...) carries its effects for the whole record, every version alike.

// KindEffect is the kind of the items that set effects on a subject of the change (built in, ADR 0107).
const KindEffect ItemKind = "effect"

// Kinds of the subject of an effect.
const (
	EffectOnImpact = "impact" // a change impact, by its id
	EffectOnItem   = "item"   // an item of the change, by its id (its record when it is a version of one)
)

var effectName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// EffectMark is the data of an effect item: the subject (Subject the kind, ID its id) and the effects it sets, by name;
// a nil value clears the effect.
type EffectMark struct {
	Subject string
	ID      string
	Effects map[string]any
}

// Data is the data of the item holding the mark.
func (m EffectMark) Data() map[string]any {
	return map[string]any{"subject": m.Subject, "id": m.ID, "effects": maps.Clone(m.Effects)}
}

// Item is a proposed effect item holding the mark.
func (m EffectMark) Item() ChangeItem {
	return ChangeItem{Kind: KindEffect, Status: ItemProposed, Data: m.Data()}
}

// EffectMarkOf reads the mark of an effect item's data.
func EffectMarkOf(data map[string]any) EffectMark {
	m := EffectMark{}
	m.Subject, _ = data["subject"].(string)
	m.ID, _ = data["id"].(string)
	if e, ok := data["effects"].(map[string]any); ok {
		m.Effects = e
	}
	return m
}

// Validate checks the shape of a mark: a known subject kind, an id, at least one effect, names usable as CEL fields and
// scalar values (bool, string, number) or null.
func (m EffectMark) Validate() error {
	if m.Subject != EffectOnImpact && m.Subject != EffectOnItem {
		return fmt.Errorf("effect item: subject must be %q or %q, not %q", EffectOnImpact, EffectOnItem, m.Subject)
	}
	if strings.TrimSpace(m.ID) == "" {
		return fmt.Errorf("effect item requires data.id")
	}
	if len(m.Effects) == 0 {
		return fmt.Errorf("effect item requires data.effects")
	}
	for name, v := range m.Effects {
		if !effectName.MatchString(name) {
			return fmt.Errorf("effect item: name %q is not an identifier", name)
		}
		switch v.(type) {
		case nil, bool, string, float64, float32, int, int32, int64:
		default:
			return fmt.Errorf("effect item: effect %s is a %T, not a bool, string or number", name, v)
		}
	}
	return nil
}

// Effect is the value of an effect on its subject and the item that set it last.
type Effect struct {
	Name  string `json:"name"`
	Value any    `json:"value"`
	// Item is the effect item that set it, On the version of the record (or the item) it was set on.
	Item       ItemID    `json:"item"`
	On         ItemID    `json:"on,omitempty"`
	ProducedBy string    `json:"producedBy,omitempty"`
	Execution  string    `json:"execution,omitempty"`
	At         time.Time `json:"at"`
}

// EffectIndex is the effects of a change by subject, a fold of its active effect items.
type EffectIndex struct {
	bySubject map[string]map[string]Effect
	items     map[ItemID]ChangeItem
}

// RecordKey is the identity an item shares with the other versions of its record (kind and data.key), "" when the item
// is no version of a keyed record.
func RecordKey(it ChangeItem) string {
	k, _ := it.Data["key"].(string)
	if k == "" {
		return ""
	}
	return string(it.Kind) + "/" + k
}

func impactSubject(id ChangeImpactID) string { return "impact:" + string(id) }
func recordSubject(kind ItemKind, key string) string {
	return "record:" + string(kind) + "/" + key
}

// subjectOf is the key of the subject an item carries effects for: its record, else itself.
func subjectOf(it ChangeItem) string {
	if r := RecordKey(it); r != "" {
		return "record:" + r
	}
	return "item:" + string(it.ID)
}

// Effects folds the effect items of the change in force (superseded and candidate ones left out), in their order.
func (c *Change) Effects() EffectIndex {
	x := EffectIndex{bySubject: map[string]map[string]Effect{}, items: make(map[ItemID]ChangeItem, len(c.Items))}
	for _, it := range c.Items {
		x.items[it.ID] = it
	}
	for _, it := range c.Items {
		if it.Kind != KindEffect || !c.Active(it.ID) {
			continue
		}
		m := EffectMarkOf(it.Data)
		var subject string
		var on ItemID
		switch m.Subject {
		case EffectOnImpact:
			subject = impactSubject(ChangeImpactID(m.ID))
		case EffectOnItem:
			target, ok := x.items[ItemID(m.ID)]
			if !ok {
				continue
			}
			subject, on = subjectOf(target), target.ID
		default:
			continue
		}
		for _, name := range slices.Sorted(maps.Keys(m.Effects)) {
			v := m.Effects[name]
			if v == nil {
				delete(x.bySubject[subject], name)
				continue
			}
			if x.bySubject[subject] == nil {
				x.bySubject[subject] = map[string]Effect{}
			}
			x.bySubject[subject][name] = Effect{Name: name, Value: v, Item: it.ID, On: on, ProducedBy: it.ProducedBy, Execution: it.Execution, At: it.CreatedAt}
		}
	}
	return x
}

// OfImpact are the effects of a change impact, by name (nil when none).
func (x EffectIndex) OfImpact(id ChangeImpactID) map[string]Effect {
	return x.bySubject[impactSubject(id)]
}

// OfItem are the effects of an item, by name: those of its record when it is a version of one (nil when none).
func (x EffectIndex) OfItem(it ChangeItem) map[string]Effect { return x.bySubject[subjectOf(it)] }

// OfRecord are the effects of a keyed record (a risk, an action...) by its kind and key, by name (nil when none).
func (x EffectIndex) OfRecord(kind ItemKind, key string) map[string]Effect {
	return x.bySubject[recordSubject(kind, key)]
}

// EffectValues are the values of effects, by name (empty, never nil).
func EffectValues(es map[string]Effect) map[string]any {
	out := make(map[string]any, len(es))
	for n, e := range es {
		out[n] = e.Value
	}
	return out
}
