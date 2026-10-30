package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/issueworkflow"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// StageDispatchResponse is an epic's stage dispatch: the squad its stages go
// to (null when off) and the person the runs answer to.
type StageDispatchResponse struct {
	IssueID    string  `json:"issue_id"`
	SquadID    *string `json:"squad_id"`
	DispatchBy *string `json:"dispatch_by"`
}

// StageDispatchService is the server-side stage dispatch wired to this
// handler's issue update path.
func (h *Handler) StageDispatchService() *service.StageDispatchService {
	return &service.StageDispatchService{Issues: h.IssueService, Start: h.startStagedIssue}
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
	updated, err := h.StageDispatchService().Set(r.Context(), issue, squadID, by)
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

// StageDispatchEpicResponse is one epic on the conveyor board: the issue and
// the squad its stages go to.
type StageDispatchEpicResponse struct {
	Issue      IssueResponse `json:"issue"`
	SquadID    string        `json:"squad_id"`
	DispatchBy *string       `json:"dispatch_by"`
}

// ListStageDispatch handles GET /api/stage-dispatch: the workspace's epics
// whose stages the server dispatches, for the conveyor board.
func (h *Handler) ListStageDispatch(w http.ResponseWriter, r *http.Request) {
	workspaceID := workspaceIDFromURL(r, "workspaceId")
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	issues, err := h.Queries.ListWorkspaceStageDispatch(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list stage dispatch")
		return
	}
	prefix := h.getIssuePrefix(r.Context(), wsUUID)
	out := make([]StageDispatchEpicResponse, len(issues))
	for i, issue := range issues {
		out[i] = StageDispatchEpicResponse{Issue: issueToResponse(issue, prefix), SquadID: uuidToString(issue.StageSquadID), DispatchBy: uuidToPtr(issue.StageDispatchBy)}
	}
	writeJSON(w, http.StatusOK, map[string]any{"epics": out})
}

// startStagedIssue opens one sub-issue of the open stage. A write follows
// UpdateIssue for a person (the one who turned dispatch on): the same workflow
// check, status guard, issue:updated payload and run decision, so the
// assignment looks like a person's. It does not process child events: the
// caller's next pass and the scheduler sweep do.
func (h *Handler) startStagedIssue(ctx context.Context, prev, parent db.Issue) (bool, error) {
	actorType, actorID := "member", uuidToString(parent.StageDispatchBy)
	ctx = context.WithValue(ctx, wakeupActorKey{}, wakeupActor{kind: actorType, id: actorID})
	assigns := !prev.AssigneeID.Valid
	params := db.UpdateIssueParams{
		ID: prev.ID, ExpectedRevision: pgtype.Int8{Int64: prev.Revision, Valid: true},
		AssigneeType: prev.AssigneeType, AssigneeID: prev.AssigneeID,
		StartDate: prev.StartDate, DueDate: prev.DueDate, ParentIssueID: prev.ParentIssueID,
		ProjectID: prev.ProjectID, Stage: prev.Stage,
	}
	if assigns {
		params.AssigneeType, params.AssigneeID = pgtype.Text{String: "squad", Valid: true}, parent.StageSquadID
	}
	leavesBacklog := issuestatus.Effective(ctx, h.Queries, prev.WorkspaceID, prev.Status) == "backlog"
	if !assigns && !leavesBacklog {
		// Assigned ahead of time and already in todo: nothing to write, only
		// the run held back until this stage opened.
		trigger, willRun := h.IssueService.WillEnqueueRun(ctx, service.IssueTriggerInput{Issue: prev, AssigneeChanged: true}, service.IssueTriggerProbe{})
		if willRun {
			h.dispatchIssueRun(ctx, prev, trigger, actorType, actorID, "")
		}
		return willRun, nil
	}
	if leavesBacklog {
		params.Status = pgtype.Text{String: "todo", Valid: true}
	}
	handoff, err := h.applyIssueWorkflow(ctx, prev, &params, leavesBacklog, assigns, false, nil)
	var notIn *issueworkflow.NotInWorkflowError
	if errors.As(err, &notIn) {
		slog.Warn("stage dispatch: the project workflow has no todo status; sub-issue left in place",
			"issue_id", uuidToString(prev.ID), "workflow", notIn.WorkflowName)
		return false, nil
	}
	if err != nil {
		return false, err
	}
	writeCtx := withWakeupHandedTo(ctx, h.handedToAgent(ctx, prev, params, handoff != nil))
	issue, err := h.updateIssueWithStatusGuard(writeCtx, prev.WorkspaceID, params.Status.String, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // changed since this pass read it
	}
	if err != nil {
		return false, err
	}

	workspaceID := uuidToString(issue.WorkspaceID)
	resp := issueToResponse(issue, h.getIssuePrefix(ctx, issue.WorkspaceID))
	h.fillStatusCategory(ctx, issue.WorkspaceID, &resp)
	assigneeChanged := prev.AssigneeType.String != issue.AssigneeType.String || prev.AssigneeID != issue.AssigneeID
	statusChanged := prev.Status != issue.Status
	trigger, willRun := h.IssueService.WillEnqueueRun(ctx, service.IssueTriggerInput{
		Issue: issue, PrevStatus: prev.Status, AssigneeChanged: assigneeChanged || handoff != nil,
		StatusChanged: statusChanged, EnteredHandoffStep: handoff != nil,
	}, service.IssueTriggerProbe{})
	payload := map[string]any{
		"issue":                      resp,
		"assignee_changed":           assigneeChanged,
		"status_changed":             statusChanged,
		"prev_assignee_type":         textToPtr(prev.AssigneeType),
		"prev_assignee_id":           uuidToPtr(prev.AssigneeID),
		"prev_status":                prev.Status,
		"creator_type":               prev.CreatorType,
		"creator_id":                 uuidToString(prev.CreatorID),
		"duplicate_of_issue_id":      liveDuplicateMark(issue.Status, issue.DuplicateOfIssueID),
		"prev_duplicate_of_issue_id": liveDuplicateMark(prev.Status, prev.DuplicateOfIssueID),
	}
	if handoff != nil {
		payload["workflow_handoff"] = workflowHandoffPayload(issue, handoff, h.workflowHandoffBrief(ctx, issue, handoff), willRun)
	}
	h.publish(protocol.EventIssueUpdated, workspaceID, actorType, actorID, payload)
	if willRun {
		h.dispatchIssueRun(ctx, issue, trigger, actorType, actorID, "")
	}
	slog.Info("stage dispatch: sub-issue opened",
		"parent_id", uuidToString(parent.ID), "issue_id", uuidToString(issue.ID), "stage", issue.Stage.Int32, "run", willRun)
	return willRun, nil
}
