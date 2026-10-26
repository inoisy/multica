package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/subscriptions"
)

func TestPrepareSubscriptionConfirmsSnapshotBeforeReturningLaunch(t *testing.T) {
	defer noSleepRetry(t)()
	for _, tc := range []struct {
		name          string
		status, calls int
		fail          bool
	}{
		{"confirmed", 200, 1, false}, {"old server", 404, 1, false}, {"access denied", 404, 1, true}, {"malformed acknowledgement", 200, 1, true}, {"wrong account", 200, 1, true}, {"stale offer", 409, 1, true},
		{"outage", 500, 3, true}, {"retry", 502, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token := filepath.Join(t.TempDir(), "token")
			if err := os.WriteFile(token, []byte("sk-ant-oat01-test"), 0600); err != nil {
				t.Fatal(err)
			}
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "POST" || r.URL.Path != "/api/daemon/tasks/task-1/account" {
					t.Errorf("request %s %s", r.Method, r.URL.Path)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				want := map[string]any{"offer_id": "offer-1", "subscription_id": "sub-1", "pool_id": "pool-1", "pool_revision": float64(7)}
				if !reflect.DeepEqual(body, want) {
					t.Errorf("body %v", body)
				}
				status := tc.status
				if tc.name == "retry" && calls > 1 {
					status = 200
				}
				w.WriteHeader(status)
				if tc.name == "old server" {
					io.WriteString(w, "404 page not found\n")
					return
				}
				if tc.name == "access denied" {
					io.WriteString(w, `{"error":"task not found"}`)
					return
				}
				if tc.name == "malformed acknowledgement" {
					io.WriteString(w, `{}`)
					return
				}
				if tc.name == "wrong account" {
					io.WriteString(w, `{"account_fact":{"subscription_id":"other","pool_id":"pool-1","pool_revision":7}}`)
					return
				}
				if status == 200 {
					io.WriteString(w, `{"account_fact":{"subscription_id":"sub-1","pool_id":"pool-1","pool_revision":7}}`)
				}
			}))
			defer srv.Close()
			d := freshDaemon(srv.URL)
			pool := &subscriptions.Pool{OfferID: "offer-1", PoolID: "pool-1", Revision: 7, SpendOrder: "first", Subscriptions: []subscriptions.Subscription{{ID: "sub-1", Key: "test", Family: "claude", Auth: "claude_oauth", CredentialPath: token}}}
			run, err := d.prepareSubscriptionRun(context.Background(), Task{ID: "task-1", SubscriptionPool: pool}, "", d.logger)
			if (err != nil) != tc.fail || calls != tc.calls {
				t.Fatalf("run=%v err=%v calls=%d", run, err, calls)
			}
			if tc.fail && run != nil {
				t.Fatal("a rejected confirmation returned launch credentials")
			}
		})
	}
}

func TestAccountConfirmationOldAndIncompletePayloads(t *testing.T) {
	for _, raw := range []string{
		`{"subscription_pool":{"subscriptions":[{"key":"old"}]}}`,
		`{"subscription_pool":{"offer_id":"offer","subscriptions":[{"key":"old"}]}}`,
	} {
		var task Task
		if err := json.Unmarshal([]byte(raw), &task); err != nil {
			t.Fatal(err)
		}
		client := NewClient("https://unused.test")
		calls := 0
		client.client.Transport = startTaskTransport(func(*http.Request) (*http.Response, error) {
			calls++
			t.Error("unexpected HTTP call")
			return nil, io.EOF
		})
		_, err := client.ConfirmTaskAccount(context.Background(), task, task.SubscriptionPool.Subscriptions[0])
		if calls != 0 || (err != nil) != (task.SubscriptionPool.OfferID != "") {
			t.Fatalf("err=%v calls=%d", err, calls)
		}
	}
}

func TestStructuredSubscriptionRefusalSurvivesOutbox(t *testing.T) {
	for _, code := range []string{"agent_cap_refusal", "disabled_refusal"} {
		err := fmt.Errorf("wrapped: %w", &subscriptions.BlockedError{Text: "arbitrary translated message", RefusalCode: code})
		if got := taskRunFailureReason(err); got != "agent_error.provider_quota_limit" {
			t.Fatalf("classification=%s", got)
		}
		store := newTerminalReportStore(Config{WorkspacesRoot: t.TempDir(), ServerBaseURL: "https://api.test", DaemonID: "daemon-test"})
		if err := store.enqueue(terminalTaskReport{kind: terminalTaskReportFail, taskID: "t", errorMessage: err.Error(), failureReason: taskRunFailureReason(err), refusalCode: accountRefusalCode(err)}); err != nil {
			t.Fatal(err)
		}
		items, err := store.list()
		if err != nil || len(items) != 1 {
			t.Fatalf("outbox %v %v", items, err)
		}
		var got string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			got, _ = body["refusal_code"].(string)
			io.WriteString(w, `{}`)
		}))
		d := freshDaemon(srv.URL)
		if err := d.sendTerminalTaskReport(context.Background(), items[0].report, nil); err != nil {
			t.Fatal(err)
		}
		srv.Close()
		if got != code {
			t.Fatalf("refusal=%s want %s", got, code)
		}
	}
}
