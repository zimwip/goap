// Command goap-runner runs inside a sandbox (container, pod or isolated
// process). It executes script actions with the DSL and reaches the platform
// only through the engine RuntimeService, with a per-job token.
package main

import (
	"context"
	"net/http"
	"time"

	"github.com/zimwip/goap/gen/goap/runtime/v1/runtimev1connect"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/internal/sandbox"
	"github.com/zimwip/goap/internal/telemetry"
)

func main() {
	log := platform.Logger("goap-runner")
	defer telemetry.Setup(context.Background(), log, "goap-runner")(context.Background())
	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	// HTTP/1.1 client: the runtime endpoint may sit behind any proxy
	hc := &http.Client{Timeout: 10 * time.Minute}
	srv.Mount(runtimev1connect.NewSandboxServiceHandler(&sandbox.Runner{HTTP: hc, Options: telemetry.ClientOptions()}, telemetry.HandlerOptions()...))
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}
