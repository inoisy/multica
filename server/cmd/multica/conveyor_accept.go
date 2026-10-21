package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// acceptanceFile is where a repository describes its own acceptance, read
// from the commit under test so the checks change in the same review as code.
const acceptanceFile = ".multica/acceptance.json"

// acceptConfig is the content of .multica/acceptance.json.
type acceptConfig struct {
	// Base is the branch the commit is merged into; empty means the remote's
	// default branch.
	Base     string   `json:"base"`
	Commands []string `json:"commands"`
	// TimeoutSeconds bounds one attempt of one command; waiting for a busy
	// shared resource does not count against it.
	TimeoutSeconds int               `json:"timeout_seconds"`
	Env            map[string]string `json:"env"`
	// BusyExitCodes mean "a shared resource is busy, try again later".
	BusyExitCodes   []int `json:"busy_exit_codes"`
	WaitPollSeconds int   `json:"wait_poll_seconds"`
	WaitMaxSeconds  int   `json:"wait_max_seconds"`
	// DiffGuard lists regular expressions an added line must not match; nil
	// keeps the default list, an empty list turns the guard off.
	DiffGuard *[]string `json:"diff_guard"`
	// Retries is how many times a failed command is run again before the
	// acceptance is red; nil means defaultAcceptRetries. A full check on a
	// loaded server fails on timeouts in a different test each run (STT-366),
	// while a real regression fails every time.
	Retries *int `json:"retries"`
}

var defaultDiffGuard = []string{`: any\b`, `as unknown as`, `@ts-(?:ignore|expect-error)`, `eslint-disable`}

const (
	defaultAcceptTimeout  = 1500 * time.Second
	defaultAcceptWaitPoll = 60 * time.Second
	defaultAcceptWaitMax  = 7200 * time.Second
	acceptPushAttempts    = 3
	defaultAcceptRetries  = 1
	maxAcceptRetries      = 3
	acceptTailLines       = 60
)

// acceptSpec is one acceptance: the checkout to work in and what to accept.
type acceptSpec struct {
	Dir    string // checkout whose origin is the repository
	Repo   string // repository URL, reported back
	Commit string // the commit the route accepted
	Branch string // branch it came from, for the merge message
	Issue  string // issue identifier, for the merge message
	Log    io.Writer
	Sleep  func(context.Context, time.Duration) error
}

// acceptCheck is one line of the result: a guard or a command.
type acceptCheck struct {
	Name    string `json:"name"`
	Command string `json:"command,omitempty"`
	OK      bool   `json:"ok"`
}

// acceptOutcome maps onto a route result: pass, changes_requested (the work
// must change), blocked (a resource never freed up) or needs_coordinator
// (acceptance could not judge the work).
type acceptOutcome struct {
	Outcome string
	// Skipped: a pass with nothing to merge (no commit, no acceptance file).
	Skipped bool
	Base    string
	Merged  string
	Checks  []acceptCheck
	Reason  string
	Tail    string
}

// runAcceptance merges the accepted commit onto the current base, checks the
// result with the repository's own commands and pushes exactly the checked
// merge. A base that moved meanwhile is fetched and checked again.
func runAcceptance(ctx context.Context, spec acceptSpec) acceptOutcome {
	if spec.Sleep == nil {
		spec.Sleep = sleepCtx
	}
	g := gitIn{dir: spec.Dir, log: spec.Log}
	if _, err := g.run(ctx, "fetch", "--quiet", "origin"); err != nil {
		return coordinator("fetch failed: " + err.Error())
	}
	if _, err := g.run(ctx, "cat-file", "-e", spec.Commit+"^{commit}"); err != nil && spec.Branch != "" {
		_, _ = g.run(ctx, "fetch", "--quiet", "origin", spec.Branch)
	}
	sha, err := g.run(ctx, "rev-parse", "--verify", spec.Commit+"^{commit}")
	if err != nil {
		return coordinator(fmt.Sprintf("commit %s is not in the repository", spec.Commit))
	}
	raw, err := g.run(ctx, "show", sha+":"+acceptanceFile)
	if err != nil {
		// A repository without the file is merged by people (MR): the step passes and merges nothing.
		return nothingToAccept(fmt.Sprintf("the repository has no %s, it is merged by people", acceptanceFile))
	}
	var cfg acceptConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return coordinator(fmt.Sprintf("%s is not valid JSON: %v", acceptanceFile, err))
	}
	if len(cfg.Commands) == 0 {
		return coordinator(acceptanceFile + " lists no commands")
	}
	base := strings.TrimSpace(cfg.Base)
	if base == "" {
		base = g.defaultBranch(ctx)
	}
	guard := defaultDiffGuard
	if cfg.DiffGuard != nil {
		guard = *cfg.DiffGuard
	}
	env := acceptEnv(cfg.Env)

	for attempt := 1; attempt <= acceptPushAttempts; attempt++ {
		if _, err := g.run(ctx, "fetch", "--quiet", "origin", base); err != nil {
			return coordinator(fmt.Sprintf("fetch of %s failed: %v", base, err))
		}
		baseSHA, err := g.run(ctx, "rev-parse", "--verify", "origin/"+base)
		if err != nil {
			return coordinator(fmt.Sprintf("base branch %s not found", base))
		}
		if _, err := g.run(ctx, "merge-base", "--is-ancestor", sha, baseSHA); err == nil {
			return acceptOutcome{Outcome: "pass", Base: base, Merged: baseSHA,
				Checks: []acceptCheck{{Name: "already merged", OK: true}}, Reason: fmt.Sprintf("%s is already in %s", short(sha), base)}
		}
		out := acceptOutcome{Base: base}
		if lines := diffGuardHits(ctx, g, baseSHA, sha, guard); len(lines) > 0 {
			out.Checks = append(out.Checks, acceptCheck{Name: "diff guard", OK: false})
			out.Outcome, out.Reason, out.Tail = "changes_requested", "the diff adds forbidden lines", strings.Join(lines, "\n")
			return out
		}
		out.Checks = append(out.Checks, acceptCheck{Name: "diff guard", OK: true})
		if _, err := g.run(ctx, "checkout", "--quiet", "--detach", baseSHA); err != nil {
			return coordinator("checkout of the base failed: " + err.Error())
		}
		msg := fmt.Sprintf("Merge %s into %s", firstNonEmpty(spec.Branch, short(sha)), base)
		if spec.Issue != "" {
			msg += "\n\n" + spec.Issue
		}
		if _, err := g.run(ctx, append(identityArgs(ctx, g), "merge", "--no-ff", "--no-edit", "-m", msg, sha)...); err != nil {
			conflicts, _ := g.run(ctx, "diff", "--name-only", "--diff-filter=U")
			_, _ = g.run(ctx, "merge", "--abort")
			out.Checks = append(out.Checks, acceptCheck{Name: "merge onto " + base, OK: false})
			out.Outcome, out.Reason, out.Tail = "changes_requested", "the commit conflicts with "+base, conflicts
			return out
		}
		out.Checks = append(out.Checks, acceptCheck{Name: "merge onto " + base, OK: true})
		var flaky []string
		for _, command := range cfg.Commands {
			ok, busy, tail := runAcceptCommand(ctx, spec, cfg, env, command)
			for try := 1; !ok && busy == "" && try <= cfg.retries(); try++ {
				fmt.Fprintf(spec.Log, "command failed, running it again (%d/%d)\n", try, cfg.retries())
				if ok, busy, tail = runAcceptCommand(ctx, spec, cfg, env, command); ok {
					flaky = append(flaky, clipName(command))
				}
			}
			out.Checks = append(out.Checks, acceptCheck{Name: clipName(command), Command: clipCommand(command), OK: ok})
			if busy != "" {
				out.Outcome, out.Reason, out.Tail = "blocked", busy, tail
				return out
			}
			if !ok {
				out.Outcome, out.Reason, out.Tail = "changes_requested", "command failed: "+command, tail
				return out
			}
		}
		merged, err := g.run(ctx, "rev-parse", "HEAD")
		if err != nil {
			return coordinator("cannot read the merge commit: " + err.Error())
		}
		_, err = g.run(ctx, "push", "--quiet", "origin", merged+":refs/heads/"+base)
		if err == nil {
			out.Outcome, out.Merged = "pass", merged
			out.Reason = fmt.Sprintf("%s merged into %s as %s", short(sha), base, short(merged))
			if len(flaky) > 0 {
				out.Reason += "; red on the first run, green on a retry: " + strings.Join(flaky, ", ")
			}
			return out
		}
		if !pushRaced(err) {
			return coordinator(fmt.Sprintf("push to %s failed: %v", base, err))
		}
		fmt.Fprintf(spec.Log, "%s moved while checking; checking again on the new base (%d/%d)\n", base, attempt, acceptPushAttempts)
	}
	return coordinator(fmt.Sprintf("%s kept moving during %d checks", base, acceptPushAttempts))
}

// retries is how many times a failed command runs again.
func (c acceptConfig) retries() int {
	if c.Retries == nil {
		return defaultAcceptRetries
	}
	return min(max(*c.Retries, 0), maxAcceptRetries)
}

func coordinator(reason string) acceptOutcome {
	return acceptOutcome{Outcome: "needs_coordinator", Reason: reason}
}

// nothingToAccept passes the step without a merge: there is no commit, or the
// repository does not describe its acceptance.
func nothingToAccept(reason string) acceptOutcome {
	return acceptOutcome{Outcome: "pass", Skipped: true, Reason: reason}
}

// runAcceptCommand runs one command until it gives an answer. A busy exit
// code waits and repeats outside the timeout; busy is set when the wait ran
// out.
func runAcceptCommand(ctx context.Context, spec acceptSpec, cfg acceptConfig, env []string, command string) (ok bool, busy string, tail string) {
	timeout := secondsOr(cfg.TimeoutSeconds, defaultAcceptTimeout)
	poll := secondsOr(cfg.WaitPollSeconds, defaultAcceptWaitPoll)
	waitMax := secondsOr(cfg.WaitMaxSeconds, defaultAcceptWaitMax)
	var waited time.Duration
	for {
		fmt.Fprintf(spec.Log, "$ %s\n", command)
		buf := newLineTail(acceptTailLines)
		code, err := runShell(ctx, spec.Dir, command, env, timeout, io.MultiWriter(redactingWriter{w: spec.Log}, buf))
		tail = redact(buf.text())
		if err != nil {
			fmt.Fprintf(spec.Log, "%v\n", err)
			return false, "", tail + "\n" + err.Error()
		}
		if !containsInt(cfg.BusyExitCodes, code) {
			return code == 0, "", tail
		}
		if waited+poll > waitMax {
			return false, fmt.Sprintf("a shared resource stayed busy for %s: %s", waited, command), tail
		}
		fmt.Fprintf(spec.Log, "resource busy (exit %d), waiting %s\n", code, poll)
		if err := spec.Sleep(ctx, poll); err != nil {
			return false, "", tail + "\n" + err.Error()
		}
		waited += poll
	}
}

// runShell runs command with sh in dir and returns its exit code. A timeout
// kills the whole process group and is an error, as is a command that could
// not start.
func runShell(ctx context.Context, dir, command string, env []string, timeout time.Duration, out io.Writer) (int, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "sh", "-c", command)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr, cmd.Stdin = dir, env, out, out, nil
	ownProcessGroup(cmd)
	cmd.WaitDelay = 10 * time.Second
	err := cmd.Run()
	if runCtx.Err() == context.DeadlineExceeded {
		return -1, fmt.Errorf("timed out after %s", timeout)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

func diffGuardHits(ctx context.Context, g gitIn, baseSHA, sha string, patterns []string) []string {
	if len(patterns) == 0 {
		return nil
	}
	re, err := regexp.Compile(`^\+.*(?:` + strings.Join(patterns, "|") + `)`)
	if err != nil {
		return []string{"invalid diff_guard: " + err.Error()}
	}
	diff, err := g.run(ctx, "diff", "--unified=0", baseSHA+"..."+sha)
	if err != nil {
		return []string{"diff failed: " + err.Error()}
	}
	var hits []string
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+++") {
			continue
		}
		if re.MatchString(line) {
			hits = append(hits, line)
		}
	}
	return hits
}

// gitIn runs git in one checkout and logs each command.
type gitIn struct {
	dir string
	log io.Writer
}

func (g gitIn) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = g.dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return strings.TrimSpace(stdout.String()), errors.New(redact(msg))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (g gitIn) defaultBranch(ctx context.Context) string {
	if ref, err := g.run(ctx, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		return strings.TrimPrefix(ref, "origin/")
	}
	return "main"
}

// identityArgs names the merge's committer when the host has no git
// identity, so a fresh machine can still accept.
func identityArgs(ctx context.Context, g gitIn) []string {
	if name, _ := g.run(ctx, "config", "user.name"); name != "" {
		return nil
	}
	who := firstNonEmpty(os.Getenv("MULTICA_AGENT_NAME"), "multica accept")
	return []string{"-c", "user.name=" + who, "-c", "user.email=accept@multica.invalid"}
}

func pushRaced(err error) bool {
	s := err.Error()
	return strings.Contains(s, "non-fast-forward") || strings.Contains(s, "fetch first") || strings.Contains(s, "[rejected]")
}

// acceptEnv is the commands' environment: the run's own, minus the task
// credential, plus what the repository asks for.
func acceptEnv(extra map[string]string) []string {
	env := make([]string, 0, len(os.Environ())+len(extra))
	for _, kv := range os.Environ() {
		if key, _, _ := strings.Cut(kv, "="); key == "MULTICA_TOKEN" {
			continue
		}
		env = append(env, kv)
	}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

var secretEnvName = regexp.MustCompile(`(?i)token|secret|password|passwd|credential|api_?key|private_?key`)

// redact hides the values of secret-looking environment variables from what
// the run shows.
func redact(text string) string {
	for _, kv := range os.Environ() {
		key, value, _ := strings.Cut(kv, "=")
		if len(value) >= 6 && secretEnvName.MatchString(key) {
			text = strings.ReplaceAll(text, value, "***")
		}
	}
	return text
}

// redactingWriter redacts each write; a secret split across two writes of a
// streaming command can slip through, the tail in the report is redacted whole.
type redactingWriter struct{ w io.Writer }

func (r redactingWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(r.w, redact(string(p))); err != nil {
		return 0, err
	}
	return len(p), nil
}

// lineTail keeps the last lines written to it.
type lineTail struct {
	max   int
	lines []string
	part  string
}

func newLineTail(max int) *lineTail { return &lineTail{max: max} }

func (t *lineTail) Write(p []byte) (int, error) {
	parts := strings.Split(t.part+string(p), "\n")
	t.part = parts[len(parts)-1]
	t.lines = append(t.lines, parts[:len(parts)-1]...)
	if len(t.lines) > t.max {
		t.lines = t.lines[len(t.lines)-t.max:]
	}
	return len(p), nil
}

func (t *lineTail) text() string {
	lines := t.lines
	if t.part != "" {
		lines = append(append([]string{}, lines...), t.part)
	}
	return strings.Join(lines, "\n")
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func secondsOr(n int, def time.Duration) time.Duration {
	if n > 0 {
		return time.Duration(n) * time.Second
	}
	return def
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func clipName(command string) string {
	if r := []rune(command); len(r) > 100 {
		return string(r[:99]) + "…"
	}
	return command
}

func clipCommand(command string) string {
	if r := []rune(command); len(r) > 500 {
		return string(r[:499]) + "…"
	}
	return command
}

// acceptRepository turns the repository a dev step named into the one the
// workspace knows. Executors working in a local checkout often name the
// directory they worked in: its origin when it has one, otherwise the main
// repository of the worktree as a file:// URL. Anything else (a URL, a path that is not a git
// checkout) is returned as given.
func acceptRepository(ctx context.Context, repo string) string {
	repo = strings.TrimSpace(repo)
	if !filepath.IsAbs(repo) {
		return repo
	}
	if info, err := os.Stat(repo); err != nil || !info.IsDir() {
		return repo
	}
	git := func(args ...string) string {
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	if origin := git("remote", "get-url", "origin"); origin != "" {
		return origin
	}
	common := git("rev-parse", "--path-format=absolute", "--git-common-dir")
	if common == "" {
		return repo
	}
	if filepath.Base(common) == ".git" {
		common = filepath.Dir(common)
	}
	return "file://" + common
}
