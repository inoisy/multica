package subscriptions

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"syscall"
	"time"
)

// ManualCommand is a claude or codex process an instance admin starts by hand
// on one account. It is not an agent task: the caller's terminal is its
// stdin, stdout and stderr.
type ManualCommand struct {
	Path   string
	Args   []string
	Env    []string
	Dir    string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// manualKillAfter is how long a process may take to leave after it was asked
// to stop before it is killed.
var manualKillAfter = 10 * time.Second

// RunManual runs c to the end and returns its exit code. Cancelling ctx asks
// the process to terminate and kills it after manualKillAfter. A process that
// did not start, or ended on a signal, yields -1.
func RunManual(ctx context.Context, c ManualCommand) (int, error) {
	cmd := exec.CommandContext(ctx, c.Path, c.Args...)
	cmd.Env, cmd.Dir = c.Env, c.Dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = c.Stdin, c.Stdout, c.Stderr
	cmd.Cancel = func() error {
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = manualKillAfter
	err := cmd.Run()
	if cmd.ProcessState == nil {
		return -1, err
	}
	code := cmd.ProcessState.ExitCode()
	var exitErr *exec.ExitError
	if err == nil || errors.As(err, &exitErr) || ctx.Err() != nil {
		return code, nil
	}
	return code, err
}
