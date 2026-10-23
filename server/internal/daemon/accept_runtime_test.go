package daemon

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/agent"
)

func TestEveryDaemonOffersTheAcceptRuntime(t *testing.T) {
	t.Setenv("MULTICA_ACCEPT_RUNTIME", "")
	entry, ok := probeAgentCLIs()[agent.AcceptProvider]
	self, _ := os.Executable()
	if !ok || entry.Path != self {
		t.Fatalf("accept runtime = %+v (ok=%v), want the daemon's own binary %s", entry, ok, self)
	}
	t.Setenv("MULTICA_ACCEPT_RUNTIME", "off")
	if _, ok := probeAgentCLIs()[agent.AcceptProvider]; ok {
		t.Fatal("MULTICA_ACCEPT_RUNTIME=off still offers the accept runtime")
	}
}

func TestTaskEnvironmentNamesTheIssue(t *testing.T) {
	env := taskMulticaEnvironment(Task{ID: "t", IssueID: "issue-1"}, "codex", "a", "mat_x", "", "", "", 1, 0, "")
	if env["MULTICA_ISSUE_ID"] != "issue-1" {
		t.Fatalf("MULTICA_ISSUE_ID = %q", env["MULTICA_ISSUE_ID"])
	}
	if _, ok := taskMulticaEnvironment(Task{ID: "t"}, "codex", "a", "mat_x", "", "", "", 1, 0, "")["MULTICA_ISSUE_ID"]; ok {
		t.Fatal("a run without an issue got MULTICA_ISSUE_ID")
	}
}

// The accept runtime goes through the daemon's real task path: workdir,
// task environment and the accept backend, with a test-created multica.
func TestRunTaskRunsTheAcceptStep(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	fake := filepath.Join(t.TempDir(), "multica")
	writeTestExecutable(t, fake, []byte("#!/bin/sh\ncase \"$*\" in *--version*) echo 'multica 1.2.3'; exit 0;; esac\n"+
		"echo \"ran: $* for $MULTICA_ISSUE_ID with $MULTICA_TOKEN in $(basename \"$PWD\")\"\necho 'accept: pass — merged'\n"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
	defer srv.Close()
	d := &Daemon{
		client: NewClient(srv.URL), logger: logger,
		workspaces: make(map[string]*workspaceState), activeEnvRoots: make(map[string]int),
		runtimeIndex: map[string]Runtime{"accept-rt": {ID: "accept-rt", Provider: agent.AcceptProvider}},
		cfg: Config{
			WorkspacesRoot: t.TempDir(), AgentTimeout: 5 * time.Second, ServerBaseURL: srv.URL,
			Agents: map[string]AgentEntry{agent.AcceptProvider: {Path: fake}},
		},
	}
	task := Task{
		ID: "accept-task", WorkspaceID: "ws", RuntimeID: "accept-rt", IssueID: "issue-7",
		AgentID: "acceptor", AuthToken: "mat_accept", Agent: &AgentData{ID: "acceptor", Name: "Приёмщик"},
	}
	result, err := d.runTask(context.Background(), task, agent.AcceptProvider, 0, logger)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" {
		t.Fatalf("result %+v", result)
	}
	if !strings.Contains(result.Comment, "accept: pass") {
		t.Fatalf("output %q", result.Comment)
	}
}
