package gitimport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	importerv1 "github.com/zimwip/goap/contracts/gen/goap/importer/v1"
	"github.com/zimwip/goap/lib/importerkit"
)

// ServiceID is the id of the git importer service (importer-git).
const ServiceID = "importer-git"

// ImporterName is the importer of the code domain the service implements (domains/code.yaml, `importers:`).
const ImporterName = "git"

// Plugin is the git importer as an importer service (ADR 0148): the code domain declares the importer `git` and its
// parameters, the service implements it with an Importer per run.
type Plugin struct {
	// Graph is the graph the runs write to; the principal of a run is in its context.
	Graph Port
	// Roots are the directories the local directory of a run must be under; none: only URLs.
	Roots []string
	// CloneDir keeps the clones of the repositories given by URL, fetched again at each run (default:
	// <user cache>/goap/git).
	CloneDir string
	// Symbols, when set, gives the extractor of a run that asks for the symbols (NewTreeSitter); nil: refused.
	Symbols func() (Extractor, error)

	mu    sync.Mutex
	locks map[string]chan struct{}
}

var (
	_ importerkit.Importer = (*Plugin)(nil)
	_ importerkit.Browser  = (*Plugin)(nil)
)

// Info implements importerkit.Importer.
func (p *Plugin) Info() *importerv1.ImporterInfo {
	return &importerv1.ImporterInfo{Id: ServiceID, Version: "1.0.0",
		Description: "Loads git repositories into the code domain: one change per commit (ADR 0147)",
		Implements:  []*importerv1.Implements{{Domain: Namespace, Importer: ImporterName}}}
}

// remoteRE are the repositories given by URL: https, http, ssh, git, or the scp-like form of ssh (git@host:path).
var remoteRE = regexp.MustCompile(`^(https?://|ssh://|git://)[^\s]+$|^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[^\s]+$`)

// Run implements importerkit.Importer.
func (p *Plugin) Run(ctx context.Context, job *importerkit.Job) error {
	url, local := strings.TrimSpace(job.String("url")), strings.TrimSpace(job.String("directory"))
	repo := url
	switch {
	case url != "" && local != "":
		return errors.New("give the URL of the repository or its local directory, not both")
	case url == "" && local == "":
		return errors.New("the URL of the repository or its local directory is required")
	case url == "":
		repo = local
	}
	name := job.String("name")
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(strings.TrimRight(strings.ReplaceAll(repo, ":", "/"), "/")), ".git")
	}
	// one run at a time per repository of the graph: two would race on the same nodes; a run cancelled while it
	// waits for the other stops there
	unlock, err := p.lock(ctx, name)
	if err != nil {
		return err
	}
	defer unlock()
	var dir string
	if url != "" {
		dir, err = p.remote(ctx, job, url)
	} else {
		dir, err = p.local(ctx, local)
	}
	if err != nil {
		return err
	}
	im := &Importer{Graph: p.Graph, Dir: dir, Name: name, Source: repo, Rev: job.String("rev"), Branches: ParseBranches(job.String("branches")),
		Base: job.String("base"), Max: job.Int("max"),
		ProjectID: job.ProjectID, OwnerOrg: job.OwnerOrg,
		OnPhase:  job.Progress,
		OnBegin:  func(st Stats, c Commit) { begin(job, st, c) },
		OnCommit: func(st Stats) { record(job, st) },
		OnPart:   func(i, n int, name string) { job.Progress(fmt.Sprintf("  part %d/%d: %s", i, n, capLine(name, 100))) }}
	im.Text, im.MaxTextBytes = job.Bool("text"), int64(job.Int("maxText"))<<10
	localDir := ""
	if url == "" {
		localDir = dir
	}
	im.ContentURL = ContentURLOf(job.String("contentUrl"), repo, localDir, name)
	if job.Bool("symbols") {
		if p.Symbols == nil {
			return errors.New("symbols: this importer service reads no symbols")
		}
		x, err := p.Symbols()
		if err != nil {
			return err
		}
		if c, ok := x.(interface{ Close() }); ok {
			defer c.Close()
		}
		im.Symbols, im.Code = x, job.Bool("code")
	}
	st, err := im.Run(ctx)
	record(job, st)
	if err == nil {
		job.Progress(summary(name, st))
	}
	return err
}

// begin says which commit the run imports now: its rank, its hash and its subject.
func begin(job *importerkit.Job, st Stats, c Commit) {
	of := ""
	if st.Total > 0 {
		of = fmt.Sprintf("/%d", st.Total)
	}
	job.Progress(fmt.Sprintf("commit %d%s %s %s", st.Commits+1, of, short(c.Hash), capLine(c.Subject(), 100)))
}

// summary is the last line of a run.
func summary(name string, st Stats) string {
	s := fmt.Sprintf("%s: %d commit(s) imported, up to %s", name, st.Commits, short(st.Last))
	if st.Commits == 0 {
		s = name + " is up to date: no new commit"
	}
	if st.Branches > 0 {
		s += fmt.Sprintf(", %d other branch(es)", st.Branches)
	}
	if len(st.Skipped) > 0 {
		s += "; left out: " + strings.Join(st.Skipped, "; ")
	}
	return s
}

// capLine cuts a line at n runes.
func capLine(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func record(job *importerkit.Job, st Stats) {
	job.Step(int64(st.Commits), int64(st.Total))
	job.Set("commits", int64(st.Commits))
	job.Set("created", int64(st.Created))
	job.Set("modified", int64(st.Modified))
	job.Set("removed", int64(st.Removed))
	job.Set("symbols", int64(st.Symbols))
	job.Set("packages", int64(st.Packages))
	job.Set("branches", int64(st.Branches))
}

func (p *Plugin) lock(ctx context.Context, name string) (func(), error) {
	p.mu.Lock()
	if p.locks == nil {
		p.locks = map[string]chan struct{}{}
	}
	m := p.locks[name]
	if m == nil {
		m = make(chan struct{}, 1)
		p.locks[name] = m
	}
	p.mu.Unlock()
	select {
	case m <- struct{}{}:
		return func() { <-m }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("waiting for the run importing %s: %w", name, context.Cause(ctx))
	}
}

// remote is the clone of a URL, brought up to date.
func (p *Plugin) remote(ctx context.Context, job *importerkit.Job, url string) (string, error) {
	if !remoteRE.MatchString(url) {
		return "", fmt.Errorf("URL %q: only https, http, ssh and git URLs are cloned (a path goes in the local directory)", url)
	}
	return p.clone(ctx, job, url)
}

// local is a repository of a directory under a root, read in place.
func (p *Plugin) local(ctx context.Context, repo string) (string, error) {
	abs, err := p.dir(repo)
	if err != nil {
		return "", err
	}
	if err := git(ctx, "-C", abs, "rev-parse", "--git-dir"); err != nil {
		return "", fmt.Errorf("directory %s: not a git repository", repo)
	}
	return abs, nil
}

// dir resolves a directory a parameter names (~ for the home directory, symbolic links followed), refused when it is
// not under the roots.
func (p *Plugin) dir(repo string) (string, error) {
	if strings.Contains(repo, "://") {
		return "", fmt.Errorf("directory %q: a URL goes in the URL", repo)
	}
	path := repo
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, path[1:])
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("directory %s: %w", repo, err)
	}
	if !p.under(abs) {
		if len(p.Roots) == 0 {
			return "", fmt.Errorf("directory %s: this importer service reads no local directory, give a URL", repo)
		}
		return "", fmt.Errorf("directory %s: not under the directories this importer service reads (%s)", repo, strings.Join(p.Roots, ", "))
	}
	return abs, nil
}

// maxBrowse bounds the directories a listing names.
const maxBrowse = 1000

// Browse implements importerkit.Browser for the parameter directory: the roots when path is empty, else the
// directories in path (the hidden ones left out), each flagged when it is a git repository.
func (p *Plugin) Browse(_ context.Context, _, _, param, path string) (*importerv1.BrowseImportResponse, error) {
	if param != "directory" {
		return nil, fmt.Errorf("%s: not a directory parameter", param)
	}
	out := &importerv1.BrowseImportResponse{}
	if path == "" {
		for _, root := range p.Roots {
			abs, err := filepath.EvalSymlinks(root)
			if err != nil {
				continue
			}
			out.Entries = append(out.Entries, &importerv1.BrowseEntry{Name: abs, Path: abs, Repository: isRepository(abs)})
		}
		return out, nil
	}
	abs, err := p.dir(path)
	if err != nil {
		return nil, err
	}
	des, err := os.ReadDir(abs)
	if err != nil {
		return nil, fmt.Errorf("directory %s: %w", path, err)
	}
	out.Path, out.Repository = abs, isRepository(abs)
	if up := filepath.Dir(abs); up != abs && p.under(up) {
		out.Parent = up
	}
	for _, de := range des {
		if strings.HasPrefix(de.Name(), ".") {
			continue
		}
		full := filepath.Join(abs, de.Name())
		if !de.IsDir() {
			// a symbolic link to a directory under the roots is one too
			if de.Type()&fs.ModeSymlink == 0 {
				continue
			}
			if st, err := os.Stat(full); err != nil || !st.IsDir() {
				continue
			}
		}
		if len(out.Entries) == maxBrowse {
			break
		}
		out.Entries = append(out.Entries, &importerv1.BrowseEntry{Name: de.Name(), Path: full, Repository: isRepository(full)})
	}
	return out, nil
}

// isRepository tells a git repository by its layout, without running git: a working tree (.git, a directory or the
// file of a linked worktree or submodule) or a bare repository (HEAD, objects and refs).
func isRepository(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return true
	}
	for _, f := range []string{"HEAD", "objects", "refs"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			return false
		}
	}
	return true
}

// under tells a path is under one of the roots.
func (p *Plugin) under(abs string) bool {
	for _, root := range p.Roots {
		r, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		if rel, err := filepath.Rel(r, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (p *Plugin) clone(ctx context.Context, job *importerkit.Job, url string) (string, error) {
	base := p.CloneDir
	if base == "" {
		c, err := os.UserCacheDir()
		if err != nil {
			c = os.TempDir()
		}
		base = filepath.Join(c, "goap", "git")
	}
	sum := sha256.Sum256([]byte(url))
	dir := filepath.Join(base, hex.EncodeToString(sum[:8])+".git")
	if _, err := os.Stat(filepath.Join(dir, "HEAD")); err == nil {
		job.Progress("fetching " + url)
		if err := git(ctx, "-C", dir, "fetch", "--prune", "--no-tags", "origin", "+refs/heads/*:refs/heads/*"); err != nil {
			return "", err
		}
		return dir, nil
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", err
	}
	job.Progress("cloning " + url)
	if err := git(ctx, "clone", "--bare", "--no-tags", "--", url, dir); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

func git(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	// never wait on a prompt for credentials
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
