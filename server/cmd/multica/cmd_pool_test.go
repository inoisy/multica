package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestPoolCLIRoundTripRevisionAndGrants(t *testing.T) {
	t.Chdir(t.TempDir())
	revision := 1
	keys := []string{"a", "b"}
	grants := []string{"ws-123"}
	writes := 0
	snapshot := func() map[string]any {
		accounts := []map[string]any{}
		for i, key := range keys {
			accounts = append(accounts, map[string]any{"key": key, "position": i + 1})
		}
		return map[string]any{"id": "pool-1", "name": "Accounts", "revision": revision, "subscriptions": accounts, "workspace_ids": grants}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch r.Method + " " + r.URL.Path {
		case "POST /api/pools":
			if body["name"] != "Accounts" || body["runtime_type"] != "codex" {
				t.Errorf("create %v", body)
			}
		case "GET /api/pools":
			json.NewEncoder(w).Encode(map[string]any{"pools": []any{snapshot()}})
			return
		case "GET /api/pools/pool-1":
		case "PATCH /api/pools/pool-1", "PUT /api/pools/pool-1/grants":
			if body["revision"] != float64(revision) {
				w.WriteHeader(409)
				io.WriteString(w, `{"code":"pool_revision_conflict","error":"reread pool"}`)
				return
			}
			writes++
			if raw, ok := body["subscriptions"]; ok {
				keys = []string{}
				for _, k := range raw.([]any) {
					keys = append(keys, k.(string))
				}
				revision++
			}
			if raw, ok := body["workspace_ids"]; ok {
				grants = []string{}
				for _, g := range raw.([]any) {
					grants = append(grants, g.(string))
				}
			}
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(snapshot())
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-123")
	run := func(args ...string) error {
		c := newPoolCommand()
		c.SetArgs(append(args, "--output=json"))
		c.SetErr(io.Discard)
		c.SetOut(io.Discard)
		return c.Execute()
	}
	for _, args := range [][]string{
		{"create", "Accounts", "--type=codex", "--subs=a,b"},
		{"list"}, {"show", "accounts"},
		{"set", "pool-1", "--add=b", "--first"},
		{"grant", "pool-1", "--workspaces=ws-123,ws-456"},
	} {
		if err := run(args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if !reflect.DeepEqual(keys, []string{"b", "a"}) || revision != 2 || len(grants) != 2 {
		t.Fatalf("keys=%v revision=%d grants=%v", keys, revision, grants)
	}
	if err := run("set", "pool-1", "--subs=a", "--revision=1"); err == nil {
		t.Fatal("stale revision accepted")
	}
	if writes != 2 {
		t.Fatalf("conflict retried or wrote state: %d", writes)
	}
	if err := run("grant", "pool-1", "--workspaces="); err != nil {
		t.Fatal(err)
	}
	if len(grants) != 0 {
		t.Fatalf("revoke %v", grants)
	}
}

func TestPersonalModelCLIUsesServerMutations(t *testing.T) {
	t.Chdir(t.TempDir())
	var writes []apiCall
	modelsServer(t, &writes)
	run := func(args ...string) {
		t.Helper()
		c := newAgentModelsCommand()
		c.SetArgs(append(args, "--output=json"))
		if err := c.Execute(); err != nil {
			t.Fatal(err)
		}
	}
	run("personal", modelsTestAgent, "--from=strong")
	if len(writes) != 1 || writes[0].method != "POST" || writes[0].body["from_profile_id"] != "p-1" {
		t.Fatalf("copy %v", writes)
	}
	writes = nil
	run("personal", modelsTestAgent, "--row=codex-main:sol:high", "--row=claude:opus")
	if writes[0].method != "PUT" || !strings.HasSuffix(writes[0].path, "/models/personal") {
		t.Fatalf("edit %v", writes)
	}
	rows := writes[0].body["options"].([]any)
	if len(rows) != 2 || rows[0].(map[string]any)["runtime_id"] != "rt-1" {
		t.Fatalf("rows %v", rows)
	}
	writes = nil
	run("single", modelsTestAgent, "codex-main:sol:high")
	if !strings.HasSuffix(writes[0].path, "/models/single") || writes[0].body["model"] != "sol" {
		t.Fatalf("single %v", writes)
	}
	writes = nil
	run("reset", modelsTestAgent)
	if writes[0].method != "DELETE" || !strings.HasSuffix(writes[0].path, "/models/personal") {
		t.Fatalf("reset %v", writes)
	}
}

func TestModelAccountDisplayUsesCodesAndUnknownFact(t *testing.T) {
	if got := reasonCodeLabel("future_code", "old Russian message"); got != "unknown reason (future_code)" {
		t.Fatal(got)
	}
	if got := accountFactLabel(nil); got != "unknown" {
		t.Fatal(got)
	}
	fact := &protocol.AccountFact{SubscriptionKey: "account-a", SubscriptionID: "id-a", PoolID: "pool-a", PoolRevision: 3}
	if got := accountFactLabel(fact); !strings.Contains(got, "confirmed by daemon") || !strings.Contains(got, "revision 3") {
		t.Fatal(got)
	}
	raw := json.RawMessage(fmt.Sprintf(`{"mode":"personal","matches_base":true,"base_profile_name":"Shared","preview":{"reason":"all_unavailable","wait_reason_code":"agent_cap","rows":[]},"last":{"account_fact":%s}}`, mustAccountJSON(t, fact)))
	cmd := newAgentModelsCommand()
	out, err := captureStdout(t, func() error { return printAgentModels(cmd, raw) })
	if err != nil || !strings.Contains(out, "matches base") || !strings.Contains(out, "agent cap reached") || !strings.Contains(out, "account-a") {
		t.Fatalf("%s %v", out, err)
	}
}

func mustAccountJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestProfileListKindUsesServerFilter(t *testing.T) {
	t.Chdir(t.TempDir())
	var kind string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kind = r.URL.Query().Get("kind")
		io.WriteString(w, `[]`)
	}))
	defer srv.Close()
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-123")
	list, _, err := profileCmd.Find([]string{"list"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = list.Flags().Set("kind", "shared"); _ = list.Flags().Set("output", "table") })
	for _, want := range []string{"personal", "shared"} {
		_ = list.Flags().Set("kind", want)
		_ = list.Flags().Set("output", "json")
		if err := list.RunE(list, nil); err != nil {
			t.Fatal(err)
		}
		if kind != want {
			t.Fatalf("filter %s", kind)
		}
	}
	_ = list.Flags().Set("kind", "invalid")
	if err := list.RunE(list, nil); err == nil {
		t.Fatal("invalid kind accepted")
	}
}
