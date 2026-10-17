//! The commands of a workspace and its background commands (`shell.go`).

use std::collections::{BTreeMap, HashMap};
use std::path::PathBuf;
use std::sync::Arc;
use std::time::{Duration, Instant};

use chrono::{DateTime, SecondsFormat, Utc};
use goap_connect::Ctx;
use goap_connectorkit::InvokeError;
use goap_ctx::{CancelHandle, CtxError};
use serde_json::{Map, Value, json};

use crate::runner::{Buffer, Output, Runner};
use crate::workspace::{Space, bool_arg, int_arg, rel, string_arg, to_map};

// Bounds of the commands.
pub const DEFAULT_TIMEOUT: Duration = Duration::from_secs(120);
pub const MAX_TIMEOUT: Duration = Duration::from_secs(600);
/// MaxOutput bounds the stdout and the stderr given back by a command: the head and, mostly, the tail are kept.
pub const MAX_OUTPUT: usize = 30000;
/// MaxJobOutput bounds what a background command keeps of its output (the latest).
pub const MAX_JOB_OUTPUT: usize = 1 << 20;
/// MaxJobs bounds the background commands running in one workspace.
pub const MAX_JOBS: usize = 8;
/// JobTimeout bounds a background command.
pub const JOB_TIMEOUT: Duration = Duration::from_secs(2 * 3600);

/// HostEnv are the variables of the host every command gets: what a shell and a toolchain need to start.
pub const HOST_ENV: &[&str] = &[
    "PATH",
    "HOME",
    "USER",
    "LOGNAME",
    "LANG",
    "LC_ALL",
    "TMPDIR",
    "TZ",
    "SHELL",
    // Windows
    "SystemRoot",
    "SystemDrive",
    "ComSpec",
    "PATHEXT",
    "TEMP",
    "TMP",
    "USERPROFILE",
    "APPDATA",
    "LOCALAPPDATA",
    "ProgramFiles",
    "ProgramData",
    "WINDIR",
];

/// The strings of a configuration list.
fn strings(config: &Map<String, Value>, name: &str) -> Vec<String> {
    config
        .get(name)
        .and_then(Value::as_array)
        .map(|l| {
            l.iter()
                .filter_map(|v| v.as_str().map(String::from))
                .collect()
        })
        .unwrap_or_default()
}

/// The `NAME=value` variables the unit sets, the identity of the commits: what the local and the container
/// environments share.
pub(crate) fn unit_vars(s: &Space, vars: &mut BTreeMap<String, String>) {
    for v in s
        .config
        .get("env")
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
    {
        let v = v.as_str().unwrap_or("");
        if let Some((k, val)) = v.split_once('=')
            && !k.is_empty()
        {
            vars.insert(k.into(), val.into());
        }
    }
    if let Some(name) = s.config.get("gitName").and_then(Value::as_str)
        && !name.is_empty()
    {
        vars.insert("GIT_AUTHOR_NAME".into(), name.into());
        vars.insert("GIT_COMMITTER_NAME".into(), name.into());
    }
    if let Some(mail) = s.config.get("gitEmail").and_then(Value::as_str)
        && !mail.is_empty()
    {
        vars.insert("GIT_AUTHOR_EMAIL".into(), mail.into());
        vars.insert("GIT_COMMITTER_EMAIL".into(), mail.into());
    }
}

/// environment is the cleaned environment of the commands of a workspace: the host variables above and the ones the
/// unit passes, its own values, the identity of the commits, the workspace. Never a secret of the adapter.
/// Sorted `NAME=value` entries.
pub(crate) fn environment(s: &Space) -> Vec<String> {
    let mut vars = BTreeMap::new();
    let mut pass: Vec<String> = HOST_ENV.iter().map(|s| s.to_string()).collect();
    pass.extend(strings(&s.config, "passEnv"));
    for name in pass {
        if let Some(v) = std::env::var_os(&name) {
            vars.insert(name, v.to_string_lossy().into_owned());
        }
    }
    unit_vars(s, &mut vars);
    vars.insert("GOAP_WORKSPACE".into(), s.dir_str());
    vars.insert("GOAP_CHANGE".into(), s.change.clone());
    vars.insert("TERM".into(), "dumb".into());
    vars.insert("GIT_TERMINAL_PROMPT".into(), "0".into());
    vars.into_iter().map(|(k, v)| format!("{k}={v}")).collect()
}

/// `exec.LookPath`: the program of that name in the PATH of the connector.
pub(crate) fn look_path(name: &str) -> Option<PathBuf> {
    let path = std::env::var_os("PATH")?;
    let exts: Vec<String> = if cfg!(windows) {
        std::env::var("PATHEXT")
            .unwrap_or_else(|_| ".COM;.EXE;.BAT;.CMD".into())
            .split(';')
            .map(String::from)
            .chain(std::iter::once(String::new()))
            .collect()
    } else {
        vec![String::new()]
    };
    for dir in std::env::split_paths(&path) {
        for ext in &exts {
            let p = dir.join(format!("{name}{ext}"));
            if is_executable(&p) {
                return Some(p);
            }
        }
    }
    None
}

#[cfg(unix)]
fn is_executable(p: &std::path::Path) -> bool {
    use std::os::unix::fs::PermissionsExt;
    std::fs::metadata(p).is_ok_and(|m| m.is_file() && m.permissions().mode() & 0o111 != 0)
}

#[cfg(not(unix))]
fn is_executable(p: &std::path::Path) -> bool {
    std::fs::metadata(p).is_ok_and(|m| m.is_file())
}

/// shell is the command line running a command: bash when there is one (Git Bash on Windows), else the system shell.
pub(crate) fn shell(command: &str) -> Vec<String> {
    if let Some(p) = look_path("bash") {
        return vec![
            p.to_string_lossy().into_owned(),
            "-c".into(),
            command.into(),
        ];
    }
    if cfg!(windows) {
        return vec!["cmd".into(), "/C".into(), command.into()];
    }
    vec!["/bin/sh".into(), "-c".into(), command.into()]
}

/// workdir is the directory of the workspace a command runs in, on the host.
pub(crate) fn workdir(s: &Space, cwd: &str) -> Result<PathBuf, InvokeError> {
    let dir = s.local(cwd);
    match std::fs::metadata(&dir) {
        Ok(md) if md.is_dir() => Ok(dir),
        _ => Err(format!("cwd {} is not a directory of the workspace", rel(cwd)).into()),
    }
}

pub(crate) async fn bash(
    ctx: &Ctx,
    jobs: &Arc<Jobs>,
    runner: Arc<dyn Runner>,
    s: &Space,
    args: &Map<String, Value>,
) -> Result<Map<String, Value>, InvokeError> {
    let line = string_arg(args, "command");
    if line.is_empty() {
        return Err("bash needs a command".into());
    }
    if bool_arg(args, "background") {
        return jobs.start(runner, s, line, string_arg(args, "cwd")).await;
    }
    let secs = int_arg(args, "timeout", 0);
    let timeout = if secs <= 0 {
        DEFAULT_TIMEOUT
    } else {
        Duration::from_secs(secs as u64)
    }
    .min(MAX_TIMEOUT);
    let (ctx, cancel) = ctx.with_timeout(timeout);
    let stdout = Arc::new(Buffer::default());
    let stderr = Arc::new(Buffer::default());
    let start = Instant::now();
    let res = async {
        let p = runner
            .start(
                &ctx,
                s,
                line,
                string_arg(args, "cwd"),
                stdout.clone(),
                stderr.clone(),
            )
            .await?;
        Ok::<_, InvokeError>(p.wait().await)
    }
    .await;
    let timed_out = ctx.err() == Some(CtxError::DeadlineExceeded);
    cancel.cancel();
    let waited = res?;
    let mut out = to_map(json!({
        "stdout": cap_output(&stdout.bytes()),
        "stderr": cap_output(&stderr.bytes()),
        "durationMs": start.elapsed().as_millis() as f64,
    }));
    if timed_out {
        out.insert("timedOut".into(), json!(true));
        out.insert("exitCode".into(), json!(-1.0));
    } else {
        out.insert("exitCode".into(), json!(waited? as f64));
    }
    Ok(out)
}

/// capOutput keeps the head and the tail of an output longer than MaxOutput.
pub(crate) fn cap_output(b: &[u8]) -> String {
    if b.len() <= MAX_OUTPUT {
        return String::from_utf8_lossy(b).into_owned();
    }
    let (head, tail) = (MAX_OUTPUT / 5, MAX_OUTPUT - MAX_OUTPUT / 5);
    let mut h = &b[..head];
    let mut t = &b[b.len() - tail..];
    while !h.is_empty() && std::str::from_utf8(h).is_err() {
        h = &h[..h.len() - 1];
    }
    while !t.is_empty() && (t[0] & 0xC0) == 0x80 {
        t = &t[1..];
    }
    format!(
        "{}\n... ({} bytes cut) ...\n{}",
        String::from_utf8_lossy(h),
        b.len() - h.len() - t.len(),
        String::from_utf8_lossy(t)
    )
}

// ---- background commands ------------------------------------------------------------------------------------

/// The background commands of the workspaces, by workspace directory and id.
#[derive(Default)]
pub(crate) struct Jobs {
    inner: tokio::sync::Mutex<JobsInner>,
}

#[derive(Default)]
struct JobsInner {
    seq: u64,
    by_workspace: HashMap<String, HashMap<String, Job>>,
}

struct Job {
    id: String,
    command: String,
    started: DateTime<Utc>,
    cancel: CancelHandle,
    out: Arc<Ring>,
    /// bytes of the output already given back
    read: u64,
    done: bool,
    exit_code: i32,
    timed_out: bool,
}

/// ring keeps the latest MaxJobOutput bytes of an output and how many were written.
#[derive(Default)]
pub(crate) struct Ring {
    inner: parking_lot::Mutex<RingInner>,
}

#[derive(Default)]
struct RingInner {
    buf: Vec<u8>,
    written: u64,
}

impl Output for Ring {
    fn write(&self, p: &[u8]) {
        let mut r = self.inner.lock();
        r.buf.extend_from_slice(p);
        if r.buf.len() > MAX_JOB_OUTPUT {
            let over = r.buf.len() - MAX_JOB_OUTPUT;
            r.buf.drain(..over);
        }
        r.written += p.len() as u64;
    }
}

impl Ring {
    /// since returns what was written after offset (what is still kept of it), how many bytes were lost and how
    /// many were written.
    pub(crate) fn since(&self, offset: u64) -> (Vec<u8>, u64, u64) {
        let r = self.inner.lock();
        let first = r.written - r.buf.len() as u64;
        let mut lost = 0;
        let mut offset = offset;
        if offset < first {
            lost = first - offset;
            offset = first;
        }
        (r.buf[(offset - first) as usize..].to_vec(), lost, r.written)
    }
}

impl Jobs {
    pub(crate) async fn start(
        self: &Arc<Self>,
        runner: Arc<dyn Runner>,
        s: &Space,
        line: &str,
        cwd: &str,
    ) -> Result<Map<String, Value>, InvokeError> {
        let mut j = self.inner.lock().await;
        let dir = s.dir_str();
        let running = j
            .by_workspace
            .get(&dir)
            .map_or(0, |m| m.values().filter(|jb| !jb.done).count());
        if running >= MAX_JOBS {
            return Err(format!(
                "{running} commands already run in the background of this workspace: stop one first"
            )
            .into());
        }
        let (ctx, cancel) = Ctx::background().with_timeout(JOB_TIMEOUT);
        j.seq += 1;
        let id = format!("job-{}", j.seq);
        let out = Arc::new(Ring::default());
        let p = match runner
            .start(&ctx, s, line, cwd, out.clone(), out.clone())
            .await
        {
            Ok(p) => p,
            Err(e) => {
                cancel.cancel();
                return Err(e);
            }
        };
        j.by_workspace.entry(dir.clone()).or_default().insert(
            id.clone(),
            Job {
                id: id.clone(),
                command: line.into(),
                started: Utc::now(),
                cancel: cancel.clone(),
                out,
                read: 0,
                done: false,
                exit_code: 0,
                timed_out: false,
            },
        );
        drop(j);
        let jobs = self.clone();
        let job_id = id.clone();
        tokio::spawn(async move {
            let res = p.wait().await;
            let timed_out = ctx.err() == Some(CtxError::DeadlineExceeded);
            let mut j = jobs.inner.lock().await;
            if let Some(jb) = j
                .by_workspace
                .get_mut(&dir)
                .and_then(|m| m.get_mut(&job_id))
            {
                jb.done = true;
                jb.exit_code = res.unwrap_or(-1);
                jb.timed_out = timed_out;
            }
            cancel.cancel();
        });
        Ok(to_map(json!({"id": id, "running": true})))
    }

    pub(crate) async fn output(
        &self,
        dir: &str,
        id: &str,
    ) -> Result<Map<String, Value>, InvokeError> {
        let mut j = self.inner.lock().await;
        let jb = find(&mut j, dir, id)?;
        let (text, lost, written) = jb.out.since(jb.read);
        jb.read = written;
        let mut out = to_map(json!({"id": id, "output": cap_output(&text), "running": !jb.done}));
        if lost > 0 {
            out.insert("lost".into(), json!(lost as f64));
        }
        if jb.done {
            out.insert("exitCode".into(), json!(jb.exit_code as f64));
            out.insert("timedOut".into(), json!(jb.timed_out));
        }
        Ok(out)
    }

    pub(crate) async fn kill(
        &self,
        dir: &str,
        id: &str,
    ) -> Result<Map<String, Value>, InvokeError> {
        let mut j = self.inner.lock().await;
        let jb = find(&mut j, dir, id)?;
        if !jb.done {
            jb.cancel.cancel();
        }
        Ok(to_map(json!({"id": id, "stopped": true})))
    }

    /// stopAll stops the commands of a workspace and forgets them, returning how many were running.
    pub(crate) async fn stop_all(&self, dir: &str) -> usize {
        let mut j = self.inner.lock().await;
        let mut n = 0;
        if let Some(m) = j.by_workspace.remove(dir) {
            for jb in m.values() {
                if !jb.done {
                    n += 1;
                    jb.cancel.cancel();
                }
            }
        }
        n
    }

    pub(crate) async fn list(&self, dir: &str) -> Vec<Value> {
        let j = self.inner.lock().await;
        let mut out: Vec<(String, Value)> = j
            .by_workspace
            .get(dir)
            .into_iter()
            .flat_map(|m| m.values())
            .map(|jb| {
                let mut item = to_map(
                    json!({"id": jb.id, "command": jb.command, "running": !jb.done,
                    "started": jb.started.to_rfc3339_opts(SecondsFormat::Secs, true)}),
                );
                if jb.done {
                    item.insert("exitCode".into(), json!(jb.exit_code as f64));
                }
                (jb.id.clone(), Value::Object(item))
            })
            .collect();
        out.sort_by(|a, b| a.0.cmp(&b.0));
        out.into_iter().map(|(_, v)| v).collect()
    }
}

fn find<'a>(j: &'a mut JobsInner, dir: &str, id: &str) -> Result<&'a mut Job, InvokeError> {
    j.by_workspace
        .get_mut(dir)
        .and_then(|m| m.get_mut(id))
        .ok_or_else(|| format!("no background command {id:?} in this workspace").into())
}
