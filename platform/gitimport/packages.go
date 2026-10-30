package gitimport

import (
	"path"
	"regexp"
	"strings"
	"unicode"
)

// pkgRef is a package: of the repository (the files of a directory, a declared Java package) or one it imports from
// outside.
type pkgRef struct {
	// lang is the language family (go, python, java, js, c, rust): C and C++ share their packages, so do JavaScript,
	// TypeScript and the Vue and Svelte components.
	lang string
	// path is its import path (Go), dotted path (Python, Java), directory or npm package (JavaScript), directory or
	// header (C), directory or crate (Rust).
	path string
	// name is the name the package gives itself (a Go package clause), else the last element of its path.
	name string
}

// family is the language family of the packages of a language.
func family(lang string) string {
	switch lang {
	case "cpp":
		return "c"
	case "javascript", "typescript", "tsx", "vue", "svelte":
		return "js"
	}
	return lang
}

// PackageKey is the key of a package of a repository: <repo>@<family>:<path>.
func PackageKey(repo, family, p string) string { return repo + "@" + family + ":" + p }

func (p pkgRef) key(repo string) string { return PackageKey(repo, p.lang, p.path) }

// layout is what resolving a package needs to know of the repository at a commit.
type layout struct {
	// exists tells whether the repository holds a file at a path.
	exists func(p string) bool
	// goModule gives the directory of the go.mod governing a directory and its module path ("" when none).
	goModule func(dir string) (root, module string)
}

func dirOf(p string) string {
	d := path.Dir(p)
	if d == "." || d == "/" {
		return ""
	}
	return d
}

func lastElem(p, sep string) string {
	if i := strings.LastIndex(p, sep); i >= 0 {
		return p[i+len(sep):]
	}
	return p
}

func dotted(dir string) string { return strings.ReplaceAll(dir, "/", ".") }

// packageOf is the package of the file at p.
func (l layout) packageOf(lang, p string, src Source) pkgRef {
	dir := dirOf(p)
	ref := pkgRef{lang: family(lang), path: dir}
	switch lang {
	case "go":
		if root, mod := l.goModule(dir); mod != "" {
			rel := strings.TrimPrefix(strings.TrimPrefix(dir, root), "/")
			ref.path = mod
			if rel != "" {
				ref.path += "/" + rel
			}
		}
	case "python":
		ref.path = dotted(dir)
	case "java":
		if src.Package != "" {
			ref.path = src.Package
		} else {
			ref.path = dotted(dir)
		}
	}
	ref.name = src.Package
	if lang != "go" || ref.name == "" {
		ref.name = lastElem(lastElem(ref.path, "/"), ".")
	}
	return ref
}

// resolved is an import resolved to a package.
type resolved struct {
	Import
	pkg pkgRef
	// file is the file of the repository the import names, when it names one (a header, a JavaScript module, a Python
	// module).
	file string
	// alias is the qualifier the file uses for the package ("." when its names are the file's own, "" when none).
	alias string
}

var goMajor = regexp.MustCompile(`^v[0-9]+$`)

// goName guesses the name of a Go package from its import path: its last element, the major version left out.
func goName(p string) string {
	parts := strings.Split(p, "/")
	n := parts[len(parts)-1]
	if goMajor.MatchString(n) && len(parts) > 1 {
		n = parts[len(parts)-2]
	}
	if i := strings.Index(n, "."); i > 0 {
		n = n[:i] // gopkg.in/yaml.v3
	}
	n = strings.TrimPrefix(n, "go-")
	return strings.ReplaceAll(n, "-", "_")
}

var jsExtensions = []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".d.ts"}

// resolve gives the package an import of the file at p names; false when it names none (a cgo import, an empty path).
func (l layout) resolve(lang, p string, im Import) (resolved, bool) {
	r := resolved{Import: im}
	dir := dirOf(p)
	fam := family(lang)
	in := func(path, file string) (resolved, bool) {
		r.pkg = pkgRef{lang: fam, path: path, name: lastElem(lastElem(path, "/"), ".")}
		r.file = file
		return r, true
	}
	switch lang {
	case "go":
		if im.Path == "" || im.Path == "C" {
			return r, false
		}
		r.alias = im.Alias
		if r.alias == "" {
			r.alias = goName(im.Path)
		}
		if r.alias == "_" {
			r.alias = ""
		}
		res, ok := in(im.Path, "")
		res.pkg.name = goName(im.Path)
		return res, ok
	case "python":
		mod := im.Path
		base := []string{"", "src"}
		if strings.HasPrefix(mod, ".") {
			n := len(mod) - len(strings.TrimLeft(mod, "."))
			d := dir
			for i := 1; i < n; i++ {
				d = dirOf(d)
			}
			mod = strings.TrimPrefix(mod, strings.Repeat(".", n))
			base = []string{d}
		}
		r.alias = im.Alias
		if r.alias == "*" {
			r.alias = "."
		}
		var parts []string
		if mod != "" {
			parts = strings.Split(mod, ".")
		}
		for _, b := range base {
			for i := len(parts); i >= 0; i-- {
				rel := path.Join(append([]string{b}, parts[:i]...)...)
				if l.exists(path.Join(rel, "__init__.py")) {
					return in(dotted(rel), path.Join(rel, "__init__.py"))
				}
				if i > 0 && l.exists(rel+".py") {
					return in(dotted(dirOf(rel)), rel+".py")
				}
				if i == 0 && strings.HasPrefix(im.Path, ".") {
					return in(dotted(rel), "") // a namespace package
				}
			}
		}
		if len(parts) == 0 {
			return r, false
		}
		return in(parts[0], "")
	case "java":
		parts := strings.Split(strings.TrimSuffix(im.Path, ".*"), ".")
		i := 0
		for i < len(parts) && (parts[i] == "" || !unicode.IsUpper(rune(parts[i][0]))) {
			i++
		}
		if i == 0 {
			return r, false
		}
		if i < len(parts) {
			r.Names = map[string]string{parts[i]: parts[i]}
		} else {
			r.alias = "." // a package's classes
		}
		return in(strings.Join(parts[:i], "."), "")
	case "javascript", "typescript", "tsx", "vue", "svelte":
		spec := im.Path
		r.alias = im.Alias
		if strings.HasPrefix(spec, ".") || strings.HasPrefix(spec, "/") {
			target := path.Clean(path.Join(dir, spec))
			if target == "." {
				target = ""
			}
			var cands []string
			if path.Ext(target) != "" {
				cands = append(cands, target)
			}
			for _, e := range jsExtensions {
				cands = append(cands, target+e)
			}
			for _, e := range jsExtensions {
				cands = append(cands, path.Join(target, "index"+e))
			}
			for _, c := range cands {
				if l.exists(c) {
					return in(dirOf(c), c)
				}
			}
			return in(target, "")
		}
		parts := strings.Split(spec, "/")
		if strings.HasPrefix(spec, "@") && len(parts) > 1 {
			return in(parts[0]+"/"+parts[1], "")
		}
		return in(parts[0], "")
	case "c", "cpp":
		var cands []string
		if !im.System {
			cands = append(cands, path.Join(dir, im.Path))
		}
		cands = append(cands, path.Clean(im.Path), path.Join("include", im.Path))
		if !im.System {
			cands = append(cands, path.Join("src", im.Path))
		}
		for _, c := range cands {
			if l.exists(c) {
				return in(dirOf(c), c)
			}
		}
		res, ok := in(im.Path, "")
		res.pkg.name = im.Path
		return res, ok
	case "rust":
		segs := strings.Split(im.Path, "::")
		var base string
		switch segs[0] {
		case "crate":
			base, segs = crateRoot(p), segs[1:]
		case "self":
			base, segs = dir, segs[1:]
		case "super":
			base = dir
			for len(segs) > 0 && segs[0] == "super" {
				base, segs = dirOf(base), segs[1:]
			}
		default:
			root := crateRoot(p)
			if !l.exists(path.Join(root, segs[0]+".rs")) && !l.exists(path.Join(root, segs[0], "mod.rs")) {
				if r.Alias == "*" {
					r.alias = "."
				}
				return in(segs[0], "") // a crate
			}
			base = root
		}
		if r.Alias == "*" {
			r.alias = "."
		}
		for i := len(segs); i >= 0; i-- {
			d := path.Join(append([]string{base}, segs[:i]...)...)
			file, pkg := "", d
			switch {
			case i > 0 && l.exists(d+".rs"):
				file, pkg = d+".rs", dirOf(d+".rs")
			case l.exists(path.Join(d, "mod.rs")):
				file = path.Join(d, "mod.rs")
			case i > 0:
				continue
			}
			if i == len(segs) && i > 0 && r.alias == "" {
				// the use names the module itself: its name qualifies what it holds
				r.alias = segs[i-1]
				for local := range im.Names {
					r.alias = local
				}
				r.Names = nil
			}
			return in(pkg, file)
		}
	}
	return r, false
}

// crateRoot is the source directory of the crate of a Rust file: its path up to its last src directory.
func crateRoot(p string) string {
	if i := strings.LastIndex(p, "/src/"); i >= 0 {
		return p[:i+4]
	}
	if strings.HasPrefix(p, "src/") {
		return "src"
	}
	return dirOf(p)
}

// goModulePath reads the module path of a go.mod.
func goModulePath(src []byte) string {
	for _, line := range strings.Split(string(src), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "module"); ok && (rest == "" || rest[0] == ' ' || rest[0] == '\t') {
			rest, _, _ = strings.Cut(rest, "//")
			return strings.Trim(strings.TrimSpace(rest), `"`+"`")
		}
	}
	return ""
}
