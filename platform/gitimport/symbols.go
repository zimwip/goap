package gitimport

import (
	"crypto/sha1"
	"encoding/hex"
	"path"
	"strconv"
	"strings"
)

// Source is what the symbol extractor reads in a source file: its package clause, its imports and its declarations.
type Source struct {
	// Package is the package the file declares (a Go package clause, a Java package), empty when its language has none.
	Package string
	// Language is the language of the declarations when it is not the file's: the script of a Vue or Svelte
	// component (javascript, typescript, tsx).
	Language string
	Imports  []Import
	Symbols  []Symbol
}

// Import is a dependency a file names (import, #include, use), as written: the importer resolves it to a package.
type Import struct {
	// Path is the imported path as written (a Go import path, a Python module, ./x for JavaScript, a header).
	Path string
	// Alias is the name the file uses for it ("" for the default one; Go "." and "_" as written).
	Alias string
	// Names are the names it takes from it, by the local name it gives them (Python from x import a as b: b → a).
	Names map[string]string
	// System marks an #include <...>.
	System bool
}

// Symbol is a declaration a source file holds (a function, a type, a variable...), as the symbol extractor finds it.
type Symbol struct {
	// Kind is one of the symbol kinds of the code domain (enum symbol-kind): function, method, prototype, struct,
	// union, class, interface, enum, type, namespace, module, variable, constant, field, macro.
	Kind string
	Name string
	// Container is the qualified name of the declaration it is declared in (a class for a method, the receiver type of
	// a Go method), empty at the top level.
	Container string
	// Signature is the first line of its text, Digest a digest of its whole text: a symbol gets a new version when its
	// own text changes, not when lines move around it.
	Signature, Digest string
	// Code is its text, cut at MaxCodeBytes.
	Code string
	// Self is the name its body gives the object it is a member of, besides self and this (a Go receiver).
	Self string
	// Refs are the names its text calls or uses (its members' left out: they have theirs), at most once each.
	Refs []Ref
	// Start and End are its bytes in the file.
	Start, End uint
}

// Ref is a name a declaration calls or uses, resolved by the importer against the declarations it can see.
type Ref struct {
	// Kind is call (a call, a new), type (a type named) or name (a variable or a constant named).
	Kind string
	Name string
	// Qualifier is what the name is selected from (pkg in pkg.F, self in self.f, the type in T::new), empty when
	// none.
	Qualifier string
}

// The kinds of reference.
const (
	RefCall = "call"
	RefType = "type"
	RefName = "name"
)

// MaxCodeBytes bounds the source text kept on a symbol.
const MaxCodeBytes = 64 << 10

// Extractor reads the source files. Parse may be called from several goroutines at once.
type Extractor interface {
	// Language names the language of a file by its name, "" when the extractor reads none.
	Language(name string) string
	Parse(language string, src []byte) (Source, error)
}

// languages maps the extensions to the languages the Tree-sitter extractor reads.
var languages = map[string]string{
	"c": "c", "h": "c",
	"cc": "cpp", "cpp": "cpp", "cxx": "cpp", "hh": "cpp", "hpp": "cpp", "hxx": "cpp",
	"go":   "go",
	"py":   "python",
	"java": "java",
	"js":   "javascript", "mjs": "javascript", "cjs": "javascript", "jsx": "javascript",
	"ts": "typescript", "tsx": "tsx", "mts": "typescript", "cts": "typescript",
	"vue": "vue", "svelte": "svelte",
	"rs": "rust",
}

func languageOf(name string) string {
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
	return languages[ext]
}

// symbolOf completes a symbol from its text.
func symbolOf(kind, name, container string, text []byte) Symbol {
	sig, _, _ := strings.Cut(string(text), "\n")
	sum := sha1.Sum(text)
	return Symbol{Kind: kind, Name: name, Container: container, Signature: capText(sig, 200), Digest: hex.EncodeToString(sum[:10]),
		Code: capCode(string(text), MaxCodeBytes)}
}

// ownText is the text of a declaration, src[start:end], without the declarations it holds (held: their spans in src,
// in order, the outermost only), the lines they leave blank dropped: a container (a class, a struct) gets a new
// version when its own text changes, not when a member's does, the member having its own version.
func ownText(src []byte, start, end uint, held [][2]uint) []byte {
	var b []byte
	at := start
	for _, h := range held {
		b = append(b, src[at:h[0]]...)
		at = h[1]
	}
	b = append(b, src[at:end]...)
	lines := strings.Split(string(b), "\n")
	kept := lines[:0]
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			kept = append(kept, l)
		}
	}
	return []byte(strings.Join(kept, "\n"))
}

// leadOf is the indentation of the line a declaration starts at, src[line start:start], "" when something else than
// spaces and tabs is before it on its line.
func leadOf(src []byte, start uint) string {
	i := int(start)
	for i > 0 && (src[i-1] == ' ' || src[i-1] == '\t') {
		i--
	}
	if i > 0 && src[i-1] != '\n' {
		return ""
	}
	return string(src[i:start])
}

// dedent removes from the lines of a text the indentation they all share (the blank lines aside): the text of a
// declaration given its first line's indentation (leadOf) reads as if it were declared at the top level.
func dedent(s string) string {
	lines := strings.Split(s, "\n")
	common, first := "", true
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		ind := l[:len(l)-len(strings.TrimLeft(l, " \t"))]
		if first {
			common, first = ind, false
			continue
		}
		n := 0
		for n < len(common) && n < len(ind) && common[n] == ind[n] {
			n++
		}
		common = common[:n]
	}
	if common == "" {
		return s
	}
	for i, l := range lines {
		lines[i] = strings.TrimPrefix(l, common)
	}
	return strings.Join(lines, "\n")
}

// capCode cuts a text to at most n bytes on a rune boundary, its spaces kept.
func capCode(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return capText(s[:n+1], n)
}

// qualified is the name of a symbol with its container's.
func qualified(container, name string) string {
	if container == "" {
		return name
	}
	return container + "." + name
}

// symbolKeys gives each symbol of a file its key, <file key>#<kind>:<qualified name>, a repeated one numbered (~2, ~3)
// in the order of the file.
func symbolKeys(file string, syms []Symbol) []string {
	keys := make([]string, len(syms))
	seen := map[string]int{}
	for i, s := range syms {
		k := file + "#" + s.Kind + ":" + qualified(s.Container, s.Name)
		seen[k]++
		if n := seen[k]; n > 1 {
			k += "~" + strconv.Itoa(n)
		}
		keys[i] = k
	}
	return keys
}

// symbolTypes gives the node type of each symbol kind; a kind not named is a code@Symbol.
var symbolTypes = map[string]string{
	"function": TypeFunction, "prototype": TypeFunction, "method": TypeMethod,
	"class": TypeClass, "struct": TypeStruct, "union": TypeStruct, "interface": TypeInterface, "enum": TypeEnum,
	"type": TypeType, "variable": TypeVariable, "constant": TypeConstant, "field": TypeField,
}

func symbolType(kind string) string {
	if t, ok := symbolTypes[kind]; ok {
		return t
	}
	return TypeSymbol
}

// isSymbolType tells the node types of the declarations.
func isSymbolType(t string) bool {
	if t == TypeSymbol {
		return true
	}
	for _, s := range symbolTypes {
		if s == t {
			return true
		}
	}
	return false
}
