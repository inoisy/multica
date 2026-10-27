package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/internal/daemon/subscriptions"
)

type manualRecorded struct {
	Method string
	Path   string
	Auth   string
	Raw    string
	Body   map[string]any
}

// manualServer plays the server side of a manual run. Replies can be changed
// per test through the fields before the first request.
type manualServer struct {
	mu          sync.Mutex
	reqs        []manualRecorded
	sub         map[string]any
	capExceeded bool
	openStatus  int
	openBody    string
	finishCode  int
}

func (m *manualServer) find(method, suffix string) []manualRecorded {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []manualRecorded
	for _, r := range m.reqs {
		if r.Method == method && strings.HasSuffix(r.Path, suffix) {
			out = append(out, r)
		}
	}
	return out
}

func (m *manualServer) all() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var b strings.Builder
	for _, r := range m.reqs {
		b.WriteString(r.Method + " " + r.Path + " " + r.Raw + "\n")
	}
	return b.String()
}

func newManualServer(t *testing.T, sub map[string]any) (*manualServer, *cli.APIClient) {
	t.Helper()
	m := &manualServer{sub: sub, openStatus: http.StatusCreated, finishCode: http.StatusOK}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		rec := manualRecorded{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Raw: string(raw)}
		_ = json.Unmarshal(raw, &rec.Body)
		m.mu.Lock()
		m.reqs = append(m.reqs, rec)
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/subscriptions/"+sub["key"].(string):
			_ = json.NewEncoder(w).Encode(m.sub)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/manual-runs"):
			w.WriteHeader(m.openStatus)
			if m.openBody != "" {
				_, _ = w.Write([]byte(m.openBody))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "run-1", "agent_cap_exceeded": m.capExceeded})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/finish"):
			w.WriteHeader(m.finishCode)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/usage"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
		default:
			http.Error(w, `{"error":"unexpected"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return m, cli.NewAPIClient(srv.URL, "", "admin-login-token")
}

func manualScript(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts stand in for the CLIs")
	}
	path := filepath.Join(t.TempDir(), "fake-cli")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func claudeSub(t *testing.T, extra map[string]any) map[string]any {
	t.Helper()
	cred := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(cred, []byte("sk-ant-oat01-secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := map[string]any{"id": "sub-1", "key": "claude-3", "provider": "claude", "family": "claude",
		"auth": "claude_oauth", "credential_path": cred, "effective_caps": map[string]int{"week": 90}}
	for k, v := range extra {
		sub[k] = v
	}
	return sub
}

type manualRig struct {
	deps   manualRunDeps
	stdout *bytes.Buffer
	stderr *bytes.Buffer
}

func newManualRig(client *cli.APIClient, cliPath string) *manualRig {
	rig := &manualRig{stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	rig.deps = manualRunDeps{
		client: client, daemonID: "daemon-1",
		baseEnv: []string{"PATH=" + os.Getenv("PATH"), "ANTHROPIC_API_KEY=leaked-from-shell"},
		stdin:   strings.NewReader(""), stdout: rig.stdout, stderr: rig.stderr,
		lookPath: func(string) (string, error) { return cliPath, nil },
		probe: func(context.Context, subscriptions.Subscription) subscriptions.Report {
			return subscriptions.Report{State: subscriptions.State{Windows: []subscriptions.Window{{Key: "5h", Used: 12}}}}
		},
	}
	return rig
}

func TestManualRunPassesArgsAndExitCodeAndKeepsSecretsOutOfTheJournal(t *testing.T) {
	m, client := newManualServer(t, claudeSub(t, nil))
	rig := newManualRig(client, manualScript(t, `echo "args:$*"; echo "token:$CLAUDE_CODE_OAUTH_TOKEN"; echo "apikey:[$ANTHROPIC_API_KEY]"; exit 3`))
	code, err := executeManualRun(context.Background(), rig.deps, "claude-3", []string{"-p", "my private prompt"})
	if err != nil || code != 3 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	out := rig.stdout.String()
	for _, want := range []string{"args:-p my private prompt", "token:sk-ant-oat01-secret-token", "apikey:[]"} {
		if !strings.Contains(out, want) {
			t.Errorf("child output lacks %q:\n%s", want, out)
		}
	}
	open := m.find(http.MethodPost, "/manual-runs")
	if len(open) != 1 || len(open[0].Body) != 2 || open[0].Body["daemon_id"] != "daemon-1" || open[0].Body["command"] != "claude" {
		t.Fatalf("open request: %+v", open)
	}
	finish := m.find(http.MethodPost, "/manual-runs/run-1/finish")
	if len(finish) != 1 || finish[0].Body["exit_code"] != float64(3) || len(finish[0].Body) != 1 {
		t.Fatalf("finish request: %+v", finish)
	}
	for _, secret := range []string{"sk-ant-oat01-secret-token", "my private prompt", "leaked-from-shell"} {
		if strings.Contains(m.all(), secret) {
			t.Errorf("%q reached the server:\n%s", secret, m.all())
		}
		if strings.Contains(rig.stderr.String(), secret) {
			t.Errorf("%q reached the CLI's own messages:\n%s", secret, rig.stderr.String())
		}
	}
	usage := m.find(http.MethodPost, "/usage")
	if len(usage) != 1 || !strings.Contains(usage[0].Raw, `"window":"5h"`) || !strings.Contains(usage[0].Raw, `"source":"manual-run"`) {
		t.Fatalf("quota reading after the run: %+v", usage)
	}
}

func TestManualRunAPIKeyDropsModelFlags(t *testing.T) {
	cred := filepath.Join(t.TempDir(), "key")
	_ = os.WriteFile(cred, []byte("provider-key\n"), 0o600)
	sub := claudeSub(t, map[string]any{"key": "zai-1", "provider": "zai", "auth": "api_key", "credential_path": cred,
		"settings": map[string]any{"env": map[string]string{"ANTHROPIC_BASE_URL": "https://api.example/anthropic"}}})
	m, client := newManualServer(t, sub)
	rig := newManualRig(client, manualScript(t, `echo "args:$*"; echo "base:$ANTHROPIC_BASE_URL"`))
	code, err := executeManualRun(context.Background(), rig.deps, "zai-1", []string{"--model", "opus", "-p", "hi", "--model=x"})
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if out := rig.stdout.String(); !strings.Contains(out, "args:-p hi\n") || !strings.Contains(out, "base:https://api.example/anthropic") {
		t.Fatalf("child output:\n%s", out)
	}
	if !strings.Contains(rig.stderr.String(), "--model") {
		t.Errorf("the dropped flag is not mentioned: %q", rig.stderr.String())
	}
	if strings.Contains(m.all(), "provider-key") {
		t.Error("the API key reached the server")
	}
}

func TestManualRunBlockedBeforeJournalAndStart(t *testing.T) {
	failVLESS := manualScript(t, "echo 'нет выхода DE' >&2\nexit 1\n")
	cases := map[string]struct {
		sub  func(t *testing.T) map[string]any
		vpn  string
		want string
	}{
		"VLESS down": {sub: func(t *testing.T) map[string]any { return claudeSub(t, nil) }, vpn: failVLESS, want: "VLESS"},
		"missing credential": {sub: func(t *testing.T) map[string]any {
			return claudeSub(t, map[string]any{"credential_path": "/nonexistent/token"})
		}, want: "нет токена"},
		"unknown family": {sub: func(t *testing.T) map[string]any { return claudeSub(t, map[string]any{"family": "gemini"}) }, want: "no CLI"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m, client := newManualServer(t, tc.sub(t))
			marker := filepath.Join(t.TempDir(), "started")
			rig := newManualRig(client, manualScript(t, "touch "+marker))
			rig.deps.requireVLESS = tc.vpn
			if _, err := executeManualRun(context.Background(), rig.deps, "claude-3", nil); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if len(m.find(http.MethodPost, "/manual-runs")) != 0 {
				t.Error("a refused run was entered in the journal")
			}
			if _, err := os.Stat(marker); err == nil {
				t.Error("the CLI was started")
			}
		})
	}
}

func TestManualRunCodexPointsCodexHomeAtTheCredential(t *testing.T) {
	cred := filepath.Join(t.TempDir(), "auth.json")
	_ = os.WriteFile(cred, []byte(`{"tokens":{}}`), 0o600)
	sub := map[string]any{"id": "sub-2", "key": "codex-1", "provider": "codex", "family": "codex", "auth": "codex_auth", "credential_path": cred}
	m, client := newManualServer(t, sub)
	rig := newManualRig(client, manualScript(t, `readlink "$CODEX_HOME/auth.json"; echo "$CODEX_HOME"`))
	code, err := executeManualRun(context.Background(), rig.deps, "codex-1", nil)
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	lines := strings.Split(strings.TrimSpace(rig.stdout.String()), "\n")
	if len(lines) != 2 || lines[0] != cred {
		t.Fatalf("output: %q", rig.stdout.String())
	}
	if _, err := os.Stat(lines[1]); err == nil {
		t.Error("the scratch CODEX_HOME was left behind")
	}
	if open := m.find(http.MethodPost, "/manual-runs"); len(open) != 1 || open[0].Body["command"] != "codex" {
		t.Fatalf("open request: %+v", open)
	}
}

func TestManualRunOverAgentLimitWarnsAndRuns(t *testing.T) {
	m, client := newManualServer(t, claudeSub(t, nil))
	m.capExceeded = true
	rig := newManualRig(client, manualScript(t, `echo ran`))
	code, err := executeManualRun(context.Background(), rig.deps, "claude-3", nil)
	if err != nil || code != 0 || !strings.Contains(rig.stdout.String(), "ran") {
		t.Fatalf("code=%d err=%v out=%q", code, err, rig.stdout.String())
	}
	if !strings.Contains(rig.stderr.String(), "over its limit for agents") {
		t.Errorf("no warning: %q", rig.stderr.String())
	}
}

func TestManualRunCancelStopsChildAndClosesJournal(t *testing.T) {
	m, client := newManualServer(t, claudeSub(t, nil))
	rig := newManualRig(client, manualScript(t, `exec sleep 30`))
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for len(m.find(http.MethodPost, "/manual-runs")) == 0 {
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	code, err := executeManualRun(ctx, rig.deps, "claude-3", nil)
	if err != nil || code != 130 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if time.Since(start) > 8*time.Second {
		t.Fatal("the child outlived Ctrl-C")
	}
	finish := m.find(http.MethodPost, "/finish")
	if len(finish) != 1 || finish[0].Body["exit_code"] != float64(-1) {
		t.Fatalf("the cancelled run was not closed: %+v", finish)
	}
}

func TestManualRunSurvivesFailedClose(t *testing.T) {
	m, client := newManualServer(t, claudeSub(t, nil))
	m.finishCode = http.StatusServiceUnavailable
	rig := newManualRig(client, manualScript(t, `exit 2`))
	code, err := executeManualRun(context.Background(), rig.deps, "claude-3", nil)
	if err != nil || code != 2 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if !strings.Contains(rig.stderr.String(), "not finished") {
		t.Errorf("no word about the journal entry: %q", rig.stderr.String())
	}
}

func TestManualRunOnServerWithoutJournal(t *testing.T) {
	m, client := newManualServer(t, claudeSub(t, nil))
	m.openStatus, m.openBody = http.StatusNotFound, "404 page not found\n"
	marker := filepath.Join(t.TempDir(), "started")
	rig := newManualRig(client, manualScript(t, "touch "+marker))
	if _, err := executeManualRun(context.Background(), rig.deps, "claude-3", nil); err == nil || !strings.Contains(err.Error(), "update the server") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the CLI started without a journal")
	}
}

func TestManualRunRefusedForNonAdmin(t *testing.T) {
	m, client := newManualServer(t, claudeSub(t, nil))
	m.openStatus, m.openBody = http.StatusForbidden, `{"error":"only instance admins (MULTICA_INSTANCE_ADMINS) can change subscriptions"}`
	rig := newManualRig(client, manualScript(t, "echo ran"))
	_, err := executeManualRun(context.Background(), rig.deps, "claude-3", nil)
	if err == nil || !strings.Contains(err.Error(), "only instance admins") || strings.Contains(rig.stdout.String(), "ran") {
		t.Fatalf("err = %v, out = %q", err, rig.stdout.String())
	}
}

// The journal is for the admin in person: the request carries the login from
// 'multica login', and nothing that marks a task or an agent.
func TestManualRunClientUsesPersonalLogin(t *testing.T) {
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		_, _ = w.Write([]byte(`{"key":"claude-3","runs":[]}`))
	}))
	defer srv.Close()
	newCmd := func(t *testing.T) *cobra.Command {
		t.Helper()
		t.Setenv("HOME", t.TempDir())
		t.Setenv("MULTICA_TOKEN", "")
		t.Setenv("MULTICA_AGENT_ID", "")
		t.Setenv("MULTICA_TASK_ID", "")
		t.Setenv("MULTICA_DAEMON_PORT", "")
		t.Setenv("MULTICA_SERVER_URL", srv.URL)
		if err := cli.SaveCLIConfigForProfile(cli.CLIConfig{Token: "mul_personal_login"}, ""); err != nil {
			t.Fatal(err)
		}
		return subscriptionTestCmd(func(*cobra.Command) {})
	}

	t.Run("login token", func(t *testing.T) {
		cmd := newCmd(t)
		client, err := newManualRunClient(cmd, "multica subscription runs")
		if err != nil {
			t.Fatal(err)
		}
		if err := client.GetJSON(context.Background(), "/api/subscriptions/claude-3/manual-runs", &struct{}{}); err != nil {
			t.Fatal(err)
		}
		if gotHeaders.Get("Authorization") != "Bearer mul_personal_login" {
			t.Errorf("Authorization = %q", gotHeaders.Get("Authorization"))
		}
		if gotHeaders.Get("X-Agent-ID") != "" || gotHeaders.Get("X-Task-ID") != "" {
			t.Errorf("task headers: agent=%q task=%q", gotHeaders.Get("X-Agent-ID"), gotHeaders.Get("X-Task-ID"))
		}
	})
	t.Run("task token", func(t *testing.T) {
		cmd := newCmd(t)
		t.Setenv("MULTICA_TOKEN", "mat_task_token")
		if _, err := newManualRunClient(cmd, "multica subscription runs"); err == nil || !strings.Contains(err.Error(), "own login") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("inside an agent task", func(t *testing.T) {
		cmd := newCmd(t)
		t.Setenv("MULTICA_TASK_ID", "task-1")
		t.Setenv("MULTICA_TOKEN", "mat_task_token")
		if _, err := newManualRunClient(cmd, "multica subscription run"); err == nil || !strings.Contains(err.Error(), "not available inside a daemon-managed task") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("not signed in", func(t *testing.T) {
		cmd := newCmd(t)
		t.Setenv("HOME", t.TempDir())
		if _, err := newManualRunClient(cmd, "multica subscription runs"); err == nil || !strings.Contains(err.Error(), "multica login") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestManualRunArgsComeAfterDash(t *testing.T) {
	parse := func(argv ...string) (string, []string, error) {
		var key string
		var rest []string
		var perr error
		cmd := &cobra.Command{Use: "run", Args: cobra.MinimumNArgs(1), RunE: func(c *cobra.Command, args []string) error {
			key, rest, perr = manualRunArgs(c, args)
			return perr
		}}
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(argv)
		err := cmd.Execute()
		return key, rest, err
	}
	key, args, err := parse("claude-3", "--", "-p", "--model", "x")
	if err != nil || key != "claude-3" || strings.Join(args, " ") != "-p --model x" {
		t.Fatalf("key=%q args=%v err=%v", key, args, err)
	}
	if key, args, err = parse("claude-3"); err != nil || key != "claude-3" || len(args) != 0 {
		t.Fatalf("key only: %q %v %v", key, args, err)
	}
	if _, _, err = parse("claude-3", "extra"); err == nil || !strings.Contains(err.Error(), "after --") {
		t.Fatalf("arguments without -- : %v", err)
	}
}

func TestSubscriptionRunsListsJournal(t *testing.T) {
	reply := map[string]any{"key": "claude-3", "next_before": "2026-10-26T10:00:00Z", "runs": []map[string]any{
		{"id": "r1", "user_name": "Евгений", "daemon_id": "host-1", "command": "claude", "started_at": "2026-10-26T11:00:00Z",
			"finished_at": "2026-10-26T11:05:00Z", "exit_code": 0, "agent_cap_exceeded": true},
		{"id": "r2", "user_name": "Евгений", "daemon_id": "host-1", "command": "codex", "started_at": "2026-10-26T10:30:00Z",
			"finished_at": nil, "exit_code": nil, "unfinished": true},
	}}
	got := subscriptionTestServer(t, map[string]any{"GET /api/subscriptions/claude-3/manual-runs": reply})
	t.Setenv("MULTICA_TOKEN", "mul_admin")

	cmd := subscriptionTestCmd(func(c *cobra.Command) {
		c.Flags().Int("limit", 20, "")
		c.Flags().String("before", "", "")
	})
	_ = cmd.Flags().Set("limit", "2")
	_ = cmd.Flags().Set("before", "2026-10-26T12:00:00Z")
	_ = cmd.Flags().Set("output", "table")
	out, err := captureStdout(t, func() error { return runSubscriptionRuns(cmd, []string{"claude-3"}) })
	if err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 || (*got)[0].Query != "before=2026-10-26T12%3A00%3A00Z&limit=2" {
		t.Fatalf("request: %+v", *got)
	}
	for _, want := range []string{"STARTED", "Евгений", "host-1", "claude", "codex", "not finished", "over the agent limit"} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}

	_ = cmd.Flags().Set("output", "json")
	out, err = captureStdout(t, func() error { return runSubscriptionRuns(cmd, []string{"claude-3"}) })
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Runs []map[string]any `json:"runs"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil || len(parsed.Runs) != 2 {
		t.Fatalf("json output: %v\n%s", err, out)
	}
}

// A server without the journal answers with the router's plain 404; the
// server's own "subscription not found" is a JSON error and stays as it is.
func TestManualRunErrorTellsOldServerFromUnknownKey(t *testing.T) {
	old := &cli.HTTPError{StatusCode: http.StatusNotFound, Body: "404 page not found\n"}
	if got := manualRunError("manual runs", old); !strings.Contains(got.Error(), "update the server") {
		t.Fatalf("old server: %v", got)
	}
	unknown := &cli.HTTPError{StatusCode: http.StatusNotFound, Body: `{"error":"subscription not found"}`}
	if got := manualRunError("manual runs", unknown); !strings.Contains(got.Error(), "subscription not found") || strings.Contains(got.Error(), "update the server") {
		t.Fatalf("unknown key: %v", got)
	}
}

func TestAgentUpdateExplainsProfileOwnedModel(t *testing.T) {
	err := &cli.HTTPError{StatusCode: http.StatusConflict,
		Body: `{"error":"the runtime, model and thinking level of this agent come from its model profile; change them on the Models tab of the agent","code":"agent_model_owned_by_profile"}`}
	got := agentUpdateError("builder", err)
	if got == nil || !strings.Contains(got.Error(), "multica agent models builder") || strings.Contains(got.Error(), "409") {
		t.Fatalf("message = %v", got)
	}
	other := agentUpdateError("builder", &cli.HTTPError{StatusCode: http.StatusConflict, Body: `{"code":"something_else"}`})
	if other == nil || strings.Contains(other.Error(), "multica agent models") {
		t.Fatalf("another conflict was rewritten: %v", other)
	}
}
