package assistantsvc

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/zimwip/goap/internal/convsvc"
)

// The context of a turn (ADR 0092): what the person was looking at when they wrote the message, in three layers the
// prompt reads most specific first. The web collects it, the service caps and sanitises it, it is used for that turn
// only and never stored (the user message keeps a short description, Context.describe).

// Caps of the context. Texts longer than their cap are cut, lists longer than theirs lose their tail; only the
// selection (which the web clips itself) and a context that still exceeds MaxContextBytes without its entities are
// refused.
const (
	MaxEntities     = 40
	MaxErrors       = 10
	maxLabelBytes   = 200
	maxIDBytes      = 100
	maxKindBytes    = 60
	maxSummaryBytes = 600
	maxActionBytes  = 300
	maxErrorBytes   = 200
	maxProps        = 6
	maxPropBytes    = 80
	maxParams       = 10
	maxParamBytes   = 200
)

// Context is the snapshot of the page of a message.
type Context struct {
	App    App
	Screen Screen
	Focus  Focus
}

// App is the application: the active project and the tab.
type App struct {
	Project string
	Tab     Tab
}

// Tab is the active tab (kind and parameters, opaque but for a change: kind "change", parameter "id").
type Tab struct {
	Kind   string
	Params map[string]string
}

// Screen is what the open view shows.
type Screen struct {
	Kind, Title string
	// Summary is a short text of what the view shows.
	Summary  string
	Entities []Entity
}

// Entity is an element the screen shows, with a few small properties.
type Entity struct {
	Type, ID, Label, State string
	Props                  map[string]string
}

// Element is the element the person acts on.
type Element struct{ Type, ID, Label string }

// Dialog is the dialog open on the screen.
type Dialog struct{ Kind, Title string }

// Focus is the most specific layer: the element, the selection, the dialog and the action in progress.
type Focus struct {
	Element       *Element
	Selection     string
	Dialog        Dialog
	PendingAction string
	Errors        []string
	LastAction    string
}

// change is the change the page of the turn is about ("" for none): a change tab (its "id" parameter), else the
// focused element when it is a change.
func (c Context) change() string {
	if c.App.Tab.Kind == "change" {
		if id := strings.TrimSpace(c.App.Tab.Params["id"]); id != "" {
			return id
		}
	}
	if e := c.Focus.Element; e != nil && e.Type == "change" {
		return e.ID
	}
	return ""
}

// clean removes control characters (a newline is kept when multiline, else becomes a space), collapses runs of spaces
// in a single-line text and cuts the result to n bytes.
func clean(s string, n int, multiline bool) string {
	s = strings.ToValidUTF8(s, "")
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case r == '\n' && multiline:
			b.WriteRune(r)
			space = false
		case unicode.IsControl(r) || unicode.IsSpace(r) || r == ' ' || r == ' ':
			if !space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = true
		default:
			b.WriteRune(r)
			space = false
		}
	}
	out := strings.TrimSpace(b.String())
	if len(out) > n {
		out = strings.ToValidUTF8(out[:n], "")
		for !utf8.ValidString(out) {
			out = out[:len(out)-1]
		}
	}
	return out
}

func cleanMap(m map[string]string, max, keyBytes, valBytes int) map[string]string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := map[string]string{}
	for _, k := range keys {
		if len(out) == max {
			break
		}
		if ck := clean(k, keyBytes, false); ck != "" {
			out[ck] = clean(m[k], valBytes, false)
		}
	}
	return out
}

// normalise returns the context capped and sanitised. Entities beyond MaxEntities, then the last ones until the
// rendered context fits MaxContextBytes, are dropped, the entity of the focus first kept.
func (c Context) normalise() (Context, error) {
	if len(c.Focus.Selection) > MaxSelectionBytes {
		return c, fmt.Errorf("%w: the selection is %d bytes, at most %d", ErrInvalid, len(c.Focus.Selection), MaxSelectionBytes)
	}
	o := Context{
		App: App{Project: clean(c.App.Project, maxIDBytes, false), Tab: Tab{Kind: clean(c.App.Tab.Kind, maxKindBytes, false), Params: cleanMap(c.App.Tab.Params, maxParams, 40, maxParamBytes)}},
		Screen: Screen{Kind: clean(c.Screen.Kind, maxKindBytes, false), Title: clean(c.Screen.Title, maxLabelBytes, false),
			Summary: clean(c.Screen.Summary, maxSummaryBytes, true)},
		Focus: Focus{Selection: clean(c.Focus.Selection, MaxSelectionBytes, true),
			Dialog:        Dialog{Kind: clean(c.Focus.Dialog.Kind, maxKindBytes, false), Title: clean(c.Focus.Dialog.Title, maxLabelBytes, false)},
			PendingAction: clean(c.Focus.PendingAction, maxActionBytes, false), LastAction: clean(c.Focus.LastAction, maxActionBytes, false)},
	}
	if e := c.Focus.Element; e != nil {
		if el := (Element{clean(e.Type, maxKindBytes, false), clean(e.ID, maxIDBytes, false), clean(e.Label, maxLabelBytes, false)}); el != (Element{}) {
			o.Focus.Element = &el
		}
	}
	for _, e := range c.Focus.Errors {
		if e = clean(e, maxErrorBytes, false); e != "" && len(o.Focus.Errors) < MaxErrors {
			o.Focus.Errors = append(o.Focus.Errors, e)
		}
	}
	for _, e := range c.Screen.Entities {
		ce := Entity{Type: clean(e.Type, maxKindBytes, false), ID: clean(e.ID, maxIDBytes, false), Label: clean(e.Label, maxLabelBytes, false),
			State: clean(e.State, maxKindBytes, false), Props: cleanMap(e.Props, maxProps, 40, maxPropBytes)}
		if ce.Type == "" && ce.ID == "" && ce.Label == "" {
			continue
		}
		o.Screen.Entities = append(o.Screen.Entities, ce)
	}
	// the entity the person acts on comes first, so that it is the last one dropped
	if el := o.Focus.Element; el != nil {
		if i := slices.IndexFunc(o.Screen.Entities, func(e Entity) bool { return e.Type == el.Type && e.ID == el.ID }); i > 0 {
			e := o.Screen.Entities[i]
			o.Screen.Entities = slices.Insert(slices.Delete(o.Screen.Entities, i, i+1), 0, e)
		}
	}
	if len(o.Screen.Entities) > MaxEntities {
		o.Screen.Entities = o.Screen.Entities[:MaxEntities]
	}
	for o.rendered("") > MaxContextBytes && len(o.Screen.Entities) > 0 {
		o.Screen.Entities = o.Screen.Entities[:len(o.Screen.Entities)-1]
	}
	if n := o.rendered(""); n > MaxContextBytes {
		return c, fmt.Errorf("%w: the context is %d bytes, at most %d", ErrInvalid, n, MaxContextBytes)
	}
	return o, nil
}

// The JSON of the prompt: the layers most specific first, empty fields left out.
type (
	jsonContext struct {
		Focus  *jsonFocus  `json:"focus,omitempty"`
		Screen *jsonScreen `json:"screen,omitempty"`
		App    *jsonApp    `json:"app,omitempty"`
	}
	jsonFocus struct {
		Element       *jsonElement `json:"element,omitempty"`
		Selection     string       `json:"selection,omitempty"`
		Dialog        *jsonDialog  `json:"dialog,omitempty"`
		PendingAction string       `json:"pendingAction,omitempty"`
		Errors        []string     `json:"errors,omitempty"`
		LastAction    string       `json:"lastAction,omitempty"`
	}
	jsonElement struct {
		Type  string `json:"type,omitempty"`
		ID    string `json:"id,omitempty"`
		Label string `json:"label,omitempty"`
	}
	jsonDialog struct {
		Kind  string `json:"kind,omitempty"`
		Title string `json:"title,omitempty"`
	}
	jsonScreen struct {
		Kind     string       `json:"kind,omitempty"`
		Title    string       `json:"title,omitempty"`
		Summary  string       `json:"summary,omitempty"`
		Entities []jsonEntity `json:"entities,omitempty"`
	}
	jsonEntity struct {
		Type  string            `json:"type,omitempty"`
		ID    string            `json:"id,omitempty"`
		Label string            `json:"label,omitempty"`
		State string            `json:"state,omitempty"`
		Props map[string]string `json:"props,omitempty"`
	}
	jsonApp struct {
		Project string   `json:"activeProject,omitempty"`
		Tab     *jsonTab `json:"tab,omitempty"`
	}
	jsonTab struct {
		Kind   string            `json:"kind,omitempty"`
		Params map[string]string `json:"params,omitempty"`
	}
)

// rendered is the JSON of the context, `project` replacing the active project when not empty (select_project moves
// it during the turn).
func (c Context) rendered(project string) int { return len(c.render(project)) }

func (c Context) render(project string) string {
	var j jsonContext
	f := c.Focus
	if f.Element != nil || f.Selection != "" || f.Dialog != (Dialog{}) || f.PendingAction != "" || len(f.Errors) > 0 || f.LastAction != "" {
		jf := &jsonFocus{Selection: f.Selection, PendingAction: f.PendingAction, Errors: f.Errors, LastAction: f.LastAction}
		if f.Element != nil {
			jf.Element = &jsonElement{f.Element.Type, f.Element.ID, f.Element.Label}
		}
		if f.Dialog != (Dialog{}) {
			jf.Dialog = &jsonDialog{f.Dialog.Kind, f.Dialog.Title}
		}
		j.Focus = jf
	}
	if s := c.Screen; s.Kind != "" || s.Title != "" || s.Summary != "" || len(s.Entities) > 0 {
		js := &jsonScreen{Kind: s.Kind, Title: s.Title, Summary: s.Summary}
		for _, e := range s.Entities {
			js.Entities = append(js.Entities, jsonEntity(e))
		}
		j.Screen = js
	}
	if project == "" {
		project = c.App.Project
	}
	if project != "" || c.App.Tab.Kind != "" || len(c.App.Tab.Params) > 0 {
		ja := &jsonApp{Project: project}
		if c.App.Tab.Kind != "" || len(c.App.Tab.Params) > 0 {
			ja.Tab = &jsonTab{c.App.Tab.Kind, c.App.Tab.Params}
		}
		j.App = ja
	}
	b, _ := json.Marshal(j)
	return string(b)
}

// describe is the short description of the context kept on the user message: what the person was doing, on what, in
// which screen, tab and project, and that a selection existed. Never the entities, the summary, the errors, the
// selection or any form content.
func (c Context) describe() string {
	var parts []string
	if v := c.Focus.PendingAction; v != "" {
		parts = append(parts, clip(v, 80))
	}
	if e := c.Focus.Element; e != nil {
		name := e.ID
		if name == "" {
			name = e.Label
		}
		parts = append(parts, strings.TrimSpace("on "+clip(e.Type, 40)+" "+clip(name, 80)))
	}
	if d := c.Focus.Dialog; d != (Dialog{}) {
		parts = append(parts, "dialog "+clip(strings.TrimSpace(d.Kind+" "+d.Title), 80))
	}
	if s := c.Screen; s.Kind != "" || s.Title != "" {
		parts = append(parts, "screen "+clip(strings.TrimSpace(s.Kind+" "+s.Title), 100))
	}
	if c.App.Tab.Kind != "" {
		parts = append(parts, "tab "+clip(c.App.Tab.Kind, 40))
	}
	if c.App.Project != "" {
		parts = append(parts, "project "+clip(c.App.Project, 60))
	}
	if c.Focus.Selection != "" {
		parts = append(parts, fmt.Sprintf("%d selected characters", len([]rune(c.Focus.Selection))))
	}
	return clip(strings.Join(parts, ", "), convsvc.MaxContextBytes)
}
