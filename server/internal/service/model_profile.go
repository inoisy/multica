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

// SaveProfile creates (id invalid) or replaces a shared profile and its rows in
// one transaction. Profile writes of a workspace are serialized so the cycle
// check sees the graph it guards. A personal profile is not written here: only
// its agent's settings change it.
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
		shared := map[string]bool{}
		for _, p := range locked {
			key := util.UUIDToString(p.ID)
			exists[key] = true
			shared[key] = !p.OwnerAgentID.Valid
			edges[key] = util.UUIDToString(p.WhenExhaustedProfileID)
		}
		if id.Valid && (!exists[util.UUIDToString(id)] || !shared[util.UUIDToString(id)]) {
			return ErrModelProfileNotFound
		}
		if !id.Valid {
			id = dbid.NewV7()
		}
		exhausted, err := parseExhaustedProfile(in.WhenExhaustedProfileID, id, shared)
		if err != nil {
			return err
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
		return replaceProfileOptions(ctx, q, workspaceID, id, options)
	})
	return saved, err
}

// parseExhaustedProfile reads when_exhausted_profile_id: it must be a shared
// profile of the workspace other than the profile itself.
func parseExhaustedProfile(raw *string, self pgtype.UUID, shared map[string]bool) (pgtype.UUID, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return pgtype.UUID{}, nil
	}
	id, err := util.ParseUUID(strings.TrimSpace(*raw))
	if err != nil || !shared[util.UUIDToString(id)] {
		return pgtype.UUID{}, invalid("when_exhausted_profile_id is not a shared profile of this workspace")
	}
	if id == self {
		return pgtype.UUID{}, invalid("a profile cannot fall back to itself")
	}
	return id, nil
}

func replaceProfileOptions(ctx context.Context, q *db.Queries, workspaceID, profileID pgtype.UUID, options []ModelOption) error {
	if err := q.DeleteModelProfileOptions(ctx, profileID); err != nil {
		return err
	}
	for _, o := range options {
		if err := q.InsertModelProfileOption(ctx, db.InsertModelProfileOptionParams{
			WorkspaceID: workspaceID, ProfileID: profileID, Position: o.Position, RuntimeID: util.MustParseUUID(o.RuntimeID),
			Model: o.Model, ThinkingLevel: o.ThinkingLevel, CustomArgs: encodeArgs(o.CustomArgs),
		}); err != nil {
			return err
		}
	}
	return nil
}

// DeleteProfile removes a shared profile nobody references: no agent, no open
// issue and no other profile's when-exhausted. Closed issues are detached.
// Personal profiles made from it keep their rows and their agents and forget
// the base.
func (s *ModelProfileService) DeleteProfile(ctx context.Context, workspaceID, id pgtype.UUID) error {
	return s.withTx(ctx, func(q *db.Queries) error {
		if _, err := q.LockModelProfilesInWorkspace(ctx, workspaceID); err != nil {
			return err
		}
		if _, err := q.GetSharedModelProfileInWorkspace(ctx, db.GetSharedModelProfileInWorkspaceParams{ID: id, WorkspaceID: workspaceID}); errors.Is(err, pgx.ErrNoRows) {
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
		if err := q.ClearPersonalModelProfileBase(ctx, db.ClearPersonalModelProfileBaseParams{WorkspaceID: workspaceID, BasedOnProfileID: id}); err != nil {
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
//
//   - ModelProfileID / ClearProfile select a shared profile (or none). A
//     personal profile the agent has is kept, unused, until it is reset.
//   - Options writes the rows of the agent's personal profile, creating it
//     from the profile the agent is on; an empty list returns the agent to the
//     profile its personal one was based on (this is what older clients send
//     for "no own list").
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
		if _, err := q.LockModelProfilesInWorkspace(ctx, agent.WorkspaceID); err != nil {
			return err
		}
		if in.ModelProfileID != nil || in.ClearProfile {
			var profileID pgtype.UUID
			if in.ModelProfileID != nil && strings.TrimSpace(*in.ModelProfileID) != "" {
				id, err := util.ParseUUID(strings.TrimSpace(*in.ModelProfileID))
				if err != nil {
					return invalid("model_profile_id is not a UUID")
				}
				p, err := q.GetModelProfileInWorkspace(ctx, db.GetModelProfileInWorkspaceParams{ID: id, WorkspaceID: agent.WorkspaceID})
				if errors.Is(err, pgx.ErrNoRows) {
					return invalid("model_profile_id is not a profile of this workspace")
				} else if err != nil {
					return err
				}
				if p.OwnerAgentID.Valid && p.OwnerAgentID != agent.ID {
					return invalid("model_profile_id is the personal profile of another agent")
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
			a, err := s.applyOwnList(ctx, q, updated, *in.Options)
			if err != nil {
				return err
			}
			updated = a
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

// applyOwnList is the older client's "own list": rows go to the personal
// profile, none returns the agent to the shared one.
func (s *ModelProfileService) applyOwnList(ctx context.Context, q *db.Queries, agent db.Agent, rows []ModelOptionInput) (db.Agent, error) {
	if len(rows) == 0 {
		return resetPersonalProfile(ctx, q, agent)
	}
	return savePersonalProfile(ctx, q, agent, PersonalProfileInput{Options: rows})
}

// PersonalProfileInput creates or replaces the personal profile of an agent.
// FromProfileID, when set, names the shared profile it is made from (and the
// base it is compared with); otherwise the profile the agent is on stays the
// base. WhenExhaustedProfileID is kept when nil.
type PersonalProfileInput struct {
	Options                []ModelOptionInput `json:"options"`
	FromProfileID          *string            `json:"from_profile_id"`
	WhenExhaustedProfileID *string            `json:"when_exhausted_profile_id"`
}

// SetPersonalProfile writes the agent's personal profile and points the agent
// at it.
func (s *ModelProfileService) SetPersonalProfile(ctx context.Context, agent db.Agent, in PersonalProfileInput) (db.Agent, error) {
	updated := agent
	err := s.withTx(ctx, func(q *db.Queries) error {
		if _, err := q.LockModelProfilesInWorkspace(ctx, agent.WorkspaceID); err != nil {
			return err
		}
		a, err := savePersonalProfile(ctx, q, agent, in)
		updated = a
		return err
	})
	return updated, err
}

// SetSingleModel is the personal profile of one row: the agent runs that model.
func (s *ModelProfileService) SetSingleModel(ctx context.Context, agent db.Agent, row ModelOptionInput) (db.Agent, error) {
	return s.SetPersonalProfile(ctx, agent, PersonalProfileInput{Options: []ModelOptionInput{row}})
}

// ResetPersonalProfile returns the agent to the shared profile its personal one
// was based on and deletes the personal profile, in one transaction. An agent
// without a personal profile is left as it is.
func (s *ModelProfileService) ResetPersonalProfile(ctx context.Context, agent db.Agent) (db.Agent, error) {
	updated := agent
	err := s.withTx(ctx, func(q *db.Queries) error {
		if _, err := q.LockModelProfilesInWorkspace(ctx, agent.WorkspaceID); err != nil {
			return err
		}
		a, err := resetPersonalProfile(ctx, q, agent)
		updated = a
		return err
	})
	return updated, err
}

// personalProfileName is "<agent> · личный", with the start of the agent's id
// added when the lower-cased name is taken in the workspace.
func personalProfileName(ctx context.Context, q *db.Queries, agent db.Agent) (string, error) {
	name := truncateRunes(strings.TrimSpace(agent.Name), 70) + " · личный"
	_, err := q.GetModelProfileByNameInWorkspace(ctx, db.GetModelProfileByNameInWorkspaceParams{WorkspaceID: agent.WorkspaceID, Name: name})
	if errors.Is(err, pgx.ErrNoRows) {
		return name, nil
	}
	if err != nil {
		return "", err
	}
	return truncateRunes(strings.TrimSpace(agent.Name), 60) + " · личный · " + util.UUIDToString(agent.ID)[:8], nil
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func savePersonalProfile(ctx context.Context, q *db.Queries, agent db.Agent, in PersonalProfileInput) (db.Agent, error) {
	if len(in.Options) == 0 {
		return agent, invalid("a personal profile needs at least one model")
	}
	options, err := normalizeOptions(ctx, q, agent.WorkspaceID, in.Options, &agent.OwnerID)
	if err != nil {
		return agent, err
	}
	// The base: the profile named, else the shared profile the agent is on,
	// else the one the personal profile already had.
	var base pgtype.UUID
	existing, err := q.GetPersonalModelProfileOfAgent(ctx, agent.ID)
	hasPersonal := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return agent, err
	}
	if hasPersonal {
		base = existing.BasedOnProfileID
	}
	switch {
	case in.FromProfileID != nil && strings.TrimSpace(*in.FromProfileID) != "":
		id, err := util.ParseUUID(strings.TrimSpace(*in.FromProfileID))
		if err != nil {
			return agent, invalid("from_profile_id is not a UUID")
		}
		if _, err := q.GetSharedModelProfileInWorkspace(ctx, db.GetSharedModelProfileInWorkspaceParams{ID: id, WorkspaceID: agent.WorkspaceID}); errors.Is(err, pgx.ErrNoRows) {
			return agent, invalid("from_profile_id is not a shared profile of this workspace")
		} else if err != nil {
			return agent, err
		}
		base = id
	case agent.ModelProfileID.Valid && (!hasPersonal || existing.ID != agent.ModelProfileID):
		if p, err := q.GetSharedModelProfileInWorkspace(ctx, db.GetSharedModelProfileInWorkspaceParams{ID: agent.ModelProfileID, WorkspaceID: agent.WorkspaceID}); err == nil {
			base = p.ID
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return agent, err
		}
	}

	var exhausted pgtype.UUID
	keepExhausted := in.WhenExhaustedProfileID == nil
	if !keepExhausted {
		locked, err := q.ListSharedModelProfiles(ctx, agent.WorkspaceID)
		if err != nil {
			return agent, err
		}
		shared := map[string]bool{}
		for _, p := range locked {
			shared[util.UUIDToString(p.ID)] = true
		}
		if exhausted, err = parseExhaustedProfile(in.WhenExhaustedProfileID, pgtype.UUID{}, shared); err != nil {
			return agent, err
		}
	} else if hasPersonal {
		exhausted = existing.WhenExhaustedProfileID
	}

	id := existing.ID
	if hasPersonal {
		if _, err := q.UpdateModelProfile(ctx, db.UpdateModelProfileParams{ID: id, WorkspaceID: agent.WorkspaceID, Name: existing.Name, Description: existing.Description, WhenExhaustedProfileID: exhausted}); err != nil {
			return agent, err
		}
		if err := q.SetPersonalModelProfileBase(ctx, db.SetPersonalModelProfileBaseParams{ID: id, BasedOnProfileID: base}); err != nil {
			return agent, err
		}
	} else {
		name, err := personalProfileName(ctx, q, agent)
		if err != nil {
			return agent, err
		}
		id = dbid.NewV7()
		if _, err := q.CreatePersonalModelProfile(ctx, db.CreatePersonalModelProfileParams{
			ID: id, WorkspaceID: agent.WorkspaceID, Name: name, WhenExhaustedProfileID: exhausted,
			OwnerAgentID: agent.ID, BasedOnProfileID: base,
		}); err != nil {
			if isUniqueViolation(err) {
				return agent, ErrModelProfileConflict
			}
			return agent, err
		}
	}
	if err := replaceProfileOptions(ctx, q, agent.WorkspaceID, id, options); err != nil {
		return agent, err
	}
	if agent.ModelProfileID == id {
		return agent, nil
	}
	return q.SetAgentModelProfile(ctx, db.SetAgentModelProfileParams{ID: agent.ID, WorkspaceID: agent.WorkspaceID, ModelProfileID: id})
}

func resetPersonalProfile(ctx context.Context, q *db.Queries, agent db.Agent) (db.Agent, error) {
	existing, err := q.GetPersonalModelProfileOfAgent(ctx, agent.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return agent, nil
	}
	if err != nil {
		return agent, err
	}
	updated := agent
	// The agent goes back to the profile the personal one was made from. When
	// it already points at something else it stays there.
	if agent.ModelProfileID == existing.ID {
		updated, err = q.SetAgentModelProfile(ctx, db.SetAgentModelProfileParams{ID: agent.ID, WorkspaceID: agent.WorkspaceID, ModelProfileID: existing.BasedOnProfileID})
		if err != nil {
			return agent, err
		}
	}
	if err := q.DeleteModelProfileOptions(ctx, existing.ID); err != nil {
		return updated, err
	}
	if _, err := q.DeleteModelProfile(ctx, db.DeleteModelProfileParams{ID: existing.ID, WorkspaceID: agent.WorkspaceID}); err != nil {
		return updated, err
	}
	return updated, nil
}

// SetIssueProfile sets or clears (invalid id) the issue's profile. Only a
// shared profile may be set: a personal one belongs to its agent.
func (s *ModelProfileService) SetIssueProfile(ctx context.Context, issue db.Issue, profileID pgtype.UUID) (db.Issue, error) {
	if profileID.Valid {
		if _, err := s.Queries.GetSharedModelProfileInWorkspace(ctx, db.GetSharedModelProfileInWorkspaceParams{ID: profileID, WorkspaceID: issue.WorkspaceID}); errors.Is(err, pgx.ErrNoRows) {
			return db.Issue{}, invalid("model_profile_id is not a shared profile of this workspace")
		} else if err != nil {
			return db.Issue{}, err
		}
	}
	return s.Queries.SetIssueModelProfile(ctx, db.SetIssueModelProfileParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID, ModelProfileID: profileID})
}

// RuntimeBlock is one reason a runtime cannot run now: a refusal stored when a
// run failed, or the verdict computed from the accounts of its pool.
type RuntimeBlock struct {
	RuntimeID      string
	Until          time.Time
	UntilEstimated bool
	// Code is the reason code the interface shows (the Reason* codes).
	Code string
	// StoredCode is the code of the stored refusal; empty for a computed block.
	StoredCode     string
	Reason         string
	TaskID         pgtype.UUID
	SubscriptionID pgtype.UUID
	CreatedAt      pgtype.Timestamptz
	// Computed: the block comes from the accounts, nothing is stored for it.
	Computed bool
}

// ModelWorld is the workspace state a choice reads: profiles with their rows,
// runtimes with their availability. Load it once to choose for many agents.
type ModelWorld struct {
	Profiles map[string]ModelProfileData
	Runtimes map[string]RuntimeAvailability
	// Records are the refusals stored for the workspace that still hold; Blocks
	// are what actually keeps a runtime from running, stored or computed.
	Records []db.RuntimeUnavailable
	Blocks  []RuntimeBlock
}

// runtimeOnline mirrors the claim's freshness rule.
func runtimeOnline(rt db.AgentRuntime, now time.Time) bool {
	return rt.Status == "online" && rt.LastSeenAt.Valid && now.Sub(rt.LastSeenAt.Time) <= time.Duration(RuntimeClaimFreshnessSeconds*float64(time.Second))
}

// LoadModelWorld reads every profile and runtime of a workspace and decides
// at now what keeps each runtime from running: the refusals stored for it and,
// for an environment with a pool, the state of the pool's accounts.
func LoadModelWorld(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID, now time.Time) (ModelWorld, error) {
	w := ModelWorld{Profiles: map[string]ModelProfileData{}, Runtimes: map[string]RuntimeAvailability{}}
	profiles, err := q.ListModelProfiles(ctx, workspaceID)
	if err != nil {
		return w, err
	}
	for _, p := range profiles {
		w.Profiles[util.UUIDToString(p.ID)] = ModelProfileData{
			ID: util.UUIDToString(p.ID), Name: p.Name, WhenExhaustedProfileID: util.UUIDToString(p.WhenExhaustedProfileID),
			OwnerAgentID: util.UUIDToString(p.OwnerAgentID), BasedOnProfileID: util.UUIDToString(p.BasedOnProfileID),
		}
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
	poolOf, err := runtimePools(ctx, q, workspaceID)
	if err != nil {
		return w, err
	}
	accounts, members, err := poolAccounts(ctx, q, poolOf, now)
	if err != nil {
		return w, err
	}
	records, err := q.ListActiveRuntimeUnavailable(ctx, db.ListActiveRuntimeUnavailableParams{WorkspaceID: workspaceID, Now: pgtype.Timestamptz{Time: now, Valid: true}})
	if err != nil {
		return w, err
	}
	w.Records = records
	recordsOf := map[string][]db.RuntimeUnavailable{}
	for _, rec := range records {
		recordsOf[util.UUIDToString(rec.RuntimeID)] = append(recordsOf[util.UUIDToString(rec.RuntimeID)], rec)
	}
	for _, rt := range runtimes {
		id := util.UUIDToString(rt.ID)
		ra := RuntimeAvailability{
			Name: runtimeDisplayName(rt), Provider: rt.Provider, Online: runtimeOnline(rt, now),
			Private: rt.Visibility == "private", OwnerID: util.UUIDToString(rt.OwnerID),
		}
		var pool *PoolVerdict
		if poolID := poolOf[util.UUIDToString(rt.ProfileID)]; poolID != "" {
			ra.PoolID = poolID
			pv := EvaluatePool(poolAccountList(accounts, members[poolID]), rt.DaemonID.String, now)
			pool = &pv
		}
		blocks := runtimeBlocks(id, recordsOf[id], pool, now)
		w.Blocks = append(w.Blocks, blocks...)
		if top, ok := longestBlock(blocks); ok {
			ra.UnavailableUntil, ra.UnavailableWhy = top.Until, top.Reason
			ra.UnavailableCode, ra.UnavailableEstimated = top.Code, top.UntilEstimated
		}
		w.Runtimes[id] = ra
	}
	return w, nil
}

// longestBlock is the block that keeps the runtime closed the longest.
func longestBlock(blocks []RuntimeBlock) (RuntimeBlock, bool) {
	var top RuntimeBlock
	found := false
	for _, b := range blocks {
		if !found || b.Until.After(top.Until) {
			top, found = b, true
		}
	}
	return top, found
}

// runtimeBlocks lists what keeps one runtime from running at now. Stored
// refusals of a run count by their code; the daemon's own refusals on the
// agent limit or on disabled accounts stand only while the pool still has no
// usable account; the old stored verdicts of the pool are not read; the
// pool's computed verdict stands in for them.
func runtimeBlocks(runtimeID string, stored []db.RuntimeUnavailable, pool *PoolVerdict, now time.Time) []RuntimeBlock {
	var out []RuntimeBlock
	for _, rec := range stored {
		if !rec.Until.Valid || !rec.Until.Time.After(now) || rec.SubscriptionID.Valid {
			continue
		}
		code := StoredRefusalCode(rec)
		b := RuntimeBlock{RuntimeID: runtimeID, Until: rec.Until.Time, StoredCode: code, Reason: rec.Reason, TaskID: rec.TaskID, CreatedAt: rec.CreatedAt}
		switch code {
		case UnavailablePoolVerdict:
			continue
		case UnavailableAgentCapRefusal:
			if pool != nil && pool.Usable {
				continue
			}
			b.Code = ReasonAgentCap
		case UnavailableDisabledRefusal:
			if pool != nil && pool.Usable {
				continue
			}
			b.Code = ReasonDisabled
		default:
			b.Code = ReasonProviderRefusal
		}
		out = append(out, b)
	}
	if pool != nil && pool.Blocked {
		out = append(out, RuntimeBlock{
			RuntimeID: runtimeID, Until: pool.Until, UntilEstimated: !pool.UntilKnown,
			Code: pool.Code, Reason: pool.Text, Computed: true,
		})
	}
	return out
}

// runtimePools maps the runtime profiles of a workspace to their pools.
func runtimePools(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID) (map[string]string, error) {
	profiles, err := q.ListRuntimeProfiles(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, p := range profiles {
		if p.PoolID.Valid {
			out[util.UUIDToString(p.ID)] = util.UUIDToString(p.PoolID)
		}
	}
	return out, nil
}

// poolAccounts loads the members of the pools in poolOf and their accounts.
func poolAccounts(ctx context.Context, q *db.Queries, poolOf map[string]string, now time.Time) (map[string]Account, map[string][]string, error) {
	seen := map[string]bool{}
	var poolIDs []pgtype.UUID
	for _, id := range poolOf {
		if !seen[id] {
			seen[id] = true
			poolIDs = append(poolIDs, util.MustParseUUID(id))
		}
	}
	members := map[string][]string{}
	if len(poolIDs) == 0 {
		return map[string]Account{}, members, nil
	}
	rows, err := q.ListSubscriptionPoolMembersByPools(ctx, poolIDs)
	if err != nil {
		return nil, nil, err
	}
	var accountIDs []pgtype.UUID
	seenAccount := map[string]bool{}
	for _, m := range rows {
		pool := util.UUIDToString(m.PoolID)
		account := util.UUIDToString(m.SubscriptionID)
		members[pool] = append(members[pool], account)
		if !seenAccount[account] {
			seenAccount[account] = true
			accountIDs = append(accountIDs, m.SubscriptionID)
		}
	}
	accounts, err := LoadAccounts(ctx, q, accountIDs, now)
	return accounts, members, err
}

// poolAccountList is the pool's accounts in spend order; a member whose
// account is gone is skipped.
func poolAccountList(accounts map[string]Account, ids []string) []Account {
	out := make([]Account, 0, len(ids))
	for _, id := range ids {
		if a, ok := accounts[id]; ok {
			out = append(out, a)
		}
	}
	return out
}

// ChoiceAgent reads the agent's fields for a choice. Its list comes from its
// profile, shared or personal, which the world already holds.
func ChoiceAgent(_ context.Context, _ *db.Queries, agent db.Agent) (ModelChoiceAgent, error) {
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
		a.ModelProfileID = ""
	}
	in := world.Input(a, issueID, issueProfile)
	in.ExcludeFamily = route.ExcludeFamily
	return ChooseModel(in, now), nil
}
