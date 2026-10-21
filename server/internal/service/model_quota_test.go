package service

import (
	"testing"
	"time"

	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

func TestQuotaResetParsing(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name, text, runtime string
		want                time.Time
	}{
		{"duration", "Rate limited. Try again in 2h 13m.", "codex", now.Add(2*time.Hour + 13*time.Minute)},
		{"seconds", "retry after 90 seconds", "claude", now.Add(90 * time.Second)},
		{"retry-after header", "HTTP 429; Retry-After: 120", "claude", now.Add(2 * time.Minute)},
		{"epoch", `{"error":"usage_limit_reached","resets_at":1792422000}`, "codex", time.Date(2026, 10, 19, 15, 0, 0, 0, time.UTC)},
		{"clock with zone", "You've hit your limit · resets 3pm (Europe/Moscow)", "claude", time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC).Add(0)},
		{"clock utc tomorrow", "Your limit resets at 11:30", "claude", time.Date(2026, 10, 20, 11, 30, 0, 0, time.UTC)},
		{"zai date in utc+8", "Usage limit reached for 5 hour. Your limit will reset at 2026-10-19 23:53:00", "glm", time.Date(2026, 10, 19, 15, 53, 0, 0, time.UTC)},
		{"explicit zone wins", "limit; available again 2026-10-19T18:00:00Z", "glm", time.Date(2026, 10, 19, 18, 0, 0, 0, time.UTC)},
		{"nothing to read", "You have hit your usage limit", "codex", now.Add(time.Hour)},
		{"past reset", "Try again in 0m", "codex", now.Add(time.Hour)},
		{"far future clamped", "Try again in 40 days", "codex", now.Add(8 * 24 * time.Hour)},
	}
	for _, c := range cases {
		got := ParseQuotaReset(c.text, c.runtime, "", now)
		if c.name == "clock with zone" {
			// 3pm in Moscow (UTC+3) is 12:00 UTC: already passed at 12:00, so tomorrow.
			c.want = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
		}
		if !got.Equal(c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got.UTC(), c.want.UTC())
		}
	}
}

func TestQuotaReasonsAndSubsPickWitness(t *testing.T) {
	if !isQuotaReason(string(taskfailure.ReasonAgentProviderQuotaLimit)) || !isQuotaReason(string(taskfailure.ReasonAgentProviderCapacityOrRateLimit)) {
		t.Fatal("quota and capacity are quota reasons")
	}
	if isQuotaReason(string(taskfailure.ReasonAgentUnknown)) {
		t.Fatal("unknown is not a quota reason")
	}
	if got := taskfailure.Classify("subs-pick: все подписки runtime выше лимита агентам"); got != taskfailure.ReasonAgentProviderQuotaLimit {
		t.Fatalf("classify subs-pick: %s", got)
	}
	if got := taskfailure.NormalizeDaemonReason("agent_error.unknown", "подписки runtime выше лимита агентам"); got != taskfailure.ReasonAgentProviderQuotaLimit {
		t.Fatalf("normalize subs-pick: %s", got)
	}
}

// TOON-274: the daemon refused at 20:30:18 with the reset of claude-main,
// 20:30 the same evening. The reset had just passed; the clock pattern read
// "20" of the year as an hour and parked the runtime until 20:00 tomorrow.
func TestQuotaResetPastDateIsNotAClock(t *testing.T) {
	now := time.Date(2026, 10, 20, 20, 30, 18, 0, time.UTC)
	text := "usage limit: подписки runtime выше лимита агентам (остаток — резерв людям): claude-second неделя 54% ≥ 50%; claude-main 5ч 97% ≥ 75%, reset at 2026-10-20 20:30 UTC"
	if got := ParseQuotaReset(text, "Claude · подписки", "claude", now); !got.Equal(now.Add(quotaPastResetWait)) {
		t.Fatalf("past reset: got %v, want %v", got, now.Add(quotaPastResetWait))
	}
	if got := ParseQuotaReset(text, "Claude · подписки", "claude", now.Add(-time.Hour)); !got.Equal(time.Date(2026, 10, 20, 20, 30, 0, 0, time.UTC)) {
		t.Fatalf("future reset: got %v", got)
	}
}

func TestQuotaUnavailableUntilPrefersReported(t *testing.T) {
	now := time.Date(2026, 10, 20, 20, 0, 0, 0, time.UTC)
	text := "usage limit: …, reset at 2026-10-21 20:00 UTC"
	reported := now.Add(30 * time.Minute)
	if got := QuotaUnavailableUntil(reported, text, "claude", "claude", now); !got.Equal(reported) {
		t.Fatalf("reported reset wins: %v", got)
	}
	if got := QuotaUnavailableUntil(time.Time{}, text, "claude", "claude", now); !got.Equal(time.Date(2026, 10, 21, 20, 0, 0, 0, time.UTC)) {
		t.Fatalf("no report: the text, got %v", got)
	}
	if got := QuotaUnavailableUntil(now.Add(-time.Minute), text, "claude", "claude", now); !got.Equal(time.Date(2026, 10, 21, 20, 0, 0, 0, time.UTC)) {
		t.Fatalf("past report: the text, got %v", got)
	}
	if got := QuotaUnavailableUntil(now.Add(30*24*time.Hour), text, "claude", "claude", now); !got.Equal(now.Add(quotaMaxWait)) {
		t.Fatalf("far report clamped: %v", got)
	}
}
