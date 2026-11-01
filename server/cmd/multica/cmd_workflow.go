package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

type workflowHandler struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
}

type workflowStep struct {
	StatusKey string `json:"status_key,omitempty"`
	NewStatus *struct {
		Name     string `json:"name"`
		Category string `json:"category"`
		Color    string `json:"color,omitempty"`
	} `json:"new_status,omitempty"`
	Handler       *workflowHandler `json:"handler,omitempty"`
	Instructions  string           `json:"instructions,omitempty"`
	NextStatusKey string           `json:"next_status_key,omitempty"`
	BackStatusKey string           `json:"back_status_key,omitempty"`
}

// Keep omissions distinct from explicit empty values on PATCH.
type workflowWrite struct {
	Name          *string           `json:"name,omitempty"`
	Description   *string           `json:"description,omitempty"`
	Steps         *[]workflowStep   `json:"steps,omitempty"`
	StatusMapping map[string]string `json:"status_mapping,omitempty"`
	DryRun        bool              `json:"dry_run,omitempty"`
}

type workflowDTO struct {
	ID          string         `json:"id"`
	WorkspaceID string         `json:"workspace_id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	IsDefault   bool           `json:"is_default"`
	OwnerID     *string        `json:"owner_id"`
	Steps       []workflowStep `json:"steps"`
	ProjectIDs  []string       `json:"project_ids"`
	CreatedAt   string         `json:"created_at"`
	UpdatedAt   string         `json:"updated_at"`
}

var workflowCmd = newWorkflowCommand()

func init() { projectCmd.AddCommand(newProjectWorkflowCommand()) }

func newWorkflowCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "workflow", Aliases: []string{"workflows"}, Short: "Manage project workflows and status handoffs"}
	for _, action := range []string{"list", "get", "create", "update", "delete"} {
		use, count := action, 0
		if action == "get" || action == "update" || action == "delete" {
			use, count = action+" <workflow-id|default>", 1
		}
		c := &cobra.Command{Use: use, Short: strings.ToUpper(action[:1]) + action[1:] + " workflows", Args: exactArgs(count), RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkflow(cmd, args, action)
		}}
		if action == "list" {
			c.Flags().String("output", "table", "Output format: table or json")
		} else {
			c.Flags().String("output", "json", "Output format: json")
		}
		if action == "get" {
			c.Flags().Bool("definition", false, "Export only writable fields, suitable for create/update --file")
		}
		if action == "create" || action == "update" {
			c.Long = "Write a workflow using a JSON definition. Steps replace the complete ordered step list, including handlers and instructions. Only supplied fields change. Server permissions and validation apply."
			c.Flags().String("file", "", "Read a workflow definition JSON file inside the working directory")
			c.Flags().String("name", "", "Workflow name (overrides the file)")
			c.Flags().String("description", "", "Description (overrides the file; empty clears it)")
			c.Flags().Bool("allow-external-file", false, "Allow reading files outside the working directory")
		}
		if action == "update" {
			addWorkflowMappingFlags(c)
			c.Long += " Use --dry-run to inspect the status mapping plan without saving."
		}
		if action == "delete" {
			c.Long = "Delete an unused workflow. Default and workflows still used by projects cannot be deleted."
		}
		cmd.AddCommand(c)
	}
	cmd.AddCommand(newWorkflowHandoffCommand())
	return cmd
}

func newWorkflowHandoffCommand() *cobra.Command {
	handoff := &cobra.Command{Use: "handoff", Short: "Manage what happens when an issue enters a workflow status"}
	set := &cobra.Command{
		Use:   "set <workflow-id|default> <status-key>",
		Short: "Set the handler, instructions, and next/back status for one step",
		Long:  "Set the handoff of one step. Only the flags you pass change; the rest of the step is kept.\n\n--handler accepts none, project_lead, creator, or a member, agent, or squad name or UUID.",
		Args:  exactArgs(2),
		RunE:  runWorkflowHandoffSet,
	}
	set.Flags().String("handler", "", "none, project_lead, creator, or a member/agent/squad name or UUID")
	set.Flags().String("instructions", "", "Instructions the handler's run gets")
	set.Flags().String("instructions-file", "", "Read instructions from a file inside the working directory")
	set.Flags().String("next", "", "Status the handler moves the issue to when done (empty to clear)")
	set.Flags().String("back", "", "Status the handler sends the issue back to (empty to clear)")
	set.Flags().String("copy-for-project", "", "Save on a copy of the workflow used only by this project")
	set.Flags().Bool("allow-external-file", false, "Allow reading files outside the working directory")
	set.Flags().String("output", "json", "Output format: json")
	handoff.AddCommand(set)
	return handoff
}

func addWorkflowMappingFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("dry-run", false, "Validate and show the status mapping plan without saving")
	cmd.Flags().String("status-mapping", "", `JSON file mapping removed status keys to target keys, e.g. {"qa":"dev"}`)
}

func newProjectWorkflowCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "workflow", Short: "Inspect or change a project's workflow"}
	for _, action := range []string{"get", "set"} {
		c := &cobra.Command{Use: action + " <project-id>", Args: exactArgs(1), Short: strings.ToUpper(action[:1]) + action[1:] + " the project's effective workflow", RunE: func(cmd *cobra.Command, args []string) error {
			return runProjectWorkflow(cmd, args, action)
		}}
		c.Flags().String("output", "json", "Output format: json")
		if action == "set" {
			c.Long = "Switch the project to a named workflow or Default. Status remapping is administrative: it does not start runs. Use --dry-run to inspect the migration plan first."
			c.Flags().String("workflow", "", "Workflow UUID, or default")
			c.Flags().Bool("default", false, "Return to the workspace Default workflow")
			c.Flags().Bool("allow-external-file", false, "Allow reading files outside the working directory")
			addWorkflowMappingFlags(c)
		}
		cmd.AddCommand(c)
	}
	return cmd
}

// Use the same file containment guard as issue descriptions. Reject unknown
// fields and trailing JSON so misspelled configuration cannot silently save.
func readWorkflowJSON(cmd *cobra.Command, flag string, out any) error {
	path, _ := cmd.Flags().GetString(flag)
	if err := ensureFileFlagWithinWorkdir(cmd, flag, "workflow", path); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read --%s: %w", flag, err)
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("--%s must contain a JSON object", flag)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("decode --%s: %w", flag, err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("--%s must contain exactly one JSON object", flag)
	}
	return nil
}

func workflowMapping(cmd *cobra.Command, existing map[string]string) (map[string]string, error) {
	if cmd.Flags().Changed("status-mapping") {
		existing = nil
		if err := readWorkflowJSON(cmd, "status-mapping", &existing); err != nil {
			return nil, err
		}
	}
	return existing, nil
}

func fetchWorkflows(ctx context.Context, client *cli.APIClient) ([]workflowDTO, error) {
	var result struct {
		Workflows []workflowDTO `json:"workflows"`
	}
	if err := client.GetJSON(ctx, "/api/issue-workflows", &result); err != nil {
		return nil, err
	}
	return result.Workflows, nil
}

func resolveWorkflowID(ctx context.Context, client *cli.APIClient, ref string) (string, error) {
	if strings.EqualFold(ref, "default") {
		workflows, err := fetchWorkflows(ctx, client)
		if err != nil {
			return "", err
		}
		for _, workflow := range workflows {
			if workflow.IsDefault {
				return workflow.ID, nil
			}
		}
		return "", fmt.Errorf("workspace Default workflow not found")
	}
	id, err := uuid.Parse(ref)
	if err != nil {
		return "", fmt.Errorf("expected a workflow UUID or default: %q", ref)
	}
	return id.String(), nil
}

func runWorkflow(cmd *cobra.Command, args []string, action string) error {
	output, _ := cmd.Flags().GetString("output")
	if output != "json" && !(action == "list" && output == "table") {
		return fmt.Errorf("unsupported output format %q", output)
	}
	var req workflowWrite
	if action == "create" || action == "update" {
		if cmd.Flags().Changed("file") {
			if err := readWorkflowJSON(cmd, "file", &req); err != nil {
				return err
			}
		}
		if cmd.Flags().Changed("name") {
			v, _ := cmd.Flags().GetString("name")
			req.Name = &v
		}
		if cmd.Flags().Changed("description") {
			v, _ := cmd.Flags().GetString("description")
			req.Description = &v
		}
		if action == "create" {
			if req.Name == nil || strings.TrimSpace(*req.Name) == "" {
				return fmt.Errorf("workflow name is required: use --name or --file")
			}
			// Creation has no server-side preview. Never pretend a requested
			// dry run was honored, or silently ignore migration options.
			if req.DryRun || req.StatusMapping != nil {
				return fmt.Errorf("dry_run and status_mapping are only supported by workflow update")
			}
		} else {
			if req.Name == nil && req.Description == nil && req.Steps == nil {
				return fmt.Errorf("no workflow changes supplied: use --file, --name or --description")
			}
			var err error
			req.StatusMapping, err = workflowMapping(cmd, req.StatusMapping)
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("dry-run") {
				req.DryRun, _ = cmd.Flags().GetBool("dry-run")
			}
		}
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	if action == "list" {
		workflows, err := fetchWorkflows(ctx, client)
		if err != nil {
			return err
		}
		if output == "json" {
			return cli.PrintJSON(os.Stdout, workflows)
		}
		rows := make([][]string, 0, len(workflows))
		for _, w := range workflows {
			rows = append(rows, []string{w.ID, w.Name, strconv.FormatBool(w.IsDefault), strconv.Itoa(len(w.Steps)), strconv.Itoa(len(w.ProjectIDs))})
		}
		cli.PrintTable(os.Stdout, []string{"ID", "NAME", "DEFAULT", "STEPS", "PROJECTS"}, rows)
		return nil
	}
	path := "/api/issue-workflows"
	if action != "create" {
		id, err := resolveWorkflowID(ctx, client, args[0])
		if err != nil {
			return err
		}
		path += "/" + id
	}
	var result any
	switch action {
	case "get":
		err = client.GetJSON(ctx, path, &result)
	case "create":
		err = client.PostJSON(ctx, path, req, &result)
	case "update":
		err = client.PatchJSON(ctx, path, req, &result)
	case "delete":
		err = client.DeleteJSON(ctx, path)
		result = map[string]any{"deleted": true}
	}
	if err != nil {
		return fmt.Errorf("%s workflow: %w", action, err)
	}
	if definition, _ := cmd.Flags().GetBool("definition"); definition {
		row, _ := result.(map[string]any)
		result = map[string]any{"name": row["name"], "description": row["description"], "steps": row["steps"]}
	}
	return cli.PrintJSON(os.Stdout, result)
}

func runProjectWorkflow(cmd *cobra.Command, args []string, action string) error {
	if output, _ := cmd.Flags().GetString("output"); output != "json" {
		return fmt.Errorf("unsupported output format %q", output)
	}
	var req struct {
		WorkflowID    *string           `json:"workflow_id"`
		StatusMapping map[string]string `json:"status_mapping,omitempty"`
		DryRun        bool              `json:"dry_run,omitempty"`
	}
	if action == "set" {
		ref, _ := cmd.Flags().GetString("workflow")
		useDefault, _ := cmd.Flags().GetBool("default")
		if (cmd.Flags().Changed("workflow") && cmd.Flags().Changed("default")) || (!useDefault && ref == "") {
			return fmt.Errorf("use exactly one of --workflow <UUID|default> or --default")
		}
		if !useDefault && !strings.EqualFold(ref, "default") {
			id, err := uuid.Parse(ref)
			if err != nil {
				return fmt.Errorf("expected a workflow UUID or default: %q", ref)
			}
			value := id.String()
			req.WorkflowID = &value
		}
		var err error
		req.StatusMapping, err = workflowMapping(cmd, nil)
		if err != nil {
			return err
		}
		req.DryRun, _ = cmd.Flags().GetBool("dry-run")
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	project, err := resolveProjectID(ctx, client, args[0])
	if err != nil {
		return err
	}
	path := "/api/projects/" + project.ID
	var result any
	if action == "set" {
		err = client.PostJSON(ctx, path+"/workflow", req, &result)
	} else {
		var row struct {
			WorkflowID *string `json:"workflow_id"`
		}
		if err = client.GetJSON(ctx, path, &row); err != nil {
			return err
		}
		ref := "default"
		if row.WorkflowID != nil && *row.WorkflowID != "" {
			ref = *row.WorkflowID
		}
		id, resolveErr := resolveWorkflowID(ctx, client, ref)
		if resolveErr != nil {
			return resolveErr
		}
		err = client.GetJSON(ctx, "/api/issue-workflows/"+id, &result)
	}
	if err != nil {
		return fmt.Errorf("%s project workflow: %w", action, err)
	}
	return cli.PrintJSON(os.Stdout, result)
}

func resolveHandoffHandler(ctx context.Context, client *cli.APIClient, value string) (*workflowHandler, error) {
	v := strings.TrimSpace(value)
	switch strings.ToLower(v) {
	case "", "none":
		return &workflowHandler{Type: "none"}, nil
	case "project_lead", "creator":
		return &workflowHandler{Type: strings.ToLower(v)}, nil
	}
	var typ, id string
	var err error
	if uuidRegexp.MatchString(v) {
		typ, id, err = resolveAssigneeByID(ctx, client, v, issueAssigneeKinds)
	} else {
		typ, id, err = resolveAssignee(ctx, client, v, issueAssigneeKinds)
	}
	if err != nil {
		return nil, fmt.Errorf("resolve handler: %w", err)
	}
	return &workflowHandler{Type: typ, ID: id}, nil
}

func runWorkflowHandoffSet(cmd *cobra.Command, args []string) error {
	if output, _ := cmd.Flags().GetString("output"); output != "json" {
		return fmt.Errorf("unsupported output format %q", output)
	}
	if cmd.Flags().Changed("instructions") && cmd.Flags().Changed("instructions-file") {
		return fmt.Errorf("use either --instructions or --instructions-file, not both")
	}
	statusKey := strings.TrimSpace(args[1])
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(cmd.Context())
	defer cancel()
	id, err := resolveWorkflowID(ctx, client, args[0])
	if err != nil {
		return err
	}
	var workflow workflowDTO
	if err := client.GetJSON(ctx, "/api/issue-workflows/"+id, &workflow); err != nil {
		return fmt.Errorf("get workflow: %w", err)
	}
	var current *workflowStep
	for i := range workflow.Steps {
		if workflow.Steps[i].StatusKey == statusKey {
			current = &workflow.Steps[i]
			break
		}
	}
	if current == nil {
		return fmt.Errorf("workflow %s has no step %q", workflow.Name, statusKey)
	}

	// The API replaces the whole handoff; start from the current step so
	// only the flags passed change.
	body := struct {
		Handler          *workflowHandler `json:"handler"`
		Instructions     string           `json:"instructions"`
		NextStatusKey    string           `json:"next_status_key"`
		BackStatusKey    string           `json:"back_status_key"`
		CopyForProjectID string           `json:"copy_for_project_id,omitempty"`
	}{current.Handler, current.Instructions, current.NextStatusKey, current.BackStatusKey, ""}
	if body.Handler == nil {
		body.Handler = &workflowHandler{Type: "none"}
	}
	if cmd.Flags().Changed("handler") {
		v, _ := cmd.Flags().GetString("handler")
		if body.Handler, err = resolveHandoffHandler(ctx, client, v); err != nil {
			return err
		}
	}
	if cmd.Flags().Changed("instructions") {
		body.Instructions, _ = cmd.Flags().GetString("instructions")
	}
	if path, _ := cmd.Flags().GetString("instructions-file"); path != "" {
		if err := ensureFileFlagWithinWorkdir(cmd, "instructions-file", "instructions", path); err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read --instructions-file: %w", err)
		}
		body.Instructions = string(data)
	}
	if cmd.Flags().Changed("next") {
		body.NextStatusKey, _ = cmd.Flags().GetString("next")
	}
	if cmd.Flags().Changed("back") {
		body.BackStatusKey, _ = cmd.Flags().GetString("back")
	}
	if v, _ := cmd.Flags().GetString("copy-for-project"); v != "" {
		project, err := resolveProjectID(ctx, client, v)
		if err != nil {
			return fmt.Errorf("resolve project: %w", err)
		}
		body.CopyForProjectID = project.ID
	}
	var result any
	if err := client.PutJSON(ctx, "/api/issue-workflows/"+id+"/handoffs/"+url.PathEscape(statusKey), body, &result); err != nil {
		return fmt.Errorf("set handoff: %w", err)
	}
	return cli.PrintJSON(os.Stdout, result)
}
