//go:build unix

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// ownGroup runs the command in a process group of its own, which it leads: an interrupt of the terminal reaches
// goap-local only, which stops the groups in turn.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	dieWithParent(cmd.SysProcAttr)
}

// interruptGroup interrupts every process of the group the process leads.
func interruptGroup(p *os.Process) { _ = syscall.Kill(-p.Pid, syscall.SIGINT) }

// killGroup kills every process of the group the process leads.
func killGroup(p *os.Process) { _ = syscall.Kill(-p.Pid, syscall.SIGKILL) }
