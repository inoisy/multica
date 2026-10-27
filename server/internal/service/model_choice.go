package service

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Where the model list of a run comes from, in the order the claim consults
// them: the issue's profile, the agent's profile (shared or personal), and
// finally the agent's plain runtime/model/thinking fields. ModelSourceAgentList
// is only read from the history of runs made before personal profiles.
const (
	ModelSourceIssueProfile = "issue_profile"
	ModelSourceAgentList    = "agent_list"
	ModelSourceAgentProfile = "agent_profile"
	ModelSourceAgentFields  = "agent_fields"
)

// Why a row was or was not chosen.
const (
	ModelReasonPrimary         = "primary"          // the first row is available
	ModelReasonFallbackRow     = "fallback_row"     // earlier rows are unavailable
	ModelReasonFallbackProfile = "fallback_profile" // every row of the profile is unavailable, its exhausted-profile is used
	ModelReasonAllUnavailable  = "all_unavailable"  // nothing can run; the run waits
	ModelReasonNoModels        = "no_models"        // the agent has no list and no runtime
)

// States of one row at the moment of choice.
const (
	ModelRowAvailable    = "available"
	ModelRowLimited      = "limited"       // runtime_unavailable until a reset
	ModelRowOffline      = "offline"       // runtime is not heartbeating
	ModelRowMissing      = "missing"       // runtime no longer exists in the workspace
	ModelRowNotPermitted = "not_permitted" // private runtime of another owner
	ModelRowSameFamily   = "same_family"   // the route asks for another model family than this row's
)

// ModelOption is one row of a model list.
type ModelOption struct {
	Position      int32    `json:"position"`
	RuntimeID     string   `json:"runtime_id"`
	Model         string   `json:"model"`
	ThinkingLevel string   `json:"thinking_level"`
	CustomArgs    []string `json:"custom_args"` // nil: derived from the agent's disallowed tools
}

// ModelProfileData is a profile as the choice needs it. A personal profile has
// an owner agent and may name the shared profile it was made from.
type ModelProfileData struct {
	ID                     string
	Name                   string
	WhenExhaustedProfileID string
	OwnerAgentID           string
	BasedOnProfileID       string
	Options                []ModelOption
}

// Personal reports whether the profile belongs to one agent.
func (p ModelProfileData) Personal() bool { return p.OwnerAgentID != "" }

// RuntimeAvailability is what the choice knows about one runtime.
type RuntimeAvailability struct {
	Name             string
	Provider         string
	Online           bool
	Private          bool
	OwnerID          string
	UnavailableUntil time.Time // zero: not limited
	UnavailableWhy   string
	// UnavailableCode says why (the Reason* codes). UnavailableEstimated is set
	// when nobody reported a reset and UnavailableUntil is only a guess.
	UnavailableCode      string
	UnavailableEstimated bool
	PoolID               string
}

// ModelChoiceAgent is the part of an agent the choice reads.
type ModelChoiceAgent struct {
	ID              string
	OwnerID         string
	RuntimeID       string
	Model           string
	ThinkingLevel   string
	CustomArgs      []string
	ModelProfileID  string
	DisallowedTools []string
	// AnyOwner shows rows as available whoever owns a private runtime: a
	// profile viewed on its own, not through an agent.
	AnyOwner bool
}

// ModelChoiceInput is everything one choice depends on. It is a pure value so
// the choice can be computed, previewed and tested without a database.
type ModelChoiceInput struct {
	Agent          ModelChoiceAgent
	IssueID        string
	IssueProfileID string
	// ExcludeFamily, when set, makes every row of that model family
	// unavailable: a route step that must judge work on another family.
	ExcludeFamily string
	Profiles      map[string]ModelProfileData
	Runtimes      map[string]RuntimeAvailability
}

// ModelRowState is one row of the list that was consulted, with its state.
type ModelRowState struct {
	ModelOption
	ProfileID   string     `json:"profile_id,omitempty"`
	RuntimeName string     `json:"runtime_name"`
	Provider    string     `json:"provider"`
	State       string     `json:"state"`
	Until       *time.Time `json:"until,omitempty"`
	// ReasonCode says why the row cannot run: the Reason* codes for a limited
	// row, the state itself for the others. UntilEstimated: Until is a guess.
	ReasonCode     string `json:"reason_code,omitempty"`
	UntilEstimated bool   `json:"until_estimated,omitempty"`
	Chosen         bool   `json:"chosen"`
}

// ModelChoice is the outcome: the chosen row (nil when the run must wait) and
// a structured reason. It is stored on the run as model_choice.
type ModelChoice struct {
	Source      string `json:"source"`
	ProfileID   string `json:"profile_id,omitempty"`
	ProfileName string `json:"profile_name,omitempty"`
	IssueID     string `json:"issue_id,omitempty"`
	// ExcludedFamily is the model family the route ruled out for this run.
	ExcludedFamily string       `json:"excluded_family,omitempty"`
	Reason         string       `json:"reason"`
	Position       int32        `json:"position,omitempty"`
	Chosen         *ModelOption `json:"chosen,omitempty"`
	RuntimeName    string       `json:"runtime_name,omitempty"`
	Provider       string       `json:"provider,omitempty"`
	// ExhaustedProfiles are the profiles whose every row was unavailable,
	// in the order they were given up (fallback_profile and all_unavailable).
	ExhaustedProfiles []string `json:"exhausted_profiles,omitempty"`
	// WaitUntil is the earliest reset among limited rows when nothing can run.
	WaitUntil *time.Time `json:"wait_until,omitempty"`
	// WaitReasonCode is the reason code of the row that sets WaitUntil, else of
	// the first row; WaitUntilEstimated: WaitUntil is a guess.
	WaitReasonCode     string `json:"wait_reason_code,omitempty"`
	WaitUntilEstimated bool   `json:"wait_until_estimated,omitempty"`
	// ProfilePersonal: the list came from the agent's personal profile.
	ProfilePersonal bool            `json:"profile_personal,omitempty"`
	Rows            []ModelRowState `json:"rows"`
}

// Waiting reports whether no row can run now.
func (c ModelChoice) Waiting() bool { return c.Chosen == nil }

// RuntimeID is the runtime of the chosen row, "" when waiting.
func (c ModelChoice) RuntimeID() string {
	if c.Chosen == nil {
		return ""
	}
	return c.Chosen.RuntimeID
}

// LimitedRuntimes are the runtimes of the consulted rows that are on a limit.
func (c ModelChoice) LimitedRuntimes() []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range c.Rows {
		if r.State == ModelRowLimited && !seen[r.RuntimeID] {
			seen[r.RuntimeID] = true
			out = append(out, r.RuntimeName)
		}
	}
	return out
}

// Summary is a one-line English description for logs and the CLI.
func (c ModelChoice) Summary() string {
	name := func(rs ModelRowState) string {
		return strings.Join(nonEmpty(rs.RuntimeName, rs.Model, rs.ThinkingLevel), "·")
	}
	var chosen string
	for _, r := range c.Rows {
		if r.Chosen {
			chosen = name(r)
		}
	}
	switch c.Reason {
	case ModelReasonPrimary:
		return "primary: " + chosen
	case ModelReasonFallbackRow:
		return fmt.Sprintf("row %d: %s (earlier rows unavailable)", c.Position, chosen)
	case ModelReasonFallbackProfile:
		return fmt.Sprintf("every model of %s unavailable, took %q row %d: %s", strings.Join(quoteAll(c.ExhaustedProfiles), ", "), c.ProfileName, c.Position, chosen)
	case ModelReasonAllUnavailable:
		out := "every model unavailable"
		if c.ExcludedFamily != "" {
			out = "every model unavailable (family " + c.ExcludedFamily + " excluded by the route)"
		}
		if c.WaitUntil != nil {
			return out + " until " + c.WaitUntil.UTC().Format(time.RFC3339) + ", the run waits"
		}
		return out + ", the run waits"
	default:
		return "no model list and no runtime"
	}
}

func nonEmpty(parts ...string) []string {
	out := parts[:0:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func quoteAll(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = fmt.Sprintf("%q", n)
	}
	return out
}

// modelList is the list a choice walks, before availability is applied.
type modelList struct {
	source  string
	profile *ModelProfileData
	options []ModelOption
}

// ResolveModelList picks the list of a run: issue profile, else the agent's
// profile (shared or personal). The agent's plain fields stand in only for an
// agent that has no profile at all (the accept carrier); an agent whose
// profile is gone or empty has no list and waits, it never falls back to the
// projection in its fields. An issue profile that no longer exists is skipped.
func ResolveModelList(in ModelChoiceInput) (source string, profile *ModelProfileData, options []ModelOption) {
	l := resolveModelList(in)
	return l.source, l.profile, l.options
}

func resolveModelList(in ModelChoiceInput) modelList {
	if p, ok := in.Profiles[in.IssueProfileID]; ok && in.IssueProfileID != "" {
		return modelList{source: ModelSourceIssueProfile, profile: &p, options: p.Options}
	}
	if in.Agent.ModelProfileID != "" {
		p, ok := in.Profiles[in.Agent.ModelProfileID]
		if !ok {
			return modelList{source: ModelSourceAgentProfile}
		}
		return modelList{source: ModelSourceAgentProfile, profile: &p, options: p.Options}
	}
	if in.Agent.RuntimeID == "" {
		return modelList{source: ModelSourceAgentFields}
	}
	args := in.Agent.CustomArgs
	if args == nil {
		args = []string{}
	}
	return modelList{source: ModelSourceAgentFields, options: []ModelOption{{
		Position: 1, RuntimeID: in.Agent.RuntimeID, Model: in.Agent.Model,
		ThinkingLevel: in.Agent.ThinkingLevel, CustomArgs: args,
	}}}
}

// rowState is the state of one row for this agent at now. A limited row says
// why and whether its end is only a guess.
func rowState(in ModelChoiceInput, o ModelOption, now time.Time) (state string, until *time.Time, code string, estimated bool) {
	rt, ok := in.Runtimes[o.RuntimeID]
	switch {
	case !ok:
		return ModelRowMissing, nil, ModelRowMissing, false
	case rt.Private && rt.OwnerID != "" && rt.OwnerID != in.Agent.OwnerID && !in.Agent.AnyOwner:
		return ModelRowNotPermitted, nil, ModelRowNotPermitted, false
	case !rt.UnavailableUntil.IsZero() && rt.UnavailableUntil.After(now):
		u := rt.UnavailableUntil
		code = rt.UnavailableCode
		if code == "" {
			code = ReasonProviderRefusal
		}
		return ModelRowLimited, &u, code, rt.UnavailableEstimated
	case !rt.Online:
		return ModelRowOffline, nil, ModelRowOffline, false
	case in.ExcludeFamily != "" && ModelFamily(rt.Provider, o.Model) == in.ExcludeFamily:
		return ModelRowSameFamily, nil, ModelRowSameFamily, false
	}
	return ModelRowAvailable, nil, "", false
}

// ModelFamily names who trained the model, so a reviewer can be put on a
// different family than the author. The model name decides: a Claude-CLI
// runtime may serve GLM or MiniMax. Without a model the provider stands in.
func ModelFamily(provider, model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case m == "":
	case strings.HasPrefix(m, "claude") || strings.Contains(m, "opus") || strings.Contains(m, "sonnet") ||
		strings.Contains(m, "haiku") || strings.Contains(m, "fable"):
		return "anthropic"
	case strings.HasPrefix(m, "gpt") || strings.HasPrefix(m, "codex") || strings.HasPrefix(m, "o1") ||
		strings.HasPrefix(m, "o3") || strings.HasPrefix(m, "o4"):
		return "openai"
	case strings.HasPrefix(m, "glm"):
		return "zhipu"
	case strings.Contains(m, "minimax"):
		return "minimax"
	case strings.Contains(m, "gemini"):
		return "google"
	case strings.Contains(m, "qwen"):
		return "qwen"
	case strings.Contains(m, "deepseek"):
		return "deepseek"
	case strings.Contains(m, "kimi"):
		return "moonshot"
	}
	switch strings.ToLower(provider) {
	case "claude":
		return "anthropic"
	case "codex":
		return "openai"
	}
	return strings.ToLower(provider)
}

// DiffRows lists the 1-based places at which two lists differ, comparing row
// by row in order on runtime, model, thinking level and arguments. NULL
// arguments (derived from the agent's disallowed tools) and an empty list (no
// arguments) are different values. A row that only one list has differs.
func DiffRows(a, b []ModelOption) []int32 {
	a, b = sortedOptions(a), sortedOptions(b)
	var out []int32
	for i := 0; i < len(a) || i < len(b); i++ {
		if i >= len(a) || i >= len(b) || !sameRow(a[i], b[i]) {
			out = append(out, int32(i+1))
		}
	}
	return out
}

func sameRow(a, b ModelOption) bool {
	if a.RuntimeID != b.RuntimeID || a.Model != b.Model || a.ThinkingLevel != b.ThinkingLevel || (a.CustomArgs == nil) != (b.CustomArgs == nil) || len(a.CustomArgs) != len(b.CustomArgs) {
		return false
	}
	for i := range a.CustomArgs {
		if a.CustomArgs[i] != b.CustomArgs[i] {
			return false
		}
	}
	return true
}

func sortedOptions(options []ModelOption) []ModelOption {
	out := append([]ModelOption(nil), options...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Position < out[j].Position })
	return out
}

// ChooseModel takes the first available row of the run's list. When every row
// is unavailable and the list came from a profile with an exhausted-profile,
// that profile's list is walked next (never the same profile twice). The
// agent's own list and plain fields have no exhausted-profile: they wait.
func ChooseModel(in ModelChoiceInput, now time.Time) ModelChoice {
	list := resolveModelList(in)
	choice := ModelChoice{Source: list.source, IssueID: in.IssueID, ExcludedFamily: in.ExcludeFamily, Rows: []ModelRowState{}}
	var waitFirstCode string
	var waitEstimated bool
	waitCode := ""
	if list.source != ModelSourceIssueProfile {
		choice.IssueID = ""
	}
	if len(list.options) == 0 {
		choice.Reason = ModelReasonNoModels
		if list.profile != nil {
			choice.ProfileID, choice.ProfileName = list.profile.ID, list.profile.Name
			choice.Reason = ModelReasonAllUnavailable
		}
		return choice
	}
	visited := map[string]bool{}
	for {
		if list.profile != nil {
			choice.ProfileID, choice.ProfileName = list.profile.ID, list.profile.Name
			choice.ProfilePersonal = list.profile.Personal() && list.source == ModelSourceAgentProfile
			visited[list.profile.ID] = true
		}
		for _, o := range sortedOptions(list.options) {
			state, until, code, estimated := rowState(in, o, now)
			row := ModelRowState{ModelOption: o, State: state, Until: until, ReasonCode: code, UntilEstimated: estimated}
			if waitFirstCode == "" && state != ModelRowAvailable {
				waitFirstCode = code
			}
			if list.profile != nil {
				row.ProfileID = list.profile.ID
			}
			if rt, ok := in.Runtimes[o.RuntimeID]; ok {
				row.RuntimeName, row.Provider = rt.Name, rt.Provider
			}
			if state == ModelRowAvailable && choice.Chosen == nil {
				row.Chosen = true
				chosen := o
				chosen.CustomArgs = EffectiveCustomArgs(o.CustomArgs, row.Provider, in.Agent.DisallowedTools)
				choice.Chosen, choice.Position = &chosen, o.Position
				choice.RuntimeName, choice.Provider = row.RuntimeName, row.Provider
			}
			if until != nil && choice.Chosen == nil && (choice.WaitUntil == nil || until.Before(*choice.WaitUntil)) {
				u := *until
				choice.WaitUntil = &u
				waitCode, waitEstimated = code, estimated
			}
			choice.Rows = append(choice.Rows, row)
		}
		if choice.Chosen != nil {
			switch {
			case len(choice.ExhaustedProfiles) > 0:
				choice.Reason = ModelReasonFallbackProfile
			case choice.Position == sortedOptions(list.options)[0].Position:
				choice.Reason = ModelReasonPrimary
			default:
				choice.Reason = ModelReasonFallbackRow
			}
			choice.WaitUntil = nil
			return choice
		}
		if list.profile == nil {
			break
		}
		next, ok := in.Profiles[list.profile.WhenExhaustedProfileID]
		if list.profile.WhenExhaustedProfileID == "" || !ok || visited[next.ID] {
			break
		}
		choice.ExhaustedProfiles = append(choice.ExhaustedProfiles, list.profile.Name)
		list = modelList{source: list.source, profile: &next, options: next.Options}
	}
	if list.profile != nil {
		choice.ExhaustedProfiles = append(choice.ExhaustedProfiles, list.profile.Name)
	}
	choice.Reason = ModelReasonAllUnavailable
	choice.WaitReasonCode, choice.WaitUntilEstimated = waitCode, waitEstimated && choice.WaitUntil != nil
	if choice.WaitReasonCode == "" {
		choice.WaitReasonCode = waitFirstCode
	}
	return choice
}

// EffectiveCustomArgs are a row's explicit custom_args, or, when the row has
// none, the agent's disallowed tools rendered for the runtime family.
func EffectiveCustomArgs(explicit []string, provider string, disallowed []string) []string {
	if explicit != nil {
		return append([]string{}, explicit...)
	}
	return RenderDisallowedTools(provider, disallowed)
}

// DisallowedTool is one server:tool pair. Server is empty for a bare tool
// name, which only Claude understands (a built-in tool such as Bash).
type DisallowedTool struct {
	Server string
	Tool   string
}

// ParseDisallowedTool reads "server:tool" or a bare "Tool".
func ParseDisallowedTool(raw string) (DisallowedTool, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return DisallowedTool{}, fmt.Errorf("empty disallowed tool")
	}
	server, tool, hasServer := strings.Cut(s, ":")
	if !hasServer {
		server, tool = "", s
	}
	server, tool = strings.TrimSpace(server), strings.TrimSpace(tool)
	if tool == "" || (hasServer && server == "") {
		return DisallowedTool{}, fmt.Errorf("disallowed tool %q must be server:tool", raw)
	}
	for _, part := range []string{server, tool} {
		if strings.ContainsAny(part, " \t\n,\"'[]=:") {
			return DisallowedTool{}, fmt.Errorf("disallowed tool %q has a forbidden character", raw)
		}
	}
	return DisallowedTool{Server: server, Tool: tool}, nil
}

// NormalizeDisallowedTools validates, de-duplicates and sorts the list.
func NormalizeDisallowedTools(raw []string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, r := range raw {
		t, err := ParseDisallowedTool(r)
		if err != nil {
			return nil, err
		}
		key := t.Tool
		if t.Server != "" {
			key = t.Server + ":" + t.Tool
		}
		if !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out, nil
}

// RenderDisallowedTools turns server:tool pairs into the runtime family's
// arguments. Claude takes one --disallowedTools list with MCP tools named
// mcp__server__tool; Codex disables tools per MCP server through -c config
// overrides, and has no switch for its built-in tools, so bare names are
// dropped there. Other families get no arguments.
func RenderDisallowedTools(provider string, disallowed []string) []string {
	var tools []DisallowedTool
	for _, raw := range disallowed {
		if t, err := ParseDisallowedTool(raw); err == nil {
			tools = append(tools, t)
		}
	}
	if len(tools) == 0 {
		return []string{}
	}
	switch provider {
	case "claude":
		names := make([]string, 0, len(tools))
		for _, t := range tools {
			if t.Server == "" {
				names = append(names, t.Tool)
			} else {
				names = append(names, "mcp__"+t.Server+"__"+t.Tool)
			}
		}
		sort.Strings(names)
		return []string{"--disallowedTools", strings.Join(names, ",")}
	case "codex":
		byServer := map[string][]string{}
		for _, t := range tools {
			if t.Server != "" {
				byServer[t.Server] = append(byServer[t.Server], t.Tool)
			}
		}
		servers := make([]string, 0, len(byServer))
		for s := range byServer {
			servers = append(servers, s)
		}
		sort.Strings(servers)
		out := []string{}
		for _, s := range servers {
			names := byServer[s]
			sort.Strings(names)
			raw, _ := json.Marshal(names)
			out = append(out, "-c", "mcp_servers."+s+".disabled_tools="+string(raw))
		}
		return out
	}
	return []string{}
}
