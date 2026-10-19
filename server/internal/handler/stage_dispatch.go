package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// StageDispatchResponse is an epic's stage dispatch: the squad its stages go
// to (null when off) and the person the runs answer to.
type StageDispatchResponse struct {
	IssueID    string  `json:"issue_id"`
	SquadID    *string `json:"squad_id"`
	DispatchBy *string `json:"dispatch_by"`
}

func stageDispatchResponse(issue db.Issue) StageDispatchResponse {
	return StageDispatchResponse{IssueID: uuidToString(issue.ID), SquadID: uuidToPtr(issue.StageSquadID), DispatchBy: uuidToPtr(issue.StageDispatchBy)}
}

// GetIssueStageDispatch handles GET /api/issues/{id}/stage-dispatch.
func (h *Handler) GetIssueStageDispatch(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, stageDispatchResponse(issue))
}

// SetIssueStageDispatch handles PUT /api/issues/{id}/stage-dispatch with
// {"squad_id": "<id>"} to have the server assign the epic's stages to that
// squad, or {"squad_id": null} to stop. Only a person turns it on: the runs
// it starts answer to them.
func (h *Handler) SetIssueStageDispatch(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var req struct {
		SquadID *string `json:"squad_id"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid stage dispatch body")
		return
	}
	actorType, actorID := h.resolveActor(r, requestUserID(r), uuidToString(issue.WorkspaceID))
	var squadID, by pgtype.UUID
	if req.SquadID != nil {
		if actorType != "member" {
			writeError(w, http.StatusForbidden, "only a person can turn stage dispatch on")
			return
		}
		id, ok := parseUUIDOrBadRequest(w, *req.SquadID, "squad_id")
		if !ok {
			return
		}
		squad, err := h.Queries.GetSquadInWorkspace(r.Context(), db.GetSquadInWorkspaceParams{ID: id, WorkspaceID: issue.WorkspaceID})
		if err != nil {
			writeError(w, http.StatusBadRequest, "squad not found in this workspace")
			return
		}
		if !h.canEnqueueSquadLeader(r.Context(), squad.LeaderID, actorType, actorID, actorID, uuidToString(issue.WorkspaceID)) {
			writeError(w, http.StatusForbidden, "you may not hand work to this squad's leader")
			return
		}
		squadID, by = id, parseUUID(actorID)
	}
	svc := &service.StageDispatchService{Issues: h.IssueService}
	updated, err := svc.Set(r.Context(), issue, squadID, by)
	if errors.Is(err, service.ErrStageDispatchInvalid) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update stage dispatch")
		return
	}
	writeJSON(w, http.StatusOK, stageDispatchResponse(updated))
}
