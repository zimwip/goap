//go:build windows

package workspace

import (
	"os/exec"
	"strconv"
)

// ownGroup does nothing on Windows: killGroup kills the tree of the command.
func ownGroup(*exec.Cmd) {}

// killGroup kills a started command and the processes it started.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run() == nil {
		return nil
	}
	return cmd.Process.Kill()
}
