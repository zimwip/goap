//go:build cgo

package gitimport

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
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
// its name as @name (a Go method its receiver type as @container, the receiver as @self). A kind of containers only
// (impl) gives its name to what it holds and is no symbol itself. @import captures an import statement, @package the
// package a file declares, and @ref.call, @ref.type, @ref.name a name called or used, with what it is selected from
// as @qual.
var queries = map[string]string{
	"c": `
(function_definition declarator: (function_declarator declarator: (identifier) @name)) @definition.function
(function_definition declarator: (pointer_declarator declarator: (function_declarator declarator: (identifier) @name))) @definition.function
(declaration declarator: (function_declarator declarator: (identifier) @name)) @definition.prototype
(declaration declarator: (pointer_declarator declarator: (function_declarator declarator: (identifier) @name))) @definition.prototype
(struct_specifier name: (type_identifier) @name body: (field_declaration_list)) @definition.struct
(union_specifier name: (type_identifier) @name body: (field_declaration_list)) @definition.union
(enum_specifier name: (type_identifier) @name body: (enumerator_list)) @definition.enum
(field_declaration_list (field_declaration declarator: (field_identifier) @name) @definition.field)
(field_declaration_list (field_declaration declarator: (pointer_declarator declarator: (field_identifier) @name)) @definition.field)
(field_declaration_list (field_declaration declarator: (array_declarator declarator: (field_identifier) @name)) @definition.field)
(enumerator_list (enumerator name: (identifier) @name) @definition.constant)
(type_definition declarator: (type_identifier) @name) @definition.type
(preproc_include) @import
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
(call_expression function: (identifier) @ref.call)
(type_identifier) @ref.type
(identifier) @ref.name
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
(field_declaration_list (field_declaration declarator: (field_identifier) @name) @definition.field)
(field_declaration_list (field_declaration declarator: (pointer_declarator declarator: (field_identifier) @name)) @definition.field)
(field_declaration_list (field_declaration declarator: (array_declarator declarator: (field_identifier) @name)) @definition.field)
(enumerator_list (enumerator name: (identifier) @name) @definition.constant)
(namespace_definition name: (_) @name) @definition.namespace
(type_definition declarator: (type_identifier) @name) @definition.type
(alias_declaration name: (type_identifier) @name) @definition.type
(preproc_include) @import
(preproc_def name: (identifier) @name) @definition.macro
(preproc_function_def name: (identifier) @name) @definition.macro
(translation_unit (declaration declarator: (init_declarator declarator: (identifier) @name)) @definition.variable)
(translation_unit (declaration declarator: (identifier) @name) @definition.variable)
(namespace_definition body: (declaration_list (declaration declarator: (init_declarator declarator: (identifier) @name)) @definition.variable))
(namespace_definition body: (declaration_list (declaration declarator: (identifier) @name) @definition.variable))
(call_expression function: (identifier) @ref.call)
(call_expression function: (qualified_identifier scope: (_) @qual name: (identifier) @ref.call))
(call_expression function: (field_expression argument: (_) @qual field: (field_identifier) @ref.call))
(type_identifier) @ref.type
(identifier) @ref.name
`,
	"go": `
(package_clause (package_identifier) @package)
(function_declaration name: (identifier) @name) @definition.function
(method_declaration receiver: (parameter_list (parameter_declaration name: (identifier)? @self type: [(type_identifier) @container (pointer_type (type_identifier) @container) (generic_type type: (type_identifier) @container) (pointer_type (generic_type type: (type_identifier) @container))])) name: (field_identifier) @name) @definition.method
(method_declaration name: (field_identifier) @name) @definition.method
(method_elem name: (field_identifier) @name) @definition.method
(type_spec name: (type_identifier) @name type: (struct_type)) @definition.struct
(type_spec name: (type_identifier) @name type: (interface_type)) @definition.interface
(type_spec name: (type_identifier) @name type: [(type_identifier) (qualified_type) (pointer_type) (slice_type) (map_type) (array_type) (function_type) (channel_type) (generic_type)]) @definition.type
(type_alias name: (type_identifier) @name) @definition.type
(field_declaration_list (field_declaration name: (field_identifier) @name) @definition.field)
(field_declaration_list (field_declaration !name type: [(type_identifier) @name (qualified_type name: (type_identifier) @name) (pointer_type (type_identifier) @name) (pointer_type (qualified_type name: (type_identifier) @name)) (generic_type type: (type_identifier) @name)]) @definition.field)
(import_spec) @import
(source_file (var_declaration (var_spec name: (identifier) @name) @definition.variable))
(source_file (var_declaration (var_spec_list (var_spec name: (identifier) @name) @definition.variable)))
(source_file (const_declaration (const_spec name: (identifier) @name) @definition.constant))
(call_expression function: (identifier) @ref.call)
(call_expression function: (selector_expression operand: (identifier) @qual field: (field_identifier) @ref.call))
(composite_literal type: (type_identifier) @ref.type)
(type_identifier) @ref.type
(qualified_type package: (package_identifier) @qual name: (type_identifier) @ref.type)
(identifier) @ref.name
(selector_expression operand: (identifier) @qual field: (field_identifier) @ref.name)
`,
	"python": `
(class_definition name: (identifier) @name) @definition.class
(function_definition name: (identifier) @name) @definition.function
(import_statement) @import
(import_from_statement) @import
(module (expression_statement (assignment left: (identifier) @name) @definition.variable))
(class_definition body: (block (expression_statement (assignment left: (identifier) @name) @definition.field)))
(call function: (identifier) @ref.call)
(call function: (attribute object: (_) @qual attribute: (identifier) @ref.call))
(class_definition superclasses: (argument_list (identifier) @ref.type))
(class_definition superclasses: (argument_list (attribute object: (_) @qual attribute: (identifier) @ref.type)))
(type (identifier) @ref.type)
(identifier) @ref.name
(attribute object: [(identifier) (attribute)] @qual attribute: (identifier) @ref.name)
`,
	"java": `
(package_declaration [(scoped_identifier) (identifier)] @package)
(class_declaration name: (identifier) @name) @definition.class
(record_declaration name: (identifier) @name) @definition.class
(interface_declaration name: (identifier) @name) @definition.interface
(enum_declaration name: (identifier) @name) @definition.enum
(method_declaration name: (identifier) @name) @definition.method
(constructor_declaration name: (identifier) @name) @definition.method
(field_declaration declarator: (variable_declarator name: (identifier) @name)) @definition.field
(constant_declaration declarator: (variable_declarator name: (identifier) @name)) @definition.constant
(enum_constant name: (identifier) @name) @definition.constant
(import_declaration) @import
(method_invocation name: (identifier) @ref.call)
(method_invocation object: (_) @qual name: (identifier) @ref.call)
(object_creation_expression type: (type_identifier) @ref.call)
(type_identifier) @ref.type
(identifier) @ref.name
(field_access object: (_) @qual field: (identifier) @ref.name)
`,
	"javascript": `
(function_declaration name: (identifier) @name) @definition.function
(generator_function_declaration name: (identifier) @name) @definition.function
(class_declaration name: (identifier) @name) @definition.class
(method_definition name: [(property_identifier) (private_property_identifier)] @name) @definition.method
(import_statement) @import
(program (lexical_declaration (variable_declarator name: (identifier) @name) @definition.variable))
(program (variable_declaration (variable_declarator name: (identifier) @name) @definition.variable))
(program (export_statement declaration: (lexical_declaration (variable_declarator name: (identifier) @name) @definition.variable)))
(program (export_statement declaration: (variable_declaration (variable_declarator name: (identifier) @name) @definition.variable)))
(class_body (field_definition property: [(property_identifier) (private_property_identifier)] @name) @definition.field)
(call_expression function: (identifier) @ref.call)
(call_expression function: (member_expression object: (_) @qual property: (property_identifier) @ref.call))
(new_expression constructor: (identifier) @ref.call)
(class_heritage (identifier) @ref.type)
(identifier) @ref.name
(member_expression object: (identifier) @qual property: (property_identifier) @ref.name)
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
(use_declaration) @import
(const_item name: (identifier) @name) @definition.constant
(static_item name: (identifier) @name) @definition.variable
(field_declaration_list (field_declaration name: (field_identifier) @name) @definition.field)
(impl_item type: (_) @name) @definition.impl
(call_expression function: (identifier) @ref.call)
(call_expression function: (scoped_identifier path: (_) @qual name: (identifier) @ref.call))
(call_expression function: (field_expression value: (_) @qual field: (field_identifier) @ref.call))
(macro_invocation macro: (identifier) @ref.call)
(type_identifier) @ref.type
(scoped_type_identifier path: (_) @qual name: (type_identifier) @ref.type)
(identifier) @ref.name
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
(import_statement) @import
(program (lexical_declaration (variable_declarator name: (identifier) @name) @definition.variable))
(program (variable_declaration (variable_declarator name: (identifier) @name) @definition.variable))
(program (export_statement declaration: (lexical_declaration (variable_declarator name: (identifier) @name) @definition.variable)))
(program (export_statement declaration: (variable_declaration (variable_declarator name: (identifier) @name) @definition.variable)))
(public_field_definition name: [(property_identifier) (private_property_identifier)] @name) @definition.field
(property_signature name: (property_identifier) @name) @definition.field
(call_expression function: (identifier) @ref.call)
(call_expression function: (member_expression object: (_) @qual property: (property_identifier) @ref.call))
(new_expression constructor: (identifier) @ref.call)
(extends_clause value: (identifier) @ref.type)
(type_identifier) @ref.type
(nested_type_identifier module: (identifier) @qual name: (type_identifier) @ref.type)
(identifier) @ref.name
(member_expression object: (identifier) @qual property: (property_identifier) @ref.name)
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

// TreeSitter extracts the symbols of the languages it has a grammar and a query for. Parse may be called from several
// goroutines: each takes a set of parsers (a parser and a compiled query per language, made on first use) from a free
// list and gives it back.
type TreeSitter struct {
	mu   sync.Mutex
	free []parsers
	all  []parsers
}

type parsers map[string]*parser

type parser struct {
	p        *sitter.Parser
	q        *sitter.Query
	captures []string
}

// NewTreeSitter returns the extractor; Close frees its parsers.
func NewTreeSitter() (*TreeSitter, error) { return &TreeSitter{}, nil }

// Close frees the parsers and the queries (none may be in use).
func (t *TreeSitter) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, ps := range t.all {
		for _, p := range ps {
			p.q.Close()
			p.p.Close()
		}
	}
	t.all, t.free = nil, nil
}

// Language names the language of a file by its extension.
func (t *TreeSitter) Language(name string) string { return languageOf(name) }

func (t *TreeSitter) take() parsers {
	t.mu.Lock()
	defer t.mu.Unlock()
	if n := len(t.free); n > 0 {
		ps := t.free[n-1]
		t.free = t.free[:n-1]
		return ps
	}
	ps := parsers{}
	t.all = append(t.all, ps)
	return ps
}

func (t *TreeSitter) give(ps parsers) {
	t.mu.Lock()
	t.free = append(t.free, ps)
	t.mu.Unlock()
}

func (ps parsers) parser(lang string) (*parser, error) {
	if p, ok := ps[lang]; ok {
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
	pp := &parser{p: p, q: q, captures: q.CaptureNames()}
	ps[lang] = pp
	return pp, nil
}

// definition is a declaration a pattern matched, before the containers are known.
type definition struct {
	kind, name      string
	container, self string // a Go method's receiver
	start, end      uint
	text            []byte
}

// ref is a name a pattern matched, before the declaration holding it is known.
type ref struct {
	Ref
	at uint
}

// containers are the kinds that hold declarations: a function in one is a method; a declaration in a function or a
// method is local and left out.
var containers = map[string]bool{"class": true, "struct": true, "union": true, "interface": true, "enum": true, "namespace": true, "module": true, "impl": true}

func local(kind string) bool { return kind == "function" || kind == "method" }

// maxRefs bounds the names kept for one declaration.
const maxRefs = 500

// Parse parses a source and returns its package clause, its imports and its declarations in the order of the file.
func (t *TreeSitter) Parse(lang string, src []byte) (Source, error) {
	ps := t.take()
	defer t.give(ps)
	p, err := ps.parser(lang)
	if err != nil {
		return Source{}, err
	}
	tree := p.p.Parse(src, nil)
	if tree == nil {
		return Source{}, fmt.Errorf("tree-sitter: %s: no tree", lang)
	}
	defer tree.Close()
	qc := sitter.NewQueryCursor()
	defer qc.Close()
	var (
		out  Source
		defs []definition
		refs = map[[2]any]ref{} // by position and kind: a qualified one wins
	)
	seen := map[[3]any]int{} // a declaration two patterns match (a C++ prototype and a field) counts once
	matches := qc.Matches(p.q, tree.RootNode(), src)
	for m := matches.Next(); m != nil; m = matches.Next() {
		var (
			d       definition
			r       ref
			qual    string
			isRef   bool
			imports []sitter.Node
		)
		for _, c := range m.Captures {
			n := p.captures[c.Index]
			switch {
			case n == "name":
				d.name = cleanName(c.Node.Utf8Text(src))
			case n == "container":
				d.container = c.Node.Utf8Text(src)
			case n == "self":
				d.self = c.Node.Utf8Text(src)
			case n == "package":
				out.Package = cleanName(c.Node.Utf8Text(src))
			case n == "import":
				imports = append(imports, c.Node)
			case n == "qual":
				qual = strings.Join(strings.Fields(c.Node.Utf8Text(src)), "")
			case strings.HasPrefix(n, "definition."):
				d.kind = strings.TrimPrefix(n, "definition.")
				d.start, d.end = c.Node.StartByte(), c.Node.EndByte()
				d.text = src[d.start:d.end]
			case strings.HasPrefix(n, "ref."):
				isRef = true
				r.Kind, r.Name, r.at = strings.TrimPrefix(n, "ref."), c.Node.Utf8Text(src), c.Node.StartByte()
			}
		}
		for i := range imports {
			out.Imports = append(out.Imports, importsOf(lang, &imports[i], src)...)
		}
		if isRef {
			r.Qualifier = qual
			k := [2]any{r.at, r.Kind}
			if old, ok := refs[k]; !ok || old.Qualifier == "" {
				refs[k] = r
			}
			continue
		}
		if d.kind == "" || d.name == "" {
			continue
		}
		k := [3]any{d.start, d.end, d.name}
		if i, ok := seen[k]; ok {
			if defs[i].container == "" {
				defs[i].container, defs[i].self = d.container, d.self
			}
			continue
		}
		seen[k] = len(defs)
		defs = append(defs, d)
	}
	slices.SortStableFunc(defs, func(a, b definition) int {
		if a.start != b.start {
			return int(a.start) - int(b.start)
		}
		return int(b.end) - int(a.end) // the outer first
	})
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
		if isLocal || d.kind == "impl" {
			continue
		}
		kind, name := d.kind, d.name
		if d.container != "" {
			path = append(path, d.container)
		}
		// a C++ member defined out of its class (A::f) is a method of A
		if i := strings.LastIndex(name, "::"); i > 0 && (lang == "cpp" || lang == "c") {
			path = append(path, strings.Split(name[:i], "::")...)
			name, inContainer = name[i+2:], true
		}
		if kind == "function" && inContainer {
			kind = "method"
		}
		s := symbolOf(kind, name, strings.Join(path, "."), d.text)
		s.Self, s.Start, s.End = d.self, d.start, d.end
		out.Symbols = append(out.Symbols, s)
	}
	ownTexts(out.Symbols, src)
	for i := range out.Symbols {
		out.Symbols[i].Code = dedent(leadOf(src, out.Symbols[i].Start) + out.Symbols[i].Code)
	}
	assignRefs(out.Symbols, refs)
	return out, nil
}

// ownTexts gives the symbols holding others (in order, the outer first) their own text (ownText): their signature,
// digest and code leave their members out.
func ownTexts(syms []Symbol, src []byte) {
	held := map[int][][2]uint{}
	var stack []int // the symbols enclosing the current one
	for i, s := range syms {
		for len(stack) > 0 && syms[stack[len(stack)-1]].End <= s.Start {
			stack = stack[:len(stack)-1]
		}
		if len(stack) > 0 {
			if p := stack[len(stack)-1]; syms[p].Start != s.Start || syms[p].End != s.End {
				held[p] = append(held[p], [2]uint{s.Start, s.End})
			}
		}
		stack = append(stack, i)
	}
	for i, h := range held {
		s := &syms[i]
		o := symbolOf(s.Kind, s.Name, s.Container, ownText(src, s.Start, s.End, h))
		s.Signature, s.Digest, s.Code = o.Signature, o.Digest, o.Code
	}
}

// assignRefs gives each name to the innermost declaration holding it, once per declaration.
func assignRefs(syms []Symbol, refs map[[2]any]ref) {
	if len(syms) == 0 {
		return
	}
	order := make([]int, len(syms)) // by start, the outer first
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		if syms[a].Start != syms[b].Start {
			return int(syms[a].Start) - int(syms[b].Start)
		}
		return int(syms[b].End) - int(syms[a].End)
	})
	all := slices.Collect(maps.Values(refs))
	slices.SortFunc(all, func(a, b ref) int {
		if a.at != b.at {
			return int(a.at) - int(b.at)
		}
		return strings.Compare(a.Kind, b.Kind)
	})
	seen := make([]map[Ref]bool, len(syms))
	for _, r := range all {
		j, _ := slices.BinarySearchFunc(order, r.at, func(i int, at uint) int {
			if syms[i].Start <= at {
				return -1
			}
			return 1
		})
		for j--; j >= 0 && syms[order[j]].End <= r.at; j-- {
		}
		if j < 0 {
			continue // at the top level: in an import, a statement
		}
		i := order[j]
		if seen[i] == nil {
			seen[i] = map[Ref]bool{}
		}
		if seen[i][r.Ref] || len(seen[i]) >= maxRefs {
			continue
		}
		seen[i][r.Ref] = true
		syms[i].Refs = append(syms[i].Refs, r.Ref)
	}
}

// importsOf reads an import statement.
func importsOf(lang string, n *sitter.Node, src []byte) []Import {
	text := func(n *sitter.Node) string {
		if n == nil {
			return ""
		}
		return strings.Join(strings.Fields(n.Utf8Text(src)), "")
	}
	named := func(n *sitter.Node) []*sitter.Node {
		out := make([]*sitter.Node, 0, n.NamedChildCount())
		for i := uint(0); i < n.NamedChildCount(); i++ {
			out = append(out, n.NamedChild(i))
		}
		return out
	}
	switch lang {
	case "go":
		p := cleanName(text(n.ChildByFieldName("path")))
		if p == "" {
			return nil
		}
		return []Import{{Path: p, Alias: text(n.ChildByFieldName("name"))}}
	case "c", "cpp":
		pn := n.ChildByFieldName("path")
		if pn == nil {
			return nil
		}
		return []Import{{Path: cleanName(text(pn)), System: pn.Kind() == "system_lib_string"}}
	case "python":
		var out []Import
		if n.Kind() == "import_statement" {
			for i := uint(0); i < n.NamedChildCount(); i++ {
				if n.FieldNameForNamedChild(uint32(i)) != "name" {
					continue
				}
				c := n.NamedChild(i)
				if c.Kind() == "aliased_import" {
					out = append(out, Import{Path: text(c.ChildByFieldName("name")), Alias: text(c.ChildByFieldName("alias"))})
				} else {
					out = append(out, Import{Path: text(c), Alias: text(c)})
				}
			}
			return out
		}
		im := Import{Path: text(n.ChildByFieldName("module_name"))}
		for i := uint(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			switch {
			case c.Kind() == "wildcard_import":
				im.Alias = "*"
			case n.FieldNameForNamedChild(uint32(i)) != "name":
			case c.Kind() == "aliased_import":
				addName(&im, text(c.ChildByFieldName("alias")), text(c.ChildByFieldName("name")))
			default:
				addName(&im, text(c), text(c))
			}
		}
		return []Import{im}
	case "java":
		var parts []string
		for _, c := range named(n) {
			switch c.Kind() {
			case "scoped_identifier", "identifier":
				parts = append(parts, text(c))
			case "asterisk":
				parts = append(parts, "*")
			}
		}
		if len(parts) == 0 {
			return nil
		}
		return []Import{{Path: strings.Join(parts, ".")}}
	case "javascript", "typescript", "tsx":
		im := Import{Path: cleanName(text(n.ChildByFieldName("source")))}
		if im.Path == "" {
			return nil
		}
		for _, c := range named(n) {
			if c.Kind() != "import_clause" {
				continue
			}
			for _, cc := range named(c) {
				switch cc.Kind() {
				case "identifier":
					addName(&im, text(cc), "default")
				case "namespace_import":
					for _, id := range named(cc) {
						im.Alias = text(id)
					}
				case "named_imports":
					for _, s := range named(cc) {
						if s.Kind() != "import_specifier" {
							continue
						}
						name := text(s.ChildByFieldName("name"))
						local := text(s.ChildByFieldName("alias"))
						if local == "" {
							local = name
						}
						addName(&im, local, name)
					}
				}
			}
		}
		return []Import{im}
	case "rust":
		var out []Import
		var walk func(prefix string, n *sitter.Node)
		join := func(a, b string) string {
			if a == "" {
				return b
			}
			return a + "::" + b
		}
		walk = func(prefix string, n *sitter.Node) {
			if n == nil {
				return
			}
			switch n.Kind() {
			case "use_as_clause":
				p := join(prefix, text(n.ChildByFieldName("path")))
				out = append(out, Import{Path: p, Names: map[string]string{text(n.ChildByFieldName("alias")): lastSeg(p)}})
			case "scoped_use_list":
				p := join(prefix, text(n.ChildByFieldName("path")))
				walk(p, n.ChildByFieldName("list"))
			case "use_list":
				for _, c := range named(n) {
					walk(prefix, c)
				}
			case "use_wildcard":
				p := strings.TrimSuffix(strings.TrimSuffix(text(n), "*"), "::")
				out = append(out, Import{Path: join(prefix, p), Alias: "*"})
			default:
				p := join(prefix, text(n))
				if last := lastSeg(p); last == "self" {
					p = strings.TrimSuffix(strings.TrimSuffix(p, "self"), "::")
					out = append(out, Import{Path: p, Names: map[string]string{lastSeg(p): lastSeg(p)}})
				} else {
					out = append(out, Import{Path: p, Names: map[string]string{last: last}})
				}
			}
		}
		walk("", n.ChildByFieldName("argument"))
		return out
	}
	return nil
}

func addName(im *Import, local, name string) {
	if local == "" || name == "" {
		return
	}
	if im.Names == nil {
		im.Names = map[string]string{}
	}
	im.Names[local] = name
}

func lastSeg(p string) string {
	if i := strings.LastIndex(p, "::"); i >= 0 {
		return p[i+2:]
	}
	return p
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
