package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// Errors of the model profile API; the handler maps them to status codes.
var (
	ErrModelProfileInvalid    = errors.New("invalid model profile")
	ErrModelProfileNotFound   = errors.New("model profile not found")
	ErrModelProfileReferenced = errors.New("model profile is referenced")
	ErrModelProfileConflict   = errors.New("model profile name is taken")
)

const (
	maxModelOptions     = 12
	maxModelFieldLength = 200
	maxCustomArgs       = 64
	maxDisallowedTools  = 200
)

// ModelProfileService owns profiles, the agents' own lists and the reads the
// claim and the Models screen need. Writes take effect from the next run.
type ModelProfileService struct {
	Queries   *db.Queries
	TxStarter TxStarter
}

// ModelOptionInput is one row as an API caller sends it.
type ModelOptionInput struct {
	RuntimeID     string    `json:"runtime_id"`
	Model         string    `json:"model"`
	ThinkingLevel string    `json:"thinking_level"`
	CustomArgs    *[]string `json:"custom_args"`
}

// ModelProfileInput creates or replaces a profile.
type ModelProfileInput struct {
	Name                   string             `json:"name"`
	Description            string             `json:"description"`
	WhenExhaustedProfileID *string            `json:"when_exhausted_profile_id"`
	Options                []ModelOptionInput `json:"options"`
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrModelProfileInvalid, fmt.Sprintf(format, args...))
}

func (s *ModelProfileService) withTx(ctx context.Context, fn func(q *db.Queries) error) error {
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := fn(s.Queries.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// normalizeOptions validates rows against the workspace's runtimes and numbers
// them by their order. owner, when set, must be allowed to use each runtime
// (an agent's own list); a profile is shared, so its private rows are checked
// per agent at choice time instead.
func normalizeOptions(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID, in []ModelOptionInput, owner *pgtype.UUID) ([]ModelOption, error) {
	if len(in) > maxModelOptions {
		return nil, invalid("at most %d models in a list", maxModelOptions)
	}
	runtimes, err := q.ListAgentRuntimes(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	byID := map[string]db.AgentRuntime{}
	for _, rt := range runtimes {
		byID[util.UUIDToString(rt.ID)] = rt
	}
	out := make([]ModelOption, 0, len(in))
	for i, o := range in {
		id, err := util.ParseUUID(strings.TrimSpace(o.RuntimeID))
		if err != nil {
			return nil, invalid("row %d: runtime_id is not a UUID", i+1)
		}
		rt, ok := byID[util.UUIDToString(id)]
		if !ok {
			return nil, invalid("row %d: runtime is not in this workspace", i+1)
		}
		if owner != nil && rt.Visibility == "private" && rt.OwnerID.Valid && rt.OwnerID != *owner {
			return nil, invalid("row %d: runtime %q is private to another member", i+1, runtimeDisplayName(rt))
		}
		model, thinking := strings.TrimSpace(o.Model), strings.TrimSpace(o.ThinkingLevel)
		if utf8.RuneCountInString(model) > maxModelFieldLength || utf8.RuneCountInString(thinking) > maxModelFieldLength {
			return nil, invalid("row %d: model or thinking level is too long", i+1)
		}
		opt := ModelOption{Position: int32(i + 1), RuntimeID: util.UUIDToString(id), Model: model, ThinkingLevel: thinking}
		if o.CustomArgs != nil {
			if len(*o.CustomArgs) > maxCustomArgs {
				return nil, invalid("row %d: at most %d custom args", i+1, maxCustomArgs)
			}
			opt.CustomArgs = append([]string{}, (*o.CustomArgs)...)
		}
		out = append(out, opt)
	}
	return out, nil
}

func runtimeDisplayName(rt db.AgentRuntime) string {
	if rt.CustomName.Valid && rt.CustomName.String != "" {
		return rt.CustomName.String
	}
	return rt.Name
}

func encodeArgs(args []string) []byte {
	if args == nil {
		return nil
	}
	raw, _ := json.Marshal(args)
	return raw
}

func decodeArgs(raw []byte) []string {
	if raw == nil {
		return nil
	}
	var out []string
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	if out == nil {
		out = []string{}
	}
	return out
}

func profileOptionFromRow(r db.ModelProfileOption) ModelOption {
	return ModelOption{Position: r.Position, RuntimeID: util.UUIDToString(r.RuntimeID), Model: r.Model, ThinkingLevel: r.ThinkingLevel, CustomArgs: decodeArgs(r.CustomArgs)}
}

func agentOptionFromRow(r db.AgentModelOption) ModelOption {
	return ModelOption{Position: r.Position, RuntimeID: util.UUIDToString(r.RuntimeID), Model: r.Model, ThinkingLevel: r.ThinkingLevel, CustomArgs: decodeArgs(r.CustomArgs)}
}

// checkExhaustedChain refuses a when_exhausted_profile_id that would make the
// fallback graph cyclic. edges is the current graph with the edit applied.
func checkExhaustedChain(start string, edges map[string]string) error {
	seen := map[string]bool{}
	for cur := start; cur != ""; cur = edges[cur] {
		if seen[cur] {
			return invalid("the when-exhausted profiles form a cycle")
		}
		seen[cur] = true
	}
	return nil
}

func (s *ModelProfileService) validateProfileInput(in ModelProfileInput) (name, description string, err error) {
	name, description = strings.TrimSpace(in.Name), strings.TrimSpace(in.Description)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return "", "", invalid("name must be 1 to 100 characters")
	}
	if utf8.RuneCountInString(description) > 500 {
		return "", "", invalid("description must be 500 characters or fewer")
	}
	return name, description, nil
}

// SaveProfile creates (id invalid) or replaces a profile and its rows in one
// transaction. Profile writes of a workspace are serialized so the cycle check
// sees the graph it guards.
func (s *ModelProfileService) SaveProfile(ctx context.Context, workspaceID, id, actor pgtype.UUID, in ModelProfileInput) (db.ModelProfile, error) {
	name, description, err := s.validateProfileInput(in)
	if err != nil {
		return db.ModelProfile{}, err
	}
	var saved db.ModelProfile
	err = s.withTx(ctx, func(q *db.Queries) error {
		locked, err := q.LockModelProfilesInWorkspace(ctx, workspaceID)
		if err != nil {
			return err
		}
		edges := map[string]string{}
		exists := map[string]bool{}
		for _, p := range locked {
			exists[util.UUIDToString(p.ID)] = true
			edges[util.UUIDToString(p.ID)] = util.UUIDToString(p.WhenExhaustedProfileID)
		}
		if id.Valid && !exists[util.UUIDToString(id)] {
			return ErrModelProfileNotFound
		}
		if !id.Valid {
			id = dbid.NewV7()
		}
		var exhausted pgtype.UUID
		if in.WhenExhaustedProfileID != nil && strings.TrimSpace(*in.WhenExhaustedProfileID) != "" {
			exhausted, err = util.ParseUUID(strings.TrimSpace(*in.WhenExhaustedProfileID))
			if err != nil || !exists[util.UUIDToString(exhausted)] {
				return invalid("when_exhausted_profile_id is not a profile of this workspace")
			}
			if exhausted == id {
				return invalid("a profile cannot fall back to itself")
			}
		}
		edges[util.UUIDToString(id)] = util.UUIDToString(exhausted)
		if err := checkExhaustedChain(util.UUIDToString(id), edges); err != nil {
			return err
		}
		options, err := normalizeOptions(ctx, q, workspaceID, in.Options, nil)
		if err != nil {
			return err
		}
		if exists[util.UUIDToString(id)] {
			saved, err = q.UpdateModelProfile(ctx, db.UpdateModelProfileParams{ID: id, WorkspaceID: workspaceID, Name: name, Description: description, WhenExhaustedProfileID: exhausted})
		} else {
			saved, err = q.CreateModelProfile(ctx, db.CreateModelProfileParams{ID: id, WorkspaceID: workspaceID, Name: name, Description: description, WhenExhaustedProfileID: exhausted, CreatedBy: actor})
		}
		if isUniqueViolation(err) {
			return ErrModelProfileConflict
		}
		if err != nil {
			return err
		}
		if err := q.DeleteModelProfileOptions(ctx, id); err != nil {
			return err
		}
		for _, o := range options {
			if err := q.InsertModelProfileOption(ctx, db.InsertModelProfileOptionParams{
				WorkspaceID: workspaceID, ProfileID: id, Position: o.Position, RuntimeID: util.MustParseUUID(o.RuntimeID),
				Model: o.Model, ThinkingLevel: o.ThinkingLevel, CustomArgs: encodeArgs(o.CustomArgs),
			}); err != nil {
				return err
			}
		}
		return nil
	})
	return saved, err
}

// DeleteProfile removes a profile nobody references: no agent, no open issue
// and no other profile's when-exhausted. Closed issues are detached.
func (s *ModelProfileService) DeleteProfile(ctx context.Context, workspaceID, id pgtype.UUID) error {
	return s.withTx(ctx, func(q *db.Queries) error {
		if _, err := q.LockModelProfilesInWorkspace(ctx, workspaceID); err != nil {
			return err
		}
		if _, err := q.GetModelProfileInWorkspace(ctx, db.GetModelProfileInWorkspaceParams{ID: id, WorkspaceID: workspaceID}); errors.Is(err, pgx.ErrNoRows) {
			return ErrModelProfileNotFound
		} else if err != nil {
			return err
		}
		refs, err := q.CountModelProfileReferences(ctx, db.CountModelProfileReferencesParams{WorkspaceID: workspaceID, ProfileID: id})
		if err != nil {
			return err
		}
		if refs.Agents > 0 || refs.OpenIssues > 0 || refs.FallbackOf > 0 {
			return fmt.Errorf("%w: %d agents, %d open issues and %d profiles use it", ErrModelProfileReferenced, refs.Agents, refs.OpenIssues, refs.FallbackOf)
		}
		if err := q.ClearClosedIssueModelProfiles(ctx, db.ClearClosedIssueModelProfilesParams{WorkspaceID: workspaceID, ModelProfileID: id}); err != nil {
			return err
		}
		if err := q.DeleteModelProfileOptions(ctx, id); err != nil {
			return err
		}
		_, err = q.DeleteModelProfile(ctx, db.DeleteModelProfileParams{ID: id, WorkspaceID: workspaceID})
		return err
	})
}

// AgentModelsInput changes an agent's model settings; nil fields are kept.
// Options: an empty list removes the agent's own list (back on its profile).
type AgentModelsInput struct {
	ModelProfileID  *string             `json:"model_profile_id"`
	ClearProfile    bool                `json:"-"`
	Options         *[]ModelOptionInput `json:"options"`
	DisallowedTools *[]string           `json:"disallowed_tools"`
}

// SaveAgentModels applies the change in one transaction.
func (s *ModelProfileService) SaveAgentModels(ctx context.Context, agent db.Agent, in AgentModelsInput) (db.Agent, error) {
	updated := agent
	err := s.withTx(ctx, func(q *db.Queries) error {
		if in.ModelProfileID != nil || in.ClearProfile {
			var profileID pgtype.UUID
			if in.ModelProfileID != nil && strings.TrimSpace(*in.ModelProfileID) != "" {
				id, err := util.ParseUUID(strings.TrimSpace(*in.ModelProfileID))
				if err != nil {
					return invalid("model_profile_id is not a UUID")
				}
				if _, err := q.GetModelProfileInWorkspace(ctx, db.GetModelProfileInWorkspaceParams{ID: id, WorkspaceID: agent.WorkspaceID}); errors.Is(err, pgx.ErrNoRows) {
					return invalid("model_profile_id is not a profile of this workspace")
				} else if err != nil {
					return err
				}
				profileID = id
			}
			a, err := q.SetAgentModelProfile(ctx, db.SetAgentModelProfileParams{ID: agent.ID, WorkspaceID: agent.WorkspaceID, ModelProfileID: profileID})
			if err != nil {
				return err
			}
			updated = a
		}
		if in.Options != nil {
			options, err := normalizeOptions(ctx, q, agent.WorkspaceID, *in.Options, &agent.OwnerID)
			if err != nil {
				return err
			}
			if err := q.DeleteAgentModelOptions(ctx, agent.ID); err != nil {
				return err
			}
			for _, o := range options {
				if err := q.InsertAgentModelOption(ctx, db.InsertAgentModelOptionParams{
					WorkspaceID: agent.WorkspaceID, AgentID: agent.ID, Position: o.Position, RuntimeID: util.MustParseUUID(o.RuntimeID),
					Model: o.Model, ThinkingLevel: o.ThinkingLevel, CustomArgs: encodeArgs(o.CustomArgs),
				}); err != nil {
					return err
				}
			}
		}
		if in.DisallowedTools != nil {
			if len(*in.DisallowedTools) > maxDisallowedTools {
				return invalid("at most %d disallowed tools", maxDisallowedTools)
			}
			tools, err := NormalizeDisallowedTools(*in.DisallowedTools)
			if err != nil {
				return invalid("%s", err.Error())
			}
			raw, _ := json.Marshal(tools)
			a, err := q.SetAgentDisallowedTools(ctx, db.SetAgentDisallowedToolsParams{ID: agent.ID, WorkspaceID: agent.WorkspaceID, DisallowedTools: raw})
			if err != nil {
				return err
			}
			updated = a
		}
		return nil
	})
	return updated, err
}

// SetIssueProfile sets or clears (invalid id) the issue's profile.
func (s *ModelProfileService) SetIssueProfile(ctx context.Context, issue db.Issue, profileID pgtype.UUID) (db.Issue, error) {
	if profileID.Valid {
		if _, err := s.Queries.GetModelProfileInWorkspace(ctx, db.GetModelProfileInWorkspaceParams{ID: profileID, WorkspaceID: issue.WorkspaceID}); errors.Is(err, pgx.ErrNoRows) {
			return db.Issue{}, invalid("model_profile_id is not a profile of this workspace")
		} else if err != nil {
			return db.Issue{}, err
		}
	}
	return s.Queries.SetIssueModelProfile(ctx, db.SetIssueModelProfileParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID, ModelProfileID: profileID})
}

// ModelWorld is the workspace state a choice reads: profiles with their rows,
// runtimes with their availability. Load it once to choose for many agents.
type ModelWorld struct {
	Profiles map[string]ModelProfileData
	Runtimes map[string]RuntimeAvailability
	Records  []db.RuntimeUnavailable
}

// runtimeOnline mirrors the claim's freshness rule.
func runtimeOnline(rt db.AgentRuntime, now time.Time) bool {
	return rt.Status == "online" && rt.LastSeenAt.Valid && now.Sub(rt.LastSeenAt.Time) <= time.Duration(RuntimeClaimFreshnessSeconds*float64(time.Second))
}

// LoadModelWorld reads every profile, runtime and active unavailability of a
// workspace.
func LoadModelWorld(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID, now time.Time) (ModelWorld, error) {
	w := ModelWorld{Profiles: map[string]ModelProfileData{}, Runtimes: map[string]RuntimeAvailability{}}
	profiles, err := q.ListModelProfiles(ctx, workspaceID)
	if err != nil {
		return w, err
	}
	for _, p := range profiles {
		w.Profiles[util.UUIDToString(p.ID)] = ModelProfileData{ID: util.UUIDToString(p.ID), Name: p.Name, WhenExhaustedProfileID: util.UUIDToString(p.WhenExhaustedProfileID)}
	}
	rows, err := q.ListModelProfileOptionsByWorkspace(ctx, workspaceID)
	if err != nil {
		return w, err
	}
	for _, r := range rows {
		key := util.UUIDToString(r.ProfileID)
		if p, ok := w.Profiles[key]; ok {
			p.Options = append(p.Options, profileOptionFromRow(r))
			w.Profiles[key] = p
		}
	}
	runtimes, err := q.ListAgentRuntimes(ctx, workspaceID)
	if err != nil {
		return w, err
	}
	for _, rt := range runtimes {
		w.Runtimes[util.UUIDToString(rt.ID)] = RuntimeAvailability{
			Name: runtimeDisplayName(rt), Provider: rt.Provider, Online: runtimeOnline(rt, now),
			Private: rt.Visibility == "private", OwnerID: util.UUIDToString(rt.OwnerID),
		}
	}
	records, err := q.ListActiveRuntimeUnavailable(ctx, db.ListActiveRuntimeUnavailableParams{WorkspaceID: workspaceID, Now: pgtype.Timestamptz{Time: now, Valid: true}})
	if err != nil {
		return w, err
	}
	w.Records = records
	for _, rec := range records {
		key := util.UUIDToString(rec.RuntimeID)
		rt, ok := w.Runtimes[key]
		if !ok || !rec.Until.Valid || !rec.Until.Time.After(now) {
			continue
		}
		if rec.Until.Time.After(rt.UnavailableUntil) {
			rt.UnavailableUntil, rt.UnavailableWhy = rec.Until.Time, rec.Reason
			w.Runtimes[key] = rt
		}
	}
	return w, nil
}

// ChoiceAgent reads the agent's fields and own list for a choice.
func ChoiceAgent(ctx context.Context, q *db.Queries, agent db.Agent) (ModelChoiceAgent, error) {
	a := ModelChoiceAgent{
		ID: util.UUIDToString(agent.ID), OwnerID: util.UUIDToString(agent.OwnerID), RuntimeID: util.UUIDToString(agent.RuntimeID),
		Model: agent.Model.String, ThinkingLevel: agent.ThinkingLevel.String, ModelProfileID: util.UUIDToString(agent.ModelProfileID),
	}
	if agent.CustomArgs != nil {
		_ = json.Unmarshal(agent.CustomArgs, &a.CustomArgs)
	}
	if agent.DisallowedTools != nil {
		_ = json.Unmarshal(agent.DisallowedTools, &a.DisallowedTools)
	}
	rows, err := q.ListAgentModelOptions(ctx, agent.ID)
	if err != nil {
		return a, err
	}
	for _, r := range rows {
		a.Options = append(a.Options, agentOptionFromRow(r))
	}
	return a, nil
}

// Input assembles the choice input of one agent and, optionally, one issue.
func (w ModelWorld) Input(agent ModelChoiceAgent, issueID, issueProfileID pgtype.UUID) ModelChoiceInput {
	return ModelChoiceInput{
		Agent: agent, IssueID: util.UUIDToString(issueID), IssueProfileID: util.UUIDToString(issueProfileID),
		Profiles: w.Profiles, Runtimes: w.Runtimes,
	}
}

// PreviewChoice is what the server would choose for the agent's next run on
// the issue (or on no issue) now.
func PreviewChoice(ctx context.Context, q *db.Queries, agent db.Agent, issue *db.Issue, now time.Time) (ModelChoice, error) {
	return previewChoice(ctx, q, agent, issue, conveyorRunModel{}, now)
}

// previewChoice is PreviewChoice for a run of a route step: the node's
// profile wins over the issue's, and the node may rule out a model family.
func previewChoice(ctx context.Context, q *db.Queries, agent db.Agent, issue *db.Issue, route conveyorRunModel, now time.Time) (ModelChoice, error) {
	world, err := LoadModelWorld(ctx, q, agent.WorkspaceID, now)
	if err != nil {
		return ModelChoice{}, err
	}
	a, err := ChoiceAgent(ctx, q, agent)
	if err != nil {
		return ModelChoice{}, err
	}
	var issueID, issueProfile pgtype.UUID
	if issue != nil {
		issueID, issueProfile = issue.ID, issue.ModelProfileID
	}
	if route.ProfileID.Valid {
		issueProfile = route.ProfileID
	}
	if isAcceptCarrier(agent) {
		// Acceptance runs no model: no profile or list may move it off the
		// daemon's accept runtime onto a model runtime (STT-358).
		issueProfile, route = pgtype.UUID{}, conveyorRunModel{}
		a.Options, a.ModelProfileID = nil, ""
	}
	in := world.Input(a, issueID, issueProfile)
	in.ExcludeFamily = route.ExcludeFamily
	return ChooseModel(in, now), nil
}
