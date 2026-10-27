package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
)

const testWorkflowID = "abababab-2222-4222-8222-222222222222"
const testWorkflowProjectID = "cccccccc-3333-4333-8333-333333333333"

func setupWorkflowCLI(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	t.Chdir(t.TempDir())
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	t.Setenv("MULTICA_SERVER_URL", srv.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "workspace-test")
	t.Setenv("MULTICA_TOKEN", "mat_workflow-test")
}

func writeWorkflowFile(t *testing.T, name, body string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowCLIWritePreservesDefinitionAndScope(t *testing.T) {
	for _, action := range []string{"create", "update"} {
		t.Run(action, func(t *testing.T) {
			calls := 0
			setupWorkflowCLI(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				method, path := "POST", "/api/issue-workflows"
				if action == "update" {
					method, path = "PATCH", path+"/"+testWorkflowID
				}
				if r.Method != method || r.URL.Path != path {
					t.Errorf("request = %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer mat_workflow-test" || r.Header.Get("X-Workspace-ID") != "workspace-test" {
					t.Error("lost task identity or workspace scope")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body["name"] != "Пилот" || body["description"] != "" {
					t.Errorf("fields = %+v", body)
				}
				step := body["steps"].([]any)[0].(map[string]any)
				if step["instructions"] != "Проверить\nс возвратом \\n" || step["next_status_key"] != "qa" || step["back_status_key"] != "todo" || step["handler"].(map[string]any)["type"] != "project_lead" {
					t.Errorf("step = %+v", step)
				}
				if action == "update" && (body["dry_run"] != true || body["status_mapping"].(map[string]any)["review"] != "dev" || len(body["status_mapping"].(map[string]any)) != 1) {
					t.Errorf("missing migration options: %+v", body)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"id": testWorkflowID, "dry_run": action == "update"})
			})
			definition := `{"name":"Пилот","description":"old","steps":[{"status_key":"dev","handler":{"type":"project_lead"},"instructions":"Проверить\nс возвратом \\n","next_status_key":"qa","back_status_key":"todo"}]}`
			if action == "update" {
				definition = strings.TrimSuffix(definition, "}") + `,"status_mapping":{"stale":"todo"}}`
			}
			writeWorkflowFile(t, "workflow.json", definition)
			args := []string{action}
			if action == "update" {
				args = append(args, testWorkflowID, "--dry-run", "--status-mapping", "mapping.json")
				writeWorkflowFile(t, "mapping.json", `{"review":"dev"}`)
			}
			args = append(args, "--file", "workflow.json", "--description", "")
			cmd := newWorkflowCommand()
			cmd.SetArgs(args)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("calls = %d, want one write", calls)
			}
		})
	}
}

func TestWorkflowCLIRejectsInvalidInputBeforeRequest(t *testing.T) {
	setupWorkflowCLI(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	})
	for _, tc := range []struct {
		name, body string
		args       []string
	}{
		{"unknown field", `{"name":"test","step":[]}`, []string{"create", "--file", "input.json"}},
		{"unknown step field", `{"name":"test","steps":[{"status":"dev"}]}`, []string{"create", "--file", "input.json"}},
		{"trailing data", `{"name":"test"} {}`, []string{"create", "--file", "input.json"}},
		{"null", `null`, []string{"create", "--file", "input.json"}},
		{"fake create preview", `{"name":"test","dry_run":true}`, []string{"create", "--file", "input.json"}},
		{"create mapping", `{"name":"test","status_mapping":{"qa":"dev"}}`, []string{"create", "--file", "input.json"}},
		{"missing name", `{}`, []string{"create", "--file", "input.json"}},
		{"no changes", `{}`, []string{"update", testWorkflowID, "--file", "input.json"}},
		{"output typo", `{}`, []string{"create", "--name", "test", "--output", "jsno"}},
		{"invalid ID", `{}`, []string{"delete", "../other"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeWorkflowFile(t, "input.json", tc.body)
			cmd := newWorkflowCommand()
			cmd.SilenceUsage, cmd.SilenceErrors = true, true
			cmd.SetArgs(tc.args)
			if err := cmd.Execute(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	outside := t.TempDir() + "/workflow.json"
	writeWorkflowFile(t, outside, `{"name":"test"}`)
	cmd := newWorkflowCommand()
	cmd.SetArgs([]string{"create", "--file", outside})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "outside the current working directory") {
		t.Fatalf("containment error = %v", err)
	}
}

func TestWorkflowCLIExportDefaultAndPartialUpdate(t *testing.T) {
	setupWorkflowCLI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/issue-workflows":
			_ = json.NewEncoder(w).Encode(map[string]any{"workflows": []any{map[string]any{"id": testWorkflowID, "is_default": true}}})
		case "GET /api/issue-workflows/" + testWorkflowID:
			_, _ = w.Write([]byte(`{"id":"` + testWorkflowID + `","workspace_id":"ws","owner_id":null,"is_default":true,"name":"Default","description":"","steps":[{"status_key":"dev","handler":{"type":"agent","id":"agent-id"}}],"project_ids":["project-id"]}`))
		case "PATCH /api/issue-workflows/" + testWorkflowID:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if len(body) != 1 || body["description"] != "" {
				t.Errorf("partial update clobbers fields: %+v", body)
			}
			_, _ = w.Write([]byte(`{"id":"` + testWorkflowID + `"}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})
	cmd := newWorkflowCommand()
	cmd.SetArgs([]string{"get", "default", "--definition"})
	out, err := captureStdout(t, cmd.Execute)
	if err != nil {
		t.Fatal(err)
	}
	var def workflowWrite
	if err := json.Unmarshal([]byte(out), &def); err != nil {
		t.Fatal(err)
	}
	if def.Name == nil || *def.Name != "Default" || def.Steps == nil || len(*def.Steps) != 1 || strings.Contains(out, "workspace_id") {
		t.Fatalf("export = %s", out)
	}
	cmd = newWorkflowCommand()
	cmd.SetArgs([]string{"update", testWorkflowID, "--description", ""})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowCLIServerFailuresPreserveHTTPError(t *testing.T) {
	for _, status := range []int{403, 409} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			setupWorkflowCLI(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"code":"workflow_in_use","error":"cannot delete"}`))
			})
			cmd := newWorkflowCommand()
			cmd.SetArgs([]string{"delete", testWorkflowID})
			err := cmd.Execute()
			var httpErr *cli.HTTPError
			if !errors.As(err, &httpErr) || httpErr.StatusCode != status || calls != 1 {
				t.Fatalf("error = %v, calls = %d", err, calls)
			}
		})
	}
}

func TestProjectWorkflowCLISetAndDefaultPreview(t *testing.T) {
	for _, target := range []string{testWorkflowID, "default", "--default"} {
		t.Run(target, func(t *testing.T) {
			calls := 0
			setupWorkflowCLI(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "POST" || r.URL.Path != "/api/projects/"+testWorkflowProjectID+"/workflow" {
					t.Errorf("request = %s %s", r.Method, r.URL.Path)
				}
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				want := any(testWorkflowID)
				if target != testWorkflowID {
					want = nil
				}
				id, present := body["workflow_id"]
				if !present || id != want || body["dry_run"] != true || body["status_mapping"].(map[string]any)["review"] != "dev" {
					t.Errorf("body = %+v", body)
				}
				_, _ = w.Write([]byte(`{"dry_run":true,"plan":{"affected_issues":1}}`))
			})
			writeWorkflowFile(t, "mapping.json", `{"review":"dev"}`)
			args := []string{"set", testWorkflowProjectID, "--dry-run", "--status-mapping", "mapping.json"}
			if target == "--default" {
				args = append(args, target)
			} else {
				args = append(args, "--workflow", target)
			}
			cmd := newProjectWorkflowCommand()
			cmd.SetArgs(args)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("calls = %d", calls)
			}
		})
	}
}

func TestProjectWorkflowCLIRequiresExplicitTarget(t *testing.T) {
	setupWorkflowCLI(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected request") })
	for _, flags := range [][]string{nil, {"--workflow", ""}, {"--default=false"}, {"--default", "--workflow", testWorkflowID}, {"--workflow", "invalid"}} {
		cmd := newProjectWorkflowCommand()
		cmd.SetArgs(append([]string{"set", testWorkflowProjectID}, flags...))
		if err := cmd.Execute(); err == nil {
			t.Fatalf("accepted flags %v", flags)
		}
	}
}

func TestProjectWorkflowCLIGetInheritedDefault(t *testing.T) {
	setupWorkflowCLI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/projects/" + testWorkflowProjectID:
			_, _ = w.Write([]byte(`{"workflow_id":null}`))
		case "/api/issue-workflows":
			_, _ = w.Write([]byte(`{"workflows":[{"id":"` + testWorkflowID + `","is_default":true}]}`))
		case "/api/issue-workflows/" + testWorkflowID:
			_, _ = w.Write([]byte(`{"name":"Default","is_default":true}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	cmd := newProjectWorkflowCommand()
	cmd.SetArgs([]string{"get", testWorkflowProjectID})
	out, err := captureStdout(t, cmd.Execute)
	if err != nil || !strings.Contains(out, `"is_default": true`) {
		t.Fatalf("output = %s, err = %v", out, err)
	}
}

func TestWorkflowCLIListAndDelete(t *testing.T) {
	setupWorkflowCLI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/issue-workflows":
			_, _ = w.Write([]byte(`{"workflows":[{"id":"` + testWorkflowID + `","name":"Pilot","workspace_id":"workspace-test","owner_id":"owner-test","is_default":false,"steps":[],"project_ids":[],"created_at":"created","updated_at":"updated"}]}`))
		case "DELETE /api/issue-workflows/" + testWorkflowID:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})
	cmd := newWorkflowCommand()
	cmd.SetArgs([]string{"list", "--output", "json"})
	out, err := captureStdout(t, cmd.Execute)
	if err != nil {
		t.Fatal(err)
	}
	var rows []workflowDTO
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].OwnerID == nil || *rows[0].OwnerID != "owner-test" || rows[0].WorkspaceID != "workspace-test" || rows[0].UpdatedAt != "updated" {
		t.Fatalf("list lost metadata: %s", out)
	}
	cmd = newWorkflowCommand()
	cmd.SetArgs([]string{"delete", testWorkflowID})
	out, err = captureStdout(t, cmd.Execute)
	if err != nil || !strings.Contains(out, `"deleted": true`) {
		t.Fatalf("delete output: %s, err = %v", out, err)
	}
}

func TestWorkflowCLIEmptyStepsAndNewStatus(t *testing.T) {
	for _, steps := range []string{`[]`, `[{"new_status":{"name":"Check","category":"started","color":"#123456"},"handler":{"type":"none"}}]`} {
		t.Run(steps, func(t *testing.T) {
			setupWorkflowCLI(t, func(w http.ResponseWriter, r *http.Request) {
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				var got, want any
				_ = json.Unmarshal(body["steps"], &got)
				_ = json.Unmarshal([]byte(steps), &want)
				gotJSON, _ := json.Marshal(got)
				wantJSON, _ := json.Marshal(want)
				if string(gotJSON) != string(wantJSON) {
					t.Errorf("steps = %s, want %s", gotJSON, wantJSON)
				}
				_, _ = w.Write([]byte(`{"id":"` + testWorkflowID + `"}`))
			})
			writeWorkflowFile(t, "workflow.json", `{"steps":`+steps+`}`)
			cmd := newWorkflowCommand()
			cmd.SetArgs([]string{"update", testWorkflowID, "--file", "workflow.json"})
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWorkflowAcceptIsGone(t *testing.T) {
	// Acceptance was removed (STT-429): the reviewer finishes the work, so
	// neither `workflow accept` nor the hidden `issue conveyor` group exists.
	if c, _, err := newWorkflowCommand().Find([]string{"accept"}); err == nil && c.Name() == "accept" {
		t.Fatal("multica workflow accept still exists")
	}
	if c, _, err := issueCmd.Find([]string{"conveyor"}); err == nil && c.Name() == "conveyor" {
		t.Fatal("multica issue conveyor still exists")
	}
}
