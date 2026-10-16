package engine

import (
	"crypto/sha256"
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/zimwip/goap/contracts/llm"
)

// Condensing the conversation of an action without a model: what a tool returns is tidied before the model sees it
// (tidy), and a tool result the conversation holds again later is replaced by a short note (dedupResults). Nothing
// is rewritten: what stays is exact, what goes can be asked for again.

// ansiEscape matches the terminal escapes of a command's output (colours, cursor moves, titles).
var ansiEscape = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[@-Z\\-_])`)

// repeatsKept is how many identical lines in a row tidyText keeps before it counts the others.
const repeatsKept = 2

// tidy returns v with its texts tidied (tidyText), maps and lists walked; v is not changed.
func tidy(v any) any {
	switch t := v.(type) {
	case string:
		return tidyText(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = tidy(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = tidy(x)
		}
		return out
	}
	return v
}

// tidyText removes what a terminal output carries for the eye only: escapes, carriage-return redraws (a progress bar
// keeps its last state), trailing blanks; and counts the identical lines in a row past repeatsKept.
func tidyText(s string) string {
	if !strings.ContainsAny(s, "\x1b\r\n") {
		return s
	}
	s = ansiEscape.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	same := 0
	flush := func() {
		if same > 0 {
			out = append(out, "(previous line repeated "+strconv.Itoa(same)+" more times)")
			same = 0
		}
	}
	for _, l := range lines {
		if i := strings.LastIndexByte(l, '\r'); i >= 0 {
			l = l[i+1:]
		}
		l = strings.TrimRight(l, " \t")
		n := len(out)
		if l != "" && n >= repeatsKept && same == 0 && out[n-1] == l && out[n-2] == l || same > 0 && out[n-1] == l {
			same++
			continue
		}
		flush()
		out = append(out, l)
	}
	flush()
	return strings.Join(out, "\n")
}

// minDedup is the size under which a tool result is never replaced: a note would not be much shorter.
const minDedup = 512

const (
	// superseded replaces the result of a call made again later with the same arguments (a file read again).
	superseded = "(superseded: the same call was made again later, its latest result is below)"
	// repeated replaces a result identical to a later one of another call.
	repeated = "(identical to a later tool result, see below)"
)

// dedupResults replaces a tool result the conversation holds again later: the result of a call made again with the
// same tool and arguments (the latest is what holds: a file read again after an edit), else a result identical to a
// later one. Errors and short results stay. The latest occurrence always stays whole, so nothing is lost; msgs is not
// changed. Only the native tool results are looked at (a text protocol gives them back as one text).
func dedupResults(msgs []llm.Message) []llm.Message {
	calls := map[string]string{} // call id -> tool and arguments
	for _, m := range msgs {
		for _, c := range m.ToolCalls {
			raw, _ := json.Marshal(c.Arguments)
			calls[c.ID] = c.Name + "\x00" + string(raw)
		}
	}
	sigs, sums := map[string]bool{}, map[[32]byte]bool{}
	var out []llm.Message
	for i := len(msgs) - 1; i >= 1; i-- {
		for j := len(msgs[i].ToolResults) - 1; j >= 0; j-- {
			r := msgs[i].ToolResults[j]
			if r.IsError || len(r.Content) < minDedup {
				continue
			}
			sig, sum := calls[r.CallID], sha256.Sum256([]byte(r.Content))
			note := ""
			switch {
			case sig != "" && sigs[sig]:
				note = superseded
			case sums[sum]:
				note = repeated
			}
			if sig != "" {
				sigs[sig] = true
			}
			sums[sum] = true
			if note == "" {
				continue
			}
			if out == nil {
				out = slices.Clone(msgs)
			}
			if &out[i].ToolResults[0] == &msgs[i].ToolResults[0] {
				out[i].ToolResults = slices.Clone(msgs[i].ToolResults)
			}
			out[i].ToolResults[j].Content = note
		}
	}
	if out == nil {
		return msgs
	}
	return out
}
