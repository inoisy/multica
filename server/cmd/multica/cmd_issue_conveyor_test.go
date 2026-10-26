package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

const conveyorTestIssue = "a57c0511-1ebc-471d-a314-438ca16cc75d"

type conveyorCall struct {
	method, path string
	body         map[string]any
}

// conveyorServer answers a route's API and records what the CLI sent.
func conveyorServer(t *testing.T, calls *[]conveyorCall) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := conveyorCall{method: r.Method, path: r.URL.RequestURI()}
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

func TestIssueConveyorCLIActionsCarryTheRevisionTheyAct(t *testing.T) {
	t.Chdir(t.TempDir())
	var calls []conveyorCall
	conveyorServer(t, &calls)

	run := func(args ...string) {
		t.Helper()
		cmd := newIssueConveyorCommand()
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	run("resume", conveyorTestIssue, "--step", "dev")
	if len(calls) != 2 || calls[0].method != "GET" || calls[1].method != "PUT" {
		t.Fatalf("resume did not read the revision it acts on first: %+v", calls)
	}
	if b := calls[1].body; b["action"] != "resume" || b["from_step"] != "dev" || b["expected_revision"] != float64(4) {
		t.Fatalf("resume body: %+v", b)
	}

	calls = nil
	run("disable", conveyorTestIssue, "--revision", "9")
	if len(calls) != 1 || calls[0].body["expected_revision"] != float64(9) {
		t.Fatalf("explicit revision was not used as given: %+v", calls)
	}

	calls = nil
	run("enable", conveyorTestIssue, "--route", "dev:A,qa:B,close:C", "--from-step", "dev")
	last := calls[len(calls)-1]
	if last.method != "PUT" || last.body["route"] != "dev:A,qa:B,close:C" || last.body["from_step"] != "dev" || last.body["action"] != "enable" {
		t.Fatalf("enable body: %+v", last)
	}
}

func TestIssueConveyorCLIResultPostsTheFileAsIs(t *testing.T) {
	t.Chdir(t.TempDir())
	var calls []conveyorCall
	conveyorServer(t, &calls)
	const result = `{"schema_version":1,"outcome":"pass","report":"done"}`
	if err := os.WriteFile("result.json", []byte(result), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := newIssueConveyorCommand()
	cmd.SetArgs([]string{"result", conveyorTestIssue, "--file", "result.json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].method != "POST" || calls[0].path != "/api/issues/"+conveyorTestIssue+"/conveyor/results" || calls[0].body["outcome"] != "pass" {
		t.Fatalf("result call: %+v", calls)
	}

	for name, content := range map[string]string{"not json": "{", "empty": ""} {
		if err := os.WriteFile("bad.json", []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := newIssueConveyorCommand()
		cmd.SetArgs([]string{"result", conveyorTestIssue, "--file", "bad.json"})
		if err := cmd.Execute(); err == nil {
			t.Errorf("%s: the CLI sent a result that is not JSON", name)
		}
	}
	cmd = newIssueConveyorCommand()
	cmd.SetArgs([]string{"result", conveyorTestIssue})
	if err := cmd.Execute(); err == nil {
		t.Error("result without --file")
	}
}

func TestIssueConveyorCLIEventsPageByCursor(t *testing.T) {
	t.Chdir(t.TempDir())
	var calls []conveyorCall
	conveyorServer(t, &calls)
	cmd := newIssueConveyorCommand()
	cmd.SetArgs([]string{"events", conveyorTestIssue, "--cursor", "12", "--limit", "5"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].path != "/api/issues/"+conveyorTestIssue+"/conveyor/events?cursor=12&limit=5" {
		t.Fatalf("events call: %+v", calls)
	}
}

func TestIssueRerunTaskFlagNamesTheRun(t *testing.T) {
	t.Chdir(t.TempDir())
	var calls []conveyorCall
	conveyorServer(t, &calls)
	const run = "0f1c7f3e-2b1a-4c3d-8e5f-6a7b8c9d0e1f"
	cmd := issueRerunCmd
	if err := cmd.Flags().Set("task", run); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Flags().Set("task", "") })
	if err := runIssueRerun(cmd, []string{conveyorTestIssue}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].path != "/api/issues/"+conveyorTestIssue+"/rerun" || calls[0].body["task_id"] != run {
		t.Fatalf("rerun call: %+v", calls)
	}
}
