package content

import (
	"bytes"
	"strings"
	"testing"
)

func TestSniff(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	for _, c := range []struct {
		name, data       string
		text             bool
		enc, media, lang string
	}{
		{"main.go", "package main\n\nfunc main() {}\n", true, UTF8, "text/x-go", "go"},
		{"README", "Read me.\n", true, UTF8, "text/plain", ""},
		{"empty.txt", "", true, UTF8, "text/plain", ""},
		{"Makefile", "all:\n\tgo build\n", true, UTF8, "text/x-makefile", "makefile"},
		{"notes.txt", "caf\xe9 cr\xe8me \x80\n", true, Windows1252, "text/plain", ""},
		{"bom.txt", "\xef\xbb\xbfhello", true, UTF8, "text/plain", ""},
		{"wide.txt", "\xff\xfeh\x00i\x00", true, UTF16LE, "text/plain", ""},
		{"logo.png", string(png), false, "", "image/png", ""},
		{"data.bin", "abc\x00def", false, "", Octets, ""},
		{"blob.txt", "abc\x00def", false, "", Octets, ""},
		{"doc.pdf", "%PDF-1.7\n%\xe2\xe3\xcf\xd3\n", false, "", "application/pdf", ""},
		{"icon.svg", `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`, true, UTF8, "image/svg+xml", "xml"},
		{"weird.dat", "a\x01b\x02c\x03", false, "", Octets, ""},
		{"colour.log", "\x1b[31mred\x1b[0m\n", true, UTF8, "text/plain", ""},
	} {
		got := Sniff(c.name, []byte(c.data))
		if got.Text != c.text || got.Encoding != c.enc || got.Media != c.media || got.Language != c.lang {
			t.Errorf("%s: got %+v, want text %v %s %s %q", c.name, got, c.text, c.enc, c.media, c.lang)
		}
	}
}

func TestSniffCutSequence(t *testing.T) {
	// a long UTF-8 text cut in the middle of "é" at SniffBytes stays text
	data := []byte(strings.Repeat("a", SniffBytes-1) + "é and more")
	if got := Sniff("long.txt", data); !got.Text || got.Encoding != UTF8 {
		t.Fatalf("got %+v", got)
	}
}

func TestDecode(t *testing.T) {
	for _, c := range []struct{ name, data, want string }{
		{"a.txt", "caf\xe9 \x80", "café €"},
		{"b.txt", "\xef\xbb\xbfhello", "hello"},
		{"c.txt", "\xff\xfeh\x00\xe9\x00", "hé"},
		{"d.txt", "\xfe\xff\x00h\x00\xe9", "hé"},
		{"e.txt", "\xff\xfe\x00\x00h\x00\x00\x00", "h"},
	} {
		info := Sniff(c.name, []byte(c.data))
		got, ok := Decode([]byte(c.data), info)
		if !ok || got != c.want {
			t.Errorf("%s: got %q %v (%+v), want %q", c.name, got, ok, info, c.want)
		}
	}
	if _, ok := Decode(bytes.Repeat([]byte{0}, 4), Sniff("z", bytes.Repeat([]byte{0}, 4))); ok {
		t.Fatal("a blob decoded")
	}
}
