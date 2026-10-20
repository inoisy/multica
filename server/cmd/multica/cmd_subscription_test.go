package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

type recordedRequest struct {
	Method string
	Path   string
	Query  string
	Body   map[string]any
}

// subscriptionTestServer answers every request with the reply for its
// "METHOD /path" and records what it received.
func subscriptionTestServer(t *testing.T, replies map[string]any) *[]recordedRequest {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-123")
	got := &[]recordedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := recordedRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery}
		_ = json.NewDecoder(r.Body).Decode(&rec.Body)
		*got = append(*got, rec)
		reply, ok := replies[r.Method+" "+r.URL.Path]
		if !ok {
			http.Error(w, `{"error":"unexpected"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reply)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	return got
}

func subscriptionTestCmd(setup func(f *cobra.Command)) *cobra.Command {
	cmd := &cobra.Command{Use: "test"}
	addCommonProfileFlags(cmd)
	cmd.Flags().String("output", "json", "")
	setup(cmd)
	return cmd
}

func addFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	for _, name := range []string{"title", "key", "owner-note", "auth", "credential-path", "account-group", "settings-json", "disable-until"} {
		f.String(name, "", "")
	}
	f.StringSlice("windows", nil, "")
	f.StringArray("cap", nil, "")
	f.Int("reserve", 0, "")
	f.Bool("token-stdin", false, "")
	f.Bool("enable", false, "")
}

func TestSubscriptionAddSendsNoSecrets(t *testing.T) {
	got := subscriptionTestServer(t, map[string]any{
		"POST /api/subscriptions": map[string]any{"key": "claude-3", "credential_path": "~/.config/claude-3/token"},
	})
	cmd := subscriptionTestCmd(addFlags)
	_ = cmd.Flags().Set("title", "Claude, третья")
	_ = cmd.Flags().Set("cap", "5h=70")
	_ = cmd.Flags().Set("cap", "week=90%")
	_ = cmd.Flags().Set("reserve", "18")
	if err := runSubscriptionAdd(cmd, []string{"claude"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(*got) != 1 {
		t.Fatalf("requests: %+v", *got)
	}
	body := (*got)[0].Body
	if body["provider"] != "claude" || body["title"] != "Claude, третья" || body["reserve"] != float64(18) {
		t.Fatalf("body: %v", body)
	}
	if !reflect.DeepEqual(body["caps"], map[string]any{"5h": float64(70), "week": float64(90)}) {
		t.Fatalf("caps: %v", body["caps"])
	}
	if _, ok := body["key"]; ok {
		t.Fatalf("key sent although not given: %v", body)
	}

	// --token-stdin is refused before anything is read or sent.
	before := len(*got)
	cmd = subscriptionTestCmd(addFlags)
	_ = cmd.Flags().Set("token-stdin", "true")
	if err := runSubscriptionAdd(cmd, []string{"claude"}); err == nil || !strings.Contains(err.Error(), "not available yet") {
		t.Fatalf("token-stdin: err = %v", err)
	}
	if len(*got) != before {
		t.Fatal("token-stdin must not reach the server")
	}
}

func TestSubscriptionUpdateFlags(t *testing.T) {
	got := subscriptionTestServer(t, map[string]any{
		"PATCH /api/subscriptions/claude-3": map[string]any{"key": "claude-3"},
	})
	cmd := subscriptionTestCmd(addFlags)
	_ = cmd.Flags().Set("cap", "5h=70")
	_ = cmd.Flags().Set("reserve", "18")
	_ = cmd.Flags().Set("disable-until", "2026-10-21T12:00Z")
	if err := runSubscriptionUpdate(cmd, []string{"claude-3"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	body := (*got)[0].Body
	if body["disabled_until"] != "2026-10-21T12:00Z" || body["reserve"] != float64(18) ||
		!reflect.DeepEqual(body["caps"], map[string]any{"5h": float64(70)}) {
		t.Fatalf("body: %v", body)
	}

	cmd = subscriptionTestCmd(addFlags)
	_ = cmd.Flags().Set("enable", "true")
	if err := runSubscriptionUpdate(cmd, []string{"claude-3"}); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if v, ok := (*got)[1].Body["disabled_until"]; !ok || v != "" {
		t.Fatalf("enable must clear disabled_until: %v", (*got)[1].Body)
	}

	cmd = subscriptionTestCmd(addFlags)
	if err := runSubscriptionUpdate(cmd, []string{"claude-3"}); err == nil {
		t.Fatal("update without flags must fail")
	}
}

func TestSubscriptionUsageAndRm(t *testing.T) {
	got := subscriptionTestServer(t, map[string]any{
		"GET /api/subscriptions/codex-2/usage": map[string]any{"key": "codex-2", "usage": []any{}},
		"DELETE /api/subscriptions/codex-2":    map[string]any{"deleted": "codex-2", "detached_runtime_profile_ids": []string{"p1"}},
	})
	cmd := subscriptionTestCmd(func(c *cobra.Command) { c.Flags().String("since", "7d", "") })
	if err := runSubscriptionUsage(cmd, []string{"codex-2"}); err != nil {
		t.Fatalf("usage: %v", err)
	}
	if (*got)[0].Query != "since=7d" {
		t.Fatalf("query = %q", (*got)[0].Query)
	}
	if err := runSubscriptionRm(subscriptionTestCmd(func(*cobra.Command) {}), []string{"codex-2"}); err != nil {
		t.Fatalf("rm: %v", err)
	}
	if (*got)[1].Method != http.MethodDelete {
		t.Fatalf("rm request: %+v", (*got)[1])
	}
}

func poolCreateFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringSlice("subs", nil, "")
	f.String("order", "first", "")
	f.String("type", "", "")
	f.String("description", "", "")
	f.Bool("all-workspaces", false, "")
}

func TestRuntimePoolCreate(t *testing.T) {
	got := subscriptionTestServer(t, map[string]any{
		"GET /api/subscriptions/claude-second": map[string]any{"key": "claude-second", "family": "claude"},
		"POST /api/runtime-pools":              map[string]any{"share_key": "pool-1", "runtime_profiles": []any{}},
	})
	cmd := subscriptionTestCmd(poolCreateFlags)
	_ = cmd.Flags().Set("subs", "claude-second,claude-3,claude-main")
	_ = cmd.Flags().Set("order", "balanced")
	if err := runRuntimePoolCreate(cmd, []string{"Claude · подписки"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	body := (*got)[1].Body
	if body["runtime_type"] != "claude" || body["spend_order"] != "balanced" || body["all_workspaces"] != false ||
		!reflect.DeepEqual(body["subscriptions"], []any{"claude-second", "claude-3", "claude-main"}) ||
		!reflect.DeepEqual(body["workspace_ids"], []any{"ws-123"}) {
		t.Fatalf("body: %v", body)
	}

	cmd = subscriptionTestCmd(poolCreateFlags)
	_ = cmd.Flags().Set("type", "codex")
	_ = cmd.Flags().Set("all-workspaces", "true")
	if err := runRuntimePoolCreate(cmd, []string{"Codex · подписки"}); err != nil {
		t.Fatalf("create all: %v", err)
	}
	body = (*got)[2].Body
	if _, ok := body["workspace_ids"]; ok || body["all_workspaces"] != true {
		t.Fatalf("all-workspaces body: %v", body)
	}
}

func TestRuntimePoolSetByName(t *testing.T) {
	got := subscriptionTestServer(t, map[string]any{
		"GET /api/workspaces/ws-123/runtime-profiles": map[string]any{"runtime_profiles": []any{
			map[string]any{"id": "p1", "display_name": "Claude · подписки", "subscriptions": []any{"claude-second", "claude-main"}},
		}},
		"PUT /api/workspaces/ws-123/runtime-profiles/p1/subscriptions": map[string]any{"runtime_profile": map[string]any{"id": "p1"}},
	})
	cmd := subscriptionTestCmd(func(c *cobra.Command) {
		c.Flags().String("add", "", "")
		c.Flags().Bool("first", false, "")
		c.Flags().String("remove", "", "")
		c.Flags().String("order", "", "")
	})
	_ = cmd.Flags().Set("add", "claude-3")
	_ = cmd.Flags().Set("first", "true")
	if err := runRuntimePoolSet(cmd, []string{"claude · ПОДПИСКИ"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	put := (*got)[1]
	if put.Method != http.MethodPut || !reflect.DeepEqual(put.Body["subscriptions"], []any{"claude-3", "claude-second", "claude-main"}) {
		t.Fatalf("put: %+v", put)
	}
}

func TestNextPoolKeys(t *testing.T) {
	cases := []struct {
		current     []string
		add, remove string
		first       bool
		want        []string
		wantErr     bool
	}{
		{current: []string{"a", "b"}, add: "c", want: []string{"a", "b", "c"}},
		{current: []string{"a", "b"}, add: "c", first: true, want: []string{"c", "a", "b"}},
		{current: []string{"a", "b"}, add: "b", first: true, want: []string{"b", "a"}},
		{current: []string{"a", "b"}, remove: "a", want: []string{"b"}},
		{current: []string{"a"}, remove: "z", wantErr: true},
	}
	for _, c := range cases {
		got, err := nextPoolKeys(c.current, c.add, c.first, c.remove)
		if (err != nil) != c.wantErr || (!c.wantErr && !reflect.DeepEqual(got, c.want)) {
			t.Errorf("nextPoolKeys(%v, +%s first=%v, -%s) = %v, %v; want %v", c.current, c.add, c.first, c.remove, got, err, c.want)
		}
	}
}

func TestParseCaps(t *testing.T) {
	caps, err := parseCaps([]string{"5h=70", "week=90%"})
	if err != nil || !reflect.DeepEqual(caps, map[string]int{"5h": 70, "week": 90}) {
		t.Fatalf("parseCaps = %v, %v", caps, err)
	}
	for _, bad := range []string{"5h", "=70", "5h=101", "5h=x"} {
		if _, err := parseCaps([]string{bad}); err == nil {
			t.Errorf("parseCaps(%q) accepted", bad)
		}
	}
}
