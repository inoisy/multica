package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issueworkflow"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const snapshotFormat = 1

// Context kinds. Together they cover every place the server asks which model
// a run gets: an agent on its own, an agent on an open issue, a run of a route
// step, a handoff of a workflow step and a queued run as it is routed.
const (
	kindAgent        = "agent"
	kindIssue        = "issue"
	kindRouteStep    = "route_step"
	kindWorkflowStep = "workflow_step"
	kindQueuedRun    = "queued_run"
)

type manifest struct {
	Format        int            `json:"format"`
	Now           string         `json:"now"`
	WorkspaceID   string         `json:"workspace_id"`
	Schema        string         `json:"schema_version"`
	Recomputed    bool           `json:"recomputed_pool_verdicts"`
	Counts        map[string]int `json:"counts"`
	ContextSetSHA string         `json:"context_set_sha256"`
}

type snapshot struct {
	Manifest manifest        `json:"manifest"`
	Contexts []contextResult `json:"contexts"`
}

// contextResult is one thing the server could be asked, with the answer.
type contextResult struct {
	Key  string `json:"key"`
	Kind string `json:"kind"`
	// Archived marks an archived agent: it never runs, its answer is still
	// compared.
	Archived bool    `json:"archived,omitempty"`
	Error    string  `json:"error,omitempty"`
	Choice   *choice `json:"choice,omitempty"`
}

type option struct {
	RuntimeID     string    `json:"runtime_id"`
	Model         string    `json:"model"`
	ThinkingLevel string    `json:"thinking_level"`
	CustomArgs    *[]string `json:"custom_args"`
}

type row struct {
	option
	ProfileID string  `json:"profile_id,omitempty"`
	State     string  `json:"state"`
	Until     *string `json:"until,omitempty"`
	// ReasonCode is written only by the algorithm after the switch; the
	// original one has no such field, so it is compared only when both have it.
	ReasonCode string `json:"reason_code,omitempty"`
	Chosen     bool   `json:"chosen"`
}

type choice struct {
	Source            string   `json:"source"`
	ProfileID         string   `json:"profile_id,omitempty"`
	ProfileName       string   `json:"profile_name,omitempty"`
	IssueID           string   `json:"issue_id,omitempty"`
	ExcludedFamily    string   `json:"excluded_family,omitempty"`
	Reason            string   `json:"reason"`
	Position          int32    `json:"position,omitempty"`
	Chosen            *option  `json:"chosen,omitempty"`
	ExhaustedProfiles []string `json:"exhausted_profiles,omitempty"`
	WaitUntil         *string  `json:"wait_until,omitempty"`
	// ProfilePersonal is read from the choice when the server reports it: the
	// agent's list came from a personal profile.
	ProfilePersonal bool  `json:"profile_personal,omitempty"`
	Rows            []row `json:"rows"`
}

// toChoice keeps only what the comparison reads, through the choice's own JSON
// so the same source builds against every revision of the algorithm.
func toChoice(c service.ModelChoice) (*choice, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	var out choice
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	// API presentation uses seconds; the acceptance comparison must retain the
	// original precision, including subsecond differences between algorithms.
	preciseTime := func(value *time.Time) *string {
		if value == nil {
			return nil
		}
		text := value.UTC().Format(time.RFC3339Nano)
		return &text
	}
	out.WaitUntil = preciseTime(c.WaitUntil)
	for i := range out.Rows {
		out.Rows[i].Until = preciseTime(c.Rows[i].Until)
	}
	if out.Rows == nil {
		out.Rows = []row{}
	}
	return &out, nil
}

type collector struct {
	q   *db.Queries
	tx  pgx.Tx
	ws  pgtype.UUID
	now time.Time
	out []contextResult
}

func (c *collector) add(kind, key string, archived bool, fn func() (service.ModelChoice, error)) {
	res := contextResult{Key: kind + "/" + key, Kind: kind, Archived: archived}
	mc, err := fn()
	if err != nil {
		res.Error = err.Error()
	} else if ch, err := toChoice(mc); err != nil {
		res.Error = err.Error()
	} else {
		res.Choice = ch
	}
	c.out = append(c.out, res)
}

func (c *collector) ids(ctx context.Context, sql string, args ...any) ([]pgtype.UUID, error) {
	rows, err := c.tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pgtype.UUID
	for rows.Next() {
		var id pgtype.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

const openIssue = `NOT (i.status IN ('done', 'cancelled') OR EXISTS(SELECT 1 FROM issue_status s WHERE s.workspace_id = i.workspace_id AND s.key = i.status AND s.category IN ('done', 'closed')))`

func collect(ctx context.Context, tx pgx.Tx, ws pgtype.UUID, now time.Time) (snapshot, error) {
	c := &collector{q: db.New(tx), tx: tx, ws: ws, now: now}

	agentIDs, err := c.ids(ctx, `SELECT id FROM agent WHERE workspace_id = $1`, ws)
	if err != nil {
		return snapshot{}, err
	}
	agents := map[string]db.Agent{}
	for _, id := range agentIDs {
		a, err := c.q.GetAgent(ctx, id)
		if err != nil {
			return snapshot{}, err
		}
		agents[util.UUIDToString(id)] = a
		c.add(kindAgent, util.UUIDToString(id), a.ArchivedAt.Valid, func() (service.ModelChoice, error) {
			return service.PreviewChoice(ctx, c.q, a, nil, now)
		})
	}

	issueIDs, err := c.ids(ctx, `SELECT i.id FROM issue i WHERE i.workspace_id = $1 AND i.assignee_type = 'agent' AND `+openIssue, ws)
	if err != nil {
		return snapshot{}, err
	}
	issues := map[string]db.Issue{}
	for _, id := range issueIDs {
		iss, err := c.q.GetIssue(ctx, id)
		if err != nil {
			return snapshot{}, err
		}
		issues[util.UUIDToString(id)] = iss
		a, ok := agents[util.UUIDToString(iss.AssigneeID)]
		if !ok {
			c.out = append(c.out, contextResult{Key: kindIssue + "/" + util.UUIDToString(id), Kind: kindIssue, Error: "assignee agent not found"})
			continue
		}
		c.add(kindIssue, util.UUIDToString(id), a.ArchivedAt.Valid, func() (service.ModelChoice, error) {
			return service.PreviewChoice(ctx, c.q, a, &iss, now)
		})
	}

	// Steps of the routes that are still running, on open issues.
	routeIssues, err := c.ids(ctx, `SELECT i.id FROM issue i JOIN issue_conveyor cv ON cv.issue_id = i.id
		WHERE i.workspace_id = $1 AND cv.status NOT IN ('completed', 'disabled') AND `+openIssue, ws)
	if err != nil {
		return snapshot{}, err
	}
	for _, id := range routeIssues {
		cv, err := c.q.GetIssueConveyor(ctx, id)
		if err != nil {
			return snapshot{}, err
		}
		iss, err := c.q.GetIssue(ctx, id)
		if err != nil {
			return snapshot{}, err
		}
		var route []struct {
			Idx     int32  `json:"idx"`
			AgentID string `json:"agent_id"`
		}
		if err := json.Unmarshal(cv.Route, &route); err != nil {
			c.out = append(c.out, contextResult{Key: kindRouteStep + "/" + util.UUIDToString(id), Kind: kindRouteStep, Error: "unreadable route"})
			continue
		}
		for _, node := range route {
			key := fmt.Sprintf("%s/%d", util.UUIDToString(id), node.Idx)
			a, ok := agents[node.AgentID]
			if !ok {
				c.out = append(c.out, contextResult{Key: kindRouteStep + "/" + key, Kind: kindRouteStep, Error: "node agent not found"})
				continue
			}
			c.add(kindRouteStep, key, a.ArchivedAt.Valid, func() (service.ModelChoice, error) {
				return service.PreviewRouteStepChoice(ctx, c.q, cv, node.Idx, a, &iss, now)
			})
		}
	}

	// Workflow steps that hand an issue to an agent, a squad's leader or the
	// workspace's acceptance carrier: the agent's answer on an issue entering
	// the step.
	workflows, err := c.q.ListIssueWorkflows(ctx, ws)
	if err != nil {
		return snapshot{}, err
	}
	for _, wf := range workflows {
		def, err := issueworkflow.Decode(wf)
		if err != nil {
			c.out = append(c.out, contextResult{Key: kindWorkflowStep + "/" + util.UUIDToString(wf.ID), Kind: kindWorkflowStep, Error: "unreadable workflow"})
			continue
		}
		for _, step := range def.Steps {
			agentID, err := c.stepAgent(ctx, step.Handler)
			if err != nil {
				return snapshot{}, err
			}
			if agentID == "" {
				continue
			}
			a, ok := agents[agentID]
			key := util.UUIDToString(wf.ID) + "/" + step.StatusKey
			if !ok {
				c.out = append(c.out, contextResult{Key: kindWorkflowStep + "/" + key, Kind: kindWorkflowStep, Error: "step agent not found"})
				continue
			}
			c.add(kindWorkflowStep, key, a.ArchivedAt.Valid, func() (service.ModelChoice, error) {
				return service.PreviewChoice(ctx, c.q, a, nil, now)
			})
		}
	}

	// Active workflow handoffs also depend on the issue's own profile. Catalog
	// previews above intentionally have no issue, so they cannot cover these
	// contexts, especially a squad handoff with no direct agent assignee yet.
	workflowIssues, err := c.ids(ctx, `SELECT i.id FROM issue i WHERE i.workspace_id = $1 AND `+openIssue, ws)
	if err != nil {
		return snapshot{}, err
	}
	for _, id := range workflowIssues {
		iss, err := c.q.GetIssue(ctx, id)
		if err != nil {
			return snapshot{}, err
		}
		def, project, err := issueworkflow.ForProject(ctx, c.q, ws, iss.ProjectID)
		if err != nil {
			return snapshot{}, err
		}
		if def == nil || !def.ControlsExecution() {
			continue
		}
		step, ok := def.Step(iss.Status)
		if !ok {
			continue
		}
		handler := step.Handler
		if kind, resolved, ok := issueworkflow.ResolveHandler(step, project, iss.CreatorType, iss.CreatorID); ok {
			handler = issueworkflow.Handler{Type: kind, ID: util.UUIDToString(resolved)}
		}
		agentID, err := c.stepAgent(ctx, handler)
		if err != nil {
			return snapshot{}, err
		}
		if agentID == "" {
			continue
		}
		key := util.UUIDToString(def.ID) + "/" + step.StatusKey + "/" + util.UUIDToString(id)
		a, ok := agents[agentID]
		if !ok {
			c.out = append(c.out, contextResult{Key: kindWorkflowStep + "/" + key, Kind: kindWorkflowStep, Error: "step agent not found"})
			continue
		}
		c.add(kindWorkflowStep, key, a.ArchivedAt.Valid, func() (service.ModelChoice, error) {
			return service.PreviewChoice(ctx, c.q, a, &iss, now)
		})
	}

	// Queued runs as the sweep routes them: they carry the step's profile and
	// the acceptance rule.
	taskIDs, err := c.ids(ctx, `SELECT t.id FROM agent_task_queue t JOIN agent a ON a.id = t.agent_id
		WHERE a.workspace_id = $1 AND t.status IN ('queued', 'deferred')`, ws)
	if err != nil {
		return snapshot{}, err
	}
	for _, id := range taskIDs {
		task, err := c.q.GetAgentTask(ctx, id)
		if err != nil {
			return snapshot{}, err
		}
		c.add(kindQueuedRun, util.UUIDToString(id), agents[util.UUIDToString(task.AgentID)].ArchivedAt.Valid, func() (service.ModelChoice, error) {
			ch, _, err := service.RouteRunChoice(ctx, c.q, task, now)
			return ch, err
		})
	}

	sort.Slice(c.out, func(i, j int) bool { return c.out[i].Key < c.out[j].Key })
	return snapshot{Contexts: c.out}, nil
}

// stepAgent is the agent a workflow handler resolves to, "" for handlers that
// are not agents.
func (c *collector) stepAgent(ctx context.Context, h issueworkflow.Handler) (string, error) {
	switch h.Type {
	case issueworkflow.HandlerAgent:
		return h.ID, nil
	case issueworkflow.HandlerSquad:
		id, err := util.ParseUUID(h.ID)
		if err != nil {
			return "", nil
		}
		var leader pgtype.UUID
		err = c.tx.QueryRow(ctx, `SELECT leader_id FROM squad WHERE id = $1 AND workspace_id = $2`, id, c.ws).Scan(&leader)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		return util.UUIDToString(leader), nil
	case issueworkflow.HandlerAccept:
		var id pgtype.UUID
		err := c.tx.QueryRow(ctx, `SELECT id FROM agent WHERE workspace_id = $1 AND kind = 'system' AND system_key = 'accept' ORDER BY created_at LIMIT 1`, c.ws).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		return util.UUIDToString(id), nil
	}
	return "", nil
}

func finishManifest(s *snapshot, m manifest) {
	m.Format = snapshotFormat
	m.Counts = map[string]int{}
	h := sha256.New()
	for _, c := range s.Contexts {
		m.Counts[c.Kind]++
		h.Write([]byte(c.Key + "\n"))
	}
	m.ContextSetSHA = hex.EncodeToString(h.Sum(nil))
	s.Manifest = m
}

func resolveWorkspace(ctx context.Context, tx pgx.Tx, ref string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if u, err := util.ParseUUID(ref); err == nil {
		err = tx.QueryRow(ctx, `SELECT id FROM workspace WHERE id = $1`, u).Scan(&id)
		return id, err
	}
	rows, err := tx.Query(ctx, `SELECT id FROM workspace WHERE lower(slug) = lower($1) OR lower(name) = lower($1)`, strings.TrimSpace(ref))
	if err != nil {
		return id, err
	}
	defer rows.Close()
	var ids []pgtype.UUID
	for rows.Next() {
		var w pgtype.UUID
		if err := rows.Scan(&w); err != nil {
			return id, err
		}
		ids = append(ids, w)
	}
	if err := rows.Err(); err != nil {
		return id, err
	}
	if len(ids) != 1 {
		return id, fmt.Errorf("workspace %q matches %d workspaces", ref, len(ids))
	}
	return ids[0], nil
}
