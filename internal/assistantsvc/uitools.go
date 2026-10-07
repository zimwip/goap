package assistantsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/zimwip/goap/internal/convsvc"
	"github.com/zimwip/goap/pkg/authz"
)

// Screen tools (ADR 0092). The screen the person is on offers, with each message, the tools it can run (navigate,
// filter, edit a field...). The service never runs them: an `effect` tool (no data changes) is handed back to the web
// as a requested action, a `write` tool as a proposal the person confirms (ConfirmAction) and the web then runs and
// reports (ReportAction). The model calls them under the prefix `ui.`, so that one can never shadow a server tool.

// ActionUITool is the type of the actions of the screen tools; PrefixUI the prefix of their name in the model's calls.
const (
	ActionUITool = "ui_tool"
	PrefixUI     = "ui."

	LevelEffect = "effect"
	LevelWrite  = "write"

	// StatusRequested is an effect handed to the web; StatusAccepted a write the person accepted and the web is to
	// run; StatusDone and StatusFailed the outcome the web reported.
	StatusRequested = "requested"
	StatusAccepted  = "accepted"
	StatusDone      = "done"
)

// Caps of the descriptors and of what the model calls them with.
const (
	MaxUITools = 30
	// MaxProposalsPerAnswer and MaxEffectsPerAnswer cap the screen tools of one answer.
	MaxProposalsPerAnswer = 3
	MaxEffectsPerAnswer   = 5

	maxToolDescription = 300
	maxToolGuidance    = 200
	maxToolSchemaBytes = 2 << 10
	maxToolProps       = 12
	maxEnumValues      = 30
	maxEnumBytes       = 100
	maxParamDesc       = 200
	maxArrayItems      = 50
	maxArgBytes        = 4 << 10
	maxStringArg       = 2000
)

var (
	toolNameRE = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,48}$`)
	propNameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,39}$`)
)

// ErrState is returned when an action is not in a state that allows the operation (a report on a proposal not
// accepted, a decision on something that is no proposal).
var ErrState = errors.New("the action is not in a state that allows this")

// UITool describes a tool of the current screen.
type UITool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Guidance is one line: what is required to feed it.
	Guidance string `json:"guidance,omitempty"`
	// Level is effect or write.
	Level string `json:"level"`
	Args  UIArgs `json:"args"`
	// Target is the element id it acts on.
	Target string `json:"target,omitempty"`
}

// UIArgs is the subset of JSON Schema a tool takes: an object of scalar, enum or array properties.
type UIArgs struct {
	Properties map[string]UIParam `json:"properties,omitempty"`
	Required   []string           `json:"required,omitempty"`
}

// UIParam is one property: Type is string, number, boolean, enum (Enum lists its values) or array (Items is the type of
// its elements, a scalar or an enum).
type UIParam struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Items       *UIParam `json:"items,omitempty"`
}

func checkParam(name string, p UIParam, item bool) (UIParam, error) {
	out := UIParam{Type: p.Type, Description: clean(p.Description, maxParamDesc, false)}
	switch p.Type {
	case "string", "number", "boolean":
		if len(p.Enum) > 0 || p.Items != nil {
			return out, fmt.Errorf("%q: a %s takes no enum or items", name, p.Type)
		}
	case "enum":
		if p.Items != nil || len(p.Enum) == 0 || len(p.Enum) > maxEnumValues {
			return out, fmt.Errorf("%q: an enum lists 1 to %d values", name, maxEnumValues)
		}
		for _, v := range p.Enum {
			if v = clean(v, maxEnumBytes, false); v == "" || len(v) > maxEnumBytes {
				return out, fmt.Errorf("%q: an enum value is empty or too long", name)
			} else if slices.Contains(out.Enum, v) {
				return out, fmt.Errorf("%q: duplicate enum value %q", name, v)
			} else {
				out.Enum = append(out.Enum, v)
			}
		}
	case "array":
		if item || p.Items == nil || len(p.Enum) > 0 {
			return out, fmt.Errorf("%q: an array (never nested) needs items", name)
		}
		it, err := checkParam(name+"[]", *p.Items, true)
		if err != nil {
			return out, err
		}
		out.Items = &it
	default:
		return out, fmt.Errorf("%q: type %q is not one of string, number, boolean, enum, array", name, p.Type)
	}
	return out, nil
}

// checkTools validates and cleans the descriptors of a request: an invalid one refuses the request (it is a bug of
// the screen, not of the model).
func checkTools(tools []UITool) ([]UITool, error) {
	if len(tools) > MaxUITools {
		return nil, fmt.Errorf("%w: %d screen tools, at most %d", ErrInvalid, len(tools), MaxUITools)
	}
	bad := func(t UITool, format string, a ...any) error {
		return fmt.Errorf("%w: screen tool %q: %s", ErrInvalid, t.Name, fmt.Sprintf(format, a...))
	}
	var out []UITool
	seen := map[string]bool{}
	for _, t := range tools {
		if !toolNameRE.MatchString(t.Name) {
			return nil, bad(t, "the name must match %s", toolNameRE)
		}
		if seen[t.Name] {
			return nil, bad(t, "duplicate name")
		}
		seen[t.Name] = true
		if t.Level != LevelEffect && t.Level != LevelWrite {
			return nil, bad(t, "the level is %s or %s", LevelEffect, LevelWrite)
		}
		if strings.TrimSpace(t.Description) == "" || len(t.Description) > maxToolDescription || len(t.Guidance) > maxToolGuidance || len(t.Target) > maxIDBytes {
			return nil, bad(t, "a description (at most %d bytes), a guidance (%d) and a target (%d) are needed within their size", maxToolDescription, maxToolGuidance, maxIDBytes)
		}
		c := UITool{Name: t.Name, Description: clean(t.Description, maxToolDescription, false), Guidance: clean(t.Guidance, maxToolGuidance, false),
			Level: t.Level, Target: clean(t.Target, maxIDBytes, false)}
		if len(t.Args.Properties) > maxToolProps || len(t.Args.Required) > maxToolProps {
			return nil, bad(t, "at most %d properties", maxToolProps)
		}
		for name, p := range t.Args.Properties {
			if !propNameRE.MatchString(name) {
				return nil, bad(t, "the property %q must match %s", name, propNameRE)
			}
			cp, err := checkParam(name, p, false)
			if err != nil {
				return nil, bad(t, "%v", err)
			}
			if c.Args.Properties == nil {
				c.Args.Properties = map[string]UIParam{}
			}
			c.Args.Properties[name] = cp
		}
		for _, r := range t.Args.Required {
			if _, ok := c.Args.Properties[r]; !ok || slices.Contains(c.Args.Required, r) {
				return nil, bad(t, "required %q is not a (single) property", r)
			}
			c.Args.Required = append(c.Args.Required, r)
		}
		if b, _ := json.Marshal(c.Args); len(b) > maxToolSchemaBytes {
			return nil, bad(t, "the schema is %d bytes, at most %d", len(b), maxToolSchemaBytes)
		}
		out = append(out, c)
	}
	return out, nil
}

func toolIndex(tools []UITool) map[string]UITool {
	m := make(map[string]UITool, len(tools))
	for _, t := range tools {
		m[t.Name] = t
	}
	return m
}

// checkValue validates one argument against its param and returns it as it is to be recorded.
func checkValue(name string, p UIParam, v any) error {
	switch p.Type {
	case "string":
		s, ok := v.(string)
		if !ok || len(s) > maxStringArg {
			return fmt.Errorf("%q must be a string of at most %d bytes", name, maxStringArg)
		}
	case "number":
		if _, ok := v.(float64); !ok {
			return fmt.Errorf("%q must be a number", name)
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("%q must be a boolean", name)
		}
	case "enum":
		if s, ok := v.(string); !ok || !slices.Contains(p.Enum, s) {
			return fmt.Errorf("%q must be one of %s", name, strings.Join(p.Enum, ", "))
		}
	case "array":
		l, ok := v.([]any)
		if !ok || len(l) > maxArrayItems {
			return fmt.Errorf("%q must be an array of at most %d elements", name, maxArrayItems)
		}
		for i, e := range l {
			if err := checkValue(fmt.Sprintf("%s[%d]", name, i), *p.Items, e); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkArgs validates the arguments of a call against the schema of its descriptor: unknown properties, missing
// required ones, types, enums and size are refused.
func checkArgs(t UITool, a args) error {
	if b, _ := json.Marshal(a); len(b) > maxArgBytes {
		return fmt.Errorf("the arguments are %d bytes, at most %d", len(b), maxArgBytes)
	}
	var unknown []string
	for k := range a {
		if _, ok := t.Args.Properties[k]; !ok {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf("unknown argument(s) %s for %s%s", strings.Join(unknown, ", "), PrefixUI, t.Name)
	}
	for _, r := range t.Args.Required {
		if _, ok := a[r]; !ok {
			return fmt.Errorf("%q is required (%s)", r, orDefault(t.Guidance, "see the tool description"))
		}
	}
	for k, v := range a {
		if err := checkValue(k, t.Args.Properties[k], v); err != nil {
			return err
		}
	}
	return nil
}

func orDefault(s, d string) string {
	if s != "" {
		return s
	}
	return d
}

// shortArgs is a one-line view of arguments for labels and the history: k=v pairs in key order, cut.
func shortArgs(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		b, _ := json.Marshal(m[k])
		parts = append(parts, k+"="+clip(string(b), 60))
	}
	return clip(strings.Join(parts, ", "), 160)
}

// uiTool handles a call of a screen tool: it is validated against this turn's descriptors and recorded, never run.
func (t *turn) uiTool(c toolCall, a args) (any, error) {
	name := strings.TrimPrefix(c.Name, PrefixUI)
	tool, ok := t.uiTools[name]
	if !ok {
		var names []string
		for n := range t.uiTools {
			names = append(names, PrefixUI+n)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("the screen offers no tool %q this turn (screen tools: %s)", c.Name, orDefault(strings.Join(names, ", "), "none"))
	}
	if err := checkArgs(tool, a); err != nil {
		return nil, err
	}
	act := convsvc.Action{"type": ActionUITool, "level": tool.Level, "tool": tool.Name, "args": map[string]any(a)}
	if act["args"] == nil {
		act["args"] = map[string]any{}
	}
	label := tool.Description
	if s := shortArgs(a); s != "" {
		label += " (" + s + ")"
	}
	act["label"] = clip(label, 200)
	if tool.Target != "" {
		act["target"] = tool.Target
	}
	if r := clean(c.Rationale, 500, false); r != "" {
		act["rationale"] = r
	}
	if tool.Level == LevelEffect {
		if t.effects >= MaxEffectsPerAnswer {
			return nil, fmt.Errorf("too many interface actions in one answer (at most %d)", MaxEffectsPerAnswer)
		}
		t.effects++
		act["status"] = StatusRequested
		t.actions = append(t.actions, act)
		return map[string]any{"status": StatusRequested, "note": "requested on the client: no result is available, do not rely on its outcome"}, nil
	}
	if t.proposals >= MaxProposalsPerAnswer {
		return nil, fmt.Errorf("too many proposals in one answer (at most %d): the person decides these first", MaxProposalsPerAnswer)
	}
	t.proposals++
	act["status"] = StatusProposed
	act["project"] = t.project
	t.actions = append(t.actions, act)
	return map[string]any{"status": StatusProposed, "note": "proposed, awaiting the person's confirmation in the interface: nothing was changed. Say what you propose and why, and do not say it was done."}, nil
}

// ReportInput is the outcome the web reports of an action it ran.
type ReportInput struct {
	ConversationID string
	MessageID      string
	ActionIndex    int
	// Status is done or failed; Error says what went wrong.
	Status string
	Error  string
}

// Report records the outcome of a screen tool the web ran (ADR 0092), as the owner of the conversation: a write
// proposal that was accepted, or an effect that was requested, becomes done or failed. Any other state is ErrState,
// an outcome already reported ErrDecided.
func (s *Service) Report(ctx context.Context, in ReportInput) (convsvc.Message, error) {
	p := authz.From(ctx)
	if p.Anonymous() {
		return convsvc.Message{}, convsvc.ErrAnonymous
	}
	if p.System() {
		return convsvc.Message{}, convsvc.ErrForbidden
	}
	if in.Status != StatusDone && in.Status != StatusFailed {
		return convsvc.Message{}, fmt.Errorf("%w: the status is %s or %s", ErrInvalid, StatusDone, StatusFailed)
	}
	_, msgs, err := s.Convs.Get(ctx, p, in.ConversationID) // the owner only
	if err != nil {
		return convsvc.Message{}, err
	}
	i := slices.IndexFunc(msgs, func(m convsvc.Message) bool { return m.ID == in.MessageID })
	if i < 0 {
		return convsvc.Message{}, convsvc.ErrNotFound
	}
	msg := msgs[i]
	if msg.Role != convsvc.RoleAssistant || in.ActionIndex < 0 || in.ActionIndex >= len(msg.Actions) || msg.Actions[in.ActionIndex]["type"] != ActionUITool {
		return convsvc.Message{}, fmt.Errorf("%w: that is not a screen tool action", ErrInvalid)
	}
	sys := authz.With(context.WithoutCancel(ctx), Principal)
	return s.Convs.UpdateAction(sys, Principal, in.MessageID, in.ActionIndex, func(a convsvc.Action) (convsvc.Action, error) {
		switch a["status"] {
		case StatusDone, StatusFailed:
			return nil, ErrDecided
		case StatusAccepted:
			if a["level"] != LevelWrite {
				return nil, ErrState
			}
		case StatusRequested:
			if a["level"] != LevelEffect {
				return nil, ErrState
			}
		default:
			return nil, fmt.Errorf("%w: it is %v", ErrState, a["status"])
		}
		a["status"] = in.Status
		a["reported"] = map[string]any{"by": p.Subject, "at": s.now().UTC().Format(time.RFC3339)}
		if e := clean(in.Error, maxErrorText, false); e != "" && in.Status == StatusFailed {
			a["error"] = e
		}
		return a, nil
	})
}
