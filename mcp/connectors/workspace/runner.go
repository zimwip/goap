package workspace

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"time"
)

// runner runs the commands of the workspaces (ADR 0118 §3, §7): on the host for the local connector, in the
// container of the workspace for the docker one. Files and git stay on the host either way: the workspace is a
// directory of the connector's host, and the token of the remote never reaches a command.
type runner interface {
	// start starts a command line in the directory cwd of the workspace (relative), writing its outputs to stdout
	// and stderr; the command is killed when ctx ends.
	start(ctx context.Context, s space, line, cwd string, stdout, stderr io.Writer) (process, error)
	// remove removes what the runner keeps for a workspace (its container), when it is closed.
	remove(ctx context.Context, s space) error
	// status describes what the runner keeps for a workspace (nil: nothing).
	status(ctx context.Context, s space) map[string]any
}

// process is a started command.
type process interface {
	// wait waits for the end of the command and its outputs: its exit code, or an error when it could not tell.
	wait() (int, error)
}

// localRunner runs the commands on the host, in their own process group, with the cleaned environment.
type localRunner struct{}

type localProcess struct{ cmd *exec.Cmd }

func (localRunner) start(ctx context.Context, s space, line, cwd string, stdout, stderr io.Writer) (process, error) {
	cmd, err := command(ctx, s, line, cwd)
	if err != nil {
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return localProcess{cmd}, nil
}

func (p localProcess) wait() (int, error) {
	err := p.cmd.Wait()
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		return exit.ExitCode(), nil
	case err != nil:
		return -1, err
	}
	return 0, nil
}

func (localRunner) remove(context.Context, space) error          { return nil }
func (localRunner) status(context.Context, space) map[string]any { return nil }

func command(ctx context.Context, s space, line, cwd string) (*exec.Cmd, error) {
	dir, err := workdir(s, cwd)
	if err != nil {
		return nil, err
	}
	argv := shell(line)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.Env = dir, environment(s)
	cmd.Stdin = nil
	ownGroup(cmd)
	cmd.Cancel = func() error { return killGroup(cmd) }
	cmd.WaitDelay = 2 * time.Second
	return cmd, nil
}
