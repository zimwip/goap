package gitimport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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
	// Roots are the local directories a repository parameter may name a path under (a URL is cloned); none: only URLs.
	Roots []string
	// CloneDir keeps the clones of the repositories given by URL, fetched again at each run (default:
	// <user cache>/goap/git).
	CloneDir string
	// Symbols, when set, gives the extractor of a run that asks for the symbols (NewTreeSitter); nil: refused.
	Symbols func() (Extractor, error)

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

var _ importerkit.Importer = (*Plugin)(nil)

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
	repo := strings.TrimSpace(job.String("repository"))
	if repo == "" {
		return errors.New("repository required")
	}
	name := job.String("name")
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(strings.TrimRight(strings.ReplaceAll(repo, ":", "/"), "/")), ".git")
	}
	// one run at a time per repository of the graph: two would race on the same nodes
	unlock := p.lock(name)
	defer unlock()
	dir, err := p.checkout(ctx, job, repo)
	if err != nil {
		return err
	}
	im := &Importer{Graph: p.Graph, Dir: dir, Name: name, Source: repo, Rev: job.String("rev"), Base: job.String("base"), Max: job.Int("max"),
		ProjectID: job.ProjectID, OwnerOrg: job.OwnerOrg, Progress: job, ProgressEvery: 10,
		OnCommit: func(st Stats) { record(job, st) }}
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
		im.Symbols = x
	}
	st, err := im.Run(ctx)
	record(job, st)
	return err
}

func record(job *importerkit.Job, st Stats) {
	job.Set("commits", int64(st.Commits))
	job.Set("created", int64(st.Created))
	job.Set("modified", int64(st.Modified))
	job.Set("removed", int64(st.Removed))
	job.Set("symbols", int64(st.Symbols))
}

func (p *Plugin) lock(name string) func() {
	p.mu.Lock()
	if p.locks == nil {
		p.locks = map[string]*sync.Mutex{}
	}
	m := p.locks[name]
	if m == nil {
		m = &sync.Mutex{}
		p.locks[name] = m
	}
	p.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// checkout is the local repository of a run: a path under a root, or the clone of a URL brought up to date.
func (p *Plugin) checkout(ctx context.Context, job *importerkit.Job, repo string) (string, error) {
	if remoteRE.MatchString(repo) {
		return p.clone(ctx, job, repo)
	}
	if strings.Contains(repo, "://") {
		return "", fmt.Errorf("repository %q: only https, http, ssh and git URLs are cloned", repo)
	}
	abs, err := filepath.Abs(repo)
	if err != nil {
		return "", err
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("repository %s: %w", repo, err)
	}
	for _, root := range p.Roots {
		r, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		if rel, err := filepath.Rel(r, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return abs, nil
		}
	}
	if len(p.Roots) == 0 {
		return "", fmt.Errorf("repository %s: this importer service reads no local path, give a URL", repo)
	}
	return "", fmt.Errorf("repository %s: not under the directories this importer service reads (%s)", repo, strings.Join(p.Roots, ", "))
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
