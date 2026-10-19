package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The import document carries today's model state of the external control
// panel into the database (ТЗ моделей, раздел 7). It is the union of
// controls.json and the conveyor watchdog config:
//
//	{
//	  "tiers":     [{"id", "name", "description", "chain": [row...]}] | {"<id>": {...}},
//	  "providers": {"tiers": <same as tiers>, "agents": {"<agent>": [row...]}},
//	  "chains":    {"<agent>": {"tier": "<id>", "chain": [row...], "mode": "auto"|"manual", "selected": 0}},
//	  "epics":     [{"overrides": {"<ISSUE-1>": {"model", "thinking_level"}} | [{"issue", "model", "thinking_level"}]}]
//	}
//
// row: {"kind" | "runtime_id" | "runtime", "model", "thinking_level", "custom_args": null | [...]}.
// <agent> is an agent id or name. kind names a runtime of the workspace by
// provider, name or custom name; an ambiguous or unknown kind goes to the
// report. "selected" is 0-based, as the panel stores it. Tiers of controls.json
// win over providers.tiers of the same id; chains win over providers.agents.
type ModelImportDocument struct {
	Tiers     json.RawMessage             `json:"tiers"`
	Providers *modelImportProviders       `json:"providers"`
	Chains    map[string]modelImportChain `json:"chains"`
	Epics     []modelImportEpic           `json:"epics"`
}

type modelImportProviders struct {
	Tiers  json.RawMessage             `json:"tiers"`
	Agents map[string][]ModelImportRow `json:"agents"`
}

type modelImportChain struct {
	Tier     string           `json:"tier"`
	Chain    []ModelImportRow `json:"chain"`
	Mode     string           `json:"mode"`
	Selected *int             `json:"selected"`
}

type modelImportTier struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Chain       []ModelImportRow `json:"chain"`
}

type modelImportEpic struct {
	ID        string          `json:"id"`
	Overrides json.RawMessage `json:"overrides"`
}

type modelImportOverride struct {
	Issue         string `json:"issue"`
	Model         string `json:"model"`
	ThinkingLevel string `json:"thinking_level"`
}

// ModelImportRow is one row of a tier or a chain.
type ModelImportRow struct {
	Kind          string    `json:"kind"`
	RuntimeID     string    `json:"runtime_id"`
	Runtime       string    `json:"runtime"`
	Model         string    `json:"model"`
	ThinkingLevel string    `json:"thinking_level"`
	CustomArgs    *[]string `json:"custom_args"`
}

// ModelImportPlan is what the import would do (dry run) or did.
type ModelImportPlan struct {
	DryRun      bool                     `json:"dry_run"`
	Profiles    []ModelImportProfilePlan `json:"profiles"`
	Agents      []ModelImportAgentPlan   `json:"agents"`
	Issues      []ModelImportIssuePlan   `json:"issues"`
	Report      []string                 `json:"report"`
	Unexplained int                      `json:"unexplained"`
}

// ModelImportProfilePlan is one tier becoming a profile.
type ModelImportProfilePlan struct {
	Key     string        `json:"key"`
	Name    string        `json:"name"`
	Action  string        `json:"action"` // create | update | unchanged
	ID      string        `json:"id,omitempty"`
	Options []ModelOption `json:"options"`
}

// ModelImportAgentPlan is one agent's outcome.
type ModelImportAgentPlan struct {
	AgentID         string        `json:"agent_id"`
	AgentName       string        `json:"agent_name"`
	Profile         string        `json:"profile,omitempty"`
	OwnList         []ModelOption `json:"own_list"`
	OwnListWhy      string        `json:"own_list_why,omitempty"` // differs | pinned
	DisallowedTools []string      `json:"disallowed_tools"`
	Current         ModelOption   `json:"current"`
	WouldChoose     ModelChoice   `json:"would_choose"`
	Matches         bool          `json:"matches"`
	Explanation     string        `json:"explanation,omitempty"`
}

// ModelImportIssuePlan is one epic override becoming an issue profile.
type ModelImportIssuePlan struct {
	Issue   string `json:"issue"`
	Profile string `json:"profile,omitempty"`
	Note    string `json:"note,omitempty"`
}

func parseImportTiers(raw json.RawMessage) ([]modelImportTier, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var list []modelImportTier
	if err := json.Unmarshal(raw, &list); err == nil {
		return list, nil
	}
	var byID map[string]modelImportTier
	if err := json.Unmarshal(raw, &byID); err != nil {
		return nil, invalid("tiers must be a list or an object of tiers")
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		t := byID[id]
		if t.ID == "" {
			t.ID = id
		}
		list = append(list, t)
	}
	return list, nil
}

func parseImportOverrides(raw json.RawMessage) ([]modelImportOverride, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var list []modelImportOverride
	if err := json.Unmarshal(raw, &list); err == nil {
		return list, nil
	}
	var byIssue map[string]modelImportOverride
	if err := json.Unmarshal(raw, &byIssue); err != nil {
		return nil, invalid("epics[].overrides must be a list or an object keyed by issue")
	}
	keys := make([]string, 0, len(byIssue))
	for k := range byIssue {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		o := byIssue[k]
		if o.Issue == "" {
			o.Issue = k
		}
		list = append(list, o)
	}
	return list, nil
}

// SplitDisallowedArgs separates the disallowed-tool flags of claude and codex
// from the other arguments, turning the flags back into server:tool pairs.
func SplitDisallowedArgs(args []string) (tools []string, rest []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case (a == "--disallowedTools" || a == "--disallowed-tools") && i+1 < len(args):
			i++
			for _, name := range strings.FieldsFunc(args[i], func(r rune) bool { return r == ',' || r == ' ' }) {
				if server, tool, ok := strings.Cut(strings.TrimPrefix(name, "mcp__"), "__"); ok && strings.HasPrefix(name, "mcp__") {
					tools = append(tools, server+":"+tool)
				} else {
					tools = append(tools, name)
				}
			}
		case a == "-c" && i+1 < len(args) && strings.HasPrefix(args[i+1], "mcp_servers.") && strings.Contains(args[i+1], ".disabled_tools="):
			i++
			key, value, _ := strings.Cut(args[i], "=")
			server := strings.TrimSuffix(strings.TrimPrefix(key, "mcp_servers."), ".disabled_tools")
			var names []string
			if json.Unmarshal([]byte(value), &names) != nil {
				rest = append(rest, "-c", args[i])
				continue
			}
			for _, n := range names {
				tools = append(tools, server+":"+n)
			}
		default:
			rest = append(rest, a)
		}
	}
	return tools, rest
}

type importContext struct {
	runtimes []db.AgentRuntime
	report   []string
}

func (c *importContext) note(format string, args ...any) {
	c.report = append(c.report, fmt.Sprintf(format, args...))
}

// resolveRuntime maps a row to a runtime of the workspace: runtime_id first,
// then an exact name or custom name, then a unique provider.
func (c *importContext) resolveRuntime(row ModelImportRow) (string, error) {
	if row.RuntimeID != "" {
		for _, rt := range c.runtimes {
			if util.UUIDToString(rt.ID) == row.RuntimeID {
				return row.RuntimeID, nil
			}
		}
		return "", fmt.Errorf("runtime %s is not in the workspace", row.RuntimeID)
	}
	name := row.Runtime
	if name == "" {
		name = row.Kind
	}
	if name == "" {
		return "", fmt.Errorf("row names no runtime")
	}
	var byName, byProvider []db.AgentRuntime
	for _, rt := range c.runtimes {
		if rt.Name == name || (rt.CustomName.Valid && rt.CustomName.String == name) {
			byName = append(byName, rt)
		}
		if rt.Provider == name {
			byProvider = append(byProvider, rt)
		}
	}
	switch {
	case len(byName) == 1:
		return util.UUIDToString(byName[0].ID), nil
	case len(byName) > 1:
		return "", fmt.Errorf("kind %q names %d runtimes", name, len(byName))
	case len(byProvider) == 1:
		return util.UUIDToString(byProvider[0].ID), nil
	case len(byProvider) > 1:
		return "", fmt.Errorf("kind %q matches %d runtimes of that provider", name, len(byProvider))
	}
	return "", fmt.Errorf("kind %q matches no runtime", name)
}

// rows converts import rows; tools collects the disallowed tools they carry.
func (c *importContext) rows(where string, in []ModelImportRow, tools map[string]bool) ([]ModelOption, bool) {
	out := make([]ModelOption, 0, len(in))
	ok := true
	var sets []string
	for i, r := range in {
		id, err := c.resolveRuntime(r)
		if err != nil {
			c.note("%s, row %d: %v", where, i+1, err)
			ok = false
			continue
		}
		o := ModelOption{Position: int32(len(out) + 1), RuntimeID: id, Model: strings.TrimSpace(r.Model), ThinkingLevel: strings.TrimSpace(r.ThinkingLevel)}
		if r.CustomArgs != nil {
			found, rest := SplitDisallowedArgs(*r.CustomArgs)
			normalized, nerr := NormalizeDisallowedTools(found)
			if nerr != nil {
				c.note("%s, row %d: %v", where, i+1, nerr)
			}
			for _, t := range normalized {
				tools[t] = true
			}
			sets = append(sets, strings.Join(normalized, ","))
			if len(rest) > 0 {
				o.CustomArgs = *r.CustomArgs
				c.note("%s, row %d: custom_args carry more than disallowed tools (%s); kept on the row as is", where, i+1, strings.Join(rest, " "))
			}
		}
		out = append(out, o)
	}
	for i := 1; i < len(sets); i++ {
		if sets[i] != sets[0] {
			c.note("%s: rows disallow different tools (%q vs %q); the agent gets their union", where, sets[0], sets[i])
			break
		}
	}
	return out, ok
}

func sameRows(a, b []ModelOption) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].RuntimeID != b[i].RuntimeID || a[i].Model != b[i].Model || a[i].ThinkingLevel != b[i].ThinkingLevel {
			return false
		}
		if (a[i].CustomArgs == nil) != (b[i].CustomArgs == nil) || strings.Join(a[i].CustomArgs, "\x00") != strings.Join(b[i].CustomArgs, "\x00") {
			return false
		}
	}
	return true
}

func optionInputs(options []ModelOption) []ModelOptionInput {
	out := make([]ModelOptionInput, 0, len(options))
	for _, o := range options {
		in := ModelOptionInput{RuntimeID: o.RuntimeID, Model: o.Model, ThinkingLevel: o.ThinkingLevel}
		if o.CustomArgs != nil {
			args := o.CustomArgs
			in.CustomArgs = &args
		}
		out = append(out, in)
	}
	return out
}

// Import plans (dryRun) or applies the document in the workspace.
func (s *ModelProfileService) Import(ctx context.Context, workspaceID, actor pgtype.UUID, doc ModelImportDocument, dryRun bool, now time.Time) (ModelImportPlan, error) {
	plan := ModelImportPlan{DryRun: dryRun, Profiles: []ModelImportProfilePlan{}, Agents: []ModelImportAgentPlan{}, Issues: []ModelImportIssuePlan{}}
	runtimes, err := s.Queries.ListAgentRuntimes(ctx, workspaceID)
	if err != nil {
		return plan, err
	}
	c := &importContext{runtimes: runtimes}

	tiers, err := parseImportTiers(doc.Tiers)
	if err != nil {
		return plan, err
	}
	var agentsFromConfig map[string][]ModelImportRow
	if doc.Providers != nil {
		configTiers, err := parseImportTiers(doc.Providers.Tiers)
		if err != nil {
			return plan, err
		}
		have := map[string]bool{}
		for _, t := range tiers {
			have[t.ID] = true
		}
		for _, t := range configTiers {
			if !have[t.ID] {
				tiers = append(tiers, t)
			}
		}
		agentsFromConfig = doc.Providers.Agents
	}

	// Profiles: one per tier, matched to an existing profile by name.
	existing, err := s.Queries.ListModelProfiles(ctx, workspaceID)
	if err != nil {
		return plan, err
	}
	existingByName := map[string]db.ModelProfile{}
	for _, p := range existing {
		existingByName[strings.ToLower(p.Name)] = p
	}
	world, err := LoadModelWorld(ctx, s.Queries, workspaceID, now)
	if err != nil {
		return plan, err
	}
	profileByKey := map[string]ModelProfileData{}
	profileIDByKey := map[string]string{}
	for _, t := range tiers {
		name := strings.TrimSpace(t.Name)
		if name == "" {
			name = t.ID
		}
		options, _ := c.rows("tier "+t.ID, t.Chain, map[string]bool{})
		for i := range options {
			options[i].CustomArgs = nil // a profile's rows derive their args per agent
		}
		pp := ModelImportProfilePlan{Key: t.ID, Name: name, Action: "create", Options: options}
		if p, ok := existingByName[strings.ToLower(name)]; ok {
			pp.ID = util.UUIDToString(p.ID)
			pp.Action = "update"
			if sameRows(world.Profiles[pp.ID].Options, options) && p.Description == strings.TrimSpace(t.Description) {
				pp.Action = "unchanged"
			}
		}
		if !dryRun && pp.Action != "unchanged" {
			id := pgtype.UUID{}
			if pp.ID != "" {
				id = util.MustParseUUID(pp.ID)
			}
			saved, err := s.SaveProfile(ctx, workspaceID, id, actor, ModelProfileInput{Name: name, Description: t.Description, Options: optionInputs(options)})
			if err != nil {
				return plan, fmt.Errorf("tier %s: %w", t.ID, err)
			}
			pp.ID = util.UUIDToString(saved.ID)
		}
		key := pp.ID
		if key == "" {
			key = "import:" + t.ID
		}
		profileIDByKey[t.ID] = key
		profileByKey[t.ID] = ModelProfileData{ID: key, Name: name, Options: options}
		plan.Profiles = append(plan.Profiles, pp)
	}
	// The world the server would choose in after the import.
	after := ModelWorld{Profiles: map[string]ModelProfileData{}, Runtimes: world.Runtimes}
	for k, v := range world.Profiles {
		after.Profiles[k] = v
	}
	for _, p := range profileByKey {
		after.Profiles[p.ID] = p
	}

	agents, err := s.Queries.ListAgents(ctx, workspaceID)
	if err != nil {
		return plan, err
	}
	findAgent := func(key string) (db.Agent, bool) {
		for _, a := range agents {
			if util.UUIDToString(a.ID) == key || a.Name == key {
				return a, true
			}
		}
		return db.Agent{}, false
	}
	keys := map[string]bool{}
	for k := range doc.Chains {
		keys[k] = true
	}
	for k := range agentsFromConfig {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, key := range sorted {
		agent, ok := findAgent(key)
		if !ok {
			c.note("agent %q: not found in the workspace", key)
			continue
		}
		chain, hasChain := doc.Chains[key]
		rowsIn := chain.Chain
		if !hasChain || len(rowsIn) == 0 {
			rowsIn = agentsFromConfig[key]
		}
		where := "agent " + agent.Name
		tools := map[string]bool{}
		options, _ := c.rows(where, rowsIn, tools)
		ap := ModelImportAgentPlan{AgentID: util.UUIDToString(agent.ID), AgentName: agent.Name, OwnList: []ModelOption{}}
		ap.Current = ModelOption{Position: 1, RuntimeID: util.UUIDToString(agent.RuntimeID), Model: agent.Model.String, ThinkingLevel: agent.ThinkingLevel.String}
		var profile *ModelProfileData
		if chain.Tier != "" {
			if p, ok := profileByKey[chain.Tier]; ok {
				profile = &p
				ap.Profile = p.Name
			} else {
				c.note("%s: tier %q is not in the document", where, chain.Tier)
			}
		}
		// Rows equal to the profile's (ignoring their args, which become the
		// agent's disallowed tools) leave the agent on the profile alone.
		stripped := make([]ModelOption, len(options))
		for i, o := range options {
			stripped[i] = o
			stripped[i].CustomArgs = nil
		}
		switch {
		case profile != nil && len(options) > 0 && !sameRows(stripped, profile.Options):
			ap.OwnList, ap.OwnListWhy = options, "differs"
			c.note("%s: chain differs from tier %q; own list kept — decide whether to keep it", where, chain.Tier)
		case profile == nil && len(options) > 0:
			ap.OwnList, ap.OwnListWhy = options, "differs"
			c.note("%s: chain without a tier; becomes the agent's own list", where)
		}
		if chain.Mode == "manual" && chain.Selected != nil {
			src := options
			if len(src) == 0 && profile != nil {
				src = profile.Options
			}
			n := *chain.Selected
			if n >= 0 && n < len(src) {
				pinned := src[n]
				pinned.Position = 1
				ap.OwnList, ap.OwnListWhy = []ModelOption{pinned}, "pinned"
				c.note("%s: pinned to row %d; own list of that one row", where, n+1)
			} else {
				c.note("%s: pinned to row %d, which does not exist", where, n+1)
			}
		}
		ap.DisallowedTools = []string{}
		for t := range tools {
			ap.DisallowedTools = append(ap.DisallowedTools, t)
		}
		sort.Strings(ap.DisallowedTools)

		ca := ModelChoiceAgent{ID: ap.AgentID, OwnerID: util.UUIDToString(agent.OwnerID), RuntimeID: ap.Current.RuntimeID, Model: ap.Current.Model,
			ThinkingLevel: ap.Current.ThinkingLevel, DisallowedTools: ap.DisallowedTools, Options: ap.OwnList}
		if profile != nil {
			ca.ModelProfileID = profile.ID
		}
		ap.WouldChoose = ChooseModel(ModelChoiceInput{Agent: ca, Profiles: after.Profiles, Runtimes: after.Runtimes}, now)
		ap.Matches = ap.WouldChoose.Chosen != nil && ap.WouldChoose.Chosen.RuntimeID == ap.Current.RuntimeID &&
			ap.WouldChoose.Chosen.Model == ap.Current.Model && ap.WouldChoose.Chosen.ThinkingLevel == ap.Current.ThinkingLevel
		if !ap.Matches {
			ap.Explanation = explainMismatch(ap)
			if ap.Explanation == "" {
				plan.Unexplained++
				c.note("%s: runs %s now, the server would choose %s — unexplained", where, optionLabel(ap.Current, world), choiceLabel(ap.WouldChoose))
			}
		}
		if !dryRun {
			in := AgentModelsInput{DisallowedTools: &ap.DisallowedTools}
			own := optionInputs(ap.OwnList)
			in.Options = &own
			if profile != nil {
				in.ModelProfileID = &profile.ID
			}
			if _, err := s.SaveAgentModels(ctx, agent, in); err != nil {
				return plan, fmt.Errorf("%s: %w", where, err)
			}
		}
		plan.Agents = append(plan.Agents, ap)
	}

	// Epic overrides of open issues become issue profiles.
	if len(doc.Epics) > 0 {
		ws, err := s.Queries.GetWorkspace(ctx, workspaceID)
		if err != nil {
			return plan, err
		}
		for _, e := range doc.Epics {
			overrides, err := parseImportOverrides(e.Overrides)
			if err != nil {
				return plan, err
			}
			for _, o := range overrides {
				ip := ModelImportIssuePlan{Issue: o.Issue}
				issue, ok, err := s.findIssue(ctx, workspaceID, ws.IssuePrefix, o.Issue)
				if err != nil {
					return plan, err
				}
				if !ok {
					ip.Note = "issue not found"
					plan.Issues = append(plan.Issues, ip)
					continue
				}
				open, err := wakeupIssueActive(ctx, s.Queries, issue)
				if err != nil {
					return plan, err
				}
				if !open {
					ip.Note = "closed; not carried over"
					plan.Issues = append(plan.Issues, ip)
					continue
				}
				match := ""
				for _, t := range tiers {
					p := profileByKey[t.ID]
					if len(p.Options) > 0 && p.Options[0].Model == o.Model && p.Options[0].ThinkingLevel == o.ThinkingLevel {
						match = t.ID
						break
					}
				}
				if match == "" {
					ip.Note = fmt.Sprintf("no profile starts with %s·%s", o.Model, o.ThinkingLevel)
					c.note("issue %s: override %s·%s matches no profile", o.Issue, o.Model, o.ThinkingLevel)
					plan.Issues = append(plan.Issues, ip)
					continue
				}
				ip.Profile = profileByKey[match].Name
				if !dryRun {
					if _, err := s.SetIssueProfile(ctx, issue, util.MustParseUUID(profileIDByKey[match])); err != nil {
						return plan, fmt.Errorf("issue %s: %w", o.Issue, err)
					}
				}
				plan.Issues = append(plan.Issues, ip)
			}
		}
	}
	plan.Report = c.report
	if plan.Report == nil {
		plan.Report = []string{}
	}
	return plan, nil
}

// findIssue resolves "PREFIX-N", a bare number or a UUID.
func (s *ModelProfileService) findIssue(ctx context.Context, workspaceID pgtype.UUID, prefix, key string) (db.Issue, bool, error) {
	key = strings.TrimSpace(key)
	if id, err := util.ParseUUID(key); err == nil {
		issue, err := s.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: id, WorkspaceID: workspaceID})
		if err != nil {
			return db.Issue{}, false, nil
		}
		return issue, true, nil
	}
	num := strings.TrimPrefix(strings.ToUpper(key), strings.ToUpper(prefix)+"-")
	n, err := strconv.Atoi(num)
	if err != nil {
		return db.Issue{}, false, nil
	}
	issue, err := s.Queries.GetIssueByNumber(ctx, db.GetIssueByNumberParams{WorkspaceID: workspaceID, Number: int32(n)})
	if err != nil {
		return db.Issue{}, false, nil
	}
	return issue, true, nil
}

// explainMismatch names a known reason why the server would run another row
// than the agent runs today; "" when there is none.
func explainMismatch(ap ModelImportAgentPlan) string {
	if ap.WouldChoose.Chosen == nil {
		return "no row is available now: " + ap.WouldChoose.Summary()
	}
	for _, r := range ap.WouldChoose.Rows {
		if r.RuntimeID == ap.Current.RuntimeID && r.Model == ap.Current.Model && r.ThinkingLevel == ap.Current.ThinkingLevel {
			if r.Chosen {
				return ""
			}
			if r.Position < ap.WouldChoose.Position {
				return fmt.Sprintf("today's model is row %d, which is %s now", r.Position, r.State)
			}
			return fmt.Sprintf("today's model is fallback row %d; the server returns to row %d, which is available", r.Position, ap.WouldChoose.Position)
		}
	}
	return ""
}

func optionLabel(o ModelOption, w ModelWorld) string {
	return strings.Join(nonEmpty(w.Runtimes[o.RuntimeID].Name, o.Model, o.ThinkingLevel), "·")
}

func choiceLabel(c ModelChoice) string {
	if c.Chosen == nil {
		return "nothing (" + c.Summary() + ")"
	}
	return strings.Join(nonEmpty(c.RuntimeName, c.Chosen.Model, c.Chosen.ThinkingLevel), "·")
}
