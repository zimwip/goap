package devseed

import (
	"context"
	"errors"

	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/pkg/adapter"
	"github.com/zimwip/goap/pkg/algo"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/mcp"
)

// documentRepository is the generic MCP to manipulate documents.
func documentRepository() mcp.Def {
	obj := func(props map[string]any, required ...string) map[string]any {
		s := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			r := make([]any, len(required))
			for i, n := range required {
				r[i] = n
			}
			s["required"] = r
		}
		return s
	}
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	return mcp.Def{Name: "document-repository", Description: "Documents organised in folders.", Tools: []mcp.Tool{
		{Name: "list", Description: "List the documents and folders under a path.", InputSchema: obj(map[string]any{"path": str("folder path, empty for the root")})},
		{Name: "read", Description: "Read the text of a document.", InputSchema: obj(map[string]any{"path": str("document path")}, "path")},
		{Name: "write", Description: "Create or replace a document.", InputSchema: obj(map[string]any{"path": str("document path"), "content": str("text of the document")}, "path", "content")},
	}}
}

// LocalFSAdapterName is the adapter definition of the document-repository MCP on the localfs connector.
const LocalFSAdapterName = "localfs-document-repository"

// localFSAdapterDef is the reference adapter: the document-repository MCP on the local file system connector.
func localFSAdapterDef() adapter.Def {
	return adapter.Def{
		Name:        LocalFSAdapterName,
		Description: "The document-repository MCP on the local file system connector (localfs)",
		MCP:         "document-repository",
		Connector:   "localfs",
		Language:    algo.JavaScript,
		Params:      []algo.Param{{Name: "root", Type: algo.ParamString, Required: true, Description: "Directory the unit exposes as its document repository"}},
		Code: `switch (ctx.tool()) {
  case "list":
    return ctx.call("list_dir", { path: ctx.args().path || "" });
  case "read":
    return ctx.call("read_file", { path: ctx.args().path });
  case "write":
    return ctx.call("write_file", { path: ctx.args().path, content: ctx.args().content });
}
ctx.fail("unknown tool " + ctx.tool());
`,
	}
}

// LocalFSAdapter is the instance of the localfs adapter of the platform library for a unit, exposing a
// directory as its document repository (demos and tests).
func LocalFSAdapter(unit, root string) adapter.Instance {
	return adapter.Instance{Unit: unit, MCP: "document-repository", Adapter: LocalFSAdapterName, Params: map[string]any{"root": root}}
}

// DocumentRepository makes sure the document-repository MCP and the definition of its localfs adapter exist in the
// platform namespace. It is idempotent: once the MCP exists (edited or deleted since) nothing is touched. It reports
// whether it seeded. The graph is bootstrapped first (graphsvc.Boot).
func DocumentRepository(ctx context.Context, g *graph.Graph) (bool, error) {
	d := documentRepository()
	if _, err := g.NodeByKey(ctx, domain.NamespacePlatform, mcp.MCPKey(d.Name)); err == nil {
		return false, nil
	} else if !errors.Is(err, graph.ErrNotFound) {
		return false, err
	}
	a := localFSAdapterDef()
	err := graphsvc.SeedChange(ctx, g, domain.NamespacePlatform, "MCP "+d.Name, []graph.NodeEdit{
		graphsvc.SeedNode(mcp.MCPKey(d.Name), mcp.NodeTypeMCP, d.Props()),
		graphsvc.SeedNode(adapter.DefKey(a.Name), domain.TypeAdapterDef, a.Props()),
	})
	return err == nil, err
}

// LocalFS gives the default organisation its document repository on the directory root (the localfs adapter), unless
// it already holds an instance of the MCP. DocumentRepository runs first.
func LocalFS(ctx context.Context, g *graph.Graph, root string) error {
	unit := g.Structure(domain.StructureOrganisation).Root
	if _, err := g.NodeByKey(ctx, "organisation", adapter.Key(unit, "document-repository")); err == nil {
		return nil
	} else if !errors.Is(err, graph.ErrNotFound) {
		return err
	}
	return graphsvc.SeedAdapter(ctx, g, LocalFSAdapter(unit, root))
}
