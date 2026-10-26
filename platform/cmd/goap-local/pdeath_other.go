//go:build unix && !linux

package main

import "syscall"

// dieWithParent does nothing where the system has no such signal: a process left by a goap-local killed is found by
// the check of the ports at the next start.
func dieWithParent(*syscall.SysProcAttr) {}
