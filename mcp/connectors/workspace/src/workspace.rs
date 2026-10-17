//! The connector, its operations and the workspace of a call (`workspace.go`).

use std::collections::HashMap;
use std::path::{Component, Path, PathBuf};
use std::sync::Arc;

use goap_connect::{Ctx, async_trait};
use goap_connectorkit::{Connector as KitConnector, ErrUnknownOperation, InvokeError};
use goap_gen::connector::v1::{ConnectorInfo, Operation};
use serde_json::{Map, Value, json};

use crate::docker::DockerRunner;
use crate::runner::{LocalRunner, Runner};
use crate::shell::Jobs;

/// ID is the identifier of the connector.
pub const ID: &str = "workspace-local";

/// SecretGitToken is the secret the connector clones and pushes with (an HTTPS token), never given to the commands.
pub const SECRET_GIT_TOKEN: &str = "git_token";

/// DefaultBranchPrefix prefixes the branch of a change in the sources it clones.
pub const DEFAULT_BRANCH_PREFIX: &str = "goap/";

/// DockerID is the identifier of the connector running the commands of a workspace in a container (ADR 0118 §7).
pub const DOCKER_ID: &str = "workspace-docker";

/// ErrNoChange is returned for a call that runs for no change: a workspace is the one of a change.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct ErrNoChange;

impl std::fmt::Display for ErrNoChange {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("the workspace tools work for a change: this call runs for none")
    }
}

impl std::error::Error for ErrNoChange {}

// Operations of the connector: the tools of the workspace MCP, one for one.
pub const OP_OPEN: &str = "open";
pub const OP_STATUS: &str = "status";
pub const OP_CLOSE: &str = "close";
pub const OP_READ: &str = "read";
pub const OP_WRITE: &str = "write";
pub const OP_EDIT: &str = "edit";
pub const OP_LS: &str = "ls";
pub const OP_GLOB: &str = "glob";
pub const OP_GREP: &str = "grep";
pub const OP_BASH: &str = "bash";
pub const OP_BASH_OUTPUT: &str = "bash_output";
pub const OP_BASH_KILL: &str = "bash_kill";
pub const OP_DIFF: &str = "diff";
pub const OP_PUSH: &str = "push";

/// Connector implements goap_connectorkit::Connector: on the local machine ([`Connector::new`]) or with the
/// commands in a container per workspace ([`Connector::new_docker`]). Use a constructor: it keeps the background
/// commands (`Connector::default()` refuses every call, as Go's zero value).
#[derive(Clone, Default)]
pub struct Connector {
    /// Roots, when not empty, restricts the "root" a unit may configure to these directories or their
    /// sub-directories.
    pub roots: Vec<String>,
    pub(crate) jobs: Option<Arc<Jobs>>,
    /// runs the commands in containers; None: on the host
    pub(crate) docker: Option<Arc<DockerRunner>>,
}

impl std::fmt::Debug for Connector {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Connector")
            .field("roots", &self.roots)
            .field("docker", &self.docker.is_some())
            .finish()
    }
}

/// New returns a connector restricted to roots (none: any root).
pub fn new(roots: Vec<String>) -> Connector {
    Connector::new(roots)
}

/// NewDocker returns the connector running the commands of each workspace in a container of its own, through the
/// Docker Engine API at host ("unix:///var/run/docker.sock", "tcp://proxy:2375"), restricted to roots like New.
pub fn new_docker(host: &str, roots: Vec<String>) -> Connector {
    Connector::new_docker(host, roots)
}

impl Connector {
    /// See [`new`].
    pub fn new(roots: Vec<String>) -> Connector {
        Connector {
            roots,
            jobs: Some(Arc::new(Jobs::default())),
            docker: None,
        }
    }

    /// See [`new_docker`].
    pub fn new_docker(host: &str, roots: Vec<String>) -> Connector {
        let mut c = Connector::new(roots);
        c.docker = Some(Arc::new(DockerRunner::new(host)));
        c
    }

    pub(crate) fn runner(&self) -> Arc<dyn Runner> {
        match &self.docker {
            Some(d) => d.clone(),
            None => Arc::new(LocalRunner),
        }
    }
}

fn schema(v: Value) -> Option<pbjson_types::Struct> {
    goap_pbconv::to_struct(v.as_object().expect("a schema is an object"))
}

fn obj(props: Value, required: &[&str]) -> Value {
    let mut s = json!({"type": "object", "properties": props});
    if !required.is_empty() {
        s["required"] = json!(required);
    }
    s
}

fn str_(desc: &str) -> Value {
    json!({"type": "string", "description": desc})
}

fn num(desc: &str) -> Value {
    json!({"type": "integer", "description": desc})
}

fn flag(desc: &str) -> Value {
    json!({"type": "boolean", "description": desc})
}

fn op(name: &str, desc: &str, input: Value) -> Operation {
    Operation {
        name: name.into(),
        description: desc.into(),
        input_schema: schema(input),
    }
}

/// The workspace of one call: its directory and the configuration of the unit.
#[derive(Debug, Clone, Default)]
pub(crate) struct Space {
    pub(crate) id: String,
    pub(crate) dir: PathBuf,
    pub(crate) change: String,
    pub(crate) config: Map<String, Value>,
    pub(crate) secrets: HashMap<String, String>,
}

impl Space {
    /// The directory of a path argument on the host (confined by [`rel`]: it never leaves the workspace by "..").
    pub(crate) fn local(&self, p: &str) -> PathBuf {
        join_slash(&self.dir, &rel(p))
    }

    pub(crate) fn dir_str(&self) -> String {
        self.dir.to_string_lossy().into_owned()
    }
}

/// `filepath.Join(dir, filepath.FromSlash(p))` for a clean relative path.
pub(crate) fn join_slash(dir: &Path, p: &str) -> PathBuf {
    let mut out = dir.to_path_buf();
    if p != "." {
        for c in p.split('/') {
            out.push(c);
        }
    }
    out
}

impl Connector {
    fn info_impl(&self) -> ConnectorInfo {
        let p = str_("path in the workspace (relative; a leading / is the workspace itself)");
        let (mut id, mut desc, mut required) = (
            ID,
            "Workspaces of the changes in directories of the local machine (commands not isolated).",
            vec!["root"],
        );
        let mut props = json!({
            "root": str_("directory holding one workspace per change"),
            "env": {"type": "array", "items": {"type": "string"}, "description": "variables set for the commands (NAME=value)"},
            "gitName": str_("author of the commits made in the workspace"),
            "gitEmail": str_("e-mail of that author"),
            "branchPrefix": str_("prefix of the branch of a change in its sources (default goap/)"),
        });
        if self.docker.is_some() {
            (id, desc, required) = (
                DOCKER_ID,
                "Workspaces of the changes in directories of the connector's host, their commands in a container per workspace.",
                vec!["root", "image"],
            );
            props["image"] = str_(
                "image of the containers (the toolchains the changes build with, git, a shell)",
            );
            props["network"] = str_("network of the containers (default none: no network)");
            props["user"] = str_("user of the commands, uid:gid (default the connector's)");
            props["memoryMB"] = num("memory limit of a container, in MiB (default 2048)");
            props["cpus"] =
                json!({"type": "number", "description": "CPUs of a container (default 2)"});
            props["pids"] = num("process limit of a container (default 1024)");
        } else {
            props["passEnv"] = json!({"type": "array", "items": {"type": "string"}, "description": "variables of the host environment given to the commands"});
        }
        ConnectorInfo {
            id: id.into(),
            version: "1".into(),
            description: desc.into(),
            config_schema: schema(
                json!({"type": "object", "required": required, "properties": props}),
            ),
            secret_names: vec![SECRET_GIT_TOKEN.into()],
            operations: vec![
                op(
                    OP_OPEN,
                    "Open the workspace of the change: clone sources on the branch of the change",
                    obj(
                        json!({"sources": {"type": "array", "description": "repositories to clone", "items": obj(json!({
                            "repo": str_("URL or path of the repository"),
                            "ref": str_("branch or tag to start from (default: the default branch)"),
                            "path": str_("directory in the workspace (default: the name of the repository)"),
                            "branch": str_("branch to work on (default: the branch of the change)"),
                        }), &["repo"])}}),
                        &[],
                    ),
                ),
                op(
                    OP_STATUS,
                    "Describe the workspace: its directory, sources, running commands",
                    obj(json!({}), &[]),
                ),
                op(
                    OP_CLOSE,
                    "Close the workspace: stop its commands and remove its directory",
                    obj(json!({"keep": flag("keep the directory")}), &[]),
                ),
                op(
                    OP_READ,
                    "Read a text file, numbered by lines",
                    obj(
                        json!({"path": p, "offset": num("first line (1-based)"), "limit": num("number of lines")}),
                        &["path"],
                    ),
                ),
                op(
                    OP_WRITE,
                    "Create or replace a file",
                    obj(
                        json!({"path": p, "content": str_("text of the file")}),
                        &["path", "content"],
                    ),
                ),
                op(
                    OP_EDIT,
                    "Replace an exact string in a file",
                    obj(
                        json!({"path": p, "old": str_("text to replace"), "new": str_("replacement"), "all": flag("replace every occurrence")}),
                        &["path", "old", "new"],
                    ),
                ),
                op(OP_LS, "List a directory", obj(json!({"path": p}), &[])),
                op(
                    OP_GLOB,
                    "Find files by glob pattern (** for any depth), the latest modified first",
                    obj(
                        json!({"pattern": str_("glob pattern"), "path": p}),
                        &["pattern"],
                    ),
                ),
                op(
                    OP_GREP,
                    "Search file contents by regular expression",
                    obj(
                        json!({"pattern": str_("regular expression"), "path": p,
                            "glob": str_("only files matching this glob"), "mode": str_("files (default), content or count"),
                            "ignoreCase": flag("case insensitive"),
                            "context": num("lines around a match (mode content)"), "limit": num("maximum number of results")}),
                        &["pattern"],
                    ),
                ),
                op(
                    OP_BASH,
                    "Run a shell command in the workspace",
                    obj(
                        json!({"command": str_("shell command"), "cwd": p,
                            "timeout": num("seconds (default 120, at most 600)"),
                            "background": flag("run in the background: follow it with bash_output")}),
                        &["command"],
                    ),
                ),
                op(
                    OP_BASH_OUTPUT,
                    "Read the new output of a background command",
                    obj(json!({"id": str_("id of the command")}), &["id"]),
                ),
                op(
                    OP_BASH_KILL,
                    "Stop a background command",
                    obj(json!({"id": str_("id of the command")}), &["id"]),
                ),
                op(
                    OP_DIFF,
                    "Show what changed in the sources since they were cloned (git status and diff)",
                    obj(json!({"path": p}), &[]),
                ),
                op(
                    OP_PUSH,
                    "Push the branch of the change of a source to its remote",
                    obj(json!({"path": str_("directory of the source")}), &["path"]),
                ),
            ],
            ..Default::default()
        }
    }

    async fn invoke_impl(
        &self,
        ctx: &Ctx,
        op: &str,
        args: Map<String, Value>,
        config: Map<String, Value>,
        secrets: HashMap<String, String>,
    ) -> Result<Map<String, Value>, InvokeError> {
        let s = self.space(ctx, config, secrets)?;
        let Some(jobs) = self.jobs.clone() else {
            return Err("workspace connector not initialised (use workspace::new)".into());
        };
        if op == OP_STATUS || op == OP_CLOSE {
            return self.lifecycle(ctx, &jobs, op, &s, &args).await;
        }
        std::fs::create_dir_all(&s.dir)
            .map_err(|e| crate::files::path_err("mkdir", &s.dir_str(), e))?;
        match op {
            OP_OPEN => crate::git::open(ctx, &s, &args).await,
            OP_READ | OP_WRITE | OP_EDIT | OP_LS | OP_GLOB | OP_GREP => {
                let op = op.to_string();
                tokio::task::spawn_blocking(move || crate::files::files(&s, &op, &args))
                    .await
                    .map_err(|e| -> InvokeError { e.to_string().into() })?
            }
            OP_BASH => crate::shell::bash(ctx, &jobs, self.runner(), &s, &args).await,
            OP_BASH_OUTPUT => jobs.output(&s.dir_str(), string_arg(&args, "id")).await,
            OP_BASH_KILL => jobs.kill(&s.dir_str(), string_arg(&args, "id")).await,
            OP_DIFF => crate::git::diff(ctx, &s, &args).await,
            OP_PUSH => crate::git::push(ctx, &s, &args).await,
            _ => Err(format!("{op}: {ErrUnknownOperation}").into()),
        }
    }

    async fn lifecycle(
        &self,
        ctx: &Ctx,
        jobs: &Jobs,
        op: &str,
        s: &Space,
        args: &Map<String, Value>,
    ) -> Result<Map<String, Value>, InvokeError> {
        let exists = std::fs::metadata(&s.dir).is_ok();
        if op == OP_CLOSE {
            let stopped = jobs.stop_all(&s.dir_str()).await;
            self.runner().remove(ctx, s).await?;
            let keep = bool_arg(args, "keep");
            if !keep && exists {
                std::fs::remove_dir_all(&s.dir)
                    .map_err(|e| crate::files::path_err("unlinkat", &s.dir_str(), e))?;
            }
            return Ok(to_map(
                json!({"closed": exists, "stopped": stopped as f64, "kept": keep}),
            ));
        }
        let mut out =
            to_map(json!({"exists": exists, "directory": s.dir_str(), "change": s.change}));
        if exists {
            out.insert(
                "sources".into(),
                Value::Array(crate::git::sources(ctx, s).await),
            );
            out.insert(
                "commands".into(),
                Value::Array(jobs.list(&s.dir_str()).await),
            );
            if let Some(st) = self.runner().status(ctx, s).await {
                out.insert("container".into(), Value::Object(st));
            }
        }
        Ok(out)
    }

    /// The workspace of the call: the directory of its change under the root of the unit.
    fn space(
        &self,
        ctx: &Ctx,
        config: Map<String, Value>,
        secrets: HashMap<String, String>,
    ) -> Result<Space, InvokeError> {
        let change = goap_mcp::call_from(ctx).change;
        if change.is_empty() {
            return Err(Box::new(ErrNoChange));
        }
        let root = config.get("root").and_then(Value::as_str).unwrap_or("");
        if root.is_empty() {
            return Err("the adapter has no \"root\" configured".into());
        }
        let root = abs(root)?;
        if !self.roots.is_empty() && !self.roots.iter().any(|allowed| within(allowed, &root)) {
            return Err(format!(
                "root {:?} is not allowed on this connector",
                root.to_string_lossy()
            )
            .into());
        }
        let id = dir_name(&change);
        Ok(Space {
            dir: root.join(&id),
            id,
            change,
            config,
            secrets,
        })
    }
}

#[async_trait]
impl KitConnector for Connector {
    fn info(&self) -> ConnectorInfo {
        self.info_impl()
    }

    async fn invoke(
        &self,
        ctx: &Ctx,
        op: &str,
        args: Map<String, Value>,
        config: Map<String, Value>,
        secrets: HashMap<String, String>,
    ) -> Result<Map<String, Value>, InvokeError> {
        self.invoke_impl(ctx, op, args, config, secrets).await
    }
}

pub(crate) fn to_map(v: Value) -> Map<String, Value> {
    match v {
        Value::Object(m) => m,
        _ => Map::new(),
    }
}

/// DirName is the name of the directory of the workspace of a change under the root of its unit: another connector
/// working in the workspace of a change (ADR 0123) finds it there.
pub fn dir_name(s: &str) -> String {
    let b: Vec<u8> = s
        .bytes()
        .map(|ch| {
            if ch.is_ascii_alphanumeric() || ch == b'-' || ch == b'_' || ch == b'.' {
                ch
            } else {
                b'_'
            }
        })
        .collect();
    let s = String::from_utf8(b).unwrap_or_default();
    let out = s.trim_matches('.');
    if out.is_empty() {
        "_".into()
    } else {
        out.into()
    }
}

/// `filepath.Abs`: the path made absolute against the working directory, cleaned lexically.
pub fn abs(p: &str) -> std::io::Result<PathBuf> {
    let p = Path::new(p);
    let full = if p.is_absolute() {
        p.to_path_buf()
    } else {
        std::env::current_dir()?.join(p)
    };
    Ok(clean_path(&full))
}

/// `filepath.Clean` of an absolute path: "." dropped, ".." taking the previous component (never above the root).
fn clean_path(p: &Path) -> PathBuf {
    let mut out = PathBuf::new();
    for c in p.components() {
        match c {
            Component::CurDir => {}
            Component::ParentDir => {
                let at_root = out.parent().is_none()
                    || matches!(
                        out.components().next_back(),
                        Some(Component::RootDir | Component::Prefix(_))
                    );
                if !at_root {
                    out.pop();
                }
            }
            c => out.push(c.as_os_str()),
        }
    }
    out
}

/// Whether dir is base or under it (both made absolute, compared component by component).
pub fn within(base: &str, dir: &Path) -> bool {
    let Ok(base) = abs(base) else {
        return false;
    };
    clean_path(dir).starts_with(&base)
}

/// Go's `path.Clean`.
pub(crate) fn clean(p: &str) -> String {
    if p.is_empty() {
        return ".".into();
    }
    let rooted = p.starts_with('/');
    let mut out: Vec<&str> = Vec::new();
    for c in p.split('/') {
        match c {
            "" | "." => {}
            ".." => {
                if out.last().is_some_and(|l| *l != "..") {
                    out.pop();
                } else if !rooted {
                    out.push("..");
                }
            }
            c => out.push(c),
        }
    }
    let s = out.join("/");
    match (rooted, s.is_empty()) {
        (true, _) => format!("/{s}"),
        (false, true) => ".".into(),
        (false, false) => s,
    }
}

/// Go's `path.Dir`.
pub(crate) fn dir_of(p: &str) -> String {
    match p.rfind('/') {
        Some(i) => clean(&p[..i + 1]),
        None => ".".into(),
    }
}

/// rel turns a path argument into a path relative to the workspace ("." for the workspace itself).
pub(crate) fn rel(p: &str) -> String {
    let p = clean(&format!("/{}", p.replace(std::path::MAIN_SEPARATOR, "/")));
    if p == "/" {
        ".".into()
    } else {
        p.trim_start_matches('/').into()
    }
}

pub(crate) fn string_arg<'a>(args: &'a Map<String, Value>, name: &str) -> &'a str {
    args.get(name).and_then(Value::as_str).unwrap_or("")
}

pub(crate) fn bool_arg(args: &Map<String, Value>, name: &str) -> bool {
    args.get(name).and_then(Value::as_bool).unwrap_or(false)
}

/// A number argument as an integer (a float truncated, as Go's `int(v)`), else def.
pub(crate) fn int_arg(args: &Map<String, Value>, name: &str, def: i64) -> i64 {
    match args.get(name) {
        Some(Value::Number(n)) => n
            .as_i64()
            .unwrap_or_else(|| n.as_f64().map_or(def, |f| f as i64)),
        _ => def,
    }
}

pub(crate) fn branch_of(s: &Space) -> String {
    let prefix = s
        .config
        .get("branchPrefix")
        .and_then(Value::as_str)
        .unwrap_or("");
    let prefix = if prefix.is_empty() {
        DEFAULT_BRANCH_PREFIX
    } else {
        prefix
    };
    format!("{prefix}{}", s.id)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn paths_as_go_cleans_them() {
        assert_eq!(rel("/../../etc/passwd"), "etc/passwd");
        assert_eq!(rel(""), ".");
        assert_eq!(rel("/"), ".");
        assert_eq!(rel("a//b/./../c"), "a/c");
        assert_eq!(dir_name("CHG-1"), "CHG-1");
        assert_eq!(dir_name("..a/b c.."), "a_b_c");
        assert_eq!(dir_name("..."), "_");
        assert!(within("/a/b", Path::new("/a/b/c")));
        assert!(within("/a/b", Path::new("/a/b")));
        assert!(!within("/a/b", Path::new("/a/bc")));
        assert_eq!(abs("/a/./b/../c").unwrap(), PathBuf::from("/a/c"));
    }
}
