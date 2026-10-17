//! Running a script: the language, the bridge served on the caller's task, the deadline.

use std::{
    future::Future,
    sync::Arc,
    time::{Duration, Instant},
};

use serde_json::Value;
use tokio::sync::mpsc;

use crate::{
    Error,
    bridge::{Bridge, HostCall, HostReply, HostRequest},
    dsl::{Ctx, Host, Job, ScriptResult, output_of},
    engine::{Interrupt, ScriptError, ScriptHost, ScriptSource, engine},
};

/// DefaultTimeout bounds a script execution.
pub const DEFAULT_TIMEOUT: Duration = Duration::from_secs(60);

/// How long an interrupted script is given to stop and leave its state (logs) before the run returns without it.
const GRACE: Duration = Duration::from_millis(500);

/// Checks the language of a script: JavaScript (`javascript`, `js`) only.
pub(crate) fn check_language(language: &str) -> Result<(), Error> {
    match language {
        "javascript" | "js" => Ok(()),
        "go" => Err(Error::GoUnsupported),
        other => Err(Error::UnsupportedLanguage(other.to_string())),
    }
}

/// Runs a script against a host object and serves its requests on the calling task until it ends, its deadline
/// passes or `ctx` is done. Every usage of the DSL (actions, algorithms, adapters) goes through it; only the host
/// object and how its requests are served differ. The result is what `run(ctx)` returned.
pub(crate) async fn drive<F, Fut>(
    ctx: &goap_ctx::Ctx,
    timeout: Duration,
    language: &str,
    code: String,
    host: Arc<dyn ScriptHost>,
    mut rx: mpsc::UnboundedReceiver<HostCall>,
    mut serve: F,
) -> Result<Option<Value>, Error>
where
    F: FnMut(goap_ctx::Ctx, HostRequest) -> Fut,
    Fut: Future<Output = Result<HostReply, Error>>,
{
    check_language(language)?;
    let (rctx, _cancel) = ctx.with_timeout(timeout);
    let interrupt = Arc::new(Interrupt::new(rctx.deadline()));
    let engine = engine();
    let source = ScriptSource::new(code);
    let intr = interrupt.clone();
    let mut task = tokio::task::spawn_blocking(move || engine.run(&source, host, &intr));
    loop {
        tokio::select! {
            joined = &mut task => return finish(joined),
            Some(call) = rx.recv() => {
                let r = tokio::select! {
                    r = serve(rctx.clone(), call.req) => r,
                    e = rctx.done() => Err(Error::Interrupted(e.to_string())),
                };
                let _ = call.reply.send(r);
            }
            e = rctx.done() => {
                let cause = e.to_string();
                interrupt.set(cause.clone());
                // let the script stop (it sees the interrupt within a few hundred instructions) and leave its state,
                // answering what it still asks with the interruption
                let until = tokio::time::Instant::from_std(Instant::now() + GRACE);
                loop {
                    tokio::select! {
                        _ = &mut task => break,
                        Some(call) = rx.recv() => {
                            let _ = call.reply.send(Err(Error::Interrupted(cause.clone())));
                        }
                        _ = tokio::time::sleep_until(until) => break,
                    }
                }
                return Err(Error::Interrupted(cause));
            }
        }
    }
}

fn finish(
    joined: Result<Result<Option<Value>, ScriptError>, tokio::task::JoinError>,
) -> Result<Option<Value>, Error> {
    match joined {
        Ok(Ok(v)) => Ok(v),
        Ok(Err(ScriptError::Failed(m))) => Err(Error::Script(m)),
        Ok(Err(ScriptError::Interrupted(cause))) => Err(Error::Interrupted(cause)),
        Err(e) => Err(Error::Script(format!("script panic: {e}"))),
    }
}

/// Run executes a script job. Writes are returned only when the script ends without error (and without suspension);
/// the result carries the logs and the output in every case, with the error of the script beside it (Go's
/// `(Result, error)`).
pub async fn run(
    ctx: &goap_ctx::Ctx,
    job: Job,
    host: &dyn Host,
) -> (ScriptResult, Result<(), Error>) {
    let timeout = if job.timeout.is_zero() {
        DEFAULT_TIMEOUT
    } else {
        job.timeout
    };
    let (bridge, rx) = Bridge::new();
    let language = job.language.clone();
    let code = job.code.clone();
    let c = Arc::new(Ctx::with_bridge(job, bridge));
    let ran = drive(ctx, timeout, &language, code, c.clone(), rx, |rctx, req| {
        serve_action(host, rctx, req)
    })
    .await;
    let mut st = c.state.lock();
    if let Ok(Some(v)) = &ran
        && st.result.is_none()
    {
        st.result = Some(v.clone());
    }
    let mut res = ScriptResult {
        logs: std::mem::take(&mut st.logs),
        vars_set: std::mem::take(&mut st.vars),
        ..Default::default()
    };
    if let Some(v) = st.result.as_ref().filter(|v| !v.is_null()) {
        res.output = output_of(v);
    }
    if st.suspended {
        res.suspended = true;
        res.wake_on = std::mem::take(&mut st.wake_on);
        return (res, Ok(()));
    }
    if let Err(e) = ran {
        return (res, Err(e));
    }
    res.items = std::mem::take(&mut st.out);
    res.nodes = std::mem::take(&mut st.node_ops);
    if res.output.is_empty() {
        res.output = format!("{} item(s)", res.items.len());
        if !res.nodes.is_empty() {
            res.output += &format!(", {} change impact operation(s)", res.nodes.len());
        }
    }
    (res, Ok(()))
}

async fn serve_action(
    host: &dyn Host,
    ctx: goap_ctx::Ctx,
    req: HostRequest,
) -> Result<HostReply, Error> {
    Ok(match req {
        HostRequest::Complete(r) => HostReply::Complete(host.complete(&ctx, r).await?),
        HostRequest::RunAgent { name, intent } => {
            HostReply::Agent(host.run_agent(&ctx, &name, &intent).await?)
        }
        HostRequest::CallTool { name, args } => {
            HostReply::Value(host.call_tool(&ctx, &name, args).await?)
        }
        HostRequest::Node(key) => HostReply::Node(host.node(&ctx, &key).await?),
        HostRequest::Nodes(t) => HostReply::Nodes(host.nodes(&ctx, &t).await?),
        HostRequest::Links {
            key,
            direction,
            link_type,
        } => HostReply::Links(host.links(&ctx, &key, &direction, &link_type).await?),
        HostRequest::Operation { .. } => {
            return Err(Error::host("a script action calls no connector operation"));
        }
    })
}
