package service

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/featureflag"
)

// ErrStageDispatchInvalid maps to 400 in the handler.
var ErrStageDispatchInvalid = errors.New("stage dispatch: invalid request")

// StageDispatchService assigns a parent issue's stages to a squad on the
// server (#7345): the unassigned sub-issues of the first open stage go to the
// squad once every earlier stage is closed. Assignment starts the squad's
// leader exactly as a person assigning would. A stage that closed with
// cancelled work halts dispatch: the next stage would build on work that was
// never done, so the parent's assignee is woken to decide instead.
type StageDispatchService struct {
	Issues *IssueService
	Flags  *featureflag.Service
}

// Enabled reports whether stage dispatch runs at all (stage_dispatch_v1).
func (s *StageDispatchService) Enabled(ctx context.Context) bool {
	return featureflags.StageDispatchV1Enabled(ctx, s.Flags)
}

// Set turns stage dispatch on for the parent (a squad of its workspace,
// answering to the member by) or off (an invalid squad id), and dispatches
// right away when turned on.
func (s *StageDispatchService) Set(ctx context.Context, parent db.Issue, squadID, by pgtype.UUID) (*db.IssueStageDispatch, error) {
	q := s.Issues.Queries
	if !squadID.Valid {
		return nil, q.DeleteIssueStageDispatch(ctx, parent.ID)
	}
	squad, err := q.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{ID: squadID, WorkspaceID: parent.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && squad.ArchivedAt.Valid) {
		return nil, errors.Join(ErrStageDispatchInvalid, errors.New("squad not found in this workspace"))
	}
	if err != nil {
		return nil, err
	}
	row, err := q.UpsertIssueStageDispatch(ctx, db.UpsertIssueStageDispatchParams{
		IssueID: parent.ID, WorkspaceID: parent.WorkspaceID, SquadID: squadID, DispatchBy: by,
	})
	if err != nil {
		return nil, err
	}
	if _, err = s.Dispatch(ctx, parent.ID); err != nil {
		slog.Warn("stage dispatch: first pass failed; the sweep retries", "parent_id", util.UUIDToString(parent.ID), "error", err)
	}
	return &row, nil
}

// stagedChild is a sub-issue as dispatch reads it.
type stagedChild struct {
	issue     db.Issue
	closed    bool
	cancelled bool
}

// Dispatch assigns the open stage's unassigned sub-issues of one parent and
// returns what it assigned. A sub-issue someone assigned meanwhile is left
// alone: the write only lands on the revision this pass read.
func (s *StageDispatchService) Dispatch(ctx context.Context, parentID pgtype.UUID) ([]db.Issue, error) {
	if !s.Enabled(ctx) {
		return nil, nil
	}
	q := s.Issues.Queries
	cfg, err := q.GetIssueStageDispatch(ctx, parentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	parent, err := q.GetIssue(ctx, parentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if category, cerr := issuestatus.CategoryWithError(ctx, q, parent.WorkspaceID, parent.Status); cerr != nil {
		return nil, cerr
	} else if category == "done" || category == "closed" {
		return nil, nil
	}
	children, err := s.loadStagedChildren(ctx, parent)
	if err != nil {
		return nil, err
	}
	open := openStage(children)
	if open == nil {
		return nil, nil
	}
	var assigned []db.Issue
	for _, ch := range children {
		if !ch.issue.Stage.Valid || ch.issue.Stage.Int32 != *open || ch.closed || ch.issue.AssigneeID.Valid || ch.issue.TriageState.Valid {
			continue
		}
		issue, ok, err := s.assign(ctx, ch.issue, cfg)
		if err != nil {
			return assigned, err
		}
		if ok {
			assigned = append(assigned, issue)
		}
	}
	return assigned, nil
}

func (s *StageDispatchService) loadStagedChildren(ctx context.Context, parent db.Issue) ([]stagedChild, error) {
	q := s.Issues.Queries
	rows, err := q.ListChildIssues(ctx, parent.ID)
	if err != nil {
		return nil, err
	}
	children := make([]stagedChild, 0, len(rows))
	for _, issue := range rows {
		if issue.WorkspaceID != parent.WorkspaceID {
			continue
		}
		category, err := issuestatus.CategoryWithError(ctx, q, issue.WorkspaceID, issue.Status)
		if err != nil {
			return nil, err
		}
		children = append(children, stagedChild{issue: issue, closed: category == "done" || category == "closed", cancelled: category == "closed"})
	}
	return children, nil
}

// openStage is the first stage with work left. Only stages that have
// sub-issues count. A stage that closed with a cancelled sub-issue halts
// dispatch: nothing past it opens until that sub-issue is reopened, deleted
// or moved out of the stage.
func openStage(children []stagedChild) *int32 {
	var stages []int32
	for _, ch := range children {
		if ch.issue.Stage.Valid && !slices.Contains(stages, ch.issue.Stage.Int32) {
			stages = append(stages, ch.issue.Stage.Int32)
		}
	}
	slices.Sort(stages)
	for _, stage := range stages {
		open, cancelled := false, false
		for _, ch := range children {
			if !ch.issue.Stage.Valid || ch.issue.Stage.Int32 != stage {
				continue
			}
			open = open || !ch.closed
			cancelled = cancelled || ch.cancelled
		}
		if open {
			return &stage
		}
		if cancelled {
			return nil
		}
	}
	return nil
}

// stageCancelled reports whether the stages the child_done reading counts as
// closed (stage:N and every one before it) hold a cancelled sub-issue. Stage
// dispatch halts there, so that wake still reaches the parent's assignee.
func stageCancelled(observed map[string]any) bool {
	closed, _ := observed["stage"].(int32)
	stages, _ := observed["stages"].([]stageCount)
	for _, sc := range stages {
		if sc.Stage <= closed && sc.Cancelled > 0 {
			return true
		}
	}
	return false
}

// assign gives one sub-issue to the parent's squad on the revision it was
// read at; a backlog issue moves to todo so the assignment starts work.
func (s *StageDispatchService) assign(ctx context.Context, issue db.Issue, cfg db.IssueStageDispatch) (db.Issue, bool, error) {
	q := s.Issues.Queries
	status := pgtype.Text{}
	if issuestatus.Effective(ctx, q, issue.WorkspaceID, issue.Status) == "backlog" {
		status = pgtype.Text{String: "todo", Valid: true}
	}
	updated, err := q.UpdateIssue(ctx, db.UpdateIssueParams{
		ID: issue.ID, ExpectedRevision: pgtype.Int8{Int64: issue.Revision, Valid: true},
		Title: pgtype.Text{String: issue.Title, Valid: true}, Description: issue.Description,
		Status: status, Priority: pgtype.Text{String: issue.Priority, Valid: true},
		AssigneeType: pgtype.Text{String: "squad", Valid: true}, AssigneeID: cfg.SquadID,
		StartDate: issue.StartDate, DueDate: issue.DueDate, ParentIssueID: issue.ParentIssueID,
		ProjectID: issue.ProjectID, Stage: issue.Stage, DuplicateOfIssueID: issue.DuplicateOfIssueID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return issue, false, nil // changed since this pass read it
	}
	if err != nil {
		return issue, false, err
	}
	if s.Issues.TaskService != nil && s.Issues.TaskService.Bus != nil {
		s.Issues.TaskService.broadcastIssueUpdated(ctx, updated, issue.Status)
	}
	s.Issues.maybeEnqueueOnAssign(ctx, updated, "member", util.UUIDToString(cfg.DispatchBy), time.Time{})
	slog.Info("stage dispatch: sub-issue assigned to the squad",
		"parent_id", util.UUIDToString(cfg.IssueID), "issue_id", util.UUIDToString(updated.ID), "stage", updated.Stage.Int32)
	return updated, true, nil
}

// Sweep dispatches every parent with stage dispatch on. It catches what the
// pass right after a sub-issue change missed: a failed pass, and changes that
// record no sub-issue event.
func (s *StageDispatchService) Sweep(ctx context.Context) error {
	if !s.Enabled(ctx) {
		return nil
	}
	ids, err := s.Issues.Queries.ListStageDispatchParents(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, id := range ids {
		if _, err := s.Dispatch(ctx, id); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
