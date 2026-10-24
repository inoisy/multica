package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const modelsTestAgent = "0b8e3a52-1f0e-4c6c-9a52-5c2f3c9e7a11"

// modelsServer answers the model API with fixed runtimes and profiles and
// records the writes the CLI sends.
func modelsServer(t *testing.T, writes *[]conveyorCall) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/runtimes":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "rt-1", "name": "codex-main"}, {"id": "rt-2", "name": "claude", "custom_name": "Claude Opus"},
			})
			return
		case r.URL.Path == "/api/model-profiles" && r.Method == "GET":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "p-1", "name": "Strong"}})
			return
		}
		call := conveyorCall{method: r.Method, path: r.URL.RequestURI()}
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			_ = json.Unmarshal(raw, &call.body)
		}
		*writes = append(*writes, call)
		_ = json.NewEncoder(w).Encode(map[string]any{"preview": map[string]any{"source": "agent_list", "reason": "primary", "rows": []any{}}})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "test-token")
}

func TestModelOptionCLIWritesTheListItWasGiven(t *testing.T) {
	t.Chdir(t.TempDir())
	var writes []conveyorCall
	modelsServer(t, &writes)
	run := func(args ...string) {
		t.Helper()
		cmd := newAgentModelsCommand()
		cmd.SetArgs(append(args, "--output", "json"))
		cmd.SetOut(io.Discard)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}

	run("set", modelsTestAgent, "codex-main:sol:high", "claude opus:opus")
	if len(writes) != 1 || writes[0].method != "PUT" || !strings.HasSuffix(writes[0].path, "/api/agents/"+modelsTestAgent+"/models") {
		t.Fatalf("set: %+v", writes)
	}
	rows, _ := writes[0].body["options"].([]any)
	if len(rows) != 2 {
		t.Fatalf("rows: %+v", writes[0].body)
	}
	first, second := rows[0].(map[string]any), rows[1].(map[string]any)
	if first["runtime_id"] != "rt-1" || first["model"] != "sol" || first["thinking_level"] != "high" || second["runtime_id"] != "rt-2" || second["model"] != "opus" {
		t.Fatalf("rows parsed: %+v", rows)
	}

	writes = nil
	run("reset", modelsTestAgent)
	if writes[0].method != "DELETE" || !strings.HasSuffix(writes[0].path, "/models/personal") {
		t.Fatalf("reset must use the atomic server mutation: %+v", writes[0])
	}

	writes = nil
	run("profile", modelsTestAgent, "strong")
	if writes[0].body["model_profile_id"] != "p-1" {
		t.Fatalf("profile by name: %+v", writes[0].body)
	}
	writes = nil
	run("profile", modelsTestAgent, "off")
	if v, ok := writes[0].body["model_profile_id"]; !ok || v != nil {
		t.Fatalf("profile off must send null: %+v", writes[0].body)
	}

	cmd := newAgentModelsCommand()
	cmd.SetArgs([]string{"set", modelsTestAgent, "nowhere:x"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "matches 0 runtimes") {
		t.Fatalf("unknown runtime: %v", err)
	}
}

func TestModelOptionCLIReasonLabels(t *testing.T) {
	until := "2026-10-19T12:53:00Z"
	c := modelChoice{Reason: "fallback_row", Position: 2, Rows: []modelRow{
		{modelOption: modelOption{Position: 1}, State: "limited", Until: &until},
		{modelOption: modelOption{Position: 2}, State: "available", Chosen: true},
	}}
	if got := reasonLabel(c); !strings.HasPrefix(got, "row 1 limited until ") || !strings.HasSuffix(got, "took row 2") {
		t.Fatalf("fallback row: %s", got)
	}
	c = modelChoice{Reason: "fallback_profile", ProfileName: "Mid", ExhaustedProfiles: []string{"Strong"}}
	if got := reasonLabel(c); got != `every model of Strong unavailable — took "Mid"` {
		t.Fatalf("fallback profile: %s", got)
	}
	c = modelChoice{Reason: "all_unavailable", WaitUntil: &until}
	if got := reasonLabel(c); !strings.Contains(got, "the run waits") {
		t.Fatalf("waits: %s", got)
	}
}

// TOON-274: the Critic's run waited for Claude on a cross step while the
// agent's own preview said Codex. The route run is shown with its step and
// the preview is marked as the choice outside a route.
func TestAgentModelsShowsRouteRuns(t *testing.T) {
	raw := json.RawMessage(`{"profile_name":"Сильная","preview":{"source":"agent_profile","profile_name":"Сильная","reason":"primary","position":1,"runtime_name":"Codex · 1","chosen":{"position":1,"model":"gpt-6.1-sol"},"rows":[]},
		"route_runs":[{"issue_identifier":"TOON-274","step":"critic","cross":"build","choice":{"source":"agent_profile","profile_name":"Сильная","reason":"all_unavailable","excluded_family":"codex","rows":[]}}]}`)
	cmd := newAgentModelsCommand()
	_ = cmd.Flags().Set("output", "table")
	out, err := captureStdout(t, func() error { return printAgentModels(cmd, raw) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Off-route:", "Route run:  TOON-274 step critic[cross=build]", "the run waits", "family codex ruled out"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	out, _ = captureStdout(t, func() error {
		return printAgentModels(cmd, json.RawMessage(`{"preview":{"source":"agent_list","reason":"primary","rows":[]},"route_runs":[]}`))
	})
	if !strings.Contains(out, "Next run:") || strings.Contains(out, "Route run") {
		t.Errorf("no route run: %s", out)
	}
}

func TestRuntimeUnavailableRecheck(t *testing.T) {
	var path, method string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, method = r.URL.Path, r.Method
		_ = json.NewEncoder(w).Encode(map[string]any{
			"lifted":    []map[string]any{{"runtime_name": "Claude · подписки", "until": "2026-10-21T20:00:00Z", "reason": "usage limit: подписки runtime выше лимита агентам"}},
			"remaining": []map[string]any{},
		})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "test-token")
	cmd := newRuntimeUnavailableRecheckCommand()
	out, err := captureStdout(t, func() error { return cmd.RunE(cmd, nil) })
	if err != nil {
		t.Fatal(err)
	}
	if method != "POST" || path != "/api/runtime-unavailable/recheck" || !strings.Contains(out, "Lifted:") || !strings.Contains(out, "Claude · подписки") {
		t.Fatalf("%s %s:\n%s", method, path, out)
	}
}
