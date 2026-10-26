package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/issueworkflow"
	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Workflow artifacts: the code revision handed between steps of an adopted
// project workflow. A step's handler pins it with the status change that
// hands the issue on (`multica issue status <id> <next> --commit … --repo …`);
// every run queued afterwards carries a snapshot of it in its context, and
// the acceptance step merges exactly that commit. There is no second
// step-result protocol: the status change is the handoff.

// workflowArtifactWrite rides the context of a status write whose project
// workflow controls execution. writeWorkflowArtifact applies it inside the
// write's transaction, after the issue row is locked by the update.
type workflowArtifactWrite struct {
	Previous db.Issue
	// Artifact is the revision the write pins; nil keeps the current one.
	Artifact *issueworkflow.Artifact
	// Run is the assignee's own run making the write; nil for members and
	// for other agents. Only that run's snapshot is checked against the pin.
	Run *db.AgentTaskQueue
}

type workflowArtifactWriteKey struct{}

var errWorkflowArtifactChanged = errors.New("the pinned commit changed after this run started; review the current commit before handing off")

// writeWorkflowArtifact runs in the status write's transaction. A project
// move drops the pin (it belongs to the old workflow); a status change by the
// assignee's run is refused when the run saw a different pin than the one
// stored, so a reviewer cannot pass a commit someone has since replaced.
func writeWorkflowArtifact(ctx context.Context, q *db.Queries, issue db.Issue) error {
	write, ok := ctx.Value(workflowArtifactWriteKey{}).(workflowArtifactWrite)
	if !ok {
		return nil
	}
	prev := write.Previous
	if prev.ProjectID != issue.ProjectID {
		return q.DeleteIssueWorkflowArtifact(ctx, issue.ID)
	}
	if prev.Status == issue.Status {
		return nil
	}
	def, _, err := issueworkflow.ForProject(ctx, q, issue.WorkspaceID, issue.ProjectID)
	if err != nil || def == nil || !def.ControlsExecution() {
		return err
	}
	current, err := q.GetIssueWorkflowArtifact(ctx, db.GetIssueWorkflowArtifactParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if write.Run != nil && err == nil && current.WorkflowID == def.ID && current.ProjectID == issue.ProjectID {
		var pinned issueworkflow.Artifact
		if json.Unmarshal(current.Artifact, &pinned) != nil {
			return errWorkflowArtifactChanged
		}
		seen := issueworkflow.TaskArtifact(write.Run.Context)
		if seen == nil || *seen != pinned {
			return errWorkflowArtifactChanged
		}
	}
	if write.Artifact == nil {
		return nil
	}
	if !def.ID.Valid {
		return errors.New("save the workflow before pinning a commit")
	}
	raw, err := json.Marshal(write.Artifact)
	if err != nil {
		return err
	}
	return q.PutIssueWorkflowArtifact(ctx, db.PutIssueWorkflowArtifactParams{
		IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, ProjectID: issue.ProjectID,
		WorkflowID: def.ID, Artifact: raw,
	})
}

// prepareWorkflowArtifact validates a status write against the project's
// workflow artifact and returns the context the write must run under. It
// writes the refusal and reports false when the write may not go on.
func (h *Handler) prepareWorkflowArtifact(w http.ResponseWriter, r *http.Request, prev db.Issue, params db.UpdateIssueParams, req UpdateIssueRequest, run *db.AgentTaskQueue) (context.Context, bool) {
	ctx := r.Context()
	statusChanging := params.Status.Valid && params.Status.String != prev.Status
	projectMoving := params.ProjectID != prev.ProjectID
	if req.WorkflowArtifact == nil && !statusChanging && !projectMoving {
		return ctx, true
	}
	def, _, err := issueworkflow.ForProject(ctx, h.Queries, prev.WorkspaceID, params.ProjectID)
	if err != nil {
		slog.Error("update issue: load workflow for artifact", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to load workflow")
		return ctx, false
	}
	controls := def != nil && def.ControlsExecution()
	if req.WorkflowArtifact != nil {
		if !controls {
			writeError(w, http.StatusBadRequest, "a pinned commit needs a project Workflow with handoffs")
			return ctx, false
		}
		if err := req.WorkflowArtifact.Validate(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return ctx, false
		}
		if !statusChanging || projectMoving {
			writeError(w, http.StatusBadRequest, "pin the commit with a status change inside the same project")
			return ctx, false
		}
	}
	if !controls && !projectMoving {
		return ctx, true
	}
	// Only the assignee's own run may pin, and only its snapshot is checked:
	// other agents work on the issue as observers.
	var ownRun *db.AgentTaskQueue
	if run != nil && run.Status == "running" && taskRunsForAssignee(prev, run) {
		ownRun = run
	}
	actorType, _ := h.resolveActor(r, requestUserID(r), uuidToString(prev.WorkspaceID))
	if actorType == "agent" && req.WorkflowArtifact != nil && ownRun == nil {
		writeErrorCode(w, http.StatusForbidden, "workflow_pin_not_step_run", "only the run of the step's handler may pin a commit")
		return ctx, false
	}
	return context.WithValue(ctx, workflowArtifactWriteKey{}, workflowArtifactWrite{Previous: prev, Artifact: req.WorkflowArtifact, Run: ownRun}), true
}

func writeWorkflowArtifactError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, errWorkflowArtifactChanged):
		writeErrorCode(w, http.StatusConflict, "workflow_artifact_changed", err.Error())
	default:
		return false
	}
	return true
}

// GetIssueWorkflowArtifact handles GET /api/issues/{id}/workflow-artifact:
// the commit pinned for the issue's current workflow and where the current
// step leads (next on success, back on a return). The acceptance step reads
// it to know what to merge. A run of the issue gets the snapshot it was
// queued with, and 409 when the pin has moved since: it must not merge a
// commit nobody handed to it.
func (h *Handler) GetIssueWorkflowArtifact(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	def, _, err := issueworkflow.ForProject(r.Context(), h.Queries, issue.WorkspaceID, issue.ProjectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load workflow")
		return
	}
	if def == nil || !def.ControlsExecution() {
		writeError(w, http.StatusNotFound, "the issue's project has no Workflow with handoffs")
		return
	}
	out := map[string]any{"status": issue.Status, "next_status": def.NextStatus(issue.Status)}
	if step, ok := def.Step(issue.Status); ok && step.BackStatusKey != "" {
		out["back_status"] = step.BackStatusKey
	}
	stored, err := h.Queries.GetIssueWorkflowArtifact(r.Context(), db.GetIssueWorkflowArtifactParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to load the pinned commit")
		return
	}
	var pinned *issueworkflow.Artifact
	if err == nil && stored.WorkflowID == def.ID && stored.ProjectID == issue.ProjectID {
		var a issueworkflow.Artifact
		if err := json.Unmarshal(stored.Artifact, &a); err != nil {
			writeError(w, http.StatusInternalServerError, "the pinned commit is unreadable")
			return
		}
		pinned = &a
	}
	actorType, _ := h.resolveActor(r, requestUserID(r), uuidToString(issue.WorkspaceID))
	if run := h.agentRunOnIssue(r, actorType, issue); run != nil {
		seen := issueworkflow.TaskArtifact(run.Context)
		if (seen == nil) != (pinned == nil) || (seen != nil && *seen != *pinned) {
			writeErrorCode(w, http.StatusConflict, "workflow_artifact_changed", errWorkflowArtifactChanged.Error())
			return
		}
	}
	if pinned != nil {
		out["artifact"] = pinned
	}
	writeJSON(w, http.StatusOK, out)
}
