package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ---------------------------------------------------------------------------
// Instance subscriptions
//
// A subscription is one provider account (a Claude or Codex plan, an API key)
// shared by the whole instance. Runtime profiles list the subscriptions they
// spend, in order, plus a spend order; the launch wrapper on the daemon host
// (subs-run) reads that list through the profile endpoint below and picks one
// subscription per run.
//
// Access: any member of any workspace may read; only instance admins
// (Config.InstanceAdmins, from MULTICA_INSTANCE_ADMINS) may change anything,
// and only in person — task tokens and cloud-node PATs carry the owner's
// identity but never the owner's decision.
//
// Secrets never pass through here. A subscription stores where the credential
// file lives on the daemon host (credential_path); writes that look like they
// carry a token or key are refused.
// ---------------------------------------------------------------------------

var (
	subscriptionAuths = map[string]bool{
		"claude_login": true,
		"claude_oauth": true,
		"codex_auth":   true,
		"api_key":      true,
	}
	subscriptionKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	subscriptionWindowRe   = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)
	// Values that are credentials, not settings. The list errs on the side of
	// refusing: a refused write is a message, a stored secret is a leak.
	secretValuePrefixes = []string{"sk-", "sk_", "ghp_", "gho_", "github_pat_", "mat_", "mul_", "mcn_", "xox", "eyJ", "AIza"}
	// Name segments that mark a credential: ANTHROPIC_AUTH_TOKEN, api_key,
	// client-secret. Matched per segment so CLAUDE_CODE_MAX_CONTEXT_TOKENS
	// (a window size) is not mistaken for one.
	secretKeySegments = map[string]bool{"token": true, "secret": true, "password": true, "passwd": true, "apikey": true, "credential": true}
	secretKeyPairs    = [][2]string{{"api", "key"}, {"auth", "key"}, {"private", "key"}, {"access", "key"}}
	keySegmentSplit   = regexp.MustCompile(`[^a-z0-9]+`)
)

const subscriptionUsageMaxRows = 2000

// SubscriptionFamily is the CLI family a provider runs on: every provider but
// codex speaks the Anthropic API through Claude Code.
func SubscriptionFamily(provider string) string { return service.SubscriptionFamily(provider) }

type SubscriptionUsageResponse struct {
	Window       string  `json:"window"`
	UsedPercent  float64 `json:"used_percent"`
	ResetsAt     *string `json:"resets_at"`
	LimitReached bool    `json:"limit_reached"`
	Source       string  `json:"source"`
	ObservedAt   string  `json:"observed_at"`
}

type SubscriptionUserResponse struct {
	WorkspaceID      string `json:"workspace_id"`
	RuntimeProfileID string `json:"runtime_profile_id"`
	DisplayName      string `json:"display_name"`
	Position         int    `json:"position"`
}

type SubscriptionResponse struct {
	ID             string                      `json:"id"`
	Key            string                      `json:"key"`
	Provider       string                      `json:"provider"`
	Family         string                      `json:"family"`
	Title          string                      `json:"title"`
	OwnerNote      string                      `json:"owner_note"`
	Auth           string                      `json:"auth"`
	CredentialPath string                      `json:"credential_path"`
	Windows        []string                    `json:"windows"`
	Caps           map[string]int              `json:"caps"`
	Reserve        int                         `json:"reserve"`
	EffectiveCaps  map[string]int              `json:"effective_caps"`
	Settings       map[string]any              `json:"settings"`
	AccountGroup   string                      `json:"account_group"`
	DisabledUntil  *string                     `json:"disabled_until"`
	Disabled       bool                        `json:"disabled"`
	Usage          []SubscriptionUsageResponse `json:"usage"`
	UsedBy         []SubscriptionUserResponse  `json:"used_by"`
	Hosts          []SubscriptionHostResponse  `json:"hosts"`
	LoginState     string                      `json:"login_state"`
	Login          *LoginSession               `json:"login"`
	CreatedAt      string                      `json:"created_at"`
	UpdatedAt      string                      `json:"updated_at"`
}

func decodeStringList(raw []byte) []string {
	out := []string{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	if out == nil {
		out = []string{}
	}
	return out
}

func decodeCaps(raw []byte) map[string]int {
	out := map[string]int{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	if out == nil {
		out = map[string]int{}
	}
	return out
}

// foldReserve applies the reserve an older client sends to the caps: the limit
// agents stop at is min(cap, 100 - reserve), and the reserve itself is not
// stored any more.
func foldReserve(windows []string, caps map[string]int, reserve int) map[string]int {
	out := map[string]int{}
	for k, v := range caps {
		out[k] = v
	}
	if reserve <= 0 {
		return out
	}
	keys := append([]string(nil), windows...)
	for k := range caps {
		keys = append(keys, k)
	}
	for _, k := range keys {
		cur := 100
		if c, ok := caps[k]; ok {
			cur = c
		}
		if 100-reserve < cur {
			out[k] = 100 - reserve
		}
	}
	return out
}

func subscriptionToResponse(s db.Subscription, usage []db.SubscriptionUsage, users []SubscriptionUserResponse) SubscriptionResponse {
	return subscriptionToResponseAt(s, usage, users, time.Now())
}

func subscriptionToResponseAt(s db.Subscription, usage []db.SubscriptionUsage, users []SubscriptionUserResponse, now time.Time) SubscriptionResponse {
	windows := decodeStringList(s.Windows)
	caps := decodeCaps(s.Caps)
	settings := map[string]any{}
	if len(s.Settings) > 0 {
		_ = json.Unmarshal(s.Settings, &settings)
	}
	if settings == nil {
		settings = map[string]any{}
	}
	resp := SubscriptionResponse{
		ID:             uuidToString(s.ID),
		Key:            s.Key,
		Provider:       s.Provider,
		Family:         SubscriptionFamily(s.Provider),
		Title:          s.Title,
		OwnerNote:      s.OwnerNote,
		Auth:           s.Auth,
		CredentialPath: s.CredentialPath,
		Windows:        windows,
		Caps:           caps,
		Reserve:        0,
		EffectiveCaps:  service.EffectiveCaps(windows, caps),
		Settings:       settings,
		AccountGroup:   s.AccountGroup,
		Usage:          []SubscriptionUsageResponse{},
		UsedBy:         users,
		Hosts:          []SubscriptionHostResponse{},
		LoginState:     "unchecked",
		CreatedAt:      timestampToString(s.CreatedAt),
		UpdatedAt:      timestampToString(s.UpdatedAt),
	}
	if resp.UsedBy == nil {
		resp.UsedBy = []SubscriptionUserResponse{}
	}
	if s.DisabledUntil.Valid {
		at := timestampToString(s.DisabledUntil)
		resp.DisabledUntil = &at
		resp.Disabled = s.DisabledUntil.Time.After(now)
	}
	for _, u := range usage {
		resp.Usage = append(resp.Usage, subscriptionUsageToResponse(u))
	}
	return resp
}

// withHostState adds what daemon hosts report about the credential and the
// sign-in in progress.
func (h *Handler) withHostState(resp SubscriptionResponse, hosts map[string][]SubscriptionHostResponse) SubscriptionResponse {
	if list := hosts[resp.ID]; len(list) > 0 {
		resp.Hosts = list
	}
	resp.LoginState = loginStateFromHosts(resp.Hosts)
	if h.SubscriptionLogins != nil {
		resp.Login = h.SubscriptionLogins.current(resp.Key)
	}
	return resp
}

func subscriptionForReader(resp SubscriptionResponse, canManage bool) SubscriptionResponse {
	if !canManage {
		resp.CredentialPath = ""
		resp.Hosts = []SubscriptionHostResponse{}
		resp.Login = nil
	}
	return resp
}

func subscriptionUsageToResponse(u db.SubscriptionUsage) SubscriptionUsageResponse {
	out := SubscriptionUsageResponse{
		Window:       u.WindowKey,
		UsedPercent:  u.UsedPercent,
		LimitReached: u.LimitReached,
		Source:       u.Source,
		ObservedAt:   timestampToString(u.ObservedAt),
	}
	if u.ResetsAt.Valid {
		at := timestampToString(u.ResetsAt)
		out.ResetsAt = &at
	}
	return out
}

// ---------------------------------------------------------------------------
// Access
// ---------------------------------------------------------------------------

// isInstanceAdmin reports whether the user's email is listed in
// MULTICA_INSTANCE_ADMINS. Comparison is case-insensitive.
func (h *Handler) isInstanceAdmin(ctx context.Context, userID string) bool {
	if len(h.cfg.InstanceAdmins) == 0 || userID == "" {
		return false
	}
	uid, ok := parseUUIDSafe(userID)
	if !ok {
		return false
	}
	user, err := h.Queries.GetUser(ctx, uid)
	if err != nil {
		return false
	}
	email := strings.ToLower(strings.TrimSpace(user.Email))
	for _, admin := range h.cfg.InstanceAdmins {
		if email != "" && strings.ToLower(strings.TrimSpace(admin)) == email {
			return true
		}
	}
	return false
}

func parseUUIDSafe(s string) (pgtype.UUID, bool) {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		return pgtype.UUID{}, false
	}
	return u, true
}

// requireInstanceAdmin gates every subscription write. Machine credentials are
// refused even when they resolve to an admin: an agent running on the admin's
// account must not be able to repoint the instance's accounts.
func (h *Handler) requireInstanceAdmin(w http.ResponseWriter, r *http.Request) (string, bool) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return "", false
	}
	if src := r.Header.Get("X-Actor-Source"); src == "task_token" || src == "cloud_pat" {
		writeError(w, http.StatusForbidden, "subscriptions can only be changed by an instance admin in person, not by an agent or machine token")
		return "", false
	}
	if !h.isInstanceAdmin(r.Context(), userID) {
		writeError(w, http.StatusForbidden, "only instance admins (MULTICA_INSTANCE_ADMINS) can change subscriptions")
		return "", false
	}
	return userID, true
}

// subscriptionReaderWorkspaces returns the workspaces whose runtimes the
// caller may see in "used by". A task token sees only its own workspace.
// A caller with no workspace (and not an instance admin) is refused.
func (h *Handler) subscriptionReaderWorkspaces(w http.ResponseWriter, r *http.Request) (map[string]bool, bool) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return nil, false
	}
	visible := map[string]bool{}
	if r.Header.Get("X-Actor-Source") == "task_token" {
		if ws := r.Header.Get("X-Workspace-ID"); ws != "" {
			visible[ws] = true
		}
		return visible, true
	}
	uid, ok := parseUUIDSafe(userID)
	if !ok {
		writeError(w, http.StatusUnauthorized, "user not authenticated")
		return nil, false
	}
	workspaces, err := h.Queries.ListWorkspaces(r.Context(), uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list workspaces")
		return nil, false
	}
	for _, ws := range workspaces {
		visible[uuidToString(ws.ID)] = true
	}
	if len(visible) == 0 && !h.isInstanceAdmin(r.Context(), userID) {
		writeError(w, http.StatusForbidden, "subscriptions are visible to workspace members only")
		return nil, false
	}
	return visible, true
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

func looksLikeSecret(value string) bool {
	v := strings.TrimSpace(value)
	for _, p := range secretValuePrefixes {
		if strings.HasPrefix(v, p) {
			return true
		}
	}
	return false
}

func isSecretKeyName(name string) bool {
	segments := keySegmentSplit.Split(strings.ToLower(name), -1)
	for i, seg := range segments {
		if secretKeySegments[seg] {
			return true
		}
		if i > 0 {
			for _, pair := range secretKeyPairs {
				if segments[i-1] == pair[0] && seg == pair[1] {
					return true
				}
			}
		}
	}
	return false
}

// validateSubscriptionSettings refuses settings that carry a credential. The
// check walks nested objects so {"env": {"ANTHROPIC_AUTH_TOKEN": "..."}} is
// caught as well as a top-level token.
func validateSubscriptionSettings(v any, path string) error {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if isSecretKeyName(k) {
				return fmt.Errorf("settings.%s%s looks like a credential; store the credential in a file on the daemon host and set credential_path", path, k)
			}
			if err := validateSubscriptionSettings(child, path+k+"."); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range t {
			if err := validateSubscriptionSettings(child, path); err != nil {
				return err
			}
		}
	case string:
		if looksLikeSecret(t) {
			return fmt.Errorf("settings.%s holds a credential-like value; store the credential in a file on the daemon host", strings.TrimSuffix(path, "."))
		}
	}
	return nil
}

func validateCredentialPath(p string) error {
	if p == "" {
		return nil
	}
	if len(p) > 512 || strings.ContainsAny(p, "\r\n\t\x00 ") {
		return errors.New("credential_path must be a single file path")
	}
	if !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "~") {
		return errors.New("credential_path must be absolute or start with ~")
	}
	if looksLikeSecret(p) {
		return errors.New("credential_path must be a path, not the credential itself")
	}
	return nil
}

func validateWindowsAndCaps(windows []string, caps map[string]int) error {
	known := make(map[string]bool, len(windows))
	for _, w := range windows {
		if !subscriptionWindowRe.MatchString(w) {
			return fmt.Errorf("invalid window %q", w)
		}
		known[w] = true
	}
	for k, v := range caps {
		if !known[k] {
			return fmt.Errorf("unknown cap window %q", k)
		}
		if v < 0 || v > 100 {
			return fmt.Errorf("cap %s must be between 0 and 100", k)
		}
	}
	return nil
}

// defaultCredentialPath is where the login flow writes a subscription's
// credential on the daemon host, relative to the daemon user's home.
func defaultCredentialPath(key, auth string) string {
	switch auth {
	case "claude_login":
		return "~/.claude/.credentials.json"
	case "claude_oauth":
		return "~/.config/" + key + "/token"
	case "codex_auth":
		return "~/.config/" + key + "/auth.json"
	default:
		return "~/.config/" + key + "/key"
	}
}

var nonKeyChars = regexp.MustCompile(`[^a-z0-9]+`)

// deriveSubscriptionKey builds a key from the title, falling back to
// <provider>-<n> when the title has no latin letters or the key is taken.
func deriveSubscriptionKey(title, provider string, taken map[string]bool) string {
	slug := strings.Trim(nonKeyChars.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if len(slug) > 40 {
		slug = strings.Trim(slug[:40], "-")
	}
	if slug != "" && subscriptionKeyPattern.MatchString(slug) && !taken[slug] {
		return slug
	}
	for n := 1; ; n++ {
		candidate := provider + "-" + strconv.Itoa(n)
		if !taken[candidate] {
			return candidate
		}
	}
}

func parseOptionalTime(s *string) (pgtype.Timestamptz, error) {
	if s == nil || strings.TrimSpace(*s) == "" {
		return pgtype.Timestamptz{}, nil
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(*s))
	if err != nil {
		// Accept minute precision, as typed on a command line: 2026-10-21T12:00Z.
		t, err = time.Parse("2006-01-02T15:04Z07:00", strings.TrimSpace(*s))
		if err != nil {
			return pgtype.Timestamptz{}, errors.New("time must be RFC 3339, e.g. 2026-10-21T12:00:00Z")
		}
	}
	return pgtype.Timestamptz{Time: t, Valid: true}, nil
}

// ---------------------------------------------------------------------------
// Read
// ---------------------------------------------------------------------------

func (h *Handler) latestUsageBySubscription(ctx context.Context) (map[string][]db.SubscriptionUsage, error) {
	rows, err := h.Queries.ListLatestSubscriptionUsage(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string][]db.SubscriptionUsage{}
	for _, row := range rows {
		id := uuidToString(row.SubscriptionID)
		out[id] = append(out[id], row)
	}
	return out, nil
}

// subscriptionUsers maps subscription key -> the environments that spend it
// through their pool, only in the given workspaces.
func (h *Handler) subscriptionUsers(ctx context.Context, visible map[string]bool) (map[string][]SubscriptionUserResponse, error) {
	d, err := h.loadPoolDirectory(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string][]SubscriptionUserResponse{}
	for _, poolID := range d.order {
		for _, env := range d.envs[poolID] {
			ws := uuidToString(env.WorkspaceID)
			if !visible[ws] {
				continue
			}
			for _, m := range d.members[poolID] {
				a, ok := d.accounts[uuidToString(m.SubscriptionID)]
				if !ok {
					continue
				}
				out[a.Key] = append(out[a.Key], SubscriptionUserResponse{
					WorkspaceID: ws, RuntimeProfileID: uuidToString(env.ID), DisplayName: env.DisplayName, Position: int(m.Position),
				})
			}
		}
	}
	return out, nil
}

// ListSubscriptions — GET /api/subscriptions.
func (h *Handler) ListSubscriptions(w http.ResponseWriter, r *http.Request) {
	visible, ok := h.subscriptionReaderWorkspaces(w, r)
	if !ok {
		return
	}
	subs, err := h.Queries.ListSubscriptions(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list subscriptions")
		return
	}
	usage, err := h.latestUsageBySubscription(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load subscription usage")
		return
	}
	users, err := h.subscriptionUsers(r.Context(), visible)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load subscription users")
		return
	}
	hosts := h.hostStatesBySubscription(r.Context())
	canManage := r.Header.Get("X-Actor-Source") == "" && h.isInstanceAdmin(r.Context(), requestUserID(r))
	resp := make([]SubscriptionResponse, len(subs))
	for i, s := range subs {
		resp[i] = subscriptionForReader(h.withHostState(subscriptionToResponse(s, usage[uuidToString(s.ID)], users[s.Key]), hosts), canManage)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"subscriptions": resp,
		"can_manage":    canManage,
	})
}

// GetSubscription — GET /api/subscriptions/{key}.
func (h *Handler) GetSubscription(w http.ResponseWriter, r *http.Request) {
	visible, ok := h.subscriptionReaderWorkspaces(w, r)
	if !ok {
		return
	}
	sub, err := h.Queries.GetSubscriptionByKey(r.Context(), chi.URLParam(r, "key"))
	if err != nil {
		writeError(w, http.StatusNotFound, "subscription not found")
		return
	}
	usage, err := h.latestUsageBySubscription(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load subscription usage")
		return
	}
	users, err := h.subscriptionUsers(r.Context(), visible)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load subscription users")
		return
	}
	canManage := r.Header.Get("X-Actor-Source") == "" && h.isInstanceAdmin(r.Context(), requestUserID(r))
	writeJSON(w, http.StatusOK, subscriptionForReader(h.withHostState(subscriptionToResponse(sub, usage[uuidToString(sub.ID)], users[sub.Key]), h.hostStatesBySubscription(r.Context())), canManage))
}

// ListSubscriptionUsage — GET /api/subscriptions/{key}/usage?since=7d.
func (h *Handler) ListSubscriptionUsage(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.subscriptionReaderWorkspaces(w, r); !ok {
		return
	}
	sub, err := h.Queries.GetSubscriptionByKey(r.Context(), chi.URLParam(r, "key"))
	if err != nil {
		writeError(w, http.StatusNotFound, "subscription not found")
		return
	}
	since, err := parseUsageSince(r.URL.Query().Get("since"), time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rows, err := h.Queries.ListSubscriptionUsageSince(r.Context(), db.ListSubscriptionUsageSinceParams{
		SubscriptionID: sub.ID,
		Since:          pgtype.Timestamptz{Time: since, Valid: true},
		MaxRows:        subscriptionUsageMaxRows,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load subscription usage")
		return
	}
	resp := make([]SubscriptionUsageResponse, len(rows))
	for i, row := range rows {
		resp[i] = subscriptionUsageToResponse(row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": sub.Key, "since": since.UTC().Format(time.RFC3339), "usage": resp})
}

// parseUsageSince accepts a duration like 7d / 12h / 30m or an RFC 3339 time.
// Empty means the last 24 hours.
func parseUsageSince(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return now.Add(-24 * time.Hour), nil
	}
	if strings.HasSuffix(s, "d") {
		if n, err := strconv.Atoi(strings.TrimSuffix(s, "d")); err == nil && n >= 0 {
			return now.Add(-time.Duration(n) * 24 * time.Hour), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil && d >= 0 {
		return now.Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Time{}, errors.New("since must be a duration like 7d or 12h, or an RFC 3339 time")
}

// ---------------------------------------------------------------------------
// Write
// ---------------------------------------------------------------------------

type subscriptionCaps map[string]int

func (caps *subscriptionCaps) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw == nil {
		return errors.New("caps must be an object of integer percentages")
	}
	out := make(subscriptionCaps, len(raw))
	for window, data := range raw {
		var value *int
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		if value == nil {
			return fmt.Errorf("cap %s must be an integer percentage", window)
		}
		out[window] = *value
	}
	*caps = out
	return nil
}

type subscriptionRequest struct {
	Key            *string          `json:"key"`
	Provider       *string          `json:"provider"`
	Title          *string          `json:"title"`
	OwnerNote      *string          `json:"owner_note"`
	Auth           *string          `json:"auth"`
	CredentialPath *string          `json:"credential_path"`
	Windows        *[]string        `json:"windows"`
	Caps           subscriptionCaps `json:"caps"`
	Reserve        *int             `json:"reserve"`
	Settings       *map[string]any  `json:"settings"`
	AccountGroup   *string          `json:"account_group"`
	DisabledUntil  *string          `json:"disabled_until"`
	// Never accepted: listed so a client sending them gets a clear refusal
	// instead of a silent drop.
	Token  *string `json:"token"`
	APIKey *string `json:"api_key"`
}

func (req subscriptionRequest) refuseSecrets() error {
	if req.Token != nil || req.APIKey != nil {
		return errors.New("tokens and keys are not accepted by the API; the login flow writes them to the daemon host")
	}
	return nil
}

// CreateSubscription — POST /api/subscriptions. Instance admin only.
func (h *Handler) CreateSubscription(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireInstanceAdmin(w, r)
	if !ok {
		return
	}
	var req subscriptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := req.refuseSecrets(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	provider := strings.TrimSpace(deref(req.Provider))
	if SubscriptionFamily(provider) == "" {
		writeError(w, http.StatusBadRequest, "provider must be one of claude, codex, zai, minimax, loodsen-ai, openrouter")
		return
	}
	auth := strings.TrimSpace(deref(req.Auth))
	if auth == "" {
		auth = defaultAuthForProvider(provider)
	}
	if !subscriptionAuths[auth] {
		writeError(w, http.StatusBadRequest, "auth must be one of claude_login, claude_oauth, codex_auth, api_key")
		return
	}
	if err := authMatchesProvider(auth, provider); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	title := strings.TrimSpace(deref(req.Title))

	existing, err := h.Queries.ListSubscriptions(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list subscriptions")
		return
	}
	taken := map[string]bool{}
	byKey := map[string]db.Subscription{}
	for _, s := range existing {
		taken[s.Key] = true
		byKey[s.Key] = s
	}
	key := strings.TrimSpace(deref(req.Key))
	explicitKey := key != ""
	if key == "" {
		key = deriveSubscriptionKey(title, provider, taken)
	}
	if !subscriptionKeyPattern.MatchString(key) {
		writeError(w, http.StatusBadRequest, "key must be lowercase latin letters, digits and dashes")
		return
	}
	if taken[key] {
		h.writeExistingSubscription(w, byKey[key], explicitKey, provider, auth)
		return
	}
	if title == "" {
		title = key
	}
	credential := strings.TrimSpace(deref(req.CredentialPath))
	if credential == "" {
		credential = defaultCredentialPath(key, auth)
	}
	if err := validateCredentialPath(credential); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	windows := defaultWindowsForProvider(provider)
	if req.Windows != nil {
		windows = *req.Windows
	}
	caps := map[string]int{}
	if req.Caps != nil {
		caps = req.Caps
	}
	if err := validateWindowsAndCaps(windows, caps); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Reserve != nil && (*req.Reserve < 0 || *req.Reserve > 100) {
		writeError(w, http.StatusBadRequest, "reserve must be between 0 and 100")
		return
	}
	if req.Reserve != nil {
		caps = foldReserve(windows, caps, *req.Reserve)
	}
	settings := map[string]any{}
	if req.Settings != nil {
		settings = *req.Settings
	}
	applyProviderDefaults(provider, settings)
	if err := validateSubscriptionSettings(settings, ""); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	disabledUntil, err := parseOptionalTime(req.DisabledUntil)
	if err != nil {
		writeError(w, http.StatusBadRequest, "disabled_until: "+err.Error())
		return
	}
	windowsJSON, _ := json.Marshal(windows)
	capsJSON, _ := json.Marshal(caps)
	settingsJSON, _ := json.Marshal(settings)

	sub, err := h.Queries.CreateSubscription(r.Context(), db.CreateSubscriptionParams{
		Key:            key,
		Provider:       provider,
		Title:          title,
		OwnerNote:      strings.TrimSpace(deref(req.OwnerNote)),
		Auth:           auth,
		CredentialPath: credential,
		Windows:        windowsJSON,
		Caps:           capsJSON,
		Settings:       settingsJSON,
		AccountGroup:   strings.TrimSpace(deref(req.AccountGroup)),
		DisabledUntil:  disabledUntil,
		CreatedBy:      parseUUID(userID),
	})
	if err != nil {
		if isUniqueViolation(err) {
			// A parallel retry of the same request won the race.
			current, getErr := h.Queries.GetSubscriptionByKey(r.Context(), key)
			if getErr != nil {
				writeError(w, http.StatusConflict, "a subscription with this key already exists")
				return
			}
			h.writeExistingSubscription(w, current, explicitKey, provider, auth)
			return
		}
		slog.Error("CreateSubscription failed", "error", err, "key", key)
		writeError(w, http.StatusInternalServerError, "failed to create subscription")
		return
	}
	writeJSON(w, http.StatusCreated, subscriptionToResponse(sub, nil, nil))
}

// writeExistingSubscription answers a create whose key is taken. A repeat of
// the same request (same key, provider and sign-in kind) after a partial
// success returns the account that is already there; anything else is a
// conflict, so a retry never creates a second account.
func (h *Handler) writeExistingSubscription(w http.ResponseWriter, sub db.Subscription, explicitKey bool, provider, auth string) {
	if explicitKey && sub.Provider == provider && sub.Auth == auth {
		writeJSON(w, http.StatusOK, subscriptionToResponse(sub, nil, nil))
		return
	}
	writeError(w, http.StatusConflict, "a subscription with this key already exists")
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// providerBaseURLs are the Anthropic-compatible endpoints of the API-key
// providers. Models stay the admin's choice: they change faster than code.
var providerBaseURLs = map[string]string{
	"zai":        "https://api.z.ai/api/anthropic",
	"minimax":    "https://api.minimax.io/anthropic",
	"openrouter": "https://openrouter.ai/api",
	"loodsen-ai": "https://ai.loodsen.ru/api",
}

// applyProviderDefaults fills settings.env.ANTHROPIC_BASE_URL for an API-key
// provider when the request did not set it.
func applyProviderDefaults(provider string, settings map[string]any) {
	url, ok := providerBaseURLs[provider]
	if !ok {
		return
	}
	env, _ := settings["env"].(map[string]any)
	if env == nil {
		env = map[string]any{}
	}
	if _, set := env["ANTHROPIC_BASE_URL"]; !set {
		env["ANTHROPIC_BASE_URL"] = url
	}
	settings["env"] = env
}

// defaultWindowsForProvider: plans with 5-hour and weekly windows, or none for
// pay-as-you-go keys.
func defaultWindowsForProvider(provider string) []string {
	switch provider {
	case "openrouter", "loodsen-ai":
		return []string{}
	}
	return []string{"5h", "week"}
}

func defaultAuthForProvider(provider string) string {
	switch provider {
	case "claude":
		return "claude_oauth"
	case "codex":
		return "codex_auth"
	default:
		return "api_key"
	}
}

func authMatchesProvider(auth, provider string) error {
	switch auth {
	case "claude_login", "claude_oauth":
		if provider != "claude" {
			return fmt.Errorf("auth %s is only for provider claude", auth)
		}
	case "codex_auth":
		if provider != "codex" {
			return errors.New("auth codex_auth is only for provider codex")
		}
	case "api_key":
		if provider == "claude" || provider == "codex" {
			return fmt.Errorf("provider %s signs in with its own login, not api_key", provider)
		}
	}
	return nil
}

// UpdateSubscription — PATCH /api/subscriptions/{key}. Instance admin only.
// key and provider are immutable.
func (h *Handler) UpdateSubscription(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireInstanceAdmin(w, r); !ok {
		return
	}
	key := chi.URLParam(r, "key")
	current, err := h.Queries.GetSubscriptionByKey(r.Context(), key)
	if err != nil {
		writeError(w, http.StatusNotFound, "subscription not found")
		return
	}
	var req subscriptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := req.refuseSecrets(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if (req.Key != nil && *req.Key != current.Key) || (req.Provider != nil && *req.Provider != current.Provider) {
		writeError(w, http.StatusBadRequest, "key and provider are immutable; create a new subscription")
		return
	}
	params := db.UpdateSubscriptionParams{Key: key}
	if req.Title != nil {
		params.Title = strToText(strings.TrimSpace(*req.Title))
	}
	if req.OwnerNote != nil {
		params.OwnerNote = strToText(strings.TrimSpace(*req.OwnerNote))
	}
	if req.Auth != nil {
		auth := strings.TrimSpace(*req.Auth)
		if !subscriptionAuths[auth] {
			writeError(w, http.StatusBadRequest, "auth must be one of claude_login, claude_oauth, codex_auth, api_key")
			return
		}
		if err := authMatchesProvider(auth, current.Provider); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		params.Auth = strToText(auth)
	}
	if req.CredentialPath != nil {
		p := strings.TrimSpace(*req.CredentialPath)
		if err := validateCredentialPath(p); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		params.CredentialPath = strToText(p)
	}
	windows := decodeStringList(current.Windows)
	caps := decodeCaps(current.Caps)
	if req.Windows != nil {
		windows = *req.Windows
		params.Windows, _ = json.Marshal(windows)
	}
	if req.Caps != nil {
		// Caps merge per window: --cap 5h=70 must not drop the week cap.
		for k, v := range req.Caps {
			caps[k] = v
		}
		params.Caps, _ = json.Marshal(caps)
	}
	if err := validateWindowsAndCaps(windows, caps); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Reserve != nil {
		if *req.Reserve < 0 || *req.Reserve > 100 {
			writeError(w, http.StatusBadRequest, "reserve must be between 0 and 100")
			return
		}
		if *req.Reserve > 0 {
			caps = foldReserve(windows, caps, *req.Reserve)
			params.Caps, _ = json.Marshal(caps)
		}
	}
	if req.Settings != nil {
		if err := validateSubscriptionSettings(*req.Settings, ""); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		params.Settings, _ = json.Marshal(*req.Settings)
	}
	if req.AccountGroup != nil {
		params.AccountGroup = strToText(strings.TrimSpace(*req.AccountGroup))
	}
	if req.DisabledUntil != nil {
		if strings.TrimSpace(*req.DisabledUntil) == "" {
			params.ClearDisabled = true
		} else {
			at, err := parseOptionalTime(req.DisabledUntil)
			if err != nil {
				writeError(w, http.StatusBadRequest, "disabled_until: "+err.Error())
				return
			}
			params.DisabledUntil = at
		}
	}
	sub, err := h.Queries.UpdateSubscription(r.Context(), params)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "subscription not found")
			return
		}
		slog.Error("UpdateSubscription failed", "error", err, "key", key)
		writeError(w, http.StatusInternalServerError, "failed to update subscription")
		return
	}
	h.afterAccountChange(r.Context())
	writeJSON(w, http.StatusOK, subscriptionToResponse(sub, nil, nil))
}

// DeleteSubscription — DELETE /api/subscriptions/{key}. Instance admin only.
// Removes the key from every runtime profile first, then its usage history,
// then the row, in one transaction.
func (h *Handler) DeleteSubscription(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireInstanceAdmin(w, r)
	if !ok {
		return
	}
	key := chi.URLParam(r, "key")
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to begin transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	sub, err := qtx.GetSubscriptionByKey(r.Context(), key)
	if err != nil {
		writeError(w, http.StatusNotFound, "subscription not found")
		return
	}
	changedPools, err := service.RemoveAccount(r.Context(), qtx, sub.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to detach subscription from pools")
		return
	}
	profiles, err := qtx.ListRuntimeProfilesByPools(r.Context(), changedPools)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to detach subscription from pools")
		return
	}
	if err := qtx.DeleteSubscriptionHostStates(r.Context(), sub.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete subscription host state")
		return
	}
	if err := qtx.DeleteSubscriptionUsage(r.Context(), sub.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete subscription usage")
		return
	}
	if err := qtx.DeleteSubscriptionManualRuns(r.Context(), sub.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete manual run journal")
		return
	}
	if err := qtx.DeleteSubscription(r.Context(), sub.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete subscription")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit transaction")
		return
	}
	h.publishRuntimeProfilesChanged(profiles, userID)
	h.afterAccountChange(r.Context())
	detached := make([]string, len(profiles))
	for i, p := range profiles {
		detached[i] = uuidToString(p.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": key, "detached_runtime_profile_ids": detached})
}

type usageSnapshotRequest struct {
	Window       string   `json:"window"`
	UsedPercent  *float64 `json:"used_percent"`
	ResetsAt     *string  `json:"resets_at"`
	LimitReached bool     `json:"limit_reached"`
	Source       string   `json:"source"`
	ObservedAt   *string  `json:"observed_at"`
}

// RecordSubscriptionUsage — POST /api/subscriptions/{key}/usage, body
// {"snapshots": [...]}. Instance admin only for now; the daemon reports its
// own probes once selection moves into it (stage B).
func (h *Handler) RecordSubscriptionUsage(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireInstanceAdmin(w, r); !ok {
		return
	}
	sub, err := h.Queries.GetSubscriptionByKey(r.Context(), chi.URLParam(r, "key"))
	if err != nil {
		writeError(w, http.StatusNotFound, "subscription not found")
		return
	}
	var req struct {
		Snapshots []usageSnapshotRequest `json:"snapshots"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Snapshots) == 0 {
		writeError(w, http.StatusBadRequest, "body must be {\"snapshots\": [...]}")
		return
	}
	out := []SubscriptionUsageResponse{}
	for _, s := range req.Snapshots {
		if !subscriptionWindowRe.MatchString(s.Window) || s.UsedPercent == nil ||
			math.IsNaN(*s.UsedPercent) || *s.UsedPercent < 0 || *s.UsedPercent > 1000 {
			writeError(w, http.StatusBadRequest, "each snapshot needs a window and used_percent")
			return
		}
		resets, err := parseOptionalTime(s.ResetsAt)
		if err != nil {
			writeError(w, http.StatusBadRequest, "resets_at: "+err.Error())
			return
		}
		observed, err := parseOptionalTime(s.ObservedAt)
		if err != nil {
			writeError(w, http.StatusBadRequest, "observed_at: "+err.Error())
			return
		}
		params := db.CreateSubscriptionUsageParams{
			SubscriptionID: sub.ID,
			WindowKey:      s.Window,
			UsedPercent:    *s.UsedPercent,
			ResetsAt:       resets,
			LimitReached:   s.LimitReached,
			Source:         strings.TrimSpace(s.Source),
		}
		if observed.Valid {
			params.ObservedAt = observed
		}
		row, err := h.Queries.CreateSubscriptionUsage(r.Context(), params)
		if err != nil {
			slog.Error("CreateSubscriptionUsage failed", "error", err, "key", sub.Key)
			writeError(w, http.StatusInternalServerError, "failed to record usage")
			return
		}
		out = append(out, subscriptionUsageToResponse(row))
	}
	h.afterAccountChange(r.Context())
	writeJSON(w, http.StatusCreated, map[string]any{"key": sub.Key, "usage": out})
}

// ---------------------------------------------------------------------------
// Environments of a pool (the older, runtime-profile shaped API)
// ---------------------------------------------------------------------------

func (h *Handler) publishRuntimeProfilesChanged(profiles []db.RuntimeProfile, userID string) {
	for _, p := range profiles {
		ws := uuidToString(p.WorkspaceID)
		id := uuidToString(p.ID)
		h.requestDaemonRuntimeProfileRefresh(ws, id)
		h.publish(protocol.EventDaemonRegister, ws, "member", userID, map[string]any{"runtime_profile_id": id})
	}
}

type poolSubscriptionsResponse struct {
	RuntimeProfile RuntimeProfileResponse `json:"runtime_profile"`
	SpendOrder     string                 `json:"spend_order"`
	Subscriptions  []SubscriptionResponse `json:"subscriptions"`
	// Keys listed on the profile with no subscription row (deleted out of
	// band); they are skipped.
	Missing []string `json:"missing"`
	// Agents bound to this runtime's instances.
	Agents []PoolAgentResponse `json:"agents"`
	PoolID *string             `json:"pool_id"`
}

// PoolAgentResponse is an agent running on a runtime of the profile.
type PoolAgentResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// GetRuntimeProfileSubscriptions — GET
// /api/workspaces/{id}/runtime-profiles/{profileId}/subscriptions. Member
// visible: it answers for the pool the environment spends. The response
// carries credential paths, never credentials.
func (h *Handler) GetRuntimeProfileSubscriptions(w http.ResponseWriter, r *http.Request) {
	wsID := strings.TrimSpace(chi.URLParam(r, "id"))
	if _, ok := h.requireWorkspaceMember(w, r, wsID, "workspace not found"); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, wsID, "workspace id")
	if !ok {
		return
	}
	profileUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "profileId"), "profile id")
	if !ok {
		return
	}
	profile, err := h.Queries.GetRuntimeProfileForWorkspace(r.Context(), db.GetRuntimeProfileForWorkspaceParams{ID: profileUUID, WorkspaceID: wsUUID})
	if err != nil {
		writeError(w, http.StatusNotFound, "runtime profile not found")
		return
	}
	writeJSON(w, http.StatusOK, h.poolResponse(r.Context(), profile))
}

// poolResponse is the accounts of the pool an environment spends, in order.
func (h *Handler) poolResponse(ctx context.Context, profile db.RuntimeProfile) poolSubscriptionsResponse {
	resp := poolSubscriptionsResponse{
		RuntimeProfile: h.runtimeProfileResponse(ctx, profile),
		SpendOrder:     "first",
		Subscriptions:  []SubscriptionResponse{},
		Missing:        []string{},
		Agents:         []PoolAgentResponse{},
		PoolID:         uuidToPtr(profile.PoolID),
	}
	if agents, err := h.Queries.ListAgentsOnRuntimeProfiles(ctx, []pgtype.UUID{profile.ID}); err == nil {
		for _, a := range agents {
			resp.Agents = append(resp.Agents, PoolAgentResponse{ID: uuidToString(a.ID), Name: a.Name})
		}
	}
	if !profile.PoolID.Valid {
		return resp
	}
	pool, err := h.Queries.GetSubscriptionPool(ctx, profile.PoolID)
	if err != nil {
		return resp
	}
	resp.SpendOrder = pool.SpendOrder
	members, err := h.Queries.ListSubscriptionPoolMembersByPools(ctx, []pgtype.UUID{pool.ID})
	if err != nil {
		slog.Error("ListSubscriptionPoolMembersByPools failed", "error", err)
		return resp
	}
	ids := make([]pgtype.UUID, len(members))
	for i, m := range members {
		ids[i] = m.SubscriptionID
	}
	subs, err := h.Queries.ListSubscriptionsByIDs(ctx, ids)
	if err != nil {
		slog.Error("ListSubscriptionsByIDs failed", "error", err)
		return resp
	}
	usage, err := h.latestUsageBySubscription(ctx)
	if err != nil {
		usage = nil
	}
	byID := map[string]db.Subscription{}
	for _, s := range subs {
		byID[uuidToString(s.ID)] = s
	}
	for _, m := range members {
		s, ok := byID[uuidToString(m.SubscriptionID)]
		if !ok {
			resp.Missing = append(resp.Missing, uuidToString(m.SubscriptionID))
			continue
		}
		resp.Subscriptions = append(resp.Subscriptions, subscriptionToResponse(s, usage[uuidToString(s.ID)], nil))
	}
	return resp
}

type setPoolRequest struct {
	Subscriptions *[]string `json:"subscriptions"`
	SpendOrder    *string   `json:"spend_order"`
	// Revision is the pool revision the client read; when sent, a pool that
	// has moved on is refused with 409.
	Revision *int32 `json:"revision"`
}

// SetRuntimeProfileSubscriptions — PUT
// /api/workspaces/{id}/runtime-profiles/{profileId}/subscriptions. Instance
// admin only. It edits the pool the environment spends, so it reaches every
// workspace the pool is granted to.
func (h *Handler) SetRuntimeProfileSubscriptions(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireInstanceAdmin(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(chi.URLParam(r, "id")), "workspace id")
	if !ok {
		return
	}
	profileUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "profileId"), "profile id")
	if !ok {
		return
	}
	profile, err := h.Queries.GetRuntimeProfileForWorkspace(r.Context(), db.GetRuntimeProfileForWorkspaceParams{ID: profileUUID, WorkspaceID: wsUUID})
	if err != nil {
		writeError(w, http.StatusNotFound, "runtime profile not found")
		return
	}
	if !profile.PoolID.Valid {
		writeError(w, http.StatusBadRequest, "this runtime does not spend a subscription pool; create a pool and grant it to the workspace")
		return
	}
	var req setPoolRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	res, err := h.pools().Update(r.Context(), profile.PoolID, parseUUID(userID), req.Revision, service.PoolPatch{Subscriptions: req.Subscriptions, SpendOrder: req.SpendOrder})
	if err != nil {
		writePoolError(w, err)
		return
	}
	h.publishRuntimeProfilesChanged(append(res.Profiles, profile), userID)
	h.afterAccountChange(r.Context())
	writeJSON(w, http.StatusOK, h.poolResponse(r.Context(), profile))
}

type createPoolRequest struct {
	DisplayName   string   `json:"display_name"`
	RuntimeType   string   `json:"runtime_type"`
	Description   *string  `json:"description"`
	Subscriptions []string `json:"subscriptions"`
	SpendOrder    string   `json:"spend_order"`
	WorkspaceIDs  []string `json:"workspace_ids"`
	AllWorkspaces bool     `json:"all_workspaces"`
}

func newShareKey() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "pool-" + hex.EncodeToString(b)
}

// CreateRuntimePool — POST /api/runtime-pools. Instance admin only. The older
// way to make a pool: it creates the pool, grants it to the workspaces and
// makes the environment in each, and answers in the shape older clients read.
func (h *Handler) CreateRuntimePool(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireInstanceAdmin(w, r)
	if !ok {
		return
	}
	var req createPoolRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.DisplayName) == "" {
		writeError(w, http.StatusBadRequest, "display_name is required")
		return
	}
	if !req.AllWorkspaces && len(req.WorkspaceIDs) == 0 {
		writeError(w, http.StatusBadRequest, "workspace_ids or all_workspaces is required")
		return
	}
	workspaces, ok := parseWorkspaceIDs(w, req.WorkspaceIDs)
	if !ok {
		return
	}
	res, err := h.pools().Create(r.Context(), parseUUID(userID), service.PoolInput{
		Name: req.DisplayName, RuntimeType: req.RuntimeType, Description: deref(req.Description), SpendOrder: req.SpendOrder,
		Subscriptions: req.Subscriptions, Workspaces: workspaces, AllWorkspaces: req.AllWorkspaces,
	})
	if err != nil {
		writePoolError(w, err)
		return
	}
	h.publishRuntimeProfilesChanged(res.Profiles, userID)
	profiles := h.runtimeProfileResponses(r.Context(), res.Profiles)
	skipped := make([]string, len(res.Skipped))
	for i, ws := range res.Skipped {
		skipped[i] = uuidToString(ws)
	}
	shareKey := ""
	if k := legacyShareKey(res.Pool, len(res.Grants)); k != nil {
		shareKey = *k
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"pool_id":                      uuidToString(res.Pool.ID),
		"share_key":                    shareKey,
		"runtime_profiles":             profiles,
		"skipped_workspace_ids":        skipped,
		"skipped_workspace_ids_reason": "a runtime profile with this display_name already exists",
	})
}

// copyAllWorkspaceRuntimeProfiles gives a new workspace the environment of
// every pool granted to all workspaces. Runs inside CreateWorkspace's
// transaction; a name already taken is skipped.
func copyAllWorkspaceRuntimeProfiles(ctx context.Context, q *db.Queries, workspaceID, actor pgtype.UUID) error {
	return service.EnsureAllWorkspacePools(ctx, q, workspaceID, actor)
}
