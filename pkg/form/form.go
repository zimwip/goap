// Package form is the form of a human action (ADR 0115): the fields a person fills in, the widgets that render them
// and the constraints limiting their choices, checked when the methodology compiles (CheckForm) and when a person
// submits the values (Validate). It is a leaf package: it knows no graph, no engine and no methodology.
package form

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Form is what a person sees and fills in to carry out a human action.
type Form struct {
	Title       string `yaml:"title,omitempty" json:"title,omitempty"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// Submit is the label of the button sending the values.
	Submit string `yaml:"submit,omitempty" json:"submit,omitempty"`
	// Artifact is the type of the artifact recording the values when the action has no code (default: the action name).
	Artifact string  `yaml:"artifact,omitempty" json:"artifact,omitempty"`
	Fields   []Field `yaml:"fields,omitempty" json:"fields,omitempty"`
}

// Field is one input of a form, rendered by its widget.
type Field struct {
	// Name is the key of the value.
	Name        string `yaml:"name" json:"name"`
	Widget      string `yaml:"widget" json:"widget"`
	Label       string `yaml:"label,omitempty" json:"label,omitempty"`
	Help        string `yaml:"help,omitempty" json:"help,omitempty"`
	Placeholder string `yaml:"placeholder,omitempty" json:"placeholder,omitempty"`
	// Section groups fields, in order of first appearance.
	Section  string `yaml:"section,omitempty" json:"section,omitempty"`
	Required bool   `yaml:"required,omitempty" json:"required,omitempty"`
	Default  any    `yaml:"default,omitempty" json:"default,omitempty"`
	// Constraints limit the choices of the widget; their keys are the ones it declares.
	Constraints map[string]any `yaml:"constraints,omitempty" json:"constraints,omitempty"`
	// VisibleWhen: the field is shown, checked and submitted only while every condition holds (none: always).
	VisibleWhen []Condition `yaml:"visibleWhen,omitempty" json:"visibleWhen,omitempty"`
	// RequiredWhen: the field must be filled in while every condition holds (Required: always).
	RequiredWhen []Condition `yaml:"requiredWhen,omitempty" json:"requiredWhen,omitempty"`
	// Fields are the sub-fields of a group: each entry of its value is an object of their values.
	Fields []Field `yaml:"fields,omitempty" json:"fields,omitempty"`
}

// IsZero reports whether the form declares nothing.
func (f *Form) IsZero() bool {
	return f == nil || len(f.Fields) == 0 && f.Title == "" && f.Description == ""
}

// Field returns the field of that name.
func (f Form) Field(name string) (Field, bool) {
	for _, x := range f.Fields {
		if x.Name == name {
			return x, true
		}
	}
	return Field{}, false
}

// Issue is a problem of a form definition (Path relative to the form) or of a submitted value (Field).
type Issue struct {
	Path    string `json:"path,omitempty"`
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

func (i Issue) String() string {
	if i.Field != "" {
		return i.Field + ": " + i.Message
	}
	if i.Path != "" {
		return i.Path + ": " + i.Message
	}
	return i.Message
}

// Issues is a list of issues, an error when not empty.
type Issues []Issue

func (is Issues) Error() string {
	parts := make([]string, len(is))
	for i, x := range is {
		parts[i] = x.String()
	}
	return strings.Join(parts, "; ")
}

// NameRE is the shape of a field name.
var NameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// CheckForm reports the problems of a form definition: names, widgets known to the registry, constraints declared by
// the widget and of their type, the constraints a widget requires, the references between fields, the conditions
// (on an earlier field) and the sub-fields of a group (no group nor link field inside a group).
func CheckForm(f Form) Issues { return checkFields(f.Fields, "fields", false) }

func checkFields(fields []Field, path string, nested bool) Issues {
	var out Issues
	seen := map[string]bool{}
	for j, fd := range fields {
		at := fmt.Sprintf("%s[%d]", path, j)
		switch {
		case !NameRE.MatchString(fd.Name):
			out = append(out, Issue{Path: at + ".name", Message: fmt.Sprintf("invalid field name %q (a letter, then letters, digits or _)", fd.Name)})
		case seen[fd.Name]:
			out = append(out, Issue{Path: at + ".name", Message: fmt.Sprintf("duplicate field %q", fd.Name)})
		}
		seen[fd.Name] = true
		out = append(out, checkConditions(fields, j, "visibleWhen", fd.VisibleWhen, at)...)
		out = append(out, checkConditions(fields, j, "requiredWhen", fd.RequiredWhen, at)...)
		w, ok := Lookup(fd.Widget)
		if !ok {
			out = append(out, Issue{Path: at + ".widget", Message: fmt.Sprintf("unknown widget %q (known: %s)", fd.Widget, strings.Join(Names(), ", "))})
			continue
		}
		if nested && (w.Value == ValueObjects || w.Value == ValueLinks || w.Name == WidgetNewNode || w.Name == WidgetDecision) {
			out = append(out, Issue{Path: at + ".widget", Message: fmt.Sprintf("a group holds no %s field", w.Name)})
		}
		if w.Value == ValueObjects {
			if len(fd.Fields) == 0 {
				out = append(out, Issue{Path: at + ".fields", Message: "a group needs at least one sub-field"})
			}
			out = append(out, checkFields(fd.Fields, at+".fields", true)...)
		} else if len(fd.Fields) > 0 {
			out = append(out, Issue{Path: at + ".fields", Message: fmt.Sprintf("only a group has sub-fields, not a %s", w.Name)})
		}
		for _, k := range sortedKeys(fd.Constraints) {
			c, ok := w.constraint(k)
			if !ok {
				out = append(out, Issue{Path: at + ".constraints." + k, Message: fmt.Sprintf("widget %s has no constraint %q", w.Name, k)})
				continue
			}
			if msg := c.Type.check(fd.Constraints[k]); msg != "" {
				out = append(out, Issue{Path: at + ".constraints." + k, Message: msg})
				continue
			}
			if c.Type == ConstraintField {
				ref, _ := fd.Constraints[k].(string)
				other, ok := fieldOf(fields, ref)
				if !ok || ref == fd.Name {
					out = append(out, Issue{Path: at + ".constraints." + k, Message: fmt.Sprintf("no other field %q", ref)})
				} else if len(c.Widgets) > 0 && !contains(c.Widgets, other.Widget) {
					out = append(out, Issue{Path: at + ".constraints." + k, Message: fmt.Sprintf("field %q is a %s, not a %s", ref, other.Widget, strings.Join(c.Widgets, " or "))})
				}
			}
		}
		for _, c := range w.Constraints {
			if c.Required {
				if _, ok := fd.Constraints[c.Name]; !ok {
					out = append(out, Issue{Path: at + ".constraints", Message: fmt.Sprintf("widget %s needs the constraint %q", w.Name, c.Name)})
				}
			}
		}
		if w.Define != nil {
			for _, msg := range w.Define(fd) {
				out = append(out, Issue{Path: at, Message: msg})
			}
		}
		if fd.Default != nil && w.Value != ValueNone && w.Value != ValueLinks && w.Value != ValueObjects && w.Value != ValueObject {
			if is := w.check(fd, fd.Default, Env{}, nil); len(is) > 0 {
				out = append(out, Issue{Path: at + ".default", Message: is[0]})
			}
		}
	}
	return out
}

func fieldOf(fields []Field, name string) (Field, bool) {
	for _, x := range fields {
		if x.Name == name {
			return x, true
		}
	}
	return Field{}, false
}

// Validate checks the values a person submitted against the form and returns them cleaned (numbers as float64, lists
// as []any, the defaults of the fields left out, the links of a link field completed with their source, the entries of
// a group cleaned the same way): a value for a field the form does not declare, a missing required value, a value of
// the wrong kind or one its constraints exclude is an issue. A field its conditions hide is left out, whatever was
// sent for it. env resolves the node keys (nil Node: unchecked).
func Validate(f Form, values map[string]any, env Env) (map[string]any, Issues) {
	return validateFields(f, f.Fields, values, env)
}

func validateFields(f Form, fields []Field, values map[string]any, env Env) (map[string]any, Issues) {
	var out Issues
	clean := map[string]any{}
	for _, k := range sortedKeys(values) {
		if _, ok := fieldOf(fields, k); !ok {
			out = append(out, Issue{Field: k, Message: "not a field of the form"})
		}
	}
	for _, fd := range fields {
		w, ok := Lookup(fd.Widget)
		if !ok {
			out = append(out, Issue{Field: fd.Name, Message: fmt.Sprintf("unknown widget %q", fd.Widget)})
			continue
		}
		if w.Value == ValueNone || !fd.Visible(clean) {
			continue
		}
		v, ok := values[fd.Name]
		if !ok || empty(v) {
			v = fd.Default
		}
		if empty(v) {
			if fd.RequiredFor(clean) {
				out = append(out, Issue{Field: fd.Name, Message: "required"})
			}
			continue
		}
		v = normalize(v)
		switch w.Value {
		case ValueLinks:
			v = completeLinks(f, fd, v, values)
		case ValueObjects:
			var is Issues
			v, is = validateEntries(f, fd, v, env)
			out = append(out, is...)
		}
		for _, msg := range w.check(fd, v, env, values) {
			out = append(out, Issue{Field: fd.Name, Message: msg})
		}
		clean[fd.Name] = v
		if fd.Widget == WidgetNewNode {
			// the links of the fields after it may start from or go to the nodes it creates
			env = env.with(newNodesOf(fd, v))
		}
	}
	return clean, out
}

// validateEntries checks and cleans the entries of a group, each against its sub-fields.
func validateEntries(f Form, fd Field, v any, env Env) (any, Issues) {
	list, ok := v.([]any)
	if !ok {
		return v, nil
	}
	var out Issues
	entries := make([]any, len(list))
	for i, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			entries[i] = e
			continue
		}
		c, is := validateFields(f, fd.Fields, m, env)
		for _, x := range is {
			out = append(out, Issue{Field: fd.Name, Message: fmt.Sprintf("entry %d, %s: %s", i+1, x.Field, x.Message)})
		}
		entries[i] = c
	}
	return entries, out
}

// empty reports a value the person left blank.
func empty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(x) == ""
	case []any:
		return len(x) == 0
	case []string:
		return len(x) == 0
	case []map[string]any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	}
	return false
}

// normalize gives a value the shapes of decoded JSON.
func normalize(v any) any {
	switch x := v.(type) {
	case int:
		return float64(x)
	case int32:
		return float64(x)
	case int64:
		return float64(x)
	case float32:
		return float64(x)
	case []string:
		out := make([]any, len(x))
		for i, s := range x {
			out[i] = s
		}
		return out
	case []map[string]any:
		out := make([]any, len(x))
		for i, m := range x {
			out[i] = m
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalize(e)
		}
		return out
	}
	return v
}

// keysOf reads the node keys of the value of a node or new node field: a key, keys, a new node {key}, new nodes.
func keysOf(v any) []string {
	switch x := normalize(v).(type) {
	case string:
		if x != "" {
			return []string{x}
		}
	case map[string]any:
		if k, _ := x["key"].(string); k != "" {
			return []string{k}
		}
	case []any:
		var out []string
		for _, e := range x {
			out = append(out, keysOf(e)...)
		}
		return out
	}
	return nil
}

// NewNode is a node a new node field asks the change to create.
type NewNode struct {
	Field string         `json:"field"`
	Key   string         `json:"key"`
	Type  string         `json:"type"`
	Props map[string]any `json:"props,omitempty"`
}

// NewNodes lists the nodes the new node fields of validated values create, in the order of the fields.
func NewNodes(f Form, values map[string]any) []NewNode {
	var out []NewNode
	for _, fd := range f.Fields {
		if fd.Widget == WidgetNewNode {
			out = append(out, newNodesOf(fd, values[fd.Name])...)
		}
	}
	return out
}

func newNodesOf(fd Field, v any) []NewNode {
	var out []NewNode
	for _, m := range objectsOf(v) {
		key, _ := m["key"].(string)
		typ, _ := m["type"].(string)
		props, _ := m["props"].(map[string]any)
		if key != "" && typ != "" {
			out = append(out, NewNode{Field: fd.Name, Key: key, Type: typ, Props: props})
		}
	}
	return out
}

// Ruling is the ruling of a decision point a decision field asks for.
type Ruling struct {
	Field         string `json:"field"`
	Point         string `json:"point"`
	Option        string `json:"option"`
	Justification string `json:"justification,omitempty"`
}

// Rulings lists the rulings of the decision fields of validated values, in the order of the fields.
func Rulings(f Form, values map[string]any) []Ruling {
	var out []Ruling
	for _, fd := range f.Fields {
		if fd.Widget != WidgetDecision {
			continue
		}
		for _, m := range objectsOf(values[fd.Name]) {
			p, _ := m["point"].(string)
			o, _ := m["option"].(string)
			j, _ := m["justification"].(string)
			if p != "" && o != "" {
				out = append(out, Ruling{Field: fd.Name, Point: p, Option: o, Justification: j})
			}
		}
	}
	return out
}

// objectsOf reads an object, or a list of objects.
func objectsOf(v any) []map[string]any {
	switch x := v.(type) {
	case map[string]any:
		return []map[string]any{x}
	case []any:
		var out []map[string]any
		for _, e := range x {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

// Link is a link a link field asks for.
type Link struct {
	Field string `json:"field"`
	From  string `json:"from"`
	Type  string `json:"type"`
	To    string `json:"to"`
}

// Links lists the links of the link fields of validated values, in the order of the fields.
func Links(f Form, values map[string]any) []Link {
	var out []Link
	for _, fd := range f.Fields {
		if fd.Widget != WidgetLink {
			continue
		}
		list, _ := values[fd.Name].([]any)
		for _, e := range list {
			m, _ := e.(map[string]any)
			from, _ := m["from"].(string)
			typ, _ := m["type"].(string)
			to, _ := m["to"].(string)
			if from != "" && typ != "" && to != "" {
				out = append(out, Link{Field: fd.Name, From: from, Type: typ, To: to})
			}
		}
	}
	return out
}

// completeLinks gives the links of a link field their source and their type when the field fixes them.
func completeLinks(f Form, fd Field, v any, values map[string]any) any {
	list, ok := v.([]any)
	if !ok {
		return v
	}
	from := linkSource(f, fd, values)
	var typ string
	if lt := stringsOf(fd.Constraints["linkTypes"]); len(lt) == 1 {
		typ = lt[0]
	}
	out := make([]any, len(list))
	for i, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			out[i] = e
			continue
		}
		c := make(map[string]any, len(m)+2)
		for k, x := range m {
			c[k] = x
		}
		if s, _ := c["from"].(string); s == "" && from != "" {
			c["from"] = from
		}
		if s, _ := c["type"].(string); s == "" && typ != "" {
			c["type"] = typ
		}
		out[i] = c
	}
	return out
}

// linkSource is the source a link field fixes: its fixed key, else the single node of its from field.
func linkSource(f Form, fd Field, values map[string]any) string {
	if k, _ := fd.Constraints["fromKey"].(string); k != "" {
		return k
	}
	ref, _ := fd.Constraints["from"].(string)
	if ref == "" {
		return ""
	}
	if keys := keysOf(values[ref]); len(keys) == 1 {
		return keys[0]
	} else if len(keys) > 1 {
		return ""
	}
	if fdRef, ok := f.Field(ref); ok {
		if s, ok := fdRef.Default.(string); ok {
			return s
		}
	}
	return ""
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// stringsOf reads a list of strings of a constraint (nil when it is not one).
func stringsOf(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			s, ok := e.(string)
			if !ok {
				return nil
			}
			out = append(out, s)
		}
		return out
	}
	return nil
}

func contains(list []string, s string) bool { return slices.Contains(list, s) }
