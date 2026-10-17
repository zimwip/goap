//! The runners of the commands: on the host, or in the container of the workspace (`runner.go`).

use std::process::Stdio;
use std::sync::Arc;
use std::time::Duration;

use goap_connect::{Ctx, async_trait};
use goap_connectorkit::InvokeError;
use serde_json::{Map, Value};
use tokio::io::{AsyncRead, AsyncReadExt};
use tokio::process::{Child, Command};
use tokio::task::JoinHandle;

use crate::proc::{kill_group, own_group};
use crate::shell::{environment, shell, workdir};
use crate::workspace::Space;

/// Where the output of a command goes (an `io.Writer`): the buffers of a call, the ring of a background command.
pub(crate) trait Output: Send + Sync {
    fn write(&self, b: &[u8]);
}

/// A buffer gathering an output.
#[derive(Default)]
pub(crate) struct Buffer(parking_lot::Mutex<Vec<u8>>);

impl Buffer {
    pub(crate) fn bytes(&self) -> Vec<u8> {
        self.0.lock().clone()
    }
}

impl Output for Buffer {
    fn write(&self, b: &[u8]) {
        self.0.lock().extend_from_slice(b);
    }
}

/// runner runs the commands of the workspaces (ADR 0118 §3, §7): on the host for the local connector, in the
/// container of the workspace for the docker one. Files and git stay on the host either way: the workspace is a
/// directory of the connector's host, and the token of the remote never reaches a command.
#[async_trait]
pub(crate) trait Runner: Send + Sync {
    /// start starts a command line in the directory cwd of the workspace (relative), writing its outputs to stdout
    /// and stderr; the command is killed when ctx ends.
    async fn start(
        &self,
        ctx: &Ctx,
        s: &Space,
        line: &str,
        cwd: &str,
        stdout: Arc<dyn Output>,
        stderr: Arc<dyn Output>,
    ) -> Result<Box<dyn Process>, InvokeError>;
    /// remove removes what the runner keeps for a workspace (its container), when it is closed.
    async fn remove(&self, ctx: &Ctx, s: &Space) -> Result<(), InvokeError>;
    /// status describes what the runner keeps for a workspace (None: nothing).
    async fn status(&self, ctx: &Ctx, s: &Space) -> Option<Map<String, Value>>;
}

/// process is a started command.
#[async_trait]
pub(crate) trait Process: Send {
    /// wait waits for the end of the command and its outputs: its exit code, or an error when it could not tell.
    async fn wait(self: Box<Self>) -> Result<i32, InvokeError>;
}

/// localRunner runs the commands on the host, in their own process group, with the cleaned environment.
pub(crate) struct LocalRunner;

struct LocalProcess {
    ctx: Ctx,
    child: Child,
    readers: Vec<JoinHandle<()>>,
}

/// How long the outputs are still read once the command ended (Go's `WaitDelay`).
const WAIT_DELAY: Duration = Duration::from_secs(2);

/// Copies a pipe to an output until its end.
pub(crate) fn copy_to(
    mut r: impl AsyncRead + Unpin + Send + 'static,
    w: Arc<dyn Output>,
) -> JoinHandle<()> {
    tokio::spawn(async move {
        let mut buf = vec![0u8; 32 << 10];
        loop {
            match r.read(&mut buf).await {
                Ok(0) | Err(_) => return,
                Ok(n) => w.write(&buf[..n]),
            }
        }
    })
}

#[async_trait]
impl Runner for LocalRunner {
    async fn start(
        &self,
        ctx: &Ctx,
        s: &Space,
        line: &str,
        cwd: &str,
        stdout: Arc<dyn Output>,
        stderr: Arc<dyn Output>,
    ) -> Result<Box<dyn Process>, InvokeError> {
        let mut cmd = command(s, line, cwd)?;
        cmd.stdout(Stdio::piped()).stderr(Stdio::piped());
        let mut child = cmd.spawn()?;
        let mut readers = Vec::new();
        if let Some(out) = child.stdout.take() {
            readers.push(copy_to(out, stdout));
        }
        if let Some(err) = child.stderr.take() {
            readers.push(copy_to(err, stderr));
        }
        Ok(Box::new(LocalProcess {
            ctx: ctx.clone(),
            child,
            readers,
        }))
    }

    async fn remove(&self, _ctx: &Ctx, _s: &Space) -> Result<(), InvokeError> {
        Ok(())
    }

    async fn status(&self, _ctx: &Ctx, _s: &Space) -> Option<Map<String, Value>> {
        None
    }
}

#[async_trait]
impl Process for LocalProcess {
    async fn wait(self: Box<Self>) -> Result<i32, InvokeError> {
        let LocalProcess {
            ctx,
            mut child,
            readers,
        } = *self;
        let status = tokio::select! {
            st = child.wait() => st,
            _ = ctx.done() => {
                let _ = kill_group(&mut child);
                child.wait().await
            }
        };
        // the outputs: read to their end, at most WAIT_DELAY more (a process left holding a pipe)
        let mut readers = readers;
        let gathered = tokio::time::timeout(WAIT_DELAY, async {
            for r in readers.iter_mut() {
                let _ = r.await;
            }
        })
        .await;
        if gathered.is_err() {
            for r in &readers {
                r.abort();
            }
        }
        let status = status?;
        Ok(status.code().unwrap_or(-1))
    }
}

/// The command running a command line in the workspace: the shell, the directory, the cleaned environment, its own
/// process group, killed with its group when dropped.
pub(crate) fn command(s: &Space, line: &str, cwd: &str) -> Result<Command, InvokeError> {
    let dir = workdir(s, cwd)?;
    let argv = shell(line);
    let mut cmd = Command::new(&argv[0]);
    cmd.args(&argv[1..])
        .current_dir(dir)
        .env_clear()
        .stdin(Stdio::null())
        .kill_on_drop(true);
    for kv in environment(s) {
        if let Some((k, v)) = kv.split_once('=') {
            cmd.env(k, v);
        }
    }
    own_group(&mut cmd);
    Ok(cmd)
}
