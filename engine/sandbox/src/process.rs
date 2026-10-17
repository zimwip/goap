use std::collections::HashMap;
use std::process::Stdio;

use async_trait::async_trait;
use goap_ctx::Ctx;
use parking_lot::Mutex;
use tokio::sync::oneshot;

use crate::Error;
use crate::pool::{Instance, Provisioner, Spec};

/// A started process: its pid and the signal that kills it.
struct Proc {
    #[cfg_attr(not(unix), allow(dead_code))]
    pid: Option<u32>,
    kill: Option<oneshot::Sender<()>>,
}

/// ProcessProvisioner runs goap-runner as a separate OS process (bare metal). The child gets an empty environment,
/// its own process group and a private working directory. Real isolation on bare metal requires a Wrapper such as
/// bubblewrap or nsjail (e.g. "bwrap --unshare-all --share-net --die-with-parent --ro-bind /usr /usr --tmpfs /tmp
/// --"), and/or a dedicated UID.
#[derive(Default)]
pub struct ProcessProvisioner {
    /// Binary is the goap-runner executable.
    pub binary: String,
    /// Wrapper is prepended to the command (sandboxing tool).
    pub wrapper: Vec<String>,
    /// UID / GID run the sandbox as another user (requires privileges; 0 = unchanged; ignored on Windows).
    pub uid: u32,
    pub gid: u32,
    procs: Mutex<HashMap<String, Proc>>,
}

impl ProcessProvisioner {
    pub fn new(binary: &str) -> Self {
        ProcessProvisioner {
            binary: binary.to_string(),
            ..Default::default()
        }
    }
}

fn free_port() -> std::io::Result<u16> {
    let l = std::net::TcpListener::bind("127.0.0.1:0")?;
    Ok(l.local_addr()?.port())
}

#[async_trait]
impl Provisioner for ProcessProvisioner {
    fn name(&self) -> &str {
        "process"
    }

    async fn start(&self, _ctx: &Ctx, spec: Spec) -> Result<Instance, Error> {
        if self.binary.is_empty() {
            return Err(Error::msg(
                "process provisioner: runner binary not configured",
            ));
        }
        let port = free_port()?;
        let dir = std::env::temp_dir().join(format!(
            "{}-{}",
            spec.id,
            &uuid::Uuid::new_v4().simple().to_string()[..10]
        ));
        std::fs::create_dir(&dir)?;
        let mut args: Vec<String> = self.wrapper.clone();
        args.push(self.binary.clone());
        let mut cmd = tokio::process::Command::new(&args[0]);
        cmd.args(&args[1..])
            .current_dir(&dir)
            // no inherited environment: no secrets, no credentials leak into the sandbox
            .env_clear()
            .env("GOAP_HTTP_ADDR", format!("127.0.0.1:{port}"))
            .env("GOAP_LOG_FORMAT", "text")
            .env("GOAP_SANDBOX_ID", &spec.id)
            .stdin(Stdio::null())
            .stdout(Stdio::inherit())
            .stderr(Stdio::inherit());
        #[cfg(unix)]
        {
            // its own process group, so that stop kills it and all of its descendants
            cmd.process_group(0);
            if self.uid != 0 {
                cmd.uid(self.uid).gid(self.gid);
            }
        }
        let mut child = match cmd.spawn() {
            Ok(c) => c,
            Err(e) => {
                let _ = std::fs::remove_dir_all(&dir);
                return Err(e.into());
            }
        };
        let (kill, killed) = oneshot::channel::<()>();
        self.procs.lock().insert(
            spec.id.clone(),
            Proc {
                pid: child.id(),
                kill: Some(kill),
            },
        );
        tokio::spawn(async move {
            tokio::select! {
                _ = child.wait() => {}
                Ok(()) = killed => {
                    let _ = child.start_kill();
                    let _ = child.wait().await;
                }
            }
            let _ = std::fs::remove_dir_all(&dir);
        });
        Ok(Instance {
            id: spec.id,
            endpoint: format!("http://127.0.0.1:{port}"),
        })
    }

    /// Stop implements Provisioner: the whole process group is killed.
    async fn stop(&self, _ctx: &Ctx, id: &str) -> Result<(), Error> {
        let Some(mut p) = self.procs.lock().remove(id) else {
            return Ok(());
        };
        let res = kill_process_group(&p);
        if let Some(k) = p.kill.take() {
            let _ = k.send(());
        }
        res
    }
}

/// killProcessGroup kills the whole process group of the child.
#[cfg(unix)]
fn kill_process_group(p: &Proc) -> Result<(), Error> {
    use nix::sys::signal::{Signal, killpg};
    use nix::unistd::Pid;
    let Some(pid) = p.pid.and_then(|p| i32::try_from(p).ok()) else {
        return Ok(());
    };
    match killpg(Pid::from_raw(pid), Signal::SIGKILL) {
        Ok(()) | Err(nix::errno::Errno::ESRCH) => Ok(()),
        Err(e) => Err(Error::msg(e.to_string())),
    }
}

/// Without a process group, grandchild processes are not guaranteed to be killed: the child alone is.
#[cfg(not(unix))]
fn kill_process_group(_: &Proc) -> Result<(), Error> {
    Ok(())
}
