//go:build cgo

package gitimport

import (
	"fmt"
	"slices"
	"strings"
	"unsafe"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tsc "github.com/tree-sitter/tree-sitter-c/bindings/go"
	tscpp "github.com/tree-sitter/tree-sitter-cpp/bindings/go"
	tsgo "github.com/tree-sitter/tree-sitter-go/bindings/go"
	tsjava "github.com/tree-sitter/tree-sitter-java/bindings/go"
	tsjs "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	tspython "github.com/tree-sitter/tree-sitter-python/bindings/go"
	tsrust "github.com/tree-sitter/tree-sitter-rust/bindings/go"
	tsts "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
)

// TreeSitterAvailable tells whether this build reads the symbols of the sources (Tree-sitter needs cgo).
const TreeSitterAvailable = true

// The queries follow the tags convention of Tree-sitter: a pattern captures a declaration as @definition.<kind> and
// its name as @name. A kind of containers only (impl) gives its name to what it holds and is no symbol itself.
var queries = map[string]string{
	"c": `
(function_definition declarator: (function_declarator declarator: (identifier) @name)) @definition.function
(function_definition declarator: (pointer_declarator declarator: (function_declarator declarator: (identifier) @name))) @definition.function
(declaration declarator: (function_declarator declarator: (identifier) @name)) @definition.prototype
(declaration declarator: (pointer_declarator declarator: (function_declarator declarator: (identifier) @name))) @definition.prototype
(struct_specifier name: (type_identifier) @name body: (field_declaration_list)) @definition.struct
(union_specifier name: (type_identifier) @name body: (field_declaration_list)) @definition.union
(enum_specifier name: (type_identifier) @name body: (enumerator_list)) @definition.enum
(type_definition declarator: (type_identifier) @name) @definition.type
(preproc_include path: (_) @name) @definition.import
(preproc_def name: (identifier) @name) @definition.macro
(preproc_function_def name: (identifier) @name) @definition.macro
(translation_unit (declaration declarator: (identifier) @name) @definition.variable)
(translation_unit (declaration declarator: (init_declarator declarator: (identifier) @name)) @definition.variable)
(translation_unit (declaration declarator: (init_declarator declarator: (pointer_declarator declarator: (identifier) @name))) @definition.variable)
(translation_unit (declaration declarator: (init_declarator declarator: (array_declarator declarator: (identifier) @name))) @definition.variable)
(translation_unit (declaration declarator: (array_declarator declarator: (identifier) @name)) @definition.variable)
(translation_unit (declaration declarator: (pointer_declarator declarator: (identifier) @name)) @definition.variable)
(translation_unit (declaration declarator: (init_declarator declarator: (pointer_declarator declarator: (array_declarator declarator: (identifier) @name)))) @definition.variable)
(translation_unit (declaration declarator: (pointer_declarator declarator: (array_declarator declarator: (identifier) @name))) @definition.variable)
`,
	"cpp": `
(function_definition declarator: (function_declarator declarator: [(identifier) (field_identifier) (qualified_identifier) (destructor_name) (operator_name)] @name)) @definition.function
(function_definition declarator: (reference_declarator (function_declarator declarator: [(identifier) (field_identifier) (qualified_identifier)] @name))) @definition.function
(function_definition declarator: (pointer_declarator declarator: (function_declarator declarator: [(identifier) (field_identifier) (qualified_identifier)] @name))) @definition.function
(field_declaration declarator: (function_declarator declarator: (field_identifier) @name)) @definition.prototype
(declaration declarator: (function_declarator declarator: [(identifier) (qualified_identifier)] @name)) @definition.prototype
(class_specifier name: (type_identifier) @name body: (field_declaration_list)) @definition.class
(struct_specifier name: (type_identifier) @name body: (field_declaration_list)) @definition.struct
(union_specifier name: (type_identifier) @name body: (field_declaration_list)) @definition.union
(enum_specifier name: (type_identifier) @name body: (enumerator_list)) @definition.enum
(namespace_definition name: (_) @name) @definition.namespace
(type_definition declarator: (type_identifier) @name) @definition.type
(alias_declaration name: (type_identifier) @name) @definition.type
(preproc_include path: (_) @name) @definition.import
(preproc_def name: (identifier) @name) @definition.macro
(preproc_function_def name: (identifier) @name) @definition.macro
(translation_unit (declaration declarator: (init_declarator declarator: (identifier) @name)) @definition.variable)
(translation_unit (declaration declarator: (identifier) @name) @definition.variable)
`,
	"go": `
(function_declaration name: (identifier) @name) @definition.function
(method_declaration name: (field_identifier) @name) @definition.method
(type_spec name: (type_identifier) @name type: (struct_type)) @definition.struct
(type_spec name: (type_identifier) @name type: (interface_type)) @definition.interface
(type_spec name: (type_identifier) @name type: [(type_identifier) (qualified_type) (pointer_type) (slice_type) (map_type) (array_type) (function_type) (channel_type) (generic_type)]) @definition.type
(type_alias name: (type_identifier) @name) @definition.type
(import_spec path: (_) @name) @definition.import
(source_file (var_declaration (var_spec name: (identifier) @name) @definition.variable))
(source_file (var_declaration (var_spec_list (var_spec name: (identifier) @name) @definition.variable)))
(source_file (const_declaration (const_spec name: (identifier) @name) @definition.constant))
`,
	"python": `
(class_definition name: (identifier) @name) @definition.class
(function_definition name: (identifier) @name) @definition.function
(import_statement name: [(dotted_name) (aliased_import)] @name) @definition.import
(import_from_statement module_name: (_) @name) @definition.import
(module (expression_statement (assignment left: (identifier) @name) @definition.variable))
`,
	"java": `
(class_declaration name: (identifier) @name) @definition.class
(record_declaration name: (identifier) @name) @definition.class
(interface_declaration name: (identifier) @name) @definition.interface
(enum_declaration name: (identifier) @name) @definition.enum
(method_declaration name: (identifier) @name) @definition.method
(constructor_declaration name: (identifier) @name) @definition.method
(field_declaration declarator: (variable_declarator name: (identifier) @name)) @definition.field
(import_declaration [(scoped_identifier) (identifier)] @name) @definition.import
`,
	"javascript": `
(function_declaration name: (identifier) @name) @definition.function
(generator_function_declaration name: (identifier) @name) @definition.function
(class_declaration name: (identifier) @name) @definition.class
(method_definition name: [(property_identifier) (private_property_identifier)] @name) @definition.method
(import_statement source: (string) @name) @definition.import
(program (lexical_declaration (variable_declarator name: (identifier) @name) @definition.variable))
(program (variable_declaration (variable_declarator name: (identifier) @name) @definition.variable))
(program (export_statement declaration: (lexical_declaration (variable_declarator name: (identifier) @name) @definition.variable)))
`,
	"typescript": tsQuery,
	"tsx":        tsQuery,
	"rust": `
(function_item name: (identifier) @name) @definition.function
(function_signature_item name: (identifier) @name) @definition.prototype
(struct_item name: (type_identifier) @name) @definition.struct
(union_item name: (type_identifier) @name) @definition.union
(enum_item name: (type_identifier) @name) @definition.enum
(trait_item name: (type_identifier) @name) @definition.interface
(type_item name: (type_identifier) @name) @definition.type
(mod_item name: (identifier) @name) @definition.module
(macro_definition name: (identifier) @name) @definition.macro
(use_declaration argument: (_) @name) @definition.import
(const_item name: (identifier) @name) @definition.constant
(static_item name: (identifier) @name) @definition.variable
(impl_item type: (_) @name) @definition.impl
`,
}

const tsQuery = `
(function_declaration name: (identifier) @name) @definition.function
(generator_function_declaration name: (identifier) @name) @definition.function
(function_signature name: (identifier) @name) @definition.prototype
(class_declaration name: (type_identifier) @name) @definition.class
(abstract_class_declaration name: (type_identifier) @name) @definition.class
(interface_declaration name: (type_identifier) @name) @definition.interface
(type_alias_declaration name: (type_identifier) @name) @definition.type
(enum_declaration name: (identifier) @name) @definition.enum
(internal_module name: (_) @name) @definition.namespace
(method_definition name: [(property_identifier) (private_property_identifier)] @name) @definition.method
(method_signature name: (property_identifier) @name) @definition.method
(import_statement source: (string) @name) @definition.import
(program (lexical_declaration (variable_declarator name: (identifier) @name) @definition.variable))
(program (variable_declaration (variable_declarator name: (identifier) @name) @definition.variable))
(program (export_statement declaration: (lexical_declaration (variable_declarator name: (identifier) @name) @definition.variable)))
`

func grammar(lang string) unsafe.Pointer {
	switch lang {
	case "c":
		return tsc.Language()
	case "cpp":
		return tscpp.Language()
	case "go":
		return tsgo.Language()
	case "python":
		return tspython.Language()
	case "java":
		return tsjava.Language()
	case "javascript":
		return tsjs.Language()
	case "typescript":
		return tsts.LanguageTypescript()
	case "tsx":
		return tsts.LanguageTSX()
	case "rust":
		return tsrust.Language()
	}
	return nil
}

// TreeSitter extracts the symbols of the languages it has a grammar and a query for. It holds a parser and a compiled
// query per language, made on first use: one goroutine at a time.
type TreeSitter struct {
	parsers map[string]*parser
}

type parser struct {
	p    *sitter.Parser
	q    *sitter.Query
	name uint // index of the @name capture
}

// NewTreeSitter returns the extractor; Close frees its parsers.
func NewTreeSitter() (*TreeSitter, error) { return &TreeSitter{parsers: map[string]*parser{}}, nil }

// Close frees the parsers and the queries.
func (t *TreeSitter) Close() {
	for _, p := range t.parsers {
		p.q.Close()
		p.p.Close()
	}
	t.parsers = nil
}

// Language names the language of a file by its extension.
func (t *TreeSitter) Language(name string) string { return languageOf(name) }

func (t *TreeSitter) parser(lang string) (*parser, error) {
	if p, ok := t.parsers[lang]; ok {
		return p, nil
	}
	ptr := grammar(lang)
	if ptr == nil {
		return nil, fmt.Errorf("tree-sitter: no grammar for %q", lang)
	}
	l := sitter.NewLanguage(ptr)
	q, qerr := sitter.NewQuery(l, queries[lang])
	if qerr != nil {
		return nil, fmt.Errorf("tree-sitter: query of %s: %s", lang, qerr.Message)
	}
	p := sitter.NewParser()
	if err := p.SetLanguage(l); err != nil {
		q.Close()
		p.Close()
		return nil, fmt.Errorf("tree-sitter: %s: %w", lang, err)
	}
	pp := &parser{p: p, q: q}
	for i, n := range q.CaptureNames() {
		if n == "name" {
			pp.name = uint(i)
		}
	}
	t.parsers[lang] = pp
	return pp, nil
}

// definition is a declaration a pattern matched, before the containers are known.
type definition struct {
	kind, name string
	start, end uint
	text       []byte
}

// containers are the kinds that hold declarations: a function in one is a method; a declaration in a function or a
// method is local and left out.
var containers = map[string]bool{"class": true, "struct": true, "union": true, "interface": true, "enum": true, "namespace": true, "module": true, "impl": true}

func local(kind string) bool { return kind == "function" || kind == "method" }

// Symbols parses a source and returns its declarations in the order of the file.
func (t *TreeSitter) Symbols(lang string, src []byte) ([]Symbol, error) {
	p, err := t.parser(lang)
	if err != nil {
		return nil, err
	}
	tree := p.p.Parse(src, nil)
	if tree == nil {
		return nil, fmt.Errorf("tree-sitter: %s: no tree", lang)
	}
	defer tree.Close()
	qc := sitter.NewQueryCursor()
	defer qc.Close()
	names := p.q.CaptureNames()
	var defs []definition
	seen := map[[2]uint]bool{}
	matches := qc.Matches(p.q, tree.RootNode(), src)
	for m := matches.Next(); m != nil; m = matches.Next() {
		var d definition
		for _, c := range m.Captures {
			switch n := names[c.Index]; {
			case uint(c.Index) == p.name:
				d.name = cleanName(c.Node.Utf8Text(src))
			case strings.HasPrefix(n, "definition."):
				d.kind = strings.TrimPrefix(n, "definition.")
				d.start, d.end = c.Node.StartByte(), c.Node.EndByte()
				d.text = src[d.start:d.end]
			}
		}
		if d.kind == "" || d.name == "" || seen[[2]uint{d.start, d.end}] {
			continue // a declaration two patterns match (a C++ prototype and a field) counts once
		}
		seen[[2]uint{d.start, d.end}] = true
		defs = append(defs, d)
	}
	slices.SortStableFunc(defs, func(a, b definition) int {
		if a.start != b.start {
			return int(a.start) - int(b.start)
		}
		return int(b.end) - int(a.end) // the outer first
	})
	var out []Symbol
	var stack []definition // the declarations enclosing the current one
	for _, d := range defs {
		for len(stack) > 0 && stack[len(stack)-1].end <= d.start {
			stack = stack[:len(stack)-1]
		}
		var path []string
		isLocal, inContainer := false, false
		for _, s := range stack {
			if local(s.kind) {
				isLocal = true
			}
			if containers[s.kind] {
				path = append(path, s.name)
				inContainer = true
			}
		}
		stack = append(stack, d)
		if isLocal && d.kind != "import" || d.kind == "impl" {
			continue
		}
		kind := d.kind
		if kind == "function" && inContainer {
			kind = "method"
		}
		out = append(out, symbolOf(kind, d.name, strings.Join(path, "."), d.text))
	}
	return out, nil
}

// cleanName takes the quotes and brackets off an import path and keeps a name on one line.
func cleanName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"'`<>")
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return capText(strings.Join(strings.Fields(s), " "), 200)
}
