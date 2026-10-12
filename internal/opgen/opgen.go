// Package opgen generates the catalogue of operations (pkg/operations, ADR 0103) from the `//goap:operation <code>
// <kind>` annotations of the methods of pkg/graph. It refuses a malformed annotation, an unknown kind, a code declared
// twice (or two codes giving the same constant), an annotation on anything but an exported method of *Graph, an
// annotated method that does not ask for its operation (`g.allow(ctx, id, operations.<Const>)`) and an `allow` naming
// an operation no annotation declares.
package opgen

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Directive is the comment that declares an operation on the method performing it.
const Directive = "//goap:operation"

// Receiver is the type whose methods carry the annotations.
const Receiver = "Graph"

var (
	codeRE = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z][a-z0-9-]*)+$`)
	kinds  = map[string]bool{"doer": true, "reviewer": true}
)

// Operation is an annotation found in the sources.
type Operation struct {
	Code   string
	Kind   string
	Method string // Graph.<name>
	Doc    string // the first sentence of the method's documentation
	Pos    string // file:line of the annotation
}

// Const is the name of the Go constant of a code: "impact.link-create" → "ImpactLinkCreate".
func Const(code string) string {
	var b strings.Builder
	for _, part := range strings.FieldsFunc(code, func(r rune) bool { return r == '.' || r == '-' }) {
		b.WriteString(strings.ToUpper(part[:1]) + part[1:])
	}
	return b.String()
}

// Scan reads the non-test Go files of dir and returns the operations they declare, sorted by code, with every problem
// found (joined).
func Scan(dir string) ([]Operation, error) {
	fset := token.NewFileSet()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var errs []error
	fail := func(pos token.Pos, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: %s", position(fset, pos), fmt.Sprintf(format, args...)))
	}
	var ops []Operation
	allows := map[string][]token.Pos{} // constant → the calls of g.allow naming it
	bodies := map[string]*ast.BlockStmt{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		claimed := map[*ast.Comment]bool{}
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if fn.Body != nil {
				collectAllows(fn.Body, allows)
			}
			if fn.Doc == nil {
				continue
			}
			for _, c := range fn.Doc.List {
				if !strings.HasPrefix(c.Text, Directive) {
					continue
				}
				claimed[c] = true
				fields := strings.Fields(strings.TrimPrefix(c.Text, Directive))
				switch {
				case len(fields) != 2:
					fail(c.Pos(), "want %s <code> <kind>, got %q", Directive, c.Text)
					continue
				case !codeRE.MatchString(fields[0]):
					fail(c.Pos(), "operation code %q: lowercase dotted words, at least two (impact.review)", fields[0])
					continue
				case !kinds[fields[1]]:
					fail(c.Pos(), "operation %s: unknown kind %q (doer or reviewer)", fields[0], fields[1])
					continue
				case !isGraphMethod(fn):
					fail(c.Pos(), "operation %s: only an exported method of *%s performs an operation", fields[0], Receiver)
					continue
				}
				method := Receiver + "." + fn.Name.Name
				ops = append(ops, Operation{Code: fields[0], Kind: fields[1], Method: method, Doc: firstSentence(fn.Doc.Text()), Pos: position(fset, c.Pos())})
				bodies[fields[0]] = fn.Body
			}
		}
		// a directive that documents no function is a mistake, not a comment
		for _, g := range file.Comments {
			for _, c := range g.List {
				if strings.HasPrefix(c.Text, Directive) && !claimed[c] {
					fail(c.Pos(), "%s documents no method", Directive)
				}
			}
		}
	}
	sort.SliceStable(ops, func(i, j int) bool { return ops[i].Code < ops[j].Code })
	codes, consts := map[string]Operation{}, map[string]Operation{}
	for _, o := range ops {
		if prev, dup := codes[o.Code]; dup {
			errs = append(errs, fmt.Errorf("%s: operation %s declared twice (also on %s, %s)", o.Pos, o.Code, prev.Method, prev.Pos))
			continue
		}
		codes[o.Code] = o
		if prev, dup := consts[Const(o.Code)]; dup {
			errs = append(errs, fmt.Errorf("%s: operations %s and %s give the same constant %s", o.Pos, prev.Code, o.Code, Const(o.Code)))
			continue
		}
		consts[Const(o.Code)] = o
		if !callsAllow(bodies[o.Code], Const(o.Code)) {
			errs = append(errs, fmt.Errorf("%s: %s performs %s but never asks for it: call g.allow(ctx, id, operations.%s) first", o.Pos, o.Method, o.Code, Const(o.Code)))
		}
	}
	names := make([]string, 0, len(allows))
	for name := range allows {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, ok := consts[name]; !ok {
			for _, pos := range allows[name] {
				fail(pos, "g.allow names operations.%s, which no %s annotation declares", name, Directive)
			}
		}
	}
	return ops, errors.Join(errs...)
}

// Render returns the Go source of the catalogue of package pkg, formatted.
func Render(pkg string, ops []Operation) ([]byte, error) {
	var b bytes.Buffer
	fmt.Fprintf(&b, "// Code generated by goap-opgen from the %s annotations of pkg/graph. DO NOT EDIT.\n\n", Directive)
	fmt.Fprintf(&b, "package %s\n\n", pkg)
	b.WriteString("// The operations of the catalogue.\nconst (\n")
	for _, o := range ops {
		fmt.Fprintf(&b, "\t%s = %q // %s, %s\n", Const(o.Code), o.Code, o.Method, o.Kind)
	}
	b.WriteString(")\n\nvar catalog = []Operation{\n")
	for _, o := range ops {
		k := "Doer"
		if o.Kind == "reviewer" {
			k = "Reviewer"
		}
		fmt.Fprintf(&b, "\t{Code: %s, Kind: %s, Method: %q, Doc: %q},\n", Const(o.Code), k, o.Method, o.Doc)
	}
	b.WriteString("}\n")
	return format.Source(b.Bytes())
}

// Generate scans src and writes the catalogue to out (package of out's directory name), unless it is already current.
func Generate(src, out string) error {
	ops, err := Scan(src)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(out)
	if err != nil {
		return err
	}
	code, err := Render(filepath.Base(filepath.Dir(abs)), ops)
	if err != nil {
		return err
	}
	if cur, err := os.ReadFile(out); err == nil && bytes.Equal(cur, code) {
		return nil
	}
	return os.WriteFile(out, code, 0o644)
}

func isGraphMethod(fn *ast.FuncDecl) bool {
	if fn.Recv == nil || len(fn.Recv.List) != 1 || !fn.Name.IsExported() {
		return false
	}
	star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	id, ok := star.X.(*ast.Ident)
	return ok && id.Name == Receiver
}

// allowConst returns the constant a call `<x>.allow(_, _, operations.<Const>)` names.
func allowConst(call *ast.CallExpr) (string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "allow" || len(call.Args) != 3 {
		return "", false
	}
	arg, ok := call.Args[2].(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	pkg, ok := arg.X.(*ast.Ident)
	if !ok || pkg.Name != "operations" {
		return "", false
	}
	return arg.Sel.Name, true
}

func collectAllows(body ast.Node, into map[string][]token.Pos) {
	ast.Inspect(body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if name, ok := allowConst(call); ok {
				into[name] = append(into[name], call.Pos())
			}
		}
		return true
	})
}

func callsAllow(body *ast.BlockStmt, name string) bool {
	found := false
	if body == nil {
		return false
	}
	ast.Inspect(body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if c, ok := allowConst(call); ok && c == name {
				found = true
			}
		}
		return !found
	})
	return found
}

// firstSentence is the first sentence of a doc comment (go/ast leaves the directives out of its text).
func firstSentence(doc string) string {
	text := strings.Join(strings.Fields(doc), " ")
	if i := strings.Index(text, ". "); i >= 0 {
		text = text[:i+1]
	}
	return text
}

func position(fset *token.FileSet, pos token.Pos) string {
	p := fset.Position(pos)
	return fmt.Sprintf("%s:%d", filepath.Base(p.Filename), p.Line)
}
