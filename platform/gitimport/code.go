package gitimport

import (
	"context"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/zimwip/goap/contracts/domain"
)

// relationTypes are the links a declaration holds besides its file's defines.
var relationTypes = kinds(LinkMemberOf, LinkCalls, LinkUses)

// planCode adds to the operations of a commit those its source files call for: the symbols of the files it adds,
// changes or removes and their links, the packages and the links of the files to them. Only what the commit changes
// changes: a declaration of another file naming a symbol the commit removes or changes keeps its link to the version it
// named, suspect from then on (ADR 0003), until its own text changes.
func (im *Importer) planCode(ctx context.Context, ops map[string]*op) error {
	repoPath := func(key string) string { return strings.TrimPrefix(key, im.Name+":") }
	// the go.mod files at the commit
	mods := maps.Clone(im.ix.gomods)
	for _, o := range ops {
		if o.typ == TypeFile && path.Base(repoPath(o.key)) == "go.mod" {
			if o.remove {
				delete(mods, dirOf(repoPath(o.key)))
			} else if o.props != nil {
				mods[dirOf(repoPath(o.key))], _ = o.props["blob"].(string)
			}
		}
	}
	lay := layout{
		exists: func(p string) bool {
			k := Key(im.Name, p, false)
			if o := ops[k]; o != nil {
				return !o.remove
			}
			e := im.ix.byKey[k]
			return e != nil && !e.removed && e.typ == TypeFile
		},
		goModule: func(dir string) (string, string) {
			for d := dir; ; d = dirOf(d) {
				if blob, ok := mods[d]; ok {
					return d, im.module(blob)
				}
				if d == "" {
					return "", ""
				}
			}
		},
	}
	// the files the commit changes, those to parse
	changed := map[string]*op{}
	removedFiles := map[string]bool{}
	var jobs []parseJob
	for _, o := range ops {
		if o.typ != TypeFile {
			continue
		}
		switch {
		case o.remove:
			removedFiles[o.key] = true
			changed[o.key] = o
		case o.props != nil:
			changed[o.key] = o
			if lang := im.sourceLanguage(o.props); lang != "" {
				blob, _ := o.props["blob"].(string)
				jobs = append(jobs, parseJob{key: o.key, path: repoPath(o.key), lang: lang, blob: blob})
			}
		}
	}
	slices.SortFunc(jobs, func(a, b parseJob) int { return strings.Compare(a.key, b.key) })
	files, err := im.parseAll(ctx, jobs)
	if err != nil {
		return err
	}
	// the symbols of the changed files
	symOp := func(k, typ, file string) *op {
		o := ops[k]
		if o == nil {
			o = &op{key: k, typ: typ, parent: file, depth: strings.Count(repoPath(file), "/") + 2, cur: im.ix.byKey[k]}
			ops[k] = o
		}
		return o
	}
	for _, f := range slices.Sorted(maps.Keys(changed)) {
		held := maps.Clone(im.ix.fileSyms[f])
		if p := files[f]; p != nil {
			p.keys = symbolKeys(f, p.src.Symbols)
			for i, s := range p.src.Symbols {
				k := p.keys[i]
				p.syms = append(p.syms, &symInfo{key: k, kind: s.Kind, name: s.Name, container: s.Container, file: f})
				props := map[string]any{"name": s.Name, "kind": s.Kind, "container": s.Container, "language": p.lang, "signature": s.Signature, "digest": s.Digest}
				if im.Code {
					props["code"] = s.Code
				}
				delete(held, k)
				o := symOp(k, symbolType(s.Kind), f)
				if o.cur != nil && !o.cur.removed && o.cur.sum == fingerprint(props) {
					continue // unchanged, unless its links change
				}
				o.props = props
			}
		}
		for k := range held {
			e := im.ix.byKey[k]
			if e == nil || e.removed {
				continue
			}
			symOp(k, e.typ, f).remove = true
		}
	}
	// the packages of the files and what they import
	pkgs := map[string]pkgRef{}   // the packages named, by key
	declared := map[string]bool{} // those a file of the commit is in
	sc := &scope{im: im, parsed: files, removed: removedFiles, pkgNew: map[string]string{}, files: map[string]*names{}, pkgs: map[string]*names{}}
	imports := map[string]map[string]bool{} // changed file → the packages it imports
	for _, f := range slices.Sorted(maps.Keys(files)) {
		p := files[f]
		p.pkg = lay.packageOf(p.lang, p.path, p.src)
		for _, i := range p.src.Imports {
			if r, ok := lay.resolve(p.lang, p.path, i); ok {
				p.imports = append(p.imports, r)
			}
		}
		k := p.pkg.key(im.Name)
		sc.pkgNew[f] = k
		pkgs[k], declared[k] = p.pkg, true
		imports[f] = map[string]bool{}
		for _, r := range p.imports {
			ik := r.pkg.key(im.Name)
			if ik == k {
				continue
			}
			imports[f][ik] = true
			if !declared[ik] {
				pkgs[ik] = r.pkg
			}
		}
	}
	for f := range changed {
		if _, ok := sc.pkgNew[f]; !ok {
			sc.pkgNew[f] = "" // removed, or no source any more
		}
	}
	// the links of the declarations
	for _, f := range slices.Sorted(maps.Keys(files)) {
		p := files[f]
		rel := sc.relations(p)
		for _, s := range p.syms {
			o := ops[s.key]
			if o != nil && o.remove {
				continue
			}
			var cur *entry
			if o != nil {
				cur = o.cur
			} else {
				cur = im.ix.byKey[s.key]
			}
			add, drop := im.linkDiff(cur, rel[s.key], relationTypes, o != nil && o.props != nil)
			switch {
			case o == nil && len(add)+len(drop) == 0:
				continue
			case o == nil:
				o = symOp(s.key, symbolType(s.kind), f)
			case o.props == nil && len(add)+len(drop) == 0:
				delete(ops, s.key) // unchanged: the file's new version keeps its link
				continue
			}
			o.add, o.drop = append(o.add, add...), append(o.drop, drop...)
		}
	}
	// the links of the changed files to their packages
	touched := map[string]bool{}
	for f, o := range changed {
		if o.cur != nil {
			for _, l := range o.cur.links {
				if l.typ == LinkInPackage || l.typ == LinkImports {
					touched[im.ix.keyOf[l.to]] = true
				}
			}
		}
		if o.remove {
			continue
		}
		set := map[want]bool{}
		if k := sc.pkgNew[f]; k != "" {
			set[want{LinkInPackage, k}] = true
			touched[k] = true
		}
		for k := range imports[f] {
			set[want{LinkImports, k}] = true
			touched[k] = true
		}
		add, drop := im.linkDiff(o.cur, set, kinds(LinkInPackage, LinkImports), true)
		o.add, o.drop = append(o.add, add...), append(o.drop, drop...)
	}
	// the packages: present while a file is in them or imports them, depending on what their files import
	for _, k := range slices.Sorted(maps.Keys(touched)) {
		cur := im.ix.byKey[k]
		members := sc.pkgFiles(k)
		importers := 0
		if cur != nil {
			for lid, from := range im.ix.into[cur.id] {
				fk := im.ix.keyOf[from]
				if _, ok := changed[fk]; ok {
					continue
				}
				if fe := im.ix.byKey[fk]; fe != nil && !fe.removed && fe.links[lid].typ == LinkImports {
					importers++
				}
			}
		}
		for f := range imports {
			if imports[f][k] {
				importers++
			}
		}
		if len(members)+importers == 0 {
			if cur != nil && !cur.removed {
				ops[k] = &op{key: k, typ: TypePackage, pkg: true, depth: pkgDepth, cur: cur, remove: true}
			}
			continue
		}
		deps := map[want]bool{}
		for _, f := range members {
			if set, ok := imports[f]; ok {
				for d := range set {
					deps[want{LinkDependsOn, d}] = true
				}
				continue
			}
			if fe := im.ix.byKey[f]; fe != nil {
				for _, l := range fe.links {
					if d := im.ix.keyOf[l.to]; l.typ == LinkImports && d != k {
						deps[want{LinkDependsOn, d}] = true
					}
				}
			}
		}
		ref := pkgs[k]
		if !declared[k] && cur != nil {
			ref = pkgRef{name: cur.name} // what the package says of itself stays
		}
		lang, p, _ := strings.Cut(strings.TrimPrefix(k, im.Name+"@"), ":")
		props := map[string]any{"name": ref.name, "path": p, "language": lang}
		if ref.name == "" {
			props["name"] = lastElem(lastElem(p, "/"), ".")
			if props["name"] == "" {
				props["name"] = im.Name
			}
		}
		add, drop := im.linkDiff(cur, deps, kinds(LinkDependsOn), false)
		o := &op{key: k, typ: TypePackage, pkg: true, depth: pkgDepth, cur: cur, add: add, drop: drop}
		if cur == nil || cur.removed || cur.sum != pkgFingerprint(props) {
			o.props = props
		} else if len(add)+len(drop) == 0 {
			continue
		}
		ops[k] = o
	}
	return nil
}

// pkgDepth puts the packages first: the files link to them.
const pkgDepth = 1 << 20

// linkDiff compares the links of some types a node holds with the ones it should: the links to add, those to drop.
// A link to another version of its target than the one at the head is renewed when the node changes anyway (refresh)
// or its links do.
func (im *Importer) linkDiff(cur *entry, set map[want]bool, types map[string]bool, refresh bool) (add []want, drop []domain.LinkID) {
	held := map[want]domain.LinkID{}
	stale := map[want]bool{}
	if cur != nil && !cur.removed {
		for lid, l := range cur.links {
			if !types[l.typ] {
				continue
			}
			w := want{l.typ, im.ix.keyOf[l.to]}
			if !set[w] {
				drop = append(drop, lid)
				continue
			}
			held[w] = lid
			if t := im.ix.byKey[w.key]; t != nil && t.ver != l.ver {
				stale[w] = true
			}
		}
	}
	for w := range set {
		if _, ok := held[w]; !ok {
			add = append(add, w)
		}
	}
	if refresh || len(add)+len(drop) > 0 {
		for w := range stale {
			drop = append(drop, held[w])
			add = append(add, w)
		}
	}
	return add, drop
}

// sourceLanguage is the language of a file the extractor reads, "" when it is left out (no language, not a regular
// file, too large).
func (im *Importer) sourceLanguage(props map[string]any) string {
	name, _ := props["name"].(string)
	mode, _ := props["mode"].(string)
	size, _ := props["size"].(int64)
	lang := im.Symbols.Language(name)
	if lang == "" || (mode != "100644" && mode != "100755") || size > im.MaxSymbolBytes {
		return ""
	}
	return lang
}

// module is the module path of a go.mod, by its blob.
func (im *Importer) module(blob string) string {
	if m, ok := im.modules[blob]; ok {
		return m
	}
	src, err := im.blobs.Read(blob)
	m := ""
	if err == nil {
		m = goModulePath(src)
	}
	if im.modules == nil {
		im.modules = map[string]string{}
	}
	im.modules[blob] = m
	return m
}
