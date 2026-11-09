package handler

import (
	"testing"
	"time"
)

func TestVerdictUntilHidesEstimate(t *testing.T) {
	at := time.Date(2026, 11, 9, 13, 10, 0, 0, time.UTC)
	if got := verdictUntil(at, true, true); got == nil || *got != "2026-11-09T13:10:00Z" {
		t.Fatalf("known end: %v", got)
	}
	// "Нужен вход" has no end: now+1h would slide on every refresh.
	if got := verdictUntil(at, true, false); got != nil {
		t.Fatalf("unknown end shows no time, got %s", *got)
	}
	if got := verdictUntil(at, false, true); got != nil {
		t.Fatalf("available: no time, got %s", *got)
	}
}
