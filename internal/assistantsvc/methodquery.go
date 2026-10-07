package assistantsvc

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/zimwip/goap/pkg/condition"
	"github.com/zimwip/goap/pkg/methodology"
)

// The query engine of methodology_query (ADR 0094): a pure function over the declared definition of a methodology. It
// reads nothing, calls nothing and never builds more than the caller asked for; the registry access is the Methodologies
// port of the Service. Every result is small by construction: filtered, paginated, projected, and capped in bytes.

// Kinds of element a methodology holds.
const (
	KindAgent     = "agent"
	KindAction    = "action"
	KindGoal      = "goal"
	KindProcess   = "process"
	KindStep      = "step"
	KindRole      = "role"
	KindMethod    = "method"
	KindTrigger   = "trigger"
	KindCondition = "condition"
	KindLibrary   = "library"
)

// ElementKinds lists the kinds, in the order of the overview.
var ElementKinds = []string{KindAgent, KindAction, KindGoal, KindProcess, KindStep, KindRole, KindMethod, KindTrigger, KindCondition, KindLibrary}

// Details of an item.
const (
	DetailNames   = "names"
	DetailSummary = "summary"
	DetailFull    = "full"
)

// Limits of a query.
const (
	DefaultQueryLimit = 20
	MaxQueryLimit     = 50
	// MaxFullItems caps the items of a result given in full detail.
	MaxFullItems = 3
	// MaxQueryBytes caps the JSON of a whole result.
	MaxQueryBytes = 6 << 10

	summaryText = 160  // a description in a summary: first line, cut
	fullText    = 2000 // a string of a full definition
	summaryList = 8    // the items of a list in a summary
)

// Query is the request of methodology_query, validated by Run.
type Query struct {
	Kind   string
	Name   string // exact name or glob (* and ?), case-insensitive
	Q      string // case-insensitive text in name, description and examples
	Parent string // only the children of this element
	Fields []string
	Detail string
	Limit  int
	Offset int
}

// QueryResult is the answer.
type QueryResult struct {
	Methodology string           `json:"methodology,omitempty"`
	Kind        string           `json:"kind,omitempty"`
	Total       int              `json:"total"`
	Returned    int              `json:"returned"`
	Offset      int              `json:"offset"`
	Truncated   bool             `json:"truncated"`
	Next        *NextPage        `json:"next,omitempty"`
	Counts      map[string]int   `json:"counts,omitempty"`
	Hint        string           `json:"hint,omitempty"`
	Items       []map[string]any `json:"items"`
}

// NextPage is where the next page of the same query starts.
type NextPage struct {
	Offset int `json:"offset"`
}

// element is one thing of a methodology, as the query sees it.
type element struct {
	kind    string
	name    string
	short   string   // the last segment of a step path, for the name filter
	parents []string // the elements it belongs to: the process or step of a step, the agents of an action...
	search  string   // lower-cased name, description and examples
	desc    string
	full    map[string]any
	summary []string // the fields of the summary
}

// elementFields is the valid fields of each kind, in the order of the definition, found from its struct.
var elementFields = map[string][]string{}

// actionKind avoids the clash of an action's own kind with the kind of the element.
const (
	fieldActionKind = "actionKind"
	fieldParents    = "parents"
	fieldHow        = "how"
)

func init() {
	base := []string{"kind", fieldParents}
	add := func(kind string, t reflect.Type, extra ...string) {
		out := slices.Clone(base)
		for i := 0; i < t.NumField(); i++ {
			tag, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
			switch tag {
			case "", "-":
				continue
			case "kind":
				tag = fieldActionKind
			}
			out = append(out, tag)
		}
		elementFields[kind] = append(out, extra...)
	}
	add(KindAgent, reflect.TypeOf(methodology.Agent{}))
	add(KindAction, reflect.TypeOf(methodology.Action{}))
	add(KindGoal, reflect.TypeOf(methodology.Goal{}))
	add(KindProcess, reflect.TypeOf(methodology.Process{}))
	add(KindStep, reflect.TypeOf(methodology.Step{}), fieldHow)
	add(KindRole, reflect.TypeOf(methodology.Role{}))
	add(KindMethod, reflect.TypeOf(methodology.Method{}))
	add(KindTrigger, reflect.TypeOf(methodology.Trigger{}))
	add(KindCondition, reflect.TypeOf(methodology.Condition{}))
	elementFields[KindLibrary] = []string{"name", "kind", fieldParents, "conditions"}
}

// summaryFields are the few defining fields of each kind (after name, kind and description).
var summaryFields = map[string][]string{
	KindAgent:     {"planner", "roles", "goals", "actions"},
	KindAction:    {fieldActionKind, "roles", "pre", "effects"},
	KindGoal:      {"pre", "value"},
	KindProcess:   {"steps"},
	KindStep:      {fieldHow, "action", "process", "roles", "steps"},
	KindRole:      {},
	KindMethod:    {"for", "when", "priority"},
	KindTrigger:   {"type", "event", "schedule", "enabled"},
	KindCondition: {"expr"},
	KindLibrary:   {"conditions"},
}

// validFields returns the valid fields of a kind, or of every kind when it is empty.
func validFields(kind string) []string {
	if kind != "" {
		return elementFields[kind]
	}
	var out []string
	for _, k := range ElementKinds {
		for _, f := range elementFields[k] {
			if !slices.Contains(out, f) {
				out = append(out, f)
			}
		}
	}
	return out
}

// RunQuery answers q over the methodologies given by name; with one it adds the counts by kind when no kind is asked.
func RunQuery(q Query, ms []*methodology.Methodology) (QueryResult, error) {
	q, err := q.validated()
	if err != nil {
		return QueryResult{}, err
	}
	multi := len(ms) > 1
	type found struct {
		m string
		e element
	}
	var matches []found
	counts := map[string]int{}
	for _, m := range ms {
		for _, e := range elementsOf(m) {
			if q.matches(e) {
				matches = append(matches, found{m.Name, e})
				counts[e.kind]++
			}
		}
	}
	res := QueryResult{Kind: q.Kind, Total: len(matches), Offset: q.Offset, Items: []map[string]any{}}
	if len(ms) == 1 {
		res.Methodology = ms[0].Name
	}
	if q.Kind == "" && !multi {
		res.Counts = counts
	}
	limit, fullCut := q.Limit, false
	if q.Detail == DetailFull && len(q.Fields) == 0 && limit > MaxFullItems {
		limit, fullCut = MaxFullItems, true
	}
	end := min(len(matches), q.Offset+limit)
	for i := q.Offset; i < end; i++ {
		item := matches[i].e.render(q)
		if multi {
			item["methodology"] = matches[i].m
		}
		res.Items = append(res.Items, item)
		if size(res) > MaxQueryBytes {
			res.Items = res.Items[:len(res.Items)-1]
			res.Truncated = true
			break
		}
	}
	res.Returned = len(res.Items)
	if q.Offset+res.Returned < res.Total {
		res.Next = &NextPage{Offset: q.Offset + res.Returned}
	}
	switch {
	case res.Truncated && res.Returned == 0:
		res.Hint = "the first item alone exceeds the size cap: narrow with kind, name, q or parent, and project with fields"
	case res.Truncated:
		res.Hint = "result cut at the size cap: narrow with kind, name, q or parent, project with fields, or paginate with next.offset"
	case fullCut:
		res.Truncated = true
		res.Hint = fmt.Sprintf("full detail returns at most %d items: narrow with name or q, or paginate with next.offset", MaxFullItems)
	case res.Next != nil && res.Total > 3*q.Limit:
		res.Hint = "many matches: narrow with kind, name, q or parent, or paginate with next.offset"
	}
	if res.Counts != nil && len(res.Counts) > 0 && q.Kind == "" && q.Name == "" && q.Q == "" && q.Parent == "" {
		res.Hint = strings.TrimSpace("overview by kind: zoom with kind (and name or q), then detail summary or full. " + res.Hint)
	}
	return res, nil
}

func size(r QueryResult) int {
	b, _ := json.Marshal(r)
	return len(b)
}

// validated checks and completes the query.
func (q Query) validated() (Query, error) {
	if q.Kind != "" && !slices.Contains(ElementKinds, q.Kind) {
		return q, fmt.Errorf("unknown kind %q: the kinds are %s", q.Kind, strings.Join(ElementKinds, ", "))
	}
	switch q.Detail {
	case "":
		q.Detail = DetailSummary
		if q.Kind == "" && q.Name == "" && q.Q == "" && q.Parent == "" {
			q.Detail = DetailNames // the overview: the counts say what exists, the names are enough to zoom
		}
	case DetailNames, DetailSummary, DetailFull:
	default:
		return q, fmt.Errorf("unknown detail %q: names, summary or full", q.Detail)
	}
	if len(q.Fields) > 0 {
		valid := validFields(q.Kind)
		var bad []string
		for _, f := range q.Fields {
			if !slices.Contains(valid, f) {
				bad = append(bad, f)
			}
		}
		if len(bad) > 0 {
			where := "for any kind"
			if q.Kind != "" {
				where = "for kind " + q.Kind
			}
			return q, fmt.Errorf("unknown fields %s: the valid fields %s are %s", strings.Join(bad, ", "), where, strings.Join(valid, ", "))
		}
	}
	switch {
	case q.Limit < 0 || q.Offset < 0:
		return q, fmt.Errorf("limit and offset cannot be negative")
	case q.Limit == 0:
		q.Limit = DefaultQueryLimit
	}
	q.Limit = min(q.Limit, MaxQueryLimit)
	return q, nil
}

func (q Query) matches(e element) bool {
	if q.Kind != "" && e.kind != q.Kind {
		return false
	}
	if q.Name != "" && !glob(q.Name, e.name) && !(e.short != "" && glob(q.Name, e.short)) {
		return false
	}
	if q.Q != "" && !strings.Contains(e.search, strings.ToLower(q.Q)) {
		return false
	}
	if q.Parent != "" && !slices.ContainsFunc(e.parents, func(p string) bool { return strings.EqualFold(p, q.Parent) }) {
		return false
	}
	return true
}

// glob matches name against a pattern with * and ?, case-insensitively.
func glob(pattern, name string) bool {
	p, s := []rune(strings.ToLower(pattern)), []rune(strings.ToLower(name))
	var pi, si, star, mark int
	star = -1
	for si < len(s) {
		switch {
		case pi < len(p) && (p[pi] == '?' || p[pi] == s[si]):
			pi++
			si++
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, si
			pi++
		case star >= 0:
			pi, mark = star+1, mark+1
			si = mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

// render is the item of an element for the query: name only; a projection; the summary; the whole definition.
func (e element) render(q Query) map[string]any {
	out := map[string]any{"name": e.name}
	switch {
	case len(q.Fields) > 0:
		out["kind"] = e.kind
		for _, f := range q.Fields {
			if v, ok := e.full[f]; ok && f != "name" {
				out[f] = shorten(v, fullText)
			}
		}
	case q.Detail == DetailNames:
		out["kind"] = e.kind
	case q.Detail == DetailSummary:
		out["kind"] = e.kind
		if e.desc != "" {
			out["description"] = clip(firstLine(e.desc), summaryText)
		}
		for _, f := range e.summary {
			if v, ok := e.full[f]; ok {
				out[f] = shortList(shorten(v, summaryText))
			}
		}
	default:
		for k, v := range e.full {
			if k != "name" {
				out[k] = shorten(v, fullText)
			}
		}
	}
	return out
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

// shorten cuts the strings of a value (and of the lists and maps in it).
func shorten(v any, n int) any {
	switch x := v.(type) {
	case string:
		return clip(x, n)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = shorten(e, n)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = shorten(e, n)
		}
		return out
	}
	return v
}

// shortList keeps the first items of a list, and says how many were left out.
func shortList(v any) any {
	if l, ok := v.([]any); ok && len(l) > summaryList {
		return append(slices.Clone(l[:summaryList]), fmt.Sprintf("… +%d more", len(l)-summaryList))
	}
	return v
}

// toMap is the JSON form of a definition, which carries its omitempty rules; an action's kind is renamed.
func toMap(v any, rename ...string) map[string]any {
	b, _ := json.Marshal(v)
	m := map[string]any{}
	_ = json.Unmarshal(b, &m)
	for i := 0; i+1 < len(rename); i += 2 {
		if x, ok := m[rename[i]]; ok {
			delete(m, rename[i])
			m[rename[i+1]] = x
		}
	}
	return m
}

func mk(kind, name string, parents []string, desc string, examples []string, full map[string]any) element {
	full["name"], full["kind"] = name, kind
	if len(parents) > 0 {
		full[fieldParents] = parents
	}
	return element{kind: kind, name: name, parents: parents, desc: desc, full: full,
		summary: summaryFields[kind], search: strings.ToLower(name + "\n" + desc + "\n" + strings.Join(examples, "\n"))}
}

// elementsOf lists what a methodology declares, in the order of ElementKinds. Generated agents, actions and goals (the
// ones of processes and methods) are not listed: they are the processes and methods themselves.
func elementsOf(m *methodology.Methodology) []element {
	var out []element
	owners := map[string][]string{} // action / goal -> agents admitting it
	for _, ag := range m.Agents {
		for _, a := range ag.Actions {
			owners["a:"+a] = append(owners["a:"+a], ag.Name)
		}
		for _, g := range ag.Goals {
			owners["g:"+g] = append(owners["g:"+g], ag.Name)
		}
	}
	for _, ag := range m.Agents {
		out = append(out, mk(KindAgent, ag.Name, nil, ag.Description, ag.Examples, toMap(ag)))
	}
	for _, a := range m.Actions {
		out = append(out, mk(KindAction, a.Name, owners["a:"+a.Name], a.Description, nil, toMap(a, "kind", fieldActionKind)))
	}
	for _, g := range m.Goals {
		out = append(out, mk(KindGoal, g.Name, owners["g:"+g.Name], g.Description, g.Examples, toMap(g)))
	}
	for _, p := range m.Processes {
		f := toMap(p)
		f["steps"] = stepNames(p.Name, p.Steps)
		out = append(out, mk(KindProcess, p.Name, nil, p.Description, p.Examples, f))
	}
	for _, p := range m.Processes {
		out = appendSteps(out, p.Name, p.Name, p.Steps)
	}
	for _, r := range m.Roles {
		out = append(out, mk(KindRole, r.Name, nil, r.Description, nil, toMap(r)))
	}
	for _, me := range m.Methods {
		f := toMap(me)
		f["steps"] = stepNames(me.Name, me.Steps)
		out = append(out, mk(KindMethod, me.Name, nil, me.Description, nil, f))
	}
	for _, me := range m.Methods {
		out = appendSteps(out, me.Name, me.Name, me.Steps)
	}
	for _, ag := range m.Agents {
		for _, t := range ag.Triggers {
			out = append(out, mk(KindTrigger, t.Name, []string{ag.Name}, t.Description, nil, toMap(t)))
		}
	}
	for _, c := range m.Conditions {
		out = append(out, mk(KindCondition, c.Name, nil, c.Description, nil, toMap(c)))
	}
	for _, name := range m.Imports {
		var names []string
		defs, _ := condition.Library(name)
		for _, d := range defs {
			names = append(names, d.Name)
		}
		sort.Strings(names)
		f := map[string]any{"conditions": names}
		out = append(out, mk(KindLibrary, name, nil, "built-in condition library", nil, f))
	}
	return out
}

func stepNames(prefix string, steps []methodology.Step) []string {
	var out []string
	for _, s := range steps {
		out = append(out, prefix+"/"+s.Name)
	}
	return out
}

// appendSteps adds the steps of a process or method, depth first; a step is named by its path, its parent is the
// process (or method) or the step above it.
func appendSteps(out []element, parent, prefix string, steps []methodology.Step) []element {
	for _, s := range steps {
		path := prefix + "/" + s.Name
		f := toMap(s)
		if len(s.Steps) > 0 {
			f["steps"] = stepNames(path, s.Steps)
		}
		f[fieldHow] = s.Method()
		e := mk(KindStep, path, []string{parent}, s.Description, nil, f)
		e.short = s.Name
		out = append(out, e)
		out = appendSteps(out, path, path, s.Steps)
	}
	return out
}
