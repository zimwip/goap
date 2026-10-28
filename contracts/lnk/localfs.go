package lnk

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/zimwip/goap/contracts/content"
)

// The types of the things of a local-fs source.
const (
	TypeFile      = "File"
	TypeDirectory = "Directory"
)

// LocalFS resolves the sources of kind KindLocalFS: Location is a root directory on the disk of the service, the key
// of a thing its slash-separated path under the root, its type File or Directory (lnk:docs/File/adr/0153.md). Nothing
// outside the root is read: the path is opened through an os.Root. There is no file scheme (ADR 0171 §4): a local
// file is reached through a source someone declared, which a project may map to another root.
type LocalFS struct{}

// What LocalFS.Open gives a reader of a file: its text up to MaxOpenText bytes (cut beyond, Open.Truncated), an image
// of at most MaxOpenInline bytes as a data: address; anything else is a path.
const (
	MaxOpenText   = 1 << 20
	MaxOpenInline = 4 << 20
)

// Resolve stats the path of the URL under the root of the source.
func (LocalFS) Resolve(_ context.Context, src Source, u URL) (Target, error) {
	if u.Version != 0 {
		return Target{}, fmt.Errorf("%s: a local-fs source keeps no versions: %w", u, ErrInvalid)
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

// Open reads what a reader is given of a file resolved: its text (decoded, contracts/content), an image inline, else
// its path; a directory is its path.
func (LocalFS) Open(_ context.Context, src Source, t Target) (Target, error) {
	if t.Kind != TargetFile {
		t.Open = &Open{Mode: OpenPath, Href: t.Location}
		return t, nil
	}
	root, err := os.OpenRoot(src.Location)
	if err != nil {
		return Target{}, fmt.Errorf("source %s: %w", src.Name, err)
	}
	defer root.Close()
	f, err := root.Open(filepath.FromSlash(t.Key))
	if errors.Is(err, fs.ErrNotExist) {
		return Target{}, fmt.Errorf("%s: %w", t.URL, ErrNotFound)
	}
	if err != nil {
		return Target{}, fmt.Errorf("%s: %w", t.URL, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max(MaxOpenText, MaxOpenInline)+1))
	if err != nil {
		return Target{}, fmt.Errorf("%s: %w", t.URL, err)
	}
	info := content.Sniff(t.Key, data)
	switch {
	case info.Text:
		cut := len(data) > MaxOpenText
		if cut {
			data = data[:MaxOpenText]
		}
		text, _ := content.Decode(data, info)
		t.Content = text
		t.Open = &Open{Mode: OpenText, Media: info.Media, Language: info.Language, Truncated: cut}
	case content.IsImage(info.Media) && len(data) <= MaxOpenInline:
		t.Open = &Open{Mode: OpenInline, Media: info.Media, Href: "data:" + info.Media + ";base64," + base64.StdEncoding.EncodeToString(data)}
	default:
		t.Open = &Open{Mode: OpenPath, Media: info.Media, Href: t.Location}
	}
	return t, nil
}

// URLOf gives the URL of a path: Target.Location (absolute, or relative to the root) or Target.Key.
func (LocalFS) URLOf(_ context.Context, src Source, t Target) (URL, error) {
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
