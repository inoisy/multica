package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// Model lists of agents: the profile an agent references, its own list (an
// exception to the profile), and what the server chooses for the next run.
// The server is the only writer of the model a run uses; these commands edit
// the lists it chooses from.

func init() {
	agentCmd.AddCommand(newAgentModelsCommand())
	issueCmd.AddCommand(newIssueModelProfileCommand())
	runtimeCmd.AddCommand(newRuntimeUnavailableCommand())
	profileCmd.GroupID = groupCore
	rootCmd.AddCommand(profileCmd)
}

var profileCmd = &cobra.Command{
	Use:   "profile",
	Short: "Manage the workspace's model profiles",
	Long: "A model profile is an ordered list of runtime·model·thinking rows. Agents and issues reference a\n" +
		"profile; editing it changes the next run of every agent and issue that references it.",
}

type modelOption struct {
	Position      int32     `json:"position"`
	RuntimeID     string    `json:"runtime_id"`
	Model         string    `json:"model"`
	ThinkingLevel string    `json:"thinking_level"`
	CustomArgs    *[]string `json:"custom_args"`
}

type modelRow struct {
	modelOption
	RuntimeName string  `json:"runtime_name"`
	Provider    string  `json:"provider"`
	State       string  `json:"state"`
	Until       *string `json:"until"`
	Chosen      bool    `json:"chosen"`
}

type modelChoice struct {
	Source            string       `json:"source"`
	ProfileName       string       `json:"profile_name"`
	Reason            string       `json:"reason"`
	Position          int32        `json:"position"`
	Chosen            *modelOption `json:"chosen"`
	RuntimeName       string       `json:"runtime_name"`
	ExhaustedProfiles []string     `json:"exhausted_profiles"`
	WaitUntil         *string      `json:"wait_until"`
	Rows              []modelRow   `json:"rows"`
}

type modelProfile struct {
	ID                     string     `json:"id"`
	Name                   string     `json:"name"`
	Description            string     `json:"description"`
	WhenExhaustedProfileID *string    `json:"when_exhausted_profile_id"`
	Options                []modelRow `json:"options"`
	Agents                 []struct {
		Name string `json:"name"`
	} `json:"agents"`
	OwnListAgents []struct {
		Name string `json:"name"`
	} `json:"own_list_agents"`
	OpenIssues []struct {
		Identifier string `json:"identifier"`
	} `json:"open_issues"`
}

func modelsClient(cmd *cobra.Command) (*cli.APIClient, context.Context, context.CancelFunc, error) {
	client, err := newAPIClient(cmd)
	if err != nil {
		return nil, nil, nil, err
	}
	ctx, cancel := cli.APIContext(context.Background())
	return client, ctx, cancel, nil
}

func outputJSON(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetString("output")
	return v == "json"
}

func formatLocal(ts *string) string {
	if ts == nil {
		return ""
	}
	t, err := time.Parse(time.RFC3339Nano, *ts)
	if err != nil {
		return *ts
	}
	return t.Local().Format("2006-01-02 15:04")
}

func sourceLabel(c modelChoice) string {
	switch c.Source {
	case "issue_profile":
		return fmt.Sprintf("issue profile %q", c.ProfileName)
	case "agent_list":
		return "own list"
	case "agent_profile":
		return fmt.Sprintf("profile %q", c.ProfileName)
	default:
		return "agent fields"
	}
}

func reasonLabel(c modelChoice) string {
	switch c.Reason {
	case "primary":
		return "primary"
	case "fallback_row":
		var limited []string
		for _, r := range c.Rows {
			if r.Position < c.Position && r.State != "available" {
				s := fmt.Sprintf("row %d %s", r.Position, r.State)
				if r.Until != nil {
					s += " until " + formatLocal(r.Until)
				}
				limited = append(limited, s)
			}
		}
		return fmt.Sprintf("%s — took row %d", strings.Join(limited, ", "), c.Position)
	case "fallback_profile":
		return fmt.Sprintf("every model of %s unavailable — took %q", strings.Join(c.ExhaustedProfiles, ", "), c.ProfileName)
	case "all_unavailable":
		if c.WaitUntil != nil {
			return "every model unavailable until " + formatLocal(c.WaitUntil) + " — the run waits"
		}
		return "every model unavailable — the run waits"
	default:
		return "no model list and no runtime"
	}
}

func printModelRows(rows []modelRow) {
	table := make([][]string, 0, len(rows))
	for _, r := range rows {
		state := r.State
		if r.Until != nil {
			state += " until " + formatLocal(r.Until)
		}
		mark := ""
		if r.Chosen {
			mark = "←"
		}
		table = append(table, []string{fmt.Sprint(r.Position), r.RuntimeName, r.Model, r.ThinkingLevel, state, mark})
	}
	cli.PrintTable(os.Stdout, []string{"#", "RUNTIME", "MODEL", "THINKING", "STATE", ""}, table)
}

func newAgentModelsCommand() *cobra.Command {
	models := &cobra.Command{
		Use:   "models <agent>",
		Short: "Show the agent's model list and what the server would choose now",
		Args:  exactArgs(1),
		RunE:  runAgentModelsShow,
	}
	models.Flags().String("output", "table", "Output format: table or json")

	profile := &cobra.Command{
		Use:   "profile <agent> <profile|off>",
		Short: "Put the agent on a profile (off: no profile)",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, ctx, cancel, err := modelsClient(cmd)
			if err != nil {
				return err
			}
			defer cancel()
			var id any
			if args[1] != "off" {
				pid, err := resolveModelProfile(ctx, client, args[1])
				if err != nil {
					return err
				}
				id = pid
			}
			return putAgentModels(cmd, ctx, client, args[0], map[string]any{"model_profile_id": id})
		},
	}
	set := &cobra.Command{
		Use:   "set <agent> <runtime>[:<model>[:<thinking>]]...",
		Short: "Give the agent its own list (one row pins the model)",
		Long:  "The own list replaces the agent's profile until `models reset`. Rows are tried in order.",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, ctx, cancel, err := modelsClient(cmd)
			if err != nil {
				return err
			}
			defer cancel()
			rows, err := parseModelRows(ctx, client, args[1:])
			if err != nil {
				return err
			}
			return putAgentModels(cmd, ctx, client, args[0], map[string]any{"options": rows})
		},
	}
	reset := &cobra.Command{
		Use:   "reset <agent>",
		Short: "Remove the agent's own list; it runs on its profile again",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, ctx, cancel, err := modelsClient(cmd)
			if err != nil {
				return err
			}
			defer cancel()
			return putAgentModels(cmd, ctx, client, args[0], map[string]any{"options": []any{}})
		},
	}
	tools := &cobra.Command{
		Use:   "tools <agent> [server:tool...]",
		Short: "Set the tools the agent may not use (no tools: clear)",
		Long:  "Rendered as --disallowedTools for claude and mcp_servers.<server>.disabled_tools for codex on rows without custom args.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, ctx, cancel, err := modelsClient(cmd)
			if err != nil {
				return err
			}
			defer cancel()
			list := append([]string{}, args[1:]...)
			return putAgentModels(cmd, ctx, client, args[0], map[string]any{"disallowed_tools": list})
		},
	}
	for _, c := range []*cobra.Command{profile, set, reset, tools} {
		c.Flags().String("output", "table", "Output format: table or json")
		models.AddCommand(c)
	}
	return models
}

func runAgentModelsShow(cmd *cobra.Command, args []string) error {
	client, ctx, cancel, err := modelsClient(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	agentID, err := resolveAgent(ctx, client, args[0])
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := client.GetJSON(ctx, "/api/agents/"+url.PathEscape(agentID)+"/models", &raw); err != nil {
		return err
	}
	return printAgentModels(cmd, raw)
}

func putAgentModels(cmd *cobra.Command, ctx context.Context, client *cli.APIClient, agent string, body map[string]any) error {
	agentID, err := resolveAgent(ctx, client, agent)
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := client.PutJSON(ctx, "/api/agents/"+url.PathEscape(agentID)+"/models", body, &raw); err != nil {
		return err
	}
	return printAgentModels(cmd, raw)
}

func printAgentModels(cmd *cobra.Command, raw json.RawMessage) error {
	if outputJSON(cmd) {
		var v any
		_ = json.Unmarshal(raw, &v)
		return cli.PrintJSON(os.Stdout, v)
	}
	var view struct {
		ProfileName     string      `json:"profile_name"`
		OwnList         bool        `json:"own_list"`
		DisallowedTools []string    `json:"disallowed_tools"`
		Preview         modelChoice `json:"preview"`
		Last            *struct {
			IssueIdentifier string       `json:"issue_identifier"`
			CreatedAt       string       `json:"created_at"`
			Choice          *modelChoice `json:"choice"`
		} `json:"last"`
		Issues []struct {
			Identifier  string `json:"identifier"`
			ProfileName string `json:"profile_name"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(raw, &view); err != nil {
		return err
	}
	profile := view.ProfileName
	if profile == "" {
		profile = "—"
	}
	fmt.Printf("Profile:    %s", profile)
	if view.OwnList {
		fmt.Print(" (own list differs from the profile)")
	}
	fmt.Println()
	fmt.Printf("Next run:   %s · %s\n", sourceLabel(view.Preview), reasonLabel(view.Preview))
	if view.Last != nil && view.Last.Choice != nil {
		on := view.Last.IssueIdentifier
		if on == "" {
			on = "no issue"
		}
		fmt.Printf("Last run:   %s · row %d · %s (%s, %s)\n", sourceLabel(*view.Last.Choice), view.Last.Choice.Position, reasonLabel(*view.Last.Choice), on, formatLocal(&view.Last.CreatedAt))
	}
	if len(view.DisallowedTools) > 0 {
		fmt.Printf("Disallowed: %s\n", strings.Join(view.DisallowedTools, ", "))
	}
	for _, i := range view.Issues {
		fmt.Printf("Issue %s runs on profile %q\n", i.Identifier, i.ProfileName)
	}
	fmt.Println()
	printModelRows(view.Preview.Rows)
	return nil
}

// parseModelRows reads <runtime>[:<model>[:<thinking>]] with the runtime
// given by id, name or custom name.
func parseModelRows(ctx context.Context, client *cli.APIClient, specs []string) ([]map[string]any, error) {
	var runtimes []map[string]any
	if err := client.GetJSON(ctx, "/api/runtimes", &runtimes); err != nil {
		return nil, fmt.Errorf("fetch runtimes: %w", err)
	}
	rows := make([]map[string]any, 0, len(specs))
	for _, spec := range specs {
		parts := strings.SplitN(spec, ":", 3)
		var match []string
		for _, rt := range runtimes {
			id := strVal(rt, "id")
			if id == parts[0] || strings.EqualFold(strVal(rt, "name"), parts[0]) || strings.EqualFold(strVal(rt, "custom_name"), parts[0]) {
				match = append(match, id)
			}
		}
		if len(match) != 1 {
			return nil, fmt.Errorf("runtime %q matches %d runtimes", parts[0], len(match))
		}
		row := map[string]any{"runtime_id": match[0]}
		if len(parts) > 1 {
			row["model"] = parts[1]
		}
		if len(parts) > 2 {
			row["thinking_level"] = parts[2]
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func resolveModelProfile(ctx context.Context, client *cli.APIClient, input string) (string, error) {
	var profiles []modelProfile
	if err := client.GetJSON(ctx, "/api/model-profiles", &profiles); err != nil {
		return "", fmt.Errorf("fetch profiles: %w", err)
	}
	for _, p := range profiles {
		if p.ID == input || strings.EqualFold(p.Name, input) {
			return p.ID, nil
		}
	}
	return "", fmt.Errorf("no profile %q", input)
}

func newIssueModelProfileCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "model-profile <issue> [<profile>|off]",
		Short: "Show or set the profile the issue's runs choose models from",
		Long:  "An issue profile wins over the assignee's list for runs of this issue only. off: the runs use their agent's list.",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, ctx, cancel, err := modelsClient(cmd)
			if err != nil {
				return err
			}
			defer cancel()
			ref, err := resolveIssueRef(ctx, client, args[0])
			if err != nil {
				return err
			}
			path := "/api/issues/" + url.PathEscape(ref.ID) + "/model-profile"
			var out map[string]any
			if len(args) == 1 {
				err = client.GetJSON(ctx, path, &out)
			} else {
				var id any
				if args[1] != "off" {
					if id, err = resolveModelProfile(ctx, client, args[1]); err != nil {
						return err
					}
				}
				err = client.PutJSON(ctx, path, map[string]any{"model_profile_id": id}, &out)
			}
			if err != nil {
				return err
			}
			if outputJSON(cmd) {
				return cli.PrintJSON(os.Stdout, out)
			}
			if name := strVal(out, "profile_name"); name != "" {
				fmt.Printf("%s: profile %q\n", ref.Display, name)
			} else {
				fmt.Printf("%s: no profile (runs use their agent's list)\n", ref.Display)
			}
			return nil
		},
	}
	c.Flags().String("output", "table", "Output format: table or json")
	return c
}

func newRuntimeUnavailableCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "unavailable",
		Short: "List runtimes on a provider limit and until when",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, ctx, cancel, err := modelsClient(cmd)
			if err != nil {
				return err
			}
			defer cancel()
			var out []map[string]any
			if err := client.GetJSON(ctx, "/api/runtime-unavailable", &out); err != nil {
				return err
			}
			if outputJSON(cmd) {
				return cli.PrintJSON(os.Stdout, out)
			}
			rows := make([][]string, 0, len(out))
			for _, r := range out {
				until := strVal(r, "until")
				rows = append(rows, []string{strVal(r, "runtime_name"), formatLocal(&until), strVal(r, "reason")})
			}
			cli.PrintTable(os.Stdout, []string{"RUNTIME", "UNTIL", "REASON"}, rows)
			return nil
		},
	}
	c.Flags().String("output", "table", "Output format: table or json")
	return c
}

func profileRowInputs(rows []modelRow) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		row := map[string]any{"runtime_id": r.RuntimeID, "model": r.Model, "thinking_level": r.ThinkingLevel}
		if r.CustomArgs != nil {
			row["custom_args"] = *r.CustomArgs
		}
		out = append(out, row)
	}
	return out
}

func printProfile(cmd *cobra.Command, p modelProfile) error {
	if outputJSON(cmd) {
		return cli.PrintJSON(os.Stdout, p)
	}
	fmt.Printf("%s", p.Name)
	if p.Description != "" {
		fmt.Printf(" — %s", p.Description)
	}
	fmt.Println()
	printModelRows(p.Options)
	names := func(n int, get func(int) string) string {
		var out []string
		for i := 0; i < n; i++ {
			out = append(out, get(i))
		}
		if len(out) == 0 {
			return "—"
		}
		return strings.Join(out, ", ")
	}
	fmt.Printf("\nAgents:          %s\n", names(len(p.Agents), func(i int) string { return p.Agents[i].Name }))
	fmt.Printf("Own-list agents: %s\n", names(len(p.OwnListAgents), func(i int) string { return p.OwnListAgents[i].Name }))
	fmt.Printf("Open issues:     %s\n", names(len(p.OpenIssues), func(i int) string { return p.OpenIssues[i].Identifier }))
	return nil
}

func compactRows(rows []modelRow) string {
	parts := make([]string, 0, len(rows))
	for _, r := range rows {
		parts = append(parts, strings.Join(nonEmptyStrings(r.RuntimeName, r.Model, r.ThinkingLevel), "·"))
	}
	return strings.Join(parts, " → ")
}

func nonEmptyStrings(in ...string) []string {
	var out []string
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func init() {
	list := &cobra.Command{
		Use: "list", Short: "List profiles with their models in order", Args: exactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, ctx, cancel, err := modelsClient(cmd)
			if err != nil {
				return err
			}
			defer cancel()
			var profiles []modelProfile
			if err := client.GetJSON(ctx, "/api/model-profiles", &profiles); err != nil {
				return err
			}
			if outputJSON(cmd) {
				return cli.PrintJSON(os.Stdout, profiles)
			}
			byID := map[string]string{}
			for _, p := range profiles {
				byID[p.ID] = p.Name
			}
			rows := make([][]string, 0, len(profiles))
			for _, p := range profiles {
				exhausted := "wait"
				if p.WhenExhaustedProfileID != nil {
					exhausted = byID[*p.WhenExhaustedProfileID]
				}
				rows = append(rows, []string{p.Name, compactRows(p.Options), exhausted, fmt.Sprint(len(p.Agents)), fmt.Sprint(len(p.OpenIssues))})
			}
			cli.PrintTable(os.Stdout, []string{"NAME", "MODELS", "WHEN EXHAUSTED", "AGENTS", "OPEN ISSUES"}, rows)
			return nil
		},
	}
	show := &cobra.Command{
		Use: "show <profile>", Short: "Show a profile, its rows and who references it", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, ctx, cancel, err := modelsClient(cmd)
			if err != nil {
				return err
			}
			defer cancel()
			id, err := resolveModelProfile(ctx, client, args[0])
			if err != nil {
				return err
			}
			var p modelProfile
			if err := client.GetJSON(ctx, "/api/model-profiles/"+url.PathEscape(id), &p); err != nil {
				return err
			}
			return printProfile(cmd, p)
		},
	}
	create := &cobra.Command{
		Use:   "create <name> [--row runtime[:model[:thinking]]]... | --from-agent <agent>",
		Short: "Create a profile from rows or from an agent's current list",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, ctx, cancel, err := modelsClient(cmd)
			if err != nil {
				return err
			}
			defer cancel()
			body, err := profileBody(cmd, ctx, client, map[string]any{"name": args[0]})
			if err != nil {
				return err
			}
			if from, _ := cmd.Flags().GetString("from-agent"); from != "" {
				agentID, err := resolveAgent(ctx, client, from)
				if err != nil {
					return err
				}
				body["from_agent_id"] = agentID
			}
			var p modelProfile
			if err := client.PostJSON(ctx, "/api/model-profiles", body, &p); err != nil {
				return err
			}
			return printProfile(cmd, p)
		},
	}
	create.Flags().String("from-agent", "", "Copy the rows of this agent's current list")
	edit := &cobra.Command{
		Use:   "edit <profile>",
		Short: "Change a profile; every agent and issue on it gets the change from its next run",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, ctx, cancel, err := modelsClient(cmd)
			if err != nil {
				return err
			}
			defer cancel()
			id, err := resolveModelProfile(ctx, client, args[0])
			if err != nil {
				return err
			}
			var current modelProfile
			if err := client.GetJSON(ctx, "/api/model-profiles/"+url.PathEscape(id), &current); err != nil {
				return err
			}
			base := map[string]any{"name": current.Name, "description": current.Description,
				"when_exhausted_profile_id": current.WhenExhaustedProfileID, "options": profileRowInputs(current.Options)}
			if v, _ := cmd.Flags().GetString("name"); v != "" {
				base["name"] = v
			}
			body, err := profileBody(cmd, ctx, client, base)
			if err != nil {
				return err
			}
			var p modelProfile
			if err := client.PutJSON(ctx, "/api/model-profiles/"+url.PathEscape(id), body, &p); err != nil {
				return err
			}
			return printProfile(cmd, p)
		},
	}
	edit.Flags().String("name", "", "New name")
	for _, c := range []*cobra.Command{create, edit} {
		c.Flags().StringArray("row", nil, "Row runtime[:model[:thinking]]; repeat in order (edit: replaces all rows)")
		c.Flags().String("description", "", "Description")
		c.Flags().String("when-exhausted", "", "Profile to use when every row is unavailable (off: wait)")
	}
	del := &cobra.Command{
		Use: "delete <profile>", Short: "Delete a profile nobody references", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, ctx, cancel, err := modelsClient(cmd)
			if err != nil {
				return err
			}
			defer cancel()
			id, err := resolveModelProfile(ctx, client, args[0])
			if err != nil {
				return err
			}
			if err := client.DeleteJSON(ctx, "/api/model-profiles/"+url.PathEscape(id)); err != nil {
				return err
			}
			fmt.Printf("Deleted profile %s\n", args[0])
			return nil
		},
	}
	imp := &cobra.Command{
		Use:   "import --file <document.json> [--apply]",
		Short: "Carry the control panel's tiers, chains and epic overrides into profiles (dry run by default)",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			file, _ := cmd.Flags().GetString("file")
			if file == "" {
				return fmt.Errorf("--file is required")
			}
			raw, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			var doc json.RawMessage
			if err := json.Unmarshal(raw, &doc); err != nil {
				return fmt.Errorf("document is not valid JSON: %w", err)
			}
			apply, _ := cmd.Flags().GetBool("apply")
			client, ctx, cancel, err := modelsClient(cmd)
			if err != nil {
				return err
			}
			defer cancel()
			var plan any
			if err := client.PostJSON(ctx, "/api/model-profiles/import", map[string]any{"document": doc, "dry_run": !apply}, &plan); err != nil {
				return err
			}
			return cli.PrintJSON(os.Stdout, plan)
		},
	}
	imp.Flags().String("file", "", "Import document (see docs/stt/models-import.md)")
	imp.Flags().Bool("apply", false, "Write the plan instead of only showing it")
	for _, c := range []*cobra.Command{list, show, create, edit, del, imp} {
		c.Flags().String("output", "table", "Output format: table or json")
		profileCmd.AddCommand(c)
	}
}

// profileBody applies --row, --description and --when-exhausted to base.
func profileBody(cmd *cobra.Command, ctx context.Context, client *cli.APIClient, base map[string]any) (map[string]any, error) {
	if rows, _ := cmd.Flags().GetStringArray("row"); len(rows) > 0 {
		parsed, err := parseModelRows(ctx, client, rows)
		if err != nil {
			return nil, err
		}
		base["options"] = parsed
	}
	if cmd.Flags().Changed("description") {
		v, _ := cmd.Flags().GetString("description")
		base["description"] = v
	}
	if cmd.Flags().Changed("when-exhausted") {
		v, _ := cmd.Flags().GetString("when-exhausted")
		if v == "" || v == "off" {
			base["when_exhausted_profile_id"] = nil
		} else {
			id, err := resolveModelProfile(ctx, client, v)
			if err != nil {
				return nil, err
			}
			base["when_exhausted_profile_id"] = id
		}
	}
	return base, nil
}
