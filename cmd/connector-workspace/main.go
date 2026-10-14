// Command connector-workspace is the connector of the workspace MCP on the local machine (ADR 0118): one directory
// per change, where the agents of the change work on files, commands and sources. A standalone service that
// registers itself with the MCP hub (see internal/connectorkit); it runs on a host with git, a shell and the
// toolchains the changes build with (not in the distroless image of the services).
// GOAP_WORKSPACE_ROOTS (comma separated) restricts the directories a unit may configure as its root.
package main

import (
	"os"
	"strings"

	"github.com/zimwip/goap/internal/connectorkit"
	"github.com/zimwip/goap/internal/connectors/workspace"
)

func main() {
	var roots []string
	if v := os.Getenv("GOAP_WORKSPACE_ROOTS"); v != "" {
		roots = strings.Split(v, ",")
	}
	connectorkit.Run("connector-workspace", workspace.New(roots...))
}
