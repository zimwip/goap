package main

import (
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"
)

// portUse is a port the stack listens on and who listens on it.
type portUse struct {
	port int
	name string
}

// checkPorts makes sure no process listens on the ports of the stack before it starts: a process of a previous run
// still running would answer the readiness checks in place of the new one, which could not listen. The error names
// each port taken and, where the system tells, the process holding it.
func checkPorts(uses []portUse) error {
	var busy []string
	slices.SortFunc(uses, func(a, b portUse) int { return a.port - b.port })
	for _, u := range uses {
		if u.port == 0 || portFree(u.port) {
			continue
		}
		line := fmt.Sprintf("  %d (%s)", u.port, u.name)
		if holder := portHolder(u.port); holder != "" {
			line += ": " + holder
		}
		busy = append(busy, line)
	}
	if len(busy) == 0 {
		return nil
	}
	return errors.New("ports of the stack already in use, a previous run still going? stop it, or free them:\n" +
		strings.Join(busy, "\n") + "\n  " + portHint)
}

// portFree tells whether nothing listens on the port: it can be listened on, and nothing answers it (a listener on
// another address of the machine, which some systems let a second listener share).
func portFree(port int) bool {
	ln, err := net.Listen("tcp", addr(port))
	if err != nil {
		return false
	}
	ln.Close()
	for _, host := range []string{"127.0.0.1", "[::1]"} {
		if c, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), 200*time.Millisecond); err == nil {
			c.Close()
			return false
		}
	}
	return true
}
