// Package codetext is the value of an attribute of type code (lib/def AttrCode): a piece of source text with what
// tells how to read it, written as a YAML-like front matter giving at least its language, then the text as a fenced
// Markdown code block:
//
//	---
//	language: go
//	---
//	```go
//	func F() {}
//	```
//
// The value stays a string (a property of a node like any other, readable as Markdown in an export); Parse reads it
// back. A plain string with no front matter is code of no known language, so every string attribute can become a
// code attribute.
package codetext

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// KeyLanguage is the front matter key naming the language of the text.
const KeyLanguage = "language"

// Value is a code value read from its text.
type Value struct {
	// Language of the text (go, python, c...), empty when unknown.
	Language string
	// Meta are the other keys of the front matter, kept as they come.
	Meta map[string]string
	// Code is the source text, without the fences.
	Code string
}

var keyRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]*$`)

// Format writes a value: the front matter (the language first, then the other keys in order) and the fenced code
// block, its fence longer than any run of backticks of the text (a trailing newline of the text is dropped).
func Format(v Value) string {
	var b strings.Builder
	b.WriteString("---\n")
	if v.Language != "" {
		writeKey(&b, KeyLanguage, v.Language)
	}
	keys := make([]string, 0, len(v.Meta))
	for k := range v.Meta {
		if k != KeyLanguage && keyRE.MatchString(k) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	for _, k := range keys {
		writeKey(&b, k, v.Meta[k])
	}
	b.WriteString("---\n")
	b.WriteString(Block(v))
	return b.String()
}

// Block is the fenced code block of a value alone, with no front matter (the language as its info string): how a
// Markdown document shows it.
func Block(v Value) string {
	fence := strings.Repeat("`", max(3, longestRun(v.Code, '`')+1))
	var b strings.Builder
	b.WriteString(fence)
	b.WriteString(infoString(v.Language))
	b.WriteString("\n")
	if code := strings.TrimSuffix(v.Code, "\n"); code != "" {
		b.WriteString(code)
		b.WriteString("\n")
	}
	b.WriteString(fence)
	b.WriteString("\n")
	return b.String()
}

// infoString is the language as the info string of a fence: one word, no backtick.
func infoString(lang string) string {
	if strings.ContainsAny(lang, " \t`\n") {
		return ""
	}
	return lang
}

func writeKey(b *strings.Builder, k, v string) {
	b.WriteString(k)
	b.WriteString(": ")
	if needsQuotes(v) {
		v = strconv.Quote(v)
	}
	b.WriteString(v)
	b.WriteString("\n")
}

func needsQuotes(v string) bool {
	return v == "" || strings.TrimSpace(v) != v || strings.ContainsAny(v, "\n\r\"#") || strings.HasPrefix(v, "'")
}

func longestRun(s string, c byte) int {
	best, run := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			run++
			best = max(best, run)
		} else {
			run = 0
		}
	}
	return best
}

// Parse reads a code value. A text with no front matter is the code itself (or a fenced block alone, its info string
// the language); a front matter that is not closed, or a line of it that is no `key: value`, is an error, and so is
// text after the code block. Line ends are read as \n. A code block that is not closed runs to the end of the text.
func Parse(s string) (Value, error) {
	var v Value
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	i := 0
	if lines[0] == "---" {
		closed := false
		for i = 1; i < len(lines); i++ {
			line := lines[i]
			if line == "---" {
				closed = true
				i++
				break
			}
			if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			k, val, ok := strings.Cut(line, ":")
			k = strings.TrimSpace(k)
			if !ok || !keyRE.MatchString(k) {
				return Value{}, fmt.Errorf("front matter line %d: a key: value is expected, got %q", i+1, line)
			}
			val = strings.TrimSpace(val)
			if strings.HasPrefix(val, `"`) {
				u, err := strconv.Unquote(val)
				if err != nil {
					return Value{}, fmt.Errorf("front matter line %d: %v", i+1, err)
				}
				val = u
			} else if len(val) >= 2 && strings.HasPrefix(val, "'") && strings.HasSuffix(val, "'") {
				val = strings.ReplaceAll(val[1:len(val)-1], "''", "'")
			}
			if k == KeyLanguage {
				v.Language = val
				continue
			}
			if v.Meta == nil {
				v.Meta = map[string]string{}
			}
			v.Meta[k] = val
		}
		if !closed {
			return Value{}, fmt.Errorf("the front matter is not closed by a --- line")
		}
		// blank lines between the front matter and the block
		for i < len(lines)-1 && strings.TrimSpace(lines[i]) == "" {
			i++
		}
	}
	if i >= len(lines) {
		return v, nil
	}
	rest := lines[i:]
	char, n, info := openFence(rest[0])
	if n == 0 {
		v.Code = strings.Join(rest, "\n")
		return v, nil
	}
	if v.Language == "" {
		v.Language = info
	}
	end := -1
	for j := 1; j < len(rest); j++ {
		if closeFence(rest[j], char, n) {
			end = j
			break
		}
	}
	if end < 0 {
		v.Code = strings.Join(rest[1:], "\n")
		return v, nil
	}
	v.Code = strings.Join(rest[1:end], "\n")
	if strings.TrimSpace(strings.Join(rest[end+1:], "\n")) != "" {
		return Value{}, fmt.Errorf("text after the code block")
	}
	return v, nil
}

// Check tells whether a string is a well-formed code value.
func Check(s string) error {
	_, err := Parse(s)
	return err
}

// Text is the source text of a value, "" when it does not read.
func Text(s string) string {
	v, err := Parse(s)
	if err != nil {
		return s
	}
	return v.Code
}

// openFence reads an opening fence of three backticks or tildes or more, and its info string's first word.
func openFence(line string) (byte, int, string) {
	if len(line) < 3 || (line[0] != '`' && line[0] != '~') {
		return 0, 0, ""
	}
	c := line[0]
	n := 0
	for n < len(line) && line[n] == c {
		n++
	}
	if n < 3 {
		return 0, 0, ""
	}
	info := strings.TrimSpace(line[n:])
	if c == '`' && strings.Contains(info, "`") {
		return 0, 0, ""
	}
	info, _, _ = strings.Cut(info, " ")
	return c, n, info
}

func closeFence(line string, c byte, n int) bool {
	line = strings.TrimRight(line, " \t")
	if len(line) < n {
		return false
	}
	for i := 0; i < len(line); i++ {
		if line[i] != c {
			return false
		}
	}
	return true
}
