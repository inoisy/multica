package subscriptions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func st(h5, week float64, limit bool) State {
	return State{Credential: CredentialPresent, Windows: []Window{{Key: "5h", Used: h5, Limit: limit}, {Key: "week", Used: week, Limit: limit}}}
}

func sub(key string, caps map[string]int) Subscription {
	return Subscription{Key: key, EffectiveCaps: caps}
}

var (
	capMain   = map[string]int{"5h": 70, "week": 82}
	capSecond = map[string]int{"5h": 90, "week": 90}
)

func codexPick(m, r State) string {
	return Decide("balanced", []Mark{{sub("main", capMain), m}, {sub("second", capSecond), r}}).Key
}

// The scenarios of subs-pick's test_subs_pick.py, unchanged.
func TestDecideBalanced(t *testing.T) {
	unknown := State{Credential: CredentialPresent}
	missing := State{Credential: CredentialMissing}
	cases := []struct {
		name string
		m, r State
		want string
	}{
		{"both usable: lower week", st(40, 50, false), st(10, 10, false), "second"},
		{"main 5h below cap", st(65, 50, false), st(95, 10, false), "main"},
		{"main 5h over cap", st(75, 50, false), st(10, 10, false), "second"},
		{"second over its week", st(40, 50, false), st(10, 91, false), "main"},
		{"both exhausted: first", st(75, 50, false), st(95, 10, false), "main"},
		{"unknown main first", unknown, st(10, 10, false), "main"},
		{"provider limit flag", st(10, 10, true), st(20, 20, false), "second"},
		{"no credential is never chosen", missing, unknown, "second"},
	}
	for _, c := range cases {
		if got := codexPick(c.m, c.r); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestDecideFirst(t *testing.T) {
	pick := func(second State) string {
		return Decide("first", []Mark{{sub("second", map[string]int{"5h": 50, "week": 50}), second}, {sub("main", nil), State{Credential: CredentialPresent}}}).Key
	}
	if got := pick(st(10, 40, false)); got != "second" {
		t.Errorf("second below cap: %s", got)
	}
	if got := pick(st(10, 57, false)); got != "main" {
		t.Errorf("second over cap: %s", got)
	}
	if got := pick(State{Credential: CredentialPresent}); got != "main" {
		t.Errorf("probe failed: %s", got)
	}
	if got := pick(State{Credential: CredentialMissing}); got != "main" {
		t.Errorf("no credential: %s", got)
	}
}

func TestBlocked(t *testing.T) {
	capped := map[string]int{"5h": 93, "week": 93}
	one := func(s State, key string) Mark { return Mark{sub(key, capped), s} }
	if Blocked([]Mark{one(st(51, 90, false), "max")}) != "" {
		t.Error("below cap must run")
	}
	reset := func(sec int64) *time.Time { t := time.Unix(sec, 0).UTC(); return &t }
	over := State{Credential: CredentialPresent, Windows: []Window{{Key: "5h", Used: 51, ResetAt: reset(1792327200)}, {Key: "week", Used: 95, ResetAt: reset(1792332000)}}}
	text := Blocked([]Mark{one(over, "max")})
	for _, want := range []string{"usage limit", "неделя 95% ≥ 93%", "reset at 2026-10-18 14:00 UTC"} {
		if !strings.Contains(text, want) {
			t.Errorf("blocked text %q lacks %q", text, want)
		}
	}
	if Blocked([]Mark{one(st(95, 10, false), "second"), one(st(10, 10, false), "main")}) != "" {
		t.Error("one usable must run")
	}
	if Blocked([]Mark{one(State{Credential: CredentialPresent}, "a")}) != "" {
		t.Error("unknown load must not block")
	}
	if Blocked([]Mark{one(st(10, 99, false), "a"), one(State{Credential: CredentialPresent}, "b")}) != "" {
		t.Error("one unknown must not block")
	}
	if !strings.Contains(Blocked([]Mark{one(st(10, 10, true), "a")}), "отказ провайдера") {
		t.Error("provider refusal")
	}
	a := State{Credential: CredentialPresent, Windows: []Window{{Key: "5h", Used: 95, ResetAt: reset(2000)}, {Key: "week", Used: 10, ResetAt: reset(9000)}}}
	b := State{Credential: CredentialPresent, Windows: []Window{{Key: "5h", Used: 10, ResetAt: reset(3000)}, {Key: "week", Used: 99, ResetAt: reset(5000)}}}
	if text := Blocked([]Mark{one(a, "a"), one(b, "b")}); !strings.Contains(text, "reset at 1970-01-01 00:33 UTC") {
		t.Errorf("earliest reset: %q", text)
	}
}

func TestSelectorStaleAndDisabled(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	s := &Selector{Interval: time.Minute, ClaudeInterval: 30 * time.Minute, Now: func() time.Time { return now },
		CheckCredentialFunc: func(Subscription) CredentialState { return CredentialPresent }}
	old := now.Add(-2 * time.Hour).Format(time.RFC3339)
	fresh := now.Add(-time.Minute).Format(time.RFC3339)
	over := []Usage{{Window: "week", UsedPercent: 99, ObservedAt: fresh}}
	pool := Pool{SpendOrder: "first", Subscriptions: []Subscription{
		{Key: "a", Family: "claude", Auth: "claude_oauth", EffectiveCaps: map[string]int{"week": 90}, Usage: over},
	}}
	c, _ := s.Choose(pool, true)
	if c.Blocked == "" {
		t.Error("fresh server load over cap must block an agent run")
	}
	if c, _ := s.Choose(pool, false); c.Blocked != "" {
		t.Error("manual runs keep the people's reserve")
	}
	pool.Subscriptions[0].Usage = []Usage{{Window: "week", UsedPercent: 99, ObservedAt: old}}
	if c, _ := s.Choose(pool, true); c.Blocked != "" {
		t.Error("stale load must not block")
	}
	until := "2026-10-21T12:00:00Z"
	pool.Subscriptions[0].Disabled, pool.Subscriptions[0].DisabledUntil = true, &until
	if c, _ := s.Choose(pool, true); !strings.Contains(c.Blocked, "отключены до "+until) {
		t.Errorf("all disabled: %q", c.Blocked)
	}
	pool.Subscriptions = append(pool.Subscriptions, Subscription{Key: "b", Family: "claude", Auth: "claude_login"})
	if c, _ := s.Choose(pool, true); c.Subscription.Key != "b" || c.Blocked != "" {
		t.Errorf("disabled one is skipped: %+v", c)
	}
}

func writeFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPrepare(t *testing.T) {
	dir := t.TempDir()
	token := writeFile(t, filepath.Join(dir, "token"), "sk-ant-oat01-abc\n")
	l, err := Prepare(Subscription{Key: "c", Family: "claude", Auth: "claude_oauth", CredentialPath: token}, "")
	if err != nil || l.Env["CLAUDE_CODE_OAUTH_TOKEN"] != "sk-ant-oat01-abc" || !l.NeedsVLESS || l.Env["ANTHROPIC_API_KEY"] != "" {
		t.Fatalf("oauth: %+v %v", l, err)
	}
	if _, err := Prepare(Subscription{Key: "c", Family: "claude", Auth: "claude_oauth", CredentialPath: filepath.Join(dir, "none")}, ""); err == nil || !strings.Contains(err.Error(), "нет токена подписки c") {
		t.Fatalf("missing token: %v", err)
	}
	key := writeFile(t, filepath.Join(dir, "key"), "zai-key\n")
	l, err = Prepare(Subscription{Key: "zai", Family: "claude", Auth: "api_key", CredentialPath: key, Settings: Settings{Env: map[string]string{
		"ANTHROPIC_BASE_URL": "https://api.z.ai/api/anthropic", "ANTHROPIC_MODEL": "glm", "LD_PRELOAD": "/x.so"}}}, "")
	if err != nil || l.Env["ANTHROPIC_AUTH_TOKEN"] != "zai-key" || l.Env["ANTHROPIC_MODEL"] != "glm" || l.NeedsVLESS || !l.DropModel {
		t.Fatalf("api key: %+v %v", l, err)
	}
	if _, ok := l.Env["LD_PRELOAD"]; ok {
		t.Fatal("settings.env must not set arbitrary variables")
	}
	if runtime.GOOS == "windows" {
		return
	}
	home := t.TempDir()
	shared := writeFile(t, filepath.Join(dir, "main.json"), "{}")
	second := writeFile(t, filepath.Join(dir, "second.json"), `{"tokens":{}}`)
	if err := os.Symlink(shared, filepath.Join(home, "auth.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(Subscription{Key: "x", Family: "codex", Auth: "codex_auth", CredentialPath: second}, home); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.Readlink(filepath.Join(home, "auth.json")); got != second {
		t.Fatalf("task auth.json -> %s, want %s", got, second)
	}
}

func TestClaudeProviderArgs(t *testing.T) {
	cases := []struct{ in, want []string }{
		{[]string{"--model", "opus", "-p", "x", "--model=sonnet"}, []string{"-p", "x"}},
		{[]string{"--exclude-tools=agent,Bash", "-p", "x"}, []string{"-p", "x", "--disallowedTools", "Agent", "Bash"}},
		{[]string{"--disallowedTools", "Edit", "--exclude-tools", "agent"}, []string{"--disallowedTools", "Agent", "Edit"}},
	}
	for _, c := range cases {
		if got := ClaudeProviderArgs(c.in); !slices.Equal(got, c.want) {
			t.Errorf("ClaudeProviderArgs(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestProbes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/messages":
			if r.Header.Get("Authorization") != "Bearer sk-ant-oat01-ok" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			h := w.Header()
			h.Set("anthropic-ratelimit-unified-5h-utilization", "0.41")
			h.Set("anthropic-ratelimit-unified-5h-reset", "1792327200")
			h.Set("anthropic-ratelimit-unified-7d-utilization", "0.9")
			h.Set("anthropic-ratelimit-unified-7d-status", "rejected")
			w.WriteHeader(http.StatusTooManyRequests)
		case "/backend-api/wham/usage":
			json.NewEncoder(w).Encode(map[string]any{"rate_limit": map[string]any{
				"primary_window":   map[string]any{"used_percent": 12, "reset_at": 1792327200},
				"secondary_window": map[string]any{"used_percent": 34, "reset_at": 1792332000}}})
		case "/api/monitor/usage/quota/limit":
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"limits": []any{
				map[string]any{"unit": 3, "percentage": 5, "nextResetTime": 1792327200000}, map[string]any{"unit": 6, "percentage": 7}}}})
		case "/v1/api/openplatform/coding_plan/remains":
			json.NewEncoder(w).Encode(map[string]any{"model_remains": []any{map[string]any{"model_name": "general",
				"current_interval_remaining_percent": 80, "current_weekly_remaining_percent": 60}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := &Prober{HTTP: srv.Client(), Now: time.Now, Endpoints: Endpoints{Anthropic: srv.URL, Codex: srv.URL, Zai: srv.URL, Minimax: srv.URL, OpenRouter: srv.URL}}
	dir := t.TempDir()
	ctx := context.Background()
	used := func(s State, key string) float64 { w, _ := s.window(key); return w.Used }

	s, err := p.Probe(ctx, Subscription{Auth: "claude_oauth", CredentialPath: writeFile(t, filepath.Join(dir, "t"), "sk-ant-oat01-ok")})
	if err != nil || used(s, "5h") != 41 || used(s, "week") != 90 {
		t.Fatalf("claude: %+v %v", s, err)
	}
	if w, _ := s.window("week"); !w.Limit {
		t.Fatal("claude rejected status is a provider limit")
	}
	s, _ = p.Probe(ctx, Subscription{Auth: "claude_oauth", CredentialPath: writeFile(t, filepath.Join(dir, "bad"), "sk-ant-oat01-revoked")})
	if s.Credential != CredentialRevoked {
		t.Fatalf("refused token: %+v", s)
	}
	s, err = p.Probe(ctx, Subscription{Auth: "codex_auth", CredentialPath: writeFile(t, filepath.Join(dir, "a.json"), `{"tokens":{"access_token":"x","account_id":"y"}}`)})
	if err != nil || used(s, "5h") != 12 || used(s, "week") != 34 {
		t.Fatalf("codex: %+v %v", s, err)
	}
	s, err = p.Probe(ctx, Subscription{Auth: "api_key", Provider: "zai", CredentialPath: writeFile(t, filepath.Join(dir, "z"), "k")})
	if err != nil || used(s, "5h") != 5 || used(s, "week") != 7 {
		t.Fatalf("zai: %+v %v", s, err)
	}
	s, err = p.Probe(ctx, Subscription{Auth: "api_key", Provider: "minimax", CredentialPath: writeFile(t, filepath.Join(dir, "m"), "k")})
	if err != nil || used(s, "5h") != 20 || used(s, "week") != 40 {
		t.Fatalf("minimax: %+v %v", s, err)
	}
	s, _ = p.Probe(ctx, Subscription{Auth: "claude_oauth", CredentialPath: filepath.Join(dir, "none")})
	if s.Credential != CredentialMissing || s.Known() {
		t.Fatalf("missing credential: %+v", s)
	}
}

func TestWriteCredentialAndValidate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "token")
	if err := WriteCredential(path, []byte("sk-ant-oat01-x\n")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("credential file: %v %v", fi, err)
	}
	if _, err := ValidateSecret("claude_oauth", "not-a-token"); err == nil {
		t.Error("claude_oauth accepts only setup tokens")
	}
	if _, err := ValidateSecret("api_key", "a b"); err == nil {
		t.Error("whitespace inside a key is refused")
	}
	if v, err := ValidateSecret("api_key", " key\n"); err != nil || v != "key" {
		t.Errorf("trim: %q %v", v, err)
	}
}

func TestCodexDeviceLogin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake")
	}
	dir := t.TempDir()
	fake := writeFile(t, filepath.Join(dir, "codex"), "#!/bin/sh\necho 'Open https://auth.openai.com/codex/device.'\necho 'Code: ABCD-12345'\necho '{\"tokens\":{}}' > \"$CODEX_HOME/auth.json\"\n")
	os.Chmod(fake, 0o755)
	cred := filepath.Join(dir, "home", "auth.json")
	var got LoginProgress
	if err := CodexDeviceLogin(context.Background(), fake, cred, func(p LoginProgress) { got = p }); err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://auth.openai.com/codex/device" || got.Code != "ABCD-12345" {
		t.Fatalf("progress: %+v", got)
	}
	if data, _ := os.ReadFile(cred); !strings.Contains(string(data), "tokens") {
		t.Fatalf("credential not written: %q", data)
	}
	failing := writeFile(t, filepath.Join(dir, "codex-fail"), "#!/bin/sh\nexit 3\n")
	os.Chmod(failing, 0o755)
	before, _ := os.ReadFile(cred)
	if err := CodexDeviceLogin(context.Background(), failing, cred, func(LoginProgress) {}); err == nil {
		t.Fatal("failed login must error")
	}
	if after, _ := os.ReadFile(cred); string(after) != string(before) {
		t.Fatal("a failed login must not touch the working credential")
	}
}

// TOON-274, 20.10.2026 20:30:18 UTC: claude-second over its week cap until
// tomorrow, claude-main's 5h snapshot at 97% taken before its 20:30 reset.
// The snapshot was stale the moment the run was claimed: the run must start
// on claude-main, and had both been held, the runtime frees at the earliest
// reset, not the latest.
func TestSettledWindowsAndEarliestReset(t *testing.T) {
	now := time.Date(2026, 10, 20, 20, 30, 18, 0, time.UTC)
	at := func(t time.Time) *string { s := t.Format(time.RFC3339); return &s }
	observed := now.Add(-4 * time.Minute)
	weekReset := time.Date(2026, 10, 21, 20, 0, 0, 0, time.UTC)
	mainReset := time.Date(2026, 10, 20, 20, 30, 0, 0, time.UTC)
	second := Subscription{Key: "claude-second", Family: "claude", Auth: "claude_oauth", EffectiveCaps: map[string]int{"5h": 50, "week": 50},
		Usage: []Usage{{Window: "5h", UsedPercent: 0, ObservedAt: observed.Format(time.RFC3339)}, {Window: "week", UsedPercent: 54, ResetsAt: at(weekReset), ObservedAt: observed.Format(time.RFC3339)}}}
	main := Subscription{Key: "claude-main", Family: "claude", Auth: "claude_oauth", EffectiveCaps: map[string]int{"5h": 75, "week": 76},
		Usage: []Usage{{Window: "5h", UsedPercent: 97, ResetsAt: at(mainReset), ObservedAt: observed.Format(time.RFC3339)}, {Window: "week", UsedPercent: 63, ResetsAt: at(weekReset), ObservedAt: observed.Format(time.RFC3339)}}}
	s := &Selector{Interval: time.Minute, ClaudeInterval: 30 * time.Minute, Now: func() time.Time { return now },
		CheckCredentialFunc: func(Subscription) CredentialState { return CredentialPresent }}
	pool := Pool{SpendOrder: "first", Subscriptions: []Subscription{second, main}}

	c, _ := s.Choose(pool, true)
	if c.Blocked != "" || c.Subscription.Key != "claude-main" {
		t.Fatalf("reset passed: the run goes to claude-main, got %q blocked=%q", c.Subscription.Key, c.Blocked)
	}
	if w, _ := c.Marks[1].State.window("5h"); w.Used != 0 || w.ResetAt != nil {
		t.Fatalf("settled 5h window: %+v", w)
	}

	// A minute earlier both were held: the runtime frees at 20:30, not at
	// claude-second's weekly reset.
	now = mainReset.Add(-time.Minute)
	c, _ = s.Choose(pool, true)
	if c.Blocked == "" {
		t.Fatal("both over cap must block")
	}
	if !c.BlockedUntil.Equal(mainReset) || !strings.Contains(c.Blocked, "reset at 2026-10-20 20:30 UTC") {
		t.Fatalf("until %v, text %q", c.BlockedUntil, c.Blocked)
	}

	// One held subscription with no reset: no reset to report.
	main.Usage[0].ResetsAt = nil
	c, _ = s.Choose(Pool{SpendOrder: "first", Subscriptions: []Subscription{second, main}}, true)
	if c.Blocked == "" || !c.BlockedUntil.IsZero() || strings.Contains(c.Blocked, "reset at") {
		t.Fatalf("unknown reset: until %v, text %q", c.BlockedUntil, c.Blocked)
	}

	// All disabled: free when the first comes back.
	soon, later := "2026-10-21T08:00:00Z", "2026-10-22T08:00:00Z"
	c, _ = s.Choose(Pool{Subscriptions: []Subscription{{Key: "a", Disabled: true, DisabledUntil: &later}, {Key: "b", Disabled: true, DisabledUntil: &soon}}}, true)
	if want, _ := time.Parse(time.RFC3339, soon); !c.BlockedUntil.Equal(want) {
		t.Fatalf("disabled until %v", c.BlockedUntil)
	}
}

func TestFreeAtUnknownWait(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	reset := now.Add(3 * time.Hour)
	held := Mark{sub("a", map[string]int{"week": 50}), State{Credential: CredentialPresent, Windows: []Window{{Key: "week", Used: 60, ResetAt: &reset}}}}
	noReset := Mark{sub("b", map[string]int{"week": 50}), State{Credential: CredentialPresent, Windows: []Window{{Key: "week", Used: 60}}}}
	if got := FreeAt([]Mark{held, noReset}, now, time.Hour); !got.Equal(now.Add(time.Hour)) {
		t.Fatalf("unknown counts as free after the wait: %v", got)
	}
	if got := FreeAt([]Mark{held, noReset}, now, 0); !got.IsZero() {
		t.Fatalf("no wait: zero, got %v", got)
	}
	if got := FreeAt([]Mark{held}, now, 0); !got.Equal(reset) {
		t.Fatalf("held: %v", got)
	}
}
