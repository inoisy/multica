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

// newIssueConveyorAcceptCommand builds `multica issue conveyor accept`.
func newIssueConveyorAcceptCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "accept [issue-id]",
		Short: "Run the route's accept step: check the accepted commit on top of the base and merge it",
		Long: "Runs inside the accept step's run (the daemon's accept runtime starts it). It checks out the repository\n" +
			"of the commit the route accepted, merges that commit onto the current base branch, runs the commands of\n" +
			acceptanceFile + " from the commit, pushes exactly the checked merge, and hands the outcome in as\n" +
			"the step's result. The issue defaults to MULTICA_ISSUE_ID.",
		Args: cobra.MaximumNArgs(1),
		RunE: runIssueConveyorAccept,
	}
}

// acceptRouteView is the part of GET /conveyor the accept step reads.
type acceptRouteView struct {
	Step           string `json:"step"`
	Status         string `json:"status"`
	AcceptedCommit string `json:"accepted_commit"`
	CurrentTaskID  string `json:"current_task_id"`
	Route          []struct {
		Step string `json:"step"`
	} `json:"route"`
}

// hasDev reports whether the route has a dev step: work that must hand in a commit.
func (v acceptRouteView) hasDev() bool {
	for _, n := range v.Route {
		if n.Step == "dev" {
			return true
		}
	}
	return false
}

type acceptEventPage struct {
	Events []struct {
		Kind    string           `json:"kind"`
		Action  *string          `json:"action"`
		Details acceptResultBody `json:"details"`
	} `json:"events"`
	NextCursor string `json:"next_cursor"`
}

type acceptResultBody struct {
	Artifact struct {
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
		Commit string `json:"commit"`
	} `json:"artifact"`
}

func runIssueConveyorAccept(cmd *cobra.Command, args []string) error {
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

	var view acceptRouteView
	if err := getJSON(ctx, client, base+"/conveyor", &view); err != nil {
		return err
	}
	if view.Step != "accept" {
		return fmt.Errorf("the route is on step %q, not accept", view.Step)
	}
	if task := os.Getenv("MULTICA_TASK_ID"); task != "" && view.CurrentTaskID != "" && task != view.CurrentTaskID {
		return fmt.Errorf("this run is not the route's current accept run")
	}
	var issue struct {
		Identifier string `json:"identifier"`
	}
	_ = getJSON(ctx, client, base, &issue)

	outcome, repo, commit := acceptOutcome{}, "", view.AcceptedCommit
	artifact, err := acceptedArtifact(ctx, client, base, view.AcceptedCommit)
	switch {
	case view.AcceptedCommit == "" && view.hasDev():
		// A route with dev must have accepted a commit by now: something lost
		// it, and passing would close the issue with nothing merged.
		outcome = coordinator("the route has a dev step but no accepted commit")
	case view.AcceptedCommit == "":
		// The server adds acceptance to every reviewed route; work without a
		// commit (a plan, docs) simply has nothing to merge.
		outcome = nothingToAccept("no earlier step handed in a commit")
	case err != nil:
		outcome = coordinator(err.Error())
	default:
		repo = acceptRepository(ctx, artifact.Artifact.Repo)
		workDir, _ := os.Getwd()
		fmt.Fprintf(os.Stdout, "checking out %s…\n", repo)
		checkout, cerr := daemonRepoCheckout(ctx, repo, "", true, workDir)
		if cerr != nil {
			outcome = coordinator(fmt.Sprintf("checkout of %s failed: %v", repo, cerr))
			break
		}
		fmt.Fprintf(os.Stdout, "accepting %s from %s (%s) in %s\n", short(commit), repo, artifact.Artifact.Branch, checkout.Path)
		outcome = runAcceptance(ctx, acceptSpec{
			Dir: checkout.Path, Repo: repo, Commit: commit, Branch: artifact.Artifact.Branch,
			Issue: issue.Identifier, Log: os.Stdout,
		})
	}
	fmt.Fprintf(os.Stdout, "accept: %s — %s\n", outcome.Outcome, outcome.Reason)

	result := acceptResult(outcome, repo, commit)
	apiCtx, cancel = cli.APIContext(ctx)
	defer cancel()
	var out any
	if err := client.PostJSON(apiCtx, base+"/conveyor/results", result, &out); err != nil {
		return fmt.Errorf("hand in the accept result: %w", err)
	}
	return nil
}

func getJSON(ctx context.Context, client *cli.APIClient, path string, out any) error {
	apiCtx, cancel := cli.APIContext(ctx)
	defer cancel()
	return client.GetJSON(apiCtx, path, out)
}

// acceptedArtifact finds the passing result that named the accepted commit:
// it says which repository and branch the commit lives in.
func acceptedArtifact(ctx context.Context, client *cli.APIClient, base, commit string) (acceptResultBody, error) {
	var found *acceptResultBody
	cursor := "0"
	for page := 0; page < 50; page++ {
		var p acceptEventPage
		if err := getJSON(ctx, client, base+"/conveyor/events?limit=200&cursor="+url.QueryEscape(cursor), &p); err != nil {
			return acceptResultBody{}, err
		}
		for i := range p.Events {
			e := p.Events[i]
			if e.Kind != "result" || e.Action == nil || *e.Action != "pass" || e.Details.Artifact.Repo == "" {
				continue
			}
			if c := strings.ToLower(e.Details.Artifact.Commit); c != "" && (strings.HasPrefix(commit, c) || strings.HasPrefix(c, commit)) {
				d := e.Details
				found = &d
			}
		}
		if len(p.Events) == 0 || p.NextCursor == cursor {
			break
		}
		cursor = p.NextCursor
	}
	if found == nil {
		return acceptResultBody{}, fmt.Errorf("no passing result names the repository of commit %s (artifact.repo)", short(commit))
	}
	return *found, nil
}

// acceptResult is the step's result: the accepted commit, each check, and a
// report a person can act on.
func acceptResult(o acceptOutcome, repo, commit string) map[string]any {
	checks := make([]map[string]any, 0, len(o.Checks))
	for _, c := range o.Checks {
		checks = append(checks, map[string]any{"name": c.Name, "command": c.Command, "ok": c.OK})
	}
	var report strings.Builder
	switch o.Outcome {
	case "pass":
		if o.Skipped {
			fmt.Fprintf(&report, "Приёмки нет: %s. Слияния нет.", o.Reason)
		} else {
			fmt.Fprintf(&report, "Приёмка пройдена: %s.", o.Reason)
		}
	case "changes_requested":
		fmt.Fprintf(&report, "Приёмка не пройдена: %s. Слияния нет.", o.Reason)
	case "blocked":
		fmt.Fprintf(&report, "Приёмка не дождалась ресурса: %s. Слияния нет.", o.Reason)
	default:
		fmt.Fprintf(&report, "Приёмка не смогла проверить работу: %s. Слияния нет.", o.Reason)
	}
	if tail := strings.TrimSpace(o.Tail); tail != "" {
		if len(tail) > 20000 {
			tail = "…" + tail[len(tail)-20000:]
		}
		fmt.Fprintf(&report, "\n\n```\n%s\n```", tail)
	}
	branch := o.Base
	return map[string]any{
		"schema_version": 1,
		"outcome":        o.Outcome,
		"artifact":       map[string]string{"repo": repo, "branch": branch, "commit": strings.ToLower(commit)},
		"checks":         checks,
		"report":         report.String(),
	}
}
