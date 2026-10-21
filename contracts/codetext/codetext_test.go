package codetext

import (
	"reflect"
	"strings"
	"testing"
)

func TestFormat(t *testing.T) {
	got := Format(Value{Language: "go", Code: "func F() {\n\treturn\n}"})
	want := "---\nlanguage: go\n---\n```go\nfunc F() {\n\treturn\n}\n```\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// a fence longer than the backticks of the text, the other keys sorted and quoted when needed
	got = Format(Value{Language: "markdown", Meta: map[string]string{"path": "a b.md", "note": " x"}, Code: "````js\nx\n````"})
	want = "---\nlanguage: markdown\nnote: \" x\"\npath: a b.md\n---\n`````markdown\n````js\nx\n````\n`````\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRoundTrip(t *testing.T) {
	for _, v := range []Value{
		{Language: "go", Code: "package p"},
		{Language: "python", Code: "def f():\n    ```\n    pass"},
		{Code: ""},
		{Language: "c", Meta: map[string]string{"file": "x.c", "quote": `say "hi"`}, Code: "int x;\n\n"},
	} {
		got, err := Parse(Format(v))
		if err != nil {
			t.Fatalf("%+v: %v", v, err)
		}
		v.Code = strings.TrimSuffix(v.Code, "\n")
		if !reflect.DeepEqual(got, v) {
			t.Errorf("got %+v, want %+v", got, v)
		}
	}
}

func TestParse(t *testing.T) {
	for _, c := range []struct {
		in   string
		want Value
	}{
		{"plain text\nline 2", Value{Code: "plain text\nline 2"}},
		{"```rust\nfn main() {}\n```", Value{Language: "rust", Code: "fn main() {}"}},
		{"---\nlanguage: go\n---\nfunc F() {}", Value{Language: "go", Code: "func F() {}"}},
		{"---\r\nlanguage: 'it''s'\r\n---\r\n\r\n~~~\r\nx\r\n~~~\r\n", Value{Language: "it's", Code: "x"}},
		{"---\nlanguage: go\n---\n```\nunclosed", Value{Language: "go", Code: "unclosed"}},
		{"---\n---", Value{}},
	} {
		got, err := Parse(c.in)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: got %+v, want %+v", c.in, got, c.want)
		}
	}
	for _, bad := range []string{"---\nlanguage: go\n", "---\nnot a pair\n---\nx", "---\n---\n```\nx\n```\ntrailing", "---\nk: \"open\n---\n"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q: accepted", bad)
		}
	}
}
