// Command mcp is the MCP connector. Skeleton: the API answers Unimplemented
// until milestone M3 (server registry, tool discovery, tool calls).
package main

import (
	"github.com/zimwip/goap/gen/goap/mcp/v1/mcpv1connect"
	"github.com/zimwip/goap/internal/platform"
)

func main() {
	log := platform.Logger("mcp")
	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	srv.Mount(mcpv1connect.NewMcpServiceHandler(mcpv1connect.UnimplementedMcpServiceHandler{}))
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}
