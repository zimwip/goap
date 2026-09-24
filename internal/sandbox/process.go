package sandbox

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sync"
)

// ProcessProvisioner runs goap-runner as a separate OS process (bare metal).
// The child gets an empty environment, its own process group and a private
// working directory. Real isolation on bare metal requires a Wrapper such as
// bubblewrap or nsjail (e.g. "bwrap --unshare-all --share-net --die-with-parent
// --ro-bind /usr /usr --tmpfs /tmp --"), and/or a dedicated UID.
type ProcessProvisioner struct {
	// Binary is the goap-runner executable.
	Binary string
	// Wrapper is prepended to the command (sandboxing tool).
	Wrapper []string
	// UID / GID run the sandbox as another user (requires privileges; 0 = unchanged).
	UID, GID uint32

	mu    sync.Mutex
	procs map[string]*exec.Cmd
}

var _ Provisioner = (*ProcessProvisioner)(nil)

func (p *ProcessProvisioner) Name() string { return "process" }

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// Start implements Provisioner.
func (p *ProcessProvisioner) Start(_ context.Context, spec Spec) (Instance, error) {
	if p.Binary == "" {
		return Instance{}, errors.New("process provisioner: runner binary not configured")
	}
	port, err := freePort()
	if err != nil {
		return Instance{}, err
	}
	dir, err := os.MkdirTemp("", spec.ID+"-")
	if err != nil {
		return Instance{}, err
	}
	args := append(append([]string{}, p.Wrapper...), p.Binary)
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	// no inherited environment: no secrets, no credentials leak into the sandbox
	cmd.Env = []string{fmt.Sprintf("GOAP_HTTP_ADDR=127.0.0.1:%d", port), "GOAP_LOG_FORMAT=text", "GOAP_SANDBOX_ID=" + spec.ID}
	configureSysProcAttr(cmd, p.UID, p.GID)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		os.RemoveAll(dir)
		return Instance{}, err
	}
	p.mu.Lock()
	if p.procs == nil {
		p.procs = map[string]*exec.Cmd{}
	}
	p.procs[spec.ID] = cmd
	p.mu.Unlock()
	go func() {
		_ = cmd.Wait()
		os.RemoveAll(dir)
	}()
	return Instance{ID: spec.ID, Endpoint: fmt.Sprintf("http://127.0.0.1:%d", port)}, nil
}

// Stop implements Provisioner: the whole process group is killed.
func (p *ProcessProvisioner) Stop(_ context.Context, id string) error {
	p.mu.Lock()
	cmd, ok := p.procs[id]
	delete(p.procs, id)
	p.mu.Unlock()
	if !ok || cmd.Process == nil {
		return nil
	}
	return killProcessGroup(cmd)
}
