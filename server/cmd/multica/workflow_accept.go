package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
)

// workflowAcceptView is GET /api/issues/{id}/workflow-artifact: the commit
// handed to the acceptance step and where the step leads.
type workflowAcceptView struct {
	Status     string `json:"status"`
	NextStatus string `json:"next_status"`
	BackStatus string `json:"back_status"`
	Artifact   *struct {
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
		Commit string `json:"commit"`
	} `json:"artifact"`
}

// acceptWorkflowTarget is where an acceptance outcome moves the issue: the
// next step on a pass, the step's return on requested changes, and blocked
// when acceptance could not judge the work, so a person sees it.
func acceptWorkflowTarget(o acceptOutcome, view workflowAcceptView) string {
	switch o.Outcome {
	case "pass":
		return view.NextStatus
	case "changes_requested":
		if view.BackStatus != "" {
			return view.BackStatus
		}
	}
	return "blocked"
}

// runWorkflowAccept is the acceptance step of a project Workflow: it merges
// the commit the previous step pinned with its status change, reports the
// checks as a comment and hands the issue on with an ordinary status change.
func runWorkflowAccept(ctx context.Context, client *cli.APIClient, base, identifier string, log io.Writer) error {
	var view workflowAcceptView
	if err := getJSON(ctx, client, base+"/workflow-artifact", &view); err != nil {
		return fmt.Errorf("read the pinned commit: %w", err)
	}
	outcome, repo, commit := acceptOutcome{}, "", ""
	if view.Artifact == nil {
		// A pass here would close the issue with nothing merged.
		outcome = coordinator("no commit is pinned: the previous step must hand off with --commit and --repo")
	} else {
		commit = strings.ToLower(view.Artifact.Commit)
		repo = acceptRepository(ctx, view.Artifact.Repo)
		workDir, _ := os.Getwd()
		fmt.Fprintf(log, "checking out %s…\n", repo)
		checkout, err := daemonRepoCheckout(ctx, repo, "", true, workDir)
		if err != nil {
			outcome = coordinator(fmt.Sprintf("checkout of %s failed: %v", repo, err))
		} else {
			fmt.Fprintf(log, "accepting %s from %s (%s) in %s\n", short(commit), repo, view.Artifact.Branch, checkout.Path)
			outcome = runAcceptance(ctx, acceptSpec{
				Dir: checkout.Path, Repo: repo, Commit: commit, Branch: view.Artifact.Branch,
				Issue: identifier, Log: log,
			})
		}
	}
	fmt.Fprintf(log, "accept: %s — %s\n", outcome.Outcome, outcome.Reason)

	report := acceptReport(outcome)
	apiCtx, cancel := cli.APIContext(ctx)
	defer cancel()
	var out any
	if err := client.PostJSON(apiCtx, base+"/comments", map[string]any{"content": report}, &out); err != nil {
		return fmt.Errorf("post the acceptance report: %w", err)
	}
	target := acceptWorkflowTarget(outcome, view)
	if target == "" || target == view.Status {
		return nil
	}
	if err := client.PutJSON(apiCtx, base, map[string]any{"status": target}, &out); err != nil {
		return fmt.Errorf("move the issue to %s: %w", target, err)
	}
	fmt.Fprintf(log, "issue moved to %s\n", target)
	return nil
}

// acceptReport is the comment the acceptance step leaves: the outcome and the
// tail of the checks, so a person can act on it.
func acceptReport(o acceptOutcome) string {
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
	return report.String()
}
