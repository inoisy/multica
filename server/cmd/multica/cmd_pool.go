package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

func init() {
	c := newPoolCommand()
	c.GroupID = groupRuntime
	rootCmd.AddCommand(c)
}

func newPoolCommand() *cobra.Command {
	pool := &cobra.Command{Use: "pool", Short: "Manage shared account pools and workspace grants"}
	list := &cobra.Command{Use: "list", Short: "List accessible pools and their availability", Args: exactArgs(0), RunE: runPoolList}
	show := &cobra.Command{Use: "show <pool>", Short: "Show a pool by id or name", Args: exactArgs(1), RunE: runPoolShow}
	create := &cobra.Command{Use: "create <name>", Short: "Create an account pool and grant it to workspaces (instance admin)", Args: exactArgs(1), RunE: runPoolCreate}
	set := &cobra.Command{Use: "set <pool>", Short: "Edit a pool with revision protection (instance admin)", Args: exactArgs(1), RunE: runPoolSet}
	grant := &cobra.Command{Use: "grant <pool>", Short: "Replace workspace grants (instance admin)", Args: exactArgs(1), RunE: runPoolGrant}
	f := create.Flags()
	f.StringSlice("subs", nil, "Account keys in spend order")
	f.String("order", "first", "Spend order: first or balanced")
	f.String("type", "", "claude or codex; defaults to the first account's family")
	f.String("description", "", "Description")
	f.Bool("all-workspaces", false, "Grant to every workspace, including future ones")
	f.StringSlice("workspaces", nil, "Workspace ids; defaults to the current workspace")
	f = set.Flags()
	f.StringSlice("subs", nil, "Replace the ordered account keys (empty clears)")
	f.String("add", "", "Account key to add or move")
	f.Bool("first", false, "With --add: place the account first")
	f.String("remove", "", "Account key to remove")
	f.String("order", "", "Spend order: first or balanced")
	f.String("name", "", "New name")
	f.String("description", "", "Description")
	f.Bool("all-workspaces", false, "Grant to every workspace, including future ones")
	grant.Flags().StringSlice("workspaces", nil, "Exact workspace ids to grant; empty revokes all explicit grants")
	for _, c := range []*cobra.Command{set, grant} {
		c.Flags().Int("revision", 0, "Expected revision (default: fetch current; a conflict requires rereading)")
	}
	for _, c := range []*cobra.Command{list, show, create, set, grant} {
		c.Flags().String("output", "table", "Output format: table or json")
		pool.AddCommand(c)
	}
	return pool
}

func accountPoolPath(id string) string { return "/api/pools/" + url.PathEscape(id) }

func resolveAccountPool(ctx context.Context, client *cli.APIClient, ref string) (map[string]any, error) {
	var resp struct {
		Pools []map[string]any `json:"pools"`
	}
	if err := client.GetJSON(ctx, "/api/pools", &resp); err != nil {
		return nil, err
	}
	var matches []map[string]any
	for _, p := range resp.Pools {
		if str(p["id"]) == ref {
			return p, nil
		}
		if strings.EqualFold(str(p["name"]), strings.TrimSpace(ref)) {
			matches = append(matches, p)
		}
	}
	if len(matches) != 1 {
		return nil, fmt.Errorf("pool %q matches %d pools; use its id", ref, len(matches))
	}
	return matches[0], nil
}

func runPoolList(cmd *cobra.Command, _ []string) error {
	client, ctx, cancel, err := modelsClient(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	var resp struct {
		Pools     []map[string]any `json:"pools"`
		CanManage bool             `json:"can_manage"`
	}
	if err := client.GetJSON(ctx, "/api/pools", &resp); err != nil {
		return err
	}
	if outputJSON(cmd) {
		return cli.PrintJSON(os.Stdout, resp)
	}
	rows := [][]string{}
	for _, p := range resp.Pools {
		v, _ := p["availability"].(map[string]any)
		rows = append(rows, []string{str(p["id"]), str(p["name"]), str(p["revision"]), str(p["spend_order"]), reasonCodeLabel(str(v["reason_code"]), "")})
	}
	cli.PrintTable(os.Stdout, []string{"ID", "NAME", "REVISION", "ORDER", "AVAILABILITY"}, rows)
	return nil
}

func runPoolShow(cmd *cobra.Command, args []string) error {
	client, ctx, cancel, err := modelsClient(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	p, err := resolveAccountPool(ctx, client, args[0])
	if err != nil {
		return err
	}
	var out map[string]any
	if err := client.GetJSON(ctx, accountPoolPath(str(p["id"])), &out); err != nil {
		return err
	}
	return printAccountPool(cmd, out)
}

func runPoolCreate(cmd *cobra.Command, args []string) error {
	client, ctx, cancel, err := modelsClient(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	subs, _ := cmd.Flags().GetStringSlice("subs")
	if subs == nil {
		subs = []string{}
	}
	family, _ := cmd.Flags().GetString("type")
	if family == "" {
		if len(subs) == 0 {
			return errors.New("--type is required when --subs is empty")
		}
		var sub map[string]any
		if err := client.GetJSON(ctx, subscriptionPath(subs[0]), &sub); err != nil {
			return err
		}
		family = str(sub["family"])
	}
	order, _ := cmd.Flags().GetString("order")
	all, _ := cmd.Flags().GetBool("all-workspaces")
	description, _ := cmd.Flags().GetString("description")
	body := map[string]any{"name": args[0], "runtime_type": family, "subscriptions": subs, "spend_order": order, "description": description, "all_workspaces": all}
	workspaces, _ := cmd.Flags().GetStringSlice("workspaces")
	if all && len(workspaces) > 0 {
		return errors.New("--all-workspaces and --workspaces are mutually exclusive")
	}
	if !all {
		if !cmd.Flags().Changed("workspaces") {
			ws, err := requireWorkspaceID(cmd)
			if err != nil {
				return err
			}
			workspaces = []string{ws}
		}
		if workspaces == nil {
			workspaces = []string{}
		}
		body["workspace_ids"] = workspaces
	}
	var out map[string]any
	if err := client.PostJSON(ctx, "/api/pools", body, &out); err != nil {
		return subscriptionError("create pool", err)
	}
	return printAccountPool(cmd, out)
}

func poolExpectedRevision(cmd *cobra.Command, p map[string]any) any {
	if cmd.Flags().Changed("revision") {
		v, _ := cmd.Flags().GetInt("revision")
		return v
	}
	return p["revision"]
}

func runPoolSet(cmd *cobra.Command, args []string) error {
	client, ctx, cancel, err := modelsClient(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	p, err := resolveAccountPool(ctx, client, args[0])
	if err != nil {
		return err
	}
	body := map[string]any{"revision": poolExpectedRevision(cmd, p)}
	add, _ := cmd.Flags().GetString("add")
	remove, _ := cmd.Flags().GetString("remove")
	first, _ := cmd.Flags().GetBool("first")
	if first && add == "" {
		return errors.New("--first requires --add")
	}
	if cmd.Flags().Changed("subs") {
		if add != "" || remove != "" {
			return errors.New("--subs cannot be combined with --add or --remove")
		}
		keys, _ := cmd.Flags().GetStringSlice("subs")
		if keys == nil {
			keys = []string{}
		}
		body["subscriptions"] = keys
	} else if add != "" || remove != "" {
		keys := []string{}
		subs, _ := p["subscriptions"].([]any)
		for _, raw := range subs {
			a, _ := raw.(map[string]any)
			keys = append(keys, str(a["key"]))
		}
		keys, err = nextPoolKeys(keys, add, first, remove)
		if err != nil {
			return err
		}
		body["subscriptions"] = keys
	}
	for flag, field := range map[string]string{"order": "spend_order", "name": "name", "description": "description"} {
		if cmd.Flags().Changed(flag) {
			v, _ := cmd.Flags().GetString(flag)
			body[field] = v
		}
	}
	if cmd.Flags().Changed("all-workspaces") {
		v, _ := cmd.Flags().GetBool("all-workspaces")
		body["all_workspaces"] = v
	}
	if len(body) == 1 {
		return errors.New("pass --subs, --add, --remove, --order, --name, --description or --all-workspaces")
	}
	var out map[string]any
	if err := client.PatchJSON(ctx, accountPoolPath(str(p["id"])), body, &out); err != nil {
		return subscriptionError("edit pool (reread after revision conflict)", err)
	}
	return printAccountPool(cmd, out)
}

func runPoolGrant(cmd *cobra.Command, args []string) error {
	if !cmd.Flags().Changed("workspaces") {
		return errors.New("pass --workspaces <ids> (empty revokes explicit grants)")
	}
	client, ctx, cancel, err := modelsClient(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	p, err := resolveAccountPool(ctx, client, args[0])
	if err != nil {
		return err
	}
	workspaces, _ := cmd.Flags().GetStringSlice("workspaces")
	if workspaces == nil {
		workspaces = []string{}
	}
	var out map[string]any
	if err := client.PutJSON(ctx, accountPoolPath(str(p["id"]))+"/grants", map[string]any{"revision": poolExpectedRevision(cmd, p), "workspace_ids": workspaces}, &out); err != nil {
		return err
	}
	return printAccountPool(cmd, out)
}

func printAccountPool(cmd *cobra.Command, p map[string]any) error {
	if outputJSON(cmd) {
		return cli.PrintJSON(os.Stdout, p)
	}
	fmt.Fprintf(os.Stdout, "%s (%s), revision %v, spend order: %s\n", str(p["name"]), str(p["id"]), p["revision"], str(p["spend_order"]))
	fmt.Fprintf(os.Stdout, "Workspaces: %v; all workspaces: %v\n", p["workspace_ids"], p["all_workspaces"])
	rows := [][]string{}
	subs, _ := p["subscriptions"].([]any)
	for _, raw := range subs {
		a, _ := raw.(map[string]any)
		until := str(a["until"])
		if a["until_estimated"] == true {
			until += " (estimate)"
		}
		rows = append(rows, []string{str(a["position"]), str(a["key"]), str(a["title"]), reasonCodeLabel(str(a["reason_code"]), ""), until})
	}
	cli.PrintTable(os.Stdout, []string{"#", "ACCOUNT", "TITLE", "REASON", "UNTIL"}, rows)
	return nil
}
