package localfs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalFS(t *testing.T) {
	dir := t.TempDir()
	cfg := map[string]any{"root": dir}
	c := Connector{}
	ctx := context.Background()

	if _, err := c.Invoke(ctx, "write_file", map[string]any{"path": "docs/a.txt", "content": "hello"}, cfg, nil); err != nil {
		t.Fatal(err)
	}
	got, err := c.Invoke(ctx, "read_file", map[string]any{"path": "docs/a.txt"}, cfg, nil)
	if err != nil || got["text"] != "hello" {
		t.Fatalf("read = %v, %v", got, err)
	}
	ls, err := c.Invoke(ctx, "list_dir", map[string]any{"path": "docs"}, cfg, nil)
	if err != nil || len(ls["entries"].([]any)) != 1 {
		t.Fatalf("list = %v, %v", ls, err)
	}
	root, err := c.Invoke(ctx, "list_dir", map[string]any{}, cfg, nil)
	if err != nil || len(root["entries"].([]any)) != 1 {
		t.Fatalf("list root = %v, %v", root, err)
	}

	// confinement: traversal and symlinks cannot leave the root
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"link/secret", "../" + filepath.Base(outside) + "/secret", "/../../etc/passwd"} {
		if got, err := c.Invoke(ctx, "read_file", map[string]any{"path": p}, cfg, nil); err == nil {
			t.Errorf("%s escaped the root: %v", p, got)
		}
	}
	if _, err := c.Invoke(ctx, "write_file", map[string]any{"path": "link/new", "content": "x"}, cfg, nil); err == nil {
		t.Error("write through a symlink escaped the root")
	}

	if _, err := c.Invoke(ctx, "list_dir", nil, map[string]any{}, nil); err == nil {
		t.Error("missing root accepted")
	}
	restricted := Connector{Roots: []string{filepath.Join(dir, "docs")}}
	if _, err := restricted.Invoke(ctx, "list_dir", nil, cfg, nil); err == nil {
		t.Error("root outside the allowed ones accepted")
	}
	if _, err := restricted.Invoke(ctx, "list_dir", nil, map[string]any{"root": filepath.Join(dir, "docs")}, nil); err != nil {
		t.Errorf("allowed root refused: %v", err)
	}
}
