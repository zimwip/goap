package form

import (
	"fmt"
	"reflect"
)

// Condition is a test on the value of another field of the form, which comes before it (ADR 0115, conditional
// fields): exactly one of Equals, In or Set. On a list value (a multiselect, several nodes) Equals and In hold when
// the list holds one of the values.
type Condition struct {
	// Field names the field tested: an earlier field of the same list of fields.
	Field string `yaml:"field" json:"field"`
	// Equals holds when the value is this one.
	Equals any `yaml:"equals,omitempty" json:"equals,omitempty"`
	// In holds when the value is one of these.
	In []any `yaml:"in,omitempty" json:"in,omitempty"`
	// Set holds when the field is filled in (true) or left blank (false).
	Set *bool `yaml:"set,omitempty" json:"set,omitempty"`
}

// Holds reports whether the condition holds on the value of its field (nil: left blank or hidden).
func (c Condition) Holds(v any) bool {
	if c.Set != nil {
		return empty(v) != *c.Set
	}
	if empty(v) {
		return false
	}
	want := c.In
	if c.Equals != nil {
		want = []any{c.Equals}
	}
	got := []any{normalize(v)}
	if l, ok := got[0].([]any); ok {
		got = l
	}
	for _, g := range got {
		for _, w := range want {
			if reflect.DeepEqual(g, normalize(w)) {
				return true
			}
		}
	}
	return false
}

// AllHold reports whether every condition holds on values (none: true).
func AllHold(cs []Condition, values map[string]any) bool {
	for _, c := range cs {
		if !c.Holds(values[c.Field]) {
			return false
		}
	}
	return true
}

// Visible reports whether a field is shown, checked and submitted given the values of the fields before it.
func (f Field) Visible(values map[string]any) bool { return AllHold(f.VisibleWhen, values) }

// RequiredFor reports whether a field must be filled in given the values of the fields before it.
func (f Field) RequiredFor(values map[string]any) bool {
	return f.Required || len(f.RequiredWhen) > 0 && AllHold(f.RequiredWhen, values)
}

// checkConditions reports the problems of the conditions of the j-th field of a list.
func checkConditions(fields []Field, j int, key string, cs []Condition, at string) Issues {
	var out Issues
	for i, c := range cs {
		p := fmt.Sprintf("%s.%s[%d]", at, key, i)
		n := 0
		if c.Equals != nil {
			n++
		}
		if c.In != nil {
			n++
		}
		if c.Set != nil {
			n++
		}
		if n != 1 {
			out = append(out, Issue{Path: p, Message: "a condition tests exactly one of equals, in or set"})
		}
		k := -1
		for x := range j {
			if fields[x].Name == c.Field {
				k = x
			}
		}
		if k < 0 {
			out = append(out, Issue{Path: p + ".field", Message: fmt.Sprintf("no field %q before this one", c.Field)})
		} else if w, ok := Lookup(fields[k].Widget); ok && (w.Value == ValueNone || w.Value == ValueLinks || w.Value == ValueObjects || w.Value == ValueObject) {
			out = append(out, Issue{Path: p + ".field", Message: fmt.Sprintf("field %q is a %s: a condition tests a value", c.Field, w.Name)})
		}
	}
	return out
}
