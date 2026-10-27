package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/service"
)

// RuntimeProfileRefResponse is a profile that has a row on the runtime.
type RuntimeProfileRefResponse struct {
	ProfileID    string  `json:"profile_id"`
	Name         string  `json:"name"`
	Personal     bool    `json:"personal"`
	OwnerAgentID *string `json:"owner_agent_id"`
	Positions    []int32 `json:"positions"`
}

// RuntimeServingAgentResponse is an agent that can end up on the runtime.
type RuntimeServingAgentResponse struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	ProfileIDs []string `json:"profile_ids"`
}

// RuntimeModelUsageResponse answers the environment page: where the runtime is
// selected, who it serves and whether the references let it go.
type RuntimeModelUsageResponse struct {
	SelectedIn       []RuntimeProfileRefResponse   `json:"selected_in"`
	FallbackProfiles []RuntimeProfileRefResponse   `json:"fallback_profiles"`
	BasedOn          []RuntimeProfileRefResponse   `json:"based_on"`
	ServingAgents    []RuntimeServingAgentResponse `json:"serving_agents"`
	// HiddenAgents and HiddenProfiles count what the reader may not see, so the
	// totals stay true without naming a private agent.
	HiddenAgents   int `json:"hidden_agents"`
	HiddenProfiles int `json:"hidden_profiles"`
	OpenIssues     int `json:"open_issues"`
	// Deletable is advice for the page; DELETE still decides.
	Deletable bool `json:"deletable"`
}

// GetRuntimeModelUsage handles GET /api/runtimes/{runtimeId}/model-usage.
func (h *Handler) GetRuntimeModelUsage(w http.ResponseWriter, r *http.Request) {
	rt, member, ok := h.requireRuntimeReadAccess(w, r, obsmetrics.RuntimeLookupSourceRuntimeAPI, chi.URLParam(r, "runtimeId"))
	if !ok {
		return
	}
	wsID := uuidToString(rt.WorkspaceID)
	actorType, actorID := h.resolveActor(r, requestUserID(r), wsID)
	visible, ok := h.accessibleAgentIDs(r.Context(), wsID, actorType, actorID, member.Role)
	if !ok {
		writeError(w, http.StatusInternalServerError, "failed to load visible agents")
		return
	}
	usage, err := service.RuntimeModelUsageOf(r.Context(), h.Queries, rt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load runtime references")
		return
	}
	resp := RuntimeModelUsageResponse{
		SelectedIn: []RuntimeProfileRefResponse{}, FallbackProfiles: []RuntimeProfileRefResponse{}, BasedOn: []RuntimeProfileRefResponse{},
		ServingAgents: []RuntimeServingAgentResponse{}, OpenIssues: usage.OpenIssues,
	}
	refs := func(in []service.RuntimeProfileRef) []RuntimeProfileRefResponse {
		out := []RuntimeProfileRefResponse{}
		for _, p := range in {
			if p.OwnerAgentID != "" {
				if _, ok := visible[p.OwnerAgentID]; !ok {
					resp.HiddenProfiles++
					continue
				}
			}
			item := RuntimeProfileRefResponse{ProfileID: p.ProfileID, Name: p.Name, Personal: p.OwnerAgentID != "", Positions: p.Positions}
			if p.OwnerAgentID != "" {
				owner := p.OwnerAgentID
				item.OwnerAgentID = &owner
			}
			out = append(out, item)
		}
		return out
	}
	resp.SelectedIn, resp.FallbackProfiles, resp.BasedOn = refs(usage.SelectedIn), refs(usage.FallbackProfiles), refs(usage.BasedOn)
	for _, a := range usage.Agents {
		if _, ok := visible[a.AgentID]; !ok {
			resp.HiddenAgents++
			continue
		}
		item := RuntimeServingAgentResponse{ID: a.AgentID, Name: a.Name, ProfileIDs: a.ViaProfiles}
		if item.ProfileIDs == nil {
			item.ProfileIDs = []string{}
		}
		resp.ServingAgents = append(resp.ServingAgents, item)
	}
	resp.Deletable = len(usage.Agents) == 0 && usage.OpenIssues == 0
	writeJSON(w, http.StatusOK, resp)
}
