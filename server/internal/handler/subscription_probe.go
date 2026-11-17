package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

var errNoProbeHost = errors.New("no online host spends this subscription")

// Probe states. A probe starts "probing" and ends in exactly one of the rest.
const (
	probeProbing = "probing"
	// probeDone: the provider answered with no limit; its refusals are lifted.
	probeDone = "done"
	// probeLimited: the provider is still at its limit; the block stays.
	probeLimited = "limited"
	probeFailed  = "failed"
	probeExpired = "expired"
)

// SubscriptionProbe is what the CLI sees of a forced limit re-check.
type SubscriptionProbe struct {
	ID        string  `json:"id"`
	Key       string  `json:"key"`
	State     string  `json:"state"`
	Message   string  `json:"message"`
	Lifted    int     `json:"lifted"`
	ResetsAt  *string `json:"resets_at"`
	StartedAt string  `json:"started_at"`
}

type pendingProbe struct {
	probe       SubscriptionProbe
	runtimeID   string
	workspaceID string
	delivered   bool
	started     time.Time
}

// SubscriptionProbeStore keeps probes in memory: they live for seconds, and a
// server restart only means the user asks again.
type SubscriptionProbeStore struct {
	mu        sync.Mutex
	byID      map[string]*pendingProbe
	timeout   time.Duration
	retention time.Duration
}

// NewSubscriptionProbeStore returns an empty store.
func NewSubscriptionProbeStore() *SubscriptionProbeStore {
	return &SubscriptionProbeStore{byID: map[string]*pendingProbe{}, timeout: 2 * time.Minute, retention: 10 * time.Minute}
}

func (s *SubscriptionProbeStore) expireLocked(now time.Time) {
	for id, p := range s.byID {
		age := now.Sub(p.started)
		if p.probe.State == probeProbing && age > s.timeout {
			p.probe.State, p.probe.Message = probeExpired, "the host did not answer in time"
		}
		if p.probe.State != probeProbing && age > s.retention {
			delete(s.byID, id)
		}
	}
}

// start registers a probe for the key; an unanswered earlier one is replaced.
func (s *SubscriptionProbeStore) start(key, runtimeID, workspaceID string) SubscriptionProbe {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.expireLocked(now)
	for _, p := range s.byID {
		if p.probe.Key == key && p.probe.State == probeProbing {
			p.probe.State, p.probe.Message = probeFailed, "replaced by a new probe"
		}
	}
	p := &pendingProbe{runtimeID: runtimeID, workspaceID: workspaceID, started: now,
		probe: SubscriptionProbe{ID: newShareKey()[5:], Key: key, State: probeProbing, Message: "waiting for the host", StartedAt: now.UTC().Format(time.RFC3339)}}
	s.byID[p.probe.ID] = p
	return p.probe
}

func (s *SubscriptionProbeStore) get(key, id string) *SubscriptionProbe {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked(time.Now())
	if p := s.byID[id]; p != nil && p.probe.Key == key {
		out := p.probe
		return &out
	}
	return nil
}

// popForRuntime hands one undelivered probe to the runtime's daemon.
func (s *SubscriptionProbeStore) popForRuntime(runtimeID string) *pendingProbe {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked(time.Now())
	for _, p := range s.byID {
		if p.runtimeID == runtimeID && p.probe.State == probeProbing && !p.delivered {
			p.delivered = true
			p.probe.Message = "the host is measuring"
			cp := *p
			return &cp
		}
	}
	return nil
}

// answer closes a probing probe of the key with the daemon's verdict.
func (s *SubscriptionProbeStore) answer(id, key, state, message string, lifted int, resetsAt *string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.byID[id]
	if p == nil || p.probe.Key != key || p.probe.State != probeProbing {
		return false
	}
	p.probe.State, p.probe.Message, p.probe.Lifted, p.probe.ResetsAt = state, clip(message, 300), lifted, resetsAt
	return true
}

// pendingSubscriptionProbeFor attaches a probe to a heartbeat ack.
func (h *Handler) pendingSubscriptionProbeFor(ctx context.Context, runtimeID string) *protocol.DaemonHeartbeatPendingSubscriptionProbe {
	if h.SubscriptionProbes == nil {
		return nil
	}
	p := h.SubscriptionProbes.popForRuntime(runtimeID)
	if p == nil {
		return nil
	}
	return &protocol.DaemonHeartbeatPendingSubscriptionProbe{ID: p.probe.ID, Key: p.probe.Key, WorkspaceID: p.workspaceID}
}

// pickProbeRuntime chooses the host that measures: an online runtime of a
// profile spending the subscription, preferring a daemon that last saw the
// credential present.
func (h *Handler) pickProbeRuntime(ctx context.Context, sub db.Subscription) (string, error) {
	online, err := h.onlinePoolRuntimes(ctx, sub)
	if err != nil {
		return "", err
	}
	if len(online) == 0 {
		return "", errNoProbeHost
	}
	present := map[string]bool{}
	if states, err := h.Queries.ListSubscriptionHostStatesBySubscriptions(ctx, []pgtype.UUID{sub.ID}); err == nil {
		for _, st := range states {
			if st.CredentialState == "present" {
				present[st.DaemonID] = true
			}
		}
	}
	sort.SliceStable(online, func(i, j int) bool {
		return present[online[i].DaemonID.String] && !present[online[j].DaemonID.String]
	})
	return uuidToString(online[0].ID), nil
}

// StartSubscriptionProbe — POST /api/subscriptions/{key}/probe. Owner or admin
// of a workspace whose pools spend the subscription. The host measures the
// account now; a free answer lifts the account's provider refusals.
func (h *Handler) StartSubscriptionProbe(w http.ResponseWriter, r *http.Request) {
	wsID, _, ok := h.modelAdmin(w, r)
	if !ok {
		return
	}
	if h.SubscriptionProbes == nil {
		writeError(w, http.StatusServiceUnavailable, "probing is not available on this server")
		return
	}
	ctx := r.Context()
	sub, err := h.Queries.GetSubscriptionByKey(ctx, chi.URLParam(r, "key"))
	if err != nil {
		writeError(w, http.StatusNotFound, "subscription not found")
		return
	}
	spent, err := h.workspacePoolSubscriptions(ctx, wsID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list subscriptions")
		return
	}
	found := false
	for _, s := range spent {
		found = found || s.Key == sub.Key
	}
	if !found {
		writeError(w, http.StatusNotFound, "subscription is not spent by this workspace")
		return
	}
	runtimeID, err := h.pickProbeRuntime(ctx, sub)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	probe := h.SubscriptionProbes.start(sub.Key, runtimeID, uuidToString(wsID))
	slog.Info("subscription probe requested", "key", sub.Key, "runtime_id", runtimeID, "probe_id", probe.ID)
	writeJSON(w, http.StatusAccepted, map[string]any{"probe": probe})
}

// GetSubscriptionProbe — GET /api/subscriptions/{key}/probe/{probeId}.
func (h *Handler) GetSubscriptionProbe(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.modelReader(w, r); !ok {
		return
	}
	var probe *SubscriptionProbe
	if h.SubscriptionProbes != nil {
		probe = h.SubscriptionProbes.get(chi.URLParam(r, "key"), chi.URLParam(r, "probeId"))
	}
	if probe == nil {
		writeError(w, http.StatusNotFound, "probe not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"probe": probe})
}

// probeVerdict reads the daemon's answer to a probe: free when every measured
// window is below its limit, limited with the latest reset otherwise, failed
// when nothing was measured.
func probeVerdict(errText string, windows []probeWindow) (state, message string, resetsAt *string) {
	if len(windows) == 0 {
		if errText == "" {
			errText = "the provider returned no usage"
		}
		return probeFailed, errText, nil
	}
	var latest time.Time
	limited := false
	for _, win := range windows {
		if !win.limit {
			continue
		}
		limited = true
		if win.resetsAt.After(latest) {
			latest = win.resetsAt
		}
	}
	if !limited {
		return probeDone, "the provider has no limit", nil
	}
	if !latest.IsZero() {
		s := latest.UTC().Format(time.RFC3339)
		resetsAt = &s
	}
	return probeLimited, "the provider is still at its limit", resetsAt
}

type probeWindow struct {
	limit    bool
	resetsAt time.Time
}

// settleProbe closes a probe with the daemon's report. A free answer deletes
// the account's provider_limit refusals and reports freed, so the caller lets
// the waiting runs out at once — also when the block was only the old
// snapshot and no refusal row existed.
func (h *Handler) settleProbe(ctx context.Context, probeID string, sub db.Subscription, errText string, windows []probeWindow) (freed bool) {
	state, message, resetsAt := probeVerdict(errText, windows)
	lifted := 0
	if state == probeDone {
		rows, err := h.Queries.DeleteProviderLimitRefusalsBySubscription(ctx, sub.ID)
		if err != nil {
			slog.Warn("subscription probe: lift provider limit failed", "error", err, "key", sub.Key)
			state, message = probeFailed, "the provider is free but the block could not be lifted"
		}
		lifted = len(rows)
	}
	freed = state == probeDone
	if !h.SubscriptionProbes.answer(probeID, sub.Key, state, message, lifted, resetsAt) {
		return freed
	}
	slog.Info("subscription probe answered", "key", sub.Key, "probe_id", probeID, "state", state, "lifted", lifted)
	return freed
}
