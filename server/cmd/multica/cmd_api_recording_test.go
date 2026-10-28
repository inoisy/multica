package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
