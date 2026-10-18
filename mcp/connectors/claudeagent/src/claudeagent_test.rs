use std::collections::HashMap;
use std::path::PathBuf;
use std::time::{Duration, Instant};

use goap_connect::Ctx;
use goap_connector_workspace::dir_name;
use goap_connectorkit::Connector as _;
use goap_mcp::{CallContext, with_call};
use serde_json::{Map, Value, json};

use crate::*;

/// stubSDK stands for the Claude Agent SDK: query() plays a session according to its prompt, calling canUseTool the
/// way the SDK does, so the runner's protocol is tested without a model.
const STUB_SDK: &str = r#"
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
    key: process.env.ANTHROPIC_API_KEY ?? '', base: process.env.ANTHROPIC_BASE_URL ?? '', secretLeak: process.env.CARGO_MANIFEST_DIR ?? '',
    allowed: options.allowedTools ?? [], mode: options.permissionMode, settings: options.settingSources,
    maxTurns: options.maxTurns, append: options.systemPrompt.append.length > 0 }));
}
"#;

/// A temporary directory removed on drop (`t.TempDir`).
struct TempDir(PathBuf);

impl TempDir {
    fn new() -> Self {
        use std::sync::atomic::{AtomicU64, Ordering};
        static SEQ: AtomicU64 = AtomicU64::new(0);
        let p = std::env::temp_dir().join(format!(
            "goap-claudeagent-{}-{}-{}",
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
}

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

struct Fixture {
    c: Connector,
    root: PathBuf,
    ws: PathBuf,
    ctx: Ctx,
    _dirs: Vec<TempDir>,
}

fn obj(v: Value) -> Map<String, Value> {
    match v {
        Value::Object(m) => m,
        _ => Map::new(),
    }
}

fn setup() -> Option<Fixture> {
    let Some(node) = goap_connector_workspace_look_path("node") else {
        eprintln!("node is not installed");
        return None;
    };
    let (dir, stubs, root) = (TempDir::new(), TempDir::new(), TempDir::new());
    let stub = stubs.0.join("sdk.mjs");
    std::fs::write(&stub, STUB_SDK).unwrap();
    let mut c = Connector::new(dir.0.join("runner"), vec![]);
    c.node = node.to_string_lossy().into_owned();
    c.sdk_module = format!("file://{}", stub.to_string_lossy().replace('\\', "/"));
    let ws = root.0.join(dir_name("CH-1"));
    std::fs::create_dir_all(ws.join("svc")).unwrap();
    Some(Fixture {
        c,
        root: root.0.clone(),
        ws,
        ctx: with_call(
            &Ctx::background(),
            CallContext {
                change: "CH-1".into(),
                ..Default::default()
            },
        ),
        _dirs: vec![dir, stubs, root],
    })
}

/// `exec.LookPath`.
fn goap_connector_workspace_look_path(name: &str) -> Option<PathBuf> {
    let path = std::env::var_os("PATH")?;
    std::env::split_paths(&path)
        .map(|d| d.join(name))
        .find(|p| p.is_file())
}

impl Fixture {
    fn config(&self) -> Map<String, Value> {
        obj(
            json!({"root": self.root.to_string_lossy(), "baseURL": "http://gateway/anthropic", "allowedTools": ["Read"]}),
        )
    }

    async fn call(&self, op: &str, args: Value) -> Map<String, Value> {
        let secrets = HashMap::from([(SECRET_API_KEY.to_string(), "sk-test".to_string())]);
        self.c
            .invoke(&self.ctx, op, obj(args), self.config(), secrets)
            .await
            .unwrap()
    }
}

fn played(out: &Map<String, Value>) -> Map<String, Value> {
    let s = out["summary"].as_str().unwrap_or("");
    match serde_json::from_str::<Value>(s) {
        Ok(Value::Object(m)) => m,
        r => panic!("summary {:?}: {r:?}", out.get("summary")),
    }
}

fn to_strings(v: &Value) -> Vec<String> {
    v.as_array()
        .unwrap()
        .iter()
        .map(|x| x.as_str().unwrap().to_string())
        .collect()
}

#[tokio::test]
async fn run_done() {
    let Some(f) = setup() else { return };
    let out = f
        .call(
            OP_RUN,
            json!({"task": "implement", "path": "svc", "maxTurns": 12.0}),
        )
        .await;
    assert!(
        out["status"] == STATUS_DONE
            && out["sessionId"] == "sess-1"
            && out["turns"] == 3
            && out["costUsd"] == 0.25,
        "outcome {out:?}"
    );
    assert_eq!(out["tools"]["Edit"], 1, "tools {:?}", out["tools"]);
    let p = played(&out);
    assert!(
        p["cwd"] == json!(f.ws.join("svc").to_string_lossy())
            && p["key"] == "sk-test"
            && p["base"] == "http://gateway/anthropic",
        "session {p:?}"
    );
    // a host variable not passed (Go set one with t.Setenv; cargo gives the tests this one)
    assert_eq!(
        p["secretLeak"], "",
        "a host variable not passed reached the session"
    );
    assert!(
        p["mode"] == "acceptEdits"
            && p["maxTurns"] == 12
            && p["append"] == true
            && to_strings(&p["settings"]).join(",") == "project",
        "options {p:?}"
    );
    let st = f.call(OP_STATUS, Value::Null).await;
    assert!(
        st["running"] == false && st["last"]["status"] == STATUS_DONE,
        "status {st:?}"
    );
}

#[tokio::test]
async fn questions_and_approvals_end_the_turn() {
    let Some(f) = setup() else { return };
    let out = f.call(OP_RUN, json!({"task": "ask first"})).await;
    let qs = out["questions"].as_array().cloned().unwrap_or_default();
    assert!(
        out["status"] == STATUS_NEEDS_INPUT
            && qs.len() == 1
            && qs[0]["question"] == "Which library?",
        "question {out:?}"
    );
    let out = f.call(OP_RUN, json!({"task": "bash it"})).await;
    assert!(
        out["status"] == STATUS_NEEDS_APPROVAL && out["permission"]["tool"] == "Bash",
        "approval {out:?}"
    );
    let out = f.call(OP_RUN, json!({"task": "turns"})).await;
    assert_eq!(out["status"], STATUS_BUDGET, "budget {out:?}");
}

#[tokio::test]
async fn answer_resumes_the_session() {
    let Some(f) = setup() else { return };
    let out = f
        .call(
            OP_ANSWER,
            json!({"sessionId": "sess-9", "answers": {"Which library?": "b"},
                "allow": ["Bash(npm test:*)"], "answer": "keep the API"}),
        )
        .await;
    let p = played(&out);
    let prompt = p["prompt"].as_str().unwrap_or("");
    assert!(
        out["sessionId"] == "sess-9"
            && p["resume"] == "sess-9"
            && prompt.contains("Which library? → b")
            && prompt.contains("keep the API"),
        "resume {p:?}"
    );
    assert_eq!(
        to_strings(&p["allowed"]).join(","),
        "Read,Bash(npm test:*)",
        "allowed {:?}",
        p["allowed"]
    );
    assert!(
        f.c.invoke(
            &f.ctx,
            OP_ANSWER,
            obj(json!({"sessionId": "sess-9"})),
            f.config(),
            HashMap::new()
        )
        .await
        .is_err(),
        "an answer with nothing to say"
    );
}

#[tokio::test(flavor = "multi_thread")]
async fn one_session_per_change_and_cancel() {
    let Some(f) = setup() else { return };
    let (c, ctx, config) = (f.c.clone(), f.ctx.clone(), f.config());
    let done = tokio::spawn(async move {
        c.invoke(
            &ctx,
            OP_RUN,
            obj(json!({"task": "sleep"})),
            config,
            HashMap::new(),
        )
        .await
    });
    let deadline = Instant::now() + Duration::from_secs(10);
    while f.call(OP_STATUS, Value::Null).await["running"] != true {
        assert!(Instant::now() < deadline, "the session never ran");
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
    let err =
        f.c.invoke(
            &f.ctx,
            OP_RUN,
            obj(json!({"task": "implement"})),
            f.config(),
            HashMap::new(),
        )
        .await
        .unwrap_err();
    assert_eq!(
        err.downcast_ref::<Error>(),
        Some(&Error::Busy),
        "second session: {err}"
    );
    let c = f.call(OP_CANCEL, Value::Null).await;
    assert_eq!(c["cancelled"], true, "cancel {c:?}");
    let out = tokio::time::timeout(Duration::from_secs(20), done)
        .await
        .expect("the cancelled session did not stop")
        .unwrap()
        .unwrap();
    assert_eq!(out["status"], STATUS_CANCELLED, "cancelled run {out:?}");
}

#[tokio::test]
async fn refusals() {
    let Some(f) = setup() else { return };
    let err =
        f.c.invoke(
            &Ctx::background(),
            OP_RUN,
            obj(json!({"task": "x"})),
            f.config(),
            HashMap::new(),
        )
        .await
        .unwrap_err();
    assert_eq!(
        err.downcast_ref::<Error>(),
        Some(&Error::NoChange),
        "no change: {err}"
    );
    let other = with_call(
        &Ctx::background(),
        CallContext {
            change: "CH-2".into(),
            ..Default::default()
        },
    );
    let err =
        f.c.invoke(
            &other,
            OP_RUN,
            obj(json!({"task": "x"})),
            f.config(),
            HashMap::new(),
        )
        .await
        .unwrap_err();
    assert!(
        err.to_string().contains("workspace/open"),
        "no workspace: {err}"
    );
    let r =
        f.c.invoke(
            &f.ctx,
            OP_RUN,
            obj(json!({"task": "x", "path": "../.."})),
            f.config(),
            HashMap::new(),
        )
        .await;
    assert!(r.is_ok(), "a path is confined to the workspace: {r:?}");
    let tmp = TempDir::new();
    let restricted = Connector::new(tmp.0.clone(), vec!["/somewhere/else".into()]);
    let err = restricted
        .invoke(
            &f.ctx,
            OP_RUN,
            obj(json!({"task": "x"})),
            f.config(),
            HashMap::new(),
        )
        .await
        .unwrap_err();
    assert!(
        err.to_string().contains("not allowed"),
        "root outside the allowed roots: {err}"
    );
    let real_dir = TempDir::new();
    let real = Connector::new(real_dir.0.clone(), vec![]);
    let err = real
        .invoke(
            &f.ctx,
            OP_RUN,
            obj(json!({"task": "x"})),
            f.config(),
            HashMap::new(),
        )
        .await
        .unwrap_err();
    assert!(
        matches!(err.downcast_ref::<Error>(), Some(Error::NoRunner(_))),
        "SDK not installed: {err}"
    );
    assert!(
        real.dir.join("runner.mjs").is_file(),
        "the runner is written before the SDK is checked"
    );
}

// When the hub gives the call an endpoint of the model gateway (ADR 0123 §6), the session calls the model there with
// the token of the call, whatever the adapter's key and endpoint.
#[tokio::test]
async fn the_gateway_endpoint_wins() {
    let Some(mut f) = setup() else { return };
    f.ctx = with_call(
        &f.ctx,
        CallContext {
            model_url: "http://modelgw/anthropic".into(),
            model_token: "tok-1".into(),
            ..Default::default()
        },
    );
    let p = played(&f.call(OP_RUN, json!({"task": "implement"})).await);
    assert!(
        p["key"] == "tok-1" && p["base"] == "http://modelgw/anthropic",
        "session {p:?}"
    );
}
