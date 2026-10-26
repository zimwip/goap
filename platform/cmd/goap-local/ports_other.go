//go:build !linux

package main

import "runtime"

var portHint = map[bool]string{
	true:  "Get-NetTCPConnection -State Listen -LocalPort <port> shows who listens",
	false: "lsof -nP -iTCP:<port> -sTCP:LISTEN shows who listens",
}[runtime.GOOS == "windows"]

// portHolder is unknown here: the hint tells how to find it.
func portHolder(int) string { return "" }
