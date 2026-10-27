package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/daemon/subscriptions"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
)

// The one rule that says whether an account, a pool or an environment can run
// something now, and since when it can again. The pool overview, the
// environment, the forecast of a run and the claim all read it; nothing else
// computes availability and nothing stores its verdict.

// Why a row cannot run, as the interface shows it. The states of the
// environment itself (offline, missing, not_permitted, same_family) keep their
// own names.
const (
	ReasonAgentCap        = "agent_cap"        // the account is at the limit agents may spend
	ReasonProviderLimit   = "provider_limit"   // the provider says the window is spent
	ReasonProviderRefusal = "provider_refusal" // the provider refused a run
	ReasonLoginRequired   = "login_required"   // no sign-in on the host
	ReasonDisabled        = "disabled"         // the account is switched off for a time
)

// What runtime_unavailable.reason_code says about a stored refusal.
const (
	UnavailableProviderLimit   = protocol.AccountRefusalProviderLimit // a run failed on a provider quota
	UnavailableAgentCapRefusal = protocol.AccountRefusalAgentCap      // the daemon refused on the agent limit
	UnavailableDisabledRefusal = protocol.AccountRefusalDisabled      // the daemon refused: every account is off
	UnavailableUnknown         = "unknown"                            // no mark; read as a provider limit
)

// AccountUsageMaxAge is how old a snapshot of usage or of a host's sign-in may
// be and still count: an older one says nothing about now.
const AccountUsageMaxAge = time.Hour

// unknownResetWait stands in for a reset nobody reported.
const unknownResetWait = time.Hour

// Account is an account as the rule reads it.
type Account struct {
	Auth          string
	Model         string
	ID            string
	Key           string
	Windows       []string
	Caps          map[string]int
	DisabledUntil time.Time
	Usage         []db.SubscriptionUsage
	Hosts         []db.SubscriptionHostState
	// Refusals are the provider refusals pinned to this account that still hold.
	Refusals []db.RuntimeUnavailable
}

// EffectiveCaps is the limit agents stop at per window: the window's cap, for
// the windows that have one below 100.
func EffectiveCaps(windows []string, caps map[string]int) map[string]int {
	out := map[string]int{}
	keys := append([]string(nil), windows...)
	for k := range caps {
		keys = append(keys, k)
	}
	for _, k := range keys {
		limit := 100
		if c, ok := caps[k]; ok {
			limit = c
		}
		if limit < 100 {
			out[k] = limit
		}
	}
	return out
}

// AccountVerdict says what stands between an account and a run.
type AccountVerdict struct {
	ID  string
	Key string
	// Blocked: no run may spend the account now. Usable: it was measured and is
	// below every limit, so a run may take it.
	Blocked bool
	Usable  bool
	Code    string
	// Until is when it can run again. It is an estimate (now plus an hour) when
	// UntilKnown is false.
	Until      time.Time
	UntilKnown bool
}

// PoolVerdict is the same for a pool: blocked when every account is.
type PoolVerdict struct {
	Blocked    bool
	Usable     bool
	Code       string
	Until      time.Time
	UntilKnown bool
	// Text is a short account-by-account explanation for logs and records.
	Text     string
	Accounts []AccountVerdict
}

type loginState int

const (
	loginUnknown loginState = iota
	loginPresent
	loginAbsent
)

// hostLogin reads what the hosts know of the account's sign-in. With a daemon
// the answer is that host's; without one, any host that is signed in wins and
// only hosts that all lack it make the sign-in absent.
func hostLogin(hosts []db.SubscriptionHostState, daemonID string, now time.Time) loginState {
	fresh := func(h db.SubscriptionHostState) bool {
		return h.CheckedAt.Valid && now.Sub(h.CheckedAt.Time) <= AccountUsageMaxAge
	}
	if daemonID != "" {
		for _, h := range hosts {
			if h.DaemonID != daemonID || !fresh(h) {
				continue
			}
			switch h.CredentialState {
			case "present":
				return loginPresent
			case "missing", "revoked":
				return loginAbsent
			}
		}
		return loginUnknown
	}
	absent := false
	for _, h := range hosts {
		if !fresh(h) {
			continue
		}
		switch h.CredentialState {
		case "present":
			return loginPresent
		case "missing", "revoked":
			absent = true
		}
	}
	if absent {
		return loginAbsent
	}
	return loginUnknown
}

// accountMark turns the account's latest snapshots into the daemon's own view
// of it: snapshots older than AccountUsageMaxAge count as unmeasured, windows
// whose reset has passed count as fresh.
func accountMark(a Account, now time.Time) subscriptions.Mark {
	sub := subscriptions.Subscription{Key: a.Key, Windows: a.Windows, EffectiveCaps: EffectiveCaps(a.Windows, a.Caps)}
	usage := make([]subscriptions.Usage, 0, len(a.Usage))
	for _, u := range a.Usage {
		if !u.ObservedAt.Valid || now.Sub(u.ObservedAt.Time) > AccountUsageMaxAge {
			continue
		}
		item := subscriptions.Usage{Window: u.WindowKey, UsedPercent: u.UsedPercent, LimitReached: u.LimitReached || u.UsedPercent >= 100, ObservedAt: u.ObservedAt.Time.UTC().Format(time.RFC3339)}
		if u.ResetsAt.Valid {
			at := u.ResetsAt.Time.UTC().Format(time.RFC3339)
			item.ResetsAt = &at
		}
		usage = append(usage, item)
	}
	st := subscriptions.StateFromUsage(usage)
	if now.Sub(st.ObservedAt) > AccountUsageMaxAge {
		st = subscriptions.State{}
	}
	st.Credential = subscriptions.CredentialPresent
	return subscriptions.Mark{Sub: sub, State: st.Settle(now)}
}

// windowBlock is the code of the window that holds the account the longest.
func windowBlock(m subscriptions.Mark) (code string, until time.Time, known bool) {
	known = true
	for _, w := range m.State.Windows {
		cap, capped := m.Sub.EffectiveCaps[w.Key]
		atCap := capped && w.Used >= float64(cap)
		if !w.Limit && !atCap {
			continue
		}
		c := ReasonAgentCap
		if w.Limit {
			c = ReasonProviderLimit
		}
		switch {
		case w.ResetAt == nil:
			// No reset: this window decides, whatever the others say.
			return c, time.Time{}, false
		case code == "" || w.ResetAt.After(until):
			code, until = c, *w.ResetAt
		}
	}
	return code, until, known
}

// EvaluateAccount is the verdict on one account on the host of daemonID (""
// for no particular host) at now.
func EvaluateAccount(a Account, daemonID string, now time.Time) AccountVerdict {
	v := AccountVerdict{ID: a.ID, Key: a.Key}
	type cause struct {
		code  string
		until time.Time
		known bool
	}
	var causes []cause

	if a.DisabledUntil.After(now) {
		causes = append(causes, cause{ReasonDisabled, a.DisabledUntil, true})
	}
	refusedUntil := AccountRefusedUntil(a, now)
	if !refusedUntil.IsZero() {
		causes = append(causes, cause{ReasonProviderRefusal, refusedUntil, true})
	}
	if hostLogin(a.Hosts, daemonID, now) == loginAbsent {
		causes = append(causes, cause{ReasonLoginRequired, time.Time{}, false})
	}
	mark := accountMark(a, now)
	if subscriptions.Blocked([]subscriptions.Mark{mark}) != "" {
		code, until, known := windowBlock(mark)
		causes = append(causes, cause{code, until, known})
	}

	if len(causes) == 0 {
		v.Usable = subscriptions.Usable(mark)
		return v
	}
	v.Blocked = true
	// The account is free when its last cause lifts; a cause with no end keeps
	// it blocked for an unknown time.
	best := causes[0]
	for _, c := range causes[1:] {
		switch {
		case !best.known:
		case !c.known, c.until.After(best.until):
			best = c
		}
	}
	v.Code, v.Until, v.UntilKnown = best.code, best.until, best.known
	if !v.UntilKnown {
		v.Until = now.Add(unknownResetWait)
	}
	return v
}

// OverAgentCap says whether the account was at or past the limit agents may
// spend in some window, by a measurement that is still fresh. A manual run is
// allowed over it; the journal only records that it was.
func OverAgentCap(a Account, now time.Time) bool {
	mark := accountMark(a, now)
	for _, w := range mark.State.Windows {
		if limit, capped := mark.Sub.EffectiveCaps[w.Key]; capped && w.Used >= float64(limit) {
			return true
		}
	}
	return false
}

// AccountRefusedUntil is the latest active provider refusal of the account,
// independent of any other reason that may keep it unavailable for longer.
func AccountRefusedUntil(a Account, now time.Time) time.Time {
	var until time.Time
	for _, r := range a.Refusals {
		if r.Until.Valid && r.Until.Time.After(now) && r.Until.Time.After(until) {
			until = r.Until.Time
		}
	}
	return until
}

// EvaluatePool is the verdict on a pool: blocked when every account is. It
// frees up when the first account does. An empty pool has no verdict.
func EvaluatePool(accounts []Account, daemonID string, now time.Time) PoolVerdict {
	var pv PoolVerdict
	for _, a := range accounts {
		pv.Accounts = append(pv.Accounts, EvaluateAccount(a, daemonID, now))
	}
	if len(pv.Accounts) == 0 {
		return pv
	}
	pv.Blocked = true
	var first *AccountVerdict
	for i := range pv.Accounts {
		v := &pv.Accounts[i]
		pv.Usable = pv.Usable || v.Usable
		if !v.Blocked {
			pv.Blocked = false
			continue
		}
		if first == nil || v.Until.Before(first.Until) {
			first = v
		}
	}
	if !pv.Blocked {
		return pv
	}
	pv.Code, pv.Until, pv.UntilKnown = first.Code, first.Until, first.UntilKnown
	parts := make([]string, 0, len(pv.Accounts))
	for _, v := range pv.Accounts {
		parts = append(parts, fmt.Sprintf("%s %s", v.Key, v.Code))
	}
	pv.Text = strings.Join(parts, "; ")
	return pv
}

// LoadAccounts reads the accounts with their latest usage, host sign-in states
// and refusals that still hold at now.
func LoadAccounts(ctx context.Context, q *db.Queries, ids []pgtype.UUID, now time.Time) (map[string]Account, error) {
	out := map[string]Account{}
	if len(ids) == 0 {
		return out, nil
	}
	subs, err := q.ListSubscriptionsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	usage, err := q.ListLatestSubscriptionUsageBySubscriptions(ctx, ids)
	if err != nil {
		return nil, err
	}
	hosts, err := q.ListSubscriptionHostStatesBySubscriptions(ctx, ids)
	if err != nil {
		return nil, err
	}
	refusals, err := q.ListActiveAccountRefusals(ctx, db.ListActiveAccountRefusalsParams{SubscriptionIds: ids, Now: pgtype.Timestamptz{Time: now, Valid: true}})
	if err != nil {
		return nil, err
	}
	for _, s := range subs {
		a := Account{Auth: s.Auth, Model: SubscriptionModel(s), ID: util.UUIDToString(s.ID), Key: s.Key, Windows: decodeStringListJSON(s.Windows), Caps: decodeCapsJSON(s.Caps)}
		if s.DisabledUntil.Valid {
			a.DisabledUntil = s.DisabledUntil.Time
		}
		out[a.ID] = a
	}
	for _, u := range usage {
		if a, ok := out[util.UUIDToString(u.SubscriptionID)]; ok {
			a.Usage = append(a.Usage, u)
			out[a.ID] = a
		}
	}
	for _, h := range hosts {
		if a, ok := out[util.UUIDToString(h.SubscriptionID)]; ok {
			a.Hosts = append(a.Hosts, h)
			out[a.ID] = a
		}
	}
	for _, r := range refusals {
		if a, ok := out[util.UUIDToString(r.SubscriptionID)]; ok {
			a.Refusals = append(a.Refusals, r)
			out[a.ID] = a
		}
	}
	return out, nil
}

// StoredRefusalCode reads the code classified at migration or failure time.
// An unclassified row remains an unknown provider refusal; free text never
// decides whether an operational refusal may be lifted.
func StoredRefusalCode(r db.RuntimeUnavailable) string {
	if r.ReasonCode != "" {
		return r.ReasonCode
	}
	return UnavailableUnknown
}

// RefusalCodeForFailure is the code to store for a run that failed on a quota:
// explicit codes take precedence; old daemon text is parsed only here.
func RefusalCodeForFailure(errMsg string, refusalCodes ...string) string {
	if len(refusalCodes) > 0 && refusalCodes[0] != "" {
		if protocol.IsAccountRefusalCode(refusalCodes[0]) {
			return refusalCodes[0]
		}
		return UnavailableUnknown
	}
	switch {
	case strings.Contains(errMsg, taskfailure.SubsPickRefusalWitness):
		return UnavailableAgentCapRefusal
	case strings.Contains(errMsg, subscriptions.DisabledWitness):
		return UnavailableDisabledRefusal
	}
	return UnavailableProviderLimit
}

// sortedKeys is a stable order for maps in logs and tests.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// PoolVerdicts is the verdict on each of the given pools at now, as a reader of
// the pool overview sees it: no particular host, so an account counts as signed
// in when any host has it.
func PoolVerdicts(ctx context.Context, q *db.Queries, pools map[string][]pgtype.UUID, now time.Time) (map[string]PoolVerdict, error) {
	var ids []pgtype.UUID
	seen := map[string]bool{}
	for _, members := range pools {
		for _, id := range members {
			if key := util.UUIDToString(id); !seen[key] {
				seen[key] = true
				ids = append(ids, id)
			}
		}
	}
	accounts, err := LoadAccounts(ctx, q, ids, now)
	if err != nil {
		return nil, err
	}
	out := make(map[string]PoolVerdict, len(pools))
	for pool, members := range pools {
		list := make([]Account, 0, len(members))
		for _, id := range members {
			if a, ok := accounts[util.UUIDToString(id)]; ok {
				list = append(list, a)
			}
		}
		out[pool] = EvaluatePool(list, "", now)
	}
	return out, nil
}
