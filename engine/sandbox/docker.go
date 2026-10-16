package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
)

// DockerProvisioner starts one container per sandbox through the Docker
// Engine API. Containers are hardened: read-only root file system, no
// capabilities, no privilege escalation, non-root user, resource limits and
// an internal network where only the engine is reachable.
//
// The engine needs access to the Docker API: prefer a socket proxy limited
// to the containers endpoints over mounting /var/run/docker.sock.
type DockerProvisioner struct {
	// Host is the Docker API address: "unix:///var/run/docker.sock" or "tcp://proxy:2375".
	Host string
	// Network the sandbox joins (must reach the engine RuntimeService).
	Network string
	// Runtime is an optional OCI runtime (e.g. "runsc" for gVisor).
	Runtime string
	// User inside the container.
	User string

	client *http.Client
	base   string
}

var _ Provisioner = (*DockerProvisioner)(nil)

func (d *DockerProvisioner) Name() string { return "docker" }

const dockerAPI = "/v1.43"

func (d *DockerProvisioner) http() (*http.Client, string, error) {
	if d.client != nil {
		return d.client, d.base, nil
	}
	host := d.Host
	if host == "" {
		host = "unix:///var/run/docker.sock"
	}
	u, err := url.Parse(host)
	if err != nil {
		return nil, "", err
	}
	switch u.Scheme {
	case "unix":
		path := u.Path
		d.client = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dl net.Dialer
			return dl.DialContext(ctx, "unix", path)
		}}}
		d.base = "http://docker" + dockerAPI
	case "tcp", "http":
		d.client = &http.Client{}
		d.base = "http://" + u.Host + dockerAPI
	default:
		return nil, "", fmt.Errorf("unsupported docker host %q", host)
	}
	return d.client, d.base, nil
}

func (d *DockerProvisioner) do(ctx context.Context, method, path string, body any, out any) error {
	hc, base, err := d.http()
	if err != nil {
		return err
	}
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotModified {
		return fmt.Errorf("docker %s %s: %s: %s", method, path, resp.Status, bytes.TrimSpace(raw))
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// Start implements Provisioner.
func (d *DockerProvisioner) Start(ctx context.Context, spec Spec) (Instance, error) {
	if spec.Image == "" {
		return Instance{}, fmt.Errorf("docker provisioner: sandbox image not configured")
	}
	user := d.User
	if user == "" {
		user = "65532:65532"
	}
	host := map[string]any{
		"NetworkMode":    d.Network,
		"ReadonlyRootfs": true,
		"CapDrop":        []string{"ALL"},
		"SecurityOpt":    []string{"no-new-privileges"},
		"Tmpfs":          map[string]string{"/tmp": "rw,noexec,nosuid,size=64m"},
		"AutoRemove":     true,
	}
	if spec.MemoryMB > 0 {
		host["Memory"] = spec.MemoryMB * 1024 * 1024
	}
	if spec.CPUs > 0 {
		host["NanoCpus"] = int64(spec.CPUs * 1e9)
	}
	if spec.PIDs > 0 {
		host["PidsLimit"] = spec.PIDs
	}
	if d.Runtime != "" {
		host["Runtime"] = d.Runtime
	}
	body := map[string]any{
		"Image":      spec.Image,
		"User":       user,
		"Env":        []string{"GOAP_HTTP_ADDR=:8080", "GOAP_SANDBOX_ID=" + spec.ID},
		"Labels":     map[string]string{"goap.sandbox": spec.ID, "goap.process": spec.ProcessID},
		"HostConfig": host,
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := d.do(ctx, http.MethodPost, "/containers/create?name="+url.QueryEscape(spec.ID), body, &created); err != nil {
		return Instance{}, err
	}
	if err := d.do(ctx, http.MethodPost, "/containers/"+created.ID+"/start", nil, nil); err != nil {
		_ = d.do(context.Background(), http.MethodDelete, "/containers/"+created.ID+"?force=true", nil, nil)
		return Instance{}, err
	}
	// the container name resolves on the sandbox network
	return Instance{ID: spec.ID, Endpoint: "http://" + spec.ID + ":8080"}, nil
}

// Stop implements Provisioner.
func (d *DockerProvisioner) Stop(ctx context.Context, id string) error {
	return d.do(ctx, http.MethodDelete, "/containers/"+url.PathEscape(id)+"?force=true", nil, nil)
}
