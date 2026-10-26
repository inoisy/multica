package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// newWorkflowAcceptCommand builds `multica workflow accept`, the command the
// daemon's accept runtime runs for a Workflow acceptance step.
func newWorkflowAcceptCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "accept [issue-id]",
		Short: "Run an acceptance step: check the pinned commit on top of the base and merge it",
		Long: "Runs inside the acceptance step's run (the daemon's accept runtime starts it). It checks out the\n" +
			"repository of the commit the previous step pinned (`issue status --commit`), merges it onto the current\n" +
			"base branch, runs the commands of " + acceptanceFile + " from the commit, pushes exactly the checked\n" +
			"merge, posts the report as a comment and moves the issue to the next step, back, or to blocked.\n" +
			"With \"mode\": \"ci\" in that file the platform merges after its checks: the command does not merge,\n" +
			"push or run commands, it waits until the pinned commit is in the base and blocks the issue otherwise.\n" +
			"The issue defaults to MULTICA_ISSUE_ID.",
		Args: cobra.MaximumNArgs(1),
		RunE: runAccept,
	}
}

// newIssueConveyorAcceptCommand keeps `multica issue conveyor accept` for one
// release, for daemons and scripts that still call the old name.
func newIssueConveyorAcceptCommand() *cobra.Command {
	cmd := newWorkflowAcceptCommand()
	cmd.Deprecated = "use `multica workflow accept`"
	return cmd
}

func runAccept(cmd *cobra.Command, args []string) error {
	issueInput := strings.TrimSpace(os.Getenv("MULTICA_ISSUE_ID"))
	if len(args) == 1 {
		issueInput = args[0]
	}
	if issueInput == "" {
		return fmt.Errorf("issue id is required (argument or MULTICA_ISSUE_ID)")
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	apiCtx, cancel := cli.APIContext(ctx)
	ref, err := resolveIssueRef(apiCtx, client, issueInput)
	cancel()
	if err != nil {
		return err
	}
	base := "/api/issues/" + url.PathEscape(ref.ID)

	var issue struct {
		Identifier string `json:"identifier"`
	}
	_ = getJSON(ctx, client, base, &issue)

	return runWorkflowAccept(ctx, client, base, issue.Identifier, os.Stdout)
}

func getJSON(ctx context.Context, client *cli.APIClient, path string, out any) error {
	apiCtx, cancel := cli.APIContext(ctx)
	defer cancel()
	return client.GetJSON(apiCtx, path, out)
}
