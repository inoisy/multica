package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// Outcomes of a step. Only pass can advance the route.
const (
	ConveyorOutcomePass             = "pass"
	ConveyorOutcomeChangesRequested = "changes_requested"
	ConveyorOutcomeBlocked          = "blocked"
	ConveyorOutcomeNeedsCoordinator = "needs_coordinator"

	conveyorResultSchemaVersion = 1
	conveyorReportMaxBytes      = 60000
)

var conveyorCommitPattern = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// ConveyorArtifact says where the work is.
type ConveyorArtifact struct {
	Repo   string `json:"repo,omitempty"`
	Branch string `json:"branch,omitempty"`
	Commit string `json:"commit,omitempty"`
}

// ConveyorCheck is one check the executor ran.
type ConveyorCheck struct {
	Name    string `json:"name"`
	Command string `json:"command,omitempty"`
	OK      bool   `json:"ok"`
}

// ConveyorResultInput is the structured result the run of a step hands in.
type ConveyorResultInput struct {
	SchemaVersion int              `json:"schema_version"`
	Outcome       string           `json:"outcome"`
	Artifact      ConveyorArtifact `json:"artifact"`
	Checks        []ConveyorCheck  `json:"checks"`
	Report        string           `json:"report"`
}

// normalize trims the input and rejects what cannot be a result.
func (in *ConveyorResultInput) normalize() error {
	if in.SchemaVersion != conveyorResultSchemaVersion {
		return conveyorErr(ErrConveyorInvalid, "schema_version must be %d", conveyorResultSchemaVersion)
	}
	switch in.Outcome {
	case ConveyorOutcomePass, ConveyorOutcomeChangesRequested, ConveyorOutcomeBlocked, ConveyorOutcomeNeedsCoordinator:
	default:
		return conveyorErr(ErrConveyorInvalid, "outcome must be pass, changes_requested, blocked or needs_coordinator")
	}
	in.Report = strings.TrimSpace(strings.ReplaceAll(in.Report, "\x00", ""))
	if in.Report == "" || len(in.Report) > conveyorReportMaxBytes || !utf8.ValidString(in.Report) {
		return conveyorErr(ErrConveyorInvalid, "report is required (valid UTF-8, up to %d bytes)", conveyorReportMaxBytes)
	}
	in.Artifact.Repo = strings.TrimSpace(in.Artifact.Repo)
	in.Artifact.Branch = strings.TrimSpace(in.Artifact.Branch)
	in.Artifact.Commit = strings.ToLower(strings.TrimSpace(in.Artifact.Commit))
	if len(in.Artifact.Repo) > 300 || len(in.Artifact.Branch) > 300 {
		return conveyorErr(ErrConveyorInvalid, "artifact.repo and artifact.branch are limited to 300 characters")
	}
	if in.Artifact.Commit != "" && !conveyorCommitPattern.MatchString(in.Artifact.Commit) {
		return conveyorErr(ErrConveyorInvalid, "artifact.commit must be 7-64 hex characters")
	}
	if len(in.Checks) > 50 {
		return conveyorErr(ErrConveyorInvalid, "at most 50 checks")
	}
	if in.Checks == nil {
		in.Checks = []ConveyorCheck{}
	}
	for i := range in.Checks {
		c := &in.Checks[i]
		c.Name, c.Command = strings.TrimSpace(c.Name), strings.TrimSpace(c.Command)
		if c.Name == "" || len(c.Name) > 100 || len(c.Command) > 500 {
			return conveyorErr(ErrConveyorInvalid, "each check needs a name (up to 100) and a command up to 500 characters")
		}
	}
	return nil
}

func (in ConveyorResultInput) canonical() []byte {
	raw, _ := json.Marshal(in)
	return raw
}

// ConveyorSubmission is what a hand-in produced. Created is false for an exact
// replay, which returns the stored result and posts nothing again.
type ConveyorSubmission struct {
	Event         db.IssueConveyorEvent
	Created       bool
	Comment       *db.Comment
	IssueRevision int64
}

// SubmitResult stores the result of the run that serves the current step and
// posts its report. Only that run may hand one in; the result is immutable,
// so an exact repeat answers with the stored one and anything else conflicts.
// It decides nothing: the route moves when the run ends.
func (s *IssueConveyorService) SubmitResult(ctx context.Context, issue db.Issue, taskID, agentID pgtype.UUID, in ConveyorResultInput) (ConveyorSubmission, error) {
	sub, err := s.submitResult(ctx, issue, taskID, agentID, in)
	if errors.Is(err, ErrConveyorForbidden) || errors.Is(err, ErrConveyorConflict) {
		s.noteRefusal(ctx, issue.ID, taskID, agentID, err)
	}
	return sub, err
}

// noteRefusal journals the first refusal of a run's hand-in, so an operator
// reading the journal sees why a result never took effect.
func (s *IssueConveyorService) noteRefusal(ctx context.Context, issueID, taskID, agentID pgtype.UUID, cause error) {
	cv, err := s.q().GetIssueConveyor(ctx, issueID)
	if err != nil {
		return
	}
	details, _ := json.Marshal(map[string]any{"agent_id": util.UUIDToString(agentID)})
	if _, err = s.record(ctx, s.q(), cv, db.InsertIssueConveyorEventParams{
		Kind: conveyorEventRejected, SourceTaskID: taskID, Reason: pgtype.Text{String: clip(cause.Error(), 300), Valid: true}, Details: details,
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("conveyor: journal refusal failed", "issue_id", util.UUIDToString(issueID), "error", err)
	}
}

func (s *IssueConveyorService) submitResult(ctx context.Context, issue db.Issue, taskID, agentID pgtype.UUID, in ConveyorResultInput) (ConveyorSubmission, error) {
	if err := in.normalize(); err != nil {
		return ConveyorSubmission{}, err
	}
	tx, err := s.Tasks.TxStarter.Begin(ctx)
	if err != nil {
		return ConveyorSubmission{}, err
	}
	defer tx.Rollback(ctx)
	q := s.Tasks.Queries.WithTx(tx)
	// Same attribution the HTTP comment path stamps, so event rules see who
	// and which run wrote the report.
	if _, err = tx.Exec(ctx, `SELECT set_config('multica.actor_type','agent',true),set_config('multica.actor_id',$1,true),set_config('multica.source_task_id',$2,true)`, util.UUIDToString(agentID), util.UUIDToString(taskID)); err != nil {
		return ConveyorSubmission{}, err
	}
	cv, err := q.LockIssueConveyor(ctx, issue.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ConveyorSubmission{}, conveyorErr(ErrConveyorForbidden, "the issue has no route")
	}
	if err != nil {
		return ConveyorSubmission{}, err
	}
	task, err := q.GetAgentTask(ctx, taskID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (task.AgentID != agentID || task.IssueID != issue.ID || !task.ConveyorActionID.Valid)) {
		return ConveyorSubmission{}, conveyorErr(ErrConveyorForbidden, "this run is not a step of the route")
	}
	if err != nil {
		return ConveyorSubmission{}, err
	}
	action, err := q.GetIssueConveyorEvent(ctx, task.ConveyorActionID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (action.ConveyorID != cv.ID || action.Action.String != conveyorActionRunStep)) {
		return ConveyorSubmission{}, conveyorErr(ErrConveyorForbidden, "this run is not a step of the route")
	}
	if err != nil {
		return ConveyorSubmission{}, err
	}

	if stored, serr := q.GetIssueConveyorRunEvent(ctx, db.GetIssueConveyorRunEventParams{ConveyorID: cv.ID, SourceTaskID: taskID, Kind: conveyorEventResult}); serr == nil {
		var prev ConveyorResultInput
		if json.Unmarshal(stored.Details, &prev) == nil && string(prev.canonical()) == string(in.canonical()) {
			return ConveyorSubmission{Event: stored}, nil
		}
		return ConveyorSubmission{}, conveyorErr(ErrConveyorConflict, "this run already handed in a different result")
	} else if !errors.Is(serr, pgx.ErrNoRows) {
		return ConveyorSubmission{}, serr
	}

	switch {
	case task.Status != "running":
		return ConveyorSubmission{}, conveyorErr(ErrConveyorConflict, "the run is %s; a result is accepted from a running run", task.Status)
	case cv.Status != ConveyorActive:
		return ConveyorSubmission{}, conveyorErr(ErrConveyorConflict, "the route is %s", cv.Status)
	case !isCurrentStep(cv, action):
		return ConveyorSubmission{}, conveyorErr(ErrConveyorConflict, "the route has moved past this run's step")
	}

	event, err := s.record(ctx, q, cv, db.InsertIssueConveyorEventParams{
		Kind: conveyorEventResult, SourceTaskID: taskID, TaskID: taskID, Revision: action.Revision, Idx: action.Idx, Round: action.Round,
		Action: pgtype.Text{String: in.Outcome, Valid: true}, Details: in.canonical(),
	})
	if err != nil {
		return ConveyorSubmission{}, err
	}
	suppress, err := s.routeAgentIDs(cv)
	if err != nil {
		return ConveyorSubmission{}, err
	}
	route, err := conveyorRoute(cv)
	if err != nil {
		return ConveyorSubmission{}, err
	}
	step := ""
	if node := conveyorNodeAt(route, action.Idx.Int32); node != nil {
		step = node.Step
	}
	created, err := q.CreateComment(ctx, db.CreateCommentParams{
		ID: dbid.NewV7(), IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, AuthorType: "agent", AuthorID: agentID,
		Content: conveyorReportComment(step, action.Round.Int32, in), Type: "comment",
		SourceTaskID: taskID, SuppressedAgentIds: suppress,
	})
	if err != nil {
		return ConveyorSubmission{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return ConveyorSubmission{}, err
	}
	comment := created.Comment()
	return ConveyorSubmission{Event: event, Created: true, Comment: &comment, IssueRevision: created.IssueRevision}, nil
}

func conveyorReportComment(step string, round int32, in ConveyorResultInput) string {
	var b strings.Builder
	b.WriteString(in.Report)
	fmt.Fprintf(&b, "\n\n---\n**Результат шага `%s`** (раунд %d): `%s`", step, round, in.Outcome)
	if in.Artifact.Commit != "" {
		fmt.Fprintf(&b, " · commit `%s`", in.Artifact.Commit)
	}
	if in.Artifact.Branch != "" {
		fmt.Fprintf(&b, " · ветка `%s`", in.Artifact.Branch)
	}
	for _, c := range in.Checks {
		mark := "ok"
		if !c.OK {
			mark = "FAIL"
		}
		fmt.Fprintf(&b, "\n- %s: %s", c.Name, mark)
		if c.Command != "" {
			fmt.Fprintf(&b, " (`%s`)", c.Command)
		}
	}
	return b.String()
}
