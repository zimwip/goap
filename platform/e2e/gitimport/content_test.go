package gitimporte2e

import (
	"context"
	"strings"
	"testing"

	"github.com/zimwip/goap/contracts/codetext"
	"github.com/zimwip/goap/platform/gitimport"
)

// TestImportKeepsTheText keeps the text of the files that read as text, whatever their name, decoded to UTF-8, and
// names the others by the address of their content (ADR 0171).
func TestImportKeepsTheText(t *testing.T) {
	ctx := context.Background()
	g := newGraph(t)
	r := newRepo(t)
	r.write("README", "hello\n")
	r.write("main.go", "package main\n\nfunc main() {}\n")
	r.write("legacy.dat", "caf\xe9 cr\xe8me\n")                                // Windows-1252, a name that says nothing
	r.write("logo.png", "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01") // a blob
	r.write("big.txt", strings.Repeat("a line of text\n", 100))
	r.commit("Initial")
	im := &gitimport.Importer{Graph: g, Dir: r.dir, Name: "demo", ProjectID: project, Text: true, MaxTextBytes: 1000,
		ContentURL: gitimport.ContentURLOf("https://forge.example/{name}/raw/{commit}/{path}", r.dir, "demo")}
	if _, err := im.Run(ctx); err != nil {
		t.Fatal(err)
	}
	nodes, _ := codeGraph(t, g)
	head := strings.TrimSpace(r.git("rev-parse", "HEAD"))
	for _, c := range []struct {
		key, text, lang, encoding, media string
		binary                           bool
	}{
		{"demo:README", "hello", "", "utf-8", "text/plain", false},
		{"demo:main.go", "package main\n\nfunc main() {}", "go", "utf-8", "text/x-go", false},
		{"demo:legacy.dat", "café crème", "", "windows-1252", "text/plain", false},
		{"demo:logo.png", "", "", "", "image/png", true},
		{"demo:big.txt", "", "", "utf-8", "text/plain", false},
	} {
		n, ok := nodes[c.key]
		if !ok {
			t.Fatalf("%s: not imported", c.key)
		}
		p := n.Properties
		if p["binary"] != c.binary || p["media"] != c.media || (p["encoding"] != nil && p["encoding"] != c.encoding) {
			t.Errorf("%s: binary %v, media %v, encoding %v", c.key, p["binary"], p["media"], p["encoding"])
		}
		content, has := p["content"].(string)
		if has != (c.text != "") {
			t.Errorf("%s: content %q", c.key, content)
			continue
		}
		if has {
			v, err := codetext.Parse(content)
			if err != nil || v.Code != c.text || v.Language != c.lang {
				t.Errorf("%s: %+v %v", c.key, v, err)
			}
		}
		name := strings.TrimPrefix(c.key, "demo:")
		if want := "https://forge.example/demo/raw/" + head + "/" + name; p["contentUrl"] != want {
			t.Errorf("%s: contentUrl %v, want %s", c.key, p["contentUrl"], want)
		}
	}
}

func TestContentURLOf(t *testing.T) {
	for _, c := range []struct{ source, want string }{
		{"https://github.com/zimwip/goap.git", "https://raw.githubusercontent.com/zimwip/goap/abc/a%20b/c.go"},
		{"git@github.com:zimwip/goap.git", "https://raw.githubusercontent.com/zimwip/goap/abc/a%20b/c.go"},
		{"https://gitlab.com/group/sub/proj", "https://gitlab.com/group/sub/proj/-/raw/abc/a%20b/c.go"},
		{"https://git.internal/x/y.git", ""},
	} {
		f := gitimport.ContentURLOf("", c.source, "x")
		got := ""
		if f != nil {
			got = f("abc", "a b/c.go", "blob")
		}
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.source, got, c.want)
		}
	}
	r := newRepo(t)
	if f := gitimport.ContentURLOf("", r.dir, "x"); f != nil {
		t.Error("a local directory: an address, want none (no file:// address)")
	}
	if f := gitimport.ContentURLOf("lnk:repos/File/{name}/{path}", r.dir, "x"); f == nil || f("abc", "a/b c.go", "") != "lnk:repos/File/x/a/b%20c.go" {
		t.Error("a lnk: template: not applied")
	}
}
