package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

const rerunTestIssue = "a57c0511-1ebc-471d-a314-438ca16cc75d"

type apiCall struct {
	method, path string
	body         map[string]any
}

// recordingServer answers any API call and records what the CLI sent.
func recordingServer(t *testing.T, calls *[]apiCall) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := apiCall{method: r.Method, path: r.URL.RequestURI()}
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			if err := json.Unmarshal(raw, &call.body); err != nil {
				t.Errorf("body is not JSON: %v", err)
			}
		}
		*calls = append(*calls, call)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "active", "revision": 4, "id": "x"})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "test-token")
	return srv
}

func TestIssueRerunTaskFlagNamesTheRun(t *testing.T) {
	t.Chdir(t.TempDir())
	var calls []apiCall
	recordingServer(t, &calls)
	const run = "0f1c7f3e-2b1a-4c3d-8e5f-6a7b8c9d0e1f"
	cmd := issueRerunCmd
	if err := cmd.Flags().Set("task", run); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Flags().Set("task", "") })
	if err := runIssueRerun(cmd, []string{rerunTestIssue}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].path != "/api/issues/"+rerunTestIssue+"/rerun" || calls[0].body["task_id"] != run {
		t.Fatalf("rerun call: %+v", calls)
	}
}
