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
	r.git(root, "init", "--quiet", "--bare", "--initial-branch=main", r.origin)
	r.git(root, "clone", "--quiet", r.origin, r.dev)
	r.identity(r.dev)
	r.write(r.dev, "README.md", "hello\n")
	r.git(r.dev, "add", ".")
	r.git(r.dev, "commit", "--quiet", "-m", "init")
	r.git(r.dev, "push", "--quiet", "origin", "main")
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
	r.git(r.dev, "checkout", "--quiet", "-B", name, "origin/main")
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
	r.git(r.dev, "checkout", "--quiet", "-B", "other-"+file, "origin/main")
	r.write(r.dev, file, file+"\n")
	r.git(r.dev, "add", ".")
	r.git(r.dev, "commit", "--quiet", "-m", "other "+file)
	r.git(r.dev, "push", "--quiet", "origin", "HEAD:main")
	return r.git(r.dev, "rev-parse", "HEAD")
}

func (r *acceptRig) main() string { return r.git(r.origin, "rev-parse", "main") }

func (r *acceptRig) accept(commit, branch string) (acceptOutcome, string) {
	var log bytes.Buffer
	out := runAcceptance(context.Background(), acceptSpec{
		Dir: r.work, Repo: r.origin, Commit: commit, Branch: branch, Issue: "STT-1", Log: &log,
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
	if parent := r.git(r.origin, "rev-parse", "main^2"); parent != sha {
		t.Fatalf("the merge brought %s, not the accepted %s", parent, sha)
	}
	if msg := r.git(r.origin, "log", "-1", "--format=%B", "main"); !strings.Contains(msg, "Merge t-1-feature into main") || !strings.Contains(msg, "STT-1") {
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
	race := `[ -f ` + marker + ` ] || { touch ` + marker + ` && cd ` + r.dev + ` && git fetch -q origin && git checkout -q -B racer origin/main && echo r > racer.txt && git add . && git -c user.name=t -c user.email=t@t commit -qm racer && git push -q origin HEAD:main; }`
	sha := r.branch("t-8", acceptConfig{Commands: []string{race}}, nil)
	out, log := r.accept(sha, "t-8")
	if out.Outcome != "pass" {
		t.Fatalf("outcome %s: %s\n%s", out.Outcome, out.Reason, log)
	}
	if !strings.Contains(log, "checking again on the new base") {
		t.Fatalf("the moved base was not checked again:\n%s", log)
	}
	if racer := r.git(r.origin, "log", "-1", "--format=%s", "main^1"); racer != "racer" {
		t.Fatalf("the merge is not on top of the moved base: first parent is %q", racer)
	}
}

func TestAcceptWithoutDescriptionAsksTheCoordinator(t *testing.T) {
	r := newAcceptRig(t)
	r.git(r.dev, "checkout", "--quiet", "-B", "t-9", "origin/main")
	r.write(r.dev, "x.txt", "x\n")
	r.git(r.dev, "add", ".")
	r.git(r.dev, "commit", "--quiet", "-m", "no acceptance")
	r.git(r.dev, "push", "--quiet", "origin", "t-9")
	out, _ := r.accept(r.git(r.dev, "rev-parse", "HEAD"), "t-9")
	if out.Outcome != "needs_coordinator" || !strings.Contains(out.Reason, acceptanceFile) {
		t.Fatalf("outcome %s %q", out.Outcome, out.Reason)
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

func TestAcceptResultIsARouteResult(t *testing.T) {
	sha := strings.Repeat("a", 40)
	res := acceptResult(acceptOutcome{Outcome: "changes_requested", Base: "main", Reason: "command failed: make test",
		Checks: []acceptCheck{{Name: "make test", Command: "make test"}}, Tail: "FAIL x"}, "git@host:r.git", sha)
	if res["outcome"] != "changes_requested" || res["schema_version"] != 1 {
		t.Fatalf("result %#v", res)
	}
	art := res["artifact"].(map[string]string)
	if art["commit"] != sha || art["repo"] != "git@host:r.git" || art["branch"] != "main" {
		t.Fatalf("artifact %#v", art)
	}
	if report := res["report"].(string); !strings.Contains(report, "Слияния нет") || !strings.Contains(report, "FAIL x") {
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
