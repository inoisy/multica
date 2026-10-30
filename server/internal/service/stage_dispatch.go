package service

import (
	"context"
	"errors"
	"log/slog"
	"slices"

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
// earlier stage is closed, and sub-issues someone assigned ahead of time start.
// Each write goes through Start, which is the update path a person's edit
// takes, so it starts the assignee's run, appears on the timeline and reaches
// notifications and workflow handoffs the same way. A stage that closed with
// cancelled work halts dispatch: the next stage would build on work that was
// never done, so the epic's assignee is woken to decide instead.
type StageDispatchService struct {
	Issues *IssueService
	Start  StageStarter
}

// StageStarter opens one sub-issue of the open stage on the revision it was
// read at: an unassigned one goes to the epic's squad, one in backlog moves to
// todo, and one already in todo gets its held run. It reports whether a run
// started. A sub-issue that changed meanwhile is left alone and reports false
// without an error.
type StageStarter func(ctx context.Context, issue, parent db.Issue) (started bool, err error)

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

// Dispatch opens the open stage of one epic and returns how many runs it
// started. In that stage an unassigned sub-issue goes to the squad, and a
// sub-issue assigned ahead of time starts now: it leaves backlog, or in todo
// gets the run held back until its stage opened. One that is under way or has
// had a run is left alone, and so is one waiting for the customer.
func (s *StageDispatchService) Dispatch(ctx context.Context, parentID pgtype.UUID) (int, error) {
	if s.Start == nil {
		return 0, errors.New("stage dispatch: no starter configured")
	}
	q := s.Issues.Queries
	parent, err := q.GetIssue(ctx, parentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil || !parent.StageSquadID.Valid {
		return 0, err
	}
	if category, cerr := issuestatus.CategoryWithError(ctx, q, parent.WorkspaceID, parent.Status); cerr != nil {
		return 0, cerr
	} else if category == "done" || category == "closed" {
		return 0, nil
	}
	children, err := s.loadStagedChildren(ctx, parent)
	if err != nil {
		return 0, err
	}
	open := openStage(children)
	if open == nil {
		return 0, nil
	}
	squadUsable, err := s.squadUsable(ctx, parent)
	if err != nil {
		return 0, err
	}
	started := 0
	for _, ch := range children {
		if !ch.issue.Stage.Valid || ch.issue.Stage.Int32 != *open || ch.closed || ch.held || ch.issue.TriageState.Valid {
			continue
		}
		due, err := s.startDue(ctx, ch.issue, squadUsable)
		if err != nil {
			return started, err
		}
		if !due {
			continue
		}
		ran, err := s.Start(ctx, ch.issue, parent)
		if err != nil {
			return started, err
		}
		if ran {
			started++
		}
	}
	return started, nil
}

// startDue reports whether a sub-issue of the open stage still has to be
// started: an unassigned one goes to the squad, and one assigned ahead of time
// leaves backlog or, sitting in todo, gets the run that was held back until
// its stage opened. Work that has had a run, or is under way, is left alone.
func (s *StageDispatchService) startDue(ctx context.Context, issue db.Issue, squadUsable bool) (bool, error) {
	q := s.Issues.Queries
	if !issue.AssigneeID.Valid {
		return squadUsable, nil
	}
	switch issuestatus.Effective(ctx, q, issue.WorkspaceID, issue.Status) {
	case "backlog":
		return true, nil
	case "todo":
		if issue.AssigneeType.String != "agent" && issue.AssigneeType.String != "squad" {
			return false, nil
		}
		ran, err := q.HasTaskForIssue(ctx, issue.ID)
		return !ran, err
	}
	return false, nil
}

// squadUsable reports whether the epic's squad can still take work: the squad
// may have been archived after dispatch was turned on.
func (s *StageDispatchService) squadUsable(ctx context.Context, parent db.Issue) (bool, error) {
	squad, err := s.Issues.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{ID: parent.StageSquadID, WorkspaceID: parent.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if squad.ArchivedAt.Valid {
		slog.Warn("stage dispatch: squad is archived; unassigned sub-issues wait",
			"parent_id", util.UUIDToString(parent.ID), "squad_id", util.UUIDToString(parent.StageSquadID))
		return false, nil
	}
	return true, nil
}

// Held reports whether the issue is a sub-issue of a later stage than the
// open one of an epic the server dispatches. Assigning it or moving it to
// todo then starts no run: the stage opening does. A stage dispatch that
// cannot be read holds too, since the pass that opens the stage starts the
// run anyway.
func (s *StageDispatchService) Held(ctx context.Context, issue db.Issue) bool {
	if !issue.ParentIssueID.Valid || !issue.Stage.Valid {
		return false
	}
	parent, err := s.Issues.Queries.GetIssue(ctx, issue.ParentIssueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false
	}
	if err == nil && !parent.StageSquadID.Valid {
		return false
	}
	if err == nil {
		var children []stagedChild
		if children, err = s.loadStagedChildren(ctx, parent); err == nil {
			open := openStage(children)
			return open == nil || issue.Stage.Int32 > *open
		}
	}
	slog.Warn("stage dispatch: reading the stages of an epic failed; the run waits for its stage",
		"issue_id", util.UUIDToString(issue.ID), "error", err)
	return true
}

// stageHeld is Held for the issue write paths, which have no dispatch service
// of their own.
func (s *IssueService) stageHeld(ctx context.Context, issue db.Issue) bool {
	return (&StageDispatchService{Issues: s}).Held(ctx, issue)
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

// sweepPageSize bounds one page of epics read by the sweep.
const sweepPageSize = 500

// Sweep dispatches every epic with stage dispatch on, page by page so no epic
// waits behind a full page of others. It catches what the immediate pass after
// a sub-issue change missed, and changes no event records, such as a route
// that merged through accept.
func (s *StageDispatchService) Sweep(ctx context.Context) error {
	var errs []error
	after := pgtype.UUID{Valid: true}
	for {
		ids, err := s.Issues.Queries.ListStageDispatchParents(ctx, db.ListStageDispatchParentsParams{After: after, PageSize: sweepPageSize})
		if err != nil {
			return errors.Join(append(errs, err)...)
		}
		for _, id := range ids {
			if _, err := s.Dispatch(ctx, id); err != nil {
				errs = append(errs, err)
			}
		}
		if len(ids) < sweepPageSize {
			return errors.Join(errs...)
		}
		after = ids[len(ids)-1]
	}
}
