package subscriptions

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func writeScript(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts stand in for the CLIs")
	}
	path := filepath.Join(t.TempDir(), "cli")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLaunchEnvironReplacesAndClears(t *testing.T) {
	l := Launch{Env: map[string]string{"ANTHROPIC_API_KEY": "", "CLAUDE_CODE_OAUTH_TOKEN": "tok"}}
	got := l.Environ([]string{"PATH=/bin", "ANTHROPIC_API_KEY=leak", "CLAUDE_CODE_OAUTH_TOKEN=old"})
	want := []string{"PATH=/bin", "CLAUDE_CODE_OAUTH_TOKEN=tok"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("environ = %v, want %v", got, want)
	}
}

func TestLaunchFilterArgs(t *testing.T) {
	args := []string{"--model", "opus", "-p", "hi"}
	if got := (Launch{DropModel: true}).FilterArgs(args); strings.Join(got, " ") != "-p hi" {
		t.Fatalf("API-key provider keeps its model: %v", got)
	}
	if got := (Launch{}).FilterArgs(args); strings.Join(got, " ") != strings.Join(args, " ") {
		t.Fatalf("a subscription passes arguments as they are: %v", got)
	}
}

func TestRunManualPassesStreamsAndExitCode(t *testing.T) {
	path := writeScript(t, "read line; echo \"out:$line:$1:$FOO\"; echo err >&2; exit 7\n")
	var stdout, stderr bytes.Buffer
	code, err := RunManual(context.Background(), ManualCommand{
		Path: path, Args: []string{"arg1"}, Env: []string{"FOO=bar"},
		Stdin: strings.NewReader("hello\n"), Stdout: &stdout, Stderr: &stderr,
	})
	if err != nil || code != 7 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if stdout.String() != "out:hello:arg1:bar\n" || stderr.String() != "err\n" {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunManualCancelStopsChild(t *testing.T) {
	path := writeScript(t, "exec sleep 30\n")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	code, err := RunManual(ctx, ManualCommand{Path: path, Env: os.Environ()})
	if err != nil || code != -1 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("the child outlived the cancel")
	}
}

func TestRunManualKillsChildThatIgnoresTerm(t *testing.T) {
	old := manualKillAfter
	manualKillAfter = 300 * time.Millisecond
	t.Cleanup(func() { manualKillAfter = old })
	path := writeScript(t, "trap '' TERM\nwhile :; do sleep 1; done\n")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	RunManual(ctx, ManualCommand{Path: path, Env: os.Environ()})
	if time.Since(start) > 10*time.Second {
		t.Fatal("a child that ignores SIGTERM was not killed")
	}
}

func TestRunManualMissingBinary(t *testing.T) {
	code, err := RunManual(context.Background(), ManualCommand{Path: filepath.Join(t.TempDir(), "nope")})
	if err == nil || code != -1 {
		t.Fatalf("code=%d err=%v", code, err)
	}
}
