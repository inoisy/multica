package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) modelProfiles() *service.ModelProfileService {
	return &service.ModelProfileService{Queries: h.Queries, TxStarter: h.TxStarter}
}

func writeModelProfileError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrModelProfileInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, service.ErrModelProfileNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, service.ErrModelProfileReferenced), errors.Is(err, service.ErrModelProfileConflict):
		writeError(w, http.StatusConflict, err.Error())
	default:
		slog.Error("model profile request failed", "error", err)
		writeError(w, http.StatusInternalServerError, "model profile request failed")
	}
}

func decodeStrict(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

// ModelRefResponse names an agent or an issue that references a profile.
type ModelRefResponse struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Identifier string `json:"identifier,omitempty"`
}

// ModelProfileResponse is a profile with its rows in their current state and
// who references it.
type ModelProfileResponse struct {
	ID                     string                  `json:"id"`
	Name                   string                  `json:"name"`
	Description            string                  `json:"description"`
	WhenExhaustedProfileID *string                 `json:"when_exhausted_profile_id"`
	Options                []service.ModelRowState `json:"options"`
	Agents                 []ModelRefResponse      `json:"agents"`
	OwnListAgents          []ModelRefResponse      `json:"own_list_agents"`
	OpenIssues             []ModelRefResponse      `json:"open_issues"`
	FallbackOf             []ModelRefResponse      `json:"fallback_of"`
	UpdatedAt              string                  `json:"updated_at"`
}

// modelWorkspace is everything the profile pages show, loaded once.
type modelWorkspace struct {
	world        service.ModelWorld
	profiles     []db.ModelProfile
	agents       []db.Agent
	ownListAgent map[string]bool
	issues       []db.ListOpenIssuesWithModelProfileRow
	prefix       string
}

func (h *Handler) loadModelWorkspace(ctx context.Context, workspaceID pgtype.UUID, now time.Time) (modelWorkspace, error) {
	var m modelWorkspace
	var err error
	if m.world, err = service.LoadModelWorld(ctx, h.Queries, workspaceID, now); err != nil {
		return m, err
	}
	if m.profiles, err = h.Queries.ListModelProfiles(ctx, workspaceID); err != nil {
		return m, err
	}
	if m.agents, err = h.Queries.ListAgents(ctx, workspaceID); err != nil {
		return m, err
	}
	own, err := h.Queries.ListAgentModelOptionsByWorkspace(ctx, workspaceID)
	if err != nil {
		return m, err
	}
	m.ownListAgent = map[string]bool{}
	for _, o := range own {
		m.ownListAgent[util.UUIDToString(o.AgentID)] = true
	}
	if m.issues, err = h.Queries.ListOpenIssuesWithModelProfile(ctx, db.ListOpenIssuesWithModelProfileParams{WorkspaceID: workspaceID}); err != nil {
		return m, err
	}
	if ws, werr := h.Queries.GetWorkspace(ctx, workspaceID); werr == nil {
		m.prefix = ws.IssuePrefix
	}
	return m, nil
}

// profileRows are the rows of one profile in their current state, as the
// agent who views them would see them (ownerID decides private runtimes).
func (m modelWorkspace) profileRows(profileID, ownerID string, now time.Time) []service.ModelRowState {
	p, ok := m.world.Profiles[profileID]
	if !ok {
		return []service.ModelRowState{}
	}
	in := service.ModelChoiceInput{
		Agent:    service.ModelChoiceAgent{OwnerID: ownerID, AnyOwner: ownerID == "", ModelProfileID: profileID},
		Profiles: map[string]service.ModelProfileData{profileID: {ID: p.ID, Name: p.Name, Options: p.Options}},
		Runtimes: m.world.Runtimes,
	}
	rows := service.ChooseModel(in, now).Rows
	for i := range rows {
		rows[i].Chosen = false
	}
	return rows
}

func (m modelWorkspace) profileResponse(p db.ModelProfile, now time.Time) ModelProfileResponse {
	id := util.UUIDToString(p.ID)
	resp := ModelProfileResponse{
		ID: id, Name: p.Name, Description: p.Description, WhenExhaustedProfileID: uuidToPtr(p.WhenExhaustedProfileID),
		Options: m.profileRows(id, "", now), Agents: []ModelRefResponse{}, OwnListAgents: []ModelRefResponse{},
		OpenIssues: []ModelRefResponse{}, FallbackOf: []ModelRefResponse{}, UpdatedAt: timestampToString(p.UpdatedAt),
	}
	for _, a := range m.agents {
		if a.ArchivedAt.Valid || util.UUIDToString(a.ModelProfileID) != id {
			continue
		}
		ref := ModelRefResponse{ID: util.UUIDToString(a.ID), Name: a.Name}
		if m.ownListAgent[ref.ID] {
			resp.OwnListAgents = append(resp.OwnListAgents, ref)
		} else {
			resp.Agents = append(resp.Agents, ref)
		}
	}
	for _, i := range m.issues {
		if util.UUIDToString(i.ModelProfileID) == id {
			resp.OpenIssues = append(resp.OpenIssues, ModelRefResponse{ID: util.UUIDToString(i.ID), Name: i.Title, Identifier: service.IssueIdentifier(m.prefix, i.Number)})
		}
	}
	for _, other := range m.profiles {
		if other.ID != p.ID && util.UUIDToString(other.WhenExhaustedProfileID) == id {
			resp.FallbackOf = append(resp.FallbackOf, ModelRefResponse{ID: util.UUIDToString(other.ID), Name: other.Name})
		}
	}
	return resp
}

// modelReader admits any workspace member; modelAdmin admits owner and admin.
func (h *Handler) modelReader(w http.ResponseWriter, r *http.Request) (pgtype.UUID, bool) {
	wsID := h.resolveWorkspaceID(r)
	if _, ok := h.requireWorkspaceMember(w, r, wsID, "workspace not found"); !ok {
		return pgtype.UUID{}, false
	}
	return parseUUIDOrBadRequest(w, wsID, "workspace id")
}

func (h *Handler) modelAdmin(w http.ResponseWriter, r *http.Request) (pgtype.UUID, pgtype.UUID, bool) {
	wsID := h.resolveWorkspaceID(r)
	member, ok := h.requireWorkspaceMember(w, r, wsID, "workspace not found")
	if !ok {
		return pgtype.UUID{}, pgtype.UUID{}, false
	}
	if !roleAllowed(member.Role, "owner", "admin") {
		writeError(w, http.StatusForbidden, "only workspace owners and admins can change model profiles")
		return pgtype.UUID{}, pgtype.UUID{}, false
	}
	id, ok := parseUUIDOrBadRequest(w, wsID, "workspace id")
	return id, member.UserID, ok
}

// ListModelProfiles handles GET /api/model-profiles.
func (h *Handler) ListModelProfiles(w http.ResponseWriter, r *http.Request) {
	wsID, ok := h.modelReader(w, r)
	if !ok {
		return
	}
	now := time.Now()
	m, err := h.loadModelWorkspace(r.Context(), wsID, now)
	if err != nil {
		writeModelProfileError(w, err)
		return
	}
	out := make([]ModelProfileResponse, 0, len(m.profiles))
	for _, p := range m.profiles {
		out = append(out, m.profileResponse(p, now))
	}
	writeJSON(w, http.StatusOK, out)
}

// GetModelProfile handles GET /api/model-profiles/{id}.
func (h *Handler) GetModelProfile(w http.ResponseWriter, r *http.Request) {
	wsID, ok := h.modelReader(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "profile id")
	if !ok {
		return
	}
	h.writeModelProfile(w, r, wsID, id, http.StatusOK)
}

func (h *Handler) writeModelProfile(w http.ResponseWriter, r *http.Request, wsID, id pgtype.UUID, status int) {
	now := time.Now()
	m, err := h.loadModelWorkspace(r.Context(), wsID, now)
	if err != nil {
		writeModelProfileError(w, err)
		return
	}
	for _, p := range m.profiles {
		if p.ID == id {
			writeJSON(w, status, m.profileResponse(p, now))
			return
		}
	}
	writeModelProfileError(w, service.ErrModelProfileNotFound)
}

// CreateModelProfileRequest creates a profile from rows, or from an agent's
// current list when from_agent_id is set and options is omitted.
type CreateModelProfileRequest struct {
	service.ModelProfileInput
	FromAgentID string `json:"from_agent_id"`
}

// CreateModelProfile handles POST /api/model-profiles.
func (h *Handler) CreateModelProfile(w http.ResponseWriter, r *http.Request) {
	wsID, actor, ok := h.modelAdmin(w, r)
	if !ok {
		return
	}
	var req CreateModelProfileRequest
	if !decodeStrict(w, r, 65536, &req) {
		return
	}
	if req.FromAgentID != "" && req.Options == nil {
		agentID, ok := parseUUIDOrBadRequest(w, req.FromAgentID, "from_agent_id")
		if !ok {
			return
		}
		agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{ID: agentID, WorkspaceID: wsID})
		if err != nil {
			writeError(w, http.StatusBadRequest, "from_agent_id is not an agent of this workspace")
			return
		}
		choice, err := service.PreviewChoice(r.Context(), h.Queries, agent, nil, time.Now())
		if err != nil {
			writeModelProfileError(w, err)
			return
		}
		for _, row := range choice.Rows {
			if row.ProfileID != "" && row.ProfileID != choice.ProfileID {
				continue // a when-exhausted profile's rows are not the agent's list
			}
			o := service.ModelOptionInput{RuntimeID: row.RuntimeID, Model: row.Model, ThinkingLevel: row.ThinkingLevel}
			if len(row.CustomArgs) > 0 {
				args := row.CustomArgs
				o.CustomArgs = &args
			}
			req.Options = append(req.Options, o)
		}
	}
	saved, err := h.modelProfiles().SaveProfile(r.Context(), wsID, pgtype.UUID{}, actor, req.ModelProfileInput)
	if err != nil {
		writeModelProfileError(w, err)
		return
	}
	h.writeModelProfile(w, r, wsID, saved.ID, http.StatusCreated)
}

// UpdateModelProfile handles PUT /api/model-profiles/{id}: replaces the
// profile and its rows; every referencing agent and issue gets the new rows
// on its next run.
func (h *Handler) UpdateModelProfile(w http.ResponseWriter, r *http.Request) {
	wsID, actor, ok := h.modelAdmin(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "profile id")
	if !ok {
		return
	}
	var in service.ModelProfileInput
	if !decodeStrict(w, r, 65536, &in) {
		return
	}
	if _, err := h.modelProfiles().SaveProfile(r.Context(), wsID, id, actor, in); err != nil {
		writeModelProfileError(w, err)
		return
	}
	h.writeModelProfile(w, r, wsID, id, http.StatusOK)
}

// DeleteModelProfile handles DELETE /api/model-profiles/{id}.
func (h *Handler) DeleteModelProfile(w http.ResponseWriter, r *http.Request) {
	wsID, _, ok := h.modelAdmin(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "profile id")
	if !ok {
		return
	}
	if err := h.modelProfiles().DeleteProfile(r.Context(), wsID, id); err != nil {
		writeModelProfileError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ModelProfileChangeResponse is one agent or issue whose chosen model would
// change if the edited profile were saved.
type ModelProfileChangeResponse struct {
	Kind   string               `json:"kind"` // agent | issue
	Ref    ModelRefResponse     `json:"ref"`
	Before *service.ModelOption `json:"before"`
	After  *service.ModelOption `json:"after"`
	Reason string               `json:"reason"`
	Choice service.ModelChoice  `json:"choice"`
}

// PreviewModelProfile handles POST /api/model-profiles/{id}/preview: who
// would get a different model on their next run with the edited rows. The
// edit is not saved.
func (h *Handler) PreviewModelProfile(w http.ResponseWriter, r *http.Request) {
	wsID, ok := h.modelReader(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "profile id")
	if !ok {
		return
	}
	var in service.ModelProfileInput
	if !decodeStrict(w, r, 65536, &in) {
		return
	}
	now := time.Now()
	m, err := h.loadModelWorkspace(r.Context(), wsID, now)
	if err != nil {
		writeModelProfileError(w, err)
		return
	}
	key := util.UUIDToString(id)
	current, exists := m.world.Profiles[key]
	if !exists {
		writeModelProfileError(w, service.ErrModelProfileNotFound)
		return
	}
	edited := current
	edited.Options = nil
	for i, o := range in.Options {
		opt := service.ModelOption{Position: int32(i + 1), RuntimeID: strings.TrimSpace(o.RuntimeID), Model: strings.TrimSpace(o.Model), ThinkingLevel: strings.TrimSpace(o.ThinkingLevel)}
		if o.CustomArgs != nil {
			opt.CustomArgs = *o.CustomArgs
		}
		edited.Options = append(edited.Options, opt)
	}
	if in.WhenExhaustedProfileID != nil {
		edited.WhenExhaustedProfileID = strings.TrimSpace(*in.WhenExhaustedProfileID)
	}
	after := service.ModelWorld{Profiles: map[string]service.ModelProfileData{}, Runtimes: m.world.Runtimes}
	for k, v := range m.world.Profiles {
		after.Profiles[k] = v
	}
	after.Profiles[key] = edited

	changes := []ModelProfileChangeResponse{}
	compare := func(kind string, ref ModelRefResponse, agent service.ModelChoiceAgent, issueID, issueProfile pgtype.UUID) {
		before := service.ChooseModel(m.world.Input(agent, issueID, issueProfile), now)
		next := service.ChooseModel(after.Input(agent, issueID, issueProfile), now)
		if sameOption(before.Chosen, next.Chosen) {
			return
		}
		changes = append(changes, ModelProfileChangeResponse{Kind: kind, Ref: ref, Before: before.Chosen, After: next.Chosen, Reason: next.Reason, Choice: next})
	}
	agentByID := map[string]db.Agent{}
	for _, a := range m.agents {
		agentByID[util.UUIDToString(a.ID)] = a
	}
	for _, a := range m.agents {
		if a.ArchivedAt.Valid {
			continue
		}
		ca, err := service.ChoiceAgent(r.Context(), h.Queries, a)
		if err != nil {
			writeModelProfileError(w, err)
			return
		}
		compare("agent", ModelRefResponse{ID: util.UUIDToString(a.ID), Name: a.Name}, ca, pgtype.UUID{}, pgtype.UUID{})
	}
	for _, i := range m.issues {
		if util.UUIDToString(i.ModelProfileID) != key && !profileReaches(after, util.UUIDToString(i.ModelProfileID), key) {
			continue
		}
		var ca service.ModelChoiceAgent
		if i.AssigneeType.String == "agent" {
			if a, ok := agentByID[util.UUIDToString(i.AssigneeID)]; ok {
				ca, _ = service.ChoiceAgent(r.Context(), h.Queries, a)
			}
		}
		compare("issue", ModelRefResponse{ID: util.UUIDToString(i.ID), Name: i.Title, Identifier: service.IssueIdentifier(m.prefix, i.Number)}, ca, i.ID, i.ModelProfileID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"changes": changes})
}

// profileReaches reports whether from falls back (directly or not) to target.
func profileReaches(w service.ModelWorld, from, target string) bool {
	seen := map[string]bool{}
	for cur := w.Profiles[from].WhenExhaustedProfileID; cur != "" && !seen[cur]; cur = w.Profiles[cur].WhenExhaustedProfileID {
		if cur == target {
			return true
		}
		seen[cur] = true
	}
	return false
}

func sameOption(a, b *service.ModelOption) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.RuntimeID == b.RuntimeID && a.Model == b.Model && a.ThinkingLevel == b.ThinkingLevel
}

// AgentModelChoiceEntry is one line of the agent's choice journal.
type AgentModelChoiceEntry struct {
	TaskID          string               `json:"task_id"`
	Status          string               `json:"status"`
	IssueID         *string              `json:"issue_id"`
	IssueIdentifier string               `json:"issue_identifier,omitempty"`
	IssueTitle      string               `json:"issue_title,omitempty"`
	Position        int32                `json:"position"`
	Choice          *service.ModelChoice `json:"choice"`
	CreatedAt       string               `json:"created_at"`
}

// AgentModelsResponse is the Models section of an agent.
type AgentModelsResponse struct {
	AgentID         string                  `json:"agent_id"`
	CanEdit         bool                    `json:"can_edit"`
	ModelProfileID  *string                 `json:"model_profile_id"`
	ProfileName     string                  `json:"profile_name,omitempty"`
	ProfileAgents   int                     `json:"profile_agents"`
	ProfileIssues   int                     `json:"profile_issues"`
	OwnList         bool                    `json:"own_list"`
	Options         []service.ModelOption   `json:"options"`
	ProfileRows     []service.ModelRowState `json:"profile_rows"`
	DisallowedTools []string                `json:"disallowed_tools"`
	// Preview is what the server would choose for the agent's next run on an
	// issue without its own profile.
	Preview service.ModelChoice          `json:"preview"`
	Last    *AgentModelChoiceEntry       `json:"last"`
	Journal []AgentModelChoiceEntry      `json:"journal"`
	Issues  []ModelRefWithProfile        `json:"issues"`
	Records []RuntimeUnavailableResponse `json:"runtime_unavailable"`
	// Waiting are the agent's unclaimed runs whose every model is
	// unavailable: "every model on a limit until HH:MM — the run waits".
	Waiting []AgentModelChoiceEntry `json:"waiting"`
}

// ModelRefWithProfile is an open issue of the agent with its own profile.
type ModelRefWithProfile struct {
	ModelRefResponse
	ProfileID   string `json:"profile_id"`
	ProfileName string `json:"profile_name"`
}

// RuntimeUnavailableResponse is one active unavailability record.
type RuntimeUnavailableResponse struct {
	RuntimeID   string  `json:"runtime_id"`
	RuntimeName string  `json:"runtime_name"`
	Until       string  `json:"until"`
	Reason      string  `json:"reason"`
	TaskID      *string `json:"task_id"`
	CreatedAt   string  `json:"created_at"`
}

func runtimeUnavailableResponses(world service.ModelWorld) []RuntimeUnavailableResponse {
	out := make([]RuntimeUnavailableResponse, 0, len(world.Records))
	for _, rec := range world.Records {
		out = append(out, RuntimeUnavailableResponse{
			RuntimeID: util.UUIDToString(rec.RuntimeID), RuntimeName: world.Runtimes[util.UUIDToString(rec.RuntimeID)].Name,
			Until: timestampToString(rec.Until), Reason: rec.Reason, TaskID: uuidToPtr(rec.TaskID), CreatedAt: timestampToString(rec.CreatedAt),
		})
	}
	return out
}

func (h *Handler) canEditAgentQuietly(r *http.Request, agent db.Agent) bool {
	member, err := h.getWorkspaceMember(r.Context(), requestUserID(r), uuidToString(agent.WorkspaceID))
	if err != nil {
		return false
	}
	return roleAllowed(member.Role, "owner", "admin") || uuidToString(agent.OwnerID) == requestUserID(r)
}

// GetAgentModels handles GET /api/agents/{id}/models.
func (h *Handler) GetAgentModels(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	h.writeAgentModels(w, r, agent)
}

func (h *Handler) writeAgentModels(w http.ResponseWriter, r *http.Request, agent db.Agent) {
	ctx := r.Context()
	now := time.Now()
	m, err := h.loadModelWorkspace(ctx, agent.WorkspaceID, now)
	if err != nil {
		writeModelProfileError(w, err)
		return
	}
	ca, err := service.ChoiceAgent(ctx, h.Queries, agent)
	if err != nil {
		writeModelProfileError(w, err)
		return
	}
	resp := AgentModelsResponse{
		AgentID: uuidToString(agent.ID), CanEdit: h.canEditAgentQuietly(r, agent), ModelProfileID: uuidToPtr(agent.ModelProfileID),
		OwnList: len(ca.Options) > 0, Options: ca.Options, DisallowedTools: ca.DisallowedTools,
		Preview: service.ChooseModel(m.world.Input(ca, pgtype.UUID{}, pgtype.UUID{}), now),
		Journal: []AgentModelChoiceEntry{}, Issues: []ModelRefWithProfile{}, ProfileRows: []service.ModelRowState{},
		Records: runtimeUnavailableResponses(m.world),
	}
	if resp.Options == nil {
		resp.Options = []service.ModelOption{}
	}
	if resp.DisallowedTools == nil {
		resp.DisallowedTools = []string{}
	}
	if p, ok := m.world.Profiles[ca.ModelProfileID]; ok && ca.ModelProfileID != "" {
		resp.ProfileName = p.Name
		resp.ProfileRows = m.profileRows(p.ID, ca.OwnerID, now)
		for _, a := range m.agents {
			if !a.ArchivedAt.Valid && util.UUIDToString(a.ModelProfileID) == p.ID {
				resp.ProfileAgents++
			}
		}
		for _, i := range m.issues {
			if util.UUIDToString(i.ModelProfileID) == p.ID {
				resp.ProfileIssues++
			}
		}
	}
	for _, i := range m.issues {
		if i.AssigneeType.String != "agent" || i.AssigneeID != agent.ID {
			continue
		}
		pid := util.UUIDToString(i.ModelProfileID)
		resp.Issues = append(resp.Issues, ModelRefWithProfile{
			ModelRefResponse: ModelRefResponse{ID: util.UUIDToString(i.ID), Name: i.Title, Identifier: service.IssueIdentifier(m.prefix, i.Number)},
			ProfileID:        pid, ProfileName: m.world.Profiles[pid].Name,
		})
	}
	choices, err := h.Queries.ListAgentModelChoices(ctx, db.ListAgentModelChoicesParams{AgentID: agent.ID, Limit: 10})
	if err != nil {
		writeModelProfileError(w, err)
		return
	}
	for _, c := range choices {
		entry := AgentModelChoiceEntry{TaskID: util.UUIDToString(c.ID), Status: c.Status, IssueID: uuidToPtr(c.IssueID), Position: c.ModelPosition.Int32, CreatedAt: timestampToString(c.CreatedAt)}
		if c.IssueNumber.Valid {
			entry.IssueIdentifier = service.IssueIdentifier(m.prefix, c.IssueNumber.Int32)
			entry.IssueTitle = c.IssueTitle.String
		}
		var choice service.ModelChoice
		if json.Unmarshal(c.ModelChoice, &choice) == nil {
			entry.Choice = &choice
		}
		resp.Journal = append(resp.Journal, entry)
	}
	resp.Waiting = []AgentModelChoiceEntry{}
	waiting, err := h.Queries.ListAgentWaitingModelTasks(ctx, agent.ID)
	if err != nil {
		writeModelProfileError(w, err)
		return
	}
	for _, t := range waiting {
		entry := AgentModelChoiceEntry{TaskID: util.UUIDToString(t.ID), Status: "queued", IssueID: uuidToPtr(t.IssueID), CreatedAt: timestampToString(t.CreatedAt)}
		if t.IssueNumber.Valid {
			entry.IssueIdentifier = service.IssueIdentifier(m.prefix, t.IssueNumber.Int32)
			entry.IssueTitle = t.IssueTitle.String
		}
		var choice service.ModelChoice
		if json.Unmarshal(t.ModelChoice, &choice) == nil {
			entry.Choice = &choice
		}
		resp.Waiting = append(resp.Waiting, entry)
	}
	if len(resp.Journal) > 0 {
		last := resp.Journal[0]
		resp.Last = &last
	}
	writeJSON(w, http.StatusOK, resp)
}

// UpdateAgentModelsRequest changes the agent's profile, own list or
// disallowed tools. model_profile_id: "" or null clears it. options: [] removes
// the own list. Omitted fields are kept.
type UpdateAgentModelsRequest struct {
	ModelProfileID  json.RawMessage             `json:"model_profile_id"`
	Options         *[]service.ModelOptionInput `json:"options"`
	DisallowedTools *[]string                   `json:"disallowed_tools"`
}

// UpdateAgentModels handles PUT /api/agents/{id}/models. The change applies
// from the agent's next run; a running run keeps the model it started with.
func (h *Handler) UpdateAgentModels(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if !h.canManageAgent(w, r, agent) {
		return
	}
	var req UpdateAgentModelsRequest
	if !decodeStrict(w, r, 65536, &req) {
		return
	}
	in := service.AgentModelsInput{Options: req.Options, DisallowedTools: req.DisallowedTools}
	if len(req.ModelProfileID) > 0 {
		var id *string
		if err := json.Unmarshal(req.ModelProfileID, &id); err != nil {
			writeError(w, http.StatusBadRequest, "model_profile_id must be a string or null")
			return
		}
		if id == nil || *id == "" {
			in.ClearProfile = true
		} else {
			in.ModelProfileID = id
		}
	}
	updated, err := h.modelProfiles().SaveAgentModels(r.Context(), agent, in)
	if err != nil {
		writeModelProfileError(w, err)
		return
	}
	h.writeAgentModels(w, r, updated)
}

// SetIssueModelProfileRequest sets ("<id>") or clears (null) the profile.
type SetIssueModelProfileRequest struct {
	ModelProfileID *string `json:"model_profile_id"`
}

// SetIssueModelProfile handles PUT /api/issues/{id}/model-profile. Anyone who
// may edit the issue may set it; runs of the issue choose from this profile
// instead of their agent's list.
func (h *Handler) SetIssueModelProfile(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var req SetIssueModelProfileRequest
	if !decodeStrict(w, r, 4096, &req) {
		return
	}
	var profileID pgtype.UUID
	if req.ModelProfileID != nil && *req.ModelProfileID != "" {
		id, ok := parseUUIDOrBadRequest(w, *req.ModelProfileID, "model_profile_id")
		if !ok {
			return
		}
		profileID = id
	}
	updated, err := h.modelProfiles().SetIssueProfile(r.Context(), issue, profileID)
	if err != nil {
		writeModelProfileError(w, err)
		return
	}
	h.writeIssueModelProfile(w, r, updated)
}

// IssueModelProfileResponse is the issue's profile; null means the runs use
// their agent's list.
type IssueModelProfileResponse struct {
	IssueID        string  `json:"issue_id"`
	ModelProfileID *string `json:"model_profile_id"`
	ProfileName    string  `json:"profile_name,omitempty"`
}

// GetIssueModelProfile handles GET /api/issues/{id}/model-profile.
func (h *Handler) GetIssueModelProfile(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	h.writeIssueModelProfile(w, r, issue)
}

func (h *Handler) writeIssueModelProfile(w http.ResponseWriter, r *http.Request, issue db.Issue) {
	resp := IssueModelProfileResponse{IssueID: uuidToString(issue.ID), ModelProfileID: uuidToPtr(issue.ModelProfileID)}
	if issue.ModelProfileID.Valid {
		if p, err := h.Queries.GetModelProfileInWorkspace(r.Context(), db.GetModelProfileInWorkspaceParams{ID: issue.ModelProfileID, WorkspaceID: issue.WorkspaceID}); err == nil {
			resp.ProfileName = p.Name
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// ListRuntimeUnavailable handles GET /api/runtime-unavailable: the runtimes
// that are on a provider limit now, and until when.
func (h *Handler) ListRuntimeUnavailable(w http.ResponseWriter, r *http.Request) {
	wsID, ok := h.modelReader(w, r)
	if !ok {
		return
	}
	world, err := service.LoadModelWorld(r.Context(), h.Queries, wsID, time.Now())
	if err != nil {
		writeModelProfileError(w, err)
		return
	}
	out := runtimeUnavailableResponses(world)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Until < out[j].Until })
	writeJSON(w, http.StatusOK, out)
}

// ImportModelProfilesRequest is the import document plus the dry-run switch.
type ImportModelProfilesRequest struct {
	DryRun   *bool                       `json:"dry_run"`
	Document service.ModelImportDocument `json:"document"`
}

// ImportModelProfiles handles POST /api/model-profiles/import: carries the
// control panel's tiers, chains and epic overrides into the database. Dry run
// by default; the plan names every agent whose model would change and why.
func (h *Handler) ImportModelProfiles(w http.ResponseWriter, r *http.Request) {
	wsID, actor, ok := h.modelAdmin(w, r)
	if !ok {
		return
	}
	var req ImportModelProfilesRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid import body")
		return
	}
	dryRun := req.DryRun == nil || *req.DryRun
	plan, err := h.modelProfiles().Import(r.Context(), wsID, actor, req.Document, dryRun, time.Now())
	if err != nil {
		writeModelProfileError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}
