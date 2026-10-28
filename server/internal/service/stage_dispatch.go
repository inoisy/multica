package service

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// IssueStatusWaiting is the status of a sub-issue that waits for the
// customer: stage dispatch neither assigns it nor lets it hold later stages.
const IssueStatusWaiting = "waiting"

// ErrStageDispatchInvalid maps to 400 in the handler.
var ErrStageDispatchInvalid = errors.New("stage dispatch: invalid request")

// StageDispatchService assigns an epic's stages to a squad on the server: the
// unassigned sub-issues of the first open stage go to the squad once every
// earlier stage is closed. Assignment starts the squad's default route (or
// wakes its leader) exactly as a person assigning would. A stage that closed
// with cancelled work halts dispatch: the next stage would build on work that
// was never done, so the epic's assignee is woken to decide instead.
type StageDispatchService struct {
	Issues *IssueService
}

// Set turns stage dispatch on for the parent (a squad of its workspace) or off
// (an invalid squad id), and dispatches right away when turned on.
func (s *StageDispatchService) Set(ctx context.Context, parent db.Issue, squadID, by pgtype.UUID) (db.Issue, error) {
	q := s.Issues.Queries
	if squadID.Valid {
		squad, err := q.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{ID: squadID, WorkspaceID: parent.WorkspaceID})
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && squad.ArchivedAt.Valid) {
			return parent, errors.Join(ErrStageDispatchInvalid, errors.New("squad not found in this workspace"))
		}
		if err != nil {
			return parent, err
		}
		if err = ensureWaitingStatus(ctx, q, parent.WorkspaceID); err != nil {
			return parent, err
		}
	} else {
		by = pgtype.UUID{}
	}
	updated, err := q.SetIssueStageDispatch(ctx, db.SetIssueStageDispatchParams{ID: parent.ID, WorkspaceID: parent.WorkspaceID, SquadID: squadID, DispatchBy: by})
	if err != nil {
		return parent, err
	}
	if squadID.Valid {
		if _, err = s.Dispatch(ctx, updated.ID); err != nil {
			slog.Warn("stage dispatch: first pass failed; the sweep retries", "parent_id", util.UUIDToString(updated.ID), "error", err)
		}
	}
	return updated, nil
}

// ensureWaitingStatus adds the "waits for the customer" status to the
// workspace catalog when it is missing. An existing entry keeps its name.
func ensureWaitingStatus(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID) error {
	if _, err := q.GetIssueStatusEntryByKey(ctx, db.GetIssueStatusEntryByKeyParams{WorkspaceID: workspaceID, Key: IssueStatusWaiting}); err == nil || !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	_, err := q.CreateIssueStatusEntry(ctx, db.CreateIssueStatusEntryParams{
		WorkspaceID: workspaceID, Key: IssueStatusWaiting, Name: "Ждёт заказчика",
		Category: issuestatus.CategoryUnstarted, Color: "#a855f7",
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return nil // created meanwhile, or the name is taken by an entry the operator configures
	}
	return err
}

// stagedChild is a sub-issue as dispatch reads it.
type stagedChild struct {
	issue     db.Issue
	closed    bool
	cancelled bool
	held      bool
}

// Dispatch assigns the open stage's unassigned sub-issues of one epic and
// returns what it assigned. A sub-issue someone assigned meanwhile is left
// alone: the write only lands on the revision this pass read.
func (s *StageDispatchService) Dispatch(ctx context.Context, parentID pgtype.UUID) ([]db.Issue, error) {
	q := s.Issues.Queries
	parent, err := q.GetIssue(ctx, parentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil || !parent.StageSquadID.Valid {
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
		if !ch.issue.Stage.Valid || ch.issue.Stage.Int32 != *open || ch.closed || ch.held || ch.issue.AssigneeID.Valid || ch.issue.TriageState.Valid {
			continue
		}
		issue, ok, err := s.assign(ctx, ch.issue, parent)
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
		ch := stagedChild{issue: issue, closed: category == "done" || category == "closed", cancelled: category == "closed", held: issue.Status == IssueStatusWaiting}
		children = append(children, ch)
	}
	return children, nil
}

// openStage is the first stage with work left that is not waiting for the
// customer; a stage open only by waiting sub-issues does not hold later ones.
// A stage that closed with a cancelled sub-issue halts dispatch: nothing past
// it opens until that sub-issue is reopened, deleted or moved out of the stage.
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
			open = open || (!ch.closed && !ch.held)
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
// dispatch halts there, so that wake still reaches the epic's assignee.
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
func (s *StageDispatchService) assign(ctx context.Context, issue, parent db.Issue) (db.Issue, bool, error) {
	q := s.Issues.Queries
	status := pgtype.Text{}
	if issuestatus.Effective(ctx, q, issue.WorkspaceID, issue.Status) == "backlog" {
		status = pgtype.Text{String: "todo", Valid: true}
	}
	updated, err := q.UpdateIssue(ctx, db.UpdateIssueParams{
		ID: issue.ID, ExpectedRevision: pgtype.Int8{Int64: issue.Revision, Valid: true},
		Title: pgtype.Text{String: issue.Title, Valid: true}, Description: issue.Description,
		Status: status, Priority: pgtype.Text{String: issue.Priority, Valid: true},
		AssigneeType: pgtype.Text{String: "squad", Valid: true}, AssigneeID: parent.StageSquadID,
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
	actor := ""
	if parent.StageDispatchBy.Valid {
		actor = util.UUIDToString(parent.StageDispatchBy)
	}
	s.Issues.maybeEnqueueOnAssign(ctx, updated, "member", actor, time.Time{})
	slog.Info("stage dispatch: sub-issue assigned to the squad",
		"parent_id", util.UUIDToString(parent.ID), "issue_id", util.UUIDToString(updated.ID), "stage", updated.Stage.Int32)
	return updated, true, nil
}

// Sweep dispatches every epic with stage dispatch on. It catches what the
// immediate pass after a sub-issue change missed, and changes no event
// records, such as a route that merged through accept.
func (s *StageDispatchService) Sweep(ctx context.Context) error {
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
