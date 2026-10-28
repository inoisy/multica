package subscriptions

import (
	"context"
	"sync"
	"time"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Probe cadence: Anthropic answers 429 to frequent polling, so Claude
// accounts are measured less often than the others.
const (
	DefaultProbeInterval       = 5 * time.Minute
	DefaultClaudeProbeInterval = 30 * time.Minute
)

// Selector keeps the daemon's own measurements and chooses a subscription per
// run. A measurement is trusted for twice its probe interval; older load —
// local or from the server — counts as unknown, which never blocks a run.
type Selector struct {
	Prober              *Prober
	Interval            time.Duration
	ClaudeInterval      time.Duration
	Now                 func() time.Time
	CheckCredentialFunc func(Subscription) CredentialState

	mu       sync.Mutex
	measured map[string]State
	probedAt map[string]time.Time
}

// NewSelector returns a selector with production defaults.
func NewSelector() *Selector {
	return &Selector{Prober: NewProber(), Interval: DefaultProbeInterval, ClaudeInterval: DefaultClaudeProbeInterval, Now: time.Now}
}

func (s *Selector) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Selector) interval(sub Subscription) time.Duration {
	if sub.Family == "claude" && sub.Auth != "api_key" {
		if s.ClaudeInterval > 0 {
			return s.ClaudeInterval
		}
		return DefaultClaudeProbeInterval
	}
	if s.Interval > 0 {
		return s.Interval
	}
	return DefaultProbeInterval
}

// StateFor is the freshest trustworthy load of sub: the daemon's own probe or
// the server's snapshot, whichever is newer and not stale, with the windows
// whose reset has passed zeroed. The credential is checked on disk every
// time — a missing file must never be spent.
func (s *Selector) StateFor(sub Subscription) State {
	check := s.CheckCredentialFunc
	if check == nil {
		check = CheckCredential
	}
	cred := check(sub)
	maxAge := 2 * s.interval(sub)
	best := State{}
	s.mu.Lock()
	local, ok := s.measured[sub.Key]
	s.mu.Unlock()
	if ok && s.now().Sub(local.ObservedAt) <= maxAge {
		best = local
	}
	if server := StateFromUsage(sub.Usage); server.Known() && s.now().Sub(server.ObservedAt) <= maxAge && server.ObservedAt.After(best.ObservedAt) {
		windows := server.Windows
		best = State{Windows: windows, ObservedAt: server.ObservedAt, Credential: best.Credential}
	}
	if cred != CredentialPresent || best.Credential == "" || best.Credential == CredentialUnknown {
		best.Credential = cred
	}
	return best.Settle(s.now())
}

// Choice is the outcome for one run.
type Choice struct {
	Subscription Subscription
	Marks        []Mark
	// Blocked is the refusal text for an agent run, or "".
	Blocked     string
	RefusalCode string
	// BlockedUntil is the earliest reset among the refused pool's
	// subscriptions; zero when one of them names none.
	BlockedUntil time.Time
}

// Choose picks the subscription for a run of pool. agentRun holds back the
// people's reserve: only agent runs are refused when every subscription is at
// its cap. ok is false for an empty pool.
func (s *Selector) Choose(pool Pool, agentRun bool) (Choice, bool) {
	if len(pool.Subscriptions) == 0 {
		return Choice{}, false
	}
	active := Active(pool.Subscriptions)
	if len(active) == 0 {
		c := Choice{Subscription: pool.Subscriptions[0]}
		if agentRun {
			c.Blocked = DisabledText(pool.Subscriptions)
			c.RefusalCode = protocol.AccountRefusalDisabled
			c.BlockedUntil = disabledUntil(pool.Subscriptions)
		}
		return c, true
	}
	marks := make([]Mark, len(active))
	for i, sub := range active {
		marks[i] = Mark{Sub: sub, State: s.StateFor(sub)}
	}
	c := Choice{Marks: marks}
	if len(marks) == 1 {
		c.Subscription = marks[0].Sub
	} else {
		c.Subscription = Decide(pool.SpendOrder, marks)
	}
	if agentRun {
		c.Blocked = Blocked(marks)
		if c.Blocked != "" {
			c.BlockedUntil = FreeAt(marks, s.now(), 0)
			c.RefusalCode = protocol.AccountRefusalAgentCap
		}
	}
	return c, true
}

// Report is one probe outcome to send to the server.
type Report struct {
	Key        string
	State      State
	Err        error
	Credential CredentialState
}

// Due reports whether sub should be probed now.
func (s *Selector) Due(sub Subscription) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.probedAt[sub.Key]
	return !ok || s.now().Sub(at) >= s.interval(sub)
}

// ProbeNow measures sub and remembers the result.
func (s *Selector) ProbeNow(ctx context.Context, sub Subscription) Report {
	st, err := s.Prober.Probe(ctx, sub)
	return s.Record(sub, st, err)
}

// Record remembers a measurement taken outside ProbeNow, such as the check of
// a pasted token at sign-in, so the probe loop does not repeat it at once.
func (s *Selector) Record(sub Subscription, st State, err error) Report {
	s.mu.Lock()
	if s.measured == nil {
		s.measured = map[string]State{}
		s.probedAt = map[string]time.Time{}
	}
	s.probedAt[sub.Key] = s.now()
	if err == nil {
		s.measured[sub.Key] = st
	}
	s.mu.Unlock()
	return Report{Key: sub.Key, State: st, Err: err, Credential: st.Credential}
}

// disabledUntil is the earliest end of the subscriptions' disabling; zero
// when none names one.
func disabledUntil(subs []Subscription) time.Time {
	var earliest time.Time
	for _, sub := range subs {
		if sub.DisabledUntil == nil {
			continue
		}
		if t, err := time.Parse(time.RFC3339, *sub.DisabledUntil); err == nil && (earliest.IsZero() || t.Before(earliest)) {
			earliest = t
		}
	}
	return earliest
}
