//go:build !windows

package sandbox

import (
	"os/exec"
	"strings"
	"syscall"
)

// configureSysProcAttr puts the child in its own process group so Stop can
// kill it and all of its descendants, and optionally drops privileges.
func configureSysProcAttr(cmd *exec.Cmd, uid, gid uint32) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if uid != 0 {
		cmd.SysProcAttr.Credential = &syscall.Credential{Uid: uid, Gid: gid}
	}
}

// killProcessGroup kills the whole process group started by configureSysProcAttr.
func killProcessGroup(cmd *exec.Cmd) error {
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !strings.Contains(err.Error(), "no such process") {
		return err
	}
	return nil
}
