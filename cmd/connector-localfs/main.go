// Command connector-localfs is the local file system connector: a standalone
// service that registers itself with the MCP hub (see internal/connectorkit).
// GOAP_LOCALFS_ROOTS (comma separated) restricts the directories an organization may bind.
package main

import (
	"os"
	"strings"

	"github.com/zimwip/goap/internal/connectorkit"
	"github.com/zimwip/goap/internal/connectors/localfs"
)

func main() {
	var roots []string
	if v := os.Getenv("GOAP_LOCALFS_ROOTS"); v != "" {
		roots = strings.Split(v, ",")
	}
	connectorkit.Run("connector-localfs", localfs.Connector{Roots: roots})
}
