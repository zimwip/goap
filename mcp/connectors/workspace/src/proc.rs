//! The process group of a command (`proc_unix.go`, `proc_windows.go`).

use tokio::process::{Child, Command};

/// ownGroup puts a command in its own process group, so that [`kill_group`] stops what it started too. Windows:
/// nothing to do, [`kill_group`] kills the tree of the command.
#[cfg(unix)]
pub(crate) fn own_group(cmd: &mut Command) {
    cmd.process_group(0);
}

#[cfg(not(unix))]
pub(crate) fn own_group(_cmd: &mut Command) {}

/// killGroup kills the process group of a started command (Windows: the command and the processes it started).
#[cfg(unix)]
pub(crate) fn kill_group(child: &mut Child) -> std::io::Result<()> {
    use nix::errno::Errno;
    use nix::sys::signal::{Signal, killpg};
    use nix::unistd::Pid;
    let Some(pid) = child.id() else {
        return Ok(());
    };
    match killpg(Pid::from_raw(pid as i32), Signal::SIGKILL) {
        Ok(()) | Err(Errno::ESRCH) => Ok(()),
        Err(e) => Err(std::io::Error::from(e)),
    }
}

#[cfg(not(unix))]
pub(crate) fn kill_group(child: &mut Child) -> std::io::Result<()> {
    let Some(pid) = child.id() else {
        return Ok(());
    };
    let killed = std::process::Command::new("taskkill")
        .args(["/T", "/F", "/PID", &pid.to_string()])
        .status()
        .is_ok_and(|s| s.success());
    if killed {
        return Ok(());
    }
    child.start_kill()
}
