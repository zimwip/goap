package gitimport

import (
	"testing"
)

// TestPackages resolves the package of a file and of its imports in each language.
func TestPackages(t *testing.T) {
	files := map[string]bool{
		"pkg/__init__.py": true, "pkg/util.py": true, "pkg/sub/mod.py": true, "src/app/core.py": true,
		"web/lib/a.ts": true, "web/lib/b/index.js": true,
		"include/util.h": true, "src/x/local.h": true,
		"src/model.rs": true, "src/net/mod.rs": true, "src/net/http.rs": true,
	}
	l := layout{
		exists: func(p string) bool { return files[p] },
		goModule: func(dir string) (string, string) {
			if dir == "tools" || len(dir) > 6 && dir[:6] == "tools/" {
				return "tools", "example.com/tools"
			}
			return "", "example.com/demo"
		},
	}
	for _, c := range []struct {
		lang, file string
		src        Source
		want       string // family:path name
	}{
		{"go", "shapes/point.go", Source{Package: "shapes"}, "go:example.com/demo/shapes shapes"},
		{"go", "main.go", Source{Package: "main"}, "go:example.com/demo main"},
		{"go", "tools/gen/x.go", Source{Package: "gen"}, "go:example.com/tools/gen gen"},
		{"python", "pkg/sub/mod.py", Source{}, "python:pkg.sub sub"},
		{"java", "src/main/java/com/acme/A.java", Source{Package: "com.acme"}, "java:com.acme acme"},
		{"typescript", "web/lib/a.ts", Source{}, "js:web/lib lib"},
		{"cpp", "src/x/y.cpp", Source{}, "c:src/x x"},
	} {
		p := l.packageOf(c.lang, c.file, c.src)
		if got := p.lang + ":" + p.path + " " + p.name; got != c.want {
			t.Errorf("%s: %q, want %q", c.file, got, c.want)
		}
	}
	for _, c := range []struct {
		lang, file string
		im         Import
		want       string // family:path file alias
	}{
		{"go", "main.go", Import{Path: "example.com/demo/shapes"}, "go:example.com/demo/shapes  shapes"},
		{"go", "main.go", Import{Path: "gopkg.in/yaml.v3"}, "go:gopkg.in/yaml.v3  yaml"},
		{"go", "main.go", Import{Path: "github.com/x/y/v2", Alias: "z"}, "go:github.com/x/y/v2  z"},
		{"python", "pkg/sub/mod.py", Import{Path: "..util"}, "python:pkg pkg/util.py "},
		{"python", "pkg/sub/mod.py", Import{Path: "pkg.util", Alias: "pkg.util"}, "python:pkg pkg/util.py pkg.util"},
		{"python", "pkg/sub/mod.py", Import{Path: "pkg"}, "python:pkg pkg/__init__.py "},
		{"python", "x.py", Import{Path: "app.core"}, "python:src.app src/app/core.py "},
		{"python", "x.py", Import{Path: "numpy.linalg", Alias: "la"}, "python:numpy  la"},
		{"java", "A.java", Import{Path: "java.util.List"}, "java:java.util  "},
		{"java", "A.java", Import{Path: "com.acme.model.*"}, "java:com.acme.model  ."},
		{"java", "A.java", Import{Path: "java.lang.Math.max"}, "java:java.lang  "},
		{"typescript", "web/main.ts", Import{Path: "./lib/a"}, "js:web/lib web/lib/a.ts "},
		{"typescript", "web/main.ts", Import{Path: "./lib/b"}, "js:web/lib/b web/lib/b/index.js "},
		{"typescript", "web/main.ts", Import{Path: "@scope/pkg/deep", Alias: "p"}, "js:@scope/pkg  p"},
		{"javascript", "web/main.js", Import{Path: "react"}, "js:react  "},
		{"c", "src/x/y.c", Import{Path: "local.h"}, "c:src/x src/x/local.h "},
		{"c", "src/x/y.c", Import{Path: "util.h"}, "c:include include/util.h "},
		{"c", "src/x/y.c", Import{Path: "stdio.h", System: true}, "c:stdio.h  "},
		{"rust", "src/main.rs", Import{Path: "crate::model::Thing"}, "rust:src src/model.rs "},
		{"rust", "src/main.rs", Import{Path: "crate::net::http", Names: map[string]string{"http": "http"}}, "rust:src/net src/net/http.rs http"},
		{"rust", "src/main.rs", Import{Path: "crate::net"}, "rust:src/net src/net/mod.rs net"},
		{"rust", "src/main.rs", Import{Path: "std::collections::HashMap"}, "rust:std  "},
	} {
		r, ok := l.resolve(c.lang, c.file, c.im)
		if !ok {
			t.Errorf("%s %s: unresolved", c.file, c.im.Path)
			continue
		}
		if got := r.pkg.lang + ":" + r.pkg.path + " " + r.file + " " + r.alias; got != c.want {
			t.Errorf("%s %s: %q, want %q", c.file, c.im.Path, got, c.want)
		}
	}
	if _, ok := l.resolve("go", "main.go", Import{Path: "C"}); ok {
		t.Error("cgo's C is no package")
	}
	if m := goModulePath([]byte("// x\nmodule  \"example.com/m\" // the module\n")); m != "example.com/m" {
		t.Errorf("module %q", m)
	}
}
