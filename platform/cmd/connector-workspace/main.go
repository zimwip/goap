// Command connector-workspace is the connector of the workspace MCP on the local machine (ADR 0118): one directory
// per change, where the agents of the change work on files, commands and sources. A standalone service that
// registers itself with the MCP hub (see internal/connectorkit); it runs on a host with git, a shell and the
// toolchains the changes build with (not in the distroless image of the services).
// GOAP_WORKSPACE_ROOTS (comma separated) restricts the directories a unit may configure as its root. With
// GOAP_WORKSPACE_DOCKER set to the address of a Docker Engine API (unix:///var/run/docker.sock, tcp://proxy:2375),
// it is the connector workspace-docker instead: the commands of each workspace run in a container of its own (the
// host then needs git only, the toolchains are in the image the unit configures).
package main

import (
	"os"
	"strings"

	"github.com/zimwip/goap/mcp/connectorkit"
	"github.com/zimwip/goap/mcp/connectors/workspace"
)

func main() {
	var roots []string
	if v := os.Getenv("GOAP_WORKSPACE_ROOTS"); v != "" {
		roots = strings.Split(v, ",")
	}
	if host := os.Getenv("GOAP_WORKSPACE_DOCKER"); host != "" {
		connectorkit.Run("connector-workspace-docker", workspace.NewDocker(host, roots...))
		return
	}
	connectorkit.Run("connector-workspace", workspace.New(roots...))
}
