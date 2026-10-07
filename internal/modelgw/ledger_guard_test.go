package modelgw

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The gateway is the one ledger of LLM calls (ADR 0089): a call that reaches a provider without going through
// Service.Complete / Service.Embed (or the RPC client to them) is invisible. This guard reads the production code and
// fails when a new path to a model appears: a type implementing llm.Client / llm.Embedder that is neither the ledger-backed
// service, its client nor a wrapper of one, a llm.ClientFunc in a composition that is not the gateway's Complete, or a
// call of the router outside the service.
func TestEveryModelCallGoesThroughTheLedger(t *testing.T) {
	root := filepath.Join("..", "..")
	// the implementations of llm.Client / llm.Embedder: the ledger-backed service, the RPC client to it, the router
	// below the ledger (only the service calls it), and the instrumenting wrapper of the engine's client
	implementations := map[string]bool{
		"internal/modelgw.Service":     true,
		"internal/modelgw.Client":      true,
		"internal/modelgw.Router":      true,
		"internal/telemetry.LLMClient": true,
	}
	var problems []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch d.Name() {
			case ".claude", ".git", "gen", "web", "node_modules", "docs":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.HasPrefix(rel, "pkg/llm/") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return err
		}
		dir := filepath.ToSlash(filepath.Dir(rel))
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				// a method Complete(ctx, llm.Request) or Embed(ctx, llm.EmbedRequest) implements the contract
				if x.Recv == nil || x.Type.Params == nil || len(x.Type.Params.List) != 2 {
					return true
				}
				want := map[string]string{"Complete": "llm.Request", "Embed": "llm.EmbedRequest"}[x.Name.Name]
				if want != "" && typeString(x.Type.Params.List[1].Type) == want {
					if recv := dir + "." + typeString(x.Recv.List[0].Type); !implementations[recv] {
						problems = append(problems, rel+": "+recv+"."+x.Name.Name+" implements the model contract outside the ledger")
					}
				}
			case *ast.CallExpr:
				sel, ok := x.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				// llm.ClientFunc(...) must wrap the gateway's Complete
				if typeString(sel) == "llm.ClientFunc" {
					if len(x.Args) != 1 || !strings.HasSuffix(typeString(x.Args[0]), "gw.Complete") {
						problems = append(problems, rel+": llm.ClientFunc that is not the gateway's Complete")
					}
				}
				// the router is below the ledger: only the service calls it
				if inner, ok := sel.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "Router" && (sel.Sel.Name == "Complete" || sel.Sel.Name == "Embed") && rel != "internal/modelgw/service.go" {
					problems = append(problems, rel+": Router."+sel.Sel.Name+" called outside Service")
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}

// typeString spells a type or a selector expression: pkg.Name, *Name.
func typeString(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return typeString(x.X) + "." + x.Sel.Name
	case *ast.StarExpr:
		return typeString(x.X)
	}
	return ""
}
