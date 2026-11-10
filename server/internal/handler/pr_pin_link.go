package handler

import (
	"context"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issueworkflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Linking a PR by the issue's workflow pin (fork). Some repositories forbid
// issue keys in PR titles and branches, so the title/branch claim never links
// their PRs. The workflow pin names the repository and branch the issue's work
// lives on; a PR whose head is that branch in that repository delivers the
// issue. Pin links are stored with linked_by_type 'pin': the webhook's
// reconciliation never drops them, and they are reported as "auto".

// normalizeRepoSlug turns a pinned repository — an https/ssh URL with or
// without ".git", or "owner/name" — into a lowercase "owner/name". It returns
// "" when no owner and name can be read.
func normalizeRepoSlug(repo string) string {
	s := strings.TrimSpace(repo)
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
		if j := strings.Index(s, "/"); j >= 0 {
			s = s[j+1:]
		} else {
			return ""
		}
	} else if at := strings.Index(s, "@"); at >= 0 {
		if c := strings.Index(s[at:], ":"); c >= 0 {
			s = s[at+c+1:]
		}
	}
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	s = strings.Trim(s, "/")
	s = strings.TrimSuffix(s, ".git")
	parts := strings.Split(s, "/")
	if len(parts) < 2 {
		return ""
	}
	owner, name := parts[len(parts)-2], parts[len(parts)-1]
	if owner == "" || name == "" {
		return ""
	}
	return strings.ToLower(owner + "/" + name)
}

// linkPullRequestByPins links an open or draft PR to the workspace's issues
// whose current workflow pin names the PR's repository and head branch. It
// returns the issues whose link is new.
func linkPullRequestByPins(ctx context.Context, q *db.Queries, pr db.GithubPullRequest) []pgtype.UUID {
	if !pr.Branch.Valid || pr.Branch.String == "" || (pr.State != "open" && pr.State != "draft") {
		return nil
	}
	pins, err := q.ListIssuePinsByBranch(ctx, db.ListIssuePinsByBranchParams{WorkspaceID: pr.WorkspaceID, Branch: pr.Branch.String})
	if err != nil {
		slog.Warn("pr pin link: list pins failed", "err", err)
		return nil
	}
	slug := strings.ToLower(pr.RepoOwner + "/" + pr.RepoName)
	var linked []pgtype.UUID
	for _, pin := range pins {
		if normalizeRepoSlug(pin.Repo) != slug {
			continue
		}
		rows, err := q.LinkIssueToPullRequestByPin(ctx, db.LinkIssueToPullRequestByPinParams{IssueID: pin.IssueID, PullRequestID: pr.ID})
		if err != nil {
			slog.Warn("pr pin link: link failed", "err", err)
			continue
		}
		if rows > 0 {
			linked = append(linked, pin.IssueID)
		}
	}
	return linked
}

// linkPinnedPullRequests links the issue to the workspace's mirrored PRs from
// the pinned repository and branch: every open or draft one, or, when none is
// open, the most recently merged one. Nothing is ever unlinked. It runs after
// the pinning status write commits; a failure is logged, not returned.
func linkPinnedPullRequests(ctx context.Context, q *db.Queries, issue db.Issue, pin issueworkflow.Artifact) {
	if pin.Branch == "" {
		return
	}
	slug := normalizeRepoSlug(pin.Repo)
	owner, name, ok := strings.Cut(slug, "/")
	if !ok {
		return
	}
	ws, err := q.GetWorkspace(ctx, issue.WorkspaceID)
	if err != nil {
		slog.Warn("pr pin link: load workspace failed", "err", err)
		return
	}
	if autoLink, _ := autoLinkPRsEnabledForWorkspace(ws); !autoLink || !githubFeaturesEnabled(ws) {
		return
	}
	prs, err := q.ListGitHubPullRequestsByBranch(ctx, db.ListGitHubPullRequestsByBranchParams{
		WorkspaceID: issue.WorkspaceID, RepoOwner: owner, RepoName: name, Branch: pin.Branch,
	})
	if err != nil {
		slog.Warn("pr pin link: list pull requests failed", "err", err)
		return
	}
	var targets []db.GithubPullRequest
	for _, pr := range prs {
		if pr.State == "open" || pr.State == "draft" {
			targets = append(targets, pr)
		}
	}
	if len(targets) == 0 {
		for _, pr := range prs {
			if pr.State == "merged" {
				targets = append(targets, pr)
				break
			}
		}
	}
	for _, pr := range targets {
		if _, err := q.LinkIssueToPullRequestByPin(ctx, db.LinkIssueToPullRequestByPinParams{IssueID: issue.ID, PullRequestID: pr.ID}); err != nil {
			slog.Warn("pr pin link: link failed", "err", err)
		}
	}
}
