// Package workspace is the connector of the workspace MCP on the local machine (ADR 0118): one directory per change
// under a root the unit configures, where the agents of the change read, write, edit and search files, run commands
// and work on the sources they cloned, the way a coding agent works on a checkout.
//
// The file operations are confined to the directory of the workspace (os.Root, symbolic links included). The
// commands are not: they run with the workspace as working directory, a cleaned environment, a timeout and their
// whole process group killed at the end, but nothing stops a command from reaching outside the directory. This
// connector is for development and trusted hosts; an isolated workspace is a container connector.
//
// The workspace is the one of the change the call runs for (mcp.CallContext, carried by InvokeRequest.change_id):
// the tools name no workspace.
package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"google.golang.org/protobuf/types/known/structpb"

	connectorv1 "github.com/zimwip/goap/gen/goap/connector/v1"
	"github.com/zimwip/goap/internal/connectorkit"
	"github.com/zimwip/goap/pkg/mcp"
)

// ID is the identifier of the connector.
const ID = "workspace-local"

// SecretGitToken is the secret the connector clones and pushes with (an HTTPS token), never given to the commands.
const SecretGitToken = "git_token"

// DefaultBranchPrefix prefixes the branch of a change in the sources it clones.
const DefaultBranchPrefix = "goap/"

// ErrNoChange is returned for a call that runs for no change: a workspace is the one of a change.
var ErrNoChange = errors.New("the workspace tools work for a change: this call runs for none")

// DockerID is the identifier of the connector running the commands of a workspace in a container (ADR 0118 §7).
const DockerID = "workspace-docker"

// Connector implements connectorkit.Connector: on the local machine (New) or with the commands in a container per
// workspace (NewDocker). Use a constructor: it keeps the background commands.
type Connector struct {
	// Roots, when not empty, restricts the "root" a unit may configure to these directories or their sub-directories.
	Roots []string
	jobs  *jobs
	// docker runs the commands in containers; nil: on the host
	docker *dockerRunner
}

var _ connectorkit.Connector = Connector{}

// New returns a connector restricted to roots (none: any root).
func New(roots ...string) Connector {
	return Connector{Roots: roots, jobs: &jobs{byWorkspace: map[string]map[string]*job{}}}
}

// NewDocker returns the connector running the commands of each workspace in a container of its own, through the
// Docker Engine API at host ("unix:///var/run/docker.sock", "tcp://proxy:2375"), restricted to roots like New.
func NewDocker(host string, roots ...string) Connector {
	c := New(roots...)
	c.docker = &dockerRunner{api: &dockerAPI{host: host}}
	return c
}

func (c Connector) runner() runner {
	if c.docker != nil {
		return c.docker
	}
	return localRunner{}
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

func str(desc string) map[string]any  { return map[string]any{"type": "string", "description": desc} }
func num(desc string) map[string]any  { return map[string]any{"type": "integer", "description": desc} }
func flag(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }

// Operations of the connector: the tools of the workspace MCP, one for one.
const (
	OpOpen       = "open"
	OpStatus     = "status"
	OpClose      = "close"
	OpRead       = "read"
	OpWrite      = "write"
	OpEdit       = "edit"
	OpLs         = "ls"
	OpGlob       = "glob"
	OpGrep       = "grep"
	OpBash       = "bash"
	OpBashOutput = "bash_output"
	OpBashKill   = "bash_kill"
	OpDiff       = "diff"
	OpPush       = "push"
)

// Info implements connectorkit.Connector.
func (c Connector) Info() *connectorv1.ConnectorInfo {
	p := str("path in the workspace (relative; a leading / is the workspace itself)")
	op := func(name, desc string, input map[string]any) *connectorv1.Operation {
		return &connectorv1.Operation{Name: name, Description: desc, InputSchema: schema(input)}
	}
	id, desc, required := ID, "Workspaces of the changes in directories of the local machine (commands not isolated).", []any{"root"}
	props := map[string]any{
		"root":         str("directory holding one workspace per change"),
		"env":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "variables set for the commands (NAME=value)"},
		"gitName":      str("author of the commits made in the workspace"),
		"gitEmail":     str("e-mail of that author"),
		"branchPrefix": str("prefix of the branch of a change in its sources (default goap/)"),
	}
	if c.docker != nil {
		id, desc, required = DockerID, "Workspaces of the changes in directories of the connector's host, their commands in a container per workspace.", []any{"root", "image"}
		props["image"] = str("image of the containers (the toolchains the changes build with, git, a shell)")
		props["network"] = str("network of the containers (default none: no network)")
		props["user"] = str("user of the commands, uid:gid (default the connector's)")
		props["memoryMB"] = num("memory limit of a container, in MiB (default 2048)")
		props["cpus"] = map[string]any{"type": "number", "description": "CPUs of a container (default 2)"}
		props["pids"] = num("process limit of a container (default 1024)")
	} else {
		props["passEnv"] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "variables of the host environment given to the commands"}
	}
	return &connectorv1.ConnectorInfo{
		Id: id, Version: "1", Description: desc,
		ConfigSchema: schema(map[string]any{"type": "object", "required": required, "properties": props}),
		SecretNames:  []string{SecretGitToken},
		Operations: []*connectorv1.Operation{
			op(OpOpen, "Open the workspace of the change: clone sources on the branch of the change", obj(map[string]any{
				"sources": map[string]any{"type": "array", "description": "repositories to clone", "items": obj(map[string]any{
					"repo": str("URL or path of the repository"), "ref": str("branch or tag to start from (default: the default branch)"),
					"path":   str("directory in the workspace (default: the name of the repository)"),
					"branch": str("branch to work on (default: the branch of the change)")}, "repo")}})),
			op(OpStatus, "Describe the workspace: its directory, sources, running commands", obj(nil)),
			op(OpClose, "Close the workspace: stop its commands and remove its directory", obj(map[string]any{"keep": flag("keep the directory")})),
			op(OpRead, "Read a text file, numbered by lines", obj(map[string]any{"path": p, "offset": num("first line (1-based)"), "limit": num("number of lines")}, "path")),
			op(OpWrite, "Create or replace a file", obj(map[string]any{"path": p, "content": str("text of the file")}, "path", "content")),
			op(OpEdit, "Replace an exact string in a file", obj(map[string]any{"path": p, "old": str("text to replace"), "new": str("replacement"), "all": flag("replace every occurrence")}, "path", "old", "new")),
			op(OpLs, "List a directory", obj(map[string]any{"path": p})),
			op(OpGlob, "Find files by glob pattern (** for any depth), the latest modified first", obj(map[string]any{"pattern": str("glob pattern"), "path": p}, "pattern")),
			op(OpGrep, "Search file contents by regular expression", obj(map[string]any{"pattern": str("regular expression"), "path": p,
				"glob": str("only files matching this glob"), "mode": str("files (default), content or count"), "ignoreCase": flag("case insensitive"),
				"context": num("lines around a match (mode content)"), "limit": num("maximum number of results")}, "pattern")),
			op(OpBash, "Run a shell command in the workspace", obj(map[string]any{"command": str("shell command"), "cwd": p,
				"timeout": num("seconds (default 120, at most 600)"), "background": flag("run in the background: follow it with bash_output")}, "command")),
			op(OpBashOutput, "Read the new output of a background command", obj(map[string]any{"id": str("id of the command")}, "id")),
			op(OpBashKill, "Stop a background command", obj(map[string]any{"id": str("id of the command")}, "id")),
			op(OpDiff, "Show what changed in the sources since they were cloned (git status and diff)", obj(map[string]any{"path": p})),
			op(OpPush, "Push the branch of the change of a source to its remote", obj(map[string]any{"path": str("directory of the source")}, "path")),
		},
	}
}

// space is the workspace of one call: its directory and the configuration of the unit.
type space struct {
	id, dir, change string
	config          map[string]any
	secrets         map[string]string
}

// Invoke implements connectorkit.Connector.
func (c Connector) Invoke(ctx context.Context, op string, args, config map[string]any, secrets map[string]string) (map[string]any, error) {
	s, err := c.space(ctx, config, secrets)
	if err != nil {
		return nil, err
	}
	if c.jobs == nil {
		return nil, errors.New("workspace connector not initialised (use workspace.New)")
	}
	if op == OpStatus || op == OpClose {
		return c.lifecycle(ctx, op, s, args)
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return nil, err
	}
	switch op {
	case OpOpen:
		return c.open(ctx, s, args)
	case OpRead, OpWrite, OpEdit, OpLs, OpGlob, OpGrep:
		return files(s, op, args)
	case OpBash:
		return c.bash(ctx, s, args)
	case OpBashOutput:
		return c.jobs.output(s.dir, stringArg(args, "id"))
	case OpBashKill:
		return c.jobs.kill(s.dir, stringArg(args, "id"))
	case OpDiff:
		return diff(ctx, s, args)
	case OpPush:
		return push(ctx, s, args)
	}
	return nil, fmt.Errorf("%s: %w", op, connectorkit.ErrUnknownOperation)
}

func (c Connector) lifecycle(ctx context.Context, op string, s space, args map[string]any) (map[string]any, error) {
	_, statErr := os.Stat(s.dir)
	exists := statErr == nil
	if op == OpClose {
		stopped := c.jobs.stopAll(s.dir)
		if err := c.runner().remove(ctx, s); err != nil {
			return nil, err
		}
		if !boolArg(args, "keep") && exists {
			if err := os.RemoveAll(s.dir); err != nil {
				return nil, err
			}
		}
		return map[string]any{"closed": exists, "stopped": float64(stopped), "kept": boolArg(args, "keep")}, nil
	}
	out := map[string]any{"exists": exists, "directory": s.dir, "change": s.change}
	if exists {
		out["sources"] = sources(ctx, s)
		out["commands"] = c.jobs.list(s.dir)
		if st := c.runner().status(ctx, s); st != nil {
			out["container"] = st
		}
	}
	return out, nil
}

// space resolves the workspace of the call: the directory of its change under the root of the unit.
func (c Connector) space(ctx context.Context, config map[string]any, secrets map[string]string) (space, error) {
	change := mcp.CallFrom(ctx).Change
	if change == "" {
		return space{}, ErrNoChange
	}
	root, _ := config["root"].(string)
	if root == "" {
		return space{}, errors.New("the adapter has no \"root\" configured")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return space{}, err
	}
	if len(c.Roots) > 0 {
		ok := false
		for _, allowed := range c.Roots {
			ok = ok || within(allowed, root)
		}
		if !ok {
			return space{}, fmt.Errorf("root %q is not allowed on this connector", root)
		}
	}
	id := safeName(change)
	return space{id: id, dir: filepath.Join(root, id), change: change, config: config, secrets: secrets}, nil
}

// safeName turns a change id into a directory name.
func safeName(s string) string {
	b := []byte(s)
	for i, ch := range b {
		ok := ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_' || ch == '.'
		if !ok {
			b[i] = '_'
		}
	}
	out := strings.Trim(string(b), ".")
	if out == "" {
		out = "_"
	}
	return out
}

func within(base, dir string) bool {
	base, err := filepath.Abs(base)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(base, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// rel turns a path argument into a path relative to the workspace ("." for the workspace itself).
func rel(p string) string {
	p = path.Clean("/" + filepath.ToSlash(p))
	if p == "/" {
		return "."
	}
	return strings.TrimPrefix(p, "/")
}

// local is the directory of a path argument on the host (confined by rel: it never leaves the workspace by "..").
func (s space) local(p string) string { return filepath.Join(s.dir, filepath.FromSlash(rel(p))) }

func stringArg(args map[string]any, name string) string {
	v, _ := args[name].(string)
	return v
}

func boolArg(args map[string]any, name string) bool {
	v, _ := args[name].(bool)
	return v
}

func intArg(args map[string]any, name string, def int) int {
	switch v := args[name].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64: // a script's number, in process
		return int(v)
	}
	return def
}

func branchOf(s space) string {
	prefix, _ := s.config["branchPrefix"].(string)
	if prefix == "" {
		prefix = DefaultBranchPrefix
	}
	return prefix + s.id
}
