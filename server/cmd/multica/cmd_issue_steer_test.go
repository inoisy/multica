package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

const steerTestTask = "abcd1234-0000-4000-8000-000000000001"
const steerTestOtherTask = "abcd1234-0000-4000-8000-000000000002"
const steerTestComment = "12345678-0000-4000-8000-000000000001"

func TestIssueSteerHelp(t *testing.T) {
	root := &cobra.Command{Use: "multica"}
	issue := &cobra.Command{Use: "issue"}
	steer := newIssueSteerCommand()
	root.AddCommand(issue)
	issue.AddCommand(steer)
	applyTemplates(root)
	var out bytes.Buffer
	steer.SetOut(&out)
	if err := steer.Help(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"multica issue steer <issue> [<run-id>]", "only running turn", "--client-request-id", "retry:", "agent tokens cannot steer"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help missing %q: %s", want, out.String())
		}
	}
}

func TestIssueSteerSelectRun(t *testing.T) {
	for _, tc := range []struct {
		name, input, wantError string
		runs                   []map[string]any
	}{
		{name: "only running", runs: []map[string]any{{"id": steerTestOtherTask, "status": "queued"}, {"id": steerTestTask, "status": "running"}}},
		{name: "none", wantError: "no running", runs: []map[string]any{{"id": steerTestTask, "status": "completed"}}},
		{name: "several", wantError: steerTestOtherTask, runs: []map[string]any{{"id": steerTestTask, "status": "running"}, {"id": steerTestOtherTask, "status": "running"}}},
		{name: "full", input: steerTestTask},
		{name: "prefix", input: "abcd1234", runs: []map[string]any{{"id": steerTestTask, "status": "running"}}},
		{name: "ambiguous prefix", input: "abcd1234", wantError: "ambiguous", runs: []map[string]any{{"id": steerTestTask}, {"id": steerTestOtherTask}}},
		{name: "unknown prefix", input: "ffffffff", wantError: "no task run", runs: []map[string]any{{"id": steerTestTask}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/issues/issue-1/task-runs" {
					t.Errorf("unexpected path: %s", r.URL)
					http.NotFound(w, r)
					return
				}
				_ = json.NewEncoder(w).Encode(tc.runs)
			}))
			defer srv.Close()
			got, err := resolveSteerRun(context.Background(), cli.NewAPIClient(srv.URL, "ws", "test"), "issue-1", tc.input)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("got %q, %v; want error containing %q", got, err, tc.wantError)
				}
				if tc.name == "several" && !strings.Contains(err.Error(), steerTestTask) {
					t.Fatalf("missing run in choices: %v", err)
				}
			} else if err != nil || got != steerTestTask {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}

func TestIssueSteerCreateAndIdempotency(t *testing.T) {
	var requestID string
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/issues/MUL-1":
			fmt.Fprint(w, `{"id":"issue-1"}`)
		case r.Method == "GET" && r.URL.Path == "/api/issues/issue-1/task-runs":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": steerTestTask, "status": "running"}})
		case r.Method == "POST" && r.URL.Path == "/api/issues/issue-1/tasks/"+steerTestTask+"/supplements":
			var body struct {
				Content         string `json:"content"`
				ClientRequestID string `json:"client_request_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.Content != "Не повторяй работу" {
				t.Errorf("content: %q", body.Content)
			}
			if _, err := uuid.Parse(body.ClientRequestID); err != nil {
				t.Errorf("request ID: %v", err)
			}
			if requestID != "" && requestID != body.ClientRequestID {
				t.Errorf("request ID changed on retry")
			}
			requestID = body.ClientRequestID
			posts++
			_ = json.NewEncoder(w).Encode(map[string]any{"id": steerTestComment, "supplement_task_id": steerTestTask, "supplement_status": "pending"})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	t.Setenv("MULTICA_TASK_CONFIG_ROOT", t.TempDir())
	for i := 0; i < 2; i++ {
		cmd := newIssueSteerCommand()
		var out, diag bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&diag)
		args := []string{"MUL-1", "--content", "Не повторяй работу", "--output", "json"}
		if i == 1 {
			args = append(args, "--client-request-id", requestID)
		}
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		var receipt map[string]any
		if err := json.Unmarshal(out.Bytes(), &receipt); err != nil {
			t.Fatal(err)
		}
		for k, want := range map[string]string{"comment_id": steerTestComment, "task_id": steerTestTask, "status": "pending", "client_request_id": requestID} {
			if receipt[k] != want {
				t.Errorf("%s=%v want %s", k, receipt[k], want)
			}
		}
		if !strings.Contains(diag.String(), requestID) {
			t.Fatal("request ID not available before response")
		}
	}
	if posts != 2 {
		t.Fatalf("posts=%d", posts)
	}
}

func TestIssueSteerWaitCommand(t *testing.T) {
	for _, tc := range []struct{ status, wantError string }{
		{"delivered", ""}, {"failed", "provider_rejected"}, {"pending", "timed out"},
	} {
		t.Run(tc.status, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/api/issues/MUL-1":
					fmt.Fprint(w, `{"id":"issue-1"}`)
				case r.Method == "POST":
					_ = json.NewEncoder(w).Encode(map[string]any{"id": steerTestComment, "supplement_task_id": steerTestTask, "supplement_status": "pending"})
				case r.URL.Path == "/api/issues/issue-1/timeline":
					_ = json.NewEncoder(w).Encode([]map[string]any{{"type": "activity", "id": "activity-1"}, {"type": "comment", "id": steerTestComment, "supplement_task_id": steerTestTask, "supplement_status": tc.status, "supplement_failure_reason": "provider_rejected", "supplement_delivered_at": "2026-10-24T16:00:00Z"}})
				default:
					t.Errorf("unexpected %s", r.URL)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			setCLITestServerEnv(t, srv.URL)
			t.Setenv("MULTICA_TOKEN", "mat_test-token")
			t.Setenv("MULTICA_TASK_CONFIG_ROOT", t.TempDir())
			cmd := newIssueSteerCommand()
			var out, diag bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&diag)
			timeout := "2s"
			if tc.status == "pending" {
				timeout = "25ms"
			}
			cmd.SetArgs([]string{"MUL-1", steerTestTask, "--content", "message", "--wait", "--timeout", timeout, "--output", "json"})
			err := cmd.Execute()
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("err=%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatalf("no parseable receipt after wait: %q: %v", out.String(), err)
			}
			if got["comment_id"] != steerTestComment || got["task_id"] != steerTestTask || got["status"] != tc.status {
				t.Fatalf("receipt=%v", got)
			}
			if tc.status == "delivered" && got["delivered_at"] != "2026-10-24T16:00:00Z" {
				t.Fatalf("missing timestamp: %v", got)
			}
		})
	}
}

func TestIssueSteerContentSources(t *testing.T) {
	const body = "Инструкции\nс literal \\n"
	for _, source := range []string{"file", "stdin"} {
		t.Run(source, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					fmt.Fprint(w, `{"id":"issue-1"}`)
					return
				}
				var got map[string]string
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Error(err)
				}
				if got["content"] != body {
					t.Errorf("content=%q", got["content"])
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"id": steerTestComment, "supplement_task_id": steerTestTask, "supplement_status": "pending"})
			}))
			defer srv.Close()
			setCLITestServerEnv(t, srv.URL)
			t.Setenv("MULTICA_TOKEN", "mat_test-token")
			t.Setenv("MULTICA_TASK_CONFIG_ROOT", t.TempDir())
			cmd := newIssueSteerCommand()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			args := []string{"MUL-1", steerTestTask}
			if source == "file" {
				t.Chdir(t.TempDir())
				if err := os.WriteFile("instructions.md", []byte(body+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				cmd.SetArgs(append(args, "--content-file", "./instructions.md"))
				if err := cmd.Execute(); err != nil {
					t.Fatal(err)
				}
			} else {
				cmd.SetArgs(append(args, "--content-stdin"))
				pipeStdin(t, body+"\n", func() {
					if err := cmd.Execute(); err != nil {
						t.Fatal(err)
					}
				})
			}
		})
	}
}

func TestIssueSteerErrors(t *testing.T) {
	for _, tc := range []struct {
		status     int
		code, want string
	}{
		{409, "task_supplement_turn_ended", "run has already ended"},
		{412, "task_supplement_unsupported", "restart"},
		{409, "task_supplement_not_retryable", "cannot be retried"},
		{409, "", "already in use"},
		{403, "", "permission"},
	} {
		t.Run(fmt.Sprint(tc.status, tc.code), func(t *testing.T) {
			err := steerAPIError(&cli.HTTPError{StatusCode: tc.status, Body: fmt.Sprintf(`{"code":%q,"error":"client_request_id is already in use"}`, tc.code)})
			if got := cli.FormatError(err, false); !strings.Contains(got, tc.want) {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestIssueSteerRetry(t *testing.T) {
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/issues/MUL-1":
			fmt.Fprint(w, `{"id":"issue-1"}`)
		case "/api/issues/issue-1/task-runs":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": steerTestTask, "status": "running"}})
		case "/api/issues/issue-1/tasks/" + steerTestTask + "/supplements/" + steerTestComment + "/retry":
			if r.Method != "POST" {
				t.Errorf("method %s", r.Method)
			}
			posts++
			_ = json.NewEncoder(w).Encode(map[string]any{"supplement_task_id": steerTestTask, "supplement_status": "pending"})
		default:
			t.Errorf("unexpected %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	t.Setenv("MULTICA_TOKEN", "mat_test-token")
	t.Setenv("MULTICA_TASK_CONFIG_ROOT", t.TempDir())
	cmd := newIssueSteerCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"retry", "MUL-1", "abcd1234", steerTestComment, "--output", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if posts != 1 || got["comment_id"] != steerTestComment || got["task_id"] != steerTestTask || got["status"] != "pending" {
		t.Fatalf("posts=%d receipt=%v", posts, got)
	}
}

func TestIssueSteerWait(t *testing.T) {
	for _, tc := range []struct{ name, status, wantErr string }{
		{"delivered", "delivered", ""},
		{"timeout", "pending", "timed out"},
		{"failed", "failed", "provider_rejected"},
		{"missing metadata", "", "timed out"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reads++
				// The comment list carries no receipts; only the timeline does.
				if r.URL.Path != "/api/issues/issue-1/timeline" || r.URL.RawQuery != "" {
					t.Errorf("wrong receipt read: %s", r.URL)
				}
				// The first receipt belongs to another run: waiting must use the exact run.
				_ = json.NewEncoder(w).Encode([]map[string]any{{"type": "comment", "id": "other-comment", "supplement_task_id": steerTestTask, "supplement_status": "delivered"}, {"type": "comment", "id": steerTestComment, "supplement_task_id": steerTestOtherTask, "supplement_status": "delivered", "supplements": []map[string]any{{"task_id": steerTestOtherTask, "status": "delivered"}, {"task_id": steerTestTask, "status": tc.status, "failure_reason": "provider_rejected"}}}})
			}))
			defer srv.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
			defer cancel()
			got, err := waitForSteer(ctx, cli.NewAPIClient(srv.URL, "ws", "test"), "issue-1", steerReceipt{CommentID: steerTestComment, TaskID: steerTestTask, Status: "pending"}, time.Millisecond)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err=%v", err)
				}
			} else if err != nil || got.Status != "delivered" {
				t.Fatalf("got=%v err=%v", got, err)
			}
			if reads == 0 {
				t.Fatal("did not poll")
			}
		})
	}
}

func TestIssueSteerValidation(t *testing.T) {
	for _, args := range [][]string{
		{"MUL-1"},
		{"MUL-1", "--content", " "},
		{"MUL-1", "--content", "x", "--content-stdin"},
		{"MUL-1", "--content", "", "--content-file", "instructions.md"},
		{"MUL-1", "--content", "x", "--client-request-id", "bad"},
		{"MUL-1", "--content", "x", "--timeout", "0s"},
		{"MUL-1", "--content", "x", "--output", "yaml"},
		{"retry", "MUL-1", steerTestTask, "bad"},
	} {
		cmd := newIssueSteerCommand()
		cmd.SetArgs(args)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		if err := cmd.Execute(); err == nil {
			t.Errorf("accepted invalid args: %v", args)
		}
	}
}
