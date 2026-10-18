package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// ConveyorEventResponse is one line of the route journal.
type ConveyorEventResponse struct {
	ID           string          `json:"id"`
	Seq          int64           `json:"seq"`
	Kind         string          `json:"kind"`
	SourceTaskID *string         `json:"source_task_id"`
	Revision     *int64          `json:"revision"`
	Idx          *int32          `json:"idx"`
	Round        *int32          `json:"round"`
	Action       *string         `json:"action"`
	State        *string         `json:"state"`
	TaskID       *string         `json:"task_id"`
	CoveredBy    *string         `json:"covered_by"`
	Reason       *string         `json:"reason"`
	Details      json.RawMessage `json:"details"`
	CreatedAt    string          `json:"created_at"`
}

func conveyorEventResponse(e db.IssueConveyorEvent) ConveyorEventResponse {
	resp := ConveyorEventResponse{
		ID: uuidToString(e.ID), Seq: e.Seq.Int64, Kind: e.Kind,
		SourceTaskID: uuidToPtr(e.SourceTaskID), TaskID: uuidToPtr(e.TaskID),
		Action: textToPtr(e.Action), State: textToPtr(e.State), CoveredBy: textToPtr(e.CoveredBy), Reason: textToPtr(e.Reason),
		Details: json.RawMessage(e.Details), CreatedAt: timestampToString(e.CreatedAt),
	}
	if e.Revision.Valid {
		resp.Revision = &e.Revision.Int64
	}
	if e.Idx.Valid {
		resp.Idx = &e.Idx.Int32
	}
	if e.Round.Valid {
		resp.Round = &e.Round.Int32
	}
	if len(resp.Details) == 0 {
		resp.Details = json.RawMessage(`{}`)
	}
	return resp
}

func writeConveyorError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrConveyorInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, service.ErrConveyorForbidden):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, service.ErrConveyorNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, service.ErrConveyorConflict), errors.Is(err, service.ErrConveyorStale):
		writeError(w, http.StatusConflict, err.Error())
	default:
		slog.Error("conveyor request failed", "error", err)
		writeError(w, http.StatusInternalServerError, "conveyor request failed")
	}
}

func (h *Handler) conveyorService() service.IssueConveyorService {
	return service.IssueConveyorService{Tasks: h.TaskService}
}

// ApplyIssueConveyor handles PUT /api/issues/{id}/conveyor: enable, pause,
// resume or disable the issue's route.
func (h *Handler) ApplyIssueConveyor(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var cmd service.ConveyorCommand
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cmd); err != nil {
		writeError(w, http.StatusBadRequest, "invalid conveyor body")
		return
	}
	actorType, actorID := h.resolveActor(r, requestUserID(r), uuidToString(issue.WorkspaceID))
	var invoker pgtype.UUID
	if originator := h.invokeOriginatorFromRequest(r, actorType, actorID); originator != "" {
		invoker = parseUUID(originator)
	}
	svc := h.conveyorService()
	// An agent may steer only a route it coordinates; it may start one on the
	// authority of the person its run answers to.
	if actorType == "agent" && cmd.Action != service.ConveyorCmdEnable {
		current, err := svc.View(r.Context(), issue.ID)
		if err != nil {
			writeConveyorError(w, err)
			return
		}
		if current.CoordinatorAgentID != actorID {
			writeError(w, http.StatusForbidden, "only the coordinator of the route may change it")
			return
		}
	}
	view, err := svc.Apply(r.Context(), issue, invoker, cmd)
	if err != nil {
		writeConveyorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// GetIssueConveyor handles GET /api/issues/{id}/conveyor.
func (h *Handler) GetIssueConveyor(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	svc := h.conveyorService()
	view, err := svc.View(r.Context(), issue.ID)
	if err != nil {
		writeConveyorError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// ListIssueConveyorEvents handles GET /api/issues/{id}/conveyor/events: the
// journal from input to decision to run, with what the route covered.
func (h *Handler) ListIssueConveyorEvents(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var cursor, limit int64
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		var err error
		if cursor, err = strconv.ParseInt(raw, 10, 64); err != nil || cursor < 0 {
			writeError(w, http.StatusBadRequest, "invalid cursor")
			return
		}
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		var err error
		if limit, err = strconv.ParseInt(raw, 10, 32); err != nil || limit < 0 {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
	}
	svc := h.conveyorService()
	events, err := svc.Events(r.Context(), issue.ID, cursor, int32(limit))
	if err != nil {
		writeConveyorError(w, err)
		return
	}
	out := make([]ConveyorEventResponse, 0, len(events))
	next := strconv.FormatInt(cursor, 10)
	for _, e := range events {
		out = append(out, conveyorEventResponse(e))
		next = strconv.FormatInt(e.Seq.Int64, 10)
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out, "next_cursor": next})
}

// SubmitIssueConveyorResult handles POST /api/issues/{id}/conveyor/results.
// Only the run that serves the route's current step may hand in a result, and
// who that is comes from the task token, never from the body.
func (h *Handler) SubmitIssueConveyorResult(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if r.Header.Get("X-Actor-Source") != "task_token" {
		writeError(w, http.StatusForbidden, "a route result can only be handed in from within an agent run")
		return
	}
	taskID, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-Task-ID"), "task id")
	if !ok {
		return
	}
	workspaceID := uuidToString(issue.WorkspaceID)
	actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
	if actorType != "agent" {
		writeError(w, http.StatusForbidden, "a route result can only be handed in by an agent")
		return
	}
	var in service.ConveyorResultInput
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 131072))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid result body")
		return
	}
	svc := h.conveyorService()
	sub, err := svc.SubmitResult(r.Context(), issue, taskID, parseUUID(actorID), in)
	if err != nil {
		writeConveyorError(w, err)
		return
	}
	resp := map[string]any{"id": uuidToString(sub.Event.ID), "outcome": sub.Event.Action.String, "created": sub.Created}
	if !sub.Created {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	comment := *sub.Comment
	resp["comment_id"] = uuidToString(comment.ID)
	h.publish(protocol.EventCommentCreated, workspaceID, "agent", actorID, map[string]any{
		"comment":             commentToResponse(comment, nil, nil),
		"issue_title":         issue.Title,
		"issue_assignee_type": textToPtr(issue.AssigneeType),
		"issue_assignee_id":   uuidToPtr(issue.AssigneeID),
		"issue_status":        issue.Status,
		"issue_revision":      sub.IssueRevision,
	})
	// Every agent of the route is suppressed on the comment itself; anyone the
	// report mentions beyond the route is reached as for any other comment.
	h.triggerTasksForComment(r.Context(), issue, comment, nil, "agent", actorID, h.invokeOriginatorFromRequest(r, "agent", actorID), comment.SuppressedAgentIds, nil)
	writeJSON(w, http.StatusCreated, resp)
}

// dropManagedRouteTriggers removes the triggers a run of a route step aims at
// the route's own agents: the route wakes the next agent when the run ends,
// so a mention would only start a second run. Every drop is journaled.
// Triggers of any other author, or of agents outside the route, pass through.
func (h *Handler) dropManagedRouteTriggers(ctx context.Context, issue db.Issue, commentID, sourceTaskID pgtype.UUID, triggers []commentAgentTrigger) []commentAgentTrigger {
	if len(triggers) == 0 || !sourceTaskID.Valid {
		return triggers
	}
	svc := h.conveyorService()
	agents, err := svc.ManagedRouteAgents(ctx, issue.ID, sourceTaskID)
	if err != nil {
		slog.Warn("conveyor: resolve managed route agents failed", "issue_id", uuidToString(issue.ID), "error", err)
		return triggers
	}
	if len(agents) == 0 {
		return triggers
	}
	kept := make([]commentAgentTrigger, 0, len(triggers))
	for _, t := range triggers {
		coveredBy, managed := agents[t.Agent.ID]
		if !managed {
			kept = append(kept, t)
			continue
		}
		svc.NoteSuperseded(ctx, h.Queries, issue.ID, sourceTaskID, coveredBy, "managed_route", map[string]any{
			"agent_id": uuidToString(t.Agent.ID), "comment_id": uuidToString(commentID),
		})
	}
	return kept
}
