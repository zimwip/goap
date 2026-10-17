use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};

use goap_connect::Ctx;
use goap_connectorkit::{Connector as _, InvokeError};
use goap_mcp::{CallContext, with_call};
use serde_json::{Map, Value, json};

use crate::*;

/// A temporary directory removed on drop (`t.TempDir`).
pub(crate) struct TempDir(pub(crate) PathBuf);

impl TempDir {
    pub(crate) fn new() -> Self {
        use std::sync::atomic::{AtomicU64, Ordering};
        static SEQ: AtomicU64 = AtomicU64::new(0);
        let p = std::env::temp_dir().join(format!(
            "goap-workspace-{}-{}-{}",
            std::process::id(),
            SEQ.fetch_add(1, Ordering::SeqCst),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        std::fs::create_dir_all(&p).unwrap();
        TempDir(p)
    }

    pub(crate) fn path(&self) -> &Path {
        &self.0
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

pub(crate) struct Fixture {
    pub(crate) c: Connector,
    pub(crate) ctx: Ctx,
    pub(crate) config: Map<String, Value>,
    pub(crate) dir: PathBuf,
    _root: TempDir,
}

pub(crate) fn obj(v: Value) -> Map<String, Value> {
    match v {
        Value::Object(m) => m,
        _ => Map::new(),
    }
}

pub(crate) fn new_fixture() -> Fixture {
    let root = TempDir::new();
    Fixture {
        c: Connector::new(vec![]),
        ctx: with_call(
            &Ctx::background(),
            CallContext {
                unit: "ORG-DEFAULT".into(),
                change: "CHG-1".into(),
                ..Default::default()
            },
        ),
        config: obj(
            json!({"root": root.path().to_string_lossy(), "gitName": "GOAP", "gitEmail": "goap@example.com"}),
        ),
        dir: root.path().join("CHG-1"),
        _root: root,
    }
}

impl Fixture {
    pub(crate) async fn call(
        &self,
        op: &str,
        args: Value,
    ) -> Result<Map<String, Value>, InvokeError> {
        let secrets = HashMap::from([(SECRET_GIT_TOKEN.to_string(), "s3cr3t".to_string())]);
        self.c
            .invoke(&self.ctx, op, obj(args), self.config.clone(), secrets)
            .await
    }

    pub(crate) async fn must(&self, op: &str, args: Value) -> Map<String, Value> {
        match self.call(op, args.clone()).await {
            Ok(out) => out,
            Err(e) => panic!("{op} {args}: {e}"),
        }
    }
}

// The connector offers exactly the tools of the workspace MCP, so that its pass-through adapter reaches each.
#[test]
fn operations_are_the_tools_of_the_mcp() {
    let info = Connector::new(vec![]).info();
    let mut ops: Vec<String> = info.operations.iter().map(|o| o.name.clone()).collect();
    let mut tools: Vec<String> = goap_mcpbuiltin::workspace_def()
        .tools
        .iter()
        .map(|t| t.name.clone())
        .collect();
    ops.sort();
    tools.sort();
    assert_eq!(ops, tools);
    assert_eq!(
        info.id,
        goap_mcpbuiltin::WORKSPACE_LOCAL,
        "the adapter definition names another connector"
    );
}

#[tokio::test]
async fn needs_a_change() {
    let mut f = new_fixture();
    f.ctx = Ctx::background();
    let err = f.call(OP_LS, Value::Null).await.unwrap_err();
    assert!(err.downcast_ref::<ErrNoChange>().is_some(), "got {err}");
}

// One workspace per change: two changes never see each other's files.
#[tokio::test]
async fn one_workspace_per_change() {
    let f = new_fixture();
    f.must(OP_WRITE, json!({"path": "a.txt", "content": "one"}))
        .await;
    let other = Fixture {
        c: f.c.clone(),
        ctx: with_call(
            &Ctx::background(),
            CallContext {
                change: "CHG-2".into(),
                ..Default::default()
            },
        ),
        config: f.config.clone(),
        dir: f.dir.clone(),
        _root: TempDir::new(),
    };
    assert!(
        other.call(OP_READ, json!({"path": "a.txt"})).await.is_err(),
        "CHG-2 reads the workspace of CHG-1"
    );
    std::fs::metadata(f.dir.join("a.txt")).unwrap();
}

#[tokio::test]
async fn files() {
    let f = new_fixture();
    let out = f
        .must(
            OP_WRITE,
            json!({"path": "/src/pkg/main.go", "content": "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n"}),
        )
        .await;
    assert_eq!(out["created"], true, "write: {out:?}");
    f.must(
        OP_WRITE,
        json!({"path": "src/README.md", "content": "hello world\nHELLO again\n"}),
    )
    .await;

    let out = f
        .must(
            OP_READ,
            json!({"path": "src/pkg/main.go", "offset": 3.0, "limit": 2.0}),
        )
        .await;
    assert!(
        out["text"] == "     3\tfunc main() {\n     4\t\tprintln(\"hello\")\n"
            && out["lines"] == 5.0
            && out["truncated"] == true,
        "read: {out:?}"
    );

    assert!(
        f.call(
            OP_EDIT,
            json!({"path": "src/README.md", "old": "missing", "new": "x"})
        )
        .await
        .is_err(),
        "edit of a missing string"
    );
    f.must(OP_WRITE, json!({"path": "dup.txt", "content": "a a"}))
        .await;
    let err = f
        .call(OP_EDIT, json!({"path": "dup.txt", "old": "a", "new": "b"}))
        .await
        .unwrap_err();
    assert!(err.to_string().contains("2 times"), "ambiguous edit: {err}");
    let out = f
        .must(
            OP_EDIT,
            json!({"path": "dup.txt", "old": "a", "new": "b", "all": true}),
        )
        .await;
    assert_eq!(out["replacements"], 2.0, "edit all: {out:?}");
    f.must(
        OP_EDIT,
        json!({"path": "src/pkg/main.go", "old": "\"hello\"", "new": "\"bye\""}),
    )
    .await;
    let b = std::fs::read_to_string(f.dir.join("src/pkg/main.go")).unwrap();
    assert!(b.contains("\"bye\""), "edited: {b}");

    let out = f.must(OP_LS, json!({"path": "src"})).await;
    assert_eq!(out["entries"].as_array().unwrap().len(), 2, "ls: {out:?}");

    let out = f.must(OP_GLOB, json!({"pattern": "**/*.go"})).await;
    assert_eq!(out["files"], json!(["src/pkg/main.go"]), "glob: {out:?}");
    let out = f
        .must(OP_GLOB, json!({"pattern": "*.md", "path": "src"}))
        .await;
    assert_eq!(
        out["files"],
        json!(["src/README.md"]),
        "glob under a path: {out:?}"
    );

    let out = f
        .must(OP_GREP, json!({"pattern": "hello", "ignoreCase": true}))
        .await;
    assert_eq!(
        out["results"],
        json!(["src/README.md"]),
        "grep files: {out:?}"
    );
    let out = f
        .must(
            OP_GREP,
            json!({"pattern": "hello", "ignoreCase": true, "mode": "content"}),
        )
        .await;
    let r = out["results"].as_array().unwrap();
    assert!(
        r.len() == 1 && r[0]["lines"] == "1:hello world\n2:HELLO again\n",
        "grep content: {out:?}"
    );
    let out = f
        .must(
            OP_GREP,
            json!({"pattern": "func", "glob": "*.go", "mode": "count"}),
        )
        .await;
    let r = out["results"].as_array().unwrap();
    assert!(r.len() == 1 && r[0]["count"] == 1.0, "grep count: {out:?}");
}

// A path never leaves the workspace: ".." stops at its top, and a link out of it is refused.
#[tokio::test]
async fn confined() {
    let f = new_fixture();
    let outside = f.dir.parent().unwrap().join("secret.txt");
    std::fs::write(&outside, "secret").unwrap();
    f.must(
        OP_WRITE,
        json!({"path": "../secret.txt", "content": "inside"}),
    )
    .await;
    assert_eq!(
        std::fs::read_to_string(&outside).unwrap(),
        "secret",
        "a write left the workspace"
    );
    #[cfg(unix)]
    {
        std::os::unix::fs::symlink(&outside, f.dir.join("link")).unwrap();
        assert!(
            f.call(OP_READ, json!({"path": "link"})).await.is_err(),
            "read through a link out of the workspace"
        );
    }
}

fn have(program: &str) -> bool {
    crate::shell::look_path(program).is_some()
}

#[tokio::test]
async fn bash() {
    if !have("bash") {
        eprintln!("no bash");
        return;
    }
    let mut f = new_fixture();
    // a variable of the host the commands must not see (Go set one with t.Setenv; cargo gives the tests this one)
    assert!(std::env::var_os("CARGO_MANIFEST_DIR").is_some());
    f.config.insert("env".into(), json!(["GREETING=hi"]));
    f.must(OP_WRITE, json!({"path": "sub/x.txt", "content": "x"}))
        .await;
    let out = f
        .must(
            OP_BASH,
            json!({"command": "pwd; ls; echo $GREETING; echo ${CARGO_MANIFEST_DIR:-none}; echo oops >&2; exit 3", "cwd": "sub"}),
        )
        .await;
    let stdout = out["stdout"].as_str().unwrap();
    let lines: Vec<&str> = stdout.trim().split('\n').collect();
    assert!(
        out["exitCode"] == 3.0
            && lines.len() == 4
            && lines[0].ends_with(&format!("CHG-1{}sub", std::path::MAIN_SEPARATOR))
            && lines[1] == "x.txt"
            && lines[2] == "hi"
            && lines[3] == "none"
            && out["stderr"] == "oops\n",
        "bash: {out:?}"
    );
    // the secret of the adapter never reaches a command
    let out = f.must(OP_BASH, json!({"command": "env"})).await;
    assert!(
        !out["stdout"].as_str().unwrap().contains("s3cr3t"),
        "the git token reached a command"
    );

    let out = f
        .must(OP_BASH, json!({"command": "sleep 5", "timeout": 1.0}))
        .await;
    assert_eq!(out["timedOut"], true, "timeout: {out:?}");

    let out = f
        .must(
            OP_BASH,
            json!({"command": "echo started; sleep 0.3; echo finished", "background": true}),
        )
        .await;
    let id = out["id"].as_str().unwrap().to_string();
    let deadline = Instant::now() + Duration::from_secs(10);
    let mut text = String::new();
    let out = loop {
        let out = f.must(OP_BASH_OUTPUT, json!({ "id": id })).await;
        text += out["output"].as_str().unwrap();
        if out["running"] == false || Instant::now() > deadline {
            break out;
        }
        tokio::time::sleep(Duration::from_millis(50)).await;
    };
    assert!(
        text == "started\nfinished\n" && out["exitCode"] == 0.0,
        "background: {text:?} {out:?}"
    );

    let out = f
        .must(OP_BASH, json!({"command": "sleep 30", "background": true}))
        .await;
    f.must(OP_BASH_KILL, json!({"id": out["id"]})).await;
    let status = f.must(OP_STATUS, Value::Null).await;
    assert_eq!(
        status["commands"].as_array().unwrap().len(),
        2,
        "status: {status:?}"
    );
    let closed = f.must(OP_CLOSE, Value::Null).await;
    assert_eq!(closed["closed"], true, "close: {closed:?}");
    assert!(
        std::fs::metadata(&f.dir).is_err(),
        "the directory of the workspace is still there"
    );
}

fn run(dir: &Path, args: &[&str]) -> String {
    let out = std::process::Command::new("git")
        .args(args)
        .current_dir(dir)
        .env("GIT_AUTHOR_NAME", "t")
        .env("GIT_AUTHOR_EMAIL", "t@t")
        .env("GIT_COMMITTER_NAME", "t")
        .env("GIT_COMMITTER_EMAIL", "t@t")
        .output()
        .unwrap();
    assert!(
        out.status.success(),
        "git {args:?}: {}{}",
        String::from_utf8_lossy(&out.stdout),
        String::from_utf8_lossy(&out.stderr)
    );
    String::from_utf8_lossy(&out.stdout).trim().to_string()
}

#[tokio::test]
async fn sources() {
    if !have("git") || !have("bash") {
        eprintln!("no git or no bash");
        return;
    }
    let f = new_fixture();
    // a remote: a bare repository holding one commit on main
    let remotes = TempDir::new();
    let remote = remotes.path().join("app.git");
    let remote_s = remote.to_string_lossy().into_owned();
    run(
        remotes.path(),
        &[
            "init",
            "--quiet",
            "--bare",
            "--initial-branch=main",
            &remote_s,
        ],
    );
    let seed = TempDir::new();
    run(seed.path(), &["clone", "--quiet", &remote_s, "."]);
    std::fs::write(seed.path().join("main.go"), "package main\n").unwrap();
    run(seed.path(), &["add", "."]);
    run(seed.path(), &["commit", "--quiet", "-m", "first"]);
    run(seed.path(), &["push", "--quiet", "origin", "HEAD:main"]);

    let out = f
        .must(
            OP_OPEN,
            json!({"sources": [{"repo": remote_s, "ref": "main"}]}),
        )
        .await;
    let src = out["sources"].as_array().unwrap();
    assert!(
        src.len() == 1 && src[0]["path"] == "app" && src[0]["branch"] == "goap/CHG-1",
        "open: {out:?}"
    );
    // opening again keeps the work
    f.must(
        OP_WRITE,
        json!({"path": "app/main.go", "content": "package main\n\nfunc main() {}\n"}),
    )
    .await;
    f.must(OP_OPEN, json!({"sources": [{"repo": remote_s}]}))
        .await;
    f.must(
        OP_BASH,
        json!({"command": "git commit --quiet -am 'implement' && echo new > added.txt", "cwd": "app"}),
    )
    .await;

    let out = f.must(OP_DIFF, Value::Null).await;
    let d = &out["sources"][0];
    assert!(
        d["diff"].as_str().unwrap().contains("+func main() {}")
            && d["status"].as_str().unwrap().contains("?? added.txt"),
        "diff: {out:?}"
    );

    f.must(OP_PUSH, json!({"path": "app"})).await;
    let got = run(seed.path(), &["ls-remote", "origin", "goap/CHG-1"]);
    assert!(got.contains("refs/heads/goap/CHG-1"), "pushed: {got:?}");
    f.must(
        OP_BASH,
        json!({"command": "git checkout --quiet -b other", "cwd": "app"}),
    )
    .await;
    assert!(
        f.call(OP_PUSH, json!({"path": "app"})).await.is_err(),
        "pushed a branch that is not the change's"
    );
}

#[test]
fn match_() {
    for (pattern, name, want) in [
        ("**/*.go", "main.go", true),
        ("**/*.go", "a/b/main.go", true),
        ("*.go", "a/main.go", false),
        ("src/**", "src/a/b", true),
        ("src/**/x", "src/x", true),
        ("a/*/c", "a/b/c", true),
        ("a/*/c", "a/b/d/c", false),
    ] {
        assert_eq!(
            crate::files::match_glob(pattern, name),
            want,
            "match({pattern:?}, {name:?})"
        );
    }
}

#[test]
fn cap_output() {
    let long = format!("{}{}", "a".repeat(MAX_OUTPUT), "z".repeat(100));
    let got = crate::shell::cap_output(long.as_bytes());
    assert!(
        got.len() <= MAX_OUTPUT + 100
            && got.ends_with(&"z".repeat(100))
            && got.contains("bytes cut"),
        "cap: {} bytes",
        got.len()
    );
}
