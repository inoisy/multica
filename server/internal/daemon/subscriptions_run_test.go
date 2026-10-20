package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/daemon/subscriptions"
)

func TestPrepareSubscriptionRun(t *testing.T) {
	dir := t.TempDir()
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte("sk-ant-oat01-run\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := freshDaemon("")
	fresh := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	pool := &subscriptions.Pool{SpendOrder: "first", Subscriptions: []subscriptions.Subscription{{
		Key: "c", Family: "claude", Auth: "claude_oauth", CredentialPath: token, EffectiveCaps: map[string]int{"week": 90},
		Usage: []subscriptions.Usage{{Window: "week", UsedPercent: 10, ObservedAt: fresh}},
	}}}
	run, err := d.prepareSubscriptionRun(context.Background(), Task{SubscriptionPool: pool}, "", d.logger)
	if err != nil || run == nil || run.launch.Env["CLAUDE_CODE_OAUTH_TOKEN"] != "sk-ant-oat01-run" {
		t.Fatalf("run: %+v %v", run, err)
	}
	if run, err := d.prepareSubscriptionRun(context.Background(), Task{}, "", d.logger); run != nil || err != nil {
		t.Fatal("no pool: launch as configured")
	}

	pool.Subscriptions[0].Usage[0].UsedPercent = 95
	if _, err := d.prepareSubscriptionRun(context.Background(), Task{SubscriptionPool: pool}, "", d.logger); err == nil || !strings.Contains(err.Error(), "usage limit") {
		t.Fatalf("over cap: %v", err)
	}

	if runtime.GOOS == "windows" {
		return
	}
	pool.Subscriptions[0].Usage[0].UsedPercent = 10
	failing := filepath.Join(dir, "require-vless")
	os.WriteFile(failing, []byte("#!/bin/sh\necho 'нет выхода DE' >&2\nexit 1\n"), 0o755)
	d.cfg.RequireVLESS = failing
	if _, err := d.prepareSubscriptionRun(context.Background(), Task{SubscriptionPool: pool}, "", d.logger); err == nil || !strings.Contains(err.Error(), "VLESS") {
		t.Fatalf("no VLESS: %v", err)
	}
}

func TestHandleSubscriptionLoginWritesCredential(t *testing.T) {
	var mu sync.Mutex
	var reports []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		reports = append(reports, body)
		mu.Unlock()
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	d := freshDaemon(srv.URL)
	cred := filepath.Join(t.TempDir(), "claude-3", "token")
	d.handleSubscriptionLogin(context.Background(), "rt-1", &PendingSubscriptionLogin{
		ID: "login-1", Key: "claude-3", Auth: "claude_oauth", CredentialPath: cred, Token: "sk-ant-oat01-new"})
	if data, _ := os.ReadFile(cred); string(data) != "sk-ant-oat01-new\n" {
		t.Fatalf("credential = %q", data)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reports) != 1 || reports[0]["state"] != "done" {
		t.Fatalf("reports: %v", reports)
	}
	for _, r := range reports {
		if strings.Contains(r["message"].(string), "sk-ant") {
			t.Fatal("the token must not be reported back")
		}
	}
}

func TestLinkCodexAgentsMD(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink")
	}
	dir := t.TempDir()
	rules := filepath.Join(dir, "AGENTS.md")
	os.WriteFile(rules, []byte("rules"), 0o644)
	home := filepath.Join(dir, "home")
	os.Mkdir(home, 0o755)
	d := freshDaemon("")
	linkCodexAgentsMD(home, rules, d.logger)
	if got, _ := os.Readlink(filepath.Join(home, "AGENTS.md")); got != rules {
		t.Fatalf("AGENTS.md -> %q", got)
	}
	other := filepath.Join(dir, "home2")
	os.Mkdir(other, 0o755)
	os.WriteFile(filepath.Join(other, "AGENTS.md"), []byte("own"), 0o644)
	linkCodexAgentsMD(other, rules, d.logger)
	if data, _ := os.ReadFile(filepath.Join(other, "AGENTS.md")); string(data) != "own" {
		t.Fatal("an existing AGENTS.md is kept")
	}
}
