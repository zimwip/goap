//! The local file system connector (`localfs.go`).

use std::collections::HashMap;
use std::io::Write;

use cap_std::ambient_authority;
use cap_std::fs::{Dir, OpenOptions};

use goap_connect::{Ctx, async_trait};
use goap_connectorkit::{Connector as KitConnector, ErrUnknownOperation, InvokeError};
use goap_gen::connector::v1::{ConnectorInfo, Operation};
use serde_json::{Map, Value, json};

/// MaxFileSize bounds what read_file returns.
pub const MAX_FILE_SIZE: u64 = 1 << 20;

/// Connector implements goap_connectorkit::Connector on the local file system.
#[derive(Debug, Clone, Default)]
pub struct Connector {
    /// Roots, when not empty, restricts the "root" an organization may configure to these directories or their
    /// sub-directories.
    pub roots: Vec<String>,
}

fn schema(v: Value) -> Option<pbjson_types::Struct> {
    goap_pbconv::to_struct(v.as_object().expect("a schema is an object"))
}

impl Connector {
    /// The root directory of the organization, every path confined under it, symbolic links included (Go's
    /// `os.OpenRoot`: a capability on the directory, `cap_std::fs::Dir`).
    fn root(&self, config: &Map<String, Value>) -> Result<Dir, InvokeError> {
        let dir = config.get("root").and_then(Value::as_str).unwrap_or("");
        if dir.is_empty() {
            return Err("the binding has no \"root\" configured".into());
        }
        if !self.roots.is_empty() && !self.roots.iter().any(|allowed| within(allowed, dir)) {
            return Err(format!("root {dir:?} is not allowed on this connector").into());
        }
        Dir::open_ambient_dir(dir, ambient_authority()).map_err(|e| err_at("openat", dir, e))
    }
}

fn within(base: &str, dir: &str) -> bool {
    rel_path(base, dir).is_some_and(|r| !r.is_empty())
}

fn rel_path(base: &str, dir: &str) -> Option<String> {
    let (base, dir) = (clean(base), clean(dir));
    if dir == base {
        return Some(".".into());
    }
    if dir.len() > base.len() && dir.starts_with(&base) && dir.as_bytes()[base.len()] == b'/' {
        return Some(dir[base.len() + 1..].to_string());
    }
    None
}

/// Go's `path.Clean`.
fn clean(p: &str) -> String {
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

fn rel(args: &Map<String, Value>) -> String {
    let p = args.get("path").and_then(Value::as_str).unwrap_or("");
    let p = clean(&format!("/{p}"))[1..].to_string();
    if p.is_empty() { ".".into() } else { p }
}

/// Go's `path.Dir`.
fn dir_of(p: &str) -> String {
    match p.rfind('/') {
        Some(i) => clean(&p[..i + 1]),
        None => ".".into(),
    }
}

fn err_at(op: &str, p: &str, e: std::io::Error) -> InvokeError {
    format!("{op} {p}: {e}").into()
}

fn invoke_sync(
    c: &Connector,
    op: &str,
    args: &Map<String, Value>,
    config: &Map<String, Value>,
) -> Result<Map<String, Value>, InvokeError> {
    let root = c.root(config)?;
    let p = rel(args);
    let obj = |v: Value| match v {
        Value::Object(m) => m,
        _ => Map::new(),
    };
    match op {
        "list_dir" => {
            let mut ents: Vec<cap_std::fs::DirEntry> = root
                .read_dir(&p)
                .map_err(|e| err_at("readdir", &p, e))?
                .collect::<Result<_, _>>()?;
            ents.sort_by_key(|e| e.file_name());
            let list: Vec<Value> = ents
                .iter()
                .map(|e| {
                    let size = e.metadata().map(|m| m.len()).unwrap_or(0);
                    let dir = e.file_type().is_ok_and(|t| t.is_dir());
                    json!({"name": e.file_name().to_string_lossy(), "dir": dir, "size": size})
                })
                .collect();
            Ok(obj(json!({ "entries": list })))
        }
        "read_file" => {
            let st = root.metadata(&p).map_err(|e| err_at("statat", &p, e))?;
            if st.is_dir() {
                return Err(format!("{p} is a directory").into());
            }
            if st.len() > MAX_FILE_SIZE {
                return Err(format!("{p} is larger than {MAX_FILE_SIZE} bytes").into());
            }
            let b = root.read(&p).map_err(|e| err_at("open", &p, e))?;
            Ok(obj(json!({ "text": String::from_utf8_lossy(&b) })))
        }
        "write_file" => {
            let content = args.get("content").and_then(Value::as_str).unwrap_or("");
            if p == "." {
                return Err("path is required".into());
            }
            let dir = dir_of(&p);
            if dir != "." {
                root.create_dir_all(&dir)
                    .map_err(|e| err_at("mkdirat", &dir, e))?;
            }
            let mut f = root
                .open_with(
                    &p,
                    OpenOptions::new().write(true).create(true).truncate(true),
                )
                .map_err(|e| err_at("openat", &p, e))?;
            f.write_all(content.as_bytes())?;
            Ok(obj(json!({"path": p, "bytes": content.len()})))
        }
        _ => Err(Box::new(ErrUnknownOperation)),
    }
}

#[async_trait]
impl KitConnector for Connector {
    fn info(&self) -> ConnectorInfo {
        let path_arg = json!({"type": "object", "properties": {"path": {"type": "string"}}});
        ConnectorInfo {
            id: "localfs".into(),
            version: "1".into(),
            description: "Directory of the local file system of the connector host.".into(),
            config_schema: schema(
                json!({"type": "object", "required": ["root"], "properties": {
                "root": {"type": "string", "description": "directory exposed to the organization"}}}),
            ),
            operations: vec![
                Operation {
                    name: "list_dir".into(),
                    description: "List a directory: {entries: [{name, dir, size}]}".into(),
                    input_schema: schema(path_arg.clone()),
                },
                Operation {
                    name: "read_file".into(),
                    description: "Read a text file: {text}".into(),
                    input_schema: schema(path_arg),
                },
                Operation {
                    name: "write_file".into(),
                    description: "Create or replace a text file".into(),
                    input_schema: schema(json!({"type": "object", "required": ["path", "content"],
                        "properties": {"path": {"type": "string"}, "content": {"type": "string"}}})),
                },
            ],
            ..Default::default()
        }
    }

    async fn invoke(
        &self,
        _ctx: &Ctx,
        op: &str,
        args: Map<String, Value>,
        config: Map<String, Value>,
        _secrets: HashMap<String, String>,
    ) -> Result<Map<String, Value>, InvokeError> {
        let c = self.clone();
        let op = op.to_string();
        tokio::task::spawn_blocking(move || invoke_sync(&c, &op, &args, &config))
            .await
            .map_err(|e| -> InvokeError { e.to_string().into() })?
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn paths_as_go_cleans_them() {
        assert_eq!(clean("/../../etc/passwd"), "/etc/passwd");
        assert_eq!(clean("a//b/./../c"), "a/c");
        assert_eq!(clean(""), ".");
        assert_eq!(dir_of("docs/a.txt"), "docs");
        assert_eq!(dir_of("a.txt"), ".");
        assert!(within("/a/b", "/a/b/c"));
        assert!(within("/a/b", "/a/b"));
        assert!(!within("/a/b", "/a/bc"));
    }
}
