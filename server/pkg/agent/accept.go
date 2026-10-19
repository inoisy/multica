package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// AcceptProvider is the runtime of the route's accept step. It runs no model:
// the daemon's own `multica` binary checks the commit the route accepted on
// top of the base branch and merges it (`multica issue conveyor accept`).
const AcceptProvider = "accept"

// acceptBackend runs the acceptance command of the multica CLI for the task's
// issue. The prompt is ignored; the issue comes from MULTICA_ISSUE_ID in the
// task environment, and the command reports its outcome to the route itself.
type acceptBackend struct {
	cfg Config
}

func (b *acceptBackend) Execute(ctx context.Context, _ string, opts ExecOptions) (*Session, error) {
	execPath := b.cfg.ExecutablePath
	if execPath == "" {
		self, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("accept: resolve the multica executable: %w", err)
		}
		execPath = self
	}
	if strings.TrimSpace(b.cfg.Env["MULTICA_ISSUE_ID"]) == "" {
		return nil, errors.New("accept: the task has no issue (MULTICA_ISSUE_ID is empty)")
	}

	runCtx, cancel := runContext(ctx, opts.Timeout)
	args := []string{"issue", "conveyor", "accept"}
	cmd := b.cfg.commandAt(execPath).exec(runCtx, args...)
	hideAgentWindow(cmd)
	cmd.WaitDelay = 10 * time.Second
	b.cfg.logAgentCommand(cmd, newAgentCommandLogArgs(args))
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	cmd.Env = buildEnv(b.cfg.Env)

	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("accept stdout pipe: %w", err)
	}
	// One stream, in the order the command wrote it: the log reads as a run.
	cmd.Stderr = cmd.Stdout

	if err := startOwnedProcessTree(cmd, b.cfg.Logger); err != nil {
		cancel()
		return nil, fmt.Errorf("start accept: %w", err)
	}

	msgCh := make(chan Message, 256)
	resCh := make(chan Result, 1)
	var observed sync.Once
	terminal := make(chan struct{})
	started := time.Now()

	go func() {
		defer cancel()
		defer releaseProcessGroup(cmd)
		tail := newAcceptTail(40)
		scanner := bufio.NewScanner(out)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			tail.add(line)
			select {
			case msgCh <- Message{Type: MessageLog, Level: "info", Content: line}:
			case <-runCtx.Done():
			}
		}
		_, _ = io.Copy(io.Discard, out)
		waitErr := cmd.Wait()
		close(msgCh)

		res := Result{Status: "completed", Output: tail.last(), DurationMs: time.Since(started).Milliseconds()}
		switch {
		case errors.Is(runCtx.Err(), context.DeadlineExceeded):
			res.Status, res.Error = "timeout", "accept timed out"
		case ctx.Err() != nil:
			res.Status, res.Error = "cancelled", "accept cancelled"
		case waitErr != nil:
			res.Status, res.Error = "failed", fmt.Sprintf("accept exited: %v\n%s", waitErr, tail.text())
		}
		observed.Do(func() { close(terminal) })
		resCh <- res
		close(resCh)
	}()

	return &Session{
		Messages: msgCh,
		Result:   resCh,
		TerminalObserved: func() bool {
			select {
			case <-terminal:
				return true
			default:
				return false
			}
		},
	}, nil
}

// acceptTail keeps the last lines of the run for the result and the error.
type acceptTail struct {
	lines []string
	max   int
}

func newAcceptTail(max int) *acceptTail { return &acceptTail{max: max} }

func (t *acceptTail) add(line string) {
	t.lines = append(t.lines, line)
	if len(t.lines) > t.max {
		t.lines = t.lines[len(t.lines)-t.max:]
	}
}

func (t *acceptTail) text() string { return strings.Join(t.lines, "\n") }

func (t *acceptTail) last() string {
	for i := len(t.lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(t.lines[i]); s != "" {
			return s
		}
	}
	return ""
}
