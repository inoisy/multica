package service

import (
	"context"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// quotaDefaultWait is how long a runtime stays unavailable when the error
// names no reset.
const quotaDefaultWait = 60 * time.Minute

// quotaPastResetWait is how long a runtime stays unavailable when the error
// names a reset that has already passed: the limit lifted by its own words,
// the load the refusal read was taken before the reset. A short pause lets
// the next measurement catch up.
const quotaPastResetWait = 5 * time.Minute

// quotaMaxWait bounds a parsed reset: a weekly window is the longest any
// provider reports, and a typo must not park a runtime for a year.
const quotaMaxWait = 7 * 24 * time.Hour

var (
	quotaInRe       = regexp.MustCompile(`(?i)(?:try again|retry|available again|resets?)\s+(?:in|after)\s+((?:\d+\s*(?:d|days?|h|hours?|hrs?|m|mins?|minutes?|s|secs?|seconds?)\s*,?\s*(?:and\s+)?)+)`)
	quotaUnitRe     = regexp.MustCompile(`(?i)(\d+)\s*(d|days?|h|hours?|hrs?|m|mins?|minutes?|s|secs?|seconds?)`)
	quotaRetryHdrRe = regexp.MustCompile(`(?i)retry-after:?\s*(\d+)\b`)
	quotaEpochRe    = regexp.MustCompile(`(?i)"?resets?_?(?:at|_at)?"?\s*[:=]\s*(\d{10})\b`)
	quotaDateRe     = regexp.MustCompile(`(?i)(?:reset|resets|available|try again)[^0-9]{0,40}?(\d{4}-\d{2}-\d{2})[ t](\d{1,2}:\d{2}(?::\d{2})?)(.*)`)
	quotaClockRe    = regexp.MustCompile(`(?i)(?:resets?|reset at|try again at|available at)\s+(?:at\s+)?(\d{1,2})(?::(\d{2}))?\s*(am|pm)?(.*)`)
)

// quotaUTCZone refuses named/offset zones rather than guessing the provider's
// local time. A missing zone means UTC, independently of account or runtime.
func quotaUTCZone(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return true
	}
	if strings.HasPrefix(s, "(") {
		end := strings.IndexByte(s, ')')
		return end > 0 && (strings.EqualFold(s[1:end], "UTC") || strings.EqualFold(s[1:end], "GMT"))
	}
	word := strings.Fields(s)[0]
	word = strings.TrimRight(word, ".,;·")
	if strings.EqualFold(word, "UTC") || strings.EqualFold(word, "GMT") || strings.EqualFold(word, "Z") {
		return true
	}
	// Punctuation after an unzoned clock is not a timezone.
	return s[0] == '.' || s[0] == ',' || s[0] == ';' || strings.HasPrefix(s, "·")
}

// ParseQuotaReset accepts only valid UTC/GMT (or unzoned UTC) times within a
// week. An invalid, ambiguous or distant reset keeps the ordinary unknown wait.
func ParseQuotaReset(text, runtimeName, provider string, now time.Time) time.Time {
	fallback := now.Add(unknownResetWait)
	accept := func(t time.Time) time.Time {
		if !t.After(now) || t.Sub(now) > quotaMaxWait {
			return fallback
		}
		return t
	}
	if m := quotaInRe.FindStringSubmatch(text); m != nil {
		var d time.Duration
		for _, u := range quotaUnitRe.FindAllStringSubmatch(m[1], -1) {
			n, err := strconv.ParseInt(u[1], 10, 64)
			if err != nil || n > int64(quotaMaxWait/time.Second) {
				return fallback
			}
			unit := time.Second
			switch strings.ToLower(u[2])[0] {
			case 'd':
				unit = 24 * time.Hour
			case 'h':
				unit = time.Hour
			case 'm':
				unit = time.Minute
			}
			if n > int64(quotaMaxWait/unit) {
				return fallback
			}
			d += time.Duration(n) * unit
			if d > quotaMaxWait {
				return fallback
			}
		}
		return accept(now.Add(d))
	}
	if m := quotaRetryHdrRe.FindStringSubmatch(text); m != nil {
		n, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || n > int64(quotaMaxWait/time.Second) {
			return fallback
		}
		return accept(now.Add(time.Duration(n) * time.Second))
	}
	if m := quotaEpochRe.FindStringSubmatch(text); m != nil {
		n, _ := strconv.ParseInt(m[1], 10, 64)
		return accept(time.Unix(n, 0))
	}
	if m := quotaDateRe.FindStringSubmatch(text); m != nil {
		if !quotaUTCZone(m[3]) {
			return fallback
		}
		layout := "2006-01-02 15:04"
		if strings.Count(m[2], ":") == 2 {
			layout += ":05"
		}
		t, err := time.ParseInLocation(layout, m[1]+" "+m[2], time.UTC)
		if err != nil {
			return fallback
		}
		if !t.After(now) {
			return now.Add(quotaPastResetWait)
		}
		return accept(t)
	}
	if m := quotaClockRe.FindStringSubmatch(text); m != nil {
		if !quotaUTCZone(m[4]) {
			return fallback
		}
		hour, _ := strconv.Atoi(m[1])
		minute := 0
		if m[2] != "" {
			minute, _ = strconv.Atoi(m[2])
		}
		if m[3] != "" {
			if hour < 1 || hour > 12 {
				return fallback
			}
			hour %= 12
			if strings.EqualFold(m[3], "pm") {
				hour += 12
			}
		}
		if hour > 23 || minute > 59 {
			return fallback
		}
		utc := now.UTC()
		t := time.Date(utc.Year(), utc.Month(), utc.Day(), hour, minute, 0, 0, time.UTC)
		if !t.After(now) {
			t = t.Add(24 * time.Hour)
		}
		return accept(t)
	}
	return fallback
}

// isQuotaReason reports a provider quota or capacity failure: the reasons the
// route already treats as "wait for the provider".
func isQuotaReason(reason string) bool {
	r := taskfailure.Reason(reason)
	return r == taskfailure.ReasonAgentProviderQuotaLimit || r == taskfailure.ReasonAgentProviderCapacityOrRateLimit
}

// quotaRetryEligible mirrors the auto-retry scope: issue and chat runs, not
// autopilot or triage runs. The attempt budget does not apply: every repeat
// moves to another model or waits, it never hammers the same limit.
func quotaRetryEligible(t db.AgentTaskQueue) bool {
	return !t.AutopilotRunID.Valid && !IsTriageTask(t) && (t.IssueID.Valid || t.ChatSessionID.Valid)
}

// QuotaUnavailableUntil is until when a runtime stays unavailable after a
// quota failure: the reset the daemon reported apart from the text when it is
// ahead (bounded by quotaMaxWait), otherwise the reset the error text names.
func QuotaUnavailableUntil(reported time.Time, errMsg, runtimeName, provider string, now time.Time) time.Time {
	if reported.After(now) {
		if reported.Sub(now) > quotaMaxWait {
			return now.Add(unknownResetWait)
		}
		return reported
	}
	return ParseQuotaReset(errMsg, runtimeName, provider, now)
}

// markRuntimeUnavailable records the refusal of the run's runtime until the
// reset the daemon reported or, without one, the reset its error names. The
// kind of refusal is a code: the daemon's own refusals on the agent limit or
// on disabled accounts, otherwise the provider's. A provider refusal of a run
// whose account the daemon confirmed belongs to that account; without the
// fact it closes the whole runtime.
func markRuntimeUnavailable(ctx context.Context, q *db.Queries, t db.AgentTaskQueue, errMsg string, reported, now time.Time, refusalCodes ...string) error {
	rt, err := q.GetAgentRuntime(ctx, t.RuntimeID)
	if err != nil {
		return err
	}
	until := QuotaUnavailableUntil(reported, errMsg, runtimeDisplayName(rt), rt.Provider, now)
	code := RefusalCodeForFailure(errMsg, refusalCodes...)
	var account pgtype.UUID
	if fact, ok := ParseAccountFact(t.AccountFact); ok && code == UnavailableProviderLimit {
		account = util.MustParseUUID(fact.SubscriptionID)
	}
	_, err = q.InsertRuntimeUnavailable(ctx, db.InsertRuntimeUnavailableParams{
		WorkspaceID: rt.WorkspaceID, RuntimeID: rt.ID,
		Until:  pgtype.Timestamptz{Time: until, Valid: true},
		Reason: clip(t.FailureReason.String+": "+errMsg, 300), TaskID: t.ID,
		ReasonCode: code, SubscriptionID: account,
	})
	if err == nil {
		slog.Info("model choice: runtime refused a run", "runtime_id", util.UUIDToString(rt.ID), "until", until,
			"task_id", util.UUIDToString(t.ID), "reason_code", code, "account", util.UUIDToString(account))
	}
	return err
}

// clip trims s to at most n runes, marking a cut with an ellipsis.
func clip(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
