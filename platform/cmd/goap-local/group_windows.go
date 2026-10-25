package main

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// ownGroup runs the command in a process group of its own: the console's Ctrl-C reaches goap-local only, which stops
// the processes in turn.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// interruptGroup stops the process and the processes it started: Windows has no interrupt to send to another
// console group, so the tree is ended.
func interruptGroup(p *os.Process) { killGroup(p) }

// killGroup ends the process and every process it started (taskkill /T).
func killGroup(p *os.Process) {
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(p.Pid)).Run()
}
