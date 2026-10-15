package claudeagent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zimwip/goap/contracts/mcp"
	"github.com/zimwip/goap/mcp/connectors/workspace"
)

// stubSDK stands for the Claude Agent SDK: query() plays a session according to its prompt, calling canUseTool the
// way the SDK does, so the runner's protocol is tested without a model.
const stubSDK = `
export async function* query({ prompt, options }) {
  const sid = options.resume || 'sess-1';
  yield { type: 'system', subtype: 'init', session_id: sid };
  const result = (subtype, text) => ({ type: 'result', subtype, is_error: subtype !== 'success', result: text,
    num_turns: 3, total_cost_usd: 0.25, usage: { input_tokens: 10, output_tokens: 5 }, session_id: sid });
  if (prompt.startsWith('ask')) {
    await options.canUseTool('AskUserQuestion', { questions: [{ question: 'Which library?', header: 'Library',
      options: [{ label: 'a', description: '' }, { label: 'b', description: '' }], multiSelect: false }] }, {});
    yield result('error_during_execution', '');
    return;
  }
  if (prompt.startsWith('bash')) {
    const d = await options.canUseTool('Bash', { command: 'npm publish' }, {});
    if (d.behavior === 'deny' && d.interrupt) throw new Error('interrupted');
    return;
  }
  if (prompt.startsWith('turns')) { yield result('error_max_turns', ''); return; }
  if (prompt.startsWith('sleep')) {
    await new Promise((resolve, reject) => {
      const t = setTimeout(resolve, 30000);
      options.abortController.signal.addEventListener('abort', () => { clearTimeout(t); reject(new Error('aborted')); });
    });
    return;
  }
  yield { type: 'assistant', session_id: sid, message: { content: [{ type: 'tool_use', name: 'Edit' }, { type: 'text' }] } };
  yield result('success', JSON.stringify({ prompt, cwd: options.cwd, resume: options.resume ?? '',
    key: process.env.ANTHROPIC_API_KEY ?? '', base: process.env.ANTHROPIC_BASE_URL ?? '', secretLeak: process.env.GOAP_TEST_SECRET ?? '',
    allowed: options.allowedTools ?? [], mode: options.permissionMode, settings: options.settingSources,
    maxTurns: options.maxTurns, append: options.systemPrompt.append.length > 0 }));
}
`

type fixture struct {
	c    Connector
	root string
	ws   string
	ctx  context.Context
}

func setup(t *testing.T) fixture {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	dir := t.TempDir()
	stub := filepath.Join(t.TempDir(), "sdk.mjs")
	if err := os.WriteFile(stub, []byte(stubSDK), 0o644); err != nil {
		t.Fatal(err)
	}
	c := New(filepath.Join(dir, "runner"))
	c.Node, c.sdkModule = node, "file://"+filepath.ToSlash(stub)
	root := t.TempDir()
	ws := filepath.Join(root, workspace.DirName("CH-1"))
	if err := os.MkdirAll(filepath.Join(ws, "svc"), 0o755); err != nil {
		t.Fatal(err)
	}
	return fixture{c: c, root: root, ws: ws, ctx: mcp.WithCall(context.Background(), mcp.CallContext{Change: "CH-1"})}
}

func (f fixture) config() map[string]any {
	return map[string]any{"root": f.root, "baseURL": "http://gateway/anthropic", "allowedTools": []any{"Read"}}
}

func (f fixture) call(t *testing.T, op string, args map[string]any) map[string]any {
	t.Helper()
	out, err := f.c.Invoke(f.ctx, op, args, f.config(), map[string]string{SecretAPIKey: "sk-test"})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func played(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	var p map[string]any
	if err := json.Unmarshal([]byte(out["summary"].(string)), &p); err != nil {
		t.Fatalf("summary %v: %v", out["summary"], err)
	}
	return p
}

func TestRunDone(t *testing.T) {
	t.Setenv("GOAP_TEST_SECRET", "leak")
	f := setup(t)
	out := f.call(t, OpRun, map[string]any{"task": "implement", "path": "svc", "maxTurns": float64(12)})
	if out["status"] != StatusDone || out["sessionId"] != "sess-1" || out["turns"] != float64(3) || out["costUsd"] != 0.25 {
		t.Fatalf("outcome %v", out)
	}
	if tools, _ := out["tools"].(map[string]any); tools["Edit"] != float64(1) {
		t.Fatalf("tools %v", out["tools"])
	}
	p := played(t, out)
	if p["cwd"] != filepath.Join(f.ws, "svc") || p["key"] != "sk-test" || p["base"] != "http://gateway/anthropic" {
		t.Fatalf("session %v", p)
	}
	if p["secretLeak"] != "" {
		t.Fatal("a host variable not passed reached the session")
	}
	if p["mode"] != "acceptEdits" || p["maxTurns"] != float64(12) || p["append"] != true || strings.Join(toStrings(p["settings"]), ",") != "project" {
		t.Fatalf("options %v", p)
	}
	if st := f.call(t, OpStatus, nil); st["running"] != false || st["last"].(map[string]any)["status"] != StatusDone {
		t.Fatalf("status %v", st)
	}
}

func TestQuestionsAndApprovalsEndTheTurn(t *testing.T) {
	f := setup(t)
	out := f.call(t, OpRun, map[string]any{"task": "ask first"})
	qs, _ := out["questions"].([]any)
	if out["status"] != StatusNeedsInput || len(qs) != 1 || qs[0].(map[string]any)["question"] != "Which library?" {
		t.Fatalf("question %v", out)
	}
	out = f.call(t, OpRun, map[string]any{"task": "bash it"})
	perm, _ := out["permission"].(map[string]any)
	if out["status"] != StatusNeedsApproval || perm["tool"] != "Bash" {
		t.Fatalf("approval %v", out)
	}
	if out = f.call(t, OpRun, map[string]any{"task": "turns"}); out["status"] != StatusBudget {
		t.Fatalf("budget %v", out)
	}
}

func TestAnswerResumesTheSession(t *testing.T) {
	f := setup(t)
	out := f.call(t, OpAnswer, map[string]any{"sessionId": "sess-9", "answers": map[string]any{"Which library?": "b"},
		"allow": []any{"Bash(npm test:*)"}, "answer": "keep the API"})
	p := played(t, out)
	prompt, _ := p["prompt"].(string)
	if out["sessionId"] != "sess-9" || p["resume"] != "sess-9" || !strings.Contains(prompt, "Which library? → b") || !strings.Contains(prompt, "keep the API") {
		t.Fatalf("resume %v", p)
	}
	if strings.Join(toStrings(p["allowed"]), ",") != "Read,Bash(npm test:*)" {
		t.Fatalf("allowed %v", p["allowed"])
	}
	if _, err := f.c.Invoke(f.ctx, OpAnswer, map[string]any{"sessionId": "sess-9"}, f.config(), nil); err == nil {
		t.Fatal("an answer with nothing to say")
	}
}

func TestOneSessionPerChangeAndCancel(t *testing.T) {
	f := setup(t)
	done := make(chan map[string]any)
	go func() {
		out, _ := f.c.Invoke(f.ctx, OpRun, map[string]any{"task": "sleep"}, f.config(), nil)
		done <- out
	}()
	deadline := time.Now().Add(10 * time.Second)
	for f.call(t, OpStatus, nil)["running"] != true {
		if time.Now().After(deadline) {
			t.Fatal("the session never ran")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := f.c.Invoke(f.ctx, OpRun, map[string]any{"task": "implement"}, f.config(), nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("second session: %v", err)
	}
	if c := f.call(t, OpCancel, nil); c["cancelled"] != true {
		t.Fatalf("cancel %v", c)
	}
	select {
	case out := <-done:
		if out["status"] != StatusCancelled {
			t.Fatalf("cancelled run %v", out)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the cancelled session did not stop")
	}
}

func TestRefusals(t *testing.T) {
	f := setup(t)
	if _, err := f.c.Invoke(context.Background(), OpRun, map[string]any{"task": "x"}, f.config(), nil); !errors.Is(err, ErrNoChange) {
		t.Fatalf("no change: %v", err)
	}
	other := mcp.WithCall(context.Background(), mcp.CallContext{Change: "CH-2"})
	if _, err := f.c.Invoke(other, OpRun, map[string]any{"task": "x"}, f.config(), nil); err == nil || !strings.Contains(err.Error(), "workspace/open") {
		t.Fatalf("no workspace: %v", err)
	}
	if _, err := f.c.Invoke(f.ctx, OpRun, map[string]any{"task": "x", "path": "../.."}, f.config(), nil); err != nil {
		t.Fatalf("a path is confined to the workspace: %v", err)
	}
	restricted := New(t.TempDir(), "/somewhere/else")
	if _, err := restricted.Invoke(f.ctx, OpRun, map[string]any{"task": "x"}, f.config(), nil); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("root outside the allowed roots: %v", err)
	}
	real := New(t.TempDir())
	if _, err := real.Invoke(f.ctx, OpRun, map[string]any{"task": "x"}, f.config(), nil); !errors.Is(err, ErrNoRunner) {
		t.Fatalf("SDK not installed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(real.Dir, "runner.mjs")); err != nil {
		t.Fatal("the runner is written before the SDK is checked")
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}
