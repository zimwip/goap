package enginee2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/zimwip/goap/internal/connectors/workspace"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/mcp"
	"github.com/zimwip/goap/pkg/mcpbuiltin"
	"github.com/zimwip/goap/pkg/methodology"
)

// workspaceHub binds the workspace MCP (and goap-graph, which the fake model never calls) for every unit and runs
// its tools on the connector of the local machine, with a temporary root.
type workspaceHub struct {
	conn  workspace.Connector
	root  string
	mu    sync.Mutex
	calls []string
}

func (h *workspaceHub) CallTool(ctx context.Context, _, name string, args map[string]any) (any, error) {
	m, tool, _ := strings.Cut(name, "/")
	h.mu.Lock()
	h.calls = append(h.calls, name)
	h.mu.Unlock()
	if m != mcpbuiltin.Workspace {
		return nil, fmt.Errorf("unexpected tool %s", name)
	}
	if mcp.CallFrom(ctx).Change == "" {
		return nil, fmt.Errorf("%s runs for no change", name)
	}
	return h.conn.Invoke(ctx, tool, args, map[string]any{"root": h.root, "gitName": "goap", "gitEmail": "goap@example.org"}, nil)
}

func (h *workspaceHub) Tools(context.Context, string) ([]mcp.ToolInfo, []string, error) {
	var out []mcp.ToolInfo
	def := mcpbuiltin.WorkspaceDef()
	for _, t := range def.Tools {
		out = append(out, mcp.ToolInfo{Name: mcp.ToolName(def.Name, t.Name), Description: t.Description, InputSchema: t.InputSchema, ReadOnly: t.ReadOnly})
	}
	return out, []string{mcpbuiltin.Workspace, "goap-graph"}, nil
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.org", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.org")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestSDLCBuildsFromTheSources runs the delivery on a component the design gives a repository (ADR 0118): the
// workspace_build method opens the workspace of the change with its sources, the developer agent implements the
// component there with the workspace tools, its build is checked (a failure sends it back to the agent), and the
// branch of the change is pushed once it builds; the other components are recorded by their technology.
func TestSDLCBuildsFromTheSources(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, src, "init", "--quiet", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(src, "README"), []byte("installments engine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, src, "add", "README")
	runGit(t, src, "commit", "--quiet", "-m", "start")
	origin := filepath.Join(tmp, "origin.git")
	runGit(t, tmp, "clone", "--quiet", "--bare", src, origin)

	// the design creates CMP-10 with its sources; the agent implements it in two rounds: the first does not build
	base := sdlcModel(t)
	var mu sync.Mutex
	attempts := 0
	model := llm.ClientFunc(func(ctx context.Context, req llm.Request) (llm.Response, error) {
		p := req.Messages[0].Content
		switch {
		case strings.Contains(p, "Design the evolution"):
			return llm.Response{Text: `{"items":[{"type":"changeImpact","changeImpact":{"op":"declare","ref":"#c1","intent":"created","type":"Component","key":"CMP-10","rationale":"a dedicated engine"}},
			{"type":"changeImpact","changeImpact":{"op":"write","node":"#c1","props":{"title":"installments-engine","technology":"java","version":"0.0.0",
			  "repository":"file://` + filepath.ToSlash(origin) + `","buildCommand":"test -f feature.txt"},"links":[{"type":"implements","to":"FCT-1"}]}},
			{"type":"artifact","target":"design","data":{"summary":"dedicated installment scheduling engine","decisions":["new Java component"]}}]}`}, nil
		case strings.Contains(p, "You implement ONE component"):
			last := req.Messages[len(req.Messages)-1]
			if len(last.ToolResults) > 0 {
				return llm.Response{Text: `{"items":[{"type":"artifact","target":"code_change","data":{"component":"CMP-10","summary":"engine","files":["feature.txt"],"tests":"test -f"}}]}`}, nil
			}
			mu.Lock()
			attempts++
			n := attempts
			mu.Unlock()
			file := "draft.txt" // the first attempt misses what the build checks
			if n > 1 {
				if !strings.Contains(p, "FAILED") {
					t.Errorf("the second attempt is not told the build failed:\n%s", p)
				}
				file = "feature.txt"
			}
			return llm.Response{StopReason: llm.StopToolUse, ToolCalls: []llm.ToolCall{
				{ID: "w", Name: "workspace/write", Arguments: map[string]any{"path": "CMP-10/" + file, "content": "installments\n"}},
				{ID: "c", Name: "workspace/bash", Arguments: map[string]any{"command": "git add -A && git commit -qm 'installments'", "cwd": "CMP-10"}},
			}}, nil
		}
		return base.Complete(ctx, req)
	})
	e, g, baseline := sdlcSetup(t)
	hub := &workspaceHub{conn: workspace.New(), root: filepath.Join(tmp, "workspaces")}
	e.Executors[methodology.KindLLM] = engine.LLMExecutor{Client: model}
	e.Scope = engine.AuthzScope{Authz: e.Scope.(engine.AuthzScope).Authz, Hub: hub}

	ctx := devCtx
	p, err := e.Start(ctx, engine.StartRequest{Methodology: "sdlc", Agent: "delivery", Goal: "deliver", BaselineID: baseline,
		Title: "Payment in 3 installments", Intent: "Allow payment in 3 installments with no fees", ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	if p, err = e.Run(context.Background(), p.ID); err != nil || p.Pending == nil || p.Pending.Action != "validate_impact_analysis" {
		t.Fatalf("impact analysis: %v %s %s %+v", err, p.Status, p.Error, p.Pending)
	}
	if _, err = e.Submit(ctx, p.ID, []engine.ItemInput{validation}); err != nil {
		t.Fatal(err)
	}
	if p, err = e.Run(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, s := range p.Steps {
		if len(actions) == 0 || actions[len(actions)-1] != s.Action {
			actions = append(actions, s.Action)
		}
	}
	if p.Status != engine.StatusWaiting || p.Pending.Action != "review" {
		t.Fatalf("must wait for the review: %s %s %v %+v", p.Status, p.Error, actions, p.Pending)
	}
	// the build goes back to the agent once, then publishes; the recorded build follows for the others (other
	// steps may come in between: only the order of these matters)
	var build []string
	for _, a := range actions {
		if slices.Contains([]string{"prepare_workspace", "implement_component", "build_component", "publish_build", "build"}, a) {
			build = append(build, a)
		}
	}
	if want := []string{"prepare_workspace", "implement_component", "build_component", "implement_component", "build_component", "publish_build", "build"}; !slices.Equal(build, want) {
		t.Fatalf("build steps %v (steps %v)", build, actions)
	}
	if attempts != 2 {
		t.Fatalf("%d attempts", attempts)
	}

	c, _ := g.Change(ctx, p.ChangeID)
	reports, builds, artifacts := 0, 0, 0
	for _, it := range c.Items {
		switch it.Target {
		case "build_report":
			reports++
			if ok := it.Data["ok"] == true; ok != (reports == 2) {
				t.Fatalf("build report %d: %+v", reports, it.Data)
			}
		case "build":
			builds++
		}
	}
	var art domain.ChangeImpact
	for _, n := range c.Nodes {
		if n.Type == "alm@BuildArtifact" && n.Intent == domain.IntentCreated {
			artifacts++
			if strings.HasPrefix(n.Key, "ART-CMP-10-") {
				art = n
			}
		}
	}
	// CMP-10 from its sources; CMP-1 (java), CMP-4 (c), CMP-2 (go, generic) by their technology
	if reports != 2 || builds != 4 || artifacts != 4 || art.Key == "" {
		t.Fatalf("reports %d, builds %d, artifacts %d (%q)", reports, builds, artifacts, art.Key)
	}
	// the branch of the change reached the remote with the work of the agent, and nothing else did
	branch := "goap/" + string(p.ChangeID)
	if files := runGit(t, origin, "ls-tree", "--name-only", branch); files != "README\ndraft.txt\nfeature.txt" {
		t.Fatalf("files on %s: %q", branch, files)
	}
	if heads := runGit(t, origin, "for-each-ref", "--format=%(refname:short)", "refs/heads"); heads != branch+"\nmain" && heads != "main\n"+branch {
		t.Fatalf("branches of the remote: %q", heads)
	}
	if !slices.Contains(hub.calls, "workspace/push") {
		t.Fatalf("calls %v", hub.calls)
	}
}
