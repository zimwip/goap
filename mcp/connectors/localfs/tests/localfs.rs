#![cfg(unix)]

use std::path::{Path, PathBuf};

use goap_connect::Ctx;
use goap_connector_localfs::Connector;
use goap_connectorkit::Connector as _;
use serde_json::{Map, Value, json};

/// A temporary directory removed on drop (`t.TempDir`).
struct TempDir(PathBuf);

impl TempDir {
    fn new(tag: &str) -> Self {
        let p = std::env::temp_dir().join(format!(
            "goap-localfs-{tag}-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        std::fs::create_dir_all(&p).unwrap();
        TempDir(p)
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

fn obj(v: Value) -> Map<String, Value> {
    v.as_object().cloned().unwrap_or_default()
}

fn cfg(dir: &Path) -> Map<String, Value> {
    obj(json!({"root": dir.to_string_lossy()}))
}

#[tokio::test]
async fn local_fs() {
    let dir = TempDir::new("root");
    let cfg = cfg(&dir.0);
    let c = Connector::default();
    let ctx = Ctx::background();
    let call = |c: &Connector, op: &'static str, args: Value, cfg: Map<String, Value>| {
        let c = c.clone();
        let ctx = ctx.clone();
        async move { c.invoke(&ctx, op, obj(args), cfg, Default::default()).await }
    };

    call(
        &c,
        "write_file",
        json!({"path": "docs/a.txt", "content": "hello"}),
        cfg.clone(),
    )
    .await
    .unwrap();
    let got = call(&c, "read_file", json!({"path": "docs/a.txt"}), cfg.clone()).await;
    assert!(
        matches!(&got, Ok(m) if m["text"] == "hello"),
        "read = {got:?}"
    );
    let ls = call(&c, "list_dir", json!({"path": "docs"}), cfg.clone()).await;
    assert!(
        matches!(&ls, Ok(m) if m["entries"].as_array().unwrap().len() == 1),
        "list = {ls:?}"
    );
    let root = call(&c, "list_dir", json!({}), cfg.clone()).await;
    assert!(
        matches!(&root, Ok(m) if m["entries"].as_array().unwrap().len() == 1),
        "list root = {root:?}"
    );

    // confinement: traversal and symlinks cannot leave the root
    let outside = TempDir::new("outside");
    std::fs::write(outside.0.join("secret"), "x").unwrap();
    std::os::unix::fs::symlink(&outside.0, dir.0.join("link")).unwrap();
    let base = outside
        .0
        .file_name()
        .unwrap()
        .to_string_lossy()
        .into_owned();
    for p in [
        "link/secret".to_string(),
        format!("../{base}/secret"),
        "/../../etc/passwd".to_string(),
    ] {
        let got = call(&c, "read_file", json!({ "path": p }), cfg.clone()).await;
        assert!(got.is_err(), "{p} escaped the root: {got:?}");
    }
    let w = call(
        &c,
        "write_file",
        json!({"path": "link/new", "content": "x"}),
        cfg.clone(),
    )
    .await;
    assert!(w.is_err(), "write through a symlink escaped the root");
    assert!(!outside.0.join("new").exists());

    assert!(
        call(&c, "list_dir", Value::Null, Map::new()).await.is_err(),
        "missing root accepted"
    );
    let docs = dir.0.join("docs");
    let restricted = Connector {
        roots: vec![docs.to_string_lossy().into_owned()],
    };
    assert!(
        call(&restricted, "list_dir", Value::Null, cfg.clone())
            .await
            .is_err(),
        "root outside the allowed ones accepted"
    );
    let allowed = call(&restricted, "list_dir", Value::Null, self::cfg(&docs)).await;
    assert!(allowed.is_ok(), "allowed root refused: {allowed:?}");
}
