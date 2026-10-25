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
const quotaMaxWait = 8 * 24 * time.Hour

var zaiLocation = time.FixedZone("UTC+8", 8*60*60)

var (
	quotaInRe       = regexp.MustCompile(`(?i)(?:try again|retry|available again|resets?)\s+(?:in|after)\s+((?:\d+\s*(?:d|days?|h|hours?|hrs?|m|mins?|minutes?|s|secs?|seconds?)\s*,?\s*(?:and\s+)?)+)`)
	quotaUnitRe     = regexp.MustCompile(`(?i)(\d+)\s*(d|days?|h|hours?|hrs?|m|mins?|minutes?|s|secs?|seconds?)`)
	quotaRetryHdrRe = regexp.MustCompile(`(?i)retry-after:?\s*(\d+)\b`)
	quotaEpochRe    = regexp.MustCompile(`(?i)"?resets?_?(?:at|_at)?"?\s*[:=]\s*(\d{10})\b`)
	quotaDateRe     = regexp.MustCompile(`(?i)(?:reset|resets|available|try again)[^0-9]{0,40}?(\d{4}-\d{2}-\d{2})[ t](\d{1,2}:\d{2}(?::\d{2})?)(z|[+-]\d{2}:?\d{2})?`)
	quotaClockRe    = regexp.MustCompile(`(?i)(?:resets?|reset at|try again at|available at)\s+(?:at\s+)?(\d{1,2})(?::(\d{2}))?\s*(am|pm)?(?:\s*\(([A-Za-z_]+/[A-Za-z_]+|UTC|GMT)\))?`)
)

// isZai reports whether the runtime belongs to z.ai (Zhipu GLM), whose limit
// messages give wall-clock times in UTC+8.
func isZai(runtimeName, provider, text string) bool {
	all := strings.ToLower(runtimeName + " " + provider + " " + text)
	return strings.Contains(all, "z.ai") || strings.Contains(all, "zai") || strings.Contains(all, "glm") ||
		strings.Contains(all, "zhipu") || strings.Contains(all, "bigmodel")
}

// ParseQuotaReset reads when a provider limit lifts from the failure text.
// Durations ("try again in 2h 13m"), epoch seconds, dates and wall-clock
// times ("resets 3pm (Europe/Moscow)") are understood; a wall-clock time
// without a zone is UTC, or UTC+8 for z.ai. No reset in the text, or one that
// is not in the future: now + 60 minutes. A full date that has passed is
// the reset the error meant, not a wall-clock time: now + 5 minutes.
func ParseQuotaReset(text, runtimeName, provider string, now time.Time) time.Time {
	fallback := now.Add(quotaDefaultWait)
	accept := func(t time.Time) (time.Time, bool) {
		if !t.After(now) {
			return time.Time{}, false
		}
		if t.Sub(now) > quotaMaxWait {
			return now.Add(quotaMaxWait), true
		}
		return t, true
	}
	if m := quotaInRe.FindStringSubmatch(text); m != nil {
		var d time.Duration
		for _, u := range quotaUnitRe.FindAllStringSubmatch(m[1], -1) {
			n, _ := strconv.Atoi(u[1])
			switch strings.ToLower(u[2])[0] {
			case 'd':
				d += time.Duration(n) * 24 * time.Hour
			case 'h':
				d += time.Duration(n) * time.Hour
			case 'm':
				d += time.Duration(n) * time.Minute
			case 's':
				d += time.Duration(n) * time.Second
			}
		}
		if t, ok := accept(now.Add(d)); ok {
			return t
		}
	}
	if m := quotaRetryHdrRe.FindStringSubmatch(text); m != nil {
		n, _ := strconv.Atoi(m[1])
		if t, ok := accept(now.Add(time.Duration(n) * time.Second)); ok {
			return t
		}
	}
	if m := quotaEpochRe.FindStringSubmatch(text); m != nil {
		n, _ := strconv.ParseInt(m[1], 10, 64)
		if t, ok := accept(time.Unix(n, 0)); ok {
			return t
		}
	}
	loc := time.UTC
	if isZai(runtimeName, provider, text) {
		loc = zaiLocation
	}
	if m := quotaDateRe.FindStringSubmatch(text); m != nil {
		clock := m[2]
		if strings.Count(clock, ":") == 1 {
			clock += ":00"
		}
		raw := m[1] + "T" + clock
		var t time.Time
		var err error
		if zone := strings.ToUpper(m[3]); zone != "" {
			if zone != "Z" && !strings.Contains(zone, ":") {
				zone = zone[:3] + ":" + zone[3:]
			}
			t, err = time.Parse(time.RFC3339, raw+zone)
		} else {
			t, err = time.ParseInLocation("2006-01-02T15:04:05", raw, loc)
		}
		if err == nil {
			if t, ok := accept(t); ok {
				return t
			}
			// The clock pattern below would read the year's first digits as
			// an hour ("reset at 2026-…" → 20:00 tomorrow).
			return now.Add(quotaPastResetWait)
		}
	}
	if m := quotaClockRe.FindStringSubmatch(text); m != nil {
		hour, _ := strconv.Atoi(m[1])
		minute := 0
		if m[2] != "" {
			minute, _ = strconv.Atoi(m[2])
		}
		switch strings.ToLower(m[3]) {
		case "pm":
			if hour < 12 {
				hour += 12
			}
		case "am":
			if hour == 12 {
				hour = 0
			}
		}
		where := loc
		if m[4] != "" {
			if l, err := time.LoadLocation(m[4]); err == nil {
				where = l
			}
		}
		if hour < 24 && minute < 60 {
			local := now.In(where)
			t := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, where)
			if !t.After(now) {
				t = t.Add(24 * time.Hour)
			}
			if t, ok := accept(t); ok {
				return t
			}
		}
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
			return now.Add(quotaMaxWait)
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
