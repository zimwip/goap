package modelgw

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/zimwip/goap/contracts/llm"
)

// The Fake provider calls tools too, without a model, so that a tool loop (the assistant, an llm action with MCPs)
// works end to end on an install with no API key: a request offering tools whose last message brings no tool result
// gets one call of the tool the last user message names best (the words of the tool's name found in the text), its
// arguments taken from a JSON object of the text, else from an object of the tool results of the conversation one of
// whose values the text names (an object argument of required strings, the text itself); a tool that changes something is called only with arguments, so "start the analyst
// agent" calls list_agents, then start_agent with the methodology and agent of the entry named "analyst". Each tool is
// called once per answer; when none is left to call, the answer is a text listing the last tool results.
// Deterministic, never a model's judgment.

// NativeTools implements NativeTools: the Fake reads llm.Request.Tools itself.
func (Fake) NativeTools() bool { return true }

// maxFakeAnswer caps the text the Fake writes from tool results.
const maxFakeAnswer = 4000

// fakeToolCall picks the tool the last user message asks for, if any.
func fakeToolCall(req llm.Request) (llm.ToolCall, bool) {
	text, called := lastUserText(req.Messages), calledSince(req.Messages)
	if text == "" {
		return llm.ToolCall{}, false
	}
	words := wordsOf(text)
	given := jsonObjectIn(text)
	seen := priorObjects(req.Messages)
	if call, ok := askedFor(req, called, text); ok {
		return call, true
	}
	best, bestScore := -1, 0
	var bestArgs map[string]any
	for i, t := range req.Tools {
		if c := called[t.Name]; c.n > 0 && (!c.failed || c.n > 1) {
			continue
		}
		score := 0
		for _, tok := range wordsOf(t.Name) {
			if tok != "ui" && matchesWord(tok, words) {
				score++
			}
		}
		if score == 0 {
			continue
		}
		props, required := schemaOf(t.InputSchema)
		args := pick(given, props)
		if len(args) == 0 {
			args = fromSeen(seen, props, strings.ToLower(text))
		}
		if len(args) > 0 {
			score += 2
			fillObjects(args, t.InputSchema, text)
		}
		if !hasAll(args, required) || len(args) == 0 && len(props) > 0 && !readOnly(t.Name) {
			continue // a tool that changes something is not called blind: a listing may give its arguments first
		}
		if score > bestScore || score == bestScore && best >= 0 && readOnly(t.Name) && !readOnly(req.Tools[best].Name) {
			best, bestScore, bestArgs = i, score, args
		}
	}
	if best < 0 {
		return llm.ToolCall{}, false
	}
	return llm.ToolCall{ID: "call_1", Name: req.Tools[best].Name, Arguments: bestArgs}, true
}

// fakeToolAnswer is the text of an answer to the tool results of the last message.
func fakeToolAnswer(results []llm.ToolResult) string {
	var b strings.Builder
	for _, r := range results {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		if r.IsError {
			fmt.Fprintf(&b, "%s failed: %s", r.Name, r.Content)
			continue
		}
		fmt.Fprintf(&b, "%s returned:", r.Name)
		var v any
		if json.Unmarshal([]byte(r.Content), &v) != nil {
			b.WriteString("\n" + r.Content)
			continue
		}
		b.WriteString(describe(v))
	}
	s := b.String()
	if len(s) > maxFakeAnswer {
		s = s[:maxFakeAnswer] + "…"
	}
	return s
}

// describe renders a JSON result as lines: the scalars of an object, its notes, one line per element of its lists.
func describe(v any) string {
	var b strings.Builder
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			switch e := x[k].(type) {
			case []any:
				if len(e) == 0 {
					fmt.Fprintf(&b, "\n%s: none", k)
					continue
				}
				fmt.Fprintf(&b, "\n%s:", k)
				for _, it := range e {
					b.WriteString("\n- " + label(it))
				}
			case map[string]any:
				fmt.Fprintf(&b, "\n%s: %s", k, label(e))
			default:
				fmt.Fprintf(&b, "\n%s: %v", k, e)
			}
		}
	case []any:
		for _, it := range x {
			b.WriteString("\n- " + label(it))
		}
	default:
		fmt.Fprintf(&b, " %v", x)
	}
	return b.String()
}

// labelKeys are the fields naming an element of a result, in the order they are shown.
var labelKeys = []string{"methodology", "agent", "name", "key", "id", "title", "label", "status", "description"}

func label(v any) string {
	o, ok := v.(map[string]any)
	if !ok {
		return fmt.Sprint(v)
	}
	var parts []string
	for _, k := range labelKeys {
		if s, ok := o[k].(string); ok && s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		js, _ := json.Marshal(o)
		return string(js)
	}
	return strings.Join(parts, " · ")
}

// toolUse is how often a tool was called since the last message of the person, and whether its last call failed.
type toolUse struct {
	n      int
	failed bool
}

// calledSince are the tools called since the last message of the person: each is called once per answer, a second
// time after a failure.
func calledSince(msgs []llm.Message) map[string]toolUse {
	out, judged := map[string]toolUse{}, map[string]bool{}
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role == "user" && len(m.ToolResults) == 0 && strings.TrimSpace(m.Content) != "" {
			break
		}
		for _, r := range m.ToolResults { // latest first: the latest result of a tool decides
			if !judged[r.Name] {
				judged[r.Name] = true
				e := out[r.Name]
				e.failed = r.IsError
				out[r.Name] = e
			}
		}
		for _, c := range m.ToolCalls {
			e := out[c.Name]
			e.n++
			out[c.Name] = e
		}
	}
	return out
}

// askedFor is the call of a tool an error of the last results names ("search the open changes first
// (search_changes)"), not called yet, its required strings the person's text.
func askedFor(req llm.Request, called map[string]toolUse, text string) (llm.ToolCall, bool) {
	if len(req.Messages) == 0 {
		return llm.ToolCall{}, false
	}
	for _, r := range req.Messages[len(req.Messages)-1].ToolResults {
		if !r.IsError {
			continue
		}
		for _, t := range req.Tools {
			if t.Name == r.Name || called[t.Name].n > 0 || !strings.Contains(r.Content, t.Name) {
				continue
			}
			args := map[string]any{}
			_, required := schemaOf(t.InputSchema)
			props, _ := t.InputSchema["properties"].(map[string]any)
			for _, k := range required {
				if f, _ := props[k].(map[string]any); f["type"] != "string" {
					args = nil
					break
				}
				args[k] = text
			}
			if args != nil {
				return llm.ToolCall{ID: "call_1", Name: t.Name, Arguments: args}, true
			}
		}
	}
	return llm.ToolCall{}, false
}

func lastUserText(msgs []llm.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" && len(msgs[i].ToolResults) == 0 && strings.TrimSpace(msgs[i].Content) != "" {
			return msgs[i].Content
		}
	}
	return ""
}

// wordsOf splits s in lower-case words of letters and digits ("list_agents", "ui.fill_form", a sentence).
func wordsOf(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// matchesWord reports a word of the text that is tok, a plural or a verb form of it ("agents", "listing"), or its
// singular ("agent" for the token "agents").
func matchesWord(tok string, words []string) bool {
	if len(tok) < 3 {
		return false
	}
	for _, w := range words {
		if w == tok || len(w) <= len(tok)+3 && strings.HasPrefix(w, tok) || strings.TrimSuffix(tok, "s") == w {
			return true
		}
	}
	return false
}

// readOnly tells a tool that only reads by its name, preferred on a tie.
func readOnly(name string) bool {
	for _, p := range []string{"list", "get", "search", "read", "status", "query"} {
		if strings.HasPrefix(name, p) || strings.Contains(name, "_"+p) {
			return true
		}
	}
	return false
}

func schemaOf(s map[string]any) (props map[string]bool, required []string) {
	props = map[string]bool{}
	if p, ok := s["properties"].(map[string]any); ok {
		for k := range p {
			props[k] = true
		}
	}
	switch r := s["required"].(type) {
	case []string:
		required = r
	case []any:
		for _, x := range r {
			if s, ok := x.(string); ok {
				required = append(required, s)
			}
		}
	}
	return props, required
}

// jsonObjectIn is the first JSON object written in the text, if any.
func jsonObjectIn(text string) map[string]any {
	i := strings.Index(text, "{")
	if i < 0 {
		return nil
	}
	var o map[string]any
	if json.NewDecoder(strings.NewReader(text[i:])).Decode(&o) != nil {
		return nil
	}
	return o
}

func pick(o map[string]any, props map[string]bool) map[string]any {
	out := map[string]any{}
	for k, v := range o {
		if props[k] {
			out[k] = v
		}
	}
	return out
}

// priorObjects are the JSON objects found in the tool results of the conversation, the latest first.
func priorObjects(msgs []llm.Message) []map[string]any {
	var out []map[string]any
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			out = append(out, x)
			for _, e := range x {
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		for _, r := range msgs[i].ToolResults {
			var v any
			if !r.IsError && json.Unmarshal([]byte(r.Content), &v) == nil {
				walk(v)
			}
		}
	}
	return out
}

// fromSeen takes the arguments from the earlier object sharing the most fields with the tool's arguments, one of whose
// string values the text names.
func fromSeen(seen []map[string]any, props map[string]bool, text string) map[string]any {
	var best map[string]any
	for _, o := range seen {
		named := false
		args := map[string]any{}
		for k, v := range o {
			s, ok := v.(string)
			if !ok || !props[k] {
				continue
			}
			args[k] = s
			if len(s) >= 3 && strings.Contains(text, strings.ToLower(s)) {
				named = true
			}
		}
		if named && len(args) > len(best) {
			best = args
		}
	}
	return best
}

func hasAll(args map[string]any, required []string) bool {
	for _, k := range required {
		if _, ok := args[k]; !ok {
			return false
		}
	}
	return true
}

// fillObjects gives an object argument the call lacks, made of required strings only (a new change's title and
// intent), the person's text for each field.
func fillObjects(args map[string]any, schema map[string]any, text string) {
	props, _ := schema["properties"].(map[string]any)
	for k, p := range props {
		sub, _ := p.(map[string]any)
		if _, set := args[k]; set || sub["type"] != "object" {
			continue
		}
		subProps, required := schemaOf(sub)
		if len(required) == 0 {
			continue
		}
		o := map[string]any{}
		for _, r := range required {
			if f, _ := sub["properties"].(map[string]any)[r].(map[string]any); !subProps[r] || f["type"] != "string" {
				o = nil
				break
			}
			o[r] = text
		}
		if o != nil {
			args[k] = o
		}
	}
}
