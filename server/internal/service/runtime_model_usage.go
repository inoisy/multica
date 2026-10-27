package service

import (
	"context"
	"sort"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// RuntimeProfileRef is a profile with at least one row on the runtime.
type RuntimeProfileRef struct {
	ProfileID    string
	Name         string
	OwnerAgentID string // empty for a shared profile
	Positions    []int32
}

// RuntimeServingAgent is an agent that can run on the runtime: through a
// profile that has a row there, through a profile that falls back to one that
// does, or by its own runtime field when it has no profile (the accept carrier).
type RuntimeServingAgent struct {
	AgentID string
	Name    string
	// ViaProfiles are the profiles of the agent that lead to the runtime; empty
	// for an agent bound by its field.
	ViaProfiles []string
}

// RuntimeModelUsage is who selects a runtime and who can end up on it, for
// the page of the environment. It is computed over the whole workspace; the
// caller hides what its reader may not see.
type RuntimeModelUsage struct {
	// SelectedIn are the profiles, shared and personal, that have a row here.
	SelectedIn []RuntimeProfileRef
	// FallbackProfiles reach the runtime only through their when-exhausted
	// chain; BasedOn are shared profiles with a row here that personal
	// profiles were copied from.
	FallbackProfiles []RuntimeProfileRef
	BasedOn          []RuntimeProfileRef
	Agents           []RuntimeServingAgent
	// OpenIssues is the number of open issues whose own profile reaches the runtime.
	OpenIssues int
}

// RuntimeModelUsageOf reads the references of one runtime. Only references
// that keep a run on it count; a profile without agents and issues is listed
// but blocks nothing.
func RuntimeModelUsageOf(ctx context.Context, q *db.Queries, rt db.AgentRuntime) (RuntimeModelUsage, error) {
	var out RuntimeModelUsage
	rows, err := q.ListModelProfileOptionsOfRuntime(ctx, rt.ID)
	if err != nil {
		return out, err
	}
	profiles, err := q.ListModelProfiles(ctx, rt.WorkspaceID)
	if err != nil {
		return out, err
	}
	byID := map[string]db.ModelProfile{}
	for _, p := range profiles {
		byID[util.UUIDToString(p.ID)] = p
	}
	positions := map[string][]int32{}
	for _, r := range rows {
		key := util.UUIDToString(r.ProfileID)
		positions[key] = append(positions[key], r.Position)
	}
	ref := func(id string, pos []int32) RuntimeProfileRef {
		p := byID[id]
		return RuntimeProfileRef{ProfileID: id, Name: p.Name, OwnerAgentID: util.UUIDToString(p.OwnerAgentID), Positions: pos}
	}

	// reaches[p]: the profile has a row here, or falls back to one that does.
	reaches := map[string]bool{}
	var reach func(id string, seen map[string]bool) bool
	reach = func(id string, seen map[string]bool) bool {
		if v, ok := reaches[id]; ok {
			return v
		}
		if seen[id] {
			return false
		}
		seen[id] = true
		_, direct := positions[id]
		next := util.UUIDToString(byID[id].WhenExhaustedProfileID)
		v := direct || (next != "" && reach(next, seen))
		reaches[id] = v
		return v
	}
	var reached []pgtype.UUID
	for id, p := range byID {
		if !reach(id, map[string]bool{}) {
			continue
		}
		reached = append(reached, p.ID)
		if pos, direct := positions[id]; direct {
			out.SelectedIn = append(out.SelectedIn, ref(id, pos))
		} else {
			out.FallbackProfiles = append(out.FallbackProfiles, ref(id, nil))
		}
	}
	for id, pos := range positions {
		p := byID[id]
		if p.OwnerAgentID.Valid || p.BasedOnProfileID.Valid {
			continue
		}
		for _, other := range profiles {
			if other.BasedOnProfileID == p.ID {
				out.BasedOn = append(out.BasedOn, ref(id, pos))
				break
			}
		}
	}
	for _, list := range [][]RuntimeProfileRef{out.SelectedIn, out.FallbackProfiles, out.BasedOn} {
		sort.Slice(list, func(i, j int) bool {
			return list[i].Name < list[j].Name || (list[i].Name == list[j].Name && list[i].ProfileID < list[j].ProfileID)
		})
	}

	if len(reached) > 0 {
		agents, err := q.ListActiveAgentsByModelProfiles(ctx, db.ListActiveAgentsByModelProfilesParams{WorkspaceID: rt.WorkspaceID, ProfileIds: reached})
		if err != nil {
			return out, err
		}
		for _, a := range agents {
			via := []string{util.UUIDToString(a.ModelProfileID)}
			out.Agents = append(out.Agents, RuntimeServingAgent{AgentID: util.UUIDToString(a.ID), Name: a.Name, ViaProfiles: via})
		}
		issues, err := q.ListOpenIssuesWithModelProfile(ctx, db.ListOpenIssuesWithModelProfileParams{WorkspaceID: rt.WorkspaceID})
		if err != nil {
			return out, err
		}
		for _, i := range issues {
			if reaches[util.UUIDToString(i.ModelProfileID)] {
				out.OpenIssues++
			}
		}
	}
	bound, err := q.ListActiveAgentsByRuntime(ctx, rt.ID)
	if err != nil {
		return out, err
	}
	for _, a := range bound {
		if !a.ModelProfileID.Valid {
			out.Agents = append(out.Agents, RuntimeServingAgent{AgentID: util.UUIDToString(a.ID), Name: a.Name})
		}
	}
	return out, nil
}
