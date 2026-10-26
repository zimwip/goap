package main

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

// The Go modules of the repository (go.work), each its own directory, and the path their packages are imported by.
var goModules = []string{"contracts", "lib", "graph", "engine", "mcp", "gateway", "platform"}

const modulePath = "github.com/zimwip/goap/"

// The binaries built by cargo: the graph service and the proxy.
var cargoBinaries = map[string]string{"goap-graph-rs": "graph-service", "goap-proxy": "goap-proxy"}

// watcher rebuilds what a change of the sources touches and restarts the services running it (-watch, make dev):
// the Go binaries depending on a package changed, the Rust ones on a change of rust/, the graph services on a change
// of domains/ (they publish the shipped domains at start), the registry on one of methodologies/.
type watcher struct {
	cfg      config
	sup      *supervisor
	log      *slog.Logger
	services []service
	seen     map[string]stamp
	// rust asks the cargo builder for a build: cargo takes minutes where go takes seconds, so it builds on its own,
	// the Go services reloaded meanwhile; the changes made during a build make one more after it
	rust chan struct{}
}

type stamp struct {
	mod  time.Time
	size int64
}

// watch polls the sources every second until the context is done.
func watch(ctx context.Context, cfg config, sup *supervisor, log *slog.Logger, services []service) {
	w := &watcher{cfg: cfg, sup: sup, log: log, services: services, rust: make(chan struct{}, 1)}
	w.seen = w.scan()
	sup.wg.Add(1)
	go func() {
		defer sup.wg.Done()
		w.cargo(ctx)
	}()
	log.Info("watching the sources: a change rebuilds and restarts the services it touches")
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		now := w.scan()
		changed := diff(w.seen, now)
		if len(changed) == 0 {
			continue
		}
		// the files settle (an editor saving several, a git checkout) before anything is built
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(300 * time.Millisecond):
			}
			again := w.scan()
			more := diff(now, again)
			now = again
			if len(more) == 0 {
				break
			}
			changed = append(changed, more...)
		}
		w.seen = now
		w.apply(ctx, changed)
	}
}

// scan stamps the files a service is built or seeded from.
func (w *watcher) scan() map[string]stamp {
	out := map[string]stamp{}
	walk := func(dir string, keep func(rel string) bool) {
		root := filepath.Join(w.cfg.root, dir)
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				switch d.Name() {
				case "node_modules", "target", "testdata", ".git":
					return filepath.SkipDir
				}
				return nil
			}
			rel, _ := filepath.Rel(w.cfg.root, path)
			rel = filepath.ToSlash(rel)
			if !keep(rel) {
				return nil
			}
			if info, err := d.Info(); err == nil {
				out[rel] = stamp{info.ModTime(), info.Size()}
			}
			return nil
		})
	}
	for _, m := range goModules {
		walk(m, func(rel string) bool {
			if strings.HasSuffix(rel, "_test.go") {
				return false
			}
			switch filepath.Ext(rel) {
			case ".go", ".sql", ".yaml", ".yml", ".json", ".mjs", ".tmpl", ".mod", ".sum":
				return true
			}
			return false
		})
	}
	walk("rust", func(rel string) bool {
		switch filepath.Ext(rel) {
		case ".rs", ".toml", ".lock", ".json", ".sql":
			return true
		}
		return false
	})
	for _, dir := range []string{"domains", "methodologies"} {
		walk(dir, func(rel string) bool { return strings.HasSuffix(rel, ".yaml") && !strings.Contains(rel, "/examples/") })
	}
	return out
}

// diff lists the files added, changed or removed between two scans.
func diff(before, after map[string]stamp) []string {
	var out []string
	for f, s := range after {
		if before[f] != s {
			out = append(out, f)
		}
	}
	for f := range before {
		if _, ok := after[f]; !ok {
			out = append(out, f)
		}
	}
	return out
}

// apply rebuilds and restarts what the changed files touch.
func (w *watcher) apply(ctx context.Context, changed []string) {
	slices.Sort(changed)
	changed = slices.Compact(changed)
	var goPkgs []string
	allGo, rust := false, false
	restart := map[string]bool{} // services restarted with their binary as is
	for _, f := range changed {
		top, rest, _ := strings.Cut(f, "/")
		switch {
		case top == "rust":
			rust = true
		case top == "domains":
			for _, s := range w.services {
				if s.bin == "goap-graph-rs" || s.bin == "graph" {
					restart[s.name] = true
				}
			}
		case top == "methodologies":
			restart["registry"] = true
		case slices.Contains(goModules, top):
			if rest == "go.mod" || rest == "go.sum" {
				allGo = true
				continue
			}
			goPkgs = append(goPkgs, modulePath+top+"/"+filepath.ToSlash(filepath.Dir(rest)))
			// the Rust graph embeds the migrations of the graph, the domain registry and the smart tags
			if strings.Contains(rest, "/migrations") && top == "graph" {
				rust = true
			}
		}
	}
	w.log.Info("sources changed", "files", len(changed))
	if len(goPkgs) > 0 || allGo {
		w.buildGo(ctx, goPkgs, allGo)
	}
	if rust {
		select {
		case w.rust <- struct{}{}:
		default: // one asked already
		}
	}
	if len(restart) > 0 {
		w.sup.restart(ctx, slices.Sorted(maps.Keys(restart)), func() {})
	}
}

// goBinaries are the Go binaries the stack runs, but goap-local itself.
func (w *watcher) goBinaries() []string {
	var out []string
	for _, s := range w.services {
		if s.bin == "" || s.path != "" || cargoBinaries[s.bin] != "" || slices.Contains(out, s.bin) {
			continue
		}
		out = append(out, s.bin)
	}
	return out
}

// buildGo builds the binaries depending on the changed packages and restarts the services whose binary changed.
func (w *watcher) buildGo(ctx context.Context, pkgs []string, all bool) {
	bins := w.goBinaries()
	if !all {
		deps, err := w.goDeps(ctx, bins)
		if err != nil {
			w.log.Warn("go list", "err", err)
			return
		}
		var touched []string
		for _, b := range bins {
			if slices.ContainsFunc(pkgs, func(p string) bool { return slices.Contains(deps[b], p) }) {
				touched = append(touched, b)
			}
		}
		if slices.ContainsFunc(pkgs, func(p string) bool { return p == modulePath+"platform/cmd/goap-local" }) {
			w.log.Warn("goap-local itself changed: rebuild it and start it again to take it (make dev)")
		}
		bins = touched
	}
	if len(bins) == 0 {
		return
	}
	staging := filepath.Join(w.cfg.dir, "build")
	args := []string{"build", "-o", staging + string(os.PathSeparator)}
	for _, b := range bins {
		args = append(args, "./cmd/"+b)
	}
	w.log.Info("building", "binaries", bins)
	if err := w.command(ctx, "go-build", filepath.Join(w.cfg.root, "platform"), "go", args...); err != nil {
		w.log.Warn("the build failed: the services keep running as they were", "err", err)
		return
	}
	for _, b := range bins {
		w.replace(ctx, b, filepath.Join(staging, exe(b)))
	}
}

// goDeps lists the packages each binary is built from (go list -deps).
func (w *watcher) goDeps(ctx context.Context, bins []string) (map[string][]string, error) {
	args := []string{"list", "-f", "{{.Name}} {{.ImportPath}} {{join .Deps \" \"}}"}
	for _, b := range bins {
		args = append(args, "./cmd/"+b)
	}
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = filepath.Join(w.cfg.root, "platform")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	deps := map[string][]string{}
	for line := range strings.Lines(string(out)) {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		bin := f[1][strings.LastIndex(f[1], "/")+1:]
		deps[bin] = append(f[1:2], f[2:]...)
	}
	return deps, nil
}

// cargo builds the Rust binaries each time asked, until the context is done.
func (w *watcher) cargo(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.rust:
			w.buildRust(ctx)
		}
	}
}

// buildRust builds the cargo binaries the stack runs and restarts the services whose binary changed.
func (w *watcher) buildRust(ctx context.Context) {
	var bins []string
	args := []string{"build", "--profile", w.cfg.cargoProfile, "--locked"}
	for _, s := range w.services {
		if p := cargoBinaries[s.bin]; p != "" && !slices.Contains(bins, s.bin) {
			bins = append(bins, s.bin)
			args = append(args, "-p", p)
		}
	}
	if len(bins) == 0 {
		return
	}
	w.log.Info("building", "binaries", bins)
	if err := w.command(ctx, "cargo-build", filepath.Join(w.cfg.root, "rust"), "cargo", args...); err != nil {
		w.log.Warn("the build failed: the services keep running as they were", "err", err)
		return
	}
	for _, b := range bins {
		w.replace(ctx, b, filepath.Join(w.cfg.root, "rust", "target", cargoDir(w.cfg.cargoProfile), exe(b)))
	}
}

// replace restarts the services running a binary when the one built differs, putting it in place while they are
// stopped (Windows refuses to overwrite a running program).
func (w *watcher) replace(ctx context.Context, bin, built string) {
	next, err := os.ReadFile(built)
	if err != nil {
		w.log.Warn("built binary", "bin", bin, "err", err)
		return
	}
	target := filepath.Join(w.cfg.bin, exe(bin))
	if cur, err := os.ReadFile(target); err == nil && bytes.Equal(cur, next) {
		w.log.Info("binary unchanged", "bin", bin) // a build is reproducible: nothing of this binary changed
		return
	}
	var names []string
	for _, s := range w.services {
		if s.bin == bin {
			names = append(names, s.name)
		}
	}
	w.sup.restart(ctx, names, func() {
		tmp := target + ".new"
		if err := os.WriteFile(tmp, next, 0o755); err == nil {
			err = os.Rename(tmp, target)
		}
		if err != nil {
			w.log.Warn("binary not replaced", "bin", bin, "err", err)
		}
	})
}

// command runs a build, its output prefixed like a service's.
func (w *watcher) command(ctx context.Context, name, dir, path string, args ...string) error {
	file, err := os.OpenFile(filepath.Join(w.cfg.dir, "logs", name+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	p, err := exec.LookPath(path)
	if err != nil {
		return err
	}
	if err := w.sup.run(ctx, &lines{name: name, file: file, out: &w.sup.out}, dir, p, args, os.Environ()); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// cargoDir is the directory of target/ cargo puts a profile's binaries in.
func cargoDir(profile string) string {
	if profile == "dev" {
		return "debug"
	}
	return profile
}

func exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}
