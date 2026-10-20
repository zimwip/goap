package gitimport

import (
	"crypto/sha1"
	"encoding/hex"
	"path"
	"strconv"
	"strings"
)

// Symbol is a declaration a source file holds (a function, a type, an import...), as the symbol extractor finds it.
type Symbol struct {
	// Kind is one of the symbol kinds of the code domain (enum symbol-kind): function, method, prototype, struct,
	// union, class, interface, enum, type, namespace, module, import, variable, constant, field, macro.
	Kind string
	Name string
	// Container is the qualified name of the declaration it is declared in (a class for a method), empty at the top
	// level.
	Container string
	// Signature is the first line of its text, Digest a digest of its whole text: a symbol gets a new version when its
	// own text changes, not when lines move around it.
	Signature, Digest string
}

// Extractor finds the symbols of a source file.
type Extractor interface {
	// Language names the language of a file by its name, "" when the extractor reads none.
	Language(name string) string
	Symbols(language string, src []byte) ([]Symbol, error)
}

// languages maps the extensions to the languages the Tree-sitter extractor reads.
var languages = map[string]string{
	"c": "c", "h": "c",
	"cc": "cpp", "cpp": "cpp", "cxx": "cpp", "hh": "cpp", "hpp": "cpp", "hxx": "cpp",
	"go":   "go",
	"py":   "python",
	"java": "java",
	"js":   "javascript", "mjs": "javascript", "cjs": "javascript", "jsx": "javascript",
	"ts": "typescript", "tsx": "tsx",
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
	return Symbol{Kind: kind, Name: name, Container: container, Signature: capText(sig, 200), Digest: hex.EncodeToString(sum[:10])}
}

// symbolKeys gives each symbol of a file its key, <file key>#<kind>:<qualified name>, a repeated one numbered (~2, ~3)
// in the order of the file.
func symbolKeys(file string, syms []Symbol) []string {
	keys := make([]string, len(syms))
	seen := map[string]int{}
	for i, s := range syms {
		q := s.Name
		if s.Container != "" {
			q = s.Container + "." + s.Name
		}
		k := file + "#" + s.Kind + ":" + q
		seen[k]++
		if n := seen[k]; n > 1 {
			k += "~" + strconv.Itoa(n)
		}
		keys[i] = k
	}
	return keys
}
