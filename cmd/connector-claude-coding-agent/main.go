// Command connector-claude-coding-agent is the connector of the claude-coding-agent MCP (ADR 0123): Claude Code
// sessions, run by the Claude Agent SDK in the workspaces of the changes. A standalone service that registers itself
// with the MCP hub (see internal/connectorkit); it runs on the host of the workspace connector (the sessions work in
// the same directories), with Node.js and the toolchains the changes build with.
//
// GOAP_CLAUDE_AGENT_DIR (default .goap/claude-agent) is the directory of its runner; `connector-claude-coding-agent
// install` writes the runner there and installs the Claude Agent SDK with npm (make claude-agent-runner).
// GOAP_WORKSPACE_ROOTS (comma separated) restricts the roots a unit may configure, like the workspace connector's.
// GOAP_NODE names the Node.js executable (default node).
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/zimwip/goap/internal/connectorkit"
	"github.com/zimwip/goap/internal/connectors/claudeagent"
	"github.com/zimwip/goap/internal/platform"
)

func main() {
	dir := platform.Env("GOAP_CLAUDE_AGENT_DIR", ".goap/claude-agent")
	if len(os.Args) > 1 && os.Args[1] == "install" {
		if err := install(dir); err != nil {
			fmt.Fprintln(os.Stderr, "install:", err)
			os.Exit(1)
		}
		return
	}
	var roots []string
	if v := os.Getenv("GOAP_WORKSPACE_ROOTS"); v != "" {
		roots = strings.Split(v, ",")
	}
	c := claudeagent.New(dir, roots...)
	c.Node = platform.Env("GOAP_NODE", "node")
	connectorkit.Run("connector-claude-coding-agent", c)
}

// install writes the runner into dir and installs its pinned Claude Agent SDK.
func install(dir string) error {
	if err := claudeagent.Install(dir); err != nil {
		return err
	}
	npm := exec.Command("npm", "install", "--omit=dev", "--no-audit", "--no-fund")
	npm.Dir, npm.Stdout, npm.Stderr = dir, os.Stdout, os.Stderr
	if err := npm.Run(); err != nil {
		return fmt.Errorf("npm install in %s: %w", dir, err)
	}
	fmt.Println("Claude Agent SDK installed in", dir)
	return nil
}
