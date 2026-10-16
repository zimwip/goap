package mcpbuiltin

import (
	"slices"

	"github.com/zimwip/goap/contracts/algo"
	"github.com/zimwip/goap/contracts/mcp"
	"github.com/zimwip/goap/lib/adapter"
)

// Workspace is the MCP of the workspace of a change (ADR 0118): the directory where its agents read, write, edit and
// search files, run commands and work on the sources they cloned, the tool box of a coding agent. It is defined on
// every platform but, unlike the built-ins of Names, no unit holds it by default: running commands is a capability
// an organisation gives (an adapter instance on a workspace connector), not one every unit has.
const Workspace = "workspace"

// WorkspaceLocal is the adapter definition of the workspace MCP on the connector of the local machine (one directory
// per change, commands not isolated) and the id of that connector.
const WorkspaceLocal = "workspace-local"

// WorkspaceDef is the definition of the workspace MCP. The workspace is the one of the change the call runs for: no
// tool names it.
func WorkspaceDef() mcp.Def {
	path := str("path in the workspace (relative; a leading / is the workspace itself)")
	return mcp.Def{Name: Workspace, Description: "The workspace of the change: files, commands and the sources it works on (one per change).", Tools: []mcp.Tool{
		{Name: "open", Description: "Open the workspace of the change: clone the sources to work on, each on the branch of the change (an already cloned source is kept as it is).",
			InputSchema: schemaObj(map[string]any{"sources": map[string]any{"type": "array", "description": "repositories to clone",
				"items": schemaObj(map[string]any{"repo": str("URL or path of the repository"), "ref": str("branch or tag to start from (default: the default branch)"),
					"path":   str("directory at the top of the workspace (default: the name of the repository)"),
					"branch": str("branch to work on (default: the branch of the change)")}, "repo")}})},
		{Name: "status", ReadOnly: true, Description: "Describe the workspace: its directory, its sources (branch, head, changed) and its background commands.",
			InputSchema: schemaObj(map[string]any{})},
		{Name: "close", Description: "Close the workspace: stop its background commands and remove its directory (keep: leave the directory).",
			InputSchema: schemaObj(map[string]any{"keep": boolean("keep the directory")})},
		{Name: "read", ReadOnly: true, Description: "Read a text file, each line prefixed with its number; offset and limit read a part of a long file.",
			InputSchema: schemaObj(map[string]any{"path": path, "offset": num("first line, 1-based (default 1)"), "limit": num("number of lines (default 2000)")}, "path")},
		{Name: "write", Description: "Create or replace a file with the given content (its directories are created).",
			InputSchema: schemaObj(map[string]any{"path": path, "content": str("whole text of the file")}, "path", "content")},
		{Name: "edit", Description: "Replace an exact string in a file: old must appear once (give more context to make it unique), or set all to replace every occurrence.",
			InputSchema: schemaObj(map[string]any{"path": path, "old": str("exact text to replace"), "new": str("replacement text"), "all": boolean("replace every occurrence")}, "path", "old", "new")},
		{Name: "ls", ReadOnly: true, Description: "List a directory of the workspace.",
			InputSchema: schemaObj(map[string]any{"path": path})},
		{Name: "glob", ReadOnly: true, Description: "Find files by glob pattern (src/**/*.go: ** for any number of directories), the latest modified first.",
			InputSchema: schemaObj(map[string]any{"pattern": str("glob pattern, relative to path"), "path": path, "limit": num("maximum number of files (default 250)")}, "pattern")},
		{Name: "grep", ReadOnly: true, Description: "Search file contents by regular expression (RE2): the files matching (mode files), the matching lines with their numbers (mode content) or the counts (mode count).",
			InputSchema: schemaObj(map[string]any{"pattern": str("regular expression"), "path": path, "glob": str("only the files matching this glob (*.go)"),
				"mode": str("files (default), content or count"), "ignoreCase": boolean("case insensitive"),
				"context": num("lines shown around a match (mode content)"), "limit": num("maximum number of files (default 250)")}, "pattern")},
		{Name: "bash", Description: "Run a shell command in the workspace (build, test, git): exit code, stdout and stderr (long outputs keep their head and tail). Each call starts a new shell: cwd sets its directory. background: start it and follow it with bash_output.",
			InputSchema: schemaObj(map[string]any{"command": str("shell command"), "cwd": str("directory of the workspace to run in (default: the workspace)"),
				"timeout": num("seconds (default 120, at most 600)"), "background": boolean("run in the background (a long build, a server)")}, "command")},
		{Name: "bash_output", ReadOnly: true, Description: "Read the output a background command wrote since the last read, and whether it still runs (else its exit code).",
			InputSchema: schemaObj(map[string]any{"id": str("id given by bash")}, "id")},
		{Name: "bash_kill", Description: "Stop a background command.",
			InputSchema: schemaObj(map[string]any{"id": str("id given by bash")}, "id")},
		{Name: "diff", ReadOnly: true, Description: "What changed in the sources since the change started from them: the files (git status) and the diff, commits made since included.",
			InputSchema: schemaObj(map[string]any{"path": str("one source (default: every source)")})},
		{Name: "push", Description: "Push the branch of the change of a source to its remote (only that branch).",
			InputSchema: schemaObj(map[string]any{"path": str("directory of the source")}, "path")},
	}}
}

// WorkspaceDocker is the adapter definition of the workspace MCP on the docker connector (the commands of each
// workspace in a container of its own) and the id of that connector.
const WorkspaceDocker = "workspace-docker"

// WorkspaceAdapterDefs are the adapter definitions of the workspace MCP: each tool is the operation of the same name
// of the connector.
func WorkspaceAdapterDefs() []adapter.Def {
	common := []algo.Param{
		{Name: "root", Type: algo.ParamString, Required: true, Description: "Directory holding one workspace per change"},
		{Name: "env", Type: algo.ParamStrings, Description: "Variables set for the commands (NAME=value)"},
		{Name: "gitName", Type: algo.ParamString, Description: "Author of the commits made in the workspace"},
		{Name: "gitEmail", Type: algo.ParamString, Description: "E-mail of that author"},
		{Name: "branchPrefix", Type: algo.ParamString, Description: "Prefix of the branch of a change in its sources (default goap/)"},
		{Name: "git_token", Type: adapter.ParamSecret, Description: "Token the sources are cloned and pushed with (never given to the commands)"},
	}
	const code = "return ctx.call(ctx.tool(), ctx.args());\n"
	return []adapter.Def{
		{Name: WorkspaceLocal, MCP: Workspace, Connector: WorkspaceLocal, Language: algo.JavaScript, Code: code,
			Description: "The workspace MCP on the local machine: one directory per change under root; the commands are not isolated (development, trusted hosts)",
			Params:      append(slices.Clone(common), algo.Param{Name: "passEnv", Type: algo.ParamStrings, Description: "Variables of the host environment given to the commands"})},
		{Name: WorkspaceDocker, MCP: Workspace, Connector: WorkspaceDocker, Language: algo.JavaScript, Code: code,
			Description: "The workspace MCP with the commands of each workspace in a container of its own (no network by default, limits, read-only image); the files and git stay on the connector's host",
			Params: append(slices.Clone(common),
				algo.Param{Name: "image", Type: algo.ParamString, Required: true, Description: "Image of the containers: the toolchains the changes build with, git and a shell"},
				algo.Param{Name: "network", Type: algo.ParamString, Description: "Network of the containers (default none: no network; a network whose egress is filtered for the hosts the unit allows)"},
				algo.Param{Name: "user", Type: algo.ParamString, Description: "User of the commands, uid:gid (default the connector's)"},
				algo.Param{Name: "memoryMB", Type: algo.ParamNumber, Description: "Memory limit of a container, in MiB (default 2048)"},
				algo.Param{Name: "cpus", Type: algo.ParamNumber, Description: "CPUs of a container (default 2)"},
				algo.Param{Name: "pids", Type: algo.ParamNumber, Description: "Process limit of a container (default 1024)"})},
	}
}

// WorkspaceAdapter is the instance of the local workspace adapter a unit holds, its workspaces under root.
func WorkspaceAdapter(unit, root string) adapter.Instance {
	return adapter.Instance{Unit: unit, MCP: Workspace, Adapter: WorkspaceLocal, Params: map[string]any{"root": root}}
}

// WorkspaceDockerAdapter is the instance of the docker workspace adapter a unit holds: its workspaces under root,
// their commands in containers of image.
func WorkspaceDockerAdapter(unit, root, image string) adapter.Instance {
	return adapter.Instance{Unit: unit, MCP: Workspace, Adapter: WorkspaceDocker, Params: map[string]any{"root": root, "image": image}}
}
