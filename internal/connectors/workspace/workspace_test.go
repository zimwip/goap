package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zimwip/goap/pkg/mcp"
	"github.com/zimwip/goap/pkg/mcpbuiltin"
)

type fixture struct {
	t      *testing.T
	c      Connector
	ctx    context.Context
	config map[string]any
	dir    string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	return &fixture{t: t, c: New(), ctx: mcp.WithCall(context.Background(), mcp.CallContext{Unit: "ORG-DEFAULT", Change: "CHG-1"}),
		config: map[string]any{"root": root, "gitName": "GOAP", "gitEmail": "goap@example.com"}, dir: filepath.Join(root, "CHG-1")}
}

func (f *fixture) call(op string, args map[string]any) (map[string]any, error) {
	return f.c.Invoke(f.ctx, op, args, f.config, map[string]string{SecretGitToken: "s3cr3t"})
}

func (f *fixture) must(op string, args map[string]any) map[string]any {
	f.t.Helper()
	out, err := f.call(op, args)
	if err != nil {
		f.t.Fatalf("%s %v: %v", op, args, err)
	}
	return out
}

// The connector offers exactly the tools of the workspace MCP, so that its pass-through adapter reaches each.
func TestOperationsAreTheToolsOfTheMCP(t *testing.T) {
	var ops, tools []string
	for _, o := range New().Info().Operations {
		ops = append(ops, o.Name)
	}
	for _, tl := range mcpbuiltin.WorkspaceDef().Tools {
		tools = append(tools, tl.Name)
	}
	slices.Sort(ops)
	slices.Sort(tools)
	if !slices.Equal(ops, tools) {
		t.Fatalf("operations %v, tools %v", ops, tools)
	}
	if New().Info().Id != mcpbuiltin.WorkspaceLocal {
		t.Fatal("the adapter definition names another connector")
	}
}

func TestNeedsAChange(t *testing.T) {
	f := newFixture(t)
	f.ctx = context.Background()
	if _, err := f.call(OpLs, nil); !errors.Is(err, ErrNoChange) {
		t.Fatalf("got %v", err)
	}
}

// One workspace per change: two changes never see each other's files.
func TestOneWorkspacePerChange(t *testing.T) {
	f := newFixture(t)
	f.must(OpWrite, map[string]any{"path": "a.txt", "content": "one"})
	other := *f
	other.ctx = mcp.WithCall(context.Background(), mcp.CallContext{Change: "CHG-2"})
	if _, err := other.call(OpRead, map[string]any{"path": "a.txt"}); err == nil {
		t.Fatal("CHG-2 reads the workspace of CHG-1")
	}
	if _, err := os.Stat(filepath.Join(f.dir, "a.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestFiles(t *testing.T) {
	f := newFixture(t)
	out := f.must(OpWrite, map[string]any{"path": "/src/pkg/main.go", "content": "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"})
	if out["created"] != true {
		t.Fatalf("write: %v", out)
	}
	f.must(OpWrite, map[string]any{"path": "src/README.md", "content": "hello world\nHELLO again\n"})

	out = f.must(OpRead, map[string]any{"path": "src/pkg/main.go", "offset": float64(3), "limit": float64(2)})
	if out["text"] != "     3\tfunc main() {\n     4\t\tprintln(\"hello\")\n" || out["lines"] != float64(5) || out["truncated"] != true {
		t.Fatalf("read: %#v", out)
	}

	if _, err := f.call(OpEdit, map[string]any{"path": "src/README.md", "old": "missing", "new": "x"}); err == nil {
		t.Fatal("edit of a missing string")
	}
	f.must(OpWrite, map[string]any{"path": "dup.txt", "content": "a a"})
	if _, err := f.call(OpEdit, map[string]any{"path": "dup.txt", "old": "a", "new": "b"}); err == nil || !strings.Contains(err.Error(), "2 times") {
		t.Fatalf("ambiguous edit: %v", err)
	}
	if out := f.must(OpEdit, map[string]any{"path": "dup.txt", "old": "a", "new": "b", "all": true}); out["replacements"] != float64(2) {
		t.Fatalf("edit all: %v", out)
	}
	f.must(OpEdit, map[string]any{"path": "src/pkg/main.go", "old": `"hello"`, "new": `"bye"`})
	if b, _ := os.ReadFile(filepath.Join(f.dir, "src/pkg/main.go")); !strings.Contains(string(b), `"bye"`) {
		t.Fatalf("edited: %s", b)
	}

	out = f.must(OpLs, map[string]any{"path": "src"})
	if len(out["entries"].([]any)) != 2 {
		t.Fatalf("ls: %v", out)
	}

	out = f.must(OpGlob, map[string]any{"pattern": "**/*.go"})
	if files := out["files"].([]any); len(files) != 1 || files[0] != "src/pkg/main.go" {
		t.Fatalf("glob: %v", out)
	}
	out = f.must(OpGlob, map[string]any{"pattern": "*.md", "path": "src"})
	if files := out["files"].([]any); len(files) != 1 || files[0] != "src/README.md" {
		t.Fatalf("glob under a path: %v", out)
	}

	out = f.must(OpGrep, map[string]any{"pattern": "hello", "ignoreCase": true})
	if r := out["results"].([]any); len(r) != 1 || r[0] != "src/README.md" {
		t.Fatalf("grep files: %v", out)
	}
	out = f.must(OpGrep, map[string]any{"pattern": "hello", "ignoreCase": true, "mode": "content"})
	if r := out["results"].([]any); len(r) != 1 || r[0].(map[string]any)["lines"] != "1:hello world\n2:HELLO again\n" {
		t.Fatalf("grep content: %v", out)
	}
	out = f.must(OpGrep, map[string]any{"pattern": "func", "glob": "*.go", "mode": "count"})
	if r := out["results"].([]any); len(r) != 1 || r[0].(map[string]any)["count"] != float64(1) {
		t.Fatalf("grep count: %v", out)
	}
}

// A path never leaves the workspace: ".." stops at its top, and a link out of it is refused.
func TestConfined(t *testing.T) {
	f := newFixture(t)
	outside := filepath.Join(filepath.Dir(f.dir), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.must(OpWrite, map[string]any{"path": "../secret.txt", "content": "inside"})
	if b, _ := os.ReadFile(outside); string(b) != "secret" {
		t.Fatal("a write left the workspace")
	}
	if runtime.GOOS == "windows" {
		return
	}
	if err := os.Symlink(outside, filepath.Join(f.dir, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.call(OpRead, map[string]any{"path": "link"}); err == nil {
		t.Fatal("read through a link out of the workspace")
	}
}

func TestBash(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	f := newFixture(t)
	t.Setenv("GOAP_TEST_HOST_ONLY", "leak")
	f.config["env"] = []any{"GREETING=hi"}
	f.must(OpWrite, map[string]any{"path": "sub/x.txt", "content": "x"})
	out := f.must(OpBash, map[string]any{"command": "pwd; ls; echo $GREETING; echo ${GOAP_TEST_HOST_ONLY:-none}; echo oops >&2; exit 3", "cwd": "sub"})
	lines := strings.Split(strings.TrimSpace(out["stdout"].(string)), "\n")
	if out["exitCode"] != float64(3) || len(lines) != 4 || !strings.HasSuffix(lines[0], filepath.Join("CHG-1", "sub")) ||
		lines[1] != "x.txt" || lines[2] != "hi" || lines[3] != "none" || out["stderr"] != "oops\n" {
		t.Fatalf("bash: %#v", out)
	}
	// the secret of the adapter never reaches a command
	if out := f.must(OpBash, map[string]any{"command": "env"}); strings.Contains(out["stdout"].(string), "s3cr3t") {
		t.Fatal("the git token reached a command")
	}

	out = f.must(OpBash, map[string]any{"command": "sleep 5", "timeout": float64(1)})
	if out["timedOut"] != true {
		t.Fatalf("timeout: %v", out)
	}

	out = f.must(OpBash, map[string]any{"command": "echo started; sleep 0.3; echo finished", "background": true})
	id := out["id"].(string)
	deadline := time.Now().Add(10 * time.Second)
	var text string
	for {
		out = f.must(OpBashOutput, map[string]any{"id": id})
		text += out["output"].(string)
		if out["running"] == false || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if text != "started\nfinished\n" || out["exitCode"] != float64(0) {
		t.Fatalf("background: %q %v", text, out)
	}

	out = f.must(OpBash, map[string]any{"command": "sleep 30", "background": true})
	f.must(OpBashKill, map[string]any{"id": out["id"]})
	status := f.must(OpStatus, nil)
	if len(status["commands"].([]any)) != 2 {
		t.Fatalf("status: %v", status)
	}
	closed := f.must(OpClose, nil)
	if closed["closed"] != true {
		t.Fatalf("close: %v", closed)
	}
	if _, err := os.Stat(f.dir); !os.IsNotExist(err) {
		t.Fatal("the directory of the workspace is still there")
	}
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestSources(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	f := newFixture(t)
	// a remote: a bare repository holding one commit on main
	remote := filepath.Join(t.TempDir(), "app.git")
	run(t, filepath.Dir(remote), "init", "--quiet", "--bare", "--initial-branch=main", remote)
	seed := t.TempDir()
	run(t, seed, "clone", "--quiet", remote, ".")
	if err := os.WriteFile(filepath.Join(seed, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "add", ".")
	run(t, seed, "commit", "--quiet", "-m", "first")
	run(t, seed, "push", "--quiet", "origin", "HEAD:main")

	out := f.must(OpOpen, map[string]any{"sources": []any{map[string]any{"repo": remote, "ref": "main"}}})
	src := out["sources"].([]any)
	if len(src) != 1 || src[0].(map[string]any)["path"] != "app" || src[0].(map[string]any)["branch"] != "goap/CHG-1" {
		t.Fatalf("open: %v", out)
	}
	// opening again keeps the work
	f.must(OpWrite, map[string]any{"path": "app/main.go", "content": "package main\n\nfunc main() {}\n"})
	f.must(OpOpen, map[string]any{"sources": []any{map[string]any{"repo": remote}}})
	f.must(OpBash, map[string]any{"command": "git commit --quiet -am 'implement' && echo new > added.txt", "cwd": "app"})

	out = f.must(OpDiff, nil)
	d := out["sources"].([]any)[0].(map[string]any)
	if !strings.Contains(d["diff"].(string), "+func main() {}") || !strings.Contains(d["status"].(string), "?? added.txt") {
		t.Fatalf("diff: %v", out)
	}

	f.must(OpPush, map[string]any{"path": "app"})
	if got := run(t, seed, "ls-remote", "origin", "goap/CHG-1"); !strings.Contains(got, "refs/heads/goap/CHG-1") {
		t.Fatalf("pushed: %q", got)
	}
	f.must(OpBash, map[string]any{"command": "git checkout --quiet -b other", "cwd": "app"})
	if _, err := f.call(OpPush, map[string]any{"path": "app"}); err == nil {
		t.Fatal("pushed a branch that is not the change's")
	}
}

func TestMatch(t *testing.T) {
	for _, c := range []struct {
		pattern, name string
		want          bool
	}{
		{"**/*.go", "main.go", true}, {"**/*.go", "a/b/main.go", true}, {"*.go", "a/main.go", false},
		{"src/**", "src/a/b", true}, {"src/**/x", "src/x", true}, {"a/*/c", "a/b/c", true}, {"a/*/c", "a/b/d/c", false},
	} {
		if got := match(c.pattern, c.name); got != c.want {
			t.Errorf("match(%q, %q) = %v", c.pattern, c.name, got)
		}
	}
}

func TestCapOutput(t *testing.T) {
	long := strings.Repeat("a", MaxOutput) + strings.Repeat("z", 100)
	got := capOutput([]byte(long))
	if len(got) > MaxOutput+100 || !strings.HasSuffix(got, strings.Repeat("z", 100)) || !strings.Contains(got, "bytes cut") {
		t.Fatalf("cap: %d bytes", len(got))
	}
}
