package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/daemon/subscriptions"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ---------------------------------------------------------------------------
// Subscriptions, stage B: the daemon chooses, probes and signs in.
//
//   - At claim time a run on a runtime whose profile spends subscriptions
//     carries the pool (subscriptions in order, caps, latest usage); the daemon
//     picks one and launches the provider CLI with that credential.
//   - Daemons report usage snapshots and the state of each credential file.
//   - When every subscription of a runtime is at its agent cap (or disabled),
//     the server marks the runtime unavailable until the earliest reset, so
//     the model route skips it instead of a run failing at start.
//   - Sign-in travels web → server → daemon on the heartbeat. A pasted token
//     lives only in memory until a daemon picks it up and writes the file.
// ---------------------------------------------------------------------------

// SubscriptionPoolPayload is sent with a claimed task. PoolID, Revision and
// OfferID are new: a daemon that confirms the account it took sends them back.
// An older daemon ignores them and picks the account on its own.
type SubscriptionPoolPayload struct {
	ProfileID     string                 `json:"profile_id"`
	PoolID        string                 `json:"pool_id,omitempty"`
	Revision      int32                  `json:"revision,omitempty"`
	OfferID       string                 `json:"offer_id,omitempty"`
	SpendOrder    string                 `json:"spend_order"`
	Subscriptions []SubscriptionResponse `json:"subscriptions"`
}

// subscriptionPoolForTask returns the pool handed to the daemon with a claimed
// run, or nil for an environment that spends none. The accounts and their order
// are the ones stored with the claim as the offer, so the payload and the
// server's record of it are the same; the numbers in them (usage, an account
// switched off) are read now. An account with a provider refusal is sent as
// switched off until the refusal ends: the daemon must not take it.
func (h *Handler) subscriptionPoolForTask(ctx context.Context, rt db.AgentRuntime, task *db.AgentTaskQueue) *SubscriptionPoolPayload {
	if !rt.ProfileID.Valid || task == nil {
		return nil
	}
	offer, ok := service.ParseAccountOffer(task.AccountOffer)
	if !ok {
		return nil
	}
	ids := make([]pgtype.UUID, len(offer.Subscriptions))
	for i, a := range offer.Subscriptions {
		ids[i] = parseUUID(a.SubscriptionID)
	}
	now := time.Now()
	accounts, err := service.LoadAccounts(ctx, h.Queries, ids, now)
	if err != nil {
		slog.Warn("subscription pool payload: load accounts failed", "error", err)
		return nil
	}
	rows, err := h.Queries.ListSubscriptionsByIDs(ctx, ids)
	if err != nil {
		return nil
	}
	byID := map[string]db.Subscription{}
	for _, s := range rows {
		byID[uuidToString(s.ID)] = s
	}
	usage, _ := h.latestUsageBySubscription(ctx)
	out := &SubscriptionPoolPayload{
		ProfileID: uuidToString(rt.ProfileID), PoolID: offer.PoolID, Revision: offer.PoolRevision, OfferID: offer.OfferID,
		SpendOrder: offer.SpendOrder, Subscriptions: []SubscriptionResponse{},
	}
	for _, a := range offer.Subscriptions {
		s, ok := byID[a.SubscriptionID]
		if !ok {
			continue
		}
		resp := subscriptionToResponseAt(s, usage[a.SubscriptionID], nil, now)
		resp.Key, resp.AccountGroup = a.SubscriptionKey, a.AccountGroup
		if until := service.AccountRefusedUntil(accounts[a.SubscriptionID], now); !until.IsZero() {
			if s.DisabledUntil.Valid && s.DisabledUntil.Time.After(until) {
				until = s.DisabledUntil.Time
			}
			at := until.UTC().Format(time.RFC3339)
			resp.Disabled, resp.DisabledUntil = true, &at
		}
		out.Subscriptions = append(out.Subscriptions, resp)
	}
	if len(out.Subscriptions) == 0 {
		return nil
	}
	return out
}

// DaemonListSubscriptions — GET /api/daemon/workspaces/{workspaceId}/subscriptions:
// every account spent by an enabled environment of the workspace, so the
// daemon can probe them. Paths only, never credentials.
func (h *Handler) DaemonListSubscriptions(w http.ResponseWriter, r *http.Request) {
	workspaceID := strings.TrimSpace(chi.URLParam(r, "workspaceId"))
	if !h.requireDaemonWorkspaceAccess(w, r, workspaceID) {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	subs, err := h.workspacePoolSubscriptions(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list subscriptions")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscriptions": subs})
}

func (h *Handler) workspacePoolSubscriptions(ctx context.Context, ws pgtype.UUID) ([]SubscriptionResponse, error) {
	profiles, err := h.Queries.ListEnabledPoolRuntimeProfilesForWorkspace(ctx, ws)
	if err != nil {
		return nil, err
	}
	out := []SubscriptionResponse{}
	if len(profiles) == 0 {
		return out, nil
	}
	poolIDs := make([]pgtype.UUID, len(profiles))
	for i, p := range profiles {
		poolIDs[i] = p.PoolID
	}
	members, err := h.Queries.ListSubscriptionPoolMembersByPools(ctx, poolIDs)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var ids []pgtype.UUID
	for _, m := range members {
		if key := uuidToString(m.SubscriptionID); !seen[key] {
			seen[key] = true
			ids = append(ids, m.SubscriptionID)
		}
	}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := h.Queries.ListSubscriptionsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	usage, _ := h.latestUsageBySubscription(ctx)
	for _, s := range rows {
		out = append(out, subscriptionToResponse(s, usage[uuidToString(s.ID)], nil))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

type daemonUsageReport struct {
	DaemonID   string `json:"daemon_id"`
	DeviceName string `json:"device_name"`
	Reports    []struct {
		Key        string `json:"key"`
		Credential string `json:"credential_state"`
		Error      string `json:"error"`
		Windows    []struct {
			Window       string   `json:"window"`
			UsedPercent  *float64 `json:"used_percent"`
			ResetsAt     *string  `json:"resets_at"`
			LimitReached bool     `json:"limit_reached"`
		} `json:"windows"`
	} `json:"reports"`
}

var credentialStates = map[string]bool{"present": true, "missing": true, "revoked": true, "unreadable": true, "unknown": true}

// DaemonReportSubscriptionUsage — POST /api/daemon/workspaces/{workspaceId}/subscription-usage.
// Accepted only for subscriptions an enabled profile of that workspace spends.
func (h *Handler) DaemonReportSubscriptionUsage(w http.ResponseWriter, r *http.Request) {
	workspaceID := strings.TrimSpace(chi.URLParam(r, "workspaceId"))
	if !h.requireDaemonWorkspaceAccess(w, r, workspaceID) {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	var req daemonUsageReport
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.DaemonID) == "" {
		writeError(w, http.StatusBadRequest, "daemon_id is required")
		return
	}
	allowed, err := h.workspacePoolSubscriptions(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list subscriptions")
		return
	}
	byKey := map[string]SubscriptionResponse{}
	for _, s := range allowed {
		byKey[s.Key] = s
	}
	accepted := []string{}
	changed := []string{}
	for _, rep := range req.Reports {
		sub, ok := byKey[rep.Key]
		if !ok {
			continue
		}
		subUUID := parseUUID(sub.ID)
		state := rep.Credential
		if !credentialStates[state] {
			state = "unknown"
		}
		if _, err := h.Queries.UpsertSubscriptionHostState(r.Context(), db.UpsertSubscriptionHostStateParams{
			SubscriptionID:  subUUID,
			DaemonID:        clip(req.DaemonID, 200),
			DeviceName:      clip(req.DeviceName, 200),
			CredentialState: state,
			Error:           clip(rep.Error, 300),
		}); err != nil {
			slog.Warn("subscription host state upsert failed", "error", err, "key", rep.Key)
		}
		for _, win := range rep.Windows {
			if !subscriptionWindowRe.MatchString(win.Window) || win.UsedPercent == nil {
				continue
			}
			resets, _ := parseOptionalTime(win.ResetsAt)
			if _, err := h.Queries.CreateSubscriptionUsage(r.Context(), db.CreateSubscriptionUsageParams{
				SubscriptionID: subUUID,
				WindowKey:      win.Window,
				UsedPercent:    *win.UsedPercent,
				ResetsAt:       resets,
				LimitReached:   win.LimitReached,
				Source:         "daemon",
			}); err != nil {
				slog.Warn("subscription usage insert failed", "error", err, "key", rep.Key)
				continue
			}
			changed = append(changed, rep.Key)
		}
		accepted = append(accepted, rep.Key)
	}
	if len(changed) > 0 {
		h.afterAccountChange(r.Context())
	}
	writeJSON(w, http.StatusOK, map[string]any{"accepted": accepted})
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// afterAccountChange runs after usage arrives and after an admin changes an
// account or a pool. Availability is computed from the accounts whenever it is
// read, so nothing is stored for it; what remains is lifting the daemon's own
// refusals that a usable account has made stale, so the waiting runs are handed
// out at the next sweep. Returns the lifted refusals.
func (h *Handler) afterAccountChange(ctx context.Context) []db.RuntimeUnavailable {
	lifted, err := service.LiftStaleRefusals(ctx, h.Queries, time.Now())
	if err != nil {
		slog.Warn("subscription availability: lift stale refusals failed", "error", err)
	}
	return lifted
}

// ---------------------------------------------------------------------------
// Sign-in
// ---------------------------------------------------------------------------

// LoginSession is what the web and CLI see of a sign-in.
type LoginSession struct {
	ID        string  `json:"id"`
	State     string  `json:"state"`
	URL       *string `json:"url"`
	Code      *string `json:"code"`
	Message   string  `json:"message"`
	StartedAt string  `json:"started_at"`
}

type pendingLogin struct {
	session   LoginSession
	key       string
	runtimeID string
	// token is the pasted secret; cleared the moment a daemon takes it.
	token     string
	delivered bool
	started   time.Time
}

// SubscriptionLoginStore keeps sign-ins in memory only: a pasted token must
// never reach a database, a cache or a log. A server restart drops pending
// sign-ins; the user starts again.
type SubscriptionLoginStore struct {
	mu        sync.Mutex
	byKey     map[string]*pendingLogin
	byID      map[string]*pendingLogin
	timeout   time.Duration
	retention time.Duration
}

// NewSubscriptionLoginStore returns an empty store.
func NewSubscriptionLoginStore() *SubscriptionLoginStore {
	return &SubscriptionLoginStore{byKey: map[string]*pendingLogin{}, byID: map[string]*pendingLogin{}, timeout: 15 * time.Minute, retention: time.Hour}
}

func (s *SubscriptionLoginStore) expireLocked(now time.Time) {
	for id, l := range s.byID {
		if l.session.State == "waiting" && now.Sub(l.started) > s.timeout {
			l.session.State, l.session.Message, l.token = "expired", "sign-in timed out", ""
		}
		if l.session.State != "waiting" && now.Sub(l.started) > s.retention {
			delete(s.byID, id)
			if s.byKey[l.key] == l {
				delete(s.byKey, l.key)
			}
		}
	}
}

func (s *SubscriptionLoginStore) start(key, runtimeID, token string) LoginSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.expireLocked(now)
	if cur := s.byKey[key]; cur != nil && cur.session.State == "waiting" {
		cur.session.State, cur.session.Message, cur.token = "cancelled", "replaced by a new sign-in", ""
	}
	l := &pendingLogin{key: key, runtimeID: runtimeID, token: token, started: now,
		session: LoginSession{ID: newShareKey()[5:], State: "waiting", Message: "waiting for daemon", StartedAt: now.UTC().Format(time.RFC3339)}}
	s.byKey[key] = l
	s.byID[l.session.ID] = l
	return l.session
}

func (s *SubscriptionLoginStore) current(key string) *LoginSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked(time.Now())
	if l := s.byKey[key]; l != nil {
		out := l.session
		return &out
	}
	return nil
}

func (s *SubscriptionLoginStore) cancel(key string) *LoginSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.byKey[key]
	if l == nil {
		return nil
	}
	if l.session.State == "waiting" {
		l.session.State, l.session.Message, l.token = "cancelled", "sign-in cancelled", ""
	}
	out := l.session
	return &out
}

// popForRuntime hands one undelivered sign-in to the runtime's daemon. The
// token leaves the store with it.
func (s *SubscriptionLoginStore) popForRuntime(runtimeID string) (*pendingLogin, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked(time.Now())
	for _, l := range s.byID {
		if l.runtimeID == runtimeID && l.session.State == "waiting" && !l.delivered {
			l.delivered = true
			token := l.token
			l.token = ""
			l.session.Message = "daemon is signing in"
			cp := *l
			return &cp, token
		}
	}
	return nil, ""
}

func (s *SubscriptionLoginStore) report(id, runtimeID, state string, url, code *string, message string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.byID[id]
	if l == nil || l.runtimeID != runtimeID || l.session.State != "waiting" {
		return false
	}
	switch state {
	case "waiting", "done", "failed":
		l.session.State = state
	default:
		return false
	}
	if url != nil {
		l.session.URL = url
	}
	if code != nil {
		l.session.Code = code
	}
	l.session.Message = clip(message, 300)
	return true
}

// pendingSubscriptionLoginFor attaches a sign-in to a heartbeat ack.
func (h *Handler) pendingSubscriptionLoginFor(ctx context.Context, runtimeID string) *protocol.DaemonHeartbeatPendingSubscriptionLogin {
	if h.SubscriptionLogins == nil {
		return nil
	}
	l, token := h.SubscriptionLogins.popForRuntime(runtimeID)
	if l == nil {
		return nil
	}
	sub, err := h.Queries.GetSubscriptionByKey(ctx, l.key)
	if err != nil {
		h.SubscriptionLogins.report(l.session.ID, runtimeID, "failed", nil, nil, "subscription deleted")
		return nil
	}
	return &protocol.DaemonHeartbeatPendingSubscriptionLogin{
		ID: l.session.ID, Key: sub.Key, Provider: sub.Provider, Auth: sub.Auth,
		CredentialPath: sub.CredentialPath, Token: token,
	}
}

// pickLoginRuntime chooses the daemon that signs in: an online runtime of a
// profile spending the subscription, else an online runtime of the admin's
// workspaces with the subscription's CLI family.
func (h *Handler) pickLoginRuntime(ctx context.Context, sub db.Subscription, userID string) (string, error) {
	pools, err := h.Queries.ListSubscriptionPoolMembersBySubscription(ctx, sub.ID)
	if err != nil {
		return "", err
	}
	poolIDs := make([]pgtype.UUID, len(pools))
	for i, m := range pools {
		poolIDs[i] = m.PoolID
	}
	profiles, err := h.Queries.ListRuntimeProfilesByPools(ctx, poolIDs)
	if err != nil {
		return "", err
	}
	ids := make([]pgtype.UUID, len(profiles))
	for i, p := range profiles {
		ids[i] = p.ID
	}
	family := SubscriptionFamily(sub.Provider)
	if len(ids) > 0 {
		rts, err := h.Queries.ListAgentRuntimesByProfiles(ctx, ids)
		if err == nil {
			for _, rt := range rts {
				if full, err := h.Queries.GetAgentRuntime(ctx, rt.ID); err == nil && full.Status == "online" {
					return uuidToString(rt.ID), nil
				}
			}
		}
	}
	uid, ok := parseUUIDSafe(userID)
	if !ok {
		return "", errors.New("no online daemon")
	}
	workspaces, err := h.Queries.ListWorkspaces(ctx, uid)
	if err != nil {
		return "", err
	}
	for _, ws := range workspaces {
		rts, err := h.Queries.ListAgentRuntimes(ctx, ws.ID)
		if err != nil {
			continue
		}
		for _, rt := range rts {
			if rt.Status == "online" && rt.RuntimeMode == "local" && rt.Provider == family {
				return uuidToString(rt.ID), nil
			}
		}
	}
	return "", errors.New("no online daemon can sign in")
}

// StartSubscriptionLogin — POST /api/subscriptions/{key}/login. Instance
// admin only. claude_oauth and api_key need the token in the body; codex_auth
// starts a device login whose URL and code appear on the session.
func (h *Handler) StartSubscriptionLogin(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireInstanceAdmin(w, r)
	if !ok {
		return
	}
	if h.SubscriptionLogins == nil {
		writeError(w, http.StatusServiceUnavailable, "sign-in is not available on this server")
		return
	}
	sub, err := h.Queries.GetSubscriptionByKey(r.Context(), chi.URLParam(r, "key"))
	if err != nil {
		writeError(w, http.StatusNotFound, "subscription not found")
		return
	}
	var req struct {
		Token     string `json:"token"`
		RuntimeID string `json:"runtime_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	switch sub.Auth {
	case "claude_oauth", "api_key":
		if _, err := subscriptions.ValidateSecret(sub.Auth, req.Token); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	case "codex_auth":
		if req.Token != "" {
			writeError(w, http.StatusBadRequest, "Codex signs in with a device code; no token is accepted")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("auth %s has no sign-in through multica", sub.Auth))
		return
	}
	runtimeID := strings.TrimSpace(req.RuntimeID)
	if runtimeID == "" {
		runtimeID, err = h.pickLoginRuntime(r.Context(), sub, userID)
		if err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
	} else if _, ok := parseUUIDSafe(runtimeID); !ok {
		writeError(w, http.StatusBadRequest, "invalid runtime_id")
		return
	}
	session := h.SubscriptionLogins.start(sub.Key, runtimeID, strings.TrimSpace(req.Token))
	slog.Info("subscription sign-in requested", "key", sub.Key, "auth", sub.Auth, "runtime_id", runtimeID, "login_id", session.ID)
	writeJSON(w, http.StatusAccepted, map[string]any{"login": session})
}

// GetSubscriptionLogin — GET /api/subscriptions/{key}/login.
func (h *Handler) GetSubscriptionLogin(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.subscriptionReaderWorkspaces(w, r); !ok {
		return
	}
	var login *LoginSession
	if h.SubscriptionLogins != nil {
		login = h.SubscriptionLogins.current(chi.URLParam(r, "key"))
	}
	writeJSON(w, http.StatusOK, map[string]any{"login": login})
}

// CancelSubscriptionLogin — DELETE /api/subscriptions/{key}/login.
func (h *Handler) CancelSubscriptionLogin(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireInstanceAdmin(w, r); !ok {
		return
	}
	var login *LoginSession
	if h.SubscriptionLogins != nil {
		login = h.SubscriptionLogins.cancel(chi.URLParam(r, "key"))
	}
	writeJSON(w, http.StatusOK, map[string]any{"login": login})
}

// DaemonReportSubscriptionLogin — POST
// /api/daemon/runtimes/{runtimeId}/subscription-logins/{loginId}/result.
func (h *Handler) DaemonReportSubscriptionLogin(w http.ResponseWriter, r *http.Request) {
	runtimeID := chi.URLParam(r, "runtimeId")
	if _, ok := h.requireDaemonRuntimeAccess(w, r, runtimeID); !ok {
		return
	}
	var req struct {
		State   string  `json:"state"`
		URL     *string `json:"url"`
		Code    *string `json:"code"`
		Message string  `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if h.SubscriptionLogins == nil || !h.SubscriptionLogins.report(chi.URLParam(r, "loginId"), runtimeID, req.State, req.URL, req.Code, req.Message) {
		writeError(w, http.StatusNotFound, "sign-in not found or already finished")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------------------------------------------------------------------------
// Host state and discrepancies
// ---------------------------------------------------------------------------

// SubscriptionHostResponse is one daemon host's view of a credential.
type SubscriptionHostResponse struct {
	DaemonID        string `json:"daemon_id"`
	DeviceName      string `json:"device_name"`
	CredentialState string `json:"credential_state"`
	CheckedAt       string `json:"checked_at"`
	Error           string `json:"error"`
}

func (h *Handler) hostStatesBySubscription(ctx context.Context) map[string][]SubscriptionHostResponse {
	rows, err := h.Queries.ListSubscriptionHostStates(ctx)
	if err != nil {
		return nil
	}
	out := map[string][]SubscriptionHostResponse{}
	for _, r := range rows {
		id := uuidToString(r.SubscriptionID)
		out[id] = append(out[id], SubscriptionHostResponse{
			DaemonID: r.DaemonID, DeviceName: r.DeviceName, CredentialState: r.CredentialState,
			CheckedAt: timestampToString(r.CheckedAt), Error: r.Error,
		})
	}
	return out
}

// loginStateFromHosts folds host reports into the page's login status.
func loginStateFromHosts(hosts []SubscriptionHostResponse) string {
	if len(hosts) == 0 {
		return "unchecked"
	}
	states := map[string]bool{}
	for _, h := range hosts {
		states[h.CredentialState] = true
	}
	switch {
	case states["present"]:
		return "logged_in"
	case states["revoked"]:
		return "revoked"
	case states["missing"]:
		return "never"
	case states["unreadable"]:
		return "check_failed"
	}
	return "unchecked"
}

// Discrepancy is one thing on the Subscriptions page that needs attention.
type Discrepancy struct {
	Kind             string `json:"kind"`
	Message          string `json:"message"`
	SubscriptionKey  string `json:"subscription_key,omitempty"`
	RuntimeProfileID string `json:"runtime_profile_id,omitempty"`
	RuntimeName      string `json:"runtime_name,omitempty"`
	WorkspaceID      string `json:"workspace_id,omitempty"`
	AgentID          string `json:"agent_id,omitempty"`
	AgentName        string `json:"agent_name,omitempty"`
	DeviceName       string `json:"device_name,omitempty"`
}

// ListSubscriptionDiscrepancies — GET /api/subscriptions/discrepancies,
// limited to the caller's workspaces.
func (h *Handler) ListSubscriptionDiscrepancies(w http.ResponseWriter, r *http.Request) {
	visible, ok := h.subscriptionReaderWorkspaces(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	subs, err := h.Queries.ListSubscriptions(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list subscriptions")
		return
	}
	d, err := h.loadPoolDirectory(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list runtimes")
		return
	}
	hosts := h.hostStatesBySubscription(ctx)
	canManage := r.Header.Get("X-Actor-Source") == "" && h.isInstanceAdmin(ctx, requestUserID(r))
	items := []Discrepancy{}

	spent := map[string]bool{}
	byID := map[string]db.Subscription{}
	for _, s := range subs {
		byID[uuidToString(s.ID)] = s
	}
	for _, poolID := range d.order {
		for _, m := range d.members[poolID] {
			spent[uuidToString(m.SubscriptionID)] = true
		}
	}
	for _, s := range subs {
		for _, host := range hosts[uuidToString(s.ID)] {
			if !canManage {
				continue
			}
			if host.CredentialState == "missing" || host.CredentialState == "revoked" || host.CredentialState == "unreadable" {
				items = append(items, Discrepancy{
					Kind: "credential_missing", SubscriptionKey: s.Key, DeviceName: host.DeviceName,
					Message: fmt.Sprintf("%s: credential on %s is %s", s.Key, host.DeviceName, host.CredentialState),
				})
			}
		}
		if !spent[uuidToString(s.ID)] {
			items = append(items, Discrepancy{Kind: "unused_subscription", SubscriptionKey: s.Key,
				Message: fmt.Sprintf("Nobody spends subscription %s", s.Key)})
		}
	}
	// A pool without accounts leaves its environments with nothing to spend.
	for _, poolID := range d.order {
		if len(d.members[poolID]) > 0 {
			continue
		}
		for _, p := range d.envs[poolID] {
			if !visible[uuidToString(p.WorkspaceID)] {
				continue
			}
			items = append(items, Discrepancy{Kind: "runtime_without_subscriptions", RuntimeProfileID: uuidToString(p.ID),
				RuntimeName: p.DisplayName, WorkspaceID: uuidToString(p.WorkspaceID),
				Message: fmt.Sprintf("Runtime %s has no subscriptions", p.DisplayName)})
		}
	}
	// Agents on a runtime that cannot run: profile disabled, or every
	// account of its pool disabled.
	var dead []pgtype.UUID
	deadName := map[string]string{}
	for _, poolID := range d.order {
		for _, p := range d.envs[poolID] {
			if !visible[uuidToString(p.WorkspaceID)] {
				continue
			}
			disabled := !p.Enabled
			if !disabled {
				members := d.members[poolID]
				disabled = len(members) > 0
				for _, m := range members {
					if s, ok := byID[uuidToString(m.SubscriptionID)]; !ok || !s.DisabledUntil.Valid || !s.DisabledUntil.Time.After(time.Now()) {
						disabled = false
					}
				}
			}
			if disabled {
				dead = append(dead, p.ID)
				deadName[uuidToString(p.ID)] = p.DisplayName
			}
		}
	}
	if len(dead) > 0 {
		agents, err := h.Queries.ListAgentsOnRuntimeProfiles(ctx, dead)
		if err == nil {
			actorType, actorID := h.resolveActor(r, requestUserID(r), h.resolveWorkspaceID(r))
			for _, a := range agents {
				agent, err := h.Queries.GetAgent(ctx, a.ID)
				if err != nil || !h.canAccessPrivateAgent(ctx, agent, actorType, actorID, uuidToString(a.WorkspaceID)) {
					continue
				}
				items = append(items, Discrepancy{Kind: "agent_on_disabled_runtime", AgentID: uuidToString(a.ID), AgentName: a.Name,
					WorkspaceID: uuidToString(a.WorkspaceID), RuntimeProfileID: uuidToString(a.ProfileID), RuntimeName: deadName[uuidToString(a.ProfileID)],
					Message: fmt.Sprintf("Agent %s uses disabled runtime %s", a.Name, deadName[uuidToString(a.ProfileID)])})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// ConfirmTaskAccount — POST /api/daemon/tasks/{taskId}/account. The daemon
// says which account of the pool it handed to the CLI. The answer is the fact
// stored for the run, which the same call repeated returns again.
//
// What fits is decided from the offer stored with the claim: the assignment
// (offer_id), the pool and its revision, and the account must all be in it.
// Another daemon or workspace does not see the run at all (404); a stale
// assignment, an account that was not offered, a different account than the
// first confirmed one, or a first confirmation for a finished run is 409.
func (h *Handler) ConfirmTaskAccount(w http.ResponseWriter, r *http.Request) {
	task, ok := h.requireDaemonTaskAccess(w, r, chi.URLParam(r, "taskId"))
	if !ok {
		return
	}
	var req service.AccountConfirmation
	if !decodeStrict(w, r, 4096, &req) {
		return
	}
	offerID, ok := parseUUIDOrBadRequest(w, req.OfferID, "offer_id")
	if !ok {
		return
	}
	accountID, ok := parseUUIDOrBadRequest(w, req.SubscriptionID, "subscription_id")
	if !ok {
		return
	}
	poolID, ok := parseUUIDOrBadRequest(w, req.PoolID, "pool_id")
	if !ok {
		return
	}
	if req.PoolRevision < 1 {
		writeError(w, http.StatusBadRequest, "pool_revision must be positive")
		return
	}
	req.OfferID, req.SubscriptionID, req.PoolID = uuidToString(offerID), uuidToString(accountID), uuidToString(poolID)
	rt, err := h.getAgentRuntime(r.Context(), obsmetrics.RuntimeLookupSourceDaemonAPI, task.RuntimeID)
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	fact, err := h.TaskService.ConfirmTaskAccount(r.Context(), task.ID, task.RuntimeID, rt.DaemonID.String, req, time.Now())
	if err != nil {
		code, status := "", http.StatusConflict
		switch {
		case errors.Is(err, service.ErrAccountNoOffer):
			code = "no_offer"
		case errors.Is(err, service.ErrAccountStaleOffer):
			code = "stale_offer"
		case errors.Is(err, service.ErrAccountNotOffered):
			code = "not_offered"
		case errors.Is(err, service.ErrAccountTaskState):
			code = "task_finished"
		case errors.Is(err, service.ErrAccountConflict):
			code = "account_conflict"
		case errors.Is(err, service.ErrAccountWrongRuntime), errors.Is(err, pgx.ErrNoRows):
			writeError(w, http.StatusNotFound, "task not found")
			return
		default:
			slog.Error("confirm task account failed", "task_id", uuidToString(task.ID), "error", err)
			writeError(w, http.StatusInternalServerError, "failed to confirm the account")
			return
		}
		writeJSON(w, status, map[string]any{"error": err.Error(), "code": code})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account_fact": fact})
}
