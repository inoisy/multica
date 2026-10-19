package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

func init() { issueCmd.AddCommand(newIssueConveyorCommand()) }

// newIssueConveyorCommand builds `multica issue conveyor`: the opt-in linear
// route of one issue, in which an executor hands a step in as a structured
// result and the server starts the next run when the executor's run ends.
func newIssueConveyorCommand() *cobra.Command {
	conv := &cobra.Command{
		Use:   "conveyor",
		Short: "Run one issue along a route of steps (opt-in)",
		Long: "A route is a list of steps, each owned by an agent, ending with close:<coordinator>.\n" +
			"An executor finishes its step by handing in a result (`conveyor result`); when its run ends the server\n" +
			"starts the next step's run, and wakes the coordinator only for a return, a stop, a failure or the end.",
	}
	for _, action := range []string{"enable", "pause", "resume", "disable"} {
		action := action
		c := &cobra.Command{
			Use:   action + " <issue-id>",
			Short: strings.ToUpper(action[:1]) + action[1:] + " the issue's route",
			Args:  exactArgs(1),
			RunE:  func(cmd *cobra.Command, args []string) error { return runIssueConveyorCommand(cmd, args[0], action) },
		}
		c.Flags().String("output", "json", "Output format: table or json")
		c.Flags().Int64("revision", -1, "Revision the change expects (default: the current one; enable on a new route expects 0)")
		switch action {
		case "enable":
			c.Flags().String("route", "", "Route as step:Agent,step:Agent,close:Agent (default: the issue's metadata.route)")
			c.Flags().String("from-step", "", "Start at this step of the route")
		case "resume":
			c.Flags().String("step", "", "Open a new round at this step (default: the step the route stopped on)")
		}
		conv.AddCommand(c)
	}
	get := &cobra.Command{
		Use: "get <issue-id>", Short: "Show the route state, its current step and why it waits", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return printConveyor(cmd, args[0], "/conveyor", nil)
		},
	}
	get.Flags().String("output", "json", "Output format: json")
	events := &cobra.Command{
		Use: "events <issue-id>", Short: "Show what the route saw, decided and started, and what it covered", Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			q := url.Values{}
			if v, _ := cmd.Flags().GetString("cursor"); v != "" {
				q.Set("cursor", v)
			}
			if v, _ := cmd.Flags().GetInt("limit"); v > 0 {
				q.Set("limit", fmt.Sprint(v))
			}
			return printConveyor(cmd, args[0], "/conveyor/events", q)
		},
	}
	events.Flags().String("output", "json", "Output format: json")
	events.Flags().String("cursor", "", "Continue after this event (next_cursor of the previous page)")
	events.Flags().Int("limit", 0, "Events per page (default 100, at most 200)")
	result := &cobra.Command{
		Use:   "result <issue-id>",
		Short: "Hand in the result of the step this run serves",
		Long: "Run this from the run of a route step. The file is the result JSON\n" +
			"({schema_version, outcome, artifact{repo,branch,commit}, checks[{name,command,ok}], report}); \"-\" reads stdin.\n" +
			"The report is posted as a comment; the route moves when this run ends, not now.",
		Args: exactArgs(1),
		RunE: runIssueConveyorResult,
	}
	result.Flags().String("file", "", "Result JSON file, or - for stdin")
	result.Flags().String("output", "json", "Output format: json")
	conv.AddCommand(get, events, result)
	return conv
}

func printConveyor(cmd *cobra.Command, issueInput, suffix string, query url.Values) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	ref, err := resolveIssueRef(ctx, client, issueInput)
	if err != nil {
		return err
	}
	path := "/api/issues/" + url.PathEscape(ref.ID) + suffix
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	var out any
	if err := client.GetJSON(ctx, path, &out); err != nil {
		return err
	}
	return cli.PrintJSON(os.Stdout, out)
}

func runIssueConveyorCommand(cmd *cobra.Command, issueInput, action string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	ref, err := resolveIssueRef(ctx, client, issueInput)
	if err != nil {
		return err
	}
	path := "/api/issues/" + url.PathEscape(ref.ID) + "/conveyor"
	revision, _ := cmd.Flags().GetInt64("revision")
	if revision < 0 {
		revision = 0
		if action != "enable" {
			var state struct {
				Revision int64 `json:"revision"`
			}
			if err := client.GetJSON(ctx, path, &state); err != nil {
				return err
			}
			revision = state.Revision
		} else {
			// A disabled or finished route is re-enabled at its own revision.
			var state struct {
				Revision int64 `json:"revision"`
			}
			if err := client.GetJSON(ctx, path, &state); err == nil {
				revision = state.Revision
			}
		}
	}
	body := map[string]any{"action": action, "expected_revision": revision}
	if v, _ := cmd.Flags().GetString("route"); v != "" {
		body["route"] = v
	}
	if v, _ := cmd.Flags().GetString("from-step"); v != "" {
		body["from_step"] = v
	}
	if v, _ := cmd.Flags().GetString("step"); v != "" {
		body["from_step"] = v
	}
	var out any
	if err := client.PutJSON(ctx, path, body, &out); err != nil {
		return err
	}
	return cli.PrintJSON(os.Stdout, out)
}

func runIssueConveyorResult(cmd *cobra.Command, args []string) error {
	file, _ := cmd.Flags().GetString("file")
	if file == "" {
		return fmt.Errorf("--file is required")
	}
	var raw []byte
	var err error
	if file == "-" {
		raw, err = io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	} else {
		raw, err = os.ReadFile(file)
	}
	if err != nil {
		return fmt.Errorf("read result: %w", err)
	}
	var body json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		return fmt.Errorf("result is not valid JSON: %w", err)
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	ref, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return err
	}
	var out any
	if err := client.PostJSON(ctx, "/api/issues/"+url.PathEscape(ref.ID)+"/conveyor/results", body, &out); err != nil {
		return err
	}
	return cli.PrintJSON(os.Stdout, out)
}
