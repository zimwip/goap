//go:build windows

package sandbox

import "os/exec"

// configureSysProcAttr is a no-op on Windows: there is no POSIX process-group
// or credential API, so ProcessProvisioner.UID/GID are ignored on this platform.
func configureSysProcAttr(cmd *exec.Cmd, uid, gid uint32) {}

// killProcessGroup kills the child process. Without a process group, grandchild
// processes are not guaranteed to be killed.
func killProcessGroup(cmd *exec.Cmd) error {
	return cmd.Process.Kill()
}
