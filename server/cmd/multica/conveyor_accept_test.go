package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// acceptRig is an origin repository with a main branch, a clone the
// acceptance works in (as the daemon's checkout would be) and a second clone
// that plays the developer and anyone else pushing to main.
type acceptRig struct {
	t      *testing.T
	origin string
	work   string
	dev    string
}

func newAcceptRig(t *testing.T) *acceptRig {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	r := &acceptRig{t: t, origin: filepath.Join(root, "origin.git"), work: filepath.Join(root, "work"), dev: filepath.Join(root, "dev")}
	r.git(root, "init", "--quiet", "--bare", "--initial-branch=test/base", r.origin)
	r.git(root, "clone", "--quiet", r.origin, r.dev)
	r.identity(r.dev)
	r.write(r.dev, "README.md", "hello\n")
	r.git(r.dev, "add", ".")
	r.git(r.dev, "commit", "--quiet", "-m", "init")
	r.git(r.dev, "push", "--quiet", "origin", "test/base")
	r.git(root, "clone", "--quiet", r.origin, r.work)
	r.identity(r.work)
	return r
}

func (r *acceptRig) identity(dir string) {
	r.git(dir, "config", "user.name", "t")
	r.git(dir, "config", "user.email", "t@t")
}

func (r *acceptRig) git(dir string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *acceptRig) write(dir, name, content string) {
	r.t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// branch commits files on a new branch off main, pushes it and returns the commit.
func (r *acceptRig) branch(name string, cfg acceptConfig, files map[string]string) string {
	r.t.Helper()
	r.git(r.dev, "fetch", "--quiet", "origin")
	r.git(r.dev, "checkout", "--quiet", "-B", name, "origin/test/base")
	raw, _ := json.Marshal(cfg)
	r.write(r.dev, acceptanceFile, string(raw))
	for f, c := range files {
		r.write(r.dev, f, c)
	}
	r.git(r.dev, "add", ".")
	r.git(r.dev, "commit", "--quiet", "-m", name)
	r.git(r.dev, "push", "--quiet", "origin", name)
	return r.git(r.dev, "rev-parse", "HEAD")
}

// pushToMain lands an unrelated commit on main, as another merge would.
func (r *acceptRig) pushToMain(file string) string {
	r.t.Helper()
	r.git(r.dev, "fetch", "--quiet", "origin")
	r.git(r.dev, "checkout", "--quiet", "-B", "other-"+file, "origin/test/base")
	r.write(r.dev, file, file+"\n")
	r.git(r.dev, "add", ".")
	r.git(r.dev, "commit", "--quiet", "-m", "other "+file)
	r.git(r.dev, "push", "--quiet", "origin", "HEAD:test/base")
	return r.git(r.dev, "rev-parse", "HEAD")
}

func (r *acceptRig) main() string { return r.git(r.origin, "rev-parse", "test/base") }

func (r *acceptRig) accept(commit, branch string) (acceptOutcome, string) {
	var log bytes.Buffer
	out := runAcceptance(context.Background(), acceptSpec{
		Dir: r.work, Repo: r.origin, Commit: commit, Branch: branch, Issue: "test-issue", Log: &log,
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	return out, log.String()
}

func TestAcceptMergesExactlyTheCheckedCommit(t *testing.T) {
	r := newAcceptRig(t)
	sha := r.branch("t-1-feature", acceptConfig{Commands: []string{"test -f feature.txt", "grep -q hello README.md"}}, map[string]string{"feature.txt": "x\n"})
	out, log := r.accept(sha[:10], "t-1-feature")
	if out.Outcome != "pass" {
		t.Fatalf("outcome %s: %s\n%s", out.Outcome, out.Reason, log)
	}
	head := r.main()
	if head != out.Merged {
		t.Fatalf("main is %s, the checked merge is %s", head, out.Merged)
	}
	if parent := r.git(r.origin, "rev-parse", "test/base^2"); parent != sha {
		t.Fatalf("the merge brought %s, not the accepted %s", parent, sha)
	}
	if msg := r.git(r.origin, "log", "-1", "--format=%B", "test/base"); !strings.Contains(msg, "Merge t-1-feature into test/base") || !strings.Contains(msg, "test-issue") {
		t.Fatalf("merge message %q", msg)
	}
	if len(out.Checks) != 4 || !out.Checks[2].OK || out.Checks[2].Command != "test -f feature.txt" {
		t.Fatalf("checks %+v", out.Checks)
	}
	if !strings.Contains(log, "$ test -f feature.txt") {
		t.Fatalf("the run log does not show the commands:\n%s", log)
	}

	// Accepting it again finds it already merged and merges nothing.
	again, _ := r.accept(sha, "t-1-feature")
	if again.Outcome != "pass" || r.main() != head {
		t.Fatalf("second accept: %s, main moved to %s", again.Outcome, r.main())
	}
}

func TestAcceptRedCommandMergesNothing(t *testing.T) {
	r := newAcceptRig(t)
	before := r.main()
	sha := r.branch("t-2", acceptConfig{Commands: []string{"echo checking; echo boom >&2; exit 3"}}, nil)
	out, _ := r.accept(sha, "t-2")
	if out.Outcome != "changes_requested" || !strings.Contains(out.Tail, "boom") {
		t.Fatalf("outcome %s tail %q", out.Outcome, out.Tail)
	}
	if r.main() != before {
		t.Fatal("a red check merged")
	}
}

// STT-366: a full check on a loaded server timed out in a different test on
// each run. A failed command runs again once; green on the retry merges and
// says so, red twice stays red, retries=0 keeps a single run.
func TestAcceptRunsAFailedCommandAgain(t *testing.T) {
	r := newAcceptRig(t)
	counter := filepath.Join(t.TempDir(), "n")
	flaky := `n=$(cat ` + counter + ` 2>/dev/null || echo 0); n=$((n+1)); echo $n > ` + counter + `; [ $n -ge 2 ] || { echo flake >&2; exit 1; }`
	sha := r.branch("t-flaky", acceptConfig{Commands: []string{flaky}}, nil)
	out, log := r.accept(sha, "t-flaky")
	if out.Outcome != "pass" || !strings.Contains(out.Reason, "green on a retry") || !strings.Contains(log, "running it again (1/1)") {
		t.Fatalf("flaky command: %s (%s), log:\n%s", out.Outcome, out.Reason, log)
	}

	before := r.main()
	red := r.branch("t-red", acceptConfig{Commands: []string{"echo boom >&2; exit 3"}}, nil)
	out, log = r.accept(red, "t-red")
	if out.Outcome != "changes_requested" || strings.Count(log, "$ echo boom") != 2 || r.main() != before {
		t.Fatalf("red twice: %s, log:\n%s", out.Outcome, log)
	}

	zero := 0
	once := r.branch("t-once", acceptConfig{Commands: []string{"echo boom >&2; exit 3"}, Retries: &zero}, nil)
	if out, log = r.accept(once, "t-once"); out.Outcome != "changes_requested" || strings.Count(log, "$ echo boom") != 1 {
		t.Fatalf("retries=0: %s, log:\n%s", out.Outcome, log)
	}
}

func TestAcceptConflictAndDiffGuard(t *testing.T) {
	r := newAcceptRig(t)
	sha := r.branch("t-3", acceptConfig{Commands: []string{"true"}}, map[string]string{"README.md": "mine\n"})
	r.pushToMain("README.md")
	out, _ := r.accept(sha, "t-3")
	if out.Outcome != "changes_requested" || !strings.Contains(out.Reason, "conflicts") || !strings.Contains(out.Tail, "README.md") {
		t.Fatalf("conflict: %s %q %q", out.Outcome, out.Reason, out.Tail)
	}

	guarded := r.branch("t-4", acceptConfig{Commands: []string{"true"}}, map[string]string{"a.ts": "const a: any = 1\n"})
	out, _ = r.accept(guarded, "t-4")
	if out.Outcome != "changes_requested" || !strings.Contains(out.Tail, "a: any") {
		t.Fatalf("diff guard: %s %q", out.Outcome, out.Tail)
	}
	off := []string{}
	allowed := r.branch("t-5", acceptConfig{Commands: []string{"true"}, DiffGuard: &off}, map[string]string{"b.ts": "const b: any = 1\n"})
	if out, _ = r.accept(allowed, "t-5"); out.Outcome != "pass" {
		t.Fatalf("an empty diff_guard still guarded: %s %s", out.Outcome, out.Reason)
	}
}

func TestAcceptWaitsOutABusyResource(t *testing.T) {
	r := newAcceptRig(t)
	// Busy twice, then free; the counter lives outside the checkout.
	counter := filepath.Join(t.TempDir(), "n")
	cmd := `n=$(cat ` + counter + ` 2>/dev/null || echo 0); n=$((n+1)); echo $n > ` + counter + `; [ $n -ge 3 ] || exit 75`
	sha := r.branch("t-6", acceptConfig{Commands: []string{cmd}, BusyExitCodes: []int{75}, WaitPollSeconds: 1, WaitMaxSeconds: 10}, nil)
	out, log := r.accept(sha, "t-6")
	if out.Outcome != "pass" || strings.Count(log, "resource busy") != 2 {
		t.Fatalf("outcome %s, log:\n%s", out.Outcome, log)
	}

	always := r.branch("t-7", acceptConfig{Commands: []string{"exit 75"}, BusyExitCodes: []int{75}, WaitPollSeconds: 1, WaitMaxSeconds: 2}, nil)
	before := r.main()
	out, _ = r.accept(always, "t-7")
	if out.Outcome != "blocked" || r.main() != before {
		t.Fatalf("a resource that never frees up: %s (%s), main moved: %v", out.Outcome, out.Reason, r.main() != before)
	}
}

func TestAcceptChecksAgainWhenTheBaseMoves(t *testing.T) {
	r := newAcceptRig(t)
	// The first check lands another commit on main, as a parallel merge would.
	marker := filepath.Join(t.TempDir(), "raced")
	race := `[ -f ` + marker + ` ] || { touch ` + marker + ` && cd ` + r.dev + ` && git fetch -q origin && git checkout -q -B racer origin/test/base && echo r > racer.txt && git add . && git -c user.name=t -c user.email=t@t commit -qm racer && git push -q origin HEAD:test/base; }`
	sha := r.branch("t-8", acceptConfig{Commands: []string{race}}, nil)
	out, log := r.accept(sha, "t-8")
	if out.Outcome != "pass" {
		t.Fatalf("outcome %s: %s\n%s", out.Outcome, out.Reason, log)
	}
	if !strings.Contains(log, "checking again on the new base") {
		t.Fatalf("the moved base was not checked again:\n%s", log)
	}
	if racer := r.git(r.origin, "log", "-1", "--format=%s", "test/base^1"); racer != "racer" {
		t.Fatalf("the merge is not on top of the moved base: first parent is %q", racer)
	}
}

// A repository without the acceptance file is merged by people: the step
// passes without a merge, so a reviewed route there goes on to close.
func TestAcceptWithoutDescriptionPassesWithoutMerge(t *testing.T) {
	r := newAcceptRig(t)
	r.git(r.dev, "checkout", "--quiet", "-B", "t-9", "origin/test/base")
	r.write(r.dev, "x.txt", "x\n")
	r.git(r.dev, "add", ".")
	r.git(r.dev, "commit", "--quiet", "-m", "no acceptance")
	r.git(r.dev, "push", "--quiet", "origin", "t-9")
	before := r.main()
	out, _ := r.accept(r.git(r.dev, "rev-parse", "HEAD"), "t-9")
	if out.Outcome != "pass" || !out.Skipped || !strings.Contains(out.Reason, acceptanceFile) {
		t.Fatalf("outcome %s skipped %v %q", out.Outcome, out.Skipped, out.Reason)
	}
	if r.main() != before {
		t.Fatal("a repository without acceptance got a merge")
	}
	if report := acceptReport(out); !strings.HasPrefix(report, "Приёмки нет") {
		t.Fatalf("report %q", report)
	}
	missing, _ := r.accept(strings.Repeat("ab", 20), "")
	if missing.Outcome != "needs_coordinator" {
		t.Fatalf("an unknown commit: %s", missing.Outcome)
	}
}

func TestAcceptCommandTimeoutStopsTheTree(t *testing.T) {
	r := newAcceptRig(t)
	pidFile := filepath.Join(t.TempDir(), "pid")
	sha := r.branch("t-10", acceptConfig{Commands: []string{"sleep 30 & echo $! > " + pidFile + "; wait"}, TimeoutSeconds: 1}, nil)
	start := time.Now()
	out, _ := r.accept(sha, "t-10")
	if out.Outcome != "changes_requested" || !strings.Contains(out.Tail, "timed out") || time.Since(start) > 20*time.Second {
		t.Fatalf("outcome %s tail %q after %s", out.Outcome, out.Tail, time.Since(start))
	}
	raw, _ := os.ReadFile(pidFile)
	pid := strings.TrimSpace(string(raw))
	if pid == "" {
		t.Fatal("the command did not record its child")
	}
	// Killed is gone or a zombie nobody reaped yet (a container's PID 1 may not).
	deadline := time.Now().Add(5 * time.Second)
	for {
		stat, err := os.ReadFile("/proc/" + pid + "/stat")
		if err != nil || strings.Contains(string(stat), ") Z ") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the command's child %s outlived the timeout: %s", pid, stat)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestAcceptReportNamesTheFailure(t *testing.T) {
	report := acceptReport(acceptOutcome{Outcome: "changes_requested", Reason: "command failed: make test", Tail: "FAIL x"})
	if !strings.Contains(report, "Слияния нет") || !strings.Contains(report, "FAIL x") {
		t.Fatalf("report %q", report)
	}
}

func TestRedactHidesSecretEnvironment(t *testing.T) {
	t.Setenv("DEPLOY_TOKEN", "s3cr3t-value")
	t.Setenv("PLAIN", "s3cr3t-value-2")
	if got := redact("a s3cr3t-value b"); got != "a *** b" {
		t.Fatalf("redact = %q", got)
	}
	env := strings.Join(acceptEnv(map[string]string{"X": "1"}), "\n")
	if !strings.Contains(env, "X=1") {
		t.Fatal("repository env is missing")
	}
	t.Setenv("MULTICA_TOKEN", "mat_x")
	if strings.Contains(strings.Join(acceptEnv(nil), "\n"), "MULTICA_TOKEN=") {
		t.Fatal("the task credential reached the commands")
	}
}

func TestAcceptRepositoryNamesTheWorkspaceRepository(t *testing.T) {
	r := newAcceptRig(t)
	root := t.TempDir()
	// A local repository without a remote, as a workspace repository, and a
	// worktree an executor worked in.
	local := filepath.Join(root, "service")
	r.git(root, "init", "--quiet", "--initial-branch=test/base", local)
	r.identity(local)
	r.write(local, "README.md", "hello\n")
	r.git(local, "add", ".")
	r.git(local, "commit", "--quiet", "-m", "init")
	wt := filepath.Join(root, "wt", "t-1")
	r.git(local, "worktree", "add", "--quiet", "-b", "t-1", wt)

	ctx := context.Background()
	resolve := func(p string) string {
		got := acceptRepository(ctx, p)
		if !strings.HasPrefix(got, "file://") {
			t.Fatalf("%q came back as %q, want a file:// URL", p, got)
		}
		real, err := filepath.EvalSymlinks(strings.TrimPrefix(got, "file://"))
		if err != nil {
			t.Fatal(err)
		}
		return real
	}
	want, _ := filepath.EvalSymlinks(local)
	if got := resolve(local); got != want {
		t.Fatalf("repository itself: %q, want %q", got, want)
	}
	if got := resolve(wt); got != want {
		t.Fatalf("worktree: %q, want %q", got, want)
	}
	if got := acceptRepository(ctx, r.dev); got != r.origin {
		t.Fatalf("checkout with origin: %q, want %q", got, r.origin)
	}
	for _, as := range []string{"git@github.com:o/r.git", "https://github.com/o/r.git", filepath.Join(root, "missing")} {
		if got := acceptRepository(ctx, as); got != as {
			t.Fatalf("%q came back as %q", as, got)
		}
	}
}

// In a repository with CI the platform merges: acceptance confirms the pinned
// commit reached the base and never merges, pushes or runs the commands.
func TestAcceptCIConfirmsTheCommitIsInTheBase(t *testing.T) {
	r := newAcceptRig(t)
	marker := filepath.Join(t.TempDir(), "ran")
	sha := r.branch("t-ci-1", acceptConfig{Mode: "ci", Commands: []string{"touch " + marker}}, map[string]string{"f.txt": "x\n"})

	// The platform's merge: a merge commit on the base, made outside acceptance.
	r.git(r.dev, "checkout", "--quiet", "-B", "platform", "origin/test/base")
	r.git(r.dev, "merge", "--quiet", "--no-ff", "-m", "platform merge", sha)
	r.git(r.dev, "push", "--quiet", "origin", "HEAD:test/base")
	before := r.main()

	out, log := r.accept(sha, "t-ci-1")
	if out.Outcome != "pass" || out.Skipped || out.Merged != before {
		t.Fatalf("outcome %s skipped %v merged %s: %s\n%s", out.Outcome, out.Skipped, out.Merged, out.Reason, log)
	}
	if r.main() != before {
		t.Fatal("CI acceptance changed the base")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("CI acceptance ran the acceptance commands")
	}
	if strings.Contains(log, "$ ") {
		t.Fatalf("CI acceptance ran a command:\n%s", log)
	}
}

func TestAcceptCIDoesNotPassACommitOutsideTheBase(t *testing.T) {
	r := newAcceptRig(t)
	marker := filepath.Join(t.TempDir(), "ran")
	sha := r.branch("t-ci-2", acceptConfig{Mode: "ci", Commands: []string{"touch " + marker}, WaitPollSeconds: 1, WaitMaxSeconds: 3}, nil)
	before := r.main()

	out, log := r.accept(sha, "t-ci-2")
	if out.Outcome != "blocked" || !strings.Contains(out.Reason, "not merged") {
		t.Fatalf("an unmerged commit: %s (%s)", out.Outcome, out.Reason)
	}
	if got := acceptWorkflowTarget(out, workflowAcceptView{Status: "merge", NextStatus: "done", BackStatus: "dev"}); got != "blocked" {
		t.Fatalf("an unmerged commit moves the issue to %q", got)
	}
	if strings.Count(log, "waiting") != 3 {
		t.Fatalf("the wait was not bounded by the configured limit:\n%s", log)
	}
	if r.main() != before {
		t.Fatal("CI acceptance merged or pushed")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("CI acceptance ran the acceptance commands")
	}
	if got := r.git(r.origin, "branch", "--list", "test/base", "--contains", sha); got != "" {
		t.Fatalf("the commit reached the base: %q", got)
	}
}

func TestAcceptCIWaitsForTheMerge(t *testing.T) {
	r := newAcceptRig(t)
	sha := r.branch("t-ci-3", acceptConfig{Mode: "ci", WaitPollSeconds: 1, WaitMaxSeconds: 10}, map[string]string{"f.txt": "x\n"})
	var log bytes.Buffer
	sleeps := 0
	out := runAcceptance(context.Background(), acceptSpec{
		Dir: r.work, Repo: r.origin, Commit: sha, Branch: "t-ci-3", Issue: "test-issue", Log: &log,
		Sleep: func(context.Context, time.Duration) error {
			if sleeps++; sleeps == 2 {
				r.git(r.dev, "checkout", "--quiet", "-B", "platform", "origin/test/base")
				r.git(r.dev, "merge", "--quiet", "--no-ff", "-m", "platform merge", sha)
				r.git(r.dev, "push", "--quiet", "origin", "HEAD:test/base")
			}
			return nil
		},
	})
	if out.Outcome != "pass" || sleeps != 2 {
		t.Fatalf("outcome %s after %d waits: %s\n%s", out.Outcome, sleeps, out.Reason, log.String())
	}
}

func TestAcceptRejectsAnUnknownMode(t *testing.T) {
	r := newAcceptRig(t)
	sha := r.branch("t-ci-4", acceptConfig{Mode: "squash", Commands: []string{"true"}}, nil)
	before := r.main()
	out, _ := r.accept(sha, "t-ci-4")
	if out.Outcome != "needs_coordinator" || r.main() != before {
		t.Fatalf("outcome %s (%s), base moved: %v", out.Outcome, out.Reason, r.main() != before)
	}
}
