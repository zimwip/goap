package sandbox

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/zimwip/goap/pkg/engine"
)

// FromEnv builds the sandbox provider of the engine from the environment:
//
//	GOAP_SANDBOX              inproc (default) | process | docker | kubernetes
//	GOAP_RUNTIME_URL          engine URL reachable from sandboxes (RuntimeService)
//	GOAP_SANDBOX_IMAGE        goap-runner image (docker, kubernetes)
//	GOAP_SANDBOX_MEMORY_MB, GOAP_SANDBOX_CPUS, GOAP_SANDBOX_PIDS  limits
//	GOAP_SANDBOX_IDLE_TTL     stop idle sandboxes after this duration (10m)
//	GOAP_RUNNER_BIN, GOAP_SANDBOX_WRAPPER   process: binary and isolation wrapper
//	GOAP_DOCKER_HOST, GOAP_SANDBOX_NETWORK, GOAP_SANDBOX_RUNTIME   docker
//	GOAP_K8S_NAMESPACE, GOAP_SANDBOX_RUNTIME_CLASS                 kubernetes
//
// It returns the provider and, for remote sandboxes, the Runtime to mount on
// the engine server.
func FromEnv(log *slog.Logger, hc *http.Client, opts []connect.ClientOption) (engine.Sandboxes, *Runtime, *Pool, error) {
	kind := env("GOAP_SANDBOX", "inproc")
	if kind == "inproc" {
		log.Warn("script actions run in the engine process (GOAP_SANDBOX=inproc): not isolated, development only")
		return engine.InprocSandboxes{}, nil, nil, nil
	}
	var prov Provisioner
	switch kind {
	case "process":
		p := &ProcessProvisioner{Binary: env("GOAP_RUNNER_BIN", "goap-runner")}
		if w := os.Getenv("GOAP_SANDBOX_WRAPPER"); w != "" {
			p.Wrapper = strings.Fields(w)
		}
		prov = p
	case "docker":
		prov = &DockerProvisioner{Host: env("GOAP_DOCKER_HOST", "unix:///var/run/docker.sock"), Network: env("GOAP_SANDBOX_NETWORK", "goap-sandbox"),
			Runtime: os.Getenv("GOAP_SANDBOX_RUNTIME")}
	case "kubernetes":
		prov = &KubernetesProvisioner{Namespace: os.Getenv("GOAP_K8S_NAMESPACE"), RuntimeClass: os.Getenv("GOAP_SANDBOX_RUNTIME_CLASS")}
	default:
		return nil, nil, nil, fmt.Errorf("unknown GOAP_SANDBOX %q (inproc, process, docker, kubernetes)", kind)
	}
	runtimeURL := os.Getenv("GOAP_RUNTIME_URL")
	if runtimeURL == "" {
		return nil, nil, nil, fmt.Errorf("GOAP_RUNTIME_URL is required with GOAP_SANDBOX=%s", kind)
	}
	rt := NewRuntime()
	idle, _ := time.ParseDuration(env("GOAP_SANDBOX_IDLE_TTL", "10m"))
	pool := &Pool{Provisioner: prov, Runtime: rt, RuntimeURL: runtimeURL, HTTP: hc, Options: opts, IdleTTL: idle, Log: log,
		Template: Spec{Image: os.Getenv("GOAP_SANDBOX_IMAGE"), MemoryMB: envInt("GOAP_SANDBOX_MEMORY_MB", 512), PIDs: envInt("GOAP_SANDBOX_PIDS", 256),
			CPUs: envFloat("GOAP_SANDBOX_CPUS", 1)}}
	log.Info("script sandboxes", "provisioner", kind, "runtime", runtimeURL)
	return pool, rt, pool, nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int64) int64 {
	if v, err := strconv.ParseInt(os.Getenv(k), 10, 64); err == nil {
		return v
	}
	return def
}

func envFloat(k string, def float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(k), 64); err == nil {
		return v
	}
	return def
}
