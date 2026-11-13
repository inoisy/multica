package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/integrations/express"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ExpressInstallationResponse is one eXpress bot on the management API. The
// secret key is never returned.
type ExpressInstallationResponse struct {
	ID              string `json:"id"`
	WorkspaceID     string `json:"workspace_id"`
	AgentID         string `json:"agent_id"`
	BotID           string `json:"bot_id"`
	CTSURL          string `json:"cts_url"`
	BotName         string `json:"bot_name"`
	InstallerUserID string `json:"installer_user_id"`
	Status          string `json:"status"`
	InstalledAt     string `json:"installed_at"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

func expressInstallationToResponse(row db.ChannelInstallation) ExpressInstallationResponse {
	info := express.DecodePublicConfig(row.Config)
	return ExpressInstallationResponse{
		ID:              uuidToString(row.ID),
		WorkspaceID:     uuidToString(row.WorkspaceID),
		AgentID:         uuidToString(row.AgentID),
		BotID:           info.BotID,
		CTSURL:          info.CTSURL,
		BotName:         info.BotName,
		InstallerUserID: uuidToString(row.InstallerUserID),
		Status:          row.Status,
		InstalledAt:     row.InstalledAt.Time.UTC().Format(time.RFC3339),
		CreatedAt:       row.CreatedAt.Time.UTC().Format(time.RFC3339),
		UpdatedAt:       row.UpdatedAt.Time.UTC().Format(time.RFC3339),
	}
}

// ListExpressInstallations returns the workspace's eXpress bots. When the
// integration is not configured it answers configured=false instead of 503,
// so the settings page can explain why connecting is unavailable.
func (h *Handler) ListExpressInstallations(w http.ResponseWriter, r *http.Request) {
	if h.ExpressInstall == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"installations":     []ExpressInstallationResponse{},
			"configured":        false,
			"install_supported": false,
		})
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	rows, err := h.ExpressInstall.ListByWorkspace(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list express installations")
		return
	}
	out := make([]ExpressInstallationResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, expressInstallationToResponse(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"installations":     out,
		"configured":        true,
		"install_supported": true,
	})
}

// RegisterExpressRequest is the pasted bot registration from the CTS admin.
type RegisterExpressRequest struct {
	CTSURL    string `json:"cts_url"`
	BotID     string `json:"bot_id"`
	SecretKey string `json:"secret_key"`
	BotName   string `json:"bot_name"`
}

// RegisterExpressBot connects an eXpress bot to an agent (?agent_id=).
func (h *Handler) RegisterExpressBot(w http.ResponseWriter, r *http.Request) {
	if h.ExpressInstall == nil {
		writeFeatureDisabled(w, "express_not_configured", "express integration not enabled")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	agentIDStr := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	if agentIDStr == "" {
		writeError(w, http.StatusBadRequest, "agent_id is required")
		return
	}
	agentUUID, ok := parseUUIDOrBadRequest(w, agentIDStr, "agent_id")
	if !ok {
		return
	}
	if _, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          agentUUID,
		WorkspaceID: wsUUID,
	}); err != nil {
		writeError(w, http.StatusNotFound, "agent not found in this workspace")
		return
	}
	initiatorUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}
	var body RegisterExpressRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	row, err := h.ExpressInstall.Register(r.Context(), express.RegisterParams{
		WorkspaceID: wsUUID,
		AgentID:     agentUUID,
		InitiatorID: initiatorUUID,
		CTSURL:      body.CTSURL,
		BotID:       body.BotID,
		SecretKey:   body.SecretKey,
		BotName:     body.BotName,
	})
	if err != nil {
		switch {
		case errors.Is(err, express.ErrInvalidCTSURL),
			errors.Is(err, express.ErrInvalidBotID),
			errors.Is(err, express.ErrInvalidSecretKey):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, express.ErrCredentialsRejected):
			writeError(w, http.StatusBadRequest, "CTS rejected this bot_id / secret_key — check them with the CTS administrator; nothing was saved")
		case errors.Is(err, express.ErrCTSUnreachable):
			writeError(w, http.StatusServiceUnavailable, "could not reach CTS to verify this bot — check the address and the server network; nothing was saved")
		case errors.Is(err, express.ErrBotOwnedBySameWorkspace):
			writeError(w, http.StatusConflict, "this eXpress bot is already connected to another agent in this workspace — disconnect it there first")
		case errors.Is(err, express.ErrBotOwnedByArchivedAgent):
			writeError(w, http.StatusConflict, "this eXpress bot is connected to an archived agent in this workspace — restore that agent or disconnect its bot first")
		case errors.Is(err, express.ErrBotOwnedByAnotherWorkspace):
			writeError(w, http.StatusConflict, "this eXpress bot is already connected to a different Multica workspace — disconnect it there first")
		default:
			writeError(w, http.StatusInternalServerError, "could not save this eXpress bot — something went wrong on the server; nothing was saved")
		}
		return
	}
	h.publish(protocol.EventExpressInstallationCreated, uuidToString(row.WorkspaceID), "user", userID, map[string]any{
		"id": uuidToString(row.ID),
	})
	writeJSON(w, http.StatusOK, expressInstallationToResponse(row))
}

// RevokeExpressInstallation disconnects an eXpress bot.
func (h *Handler) RevokeExpressInstallation(w http.ResponseWriter, r *http.Request) {
	if h.ExpressInstall == nil {
		writeFeatureDisabled(w, "express_not_configured", "express integration not configured")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "workspace id")
	if !ok {
		return
	}
	instUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "installationId"), "installation id")
	if !ok {
		return
	}
	if _, err := h.ExpressInstall.GetInWorkspace(r.Context(), instUUID, wsUUID); err != nil {
		if errors.Is(err, express.ErrInstallationNotFound) {
			writeError(w, http.StatusNotFound, "express installation not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load installation")
		return
	}
	if err := h.ExpressInstall.Revoke(r.Context(), instUUID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to revoke installation")
		return
	}
	h.publish(protocol.EventExpressInstallationRevoked, uuidToString(wsUUID), "user", userID, map[string]any{
		"id": uuidToString(instUUID),
	})
	w.WriteHeader(http.StatusNoContent)
}

// RedeemExpressBindingTokenRequest carries the token from the bind link.
type RedeemExpressBindingTokenRequest struct {
	Token string `json:"token"`
}

// RedeemExpressBindingTokenResponse confirms which eXpress user was bound.
type RedeemExpressBindingTokenResponse struct {
	WorkspaceID    string `json:"workspace_id"`
	InstallationID string `json:"installation_id"`
	ExpressUserID  string `json:"express_user_id"`
}

// RedeemExpressBindingToken binds the logged-in user to the eXpress HUID the
// token carries.
func (h *Handler) RedeemExpressBindingToken(w http.ResponseWriter, r *http.Request) {
	if h.ExpressBindingTokens == nil {
		writeFeatureDisabled(w, "express_not_configured", "express integration not configured")
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req RedeemExpressBindingTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Token == "" {
		writeError(w, http.StatusBadRequest, "token is required")
		return
	}
	userUUID, ok := parseUUIDOrBadRequest(w, userID, "user id")
	if !ok {
		return
	}
	redeemed, err := h.ExpressBindingTokens.RedeemAndBind(r.Context(), req.Token, userUUID)
	if err != nil {
		switch {
		case errors.Is(err, express.ErrBindingTokenInvalid):
			writeError(w, http.StatusGone, "binding token invalid or expired")
		case errors.Is(err, express.ErrBindingAlreadyAssigned):
			writeError(w, http.StatusConflict, "this eXpress account is already bound to a different Multica user")
		case errors.Is(err, express.ErrBindingNotWorkspaceMember):
			writeError(w, http.StatusForbidden, "binding refused (are you a workspace member?)")
		default:
			writeError(w, http.StatusInternalServerError, "failed to redeem token")
		}
		return
	}
	writeJSON(w, http.StatusOK, RedeemExpressBindingTokenResponse{
		WorkspaceID:    uuidToString(redeemed.WorkspaceID),
		InstallationID: uuidToString(redeemed.InstallationID),
		ExpressUserID:  redeemed.ExpressUserID,
	})
}

// ExpressCommand, ExpressStatus and ExpressNotificationCallback are the BotX
// endpoints CTS calls. They answer like a disabled bot when the integration is
// not configured.
func (h *Handler) ExpressCommand(w http.ResponseWriter, r *http.Request) {
	if h.ExpressWebhook == nil {
		writeExpressDisabled(w)
		return
	}
	h.ExpressWebhook.Command(w, r)
}

func (h *Handler) ExpressStatus(w http.ResponseWriter, r *http.Request) {
	if h.ExpressWebhook == nil {
		writeExpressDisabled(w)
		return
	}
	h.ExpressWebhook.Status(w, r)
}

func (h *Handler) ExpressNotificationCallback(w http.ResponseWriter, r *http.Request) {
	if h.ExpressWebhook == nil {
		writeExpressDisabled(w)
		return
	}
	h.ExpressWebhook.NotificationCallback(w, r)
}

func writeExpressDisabled(w http.ResponseWriter) {
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{
		"status":     "error",
		"reason":     "bot_disabled",
		"errors":     []string{"express integration is not enabled on this Multica server"},
		"error_data": map[string]string{"status_message": "Multica: интеграция eXpress не включена"},
	})
}
