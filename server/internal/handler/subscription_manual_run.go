package handler

import (
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Journal of the commands an instance admin ran by hand on an account. It is
// not a run of an agent and never touches agent_task_queue. Only who, which
// account, which machine, which of the two CLIs and when are stored; the
// command's arguments, the credential path and any token are not accepted.

const (
	manualRunDefaultLimit = 50
	manualRunMaxLimit     = 200
	manualRunBodyLimit    = 4 << 10
)

var manualRunDaemonIDRe = regexp.MustCompile(`^[A-Za-z0-9._:@-]{1,200}$`)

type ManualRunResponse struct {
	ID               string  `json:"id"`
	SubscriptionKey  string  `json:"subscription_key"`
	UserID           string  `json:"user_id"`
	UserName         string  `json:"user_name,omitempty"`
	DaemonID         string  `json:"daemon_id"`
	Command          string  `json:"command"`
	StartedAt        string  `json:"started_at"`
	FinishedAt       *string `json:"finished_at"`
	ExitCode         *int32  `json:"exit_code"`
	Unfinished       bool    `json:"unfinished"`
	AgentCapExceeded bool    `json:"agent_cap_exceeded"`
}

func manualRunToResponse(key string, r db.SubscriptionManualRun, userName string) ManualRunResponse {
	out := ManualRunResponse{
		ID: uuidToString(r.ID), SubscriptionKey: key, UserID: uuidToString(r.UserID), UserName: userName,
		DaemonID: r.DaemonID, Command: r.Command, StartedAt: r.StartedAt.Time.UTC().Format(time.RFC3339),
		Unfinished: !r.FinishedAt.Valid, AgentCapExceeded: r.AgentCapExceeded,
	}
	if r.FinishedAt.Valid {
		finished := r.FinishedAt.Time.UTC().Format(time.RFC3339)
		out.FinishedAt = &finished
	}
	if r.ExitCode.Valid {
		out.ExitCode = &r.ExitCode.Int32
	}
	return out
}

// OpenSubscriptionManualRun — POST /api/subscriptions/{key}/manual-runs.
// Instance admin in person only; the account's limit for agents is recorded,
// not enforced.
func (h *Handler) OpenSubscriptionManualRun(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireInstanceAdmin(w, r)
	if !ok {
		return
	}
	var req struct {
		DaemonID string `json:"daemon_id"`
		Command  string `json:"command"`
	}
	// Unknown fields are refused, so arguments or paths cannot be parked in a
	// record under keys the server would otherwise ignore.
	if !decodeStrict(w, r, manualRunBodyLimit, &req) {
		return
	}
	if !manualRunDaemonIDRe.MatchString(req.DaemonID) {
		writeError(w, http.StatusBadRequest, "daemon_id is required")
		return
	}
	if !service.ManualRunCommands[req.Command] {
		writeError(w, http.StatusBadRequest, "command must be claude or codex, without arguments")
		return
	}
	sub, err := h.Queries.GetSubscriptionByKey(r.Context(), chi.URLParam(r, "key"))
	if err != nil {
		writeError(w, http.StatusNotFound, "subscription not found")
		return
	}
	run, err := service.OpenManualRun(r.Context(), h.Queries, sub, parseUUID(userID), req.DaemonID, req.Command, time.Now())
	if err != nil {
		slog.Error("OpenSubscriptionManualRun failed", "error", err, "key", sub.Key)
		writeError(w, http.StatusInternalServerError, "failed to record the run")
		return
	}
	slog.Info("manual run opened", "key", sub.Key, "run_id", uuidToString(run.ID), "daemon_id", run.DaemonID, "command", run.Command, "agent_cap_exceeded", run.AgentCapExceeded)
	writeJSON(w, http.StatusCreated, manualRunToResponse(sub.Key, run, ""))
}

// FinishSubscriptionManualRun — POST /api/subscriptions/{key}/manual-runs/{runId}/finish.
// Only the admin who opened the record closes it; a repeat returns the record
// as it was closed.
func (h *Handler) FinishSubscriptionManualRun(w http.ResponseWriter, r *http.Request) {
	userID, ok := h.requireInstanceAdmin(w, r)
	if !ok {
		return
	}
	var req struct {
		ExitCode *int32 `json:"exit_code"`
	}
	if !decodeStrict(w, r, manualRunBodyLimit, &req) {
		return
	}
	if req.ExitCode == nil || *req.ExitCode < -1 || *req.ExitCode > 255 {
		writeError(w, http.StatusBadRequest, "exit_code from -1 to 255 is required")
		return
	}
	runID, valid := parseUUIDSafe(chi.URLParam(r, "runId"))
	sub, err := h.Queries.GetSubscriptionByKey(r.Context(), chi.URLParam(r, "key"))
	if err != nil || !valid {
		writeError(w, http.StatusNotFound, "manual run not found")
		return
	}
	existing, err := h.Queries.GetSubscriptionManualRun(r.Context(), db.GetSubscriptionManualRunParams{ID: runID, SubscriptionID: sub.ID})
	if err != nil {
		writeError(w, http.StatusNotFound, "manual run not found")
		return
	}
	if uuidToString(existing.UserID) != userID {
		writeError(w, http.StatusForbidden, "a manual run is closed by the admin who started it")
		return
	}
	run, found, err := service.FinishManualRun(r.Context(), h.Queries, sub.ID, runID, *req.ExitCode)
	if err != nil {
		slog.Error("FinishSubscriptionManualRun failed", "error", err, "key", sub.Key, "run_id", uuidToString(runID))
		writeError(w, http.StatusInternalServerError, "failed to close the run")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "manual run not found")
		return
	}
	writeJSON(w, http.StatusOK, manualRunToResponse(sub.Key, run, ""))
}

// ListSubscriptionManualRuns — GET /api/subscriptions/{key}/manual-runs.
// Newest first, `limit` (default 50, at most 200) and `before` (RFC 3339 start
// time) page it. The rows name people and machines, so only an instance admin
// reads them.
func (h *Handler) ListSubscriptionManualRuns(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireInstanceAdmin(w, r); !ok {
		return
	}
	sub, err := h.Queries.GetSubscriptionByKey(r.Context(), chi.URLParam(r, "key"))
	if err != nil {
		writeError(w, http.StatusNotFound, "subscription not found")
		return
	}
	limit := manualRunDefaultLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > manualRunMaxLimit {
			writeError(w, http.StatusBadRequest, "limit must be from 1 to 200")
			return
		}
		limit = n
	}
	var before pgtype.Timestamptz
	if raw := strings.TrimSpace(r.URL.Query().Get("before")); raw != "" {
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "before must be an RFC 3339 time")
			return
		}
		before = pgtype.Timestamptz{Time: t, Valid: true}
	}
	rows, err := h.Queries.ListSubscriptionManualRuns(r.Context(), db.ListSubscriptionManualRunsParams{
		SubscriptionID: sub.ID, Before: before, RowLimit: int32(limit),
	})
	if err != nil {
		slog.Error("ListSubscriptionManualRuns failed", "error", err, "key", sub.Key)
		writeError(w, http.StatusInternalServerError, "failed to load manual runs")
		return
	}
	runs := make([]ManualRunResponse, len(rows))
	for i, row := range rows {
		runs[i] = manualRunToResponse(sub.Key, db.SubscriptionManualRun{
			ID: row.ID, SubscriptionID: row.SubscriptionID, UserID: row.UserID, DaemonID: row.DaemonID, Command: row.Command,
			StartedAt: row.StartedAt, FinishedAt: row.FinishedAt, ExitCode: row.ExitCode, AgentCapExceeded: row.AgentCapExceeded,
		}, row.UserName)
	}
	resp := map[string]any{"key": sub.Key, "runs": runs}
	if len(rows) == limit {
		resp["next_before"] = rows[len(rows)-1].StartedAt.Time.UTC().Format(time.RFC3339Nano)
	}
	writeJSON(w, http.StatusOK, resp)
}
