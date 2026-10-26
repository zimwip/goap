package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const portHint = "ss -ltnp shows who listens"

// portHolder names the process listening on a port: its pid and command line, read from /proc; empty when unknown.
func portHolder(port int) string {
	inodes := map[string]bool{}
	for _, table := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		f, err := os.Open(table)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			// sl local_address rem_address st tx_queue:rx_queue tr:tm->when retrnsmt uid timeout inode
			fields := strings.Fields(sc.Text())
			if len(fields) < 10 || fields[3] != "0A" { // 0A: LISTEN
				continue
			}
			_, hex, ok := strings.Cut(fields[1], ":")
			if p, err := strconv.ParseInt(hex, 16, 32); ok && err == nil && int(p) == port {
				inodes["socket:["+fields[9]+"]"] = true
			}
		}
		f.Close()
	}
	if len(inodes) == 0 {
		return ""
	}
	fds, _ := filepath.Glob("/proc/[0-9]*/fd/*")
	for _, fd := range fds {
		link, err := os.Readlink(fd)
		if err != nil || !inodes[link] {
			continue
		}
		dir := filepath.Dir(filepath.Dir(fd))
		cmd, _ := os.ReadFile(filepath.Join(dir, "cmdline"))
		line := strings.TrimSpace(strings.ReplaceAll(string(cmd), "\x00", " "))
		if len(line) > 120 {
			line = line[:120] + "…"
		}
		return fmt.Sprintf("pid %s, %s", filepath.Base(dir), line)
	}
	return ""
}
