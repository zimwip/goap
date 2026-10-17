//! The connector of the claude-coding-agent MCP (`claudeagent.go`).

use std::collections::{BTreeMap, HashMap};
use std::path::{Path, PathBuf};
use std::process::Stdio;
use std::sync::Arc;
use std::time::Duration;

use chrono::{DateTime, SecondsFormat, Utc};
use goap_connect::{Ctx, async_trait};
use goap_connector_workspace::{HOST_ENV, abs, dir_name, within};
use goap_connectorkit::{Connector as KitConnector, ErrUnknownOperation, InvokeError};
use goap_ctx::{CancelHandle, CtxError};
use goap_gen::connector::v1::{ConnectorInfo, Operation};
use goap_mcp::CallContext;
use serde::Serialize;
use serde_json::{Map, Value, json};
use tokio::io::{AsyncBufReadExt, AsyncReadExt, AsyncWriteExt, BufReader};
use tokio::process::{Child, Command};
use tokio::sync::OnceCell;

/// ID is the identifier of the connector (and of the MCP and the adapter definition, `mcpbuiltin::CLAUDE_AGENT`).
pub const ID: &str = "claude-coding-agent";

/// SecretAPIKey is the Anthropic API key the sessions call the model with.
pub const SECRET_API_KEY: &str = "api_key";

/// SDKPackage is the package the runner needs in its directory.
pub const SDK_PACKAGE: &str = "@anthropic-ai/claude-agent-sdk";

// Bounds of a run.
pub const DEFAULT_MAX_TURNS: f64 = 200.0;
pub const DEFAULT_TIMEOUT: Duration = Duration::from_secs(60 * 60);
pub const MAX_TIMEOUT: Duration = Duration::from_secs(240 * 60);
/// MaxSummary bounds the summary given back (the head and the tail are kept).
pub const MAX_SUMMARY: usize = 20000;

// Statuses of a run.
pub const STATUS_DONE: &str = "done";
pub const STATUS_NEEDS_INPUT: &str = "needs_input";
pub const STATUS_NEEDS_APPROVAL: &str = "needs_approval";
pub const STATUS_BUDGET: &str = "budget";
pub const STATUS_FAILED: &str = "failed";
pub const STATUS_CANCELLED: &str = "cancelled";

// Operations of the connector: the tools of the MCP, one for one.
pub const OP_RUN: &str = "run";
pub const OP_ANSWER: &str = "answer";
pub const OP_STATUS: &str = "status";
pub const OP_CANCEL: &str = "cancel";

/// The errors of the connector a caller may tell apart (Go's `ErrNoChange`, `ErrBusy`, `ErrNoRunner`).
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum Error {
    /// ErrNoChange: a call that runs for no change; a session works in the workspace of a change.
    #[error("the Claude coding agent works for a change: this call runs for none")]
    NoChange,
    /// ErrBusy: a session already runs for the change.
    #[error("a Claude Code session already runs for this change: wait for it or cancel it")]
    Busy,
    /// ErrNoRunner: the SDK is not installed in the runner directory (the directory to `npm install` in).
    #[error(
        "the Claude Agent SDK is not installed for this connector: run `npm install` in {0} (make claude-agent-runner)"
    )]
    NoRunner(String),
}

/// The runner script, written into the runner directory.
pub const RUNNER_SCRIPT: &[u8] = include_bytes!("../runner/runner.mjs");

/// The package of the runner (the SDK pinned), written into the runner directory.
pub const RUNNER_PACKAGE: &[u8] = include_bytes!("../runner/package.json");

/// Connector implements goap_connectorkit::Connector. Use [`Connector::new`]: it keeps the sessions running per
/// change (`Connector::default()` refuses every call, as Go's zero value).
#[derive(Clone, Default)]
pub struct Connector {
    /// Dir is the runner directory: runner.mjs and package.json are written there, the SDK installed there.
    pub dir: PathBuf,
    /// Node is the JavaScript runtime (default "node").
    pub node: String,
    /// Roots, when not empty, restricts the "root" a unit may configure to these directories or below.
    pub roots: Vec<String>,
    /// replaces the SDK in the runner (tests)
    pub(crate) sdk_module: String,
    st: Option<Arc<State>>,
}

impl std::fmt::Debug for Connector {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Connector")
            .field("dir", &self.dir)
            .field("node", &self.node)
            .field("roots", &self.roots)
            .finish()
    }
}

/// New returns a connector whose runner lives in dir, restricted to roots (none: any root).
pub fn new(dir: impl Into<PathBuf>, roots: Vec<String>) -> Connector {
    Connector::new(dir, roots)
}

impl Connector {
    /// See [`new`].
    pub fn new(dir: impl Into<PathBuf>, roots: Vec<String>) -> Connector {
        Connector {
            dir: dir.into(),
            node: "node".into(),
            roots,
            sdk_module: String::new(),
            st: Some(Arc::new(State::default())),
        }
    }
}

/// Install writes the runner into dir (runner.mjs, package.json): `npm install` there then installs the SDK.
pub fn install(dir: &Path) -> std::io::Result<()> {
    std::fs::create_dir_all(dir)?;
    for (name, data) in [
        ("runner.mjs", RUNNER_SCRIPT),
        ("package.json", RUNNER_PACKAGE),
    ] {
        let p = dir.join(name);
        if std::fs::read(&p).is_ok_and(|cur| cur == data) {
            continue;
        }
        std::fs::write(&p, data)?;
    }
    Ok(())
}

/// What the connector keeps per change: the session running and the outcome of the latest run.
#[derive(Default)]
struct State {
    inner: parking_lot::Mutex<StateInner>,
    install: OnceCell<Result<(), String>>,
}

#[derive(Default)]
struct StateInner {
    running: HashMap<String, Session>,
    last: HashMap<String, Map<String, Value>>,
}

struct Session {
    id: String,
    started: DateTime<Utc>,
    cancel: CancelHandle,
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
    json!({"type": "number", "description": desc})
}

fn strs(desc: &str) -> Value {
    json!({"type": "array", "items": {"type": "string"}, "description": desc})
}

fn op(name: &str, desc: &str, input: Value) -> Operation {
    Operation {
        name: name.into(),
        description: desc.into(),
        input_schema: schema(input),
    }
}

fn to_map(v: Value) -> Map<String, Value> {
    match v {
        Value::Object(m) => m,
        _ => Map::new(),
    }
}

fn now_rfc3339() -> String {
    Utc::now().to_rfc3339_opts(SecondsFormat::Secs, true)
}

/// What the runner reads on its standard input.
#[derive(Debug, Clone, Default, Serialize)]
#[serde(rename_all = "camelCase")]
struct RunRequest {
    prompt: String,
    cwd: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    resume: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    model: String,
    #[serde(skip_serializing_if = "is_zero_i64")]
    max_turns: i64,
    #[serde(skip_serializing_if = "is_zero_f64")]
    max_budget_usd: f64,
    #[serde(skip_serializing_if = "String::is_empty")]
    permission_mode: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    allowed_tools: Option<Vec<String>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    disallowed_tools: Option<Vec<String>>,
    /// always sent (Go: `null` when nil, never nil here)
    setting_sources: Vec<String>,
}

fn is_zero_i64(v: &i64) -> bool {
    *v == 0
}

fn is_zero_f64(v: &f64) -> bool {
    *v == 0.0
}

/// `omitempty` of a Go slice: None when empty.
fn non_empty(v: Option<Vec<String>>) -> Option<Vec<String>> {
    v.filter(|l| !l.is_empty())
}

impl Connector {
    fn info_impl(&self) -> ConnectorInfo {
        ConnectorInfo {
            id: ID.into(),
            version: "1".into(),
            description:
                "Claude Code sessions (Claude Agent SDK) in the workspaces of the changes, on this host."
                    .into(),
            config_schema: schema(json!({"type": "object", "required": ["root"], "properties": {
                "root":            str_("directory holding one workspace per change"),
                "model":           str_("model of the sessions"),
                "permissionMode":  str_("permission mode of the sessions (default acceptEdits)"),
                "allowedTools":    strs("tools allowed without asking"),
                "disallowedTools": strs("tools removed from the sessions"),
                "maxTurns":        num("turns of a run (default 200)"),
                "maxBudgetUsd":    num("budget of a run in USD"),
                "timeoutMinutes":  num("time of a run in minutes (default 60, at most 240)"),
                "settingSources":  strs("Claude Code settings loaded (default project)"),
                "baseURL":         str_("Anthropic API endpoint (ANTHROPIC_BASE_URL)"),
                "env":             strs("variables set for the sessions (NAME=value)"),
                "passEnv":         strs("variables of the host environment given to the sessions"),
            }})),
            secret_names: vec![SECRET_API_KEY.into()],
            calls_models: true,
            operations: vec![
                op(
                    OP_RUN,
                    "Run a coding task with Claude Code in the workspace of the change",
                    obj(
                        json!({
                            "task": str_("instructions of this turn"), "sessionId": str_("session to resume"),
                            "path": str_("directory of the workspace to work in"),
                            "model": str_("model of the session"), "maxTurns": num("turns of this run"),
                            "maxBudgetUsd": num("budget of this run in USD")}),
                        &["task"],
                    ),
                ),
                op(
                    OP_ANSWER,
                    "Resume a session waiting for a person with their decision",
                    obj(
                        json!({
                            "sessionId": str_("session to resume"), "answer": str_("free answer or instruction"),
                            "answers": {"type": "object", "description": "answers by question", "additionalProperties": {"type": "string"}},
                            "allow": strs("tools allowed for this run"), "maxTurns": num("turns of this run")}),
                        &["sessionId"],
                    ),
                ),
                op(
                    OP_STATUS,
                    "The session of the change running now and the outcome of the latest one",
                    obj(json!({}), &[]),
                ),
                op(
                    OP_CANCEL,
                    "Stop the session of the change running now",
                    obj(json!({}), &[]),
                ),
            ],
        }
    }

    fn state(&self) -> Result<&Arc<State>, InvokeError> {
        self.st.as_ref().ok_or_else(|| {
            "claude-coding-agent connector not initialised (use claudeagent::new)".into()
        })
    }

    fn status(&self, st: &State, change: &str) -> Map<String, Value> {
        let s = st.inner.lock();
        let mut out = to_map(json!({"running": false}));
        if let Some(sess) = s.running.get(change) {
            out.insert("running".into(), json!(true));
            out.insert("sessionId".into(), json!(sess.id));
            out.insert(
                "startedAt".into(),
                json!(sess.started.to_rfc3339_opts(SecondsFormat::Secs, true)),
            );
        }
        if let Some(l) = s.last.get(change) {
            out.insert("last".into(), Value::Object(l.clone()));
        }
        out
    }

    fn cancel(&self, st: &State, change: &str) -> Map<String, Value> {
        let s = st.inner.lock();
        match s.running.get(change) {
            None => to_map(json!({"cancelled": false})),
            Some(sess) => {
                sess.cancel.cancel();
                to_map(json!({"cancelled": true, "sessionId": sess.id}))
            }
        }
    }

    async fn run(
        &self,
        ctx: &Ctx,
        op: &str,
        change: &str,
        args: &Map<String, Value>,
        config: &Map<String, Value>,
        secrets: &HashMap<String, String>,
    ) -> Result<Map<String, Value>, InvokeError> {
        let st = self.state()?;
        let dir = self.workspace(change, config, string_arg(args, "path"))?;
        let mut req = RunRequest {
            cwd: dir.to_string_lossy().into_owned(),
            resume: string_arg(args, "sessionId").into(),
            model: first_string(args, config, "model").into(),
            max_turns: first_number(Some(args), config, "maxTurns", DEFAULT_MAX_TURNS) as i64,
            max_budget_usd: first_number(Some(args), config, "maxBudgetUsd", 0.0),
            permission_mode: string_arg(config, "permissionMode").into(),
            allowed_tools: strings1(config, "allowedTools"),
            disallowed_tools: non_empty(strings1(config, "disallowedTools")),
            setting_sources: strings1(config, "settingSources").unwrap_or_default(),
            ..Default::default()
        };
        if strings1(config, "settingSources").is_none() {
            req.setting_sources = vec!["project".into()];
        }
        if op == OP_RUN {
            req.prompt = string_arg(args, "task").into();
            if req.prompt.trim().is_empty() {
                return Err("run: task is empty".into());
            }
        } else {
            if req.resume.is_empty() {
                return Err("answer: sessionId is required".into());
            }
            req.prompt = answer_prompt(args);
            if req.prompt.is_empty() {
                return Err("answer: give an answer, answers or allowed tools".into());
            }
            let mut allowed = req.allowed_tools.take().unwrap_or_default();
            allowed.extend(strings1(args, "allow").unwrap_or_default());
            req.allowed_tools = Some(allowed);
        }
        req.allowed_tools = non_empty(req.allowed_tools);
        self.ready(st).await?;
        let minutes = first_number(None, config, "timeoutMinutes", 0.0);
        let timeout = if minutes > 0.0 {
            Duration::from_secs_f64(minutes * 60.0)
        } else {
            DEFAULT_TIMEOUT
        }
        .min(MAX_TIMEOUT);
        let (run_ctx, cancel) = ctx.without_cancel().with_timeout(timeout);
        // the caller giving up stops the session
        let stop = {
            let ctx = ctx.clone();
            let cancel = cancel.clone();
            tokio::spawn(async move {
                ctx.done().await;
                cancel.cancel();
            })
        };
        {
            let mut s = st.inner.lock();
            if s.running.contains_key(change) {
                stop.abort();
                return Err(Box::new(Error::Busy));
            }
            s.running.insert(
                change.to_string(),
                Session {
                    id: req.resume.clone(),
                    started: Utc::now(),
                    cancel: cancel.clone(),
                },
            );
        }
        let env = self.environment(&goap_mcp::call_from(ctx), &dir, config, secrets);
        let st2 = st.clone();
        let change2 = change.to_string();
        let res = self
            .exec(&run_ctx, &req, env, move |id| {
                if let Some(sess) = st2.inner.lock().running.get_mut(&change2) {
                    sess.id = id.to_string();
                }
            })
            .await;
        let run_err = run_ctx.err();
        stop.abort();
        cancel.cancel();
        st.inner.lock().running.remove(change);
        let mut out = res?;
        let status = out.get("status").and_then(Value::as_str).unwrap_or("");
        if let Some(e) = run_err
            && (status == STATUS_FAILED || status == STATUS_CANCELLED)
        {
            if e == CtxError::DeadlineExceeded {
                out.insert("status".into(), json!(STATUS_BUDGET));
                out.insert(
                    "errors".into(),
                    json!([format!(
                        "the run took more than {}; resume the session to go on",
                        go_duration(timeout)
                    )]),
                );
            } else {
                out.insert("status".into(), json!(STATUS_CANCELLED));
            }
        }
        st.inner.lock().last.insert(
            change.to_string(),
            to_map(
                json!({"status": out.get("status").cloned().unwrap_or(Value::Null),
                "sessionId": out.get("sessionId").cloned().unwrap_or(Value::Null),
                "endedAt": now_rfc3339()}),
            ),
        );
        Ok(out)
    }

    /// ready writes the runner once and checks that the SDK is installed (unless the tests replace it).
    async fn ready(&self, st: &State) -> Result<(), InvokeError> {
        let r = st
            .install
            .get_or_init(|| async { install(&self.dir).map_err(|e| e.to_string()) })
            .await;
        if let Err(e) = r {
            return Err(e.clone().into());
        }
        if !self.sdk_module.is_empty() {
            return Ok(());
        }
        let mut p = self.dir.join("node_modules");
        for part in SDK_PACKAGE.split('/') {
            p.push(part);
        }
        if std::fs::metadata(p.join("package.json")).is_err() {
            return Err(Box::new(Error::NoRunner(
                self.dir.to_string_lossy().into_owned(),
            )));
        }
        Ok(())
    }

    /// exec runs the runner for one turn: the request on its standard input, the outcome on the last line of its
    /// output.
    async fn exec(
        &self,
        ctx: &Ctx,
        req: &RunRequest,
        env: Vec<String>,
        on_session: impl Fn(&str),
    ) -> Result<Map<String, Value>, InvokeError> {
        let input = serde_json::to_vec(req)?;
        let node = if self.node.is_empty() {
            "node"
        } else {
            &self.node
        };
        let mut cmd = Command::new(node);
        cmd.arg(self.dir.join("runner.mjs"))
            .current_dir(&req.cwd)
            .env_clear()
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .kill_on_drop(true);
        for kv in &env {
            if let Some((k, v)) = kv.split_once('=') {
                cmd.env(k, v);
            }
        }
        let mut child = cmd
            .spawn()
            .map_err(|e| -> InvokeError { format!("start the runner: {e}").into() })?;
        if let Some(mut stdin) = child.stdin.take() {
            tokio::spawn(async move {
                let _ = stdin.write_all(&input).await;
            });
        }
        let stderr = child.stderr.take().map(|mut r| {
            tokio::spawn(async move {
                let mut tail = Tail::default();
                let mut buf = vec![0u8; 8192];
                loop {
                    match r.read(&mut buf).await {
                        Ok(0) | Err(_) => return tail,
                        Ok(n) => tail.write(&buf[..n]),
                    }
                }
            })
        });
        let mut result: Option<Map<String, Value>> = None;
        if let Some(stdout) = child.stdout.take() {
            let mut lines = BufReader::new(stdout).lines();
            let mut interrupted = false;
            let mut kill_at = tokio::time::Instant::now();
            loop {
                tokio::select! {
                    line = lines.next_line() => match line {
                        Ok(Some(line)) => {
                            let Ok(Value::Object(m)) = serde_json::from_str::<Value>(&line) else {
                                continue;
                            };
                            if let Some(id) = m.get("sessionId").and_then(Value::as_str)
                                && !id.is_empty()
                            {
                                on_session(id);
                            }
                            if m.get("type").and_then(Value::as_str) == Some("result") {
                                result = Some(m);
                            }
                        }
                        _ => break,
                    },
                    _ = ctx.done(), if !interrupted => {
                        interrupted = true;
                        // Go's WaitDelay: the runner has this long to end once interrupted
                        kill_at = tokio::time::Instant::now() + Duration::from_secs(15);
                        interrupt(&mut child);
                    }
                    _ = tokio::time::sleep_until(kill_at), if interrupted => {
                        let _ = child.start_kill();
                        break;
                    }
                }
            }
        }
        let wait = child.wait().await;
        let tail = match stderr {
            Some(h) => h.await.unwrap_or_default(),
            None => Tail::default(),
        };
        let Some(mut result) = result else {
            let mut msg = tail.text().trim().to_string();
            if msg.is_empty() {
                msg = match &wait {
                    Ok(s) if !s.success() => exit_text(s),
                    Err(e) => e.to_string(),
                    Ok(_) => String::new(),
                };
            }
            return Ok(to_map(
                json!({"status": STATUS_FAILED, "sessionId": req.resume,
                "errors": [format!("the runner gave no outcome: {msg}")]}),
            ));
        };
        result.remove("type");
        if let Some(s) = result.get("summary").and_then(Value::as_str)
            && s.len() > MAX_SUMMARY
        {
            let head = floor_boundary(s, MAX_SUMMARY / 4);
            let tail = ceil_boundary(s, s.len() - MAX_SUMMARY * 3 / 4);
            let cut = format!("{}\n…\n{}", &s[..head], &s[tail..]);
            result.insert("summary".into(), json!(cut));
        }
        let sid = result.get("sessionId");
        if sid.is_none() || sid == Some(&Value::Null) || sid == Some(&json!("")) {
            result.insert("sessionId".into(), json!(req.resume));
        }
        Ok(result)
    }

    /// workspace is the directory a session of the change works in: the workspace of the change under the unit's
    /// root, or a directory of it.
    fn workspace(
        &self,
        change: &str,
        config: &Map<String, Value>,
        sub: &str,
    ) -> Result<PathBuf, InvokeError> {
        let root = string_arg(config, "root");
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
        let mut dir = root.join(dir_name(change));
        let p = clean(&format!("/{}", sub.replace(std::path::MAIN_SEPARATOR, "/")));
        if p != "/" {
            for c in p.trim_start_matches('/').split('/') {
                dir.push(c);
            }
        }
        match std::fs::metadata(&dir) {
            Ok(m) if m.is_dir() => Ok(dir),
            _ => Err(format!(
                "no workspace directory {}: open the workspace of the change first (workspace/open)",
                dir.to_string_lossy()
            )
            .into()),
        }
    }

    /// environment is the cleaned environment of a session: the host variables a toolchain needs and the ones the
    /// unit passes, its own values, the API key and endpoint. Nothing else of the host. When the hub gives the call an
    /// endpoint of the model gateway (ADR 0123 §6), the session calls the model there with the token of the call,
    /// whatever the adapter says: every call of the session is then a row of the ledger, attributed to the person,
    /// the change and the process.
    fn environment(
        &self,
        cc: &CallContext,
        dir: &Path,
        config: &Map<String, Value>,
        secrets: &HashMap<String, String>,
    ) -> Vec<String> {
        let mut vars = BTreeMap::new();
        let mut pass: Vec<String> = HOST_ENV.iter().map(|s| s.to_string()).collect();
        pass.extend(strings1(config, "passEnv").unwrap_or_default());
        for name in pass {
            if let Some(v) = std::env::var_os(&name) {
                vars.insert(name, v.to_string_lossy().into_owned());
            }
        }
        for kv in strings1(config, "env").unwrap_or_default() {
            if let Some((k, v)) = kv.split_once('=')
                && !k.is_empty()
            {
                vars.insert(k.into(), v.into());
            }
        }
        if let Some(key) = secrets.get(SECRET_API_KEY)
            && !key.is_empty()
        {
            vars.insert("ANTHROPIC_API_KEY".into(), key.clone());
        }
        let u = string_arg(config, "baseURL");
        if !u.is_empty() {
            vars.insert("ANTHROPIC_BASE_URL".into(), u.into());
        }
        if !cc.model_url.is_empty() && !cc.model_token.is_empty() {
            vars.insert("ANTHROPIC_BASE_URL".into(), cc.model_url.clone());
            vars.insert("ANTHROPIC_API_KEY".into(), cc.model_token.clone());
            vars.remove("ANTHROPIC_AUTH_TOKEN");
        }
        if !self.sdk_module.is_empty() {
            vars.insert(
                "GOAP_CLAUDE_AGENT_SDK_MODULE".into(),
                self.sdk_module.clone(),
            );
        }
        vars.insert("GOAP_WORKSPACE".into(), dir.to_string_lossy().into_owned());
        vars.insert("GOAP_CHANGE".into(), cc.change.clone());
        vars.insert("CLAUDE_AGENT_SDK_CLIENT_APP".into(), format!("goap/{ID}"));
        vars.into_iter().map(|(k, v)| format!("{k}={v}")).collect()
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
        let st = self.state()?.clone();
        let change = goap_mcp::call_from(ctx).change;
        if change.is_empty() {
            return Err(Box::new(Error::NoChange));
        }
        match op {
            OP_STATUS => Ok(self.status(&st, &change)),
            OP_CANCEL => Ok(self.cancel(&st, &change)),
            OP_RUN | OP_ANSWER => self.run(ctx, op, &change, &args, &config, &secrets).await,
            _ => Err(format!("{op}: {ErrUnknownOperation}").into()),
        }
    }
}

/// answerPrompt is the prompt resuming a session with a person's decision.
fn answer_prompt(args: &Map<String, Value>) -> String {
    let mut b = String::new();
    if let Some(answers) = args.get("answers").and_then(Value::as_object)
        && !answers.is_empty()
    {
        b.push_str("A person answered your questions:\n");
        let mut qs: Vec<&String> = answers.keys().collect();
        qs.sort();
        for q in qs {
            let a = match &answers[q] {
                Value::String(s) => s.clone(),
                v => v.to_string(),
            };
            b.push_str(&format!("- {q} → {a}\n"));
        }
    }
    let allow = strings1(args, "allow").unwrap_or_default();
    if !allow.is_empty() {
        b.push_str(&format!(
            "A person allowed these tools for this run: {}.\n",
            allow.join(", ")
        ));
    }
    let a = string_arg(args, "answer").trim();
    if !a.is_empty() {
        if !b.is_empty() {
            b.push('\n');
        }
        b.push_str(a);
        b.push('\n');
    }
    if b.is_empty() {
        return String::new();
    }
    b + "\nGo on with the task."
}

/// Interrupts the runner (SIGINT: the SDK aborts the session), else kills it.
fn interrupt(child: &mut Child) {
    #[cfg(unix)]
    if let Some(pid) = child.id() {
        use nix::sys::signal::{Signal, kill};
        use nix::unistd::Pid;
        if kill(Pid::from_raw(pid as i32), Signal::SIGINT).is_ok() {
            return;
        }
    }
    let _ = child.start_kill();
}

/// The text of a failed exit, as Go's `exec.ExitError` words it.
fn exit_text(s: &std::process::ExitStatus) -> String {
    if let Some(c) = s.code() {
        return format!("exit status {c}");
    }
    #[cfg(unix)]
    {
        use std::os::unix::process::ExitStatusExt;
        if let Some(sig) = s.signal() {
            return match sig {
                2 => "signal: interrupt".into(),
                9 => "signal: killed".into(),
                15 => "signal: terminated".into(),
                n => format!("signal: {n}"),
            };
        }
    }
    s.to_string()
}

fn floor_boundary(s: &str, mut i: usize) -> usize {
    while i > 0 && !s.is_char_boundary(i) {
        i -= 1;
    }
    i
}

fn ceil_boundary(s: &str, mut i: usize) -> usize {
    while i < s.len() && !s.is_char_boundary(i) {
        i += 1;
    }
    i
}

/// Go's `time.Duration.String` (`1h0m0s`, `30m0s`, `1m30s`, `1.5s`, `500ms`).
pub fn go_duration(d: Duration) -> String {
    let nanos = d.as_nanos();
    if nanos == 0 {
        return "0s".into();
    }
    if nanos < 1_000_000_000 {
        let (unit, div) = if nanos < 1_000 {
            ("ns", 1u128)
        } else if nanos < 1_000_000 {
            ("µs", 1_000)
        } else {
            ("ms", 1_000_000)
        };
        return format!("{}{unit}", frac(nanos, div));
    }
    let secs = nanos / 1_000_000_000;
    let (h, m) = (secs / 3600, (secs % 3600) / 60);
    let s = frac(nanos % 60_000_000_000, 1_000_000_000);
    match (h, m) {
        (0, 0) => format!("{s}s"),
        (0, m) => format!("{m}m{s}s"),
        (h, m) => format!("{h}h{m}m{s}s"),
    }
}

/// v / div with its fraction, trailing zeros dropped.
fn frac(v: u128, div: u128) -> String {
    let whole = v / div;
    let rest = v % div;
    if rest == 0 {
        return whole.to_string();
    }
    let width = div.to_string().len() - 1;
    let f = format!("{rest:0width$}");
    format!("{whole}.{}", f.trim_end_matches('0'))
}

/// Go's `path.Clean`.
fn clean(p: &str) -> String {
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

fn string_arg<'a>(m: &'a Map<String, Value>, name: &str) -> &'a str {
    m.get(name).and_then(Value::as_str).unwrap_or("")
}

fn first_string<'a>(
    args: &'a Map<String, Value>,
    config: &'a Map<String, Value>,
    name: &str,
) -> &'a str {
    match string_arg(args, name) {
        "" => string_arg(config, name),
        v => v,
    }
}

fn number(m: &Map<String, Value>, name: &str) -> Option<f64> {
    m.get(name).and_then(Value::as_f64)
}

fn first_number(
    args: Option<&Map<String, Value>>,
    config: &Map<String, Value>,
    name: &str,
    def: f64,
) -> f64 {
    if let Some(v) = args.and_then(|a| number(a, name))
        && v > 0.0
    {
        return v;
    }
    if let Some(v) = number(config, name)
        && v > 0.0
    {
        return v;
    }
    def
}

/// strings1 reads a list of strings (None: no list).
fn strings1(m: &Map<String, Value>, name: &str) -> Option<Vec<String>> {
    m.get(name).and_then(Value::as_array).map(|l| {
        l.iter()
            .filter_map(Value::as_str)
            .filter(|s| !s.is_empty())
            .map(String::from)
            .collect()
    })
}

/// tail keeps the end of what is written to it (the stderr of the runner).
#[derive(Default)]
struct Tail(Vec<u8>);

impl Tail {
    fn write(&mut self, p: &[u8]) {
        self.0.extend_from_slice(p);
        if self.0.len() > 4096 {
            let over = self.0.len() - 4096;
            self.0.drain(..over);
        }
    }

    fn text(&self) -> String {
        String::from_utf8_lossy(&self.0).into_owned()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn durations_as_go_writes_them() {
        assert_eq!(go_duration(Duration::from_secs(3600)), "1h0m0s");
        assert_eq!(go_duration(Duration::from_secs(90)), "1m30s");
        assert_eq!(go_duration(Duration::from_millis(1500)), "1.5s");
        assert_eq!(go_duration(Duration::from_millis(500)), "500ms");
    }

    #[test]
    fn the_request_omits_what_is_empty() {
        let r = RunRequest {
            prompt: "p".into(),
            cwd: "/w".into(),
            ..Default::default()
        };
        assert_eq!(
            serde_json::to_string(&r).unwrap(),
            r#"{"prompt":"p","cwd":"/w","settingSources":[]}"#
        );
    }
}
