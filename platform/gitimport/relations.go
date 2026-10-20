package gitimport

import (
	"slices"
	"strings"
)

// symInfo is a declaration as the resolution of the names sees it.
type symInfo struct {
	key, kind, name, container string
	file                       string // the key of its file
}

func (s *symInfo) qualified() string { return qualified(s.container, s.name) }

// parsed is a source file read at a commit.
type parsed struct {
	key, path, lang string
	src             Source
	keys            []string // the keys of its symbols
	syms            []*symInfo
	pkg             pkgRef
	imports         []resolved
}

// names are the declarations of a file or a package by name: the top level ones, and the members by container.
type names struct {
	top     map[string][]*symInfo
	members map[[2]string][]*symInfo // container, name
}

func namesOf(syms []*symInfo) *names {
	n := &names{top: map[string][]*symInfo{}, members: map[[2]string][]*symInfo{}}
	for _, s := range syms {
		if s.container == "" {
			n.top[s.name] = append(n.top[s.name], s)
		} else {
			n.members[[2]string{s.container, s.name}] = append(n.members[[2]string{s.container, s.name}], s)
		}
	}
	return n
}

// The kinds a reference may name.
var (
	callable  = kinds("function", "method", "prototype", "class", "struct", "macro")
	typeKinds = kinds("struct", "union", "class", "interface", "enum", "type")
	nameKinds = kinds("variable", "constant", "macro", "field")
	holders   = kinds("class", "struct", "union", "interface", "enum", "type", "namespace", "module")
	refKinds  = map[string]map[string]bool{RefCall: callable, RefType: typeKinds, RefName: nameKinds}
	selfNames = kinds("self", "this", "cls", "Self")
)

func kinds(ks ...string) map[string]bool {
	m := map[string]bool{}
	for _, k := range ks {
		m[k] = true
	}
	return m
}

// sharedScope: the languages whose files of a package see each other's declarations without an import.
var sharedScope = kinds("go", "java", "c", "cpp")

// implicitThis: the languages whose methods name the members of their class without this.
var implicitThis = kinds("java", "cpp")

// maxTargets: a name resolving to more declarations than that is left unresolved (too ambiguous to link).
const maxTargets = 4

// scope is what the resolution of the names of a commit sees: the declarations of the files the commit read, the
// others' as the graph holds them.
type scope struct {
	im      *Importer
	parsed  map[string]*parsed
	removed map[string]bool   // the files the commit removes
	pkgNew  map[string]string // file → the key of its package, for the files the commit changed
	files   map[string]*names
	pkgs    map[string]*names
}

// fileSyms are the declarations of a file at the commit.
func (s *scope) fileSyms(file string) []*symInfo {
	if p, ok := s.parsed[file]; ok {
		return p.syms
	}
	if s.removed[file] {
		return nil
	}
	var out []*symInfo
	for k := range s.im.ix.fileSyms[file] {
		if e := s.im.ix.byKey[k]; e != nil && e.sym != nil && !e.removed {
			out = append(out, e.sym)
		}
	}
	return out
}

// filePkg is the key of the package of a file at the commit.
func (s *scope) filePkg(file string) string {
	if p, ok := s.pkgNew[file]; ok {
		return p
	}
	e := s.im.ix.byKey[file]
	if e == nil || e.removed {
		return ""
	}
	for _, l := range e.links {
		if l.typ == LinkInPackage {
			return s.im.ix.keyOf[l.to]
		}
	}
	return ""
}

// pkgFiles are the files of a package at the commit.
func (s *scope) pkgFiles(pkg string) []string {
	var out []string
	if e := s.im.ix.byKey[pkg]; e != nil {
		for lid, from := range s.im.ix.into[e.id] {
			f := s.im.ix.keyOf[from]
			if _, changed := s.pkgNew[f]; changed || s.removed[f] {
				continue
			}
			if fe := s.im.ix.byKey[f]; fe != nil && fe.links[lid].typ == LinkInPackage {
				out = append(out, f)
			}
		}
	}
	for f, p := range s.pkgNew {
		if p == pkg {
			out = append(out, f)
		}
	}
	slices.Sort(out)
	return out
}

func (s *scope) fileNames(file string) *names {
	n, ok := s.files[file]
	if !ok {
		n = namesOf(s.fileSyms(file))
		s.files[file] = n
	}
	return n
}

func (s *scope) pkgNames(pkg string) *names {
	n, ok := s.pkgs[pkg]
	if !ok {
		var syms []*symInfo
		for _, f := range s.pkgFiles(pkg) {
			syms = append(syms, s.fileSyms(f)...)
		}
		n = namesOf(syms)
		s.pkgs[pkg] = n
	}
	return n
}

// pick keeps the declarations of the kinds wanted, but the one resolving.
func pick(cands []*symInfo, want map[string]bool, self string) []*symInfo {
	var out []*symInfo
	for _, c := range cands {
		if want[c.kind] && c.key != self {
			out = append(out, c)
		}
	}
	return out
}

// lookup finds a name among the top level declarations of a file, then of its package.
func (s *scope) lookup(file, pkg, name string, want map[string]bool, self string) []*symInfo {
	if file != "" {
		if got := pick(s.fileNames(file).top[name], want, self); len(got) > 0 {
			return got
		}
	}
	if pkg != "" {
		return pick(s.pkgNames(pkg).top[name], want, self)
	}
	return nil
}

// members finds a member of a container among the declarations of a file, then of a package.
func (s *scope) members(file, pkg, container, name string, want map[string]bool, self string) []*symInfo {
	k := [2]string{container, name}
	if file != "" {
		if got := pick(s.fileNames(file).members[k], want, self); len(got) > 0 {
			return got
		}
	}
	if pkg != "" {
		return pick(s.pkgNames(pkg).members[k], want, self)
	}
	return nil
}

// resolve finds what a name of a declaration of a file names: through the import its qualifier names, the members of
// the declaration's class, the file, the names it imports, the files it includes, its package.
func (s *scope) resolve(f *parsed, from *symInfo, self string, r Ref) []*symInfo {
	want := refKinds[r.Kind]
	if want == nil {
		return nil
	}
	pkg := f.pkg.key(s.im.Name)
	if r.Qualifier != "" {
		q := r.Qualifier
		if selfNames[q] || (self != "" && q == self) {
			c := from.container
			if holders[from.kind] {
				c = from.qualified()
			}
			if c == "" {
				return nil
			}
			return s.members(f.key, pkg, c, r.Name, want, from.key)
		}
		for _, im := range f.imports {
			if im.alias != "" && im.alias != "." && im.alias == q {
				return s.lookup(im.file, im.pkg.key(s.im.Name), r.Name, want, from.key)
			}
		}
		// a static member of a type (T::new, T.of)
		for _, t := range s.unqualified(f, from, pkg, q, typeKinds, "") {
			if got := s.members(t.file, s.filePkg(t.file), t.qualified(), r.Name, want, from.key); len(got) > 0 {
				return got
			}
		}
		return nil
	}
	return s.unqualified(f, from, pkg, r.Name, want, from.key)
}

func (s *scope) unqualified(f *parsed, from *symInfo, pkg, name string, want map[string]bool, self string) []*symInfo {
	if from != nil && from.container != "" && implicitThis[f.lang] {
		if got := s.members(f.key, pkg, from.container, name, want, self); len(got) > 0 {
			return got
		}
	}
	if got := pick(s.fileNames(f.key).top[name], want, self); len(got) > 0 {
		return got
	}
	for _, im := range f.imports {
		if remote, ok := im.Names[name]; ok {
			if got := s.lookup(im.file, im.pkg.key(s.im.Name), remote, want, self); len(got) > 0 {
				return got
			}
		}
	}
	for _, im := range f.imports {
		if im.alias == "." || (im.file != "" && (f.lang == "c" || f.lang == "cpp")) {
			if got := s.lookup(im.file, im.pkg.key(s.im.Name), name, want, self); len(got) > 0 {
				return got
			}
		}
	}
	if sharedScope[f.lang] {
		return s.lookup("", pkg, name, want, self)
	}
	return nil
}

// holder finds the declaration a member is declared in: in its file, then its package (a Go method of a type of
// another file of the package, a C++ method defined out of its class).
func (s *scope) holder(f *parsed, sym *symInfo) []*symInfo {
	parent, name := "", sym.container
	if i := strings.LastIndex(name, "."); i >= 0 {
		parent, name = name[:i], name[i+1:]
	}
	pkg := f.pkg.key(s.im.Name)
	if parent == "" {
		if got := s.unqualified(f, nil, pkg, name, holders, sym.key); len(got) > 0 {
			return got
		}
		return s.lookup("", pkg, name, holders, sym.key)
	}
	return s.members(f.key, pkg, parent, name, holders, sym.key)
}

// want is a link an operation wants its node to hold: its type and the key of its target.
type want struct{ typ, key string }

// relations are the links of each declaration of a file: member_of its holder, calls, uses.
func (s *scope) relations(f *parsed) map[string]map[want]bool {
	out := make(map[string]map[want]bool, len(f.syms))
	for i, sym := range f.syms {
		set := map[want]bool{}
		var holder string
		if sym.container != "" {
			if got := s.holder(f, sym); len(got) > 0 && len(got) <= maxTargets {
				holder = got[0].key
				set[want{LinkMemberOf, holder}] = true
			}
		}
		src := f.src.Symbols[i]
		for _, r := range src.Refs {
			got := s.resolve(f, sym, src.Self, r)
			if len(got) == 0 || len(got) > maxTargets {
				continue
			}
			for _, t := range got {
				typ := LinkUses
				if r.Kind == RefCall && callable[t.kind] {
					typ = LinkCalls
				}
				set[want{typ, t.key}] = true
			}
		}
		for w := range set {
			if w.typ == LinkUses && (w.key == holder || set[want{LinkCalls, w.key}]) {
				delete(set, w)
			}
		}
		out[sym.key] = set
	}
	return out
}
