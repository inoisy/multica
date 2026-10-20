//go:build !windows

package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeMultica writes a script that stands in for `multica issue conveyor accept`.
func fakeMultica(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "multica")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func runAccept(t *testing.T, exe string, env map[string]string) ([]Message, Result) {
	t.Helper()
	backend, err := ResolveBackend(AcceptProvider, Config{ExecutablePath: exe, Env: env})
	if err != nil {
		t.Fatal(err)
	}
	session, err := backend.Execute(context.Background(), "ignored prompt", ExecOptions{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var msgs []Message
	for m := range session.Messages {
		msgs = append(msgs, m)
	}
	res := <-session.Result
	if !session.TerminalObserved() {
		t.Fatal("result delivered before the terminal was observed")
	}
	return msgs, res
}

func TestAcceptBackendRunsTheAcceptCommand(t *testing.T) {
	exe := fakeMultica(t, `echo "args: $*"; echo "issue: $MULTICA_ISSUE_ID"; echo "to stderr" >&2; echo "accept: pass — merged"`)
	msgs, res := runAccept(t, exe, map[string]string{"MULTICA_ISSUE_ID": "issue-1"})
	if res.Status != "completed" || res.Output != "accept: pass — merged" {
		t.Fatalf("result %+v", res)
	}
	var lines []string
	for _, m := range msgs {
		if m.Type != MessageLog {
			t.Fatalf("message type %s", m.Type)
		}
		lines = append(lines, m.Content)
	}
	got := strings.Join(lines, "\n")
	for _, want := range []string{"args: issue conveyor accept", "issue: issue-1", "to stderr"} {
		if !strings.Contains(got, want) {
			t.Fatalf("log misses %q:\n%s", want, got)
		}
	}
}

func TestAcceptBackendReportsAFailedCommand(t *testing.T) {
	exe := fakeMultica(t, `echo "hand in failed" >&2; exit 2`)
	_, res := runAccept(t, exe, map[string]string{"MULTICA_ISSUE_ID": "issue-1"})
	if res.Status != "failed" || !strings.Contains(res.Error, "hand in failed") {
		t.Fatalf("result %+v", res)
	}
}

func TestAcceptBackendNeedsAnIssue(t *testing.T) {
	backend, err := ResolveBackend(AcceptProvider, Config{ExecutablePath: fakeMultica(t, "true")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Execute(context.Background(), "", ExecOptions{}); err == nil || !strings.Contains(err.Error(), "MULTICA_ISSUE_ID") {
		t.Fatalf("err = %v", err)
	}
	if IsSupportedType(AcceptProvider) {
		t.Fatal("accept must not be a protocol family custom runtimes can claim")
	}
}
