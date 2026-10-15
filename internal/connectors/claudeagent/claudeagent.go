// Package claudeagent is the connector of the claude-coding-agent MCP (ADR 0123): a Claude Code session, run by the
// Claude Agent SDK in the workspace of the change, that a methodology hands a coding task to and resumes.
//
// The SDK exists in TypeScript, not in Go: each call starts the runner (runner/runner.mjs, embedded and written into
// the connector's directory next to its package.json, the SDK installed there by `npm install`), gives it one JSON
// request on its standard input and reads JSON lines from its output, the last one the outcome of the turn. The
// session works in the directory of the workspace of the change (the workspace connector's, under the same root),
// with a cleaned environment, the unit's API key and the repository's Claude Code settings; it runs with the rights
// of the connector's host, like the local workspace commands.
package claudeagent

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	connectorv1 "github.com/zimwip/goap/gen/goap/connector/v1"
	"github.com/zimwip/goap/internal/connectorkit"
	"github.com/zimwip/goap/internal/connectors/workspace"
	"github.com/zimwip/goap/pkg/mcp"
)

// ID is the identifier of the connector (and of the MCP and the adapter definition, mcpbuiltin.ClaudeAgent).
const ID = "claude-coding-agent"

// SecretAPIKey is the Anthropic API key the sessions call the model with.
const SecretAPIKey = "api_key"

// SDKPackage is the package the runner needs in its directory.
const SDKPackage = "@anthropic-ai/claude-agent-sdk"

// Bounds of a run.
const (
	DefaultMaxTurns = 200
	DefaultTimeout  = 60 * time.Minute
	MaxTimeout      = 240 * time.Minute
	// MaxSummary bounds the summary given back (the head and the tail are kept).
	MaxSummary = 20000
	maxLine    = 8 << 20
)

// Statuses of a run.
const (
	StatusDone          = "done"
	StatusNeedsInput    = "needs_input"
	StatusNeedsApproval = "needs_approval"
	StatusBudget        = "budget"
	StatusFailed        = "failed"
	StatusCancelled     = "cancelled"
)

// Operations of the connector: the tools of the MCP, one for one.
const (
	OpRun    = "run"
	OpAnswer = "answer"
	OpStatus = "status"
	OpCancel = "cancel"
)

var (
	// ErrNoChange is returned for a call that runs for no change: a session works in the workspace of a change.
	ErrNoChange = errors.New("the Claude coding agent works for a change: this call runs for none")
	// ErrBusy is returned when a session already runs for the change.
	ErrBusy = errors.New("a Claude Code session already runs for this change: wait for it or cancel it")
	// ErrNoRunner is returned when the SDK is not installed in the runner directory.
	ErrNoRunner = errors.New("the Claude Agent SDK is not installed for this connector")
)

//go:embed runner/runner.mjs
var runnerScript []byte

//go:embed runner/package.json
var runnerPackage []byte

// Connector implements connectorkit.Connector. Use New: it keeps the sessions running per change.
type Connector struct {
	// Dir is the runner directory: runner.mjs and package.json are written there, the SDK installed there.
	Dir string
	// Node is the JavaScript runtime (default "node").
	Node string
	// Roots, when not empty, restricts the "root" a unit may configure to these directories or below.
	Roots []string
	// sdkModule replaces the SDK in the runner (tests)
	sdkModule string
	st        *state
}

var _ connectorkit.Connector = Connector{}

// New returns a connector whose runner lives in dir, restricted to roots (none: any root).
func New(dir string, roots ...string) Connector {
	return Connector{Dir: dir, Node: "node", Roots: roots, st: &state{running: map[string]*session{}, last: map[string]map[string]any{}}}
}

// Install writes the runner into dir (runner.mjs, package.json): `npm install` there then installs the SDK.
func Install(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, data := range map[string][]byte{"runner.mjs": runnerScript, "package.json": runnerPackage} {
		p := filepath.Join(dir, name)
		if cur, err := os.ReadFile(p); err == nil && bytes.Equal(cur, data) {
			continue
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// state is what the connector keeps per change: the session running and the outcome of the latest run.
type state struct {
	mu      sync.Mutex
	running map[string]*session
	last    map[string]map[string]any
	install sync.Once
	instErr error
}

type session struct {
	id      string
	started time.Time
	cancel  context.CancelFunc
}

func schema(v map[string]any) *structpb.Struct {
	s, err := structpb.NewStruct(v)
	if err != nil {
		panic(err)
	}
	return s
}

func obj(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		r := make([]any, len(required))
		for i, n := range required {
			r[i] = n
		}
		s["required"] = r
	}
	return s
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
func num(desc string) map[string]any { return map[string]any{"type": "number", "description": desc} }
func strs(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
}

// Info implements connectorkit.Connector.
func (c Connector) Info() *connectorv1.ConnectorInfo {
	op := func(name, desc string, input map[string]any) *connectorv1.Operation {
		return &connectorv1.Operation{Name: name, Description: desc, InputSchema: schema(input)}
	}
	return &connectorv1.ConnectorInfo{
		Id: ID, Version: "1", Description: "Claude Code sessions (Claude Agent SDK) in the workspaces of the changes, on this host.",
		ConfigSchema: schema(map[string]any{"type": "object", "required": []any{"root"}, "properties": map[string]any{
			"root":            str("directory holding one workspace per change"),
			"model":           str("model of the sessions"),
			"permissionMode":  str("permission mode of the sessions (default acceptEdits)"),
			"allowedTools":    strs("tools allowed without asking"),
			"disallowedTools": strs("tools removed from the sessions"),
			"maxTurns":        num("turns of a run (default 200)"),
			"maxBudgetUsd":    num("budget of a run in USD"),
			"timeoutMinutes":  num("time of a run in minutes (default 60, at most 240)"),
			"settingSources":  strs("Claude Code settings loaded (default project)"),
			"baseURL":         str("Anthropic API endpoint (ANTHROPIC_BASE_URL)"),
			"env":             strs("variables set for the sessions (NAME=value)"),
			"passEnv":         strs("variables of the host environment given to the sessions"),
		}}),
		SecretNames: []string{SecretAPIKey},
		Operations: []*connectorv1.Operation{
			op(OpRun, "Run a coding task with Claude Code in the workspace of the change", obj(map[string]any{
				"task": str("instructions of this turn"), "sessionId": str("session to resume"), "path": str("directory of the workspace to work in"),
				"model": str("model of the session"), "maxTurns": num("turns of this run"), "maxBudgetUsd": num("budget of this run in USD")}, "task")),
			op(OpAnswer, "Resume a session waiting for a person with their decision", obj(map[string]any{
				"sessionId": str("session to resume"), "answer": str("free answer or instruction"),
				"answers": map[string]any{"type": "object", "description": "answers by question", "additionalProperties": map[string]any{"type": "string"}},
				"allow":   strs("tools allowed for this run"), "maxTurns": num("turns of this run")}, "sessionId")),
			op(OpStatus, "The session of the change running now and the outcome of the latest one", obj(nil)),
			op(OpCancel, "Stop the session of the change running now", obj(nil)),
		},
	}
}

// Invoke implements connectorkit.Connector.
func (c Connector) Invoke(ctx context.Context, op string, args, config map[string]any, secrets map[string]string) (map[string]any, error) {
	if c.st == nil {
		return nil, errors.New("claude-coding-agent connector not initialised (use claudeagent.New)")
	}
	change := mcp.CallFrom(ctx).Change
	if change == "" {
		return nil, ErrNoChange
	}
	switch op {
	case OpStatus:
		return c.status(change), nil
	case OpCancel:
		return c.cancel(change), nil
	case OpRun, OpAnswer:
		return c.run(ctx, op, change, args, config, secrets)
	}
	return nil, fmt.Errorf("%s: %w", op, connectorkit.ErrUnknownOperation)
}

func (c Connector) status(change string) map[string]any {
	c.st.mu.Lock()
	defer c.st.mu.Unlock()
	out := map[string]any{"running": false}
	if s := c.st.running[change]; s != nil {
		out["running"], out["sessionId"], out["startedAt"] = true, s.id, s.started.UTC().Format(time.RFC3339)
	}
	if l := c.st.last[change]; l != nil {
		out["last"] = l
	}
	return out
}

func (c Connector) cancel(change string) map[string]any {
	c.st.mu.Lock()
	s := c.st.running[change]
	var id string
	if s != nil {
		id = s.id
	}
	c.st.mu.Unlock()
	if s == nil {
		return map[string]any{"cancelled": false}
	}
	s.cancel()
	return map[string]any{"cancelled": true, "sessionId": id}
}

// request is what the runner reads on its standard input.
type request struct {
	Prompt          string   `json:"prompt"`
	Cwd             string   `json:"cwd"`
	Resume          string   `json:"resume,omitempty"`
	Model           string   `json:"model,omitempty"`
	MaxTurns        int      `json:"maxTurns,omitempty"`
	MaxBudgetUsd    float64  `json:"maxBudgetUsd,omitempty"`
	PermissionMode  string   `json:"permissionMode,omitempty"`
	AllowedTools    []string `json:"allowedTools,omitempty"`
	DisallowedTools []string `json:"disallowedTools,omitempty"`
	SettingSources  []string `json:"settingSources"`
}

func (c Connector) run(ctx context.Context, op, change string, args, config map[string]any, secrets map[string]string) (map[string]any, error) {
	dir, err := c.workspace(change, config, stringArg(args, "path"))
	if err != nil {
		return nil, err
	}
	req := request{Cwd: dir, Resume: stringArg(args, "sessionId"), Model: firstString(args, config, "model"),
		MaxTurns: int(firstNumber(args, config, "maxTurns", DefaultMaxTurns)), MaxBudgetUsd: firstNumber(args, config, "maxBudgetUsd", 0),
		PermissionMode: stringArg(config, "permissionMode"), AllowedTools: strings1(config, "allowedTools"),
		DisallowedTools: strings1(config, "disallowedTools"), SettingSources: strings1(config, "settingSources")}
	if req.SettingSources == nil {
		req.SettingSources = []string{"project"}
	}
	if op == OpRun {
		req.Prompt = stringArg(args, "task")
		if strings.TrimSpace(req.Prompt) == "" {
			return nil, errors.New("run: task is empty")
		}
	} else {
		if req.Resume == "" {
			return nil, errors.New("answer: sessionId is required")
		}
		req.Prompt = answerPrompt(args)
		if req.Prompt == "" {
			return nil, errors.New("answer: give an answer, answers or allowed tools")
		}
		req.AllowedTools = append(req.AllowedTools, strings1(args, "allow")...)
	}
	if err := c.ready(); err != nil {
		return nil, err
	}
	timeout := time.Duration(firstNumber(nil, config, "timeoutMinutes", 0) * float64(time.Minute))
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	timeout = min(timeout, MaxTimeout)
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel) // the caller giving up stops the session
	defer stop()
	s := &session{id: req.Resume, started: time.Now(), cancel: cancel}
	c.st.mu.Lock()
	if c.st.running[change] != nil {
		c.st.mu.Unlock()
		return nil, ErrBusy
	}
	c.st.running[change] = s
	c.st.mu.Unlock()
	defer func() {
		c.st.mu.Lock()
		delete(c.st.running, change)
		c.st.mu.Unlock()
	}()
	out, err := c.exec(runCtx, req, c.environment(change, dir, config, secrets), func(id string) {
		c.st.mu.Lock()
		s.id = id
		c.st.mu.Unlock()
	})
	if err != nil {
		return nil, err
	}
	if runCtx.Err() != nil && (out["status"] == StatusFailed || out["status"] == StatusCancelled) {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			out["status"], out["errors"] = StatusBudget, []any{fmt.Sprintf("the run took more than %s; resume the session to go on", timeout)}
		} else {
			out["status"] = StatusCancelled
		}
	}
	c.st.mu.Lock()
	c.st.last[change] = map[string]any{"status": out["status"], "sessionId": out["sessionId"], "endedAt": time.Now().UTC().Format(time.RFC3339)}
	c.st.mu.Unlock()
	return out, nil
}

// answerPrompt is the prompt resuming a session with a person's decision.
func answerPrompt(args map[string]any) string {
	var b strings.Builder
	if answers, ok := args["answers"].(map[string]any); ok && len(answers) > 0 {
		b.WriteString("A person answered your questions:\n")
		qs := make([]string, 0, len(answers))
		for q := range answers {
			qs = append(qs, q)
		}
		sort.Strings(qs)
		for _, q := range qs {
			fmt.Fprintf(&b, "- %s → %v\n", q, answers[q])
		}
	}
	if allow := strings1(args, "allow"); len(allow) > 0 {
		fmt.Fprintf(&b, "A person allowed these tools for this run: %s.\n", strings.Join(allow, ", "))
	}
	if a := strings.TrimSpace(stringArg(args, "answer")); a != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(a)
		b.WriteString("\n")
	}
	if b.Len() == 0 {
		return ""
	}
	return b.String() + "\nGo on with the task."
}

// ready writes the runner once and checks that the SDK is installed (unless the tests replace it).
func (c Connector) ready() error {
	c.st.install.Do(func() { c.st.instErr = Install(c.Dir) })
	if c.st.instErr != nil {
		return c.st.instErr
	}
	if c.sdkModule != "" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(c.Dir, "node_modules", filepath.FromSlash(SDKPackage), "package.json")); err != nil {
		return fmt.Errorf("%w: run `npm install` in %s (make claude-agent-runner)", ErrNoRunner, c.Dir)
	}
	return nil
}

// exec runs the runner for one turn: the request on its standard input, the outcome on the last line of its output.
func (c Connector) exec(ctx context.Context, req request, env []string, onSession func(string)) (map[string]any, error) {
	input, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	node := c.Node
	if node == "" {
		node = "node"
	}
	cmd := exec.CommandContext(ctx, node, filepath.Join(c.Dir, "runner.mjs"))
	cmd.Dir, cmd.Env, cmd.Stdin = req.Cwd, env, bytes.NewReader(input)
	cmd.Cancel = func() error { return interrupt(cmd.Process) }
	cmd.WaitDelay = 15 * time.Second
	var stderr tail
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start the runner: %w", err)
	}
	var result map[string]any
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64<<10), maxLine)
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		if id, _ := m["sessionId"].(string); id != "" {
			onSession(id)
		}
		if m["type"] == "result" {
			result = m
		}
	}
	waitErr := cmd.Wait()
	if result == nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" && waitErr != nil {
			msg = waitErr.Error()
		}
		return map[string]any{"status": StatusFailed, "sessionId": req.Resume, "errors": []any{"the runner gave no outcome: " + msg}}, nil
	}
	delete(result, "type")
	if s, _ := result["summary"].(string); len(s) > MaxSummary {
		result["summary"] = s[:MaxSummary/4] + "\n…\n" + s[len(s)-MaxSummary*3/4:]
	}
	if result["sessionId"] == "" || result["sessionId"] == nil {
		result["sessionId"] = req.Resume
	}
	return result, nil
}

// workspace is the directory a session of the change works in: the workspace of the change under the unit's root,
// or a directory of it.
func (c Connector) workspace(change string, config map[string]any, sub string) (string, error) {
	root := stringArg(config, "root")
	if root == "" {
		return "", errors.New("the adapter has no \"root\" configured")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if len(c.Roots) > 0 {
		ok := false
		for _, allowed := range c.Roots {
			ok = ok || within(allowed, root)
		}
		if !ok {
			return "", fmt.Errorf("root %q is not allowed on this connector", root)
		}
	}
	dir := filepath.Join(root, workspace.DirName(change))
	if p := path.Clean("/" + filepath.ToSlash(sub)); p != "/" {
		dir = filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(p, "/")))
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return "", fmt.Errorf("no workspace directory %s: open the workspace of the change first (workspace/open)", dir)
	}
	return dir, nil
}

// environment is the cleaned environment of a session: the host variables a toolchain needs and the ones the unit
// passes, its own values, the API key and endpoint. Nothing else of the host.
func (c Connector) environment(change, dir string, config map[string]any, secrets map[string]string) []string {
	vars := map[string]string{}
	for _, name := range append(append([]string{}, workspace.HostEnv...), strings1(config, "passEnv")...) {
		if v, ok := os.LookupEnv(name); ok {
			vars[name] = v
		}
	}
	for _, kv := range strings1(config, "env") {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			vars[k] = v
		}
	}
	if key := secrets[SecretAPIKey]; key != "" {
		vars["ANTHROPIC_API_KEY"] = key
	}
	if u := stringArg(config, "baseURL"); u != "" {
		vars["ANTHROPIC_BASE_URL"] = u
	}
	if c.sdkModule != "" {
		vars["GOAP_CLAUDE_AGENT_SDK_MODULE"] = c.sdkModule
	}
	vars["GOAP_WORKSPACE"], vars["GOAP_CHANGE"] = dir, change
	vars["CLAUDE_AGENT_SDK_CLIENT_APP"] = "goap/" + ID
	out := make([]string, 0, len(vars))
	for k, v := range vars {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

func interrupt(p *os.Process) error {
	if err := p.Signal(os.Interrupt); err != nil {
		return p.Kill()
	}
	return nil
}

func within(base, dir string) bool {
	base, err := filepath.Abs(base)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(base, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func stringArg(m map[string]any, name string) string {
	v, _ := m[name].(string)
	return v
}

func firstString(args, config map[string]any, name string) string {
	if v := stringArg(args, name); v != "" {
		return v
	}
	return stringArg(config, name)
}

func number(m map[string]any, name string) (float64, bool) {
	switch v := m[name].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	}
	return 0, false
}

func firstNumber(args, config map[string]any, name string, def float64) float64 {
	if v, ok := number(args, name); ok && v > 0 {
		return v
	}
	if v, ok := number(config, name); ok && v > 0 {
		return v
	}
	return def
}

// strings1 reads a list of strings.
func strings1(m map[string]any, name string) []string {
	switch v := m[name].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			if s, ok := x.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// tail keeps the end of what is written to it (the stderr of the runner).
type tail struct{ b []byte }

func (t *tail) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > 4096 {
		t.b = t.b[len(t.b)-4096:]
	}
	return len(p), nil
}

func (t *tail) String() string { return string(t.b) }
