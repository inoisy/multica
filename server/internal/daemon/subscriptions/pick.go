// Package subscriptions picks which provider subscription a run spends and
// prepares the run's environment for it. It is the daemon-side successor of
// the subs-pick / subs-run host wrappers: the server sends the runtime's
// ordered subscriptions at claim time, the daemon chooses one, points the CLI
// at its credential file and launches the provider CLI directly.
//
// Credentials stay files on the daemon host. Nothing here logs or returns a
// token; errors name the credential path, never its content.
package subscriptions

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Subscription is one entry of a runtime's pool as the server sends it.
type Subscription struct {
	Key            string         `json:"key"`
	Provider       string         `json:"provider"`
	Family         string         `json:"family"`
	Auth           string         `json:"auth"`
	CredentialPath string         `json:"credential_path"`
	Windows        []string       `json:"windows"`
	EffectiveCaps  map[string]int `json:"effective_caps"`
	Settings       Settings       `json:"settings"`
	AccountGroup   string         `json:"account_group"`
	Disabled       bool           `json:"disabled"`
	DisabledUntil  *string        `json:"disabled_until"`
	Usage          []Usage        `json:"usage"`
}

// Settings are the non-secret launch settings of a subscription.
type Settings struct {
	// Env holds launch variables for API-key providers: base URL, models,
	// context window. Only an allow-listed set reaches the CLI.
	Env        map[string]string `json:"env,omitempty"`
	ProbeModel string            `json:"probe_model,omitempty"`
}

// Usage is the latest server-side snapshot of one quota window.
type Usage struct {
	Window       string  `json:"window"`
	UsedPercent  float64 `json:"used_percent"`
	ResetsAt     *string `json:"resets_at"`
	LimitReached bool    `json:"limit_reached"`
	ObservedAt   string  `json:"observed_at"`
}

// Pool is a runtime's subscriptions in spend order.
type Pool struct {
	ProfileID     string         `json:"profile_id"`
	SpendOrder    string         `json:"spend_order"`
	Subscriptions []Subscription `json:"subscriptions"`
}

// CredentialState is what the daemon knows about a credential file.
type CredentialState string

const (
	CredentialPresent    CredentialState = "present"
	CredentialMissing    CredentialState = "missing"
	CredentialRevoked    CredentialState = "revoked"
	CredentialUnreadable CredentialState = "unreadable"
	CredentialUnknown    CredentialState = "unknown"
)

// Window is one measured quota window.
type Window struct {
	Key     string     `json:"window"`
	Used    float64    `json:"used_percent"`
	ResetAt *time.Time `json:"-"`
	Limit   bool       `json:"limit_reached"`
}

// State is a subscription's load as far as the daemon knows it.
type State struct {
	// Credential is missing/revoked when the account cannot be used at all.
	Credential CredentialState
	// Windows is empty when the load is unknown.
	Windows    []Window
	ObservedAt time.Time
}

func (s State) window(key string) (Window, bool) {
	for _, w := range s.Windows {
		if w.Key == key {
			return w, true
		}
	}
	return Window{}, false
}

// Known reports whether the load was measured.
func (s State) Known() bool { return len(s.Windows) > 0 }

// Mark is a candidate with its state, in spend order.
type Mark struct {
	Sub   Subscription
	State State
}

type status int

const (
	statusUnknown status = iota
	statusYes
	statusNo
)

// statusOf: yes — every capped window is below its agent cap; no — a window is
// at its cap, the provider refused, or there is no usable credential; unknown —
// nothing measured.
func statusOf(m Mark) status {
	if m.State.Credential == CredentialMissing || m.State.Credential == CredentialRevoked {
		return statusNo
	}
	if !m.State.Known() {
		return statusUnknown
	}
	for _, w := range m.State.Windows {
		if w.Limit {
			return statusNo
		}
	}
	for key, cap := range m.Sub.EffectiveCaps {
		if w, ok := m.State.window(key); ok && w.Used >= float64(cap) {
			return statusNo
		}
	}
	return statusYes
}

func usableCredential(m Mark) bool {
	return m.State.Credential != CredentialMissing && m.State.Credential != CredentialRevoked
}

// Decide picks the subscription to spend; marks are in spend order and not empty.
//
//	first    — the first one below its caps; none — the last one with a credential.
//	balanced — below caps or unknown; an unknown one first (by order),
//	           otherwise the least-used week; none — the first with a credential.
func Decide(order string, marks []Mark) Subscription {
	usable := make([]Mark, 0, len(marks))
	for _, m := range marks {
		if usableCredential(m) {
			usable = append(usable, m)
		}
	}
	pool := usable
	if len(pool) == 0 {
		pool = marks
	}
	if order != "balanced" {
		for _, m := range marks {
			if statusOf(m) == statusYes {
				return m.Sub
			}
		}
		return pool[len(pool)-1].Sub
	}
	var candidates []Mark
	for _, m := range marks {
		if s := statusOf(m); s == statusYes || s == statusUnknown {
			candidates = append(candidates, m)
		}
	}
	if len(candidates) == 0 {
		return pool[0].Sub
	}
	for _, m := range candidates {
		if statusOf(m) == statusUnknown {
			return m.Sub
		}
	}
	best := candidates[0]
	bestWeek := math.Inf(1)
	for _, m := range candidates {
		if w, ok := m.State.window("week"); ok && w.Used < bestWeek {
			best, bestWeek = m, w.Used
		}
	}
	return best.Sub
}

// windowNames are the labels the refusal text uses, as subs-pick did.
var windowNames = map[string]string{"5h": "5ч", "week": "неделя"}

func windowName(key string) string {
	if n, ok := windowNames[key]; ok {
		return n
	}
	return key
}

// Blocked returns why an agent run must not start, or "": only when every
// subscription's load is known (at least one measured) and each one sits at an
// agent cap, a provider refusal or has no credential. Unknown load never
// blocks. The text is the run's error: "usage limit" makes the server treat it
// as a quota failure and move on, "reset at … UTC" says until when.
func Blocked(marks []Mark) string {
	measured := false
	for _, m := range marks {
		if statusOf(m) != statusNo {
			return ""
		}
		if m.State.Known() {
			measured = true
		}
	}
	if !measured {
		return ""
	}
	parts := make([]string, 0, len(marks))
	var frees []time.Time
	allReset := true
	for _, m := range marks {
		if !m.State.Known() {
			parts = append(parts, fmt.Sprintf("%s %s", m.Sub.Key, m.State.Credential))
			allReset = false
			continue
		}
		var over []string
		var resets []time.Time
		keys := make([]string, 0, len(m.Sub.EffectiveCaps))
		for k := range m.Sub.EffectiveCaps {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if w, ok := m.State.window(k); ok && w.Used >= float64(m.Sub.EffectiveCaps[k]) {
				over = append(over, fmt.Sprintf("%s %d%% ≥ %d%%", windowName(k), int(math.Round(w.Used)), m.Sub.EffectiveCaps[k]))
				if w.ResetAt != nil {
					resets = append(resets, *w.ResetAt)
				} else {
					allReset = false
				}
			}
		}
		if len(over) == 0 {
			parts = append(parts, m.Sub.Key+" отказ провайдера")
			for _, w := range m.State.Windows {
				if w.Limit && w.ResetAt != nil {
					resets = append(resets, *w.ResetAt)
				}
			}
			if len(resets) == 0 {
				allReset = false
			}
		} else {
			parts = append(parts, m.Sub.Key+" "+strings.Join(over, ", "))
		}
		if len(resets) > 0 {
			latest := resets[0]
			for _, r := range resets[1:] {
				if r.After(latest) {
					latest = r
				}
			}
			frees = append(frees, latest)
		}
	}
	text := "usage limit: подписки runtime выше лимита агентам (остаток — резерв людям): " + strings.Join(parts, "; ")
	if allReset && len(frees) == len(marks) && len(frees) > 0 {
		earliest := frees[0]
		for _, f := range frees[1:] {
			if f.Before(earliest) {
				earliest = f
			}
		}
		text += ", reset at " + earliest.UTC().Format("2006-01-02 15:04") + " UTC"
	}
	return text
}

// Active drops subscriptions an admin disabled for now.
func Active(subs []Subscription) []Subscription {
	out := make([]Subscription, 0, len(subs))
	for _, s := range subs {
		if !s.Disabled {
			out = append(out, s)
		}
	}
	return out
}

// DisabledText is the refusal when every subscription of a runtime is disabled.
func DisabledText(subs []Subscription) string {
	until := ""
	for _, s := range subs {
		if s.DisabledUntil != nil && (until == "" || *s.DisabledUntil < until) {
			until = *s.DisabledUntil
		}
	}
	return "usage limit: подписки среды отключены до " + until
}

// StateFromUsage turns the server's latest snapshots into a State.
func StateFromUsage(usage []Usage) State {
	var st State
	st.Credential = CredentialUnknown
	for _, u := range usage {
		w := Window{Key: u.Window, Used: u.UsedPercent, Limit: u.LimitReached}
		if u.ResetsAt != nil {
			if t, err := time.Parse(time.RFC3339, *u.ResetsAt); err == nil {
				w.ResetAt = &t
			}
		}
		if t, err := time.Parse(time.RFC3339, u.ObservedAt); err == nil && t.After(st.ObservedAt) {
			st.ObservedAt = t
		}
		st.Windows = append(st.Windows, w)
	}
	return st
}
