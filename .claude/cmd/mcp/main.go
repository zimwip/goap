// Command mcp is the MCP connector. Skeleton: the API answers Unimplemented
// until milestone M3 (server registry, tool discovery, tool calls).
package main

import (
	"context"
	"github.com/zimwip/goap/gen/goap/mcp/v1/mcpv1connect"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/telemetry"
)

func main() {
	log := platform.Logger("mcp")
	defer telemetry.Setup(context.Background(), log, "mcp")(context.Background())
	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	srv.Mount(mcpv1connect.NewMcpServiceHandler(mcpv1connect.UnimplementedMcpServiceHandler{}, telemetry.HandlerOptions()...))
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}
