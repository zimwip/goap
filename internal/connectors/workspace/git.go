package workspace

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// baseKey is the git configuration of a source holding the commit the change started from.
const baseKey = "goap.base"

// gitTimeout bounds a git operation of the connector (a clone of a large repository included).
const gitTimeout = 10 * time.Minute

// git runs git in dir with the environment of the workspace and, when the unit gives one, the token of the
// remote, passed as configuration in the environment (never on the command line, never to the commands).
func git(ctx context.Context, s space, dir string, auth bool, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir, cmd.Env = dir, environment(s)
	if token := s.secrets[SecretGitToken]; auth && token != "" {
		basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
		cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Basic "+basic)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", args[0], msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// repoName is the directory a repository is cloned in by default: the last element of its URL or path.
func repoName(repo string) string {
	repo = strings.TrimRight(filepath.ToSlash(repo), "/")
	if i := strings.LastIndexAny(repo, "/:"); i >= 0 {
		repo = repo[i+1:]
	}
	return strings.TrimSuffix(repo, ".git")
}

func (c Connector) open(ctx context.Context, s space, args map[string]any) (map[string]any, error) {
	list, _ := args["sources"].([]any)
	for _, item := range list {
		src, _ := item.(map[string]any)
		repo := stringArg(src, "repo")
		if repo == "" {
			return nil, errors.New("a source needs a repo")
		}
		p := rel(stringArg(src, "path"))
		if p == "." {
			p = rel(repoName(repo))
		}
		if p == "." || strings.Contains(p, "/") {
			return nil, fmt.Errorf("source %s: path %q must name a directory at the top of the workspace", repo, p)
		}
		branch := stringArg(src, "branch")
		if branch == "" {
			branch = branchOf(s)
		}
		dir := s.local(p)
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			continue // already cloned: opening again keeps the work
		}
		clone := []string{"clone", "--quiet"}
		if ref := stringArg(src, "ref"); ref != "" {
			clone = append(clone, "--branch", ref)
		}
		if _, err := git(ctx, s, s.dir, true, append(clone, "--", repo, p)...); err != nil {
			return nil, err
		}
		if _, err := git(ctx, s, dir, false, "checkout", "--quiet", "-B", branch); err != nil {
			return nil, err
		}
		head, err := git(ctx, s, dir, false, "rev-parse", "HEAD")
		if err != nil {
			return nil, err
		}
		// the commit the change started from: what diff compares with, whatever the commits made since
		if _, err := git(ctx, s, dir, false, "config", baseKey, head); err != nil {
			return nil, err
		}
	}
	return map[string]any{"directory": s.dir, "sources": sources(ctx, s)}, nil
}

// sources are the git repositories at the top of the workspace: their branch, head and whether they changed.
func sources(ctx context.Context, s space) []any {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return []any{}
	}
	out := []any{}
	for _, e := range entries {
		dir := filepath.Join(s.dir, e.Name())
		if _, err := os.Stat(filepath.Join(dir, ".git")); !e.IsDir() || err != nil {
			continue
		}
		item := map[string]any{"path": e.Name()}
		if b, err := git(ctx, s, dir, false, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
			item["branch"] = b
		}
		if h, err := git(ctx, s, dir, false, "rev-parse", "HEAD"); err == nil {
			item["head"] = h
		}
		if st, err := git(ctx, s, dir, false, "status", "--porcelain"); err == nil {
			item["changed"] = st != ""
		}
		if r, err := git(ctx, s, dir, false, "remote", "get-url", "origin"); err == nil {
			item["remote"] = r
		}
		out = append(out, item)
	}
	sort.Slice(out, func(a, b int) bool {
		return out[a].(map[string]any)["path"].(string) < out[b].(map[string]any)["path"].(string)
	})
	return out
}

// diff gives, per source (or for the one named), what changed: the files (git status, new files included) and the
// diff of the tracked ones against the commit the change started from, commits made since included.
func diff(ctx context.Context, s space, args map[string]any) (map[string]any, error) {
	var dirs []string
	if p := rel(stringArg(args, "path")); p != "." {
		dirs = []string{p}
	} else {
		for _, src := range sources(ctx, s) {
			dirs = append(dirs, src.(map[string]any)["path"].(string))
		}
	}
	out := []any{}
	for _, p := range dirs {
		dir := s.local(p)
		status, err := git(ctx, s, dir, false, "status", "--porcelain")
		if err != nil {
			return nil, err
		}
		base := "HEAD"
		if b, err := git(ctx, s, dir, false, "config", "--get", baseKey); err == nil && b != "" {
			base = b
		}
		text, err := git(ctx, s, dir, false, "diff", base)
		if err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"path": p, "base": base, "status": status, "diff": capOutput([]byte(text))})
	}
	return map[string]any{"sources": out}, nil
}

// push pushes the current branch of a source to its origin, only when it is the branch of the change: the commands
// of the workspace do not get the token, so that what reaches the remote is the work of the change only.
func push(ctx context.Context, s space, args map[string]any) (map[string]any, error) {
	p := rel(stringArg(args, "path"))
	if p == "." {
		return nil, errors.New("push needs the directory of a source")
	}
	dir := s.local(p)
	branch, err := git(ctx, s, dir, false, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return nil, err
	}
	if want := branchOf(s); branch != want {
		return nil, fmt.Errorf("source %s is on %s: only the branch of the change (%s) is pushed", p, branch, want)
	}
	if _, err := git(ctx, s, dir, true, "push", "--quiet", "--set-upstream", "origin", branch); err != nil {
		return nil, err
	}
	head, _ := git(ctx, s, dir, false, "rev-parse", "HEAD")
	remote, _ := git(ctx, s, dir, false, "remote", "get-url", "origin")
	return map[string]any{"path": p, "branch": branch, "head": head, "remote": remote}, nil
}
