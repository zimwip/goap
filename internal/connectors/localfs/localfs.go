// Package localfs is the reference connector: it exposes a directory of the local
// file system. The root directory is configured per organization ("root"), and
// every path is confined under it, symbolic links included.
package localfs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"

	"google.golang.org/protobuf/types/known/structpb"

	connectorv1 "github.com/zimwip/goap/gen/goap/connector/v1"
	"github.com/zimwip/goap/internal/connectorkit"
)

// MaxFileSize bounds what read_file returns.
const MaxFileSize = 1 << 20

// Connector implements connectorkit.Connector on the local file system.
type Connector struct {
	// Roots, when not empty, restricts the "root" an organization may configure to
	// these directories or their sub-directories.
	Roots []string
}

var _ connectorkit.Connector = Connector{}

func schema(v map[string]any) *structpb.Struct {
	s, _ := structpb.NewStruct(v)
	return s
}

// Info implements connectorkit.Connector.
func (Connector) Info() *connectorv1.ConnectorInfo {
	pathArg := map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}
	return &connectorv1.ConnectorInfo{
		Id: "localfs", Version: "1", Description: "Directory of the local file system of the connector host.",
		ConfigSchema: schema(map[string]any{"type": "object", "required": []any{"root"}, "properties": map[string]any{
			"root": map[string]any{"type": "string", "description": "directory exposed to the organization"}}}),
		Operations: []*connectorv1.Operation{
			{Name: "list_dir", Description: "List a directory: {entries: [{name, dir, size}]}", InputSchema: schema(pathArg)},
			{Name: "read_file", Description: "Read a text file: {text}", InputSchema: schema(pathArg)},
			{Name: "write_file", Description: "Create or replace a text file", InputSchema: schema(map[string]any{"type": "object",
				"required": []any{"path", "content"}, "properties": map[string]any{"path": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}}})},
		},
	}
}

func (c Connector) root(config map[string]any) (*os.Root, error) {
	dir, _ := config["root"].(string)
	if dir == "" {
		return nil, errors.New("the binding has no \"root\" configured")
	}
	if len(c.Roots) > 0 {
		ok := false
		for _, allowed := range c.Roots {
			ok = ok || within(allowed, dir)
		}
		if !ok {
			return nil, fmt.Errorf("root %q is not allowed on this connector", dir)
		}
	}
	return os.OpenRoot(dir)
}

func within(base, dir string) bool {
	rel, err := relPath(base, dir)
	return err == nil && rel != ""
}

func relPath(base, dir string) (string, error) {
	base, dir = path.Clean(base), path.Clean(dir)
	if dir == base {
		return ".", nil
	}
	if len(dir) > len(base) && dir[:len(base)] == base && dir[len(base)] == '/' {
		return dir[len(base)+1:], nil
	}
	return "", errors.New("outside")
}

func rel(args map[string]any) string {
	p, _ := args["path"].(string)
	p = path.Clean("/" + p)[1:]
	if p == "" {
		return "."
	}
	return p
}

// Invoke implements connectorkit.Connector.
func (c Connector) Invoke(_ context.Context, op string, args, config map[string]any, _ map[string]string) (map[string]any, error) {
	root, err := c.root(config)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	p := rel(args)
	switch op {
	case "list_dir":
		f, err := root.Open(p)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		ents, err := f.ReadDir(-1)
		if err != nil {
			return nil, err
		}
		sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
		list := make([]any, 0, len(ents))
		for _, e := range ents {
			var size int64
			if info, err := e.Info(); err == nil {
				size = info.Size()
			}
			list = append(list, map[string]any{"name": e.Name(), "dir": e.IsDir(), "size": float64(size)})
		}
		return map[string]any{"entries": list}, nil
	case "read_file":
		st, err := root.Stat(p)
		if err != nil {
			return nil, err
		}
		if st.IsDir() {
			return nil, fmt.Errorf("%s is a directory", p)
		}
		if st.Size() > MaxFileSize {
			return nil, fmt.Errorf("%s is larger than %d bytes", p, MaxFileSize)
		}
		b, err := fs.ReadFile(root.FS(), p)
		if err != nil {
			return nil, err
		}
		return map[string]any{"text": string(b)}, nil
	case "write_file":
		content, _ := args["content"].(string)
		if p == "." {
			return nil, errors.New("path is required")
		}
		if dir := path.Dir(p); dir != "." {
			if err := root.MkdirAll(dir, 0o755); err != nil {
				return nil, err
			}
		}
		f, err := root.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		if _, err := f.WriteString(content); err != nil {
			return nil, err
		}
		return map[string]any{"path": p, "bytes": float64(len(content))}, nil
	}
	return nil, connectorkit.ErrUnknownOperation
}
