//! The MCP of the workspace of a change (ADR 0118).

use goap_adapter as adapter;
use goap_algo as algo;
use goap_mcp as mcp;
use serde_json::{Map, json};

use crate::builtin::{PASS_THROUGH, boolean, num, schema_obj, string, tool};

/// Workspace is the MCP of the workspace of a change (ADR 0118): the directory where its agents read, write, edit
/// and search files, run commands and work on the sources they cloned, the tool box of a coding agent. It is defined
/// on every platform but, unlike the built-ins of NAMES, no unit holds it by default: running commands is a
/// capability an organisation gives (an adapter instance on a workspace connector), not one every unit has.
pub const WORKSPACE: &str = "workspace";

/// WorkspaceLocal is the adapter definition of the workspace MCP on the connector of the local machine (one
/// directory per change, commands not isolated) and the id of that connector.
pub const WORKSPACE_LOCAL: &str = "workspace-local";

/// WorkspaceDef is the definition of the workspace MCP. The workspace is the one of the change the call runs for: no
/// tool names it.
pub fn workspace_def() -> mcp::Def {
    let path = string("path in the workspace (relative; a leading / is the workspace itself)");
    mcp::Def {
        name: WORKSPACE.into(),
        description: "The workspace of the change: files, commands and the sources it works on (one per change).".into(),
        tools: vec![
        tool("open", false, "Open the workspace of the change: clone the sources to work on, each on the branch of the change (an already cloned source is kept as it is).", schema_obj(json!({"sources": json!({"type": "array", "description": "repositories to clone", "items": schema_obj(json!({"repo": string("URL or path of the repository"), "ref": string("branch or tag to start from (default: the default branch)"), "path": string("directory at the top of the workspace (default: the name of the repository)"), "branch": string("branch to work on (default: the branch of the change)")}), &["repo"])})}), &[])),
        tool("status", true, "Describe the workspace: its directory, its sources (branch, head, changed) and its background commands.", schema_obj(json!({}), &[])),
        tool("close", false, "Close the workspace: stop its background commands and remove its directory (keep: leave the directory).", schema_obj(json!({"keep": boolean("keep the directory")}), &[])),
        tool("read", true, "Read a text file, each line prefixed with its number; offset and limit read a part of a long file.", schema_obj(json!({"path": path.clone(), "offset": num("first line, 1-based (default 1)"), "limit": num("number of lines (default 2000)")}), &["path"])),
        tool("write", false, "Create or replace a file with the given content (its directories are created).", schema_obj(json!({"path": path.clone(), "content": string("whole text of the file")}), &["path", "content"])),
        tool("edit", false, "Replace an exact string in a file: old must appear once (give more context to make it unique), or set all to replace every occurrence.", schema_obj(json!({"path": path.clone(), "old": string("exact text to replace"), "new": string("replacement text"), "all": boolean("replace every occurrence")}), &["path", "old", "new"])),
        tool("ls", true, "List a directory of the workspace.", schema_obj(json!({"path": path.clone()}), &[])),
        tool("glob", true, "Find files by glob pattern (src/**/*.go: ** for any number of directories), the latest modified first.", schema_obj(json!({"pattern": string("glob pattern, relative to path"), "path": path.clone(), "limit": num("maximum number of files (default 250)")}), &["pattern"])),
        tool("grep", true, "Search file contents by regular expression (RE2): the files matching (mode files), the matching lines with their numbers (mode content) or the counts (mode count).", schema_obj(json!({"pattern": string("regular expression"), "path": path.clone(), "glob": string("only the files matching this glob (*.go)"), "mode": string("files (default), content or count"), "ignoreCase": boolean("case insensitive"), "context": num("lines shown around a match (mode content)"), "limit": num("maximum number of files (default 250)")}), &["pattern"])),
        tool("bash", false, "Run a shell command in the workspace (build, test, git): exit code, stdout and stderr (long outputs keep their head and tail). Each call starts a new shell: cwd sets its directory. background: start it and follow it with bash_output.", schema_obj(json!({"command": string("shell command"), "cwd": string("directory of the workspace to run in (default: the workspace)"), "timeout": num("seconds (default 120, at most 600)"), "background": boolean("run in the background (a long build, a server)")}), &["command"])),
        tool("bash_output", true, "Read the output a background command wrote since the last read, and whether it still runs (else its exit code).", schema_obj(json!({"id": string("id given by bash")}), &["id"])),
        tool("bash_kill", false, "Stop a background command.", schema_obj(json!({"id": string("id given by bash")}), &["id"])),
        tool("diff", true, "What changed in the sources since the change started from them: the files (git status) and the diff, commits made since included.", schema_obj(json!({"path": string("one source (default: every source)")}), &[])),
        tool("push", false, "Push the branch of the change of a source to its remote (only that branch).", schema_obj(json!({"path": string("directory of the source")}), &["path"])),
    ],
        ..Default::default()
    }
}

/// WorkspaceDocker is the adapter definition of the workspace MCP on the docker connector (the commands of each
/// workspace in a container of its own) and the id of that connector.
pub const WORKSPACE_DOCKER: &str = "workspace-docker";

fn param(name: &str, typ: &str, required: bool, description: &str) -> algo::Param {
    algo::Param {
        name: name.into(),
        r#type: typ.into(),
        required,
        description: description.into(),
        ..Default::default()
    }
}

/// WorkspaceAdapterDefs are the adapter definitions of the workspace MCP: each tool is the operation of the same
/// name of the connector.
pub fn workspace_adapter_defs() -> Vec<adapter::Def> {
    let common = vec![
        param(
            "root",
            algo::PARAM_STRING,
            true,
            "Directory holding one workspace per change",
        ),
        param(
            "env",
            algo::PARAM_STRINGS,
            false,
            "Variables set for the commands (NAME=value)",
        ),
        param(
            "gitName",
            algo::PARAM_STRING,
            false,
            "Author of the commits made in the workspace",
        ),
        param(
            "gitEmail",
            algo::PARAM_STRING,
            false,
            "E-mail of that author",
        ),
        param(
            "branchPrefix",
            algo::PARAM_STRING,
            false,
            "Prefix of the branch of a change in its sources (default goap/)",
        ),
        param(
            "git_token",
            adapter::PARAM_SECRET,
            false,
            "Token the sources are cloned and pushed with (never given to the commands)",
        ),
    ];
    let with = |extra: Vec<algo::Param>| {
        let mut p = common.clone();
        p.extend(extra);
        p
    };
    vec![
        adapter::Def {
            name: WORKSPACE_LOCAL.into(),
            mcp: WORKSPACE.into(),
            connector: WORKSPACE_LOCAL.into(),
            language: algo::JAVASCRIPT.into(),
            code: PASS_THROUGH.into(),
            description: "The workspace MCP on the local machine: one directory per change under root; the commands are not isolated (development, trusted hosts)".into(),
            params: with(vec![param("passEnv", algo::PARAM_STRINGS, false, "Variables of the host environment given to the commands")]),
        },
        adapter::Def {
            name: WORKSPACE_DOCKER.into(),
            mcp: WORKSPACE.into(),
            connector: WORKSPACE_DOCKER.into(),
            language: algo::JAVASCRIPT.into(),
            code: PASS_THROUGH.into(),
            description: "The workspace MCP with the commands of each workspace in a container of its own (no network by default, limits, read-only image); the files and git stay on the connector's host".into(),
            params: with(vec![
                param("image", algo::PARAM_STRING, true, "Image of the containers: the toolchains the changes build with, git and a shell"),
                param("network", algo::PARAM_STRING, false, "Network of the containers (default none: no network; a network whose egress is filtered for the hosts the unit allows)"),
                param("user", algo::PARAM_STRING, false, "User of the commands, uid:gid (default the connector's)"),
                param("memoryMB", algo::PARAM_NUMBER, false, "Memory limit of a container, in MiB (default 2048)"),
                param("cpus", algo::PARAM_NUMBER, false, "CPUs of a container (default 2)"),
                param("pids", algo::PARAM_NUMBER, false, "Process limit of a container (default 1024)"),
            ]),
        },
    ]
}

/// WorkspaceAdapter is the instance of the local workspace adapter a unit holds, its workspaces under root.
pub fn workspace_adapter(unit: &str, root: &str) -> adapter::Instance {
    let mut params = Map::new();
    params.insert("root".into(), json!(root));
    adapter::Instance {
        unit: unit.into(),
        mcp: WORKSPACE.into(),
        adapter: WORKSPACE_LOCAL.into(),
        params,
        ..Default::default()
    }
}

/// WorkspaceDockerAdapter is the instance of the docker workspace adapter a unit holds: its workspaces under root,
/// their commands in containers of image.
pub fn workspace_docker_adapter(unit: &str, root: &str, image: &str) -> adapter::Instance {
    let mut params = Map::new();
    params.insert("root".into(), json!(root));
    params.insert("image".into(), json!(image));
    adapter::Instance {
        unit: unit.into(),
        mcp: WORKSPACE.into(),
        adapter: WORKSPACE_DOCKER.into(),
        params,
        ..Default::default()
    }
}
