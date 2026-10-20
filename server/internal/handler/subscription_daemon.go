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
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/daemon/subscriptions"
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

// SubscriptionPoolPayload is sent with a claimed task.
type SubscriptionPoolPayload struct {
	ProfileID     string                 `json:"profile_id"`
	SpendOrder    string                 `json:"spend_order"`
	Subscriptions []SubscriptionResponse `json:"subscriptions"`
}

// subscriptionPoolForRuntime returns the pool of a profile runtime, or nil for
// built-in runtimes and profiles without subscriptions. Errors degrade to nil:
// the daemon then launches the profile's command on the host's own login.
func (h *Handler) subscriptionPoolForRuntime(ctx context.Context, rt db.AgentRuntime) *SubscriptionPoolPayload {
	if !rt.ProfileID.Valid {
		return nil
	}
	profile, err := h.Queries.GetRuntimeProfile(ctx, rt.ProfileID)
	if err != nil || len(decodeStringList(profile.Subscriptions)) == 0 {
		return nil
	}
	pool := h.poolResponse(ctx, profile)
	if len(pool.Subscriptions) == 0 {
		return nil
	}
	return &SubscriptionPoolPayload{ProfileID: uuidToString(profile.ID), SpendOrder: pool.SpendOrder, Subscriptions: pool.Subscriptions}
}

// DaemonListSubscriptions — GET /api/daemon/workspaces/{workspaceId}/subscriptions:
// every subscription spent by an enabled profile of the workspace, so the
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
	profiles, err := h.Queries.ListEnabledRuntimeProfilesWithSubscriptionsForWorkspace(ctx, ws)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var keys []string
	for _, p := range profiles {
		for _, k := range decodeStringList(p.Subscriptions) {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	out := []SubscriptionResponse{}
	if len(keys) == 0 {
		return out, nil
	}
	rows, err := h.Queries.ListSubscriptionsByKeys(ctx, keys)
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
		h.refreshSubscriptionAvailability(r.Context())
	}
	writeJSON(w, http.StatusOK, map[string]any{"accepted": accepted})
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// subscriptionUnavailableReason prefixes the runtime_unavailable rows this
// file owns, so lifting them never touches a row a failed run wrote.
const subscriptionUnavailableReason = "subscriptions: "

// subscriptionUsageMaxAge: older snapshots are not trusted to park a runtime.
const subscriptionUsageMaxAge = time.Hour

// refreshSubscriptionAvailability parks every profile runtime whose
// subscriptions are all at their agent caps (or disabled) until the earliest
// reset, and lifts the park once one of them is usable again. Runs after usage
// arrives and after an admin changes caps or disables a subscription.
func (h *Handler) refreshSubscriptionAvailability(ctx context.Context) {
	profiles, err := h.Queries.ListRuntimeProfilesWithSubscriptions(ctx)
	if err != nil {
		slog.Warn("subscription availability: list profiles failed", "error", err)
		return
	}
	if len(profiles) == 0 {
		return
	}
	subs, err := h.Queries.ListSubscriptions(ctx)
	if err != nil {
		return
	}
	usage, err := h.latestUsageBySubscription(ctx)
	if err != nil {
		return
	}
	byKey := map[string]db.Subscription{}
	for _, s := range subs {
		byKey[s.Key] = s
	}
	ids := make([]pgtype.UUID, len(profiles))
	for i, p := range profiles {
		ids[i] = p.ID
	}
	runtimes, err := h.Queries.ListAgentRuntimesByProfiles(ctx, ids)
	if err != nil {
		return
	}
	now := time.Now()
	verdict := map[string]struct {
		text  string
		until time.Time
	}{}
	for _, p := range profiles {
		text, until := poolVerdict(p, byKey, usage, now)
		verdict[uuidToString(p.ID)] = struct {
			text  string
			until time.Time
		}{text, until}
	}
	for _, rt := range runtimes {
		v := verdict[uuidToString(rt.ProfileID)]
		if err := h.Queries.DeleteRuntimeUnavailableByReasonPrefix(ctx, db.DeleteRuntimeUnavailableByReasonPrefixParams{
			RuntimeID: rt.ID, Prefix: subscriptionUnavailableReason,
		}); err != nil {
			slog.Warn("subscription availability: lift failed", "error", err, "runtime_id", uuidToString(rt.ID))
			continue
		}
		if v.text == "" {
			continue
		}
		if _, err := h.Queries.InsertRuntimeUnavailable(ctx, db.InsertRuntimeUnavailableParams{
			WorkspaceID: rt.WorkspaceID, RuntimeID: rt.ID,
			Until:  pgtype.Timestamptz{Time: v.until, Valid: true},
			Reason: clip(subscriptionUnavailableReason+v.text, 300),
		}); err != nil {
			slog.Warn("subscription availability: park failed", "error", err, "runtime_id", uuidToString(rt.ID))
		}
	}
}

// poolVerdict applies the daemon's own rule (subscriptions.Blocked) to the
// server's fresh snapshots. It returns "" while any subscription may be used.
func poolVerdict(p db.RuntimeProfile, byKey map[string]db.Subscription, usage map[string][]db.SubscriptionUsage, now time.Time) (string, time.Time) {
	var pool []subscriptions.Subscription
	for _, k := range decodeStringList(p.Subscriptions) {
		s, ok := byKey[k]
		if !ok {
			continue
		}
		resp := subscriptionToResponse(s, usage[uuidToString(s.ID)], nil)
		pool = append(pool, toDaemonSubscription(resp))
	}
	if len(pool) == 0 {
		return "", time.Time{}
	}
	active := subscriptions.Active(pool)
	if len(active) == 0 {
		earliest := time.Time{}
		for _, s := range pool {
			if s.DisabledUntil != nil {
				if t, err := time.Parse(time.RFC3339, *s.DisabledUntil); err == nil && (earliest.IsZero() || t.Before(earliest)) {
					earliest = t
				}
			}
		}
		if earliest.IsZero() {
			earliest = now.Add(time.Hour)
		}
		return subscriptions.DisabledText(pool), earliest
	}
	marks := make([]subscriptions.Mark, 0, len(active))
	for _, s := range active {
		st := subscriptions.StateFromUsage(s.Usage)
		if now.Sub(st.ObservedAt) > subscriptionUsageMaxAge {
			st = subscriptions.State{}
		}
		st.Credential = subscriptions.CredentialPresent
		marks = append(marks, subscriptions.Mark{Sub: s, State: st})
	}
	text := subscriptions.Blocked(marks)
	if text == "" {
		return "", time.Time{}
	}
	// Earliest moment any subscription frees up; unknown resets wait an hour.
	until := time.Time{}
	for _, m := range marks {
		latest := time.Time{}
		for _, win := range m.State.Windows {
			cap, capped := m.Sub.EffectiveCaps[win.Key]
			if (win.Limit || (capped && win.Used >= float64(cap))) && win.ResetAt != nil && win.ResetAt.After(latest) {
				latest = *win.ResetAt
			}
		}
		if latest.IsZero() {
			latest = now.Add(time.Hour)
		}
		if until.IsZero() || latest.Before(until) {
			until = latest
		}
	}
	return text, until
}

func toDaemonSubscription(s SubscriptionResponse) subscriptions.Subscription {
	out := subscriptions.Subscription{
		Key: s.Key, Provider: s.Provider, Family: s.Family, Auth: s.Auth, CredentialPath: s.CredentialPath,
		Windows: s.Windows, EffectiveCaps: s.EffectiveCaps, AccountGroup: s.AccountGroup,
		Disabled: s.Disabled, DisabledUntil: s.DisabledUntil,
	}
	for _, u := range s.Usage {
		out.Usage = append(out.Usage, subscriptions.Usage{Window: u.Window, UsedPercent: u.UsedPercent, ResetsAt: u.ResetsAt, LimitReached: u.LimitReached, ObservedAt: u.ObservedAt})
	}
	return out
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
			l.session.State, l.session.Message, l.token = "expired", "вход не завершён за отведённое время", ""
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
		cur.session.State, cur.session.Message, cur.token = "cancelled", "заменён новым входом", ""
	}
	l := &pendingLogin{key: key, runtimeID: runtimeID, token: token, started: now,
		session: LoginSession{ID: newShareKey()[5:], State: "waiting", Message: "ждём демон", StartedAt: now.UTC().Format(time.RFC3339)}}
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
		l.session.State, l.session.Message, l.token = "cancelled", "вход отменён", ""
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
			l.session.Message = "демон выполняет вход"
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
		h.SubscriptionLogins.report(l.session.ID, runtimeID, "failed", nil, nil, "подписка удалена")
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
	profiles, err := h.Queries.ListRuntimeProfilesUsingSubscription(ctx, sub.Key)
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
	return "", errors.New("нет демона онлайн, который может выполнить вход")
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
	profiles, err := h.Queries.ListRuntimeProfilesWithSubscriptions(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list runtimes")
		return
	}
	all, err := h.Queries.ListSharedRuntimeProfiles(ctx)
	if err != nil {
		all = nil
	}
	hosts := h.hostStatesBySubscription(ctx)
	items := []Discrepancy{}

	spent := map[string]bool{}
	byKey := map[string]db.Subscription{}
	for _, s := range subs {
		byKey[s.Key] = s
	}
	for _, p := range profiles {
		for _, k := range decodeStringList(p.Subscriptions) {
			spent[k] = true
		}
	}
	for _, s := range subs {
		for _, host := range hosts[uuidToString(s.ID)] {
			if host.CredentialState == "missing" || host.CredentialState == "revoked" || host.CredentialState == "unreadable" {
				items = append(items, Discrepancy{
					Kind: "credential_missing", SubscriptionKey: s.Key, DeviceName: host.DeviceName,
					Message: fmt.Sprintf("%s: учётка на %s — %s (%s)", s.Key, host.DeviceName, host.CredentialState, s.CredentialPath),
				})
			}
		}
		if !spent[s.Key] {
			items = append(items, Discrepancy{Kind: "unused_subscription", SubscriptionKey: s.Key,
				Message: fmt.Sprintf("%s никто не тратит", s.Key)})
		}
	}
	// A pool without subscriptions: a shared runtime (share_key) left empty.
	seenPool := map[string]bool{}
	for _, p := range append(all, profiles...) {
		if !p.ShareKey.Valid || !visible[uuidToString(p.WorkspaceID)] || seenPool[uuidToString(p.ID)] {
			continue
		}
		seenPool[uuidToString(p.ID)] = true
		if len(decodeStringList(p.Subscriptions)) == 0 {
			items = append(items, Discrepancy{Kind: "runtime_without_subscriptions", RuntimeProfileID: uuidToString(p.ID),
				RuntimeName: p.DisplayName, WorkspaceID: uuidToString(p.WorkspaceID),
				Message: fmt.Sprintf("среда «%s» без подписок", p.DisplayName)})
		}
	}
	// Agents on a runtime that cannot run: profile disabled, or every
	// subscription disabled.
	var dead []pgtype.UUID
	deadName := map[string]string{}
	for _, p := range profiles {
		if !visible[uuidToString(p.WorkspaceID)] {
			continue
		}
		disabled := !p.Enabled
		if !disabled {
			keys := decodeStringList(p.Subscriptions)
			disabled = len(keys) > 0
			for _, k := range keys {
				if s, ok := byKey[k]; !ok || !s.DisabledUntil.Valid || !s.DisabledUntil.Time.After(time.Now()) {
					disabled = false
				}
			}
		}
		if disabled {
			dead = append(dead, p.ID)
			deadName[uuidToString(p.ID)] = p.DisplayName
		}
	}
	if len(dead) > 0 {
		agents, err := h.Queries.ListAgentsOnRuntimeProfiles(ctx, dead)
		if err == nil {
			for _, a := range agents {
				items = append(items, Discrepancy{Kind: "agent_on_disabled_runtime", AgentID: uuidToString(a.ID), AgentName: a.Name,
					WorkspaceID: uuidToString(a.WorkspaceID), RuntimeProfileID: uuidToString(a.ProfileID), RuntimeName: deadName[uuidToString(a.ProfileID)],
					Message: fmt.Sprintf("агент «%s» на выключенной среде «%s»", a.Name, deadName[uuidToString(a.ProfileID)])})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
