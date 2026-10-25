package handler

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
)

// Pools: the ordered accounts an environment spends, the workspaces they are
// granted to, and the environment each of those workspaces keeps. Read by any
// workspace member (an outsider's workspaces stay out of the answer), written
// by instance admins in person.

func (h *Handler) pools() *service.PoolService {
	return &service.PoolService{Queries: h.Queries, TxStarter: h.TxStarter}
}

func writePoolError(w http.ResponseWriter, err error) {
	var rev *service.PoolRevisionError
	switch {
	case errors.As(err, &rev):
		writeJSON(w, http.StatusConflict, map[string]any{"error": rev.Error(), "code": "pool_revision_conflict", "current_revision": rev.Current})
	case errors.Is(err, service.ErrPoolInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, service.ErrPoolNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, service.ErrPoolInUse), errors.Is(err, service.ErrPoolNameTaken):
		writeError(w, http.StatusConflict, err.Error())
	default:
		slog.Error("pool request failed", "error", err)
		writeError(w, http.StatusInternalServerError, "pool request failed")
	}
}

// PoolAccountResponse is an account in a pool with whether it can run now.
type PoolAccountResponse struct {
	ID            string  `json:"id"`
	Key           string  `json:"key"`
	Title         string  `json:"title"`
	Provider      string  `json:"provider"`
	AccountGroup  string  `json:"account_group"`
	Position      int     `json:"position"`
	Blocked       bool    `json:"blocked"`
	Usable        bool    `json:"usable"`
	ReasonCode    string  `json:"reason_code,omitempty"`
	Until         *string `json:"until"`
	UntilEstimate bool    `json:"until_estimated,omitempty"`
}

// PoolAvailabilityResponse is the one verdict on a pool.
type PoolAvailabilityResponse struct {
	Blocked        bool    `json:"blocked"`
	Usable         bool    `json:"usable"`
	ReasonCode     string  `json:"reason_code,omitempty"`
	Until          *string `json:"until"`
	UntilEstimated bool    `json:"until_estimated,omitempty"`
}

// PoolEnvironmentResponse is the environment a workspace keeps for a pool.
type PoolEnvironmentResponse struct {
	RuntimeProfileID string `json:"runtime_profile_id"`
	WorkspaceID      string `json:"workspace_id"`
	DisplayName      string `json:"display_name"`
	Enabled          bool   `json:"enabled"`
}

// PoolResponse is a pool as a reader may see it.
type PoolResponse struct {
	ID            string                    `json:"id"`
	Name          string                    `json:"name"`
	RuntimeType   string                    `json:"runtime_type"`
	Description   string                    `json:"description"`
	SpendOrder    string                    `json:"spend_order"`
	AllWorkspaces bool                      `json:"all_workspaces"`
	Revision      int32                     `json:"revision"`
	Subscriptions []PoolAccountResponse     `json:"subscriptions"`
	WorkspaceIDs  []string                  `json:"workspace_ids"`
	Environments  []PoolEnvironmentResponse `json:"environments"`
	Availability  PoolAvailabilityResponse  `json:"availability"`
	CreatedAt     string                    `json:"created_at"`
	UpdatedAt     string                    `json:"updated_at"`
}

func verdictUntil(until time.Time, blocked bool) *string {
	if !blocked {
		return nil
	}
	s := until.UTC().Format(time.RFC3339)
	return &s
}

// poolResponses describes the pools for a reader. visible is the set of
// workspaces the reader belongs to; an instance admin (admin) sees them all.
func (h *Handler) poolResponses(r *http.Request, d *poolDirectory, ids []string, visible map[string]bool, admin bool) ([]PoolResponse, error) {
	members := map[string][]pgtype.UUID{}
	for _, id := range ids {
		members[id] = d.accountIDs(id)
	}
	verdicts, err := service.PoolVerdicts(r.Context(), h.Queries, members, time.Now())
	if err != nil {
		return nil, err
	}
	out := make([]PoolResponse, 0, len(ids))
	for _, id := range ids {
		pool := d.pools[id]
		v := verdicts[id]
		resp := PoolResponse{
			ID: id, Name: pool.Name, RuntimeType: pool.RuntimeType, Description: pool.Description, SpendOrder: pool.SpendOrder,
			AllWorkspaces: pool.AllWorkspaces, Revision: pool.Revision, Subscriptions: []PoolAccountResponse{},
			WorkspaceIDs: []string{}, Environments: []PoolEnvironmentResponse{},
			Availability: PoolAvailabilityResponse{Blocked: v.Blocked, Usable: v.Usable, ReasonCode: v.Code, Until: verdictUntil(v.Until, v.Blocked), UntilEstimated: v.Blocked && !v.UntilKnown},
			CreatedAt:    timestampToString(pool.CreatedAt), UpdatedAt: timestampToString(pool.UpdatedAt),
		}
		byAccount := map[string]service.AccountVerdict{}
		for _, av := range v.Accounts {
			byAccount[av.ID] = av
		}
		for _, m := range d.members[id] {
			a, ok := d.accounts[uuidToString(m.SubscriptionID)]
			if !ok {
				continue
			}
			av := byAccount[uuidToString(a.ID)]
			resp.Subscriptions = append(resp.Subscriptions, PoolAccountResponse{
				ID: uuidToString(a.ID), Key: a.Key, Title: a.Title, Provider: a.Provider, AccountGroup: a.AccountGroup, Position: int(m.Position),
				Blocked: av.Blocked, Usable: av.Usable, ReasonCode: av.Code, Until: verdictUntil(av.Until, av.Blocked), UntilEstimate: av.Blocked && !av.UntilKnown,
			})
		}
		for _, g := range d.grants[id] {
			if ws := uuidToString(g.WorkspaceID); admin || visible[ws] {
				resp.WorkspaceIDs = append(resp.WorkspaceIDs, ws)
			}
		}
		for _, e := range d.envs[id] {
			if ws := uuidToString(e.WorkspaceID); admin || visible[ws] {
				resp.Environments = append(resp.Environments, PoolEnvironmentResponse{
					RuntimeProfileID: uuidToString(e.ID), WorkspaceID: ws, DisplayName: e.DisplayName, Enabled: e.Enabled,
				})
			}
		}
		out = append(out, resp)
	}
	return out, nil
}

// reaches reports whether the pool is meant for a workspace the reader is in.
func (d *poolDirectory) reaches(poolID string, visible map[string]bool) bool {
	if d.pools[poolID].AllWorkspaces {
		return true
	}
	for _, g := range d.grants[poolID] {
		if visible[uuidToString(g.WorkspaceID)] {
			return true
		}
	}
	return false
}

// ListPools — GET /api/pools. The pools that reach the reader's workspaces; an
// instance admin sees every pool and every grant.
func (h *Handler) ListPools(w http.ResponseWriter, r *http.Request) {
	visible, ok := h.subscriptionReaderWorkspaces(w, r)
	if !ok {
		return
	}
	admin := r.Header.Get("X-Actor-Source") == "" && h.isInstanceAdmin(r.Context(), requestUserID(r))
	d, err := h.loadPoolDirectory(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list pools")
		return
	}
	var ids []string
	for _, id := range d.order {
		if admin || d.reaches(id, visible) {
			ids = append(ids, id)
		}
	}
	resp, err := h.poolResponses(r, d, ids, visible, admin)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load pool availability")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pools": resp, "can_manage": admin})
}

// GetPool — GET /api/pools/{id}.
func (h *Handler) GetPool(w http.ResponseWriter, r *http.Request) {
	visible, ok := h.subscriptionReaderWorkspaces(w, r)
	if !ok {
		return
	}
	admin := r.Header.Get("X-Actor-Source") == "" && h.isInstanceAdmin(r.Context(), requestUserID(r))
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "pool id")
	if !ok {
		return
	}
	h.writePool(w, r, uuidToString(id), visible, admin, http.StatusOK)
}

func (h *Handler) writePool(w http.ResponseWriter, r *http.Request, id string, visible map[string]bool, admin bool, status int) {
	d, err := h.loadPoolDirectory(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load pool")
		return
	}
	// A pool that does not reach the reader is not found, not forbidden: its
	// existence is not theirs to know.
	if _, ok := d.pools[id]; !ok || (!admin && !d.reaches(id, visible)) {
		writePoolError(w, service.ErrPoolNotFound)
		return
	}
	resp, err := h.poolResponses(r, d, []string{id}, visible, admin)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load pool availability")
		return
	}
	writeJSON(w, status, resp[0])
}

type createPoolBody struct {
	Name          string   `json:"name"`
	RuntimeType   string   `json:"runtime_type"`
	Description   string   `json:"description"`
	SpendOrder    string   `json:"spend_order"`
	Subscriptions []string `json:"subscriptions"`
	WorkspaceIDs  []string `json:"workspace_ids"`
	AllWorkspaces bool     `json:"all_workspaces"`
}

func parseWorkspaceIDs(w http.ResponseWriter, ids []string) ([]pgtype.UUID, bool) {
	out := make([]pgtype.UUID, 0, len(ids))
	for _, id := range ids {
		u, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(id), "workspace id")
		if !ok {
			return nil, false
		}
		out = append(out, u)
	}
	return out, true
}

// CreatePool — POST /api/pools. Instance admin only.
func (h *Handler) CreatePool(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireInstanceAdmin(w, r)
	if !ok {
		return
	}
	var body createPoolBody
	if !decodeStrict(w, r, 65536, &body) {
		return
	}
	workspaces, ok := parseWorkspaceIDs(w, body.WorkspaceIDs)
	if !ok {
		return
	}
	res, err := h.pools().Create(r.Context(), parseUUID(userID), service.PoolInput{
		Name: body.Name, RuntimeType: body.RuntimeType, Description: body.Description, SpendOrder: body.SpendOrder,
		Subscriptions: body.Subscriptions, Workspaces: workspaces, AllWorkspaces: body.AllWorkspaces,
	})
	if err != nil {
		writePoolError(w, err)
		return
	}
	h.publishRuntimeProfilesChanged(res.Profiles, userID)
	h.writePool(w, r, uuidToString(res.Pool.ID), nil, true, http.StatusCreated)
}

type patchPoolBody struct {
	// Revision is the revision the client read. It is required when the
	// accounts or the spend order change.
	Revision      *int32    `json:"revision"`
	Name          *string   `json:"name"`
	Description   *string   `json:"description"`
	SpendOrder    *string   `json:"spend_order"`
	Subscriptions *[]string `json:"subscriptions"`
	AllWorkspaces *bool     `json:"all_workspaces"`
}

// UpdatePool — PATCH /api/pools/{id}. Instance admin only.
func (h *Handler) UpdatePool(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireInstanceAdmin(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "pool id")
	if !ok {
		return
	}
	var body patchPoolBody
	if !decodeStrict(w, r, 65536, &body) {
		return
	}
	if body.Revision == nil {
		writeError(w, http.StatusBadRequest, "revision is required to update a pool")
		return
	}
	res, err := h.pools().Update(r.Context(), id, parseUUID(userID), body.Revision, service.PoolPatch{
		Name: body.Name, Description: body.Description, SpendOrder: body.SpendOrder, Subscriptions: body.Subscriptions, AllWorkspaces: body.AllWorkspaces,
	})
	if err != nil {
		writePoolError(w, err)
		return
	}
	h.publishRuntimeProfilesChanged(res.Profiles, userID)
	h.afterAccountChange(r.Context())
	h.writePool(w, r, uuidToString(id), nil, true, http.StatusOK)
}

type poolGrantsBody struct {
	Revision     *int32   `json:"revision"`
	WorkspaceIDs []string `json:"workspace_ids"`
}

// SetPoolGrants — PUT /api/pools/{id}/grants. Instance admin only: the
// pool is granted to exactly these workspaces.
func (h *Handler) SetPoolGrants(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireInstanceAdmin(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "pool id")
	if !ok {
		return
	}
	var body poolGrantsBody
	if !decodeStrict(w, r, 65536, &body) {
		return
	}
	if body.Revision == nil {
		writeError(w, http.StatusBadRequest, "revision is required to change grants")
		return
	}
	workspaces, ok := parseWorkspaceIDs(w, body.WorkspaceIDs)
	if !ok {
		return
	}
	res, err := h.pools().SetGrants(r.Context(), id, parseUUID(userID), body.Revision, workspaces)
	if err != nil {
		writePoolError(w, err)
		return
	}
	h.publishRuntimeProfilesChanged(res.Profiles, userID)
	h.writePool(w, r, uuidToString(id), nil, true, http.StatusOK)
}

// DeletePool — DELETE /api/pools/{id}. Instance admin only; a pool whose
// environments still exist is refused.
func (h *Handler) DeletePool(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireInstanceAdmin(w, r); !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "pool id")
	if !ok {
		return
	}
	if err := h.pools().Delete(r.Context(), id); err != nil {
		writePoolError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
