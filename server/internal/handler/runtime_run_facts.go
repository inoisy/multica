package handler

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/service"
)

// RunFactsResponse is the 24-hour window of recorded runs.
type RunFactsResponse struct {
	Since              string           `json:"since"`
	Until              string           `json:"until"`
	Runs               int64            `json:"runs"`
	UnknownAccountRuns int64            `json:"unknown_account_runs"`
	Accounts           map[string]int64 `json:"accounts"`
}

// RuntimeRunFactsResponse answers the environment page about what happened,
// apart from what the next run would do and whether the machine is online.
type RuntimeRunFactsResponse struct {
	RuntimeID      string           `json:"runtime_id"`
	SelectionState string           `json:"selection_state"`
	MachineStatus  string           `json:"machine_status"`
	RunFacts24h    RunFactsResponse `json:"run_facts_24h"`
}

// GetRuntimeRunFacts handles GET /api/runtimes/{runtimeId}/run-facts.
func (h *Handler) GetRuntimeRunFacts(w http.ResponseWriter, r *http.Request) {
	rt, _, ok := h.requireRuntimeReadAccess(w, r, obsmetrics.RuntimeLookupSourceRuntimeAPI, chi.URLParam(r, "runtimeId"))
	if !ok {
		return
	}
	now := time.Now()
	facts, err := service.LoadRunFacts(r.Context(), h.Queries, rt.ID, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load run facts")
		return
	}
	usage, err := service.RuntimeModelUsageOf(r.Context(), h.Queries, rt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load runtime references")
		return
	}
	closed := false
	if rt.ProfileID.Valid {
		profile, err := h.Queries.GetRuntimeProfile(r.Context(), rt.ProfileID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load runtime profile")
			return
		}
		closed = !profile.Enabled
	}
	writeJSON(w, http.StatusOK, RuntimeRunFactsResponse{
		RuntimeID:      uuidToString(rt.ID),
		SelectionState: service.SelectionState(facts, usage, closed),
		MachineStatus:  rt.Status,
		RunFacts24h: RunFactsResponse{
			Since: facts.Since.UTC().Format(time.RFC3339), Until: facts.Until.UTC().Format(time.RFC3339),
			Runs: facts.Runs, UnknownAccountRuns: facts.UnknownAccountRuns, Accounts: facts.Accounts,
		},
	})
}
