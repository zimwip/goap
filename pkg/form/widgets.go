package form

import (
	"encoding/base64"
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// ValueKind is the shape of the value a widget produces.
type ValueKind string

// Value kinds.
const (
	ValueString  ValueKind = "string"
	ValueNumber  ValueKind = "number"
	ValueBoolean ValueKind = "boolean"
	// ValueList is a list of strings.
	ValueList ValueKind = "list"
	// ValueNode is a node key, or a list of keys when the field allows several.
	ValueNode ValueKind = "node"
	// ValueLinks is a list of links {from, type, to}.
	ValueLinks ValueKind = "links"
	// ValueObject is an object, or a list of objects when the field allows several.
	ValueObject ValueKind = "object"
	// ValueObjects is a list of objects, the values of the sub-fields of a group.
	ValueObjects ValueKind = "objects"
	// ValueNone: a display widget, it produces no value.
	ValueNone ValueKind = "none"
	// ValueAny: any JSON value, checked by the widget only.
	ValueAny ValueKind = "any"
)

// ConstraintType is the type of the value of a constraint.
type ConstraintType string

// Constraint types.
const (
	ConstraintString  ConstraintType = "string"
	ConstraintNumber  ConstraintType = "number"
	ConstraintBoolean ConstraintType = "boolean"
	ConstraintStrings ConstraintType = "strings"
	// ConstraintField names another field of the form.
	ConstraintField ConstraintType = "field"
)

func (t ConstraintType) check(v any) string {
	switch t {
	case ConstraintString, ConstraintField:
		if s, ok := v.(string); !ok || s == "" {
			return "expects a non-empty string"
		}
	case ConstraintNumber:
		if _, ok := number(v); !ok {
			return "expects a number"
		}
	case ConstraintBoolean:
		if _, ok := v.(bool); !ok {
			return "expects true or false"
		}
	case ConstraintStrings:
		if l := stringsOf(v); l == nil {
			return "expects a list of strings"
		}
	}
	return ""
}

// Constraint is a constraint a widget accepts.
type Constraint struct {
	Name        string         `json:"name"`
	Type        ConstraintType `json:"type"`
	Description string         `json:"description,omitempty"`
	Required    bool           `json:"required,omitempty"`
	// Widgets are the widgets the field named by a ConstraintField may have (none: any).
	Widgets []string `json:"widgets,omitempty"`
}

// Node is what a form needs to know of a node a field names.
type Node struct {
	Key       string
	Type      string
	State     string
	Namespace string
	// InChange: the change touches the node (an impact of the change).
	InChange bool
}

// Env resolves what the checks of the values need.
type Env struct {
	// Node finds a node by key (nil: the nodes are not checked).
	Node func(key string) (Node, bool)
	// IsA reports whether a type is the other one or one of its subtypes (nil: equality).
	IsA func(typ, super string) bool
	// Decision finds a decision point of the change by id (nil: the points are not checked).
	Decision func(id string) (Decision, bool)
}

// Decision is what a form needs to know of a decision point of the change.
type Decision struct {
	ID       string
	Question string
	// Options are the options the point is about (ids; none: any option).
	Options []string
	// Decided: the point is settled, nobody rules it any more.
	Decided bool
}

// with adds the nodes a form creates to the nodes the environment knows.
func (e Env) with(created []NewNode) Env {
	if e.Node == nil || len(created) == 0 {
		return e
	}
	known := e.Node
	byKey := map[string]Node{}
	for _, n := range created {
		byKey[n.Key] = Node{Key: n.Key, Type: n.Type, InChange: true}
	}
	e.Node = func(k string) (Node, bool) {
		if n, ok := byKey[k]; ok {
			return n, true
		}
		return known(k)
	}
	return e
}

func (e Env) isA(typ, super string) bool {
	if e.IsA != nil {
		return e.IsA(typ, super)
	}
	return typ == super
}

// Widget is a kind of field: its value and its constraints. The web renders it with the component registered under
// the same name.
type Widget struct {
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	Value       ValueKind    `json:"value"`
	Constraints []Constraint `json:"constraints,omitempty"`
	// Define checks a field of the widget beyond its constraints' types (nil: nothing more).
	Define func(f Field) []string `json:"-"`
	// Check checks a value of the right kind against the constraints of the field (nil: nothing more).
	Check func(f Field, v any, env Env, values map[string]any) []string `json:"-"`
}

func (w Widget) constraint(name string) (Constraint, bool) {
	for _, c := range w.Constraints {
		if c.Name == name {
			return c, true
		}
	}
	return Constraint{}, false
}

// check checks a value: its kind, then the widget's own check.
func (w Widget) check(f Field, v any, env Env, values map[string]any) []string {
	v = normalize(v)
	if msg := kindIssue(w.Value, f, v); msg != "" {
		return []string{msg}
	}
	if w.Check != nil {
		return w.Check(f, v, env, values)
	}
	return nil
}

func kindIssue(k ValueKind, f Field, v any) string {
	switch k {
	case ValueString:
		if _, ok := v.(string); !ok {
			return "expects a text"
		}
	case ValueNumber:
		if _, ok := number(v); !ok {
			return "expects a number"
		}
	case ValueBoolean:
		if _, ok := v.(bool); !ok {
			return "expects true or false"
		}
	case ValueList:
		if stringsOf(v) == nil {
			return "expects a list of texts"
		}
	case ValueNode:
		if multiple(f) {
			if stringsOf(v) == nil {
				return "expects a list of node keys"
			}
		} else if _, ok := v.(string); !ok {
			return "expects a node key"
		}
	case ValueObject:
		if multiple(f) {
			list, ok := v.([]any)
			if !ok {
				return "expects a list of objects"
			}
			for _, e := range list {
				if _, ok := e.(map[string]any); !ok {
					return "expects a list of objects"
				}
			}
		} else if _, ok := v.(map[string]any); !ok {
			return "expects an object"
		}
	case ValueObjects:
		list, ok := v.([]any)
		if !ok {
			return "expects a list of entries"
		}
		for _, e := range list {
			if _, ok := e.(map[string]any); !ok {
				return "expects a list of entries {field: value}"
			}
		}
	case ValueLinks:
		list, ok := v.([]any)
		if !ok {
			return "expects a list of links"
		}
		for _, e := range list {
			if _, ok := e.(map[string]any); !ok {
				return "expects a list of links {from, type, to}"
			}
		}
	}
	return ""
}

var (
	mu       sync.RWMutex
	registry = map[string]Widget{}
)

// Register adds a widget to the registry; a duplicate or nameless widget panics.
func Register(w Widget) {
	mu.Lock()
	defer mu.Unlock()
	if w.Name == "" {
		panic("form: a widget needs a name")
	}
	if _, dup := registry[w.Name]; dup {
		panic(fmt.Sprintf("form: widget %q registered twice", w.Name))
	}
	if w.Value == "" {
		w.Value = ValueAny
	}
	registry[w.Name] = w
}

// Lookup finds a widget by name.
func Lookup(name string) (Widget, bool) {
	mu.RLock()
	defer mu.RUnlock()
	w, ok := registry[name]
	return w, ok
}

// Names lists the registered widgets, sorted.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// All lists the registered widgets, sorted by name.
func All() []Widget {
	out := []Widget{}
	for _, n := range Names() {
		w, _ := Lookup(n)
		out = append(out, w)
	}
	return out
}

// Names of the built-in widgets.
const (
	WidgetText        = "text"
	WidgetTextarea    = "textarea"
	WidgetNumber      = "number"
	WidgetBoolean     = "boolean"
	WidgetDate        = "date"
	WidgetSelect      = "select"
	WidgetMultiselect = "multiselect"
	WidgetNode        = "node"
	WidgetLink        = "link"
	WidgetMarkdown    = "markdown"
	WidgetGroup       = "group"
	WidgetNewNode     = "newNode"
	WidgetDecision    = "decision"
	WidgetFile        = "file"
)

// MaxFileBytes is the largest file a file field accepts: the file travels in the values and stays in the artifact.
const MaxFileBytes = 512 << 10

func init() {
	textConstraints := []Constraint{
		{Name: "minLength", Type: ConstraintNumber, Description: "fewest characters"},
		{Name: "maxLength", Type: ConstraintNumber, Description: "most characters"},
		{Name: "pattern", Type: ConstraintString, Description: "regular expression the whole text matches"},
	}
	for _, n := range []string{WidgetText, WidgetTextarea} {
		Register(Widget{Name: n, Value: ValueString, Constraints: textConstraints, Define: definePattern, Check: checkText,
			Description: map[string]string{WidgetText: "a line of text", WidgetTextarea: "a text of several lines"}[n]})
	}
	Register(Widget{Name: WidgetNumber, Description: "a number", Value: ValueNumber, Constraints: []Constraint{
		{Name: "min", Type: ConstraintNumber}, {Name: "max", Type: ConstraintNumber}, {Name: "step", Type: ConstraintNumber},
		{Name: "integer", Type: ConstraintBoolean, Description: "whole numbers only"},
	}, Check: checkNumber})
	Register(Widget{Name: WidgetBoolean, Description: "yes or no", Value: ValueBoolean})
	Register(Widget{Name: WidgetDate, Description: "a date, YYYY-MM-DD", Value: ValueString, Constraints: []Constraint{
		{Name: "min", Type: ConstraintString, Description: "earliest date, YYYY-MM-DD"},
		{Name: "max", Type: ConstraintString, Description: "latest date, YYYY-MM-DD"},
	}, Check: checkDate})
	options := Constraint{Name: "options", Type: ConstraintStrings, Required: true, Description: "the values offered"}
	Register(Widget{Name: WidgetSelect, Description: "one value among options", Value: ValueString, Constraints: []Constraint{options},
		Define: defineOptions, Check: checkSelect})
	Register(Widget{Name: WidgetMultiselect, Description: "values among options", Value: ValueList, Constraints: []Constraint{
		options, {Name: "min", Type: ConstraintNumber, Description: "fewest values"}, {Name: "max", Type: ConstraintNumber, Description: "most values"},
	}, Define: defineOptions, Check: checkMultiselect})
	Register(Widget{Name: WidgetNode, Description: "a node of the graph, found by a search", Value: ValueNode, Constraints: []Constraint{
		{Name: "types", Type: ConstraintStrings, Description: "qualified node types offered (a subtype qualifies)"},
		{Name: "states", Type: ConstraintStrings, Description: "lifecycle states offered"},
		{Name: "namespace", Type: ConstraintString, Description: "namespace of the nodes offered"},
		{Name: "inChange", Type: ConstraintBoolean, Description: "only the nodes the change touches"},
		{Name: "multiple", Type: ConstraintBoolean, Description: "several nodes"},
		{Name: "min", Type: ConstraintNumber, Description: "fewest nodes (multiple)"},
		{Name: "max", Type: ConstraintNumber, Description: "most nodes (multiple)"},
	}, Check: checkNode})
	Register(Widget{Name: WidgetLink, Description: "links to create from a node to nodes found by a search", Value: ValueLinks, Constraints: []Constraint{
		{Name: "from", Type: ConstraintField, Widgets: []string{WidgetNode, WidgetNewNode}, Description: "the node field (or new node field) whose node is the source"},
		{Name: "fromKey", Type: ConstraintString, Description: "a fixed source node"},
		{Name: "linkTypes", Type: ConstraintStrings, Description: "qualified link types offered"},
		{Name: "toTypes", Type: ConstraintStrings, Description: "qualified node types of the targets (a subtype qualifies)"},
		{Name: "toStates", Type: ConstraintStrings, Description: "lifecycle states of the targets"},
		{Name: "min", Type: ConstraintNumber, Description: "fewest links"},
		{Name: "max", Type: ConstraintNumber, Description: "most links"},
	}, Define: defineLink, Check: checkLinks})
	Register(Widget{Name: WidgetGroup, Description: "entries repeated by the person, each made of the sub-fields", Value: ValueObjects,
		Constraints: []Constraint{
			{Name: "min", Type: ConstraintNumber, Description: "fewest entries"},
			{Name: "max", Type: ConstraintNumber, Description: "most entries"},
		}, Check: func(f Field, v any, _ Env, _ map[string]any) []string {
			return checkCount(f, len(v.([]any)), "entries")
		}})
	Register(Widget{Name: WidgetNewNode, Description: "a node the change creates: its type, key and properties", Value: ValueObject, Constraints: []Constraint{
		{Name: "types", Type: ConstraintStrings, Description: "qualified node types the person may create (a subtype qualifies)"},
		{Name: "properties", Type: ConstraintStrings, Description: "the attributes asked (none: the name of the type)"},
		{Name: "multiple", Type: ConstraintBoolean, Description: "several nodes"},
		{Name: "min", Type: ConstraintNumber, Description: "fewest nodes (multiple)"},
		{Name: "max", Type: ConstraintNumber, Description: "most nodes (multiple)"},
	}, Check: checkNewNodes})
	Register(Widget{Name: WidgetDecision, Description: "rules a decision point of the change: an option and why", Value: ValueObject, Constraints: []Constraint{
		{Name: "justify", Type: ConstraintBoolean, Description: "the justification is required"},
	}, Check: checkDecision})
	Register(Widget{Name: WidgetFile, Description: "a file the person attaches, kept in the values", Value: ValueObject, Constraints: []Constraint{
		{Name: "accept", Type: ConstraintStrings, Description: "media types (text/*, application/pdf) or extensions (.csv) accepted"},
		{Name: "maxBytes", Type: ConstraintNumber, Description: fmt.Sprintf("largest file, in bytes (at most %d)", MaxFileBytes)},
		{Name: "multiple", Type: ConstraintBoolean, Description: "several files"},
		{Name: "min", Type: ConstraintNumber, Description: "fewest files (multiple)"},
		{Name: "max", Type: ConstraintNumber, Description: "most files (multiple)"},
	}, Check: checkFiles})
	Register(Widget{Name: WidgetMarkdown, Description: "a text shown to the person", Value: ValueNone, Constraints: []Constraint{
		{Name: "text", Type: ConstraintString, Required: true, Description: "the Markdown text"},
	}})
}

func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case int32:
		return float64(x), true
	}
	return 0, false
}

func multiple(f Field) bool { b, _ := f.Constraints["multiple"].(bool); return b }

func bound(f Field, name string) (float64, bool) { return number(f.Constraints[name]) }

func definePattern(f Field) []string {
	if p, ok := f.Constraints["pattern"].(string); ok {
		if _, err := regexp.Compile("^(?:" + p + ")$"); err != nil {
			return []string{fmt.Sprintf("invalid pattern: %v", err)}
		}
	}
	return nil
}

func defineOptions(f Field) []string {
	if o, ok := f.Constraints["options"]; ok && len(stringsOf(o)) == 0 {
		return []string{"options must list at least one value"}
	}
	return nil
}

func defineLink(f Field) []string {
	_, from := f.Constraints["from"]
	_, key := f.Constraints["fromKey"]
	switch {
	case from && key:
		return []string{"a link field names its source by from or by fromKey, not both"}
	case !from && !key:
		return []string{"a link field names its source: from (a node field) or fromKey"}
	}
	return nil
}

func checkText(f Field, v any, _ Env, _ map[string]any) []string {
	s := v.(string)
	n := len([]rune(s))
	var out []string
	if m, ok := bound(f, "minLength"); ok && float64(n) < m {
		out = append(out, fmt.Sprintf("at least %g characters", m))
	}
	if m, ok := bound(f, "maxLength"); ok && float64(n) > m {
		out = append(out, fmt.Sprintf("at most %g characters", m))
	}
	if p, ok := f.Constraints["pattern"].(string); ok {
		if re, err := regexp.Compile("^(?:" + p + ")$"); err == nil && !re.MatchString(s) {
			out = append(out, fmt.Sprintf("does not match %s", p))
		}
	}
	return out
}

func checkNumber(f Field, v any, _ Env, _ map[string]any) []string {
	x, _ := number(v)
	var out []string
	if m, ok := bound(f, "min"); ok && x < m {
		out = append(out, fmt.Sprintf("at least %g", m))
	}
	if m, ok := bound(f, "max"); ok && x > m {
		out = append(out, fmt.Sprintf("at most %g", m))
	}
	if b, _ := f.Constraints["integer"].(bool); b && x != float64(int64(x)) {
		out = append(out, "a whole number")
	}
	return out
}

func checkDate(f Field, v any, _ Env, _ map[string]any) []string {
	s := v.(string)
	if _, err := time.Parse(time.DateOnly, s); err != nil {
		return []string{"expects a date YYYY-MM-DD"}
	}
	var out []string
	if m, ok := f.Constraints["min"].(string); ok && s < m {
		out = append(out, "on or after "+m)
	}
	if m, ok := f.Constraints["max"].(string); ok && s > m {
		out = append(out, "on or before "+m)
	}
	return out
}

func checkSelect(f Field, v any, _ Env, _ map[string]any) []string {
	if s := v.(string); !contains(stringsOf(f.Constraints["options"]), s) {
		return []string{fmt.Sprintf("%q is not one of the options", s)}
	}
	return nil
}

func checkCount(f Field, n int, what string) []string {
	var out []string
	if m, ok := bound(f, "min"); ok && float64(n) < m {
		out = append(out, fmt.Sprintf("at least %g %s", m, what))
	}
	if m, ok := bound(f, "max"); ok && float64(n) > m {
		out = append(out, fmt.Sprintf("at most %g %s", m, what))
	}
	return out
}

func checkMultiselect(f Field, v any, _ Env, _ map[string]any) []string {
	opts := stringsOf(f.Constraints["options"])
	list := stringsOf(v)
	var out []string
	for _, s := range list {
		if !contains(opts, s) {
			out = append(out, fmt.Sprintf("%q is not one of the options", s))
		}
	}
	return append(out, checkCount(f, len(list), "values")...)
}

// nodeIssue checks one node key against type and state constraints.
func nodeIssue(key string, types, states []string, namespace string, inChange bool, env Env) string {
	if key == "" {
		return "empty node key"
	}
	if env.Node == nil {
		return ""
	}
	n, ok := env.Node(key)
	if !ok {
		return fmt.Sprintf("unknown node %q", key)
	}
	if len(types) > 0 {
		ok := false
		for _, t := range types {
			if env.isA(n.Type, t) {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Sprintf("%s is a %s, not one of %s", key, n.Type, strings.Join(types, ", "))
		}
	}
	if len(states) > 0 && !contains(states, n.State) {
		return fmt.Sprintf("%s is %s, not one of %s", key, stateName(n.State), strings.Join(states, ", "))
	}
	if namespace != "" && n.Namespace != namespace {
		return fmt.Sprintf("%s is not in the namespace %s", key, namespace)
	}
	if inChange && !n.InChange {
		return fmt.Sprintf("%s is not a node of the change", key)
	}
	return ""
}

func stateName(s string) string {
	if s == "" {
		return "in no state"
	}
	return "in the state " + s
}

func checkNode(f Field, v any, env Env, _ map[string]any) []string {
	keys := []string{}
	if multiple(f) {
		keys = stringsOf(v)
	} else {
		keys = append(keys, v.(string))
	}
	types, states := stringsOf(f.Constraints["types"]), stringsOf(f.Constraints["states"])
	ns, _ := f.Constraints["namespace"].(string)
	inChange, _ := f.Constraints["inChange"].(bool)
	var out []string
	seen := map[string]bool{}
	for _, k := range keys {
		if seen[k] {
			out = append(out, fmt.Sprintf("%s is given twice", k))
			continue
		}
		seen[k] = true
		if msg := nodeIssue(k, types, states, ns, inChange, env); msg != "" {
			out = append(out, msg)
		}
	}
	if multiple(f) {
		out = append(out, checkCount(f, len(keys), "nodes")...)
	}
	return out
}

func checkLinks(f Field, v any, env Env, values map[string]any) []string {
	list := v.([]any)
	linkTypes, toTypes, toStates := stringsOf(f.Constraints["linkTypes"]), stringsOf(f.Constraints["toTypes"]), stringsOf(f.Constraints["toStates"])
	var sources []string
	if k, _ := f.Constraints["fromKey"].(string); k != "" {
		sources = []string{k}
	} else if ref, _ := f.Constraints["from"].(string); ref != "" {
		sources = keysOf(values[ref])
	}
	var out []string
	seen := map[string]bool{}
	for i, e := range list {
		m := e.(map[string]any)
		from, _ := m["from"].(string)
		typ, _ := m["type"].(string)
		to, _ := m["to"].(string)
		at := fmt.Sprintf("link %d: ", i+1)
		switch {
		case from == "" || typ == "" || to == "":
			out = append(out, at+"needs from, type and to")
			continue
		case !contains(sources, from):
			if len(sources) == 0 {
				out = append(out, at+"no source node is chosen")
			} else {
				out = append(out, fmt.Sprintf("%ssource %s is not %s", at, from, strings.Join(sources, " or ")))
			}
			continue
		case len(linkTypes) > 0 && !contains(linkTypes, typ):
			out = append(out, fmt.Sprintf("%s%s is not one of %s", at, typ, strings.Join(linkTypes, ", ")))
			continue
		}
		if id := from + "|" + typ + "|" + to; seen[id] {
			out = append(out, at+"given twice")
			continue
		} else {
			seen[id] = true
		}
		if msg := nodeIssue(to, toTypes, toStates, "", false, env); msg != "" {
			out = append(out, at+msg)
		}
	}
	return append(out, checkCount(f, len(list), "links")...)
}

// keyRE is the shape of the key of a node a form creates.
var keyRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]*$`)

func checkNewNodes(f Field, v any, env Env, _ map[string]any) []string {
	entries := objectsOf(v)
	types, asked := stringsOf(f.Constraints["types"]), stringsOf(f.Constraints["properties"])
	var out []string
	seen := map[string]bool{}
	for i, m := range entries {
		at := ""
		if multiple(f) {
			at = fmt.Sprintf("node %d: ", i+1)
		}
		key, _ := m["key"].(string)
		typ, _ := m["type"].(string)
		switch {
		case typ == "":
			out = append(out, at+"needs a type")
		case len(types) > 0 && !slices.ContainsFunc(types, func(t string) bool { return env.isA(typ, t) }):
			out = append(out, fmt.Sprintf("%s%s is not one of %s", at, typ, strings.Join(types, ", ")))
		}
		switch {
		case key == "":
			out = append(out, at+"needs a key")
		case !keyRE.MatchString(key):
			out = append(out, fmt.Sprintf("%sinvalid key %q (letters, digits, _ . : -)", at, key))
		case seen[key]:
			out = append(out, fmt.Sprintf("%s%s is given twice", at, key))
		default:
			if env.Node != nil {
				if _, exists := env.Node(key); exists {
					out = append(out, fmt.Sprintf("%s%s already exists", at, key))
				}
			}
		}
		seen[key] = true
		if p, ok := m["props"]; ok {
			props, ok := p.(map[string]any)
			if !ok {
				out = append(out, at+"props is an object of properties")
			}
			for _, k := range sortedKeys(props) {
				if len(asked) > 0 && !contains(asked, k) {
					out = append(out, fmt.Sprintf("%s%s is not a property asked", at, k))
				}
			}
		}
		for _, k := range sortedKeys(m) {
			if k != "key" && k != "type" && k != "props" {
				out = append(out, fmt.Sprintf("%sunknown entry %q", at, k))
			}
		}
	}
	if multiple(f) {
		out = append(out, checkCount(f, len(entries), "nodes")...)
	}
	return out
}

func checkDecision(f Field, v any, env Env, _ map[string]any) []string {
	m := v.(map[string]any)
	point, _ := m["point"].(string)
	option, _ := m["option"].(string)
	why, _ := m["justification"].(string)
	var out []string
	if point == "" {
		out = append(out, "needs the decision point")
	}
	if option == "" {
		out = append(out, "needs an option")
	}
	if b, _ := f.Constraints["justify"].(bool); b && strings.TrimSpace(why) == "" {
		out = append(out, "needs a justification")
	}
	if len(out) > 0 || env.Decision == nil {
		return out
	}
	d, ok := env.Decision(point)
	switch {
	case !ok:
		out = append(out, fmt.Sprintf("unknown decision point %q", point))
	case d.Decided:
		out = append(out, fmt.Sprintf("the decision point %q is already decided", d.Question))
	case len(d.Options) > 0 && !contains(d.Options, option):
		out = append(out, fmt.Sprintf("%q is not an option of %q", option, d.Question))
	}
	return out
}

func checkFiles(f Field, v any, _ Env, _ map[string]any) []string {
	entries := objectsOf(v)
	limit := float64(MaxFileBytes)
	if m, ok := bound(f, "maxBytes"); ok && m < limit {
		limit = m
	}
	accept := stringsOf(f.Constraints["accept"])
	var out []string
	for i, m := range entries {
		at := ""
		if multiple(f) {
			at = fmt.Sprintf("file %d: ", i+1)
		}
		name, _ := m["name"].(string)
		typ, _ := m["type"].(string)
		content, _ := m["content"].(string)
		raw, err := base64.StdEncoding.DecodeString(content)
		switch {
		case name == "":
			out = append(out, at+"needs a name")
		case err != nil:
			out = append(out, at+"content is not base64")
		case float64(len(raw)) > limit:
			out = append(out, fmt.Sprintf("%s%s is larger than %g bytes", at, name, limit))
		case len(accept) > 0 && !accepts(accept, name, typ):
			out = append(out, fmt.Sprintf("%s%s is not one of %s", at, name, strings.Join(accept, ", ")))
		}
	}
	if multiple(f) {
		out = append(out, checkCount(f, len(entries), "files")...)
	}
	return out
}

// accepts reports whether a file matches one of the accepted media types (text/*) or extensions (.csv).
func accepts(accept []string, name, typ string) bool {
	ext := strings.ToLower(path.Ext(name))
	for _, a := range accept {
		a = strings.ToLower(strings.TrimSpace(a))
		switch {
		case strings.HasPrefix(a, "."):
			if ext == a {
				return true
			}
		case strings.HasSuffix(a, "/*"):
			if strings.HasPrefix(strings.ToLower(typ), strings.TrimSuffix(a, "*")) {
				return true
			}
		case a == strings.ToLower(typ):
			return true
		}
	}
	return false
}
