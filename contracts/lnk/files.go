package lnk

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// The types of the things of a files source.
const (
	TypeFile      = "File"
	TypeDirectory = "Directory"
)

// Files resolves the sources of kind KindFiles: Location is a root directory, the key of a thing its slash-separated
// path under the root, its type File or Directory (lnk:docs/File/adr/0153.md). Nothing outside the root is read: the
// path is opened through an os.Root.
type Files struct{}

// Resolve stats the path of the URL under the root of the source.
func (Files) Resolve(_ context.Context, src Source, u URL) (Target, error) {
	if u.Version != 0 {
		return Target{}, fmt.Errorf("%s: a files source keeps no versions: %w", u, ErrInvalid)
	}
	p, err := cleanKey(u.Key)
	if err != nil {
		return Target{}, fmt.Errorf("%s: %w", u, err)
	}
	root, err := os.OpenRoot(src.Location)
	if err != nil {
		return Target{}, fmt.Errorf("source %s: %w", src.Name, err)
	}
	defer root.Close()
	info, err := root.Stat(filepath.FromSlash(p))
	if errors.Is(err, fs.ErrNotExist) {
		return Target{}, fmt.Errorf("%s: %w", u, ErrNotFound)
	}
	if err != nil {
		return Target{}, fmt.Errorf("%s: %w", u, err)
	}
	kind, typ := TargetFile, TypeFile
	if info.IsDir() {
		kind, typ = TargetDirectory, TypeDirectory
	}
	if u.Type != typ {
		return Target{}, fmt.Errorf("%s is a %s: %w", u, typ, ErrNotFound)
	}
	props := map[string]any{"modified": info.ModTime().UTC()}
	if !info.IsDir() {
		props["size"] = info.Size()
	}
	return Target{
		URL: u, Kind: kind, Type: typ, Key: p, Title: path.Base(p),
		Location: filepath.Join(src.Location, filepath.FromSlash(p)), Properties: props,
	}, nil
}

// URLOf gives the URL of a path: Target.Location (absolute, or relative to the root) or Target.Key.
func (Files) URLOf(_ context.Context, src Source, t Target) (URL, error) {
	p := t.Key
	if t.Location != "" {
		rel := t.Location
		if filepath.IsAbs(rel) {
			r, err := filepath.Rel(src.Location, rel)
			if err != nil {
				return URL{}, fmt.Errorf("%s: %w", t.Location, ErrInvalid)
			}
			rel = r
		}
		p = filepath.ToSlash(rel)
	}
	p, err := cleanKey(p)
	if err != nil {
		return URL{}, err
	}
	typ := TypeFile
	if t.Kind == TargetDirectory || t.Type == TypeDirectory {
		typ = TypeDirectory
	}
	return New(src.Name, typ, p), nil
}

// cleanKey keeps a clean relative path that stays under the root ("." is the root itself).
func cleanKey(k string) (string, error) {
	if p := path.Clean(k); p != k || path.IsAbs(k) || p == ".." || strings.HasPrefix(p, "../") {
		return "", fmt.Errorf("path %q: not a clean path under the root: %w", k, ErrInvalid)
	}
	return k, nil
}
