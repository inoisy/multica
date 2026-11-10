package handler

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestNormalizeRepoSlug(t *testing.T) {
	cases := map[string]string{
		"https://github.com/inoisy/resource-planning.git": "inoisy/resource-planning",
		"https://github.com/inoisy/resource-planning":     "inoisy/resource-planning",
		"https://github.com/Inoisy/Resource-Planning/":    "inoisy/resource-planning",
		"inoisy/resource-planning":                        "inoisy/resource-planning",
		"git@github.com:inoisy/resource-planning.git":     "inoisy/resource-planning",
		"ssh://git@github.com/inoisy/resource-planning":   "inoisy/resource-planning",
		" resource-planning ":                             "",
		"https://github.com/":                             "",
		"":                                                "",
	}
	for in, want := range cases {
		if got := normalizeRepoSlug(in); got != want {
			t.Errorf("normalizeRepoSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

// pinLinkFixture is a project whose Workflow hands in_review to an agent, so
// a status change into in_review may pin a commit, plus a GitHub installation
// bound to the test workspace.
func pinLinkFixture(t *testing.T, installationID int64) string {
	t.Helper()
	seedTestCatalog(t)
	agentID := seededReadyAgentID(t)
	projectID := deliveryProject(t, fmt.Sprintf("PR pin link project %d", installationID), agentID)
	setColumnHandoff(t, projectID, "in_review", handToAgent(agentID, "Review the pinned commit.")).Want(http.StatusOK)
	if _, err := testHandler.Queries.CreateGitHubInstallation(context.Background(), db.CreateGitHubInstallationParams{
		WorkspaceID: parseUUID(testWorkspaceID), InstallationID: installationID,
		AccountLogin: fmt.Sprintf("acct-%d", installationID), AccountType: "User",
	}); err != nil {
		t.Fatalf("CreateGitHubInstallation: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		testPool.Exec(bg, `DELETE FROM issue_pull_request WHERE pull_request_id IN (SELECT id FROM github_pull_request WHERE workspace_id = $1)`, testWorkspaceID)
		testPool.Exec(bg, `DELETE FROM github_pull_request WHERE workspace_id = $1`, testWorkspaceID)
		testPool.Exec(bg, `DELETE FROM github_installation WHERE workspace_id = $1`, testWorkspaceID)
	})
	return projectID
}

func pinIssueForTest(t *testing.T, projectID, title, repo, branch string) string {
	t.Helper()
	issueID := dbfx.Issue(t, title, testutil.Cols{"project_id": projectID, "status": "in_progress"})
	updateIssueForTest(t, issueID, map[string]any{"status": "in_review", "workflow_artifact": map[string]any{
		"repo": repo, "branch": branch, "commit": testPinnedCommit,
	}}).Want(http.StatusOK)
	return issueID
}

// The title and branch carry no issue key (the repository's linter forbids
// them); the pin names the branch, and the PR from it links.
func TestPinThenWebhookLinksPullRequest(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	secret := "pin-link-secret"
	t.Setenv("GITHUB_WEBHOOK_SECRET", secret)
	const installationID int64 = 40526001
	projectID := pinLinkFixture(t, installationID)

	pinned := pinIssueForTest(t, projectID, "pinned by URL", "https://github.com/Acme/Widget.git", "t-12345-login")
	otherRepo := pinIssueForTest(t, projectID, "other repo", "acme/gadget", "t-12345-login")
	otherBranch := pinIssueForTest(t, projectID, "other branch", "git@github.com:acme/widget.git", "t-12345-logout")

	firePRWebhook(t, secret, installationID, 25, "fix(login): keep the session. T-12345", "", "t-12345-login", "opened")

	if n := linkedPRCountForTest(t, pinned); n != 1 {
		t.Fatalf("pinned issue: %d linked PRs, want 1", n)
	}
	list := listIssuePRsForTest(t, pinned)
	if list.PullRequests[0].LinkSource != "auto" {
		t.Errorf("link_source = %q, want auto", list.PullRequests[0].LinkSource)
	}
	for name, id := range map[string]string{"other repo": otherRepo, "other branch": otherBranch} {
		if n := linkedPRCountForTest(t, id); n != 0 {
			t.Errorf("%s: %d linked PRs, want 0", name, n)
		}
	}

	// An edit of the open PR reconciles title/branch links; the pin link stays.
	firePRWebhook(t, secret, installationID, 25, "fix(login): keep the session longer. T-12345", "", "t-12345-login", "edited")
	if n := linkedPRCountForTest(t, pinned); n != 1 {
		t.Fatalf("after edit: %d linked PRs, want 1", n)
	}
	firePRWebhook(t, secret, installationID, 25, "fix(login): keep the session longer. T-12345", "", "t-12345-login", "merged")
	if n := linkedPRCountForTest(t, pinned); n != 1 {
		t.Fatalf("after merge: %d linked PRs, want 1", n)
	}
}

// A PR opened before the pin links when the pin is written.
func TestWebhookThenPinLinksPullRequest(t *testing.T) {
	if testHandler == nil {
		t.Skip("handler test fixture not initialized (no DB?)")
	}
	secret := "pin-link-secret-2"
	t.Setenv("GITHUB_WEBHOOK_SECRET", secret)
	const installationID int64 = 40526002
	projectID := pinLinkFixture(t, installationID)

	firePRWebhook(t, secret, installationID, 31, "feat(plan): weekly view. T-12345", "", "t-12345-weekly", "opened")
	firePRWebhook(t, secret, installationID, 32, "feat(plan): monthly view. T-12345", "", "t-12345-monthly", "opened")

	pinned := pinIssueForTest(t, projectID, "pinned after PR", "acme/widget", "t-12345-weekly")
	otherRepo := pinIssueForTest(t, projectID, "pinned other repo", "https://github.com/acme/gadget", "t-12345-weekly")

	list := listIssuePRsForTest(t, pinned)
	if len(list.PullRequests) != 1 || list.PullRequests[0].Number != 31 {
		t.Fatalf("pinned issue PRs = %+v, want only #31", list.PullRequests)
	}
	if n := linkedPRCountForTest(t, otherRepo); n != 0 {
		t.Errorf("other repo: %d linked PRs, want 0", n)
	}

	// A person removed the PR: a repeated pin must not bring it back.
	unlinkPRForTest(t, pinned, list.PullRequests[0].ID)
	updateIssueForTest(t, pinned, map[string]any{"status": "in_progress"}).Want(http.StatusOK)
	updateIssueForTest(t, pinned, map[string]any{"status": "in_review", "workflow_artifact": map[string]any{
		"repo": "acme/widget", "branch": "t-12345-weekly", "commit": testPinnedCommit,
	}}).Want(http.StatusOK)
	if n := linkedPRCountForTest(t, pinned); n != 0 {
		t.Errorf("after removal and re-pin: %d linked PRs, want 0", n)
	}
}
