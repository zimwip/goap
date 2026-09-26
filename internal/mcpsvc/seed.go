package mcpsvc

import (
	"context"
	"errors"

	"github.com/zimwip/goap/pkg/mcp"
)

func obj(props map[string]any, required ...string) map[string]any {
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

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

// DocumentRepository is the generic MCP to manipulate documents.
func DocumentRepository() mcp.Def {
	return mcp.Def{Name: "document-repository", Description: "Documents organised in folders.", Tools: []mcp.Tool{
		{Name: "list", Description: "List the documents and folders under a path.", InputSchema: obj(map[string]any{"path": str("folder path, empty for the root")})},
		{Name: "read", Description: "Read the text of a document.", InputSchema: obj(map[string]any{"path": str("document path")}, "path")},
		{Name: "write", Description: "Create or replace a document.", InputSchema: obj(map[string]any{"path": str("document path"), "content": str("text of the document")}, "path", "content")},
	}}
}

// LocalFSAdapter implements document-repository on the localfs connector.
func LocalFSAdapter() mcp.Adapter {
	return mcp.Adapter{MCP: "document-repository", Connector: "localfs", Tools: []mcp.ToolMapping{
		{Tool: "list", Operation: "list_dir", Arguments: map[string]any{"path": "$.path"}, ResultPath: ""},
		{Tool: "read", Operation: "read_file", Arguments: map[string]any{"path": "$.path"}},
		{Tool: "write", Operation: "write_file", Arguments: map[string]any{"path": "$.path", "content": "$.content"}},
	}}
}

// Seed installs the document-repository MCP and its localfs adapter when the hub has no
// MCP of that name; an existing (possibly edited) definition is left alone.
func Seed(ctx context.Context, s Store) error {
	d := DocumentRepository()
	if _, err := s.Mcp(ctx, d.Name); err == nil {
		return nil
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	if err := s.SaveMcp(ctx, d); err != nil {
		return err
	}
	return s.SaveAdapter(ctx, LocalFSAdapter())
}
