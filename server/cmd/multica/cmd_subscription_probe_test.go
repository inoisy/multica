package main

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func runProbeCapturing(t *testing.T, key string) (string, error) {
	t.Helper()
	probePollInterval = time.Millisecond
	cmd := subscriptionTestCmd(func(*cobra.Command) {})
	orig := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	client, err := newAPIClient(cmd)
	if err != nil {
		os.Stdout = orig
		t.Fatalf("client: %v", err)
	}
	_ = cmd.Flags().Set("output", "table")
	runErr := probeSubscription(cmd, client, key, 5*time.Second)
	w.Close()
	os.Stdout = orig
	out, _ := io.ReadAll(r)
	return string(out), runErr
}

func TestSubscriptionProbePrintsFreeAndLifted(t *testing.T) {
	got := subscriptionTestServer(t, map[string]any{
		"POST /api/subscriptions/claude-main/probe":   map[string]any{"probe": map[string]any{"id": "p1", "state": "probing"}},
		"GET /api/subscriptions/claude-main/probe/p1": map[string]any{"probe": map[string]any{"id": "p1", "state": "done", "lifted": 2}},
	})
	out, err := runProbeCapturing(t, "claude-main")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !strings.Contains(out, "claude-main is free: lifted 2 limit blocks") {
		t.Fatalf("output: %q", out)
	}
	if (*got)[0].Method != "POST" || len(*got) != 2 {
		t.Fatalf("requests: %+v", *got)
	}
}

func TestSubscriptionProbePrintsResetWhenStillLimited(t *testing.T) {
	subscriptionTestServer(t, map[string]any{
		"POST /api/subscriptions/claude-main/probe":   map[string]any{"probe": map[string]any{"id": "p1", "state": "probing"}},
		"GET /api/subscriptions/claude-main/probe/p1": map[string]any{"probe": map[string]any{"id": "p1", "state": "limited", "resets_at": "2026-11-17T07:40:00Z"}},
	})
	out, err := runProbeCapturing(t, "claude-main")
	if err != nil {
		t.Fatalf("a limited answer is a result, not an error: %v", err)
	}
	if !strings.Contains(out, "still at its limit until 2026-11-17T07:40:00Z") {
		t.Fatalf("output: %q", out)
	}
}

func TestSubscriptionProbeFailureIsAnError(t *testing.T) {
	subscriptionTestServer(t, map[string]any{
		"POST /api/subscriptions/claude-main/probe":   map[string]any{"probe": map[string]any{"id": "p1", "state": "probing"}},
		"GET /api/subscriptions/claude-main/probe/p1": map[string]any{"probe": map[string]any{"id": "p1", "state": "expired", "message": "the host did not answer in time"}},
	})
	if _, err := runProbeCapturing(t, "claude-main"); err == nil || !strings.Contains(err.Error(), "did not answer") {
		t.Fatalf("err: %v", err)
	}
}
