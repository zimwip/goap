package workspace

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Bounds of the file operations: what a model is given back stays readable.
const (
	// MaxFileSize bounds a file read or searched.
	MaxFileSize = 4 << 20
	// DefaultReadLines is the number of lines read when the call names none.
	DefaultReadLines = 2000
	// MaxLineLength cuts the longer lines of a read or a search.
	MaxLineLength = 2000
	// DefaultResults bounds a glob or a search.
	DefaultResults = 250
)

// skipDirs are never walked by glob and grep: the metadata of the sources.
var skipDirs = map[string]bool{".git": true}

func files(s space, op string, args map[string]any) (map[string]any, error) {
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	p := rel(stringArg(args, "path"))
	switch op {
	case OpRead:
		return read(root, p, intArg(args, "offset", 1), intArg(args, "limit", DefaultReadLines))
	case OpWrite:
		return write(root, p, stringArg(args, "content"))
	case OpEdit:
		return edit(root, p, stringArg(args, "old"), stringArg(args, "new"), boolArg(args, "all"))
	case OpLs:
		return ls(root, p)
	case OpGlob:
		return glob(root, p, stringArg(args, "pattern"), intArg(args, "limit", DefaultResults))
	default:
		return grep(root, p, args)
	}
}

func readText(root *os.Root, p string) ([]byte, error) {
	info, err := root.Stat(p)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory", p)
	}
	if info.Size() > MaxFileSize {
		return nil, fmt.Errorf("%s is larger than %d bytes", p, MaxFileSize)
	}
	b, err := root.ReadFile(p)
	if err != nil {
		return nil, err
	}
	if binary(b) {
		return nil, fmt.Errorf("%s is not a text file", p)
	}
	return b, nil
}

func binary(b []byte) bool {
	head := b
	if len(head) > 8000 {
		head = head[:8000]
	}
	return bytes.IndexByte(head, 0) >= 0
}

func cutLine(l string) string {
	if len(l) <= MaxLineLength {
		return l
	}
	cut := MaxLineLength
	for cut > 0 && !utf8.RuneStart(l[cut]) {
		cut--
	}
	return l[:cut] + "…"
}

func read(root *os.Root, p string, offset, limit int) (map[string]any, error) {
	b, err := readText(root, p)
	if err != nil {
		return nil, err
	}
	if offset < 1 {
		offset = 1
	}
	if limit < 1 {
		limit = DefaultReadLines
	}
	lines := strings.Split(string(b), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	var out strings.Builder
	last := min(len(lines), offset-1+limit)
	for i := offset - 1; i < last; i++ {
		fmt.Fprintf(&out, "%6d\t%s\n", i+1, cutLine(lines[i]))
	}
	return map[string]any{"path": p, "text": out.String(), "lines": float64(len(lines)), "from": float64(offset),
		"to": float64(last), "truncated": last < len(lines)}, nil
}

func write(root *os.Root, p, content string) (map[string]any, error) {
	if p == "." {
		return nil, errors.New("write needs a file path")
	}
	_, err := root.Stat(p)
	created := errors.Is(err, fs.ErrNotExist)
	if dir := path.Dir(p); dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	if err := root.WriteFile(p, []byte(content), 0o644); err != nil {
		return nil, err
	}
	return map[string]any{"path": p, "bytes": float64(len(content)), "created": created}, nil
}

func edit(root *os.Root, p, old, repl string, all bool) (map[string]any, error) {
	if old == "" {
		return nil, errors.New("old must not be empty: write creates a file")
	}
	if old == repl {
		return nil, errors.New("old and new are the same")
	}
	b, err := readText(root, p)
	if err != nil {
		return nil, err
	}
	text := string(b)
	n := strings.Count(text, old)
	switch {
	case n == 0:
		return nil, fmt.Errorf("old was not found in %s", p)
	case n > 1 && !all:
		return nil, fmt.Errorf("old was found %d times in %s: give more context to make it unique, or all", n, p)
	}
	if all {
		text = strings.ReplaceAll(text, old, repl)
	} else {
		text = strings.Replace(text, old, repl, 1)
	}
	if err := root.WriteFile(p, []byte(text), 0o644); err != nil {
		return nil, err
	}
	return map[string]any{"path": p, "replacements": float64(n)}, nil
}

func ls(root *os.Root, p string) (map[string]any, error) {
	entries, err := fs.ReadDir(root.FS(), p)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(entries))
	for _, e := range entries {
		item := map[string]any{"name": e.Name(), "dir": e.IsDir()}
		if info, err := e.Info(); err == nil && !e.IsDir() {
			item["size"] = float64(info.Size())
		}
		out = append(out, item)
	}
	return map[string]any{"path": p, "entries": out}, nil
}

// match reports whether a slash-separated path matches a glob pattern where ** stands for any number of
// directories.
func match(pattern, name string) bool {
	return matchParts(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchParts(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for i := 0; i <= len(name); i++ {
				if matchParts(pat[1:], name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if ok, err := path.Match(pat[0], name[0]); err != nil || !ok {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}

// walk visits the files under p, sources metadata left out, with their path relative to p.
func walk(root *os.Root, p string, visit func(full, relative string, d fs.DirEntry) error) error {
	return fs.WalkDir(root.FS(), p, func(full string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] && full != p {
				return fs.SkipDir
			}
			return nil
		}
		relative := full
		if p != "." {
			relative = strings.TrimPrefix(full, p+"/")
		}
		return visit(full, relative, d)
	})
}

var errEnough = errors.New("enough")

func glob(root *os.Root, p, pattern string, limit int) (map[string]any, error) {
	if pattern == "" {
		return nil, errors.New("glob needs a pattern")
	}
	pattern = strings.TrimPrefix(path.Clean(pattern), "/")
	if limit < 1 {
		limit = DefaultResults
	}
	type hit struct {
		path string
		mod  int64
	}
	var hits []hit
	err := walk(root, p, func(full, relative string, d fs.DirEntry) error {
		if !match(pattern, relative) {
			return nil
		}
		var mod int64
		if info, err := d.Info(); err == nil {
			mod = info.ModTime().UnixNano()
		}
		hits = append(hits, hit{full, mod})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].mod > hits[j].mod })
	truncated := len(hits) > limit
	if truncated {
		hits = hits[:limit]
	}
	out := make([]any, len(hits))
	for i, h := range hits {
		out[i] = h.path
	}
	return map[string]any{"files": out, "truncated": truncated}, nil
}

func grep(root *os.Root, p string, args map[string]any) (map[string]any, error) {
	expr := stringArg(args, "pattern")
	if boolArg(args, "ignoreCase") {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, fmt.Errorf("pattern: %w", err)
	}
	mode := stringArg(args, "mode")
	if mode == "" {
		mode = "files"
	}
	if mode != "files" && mode != "content" && mode != "count" {
		return nil, fmt.Errorf("mode %q: files, content or count", mode)
	}
	only := stringArg(args, "glob")
	around := max(0, intArg(args, "context", 0))
	limit := intArg(args, "limit", DefaultResults)
	if limit < 1 {
		limit = DefaultResults
	}
	var results []any
	truncated := false
	err = walk(root, p, func(full, relative string, d fs.DirEntry) error {
		if only != "" {
			target := d.Name()
			if strings.Contains(only, "/") {
				target = relative
			}
			if !match(only, target) {
				return nil
			}
		}
		info, err := d.Info()
		if err != nil || info.Size() > MaxFileSize {
			return nil
		}
		b, err := root.ReadFile(full)
		if err != nil || binary(b) || !re.Match(b) {
			return nil
		}
		if len(results) >= limit {
			truncated = true
			return errEnough
		}
		switch mode {
		case "files":
			results = append(results, full)
		case "count":
			results = append(results, map[string]any{"path": full, "count": float64(len(re.FindAllIndex(b, -1)))})
		default:
			results = append(results, map[string]any{"path": full, "lines": matchingLines(b, re, around)})
		}
		return nil
	})
	if err != nil && !errors.Is(err, errEnough) {
		return nil, err
	}
	return map[string]any{"mode": mode, "results": results, "truncated": truncated}, nil
}

// matchingLines are the lines of a file matching re, "<n>:<text>", with the lines around them "<n>-<text>".
func matchingLines(b []byte, re *regexp.Regexp, around int) string {
	var lines []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), MaxFileSize)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	keep := map[int]bool{}
	hit := map[int]bool{}
	for i, l := range lines {
		if re.MatchString(l) {
			hit[i] = true
			for j := max(0, i-around); j <= min(len(lines)-1, i+around); j++ {
				keep[j] = true
			}
		}
	}
	var out strings.Builder
	prev := -2
	for i, l := range lines {
		if !keep[i] {
			continue
		}
		if around > 0 && prev >= 0 && i > prev+1 {
			out.WriteString("--\n")
		}
		sep := "-"
		if hit[i] {
			sep = ":"
		}
		fmt.Fprintf(&out, "%d%s%s\n", i+1, sep, cutLine(l))
		prev = i
	}
	return out.String()
}
