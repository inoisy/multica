package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

// commitOn adds a commit to a pushed branch: files to write, paths to remove.
func (r *acceptRig) commitOn(branch string, files map[string]string, remove ...string) string {
	r.t.Helper()
	r.git(r.dev, "checkout", "--quiet", branch)
	for f, c := range files {
		r.write(r.dev, f, c)
	}
	for _, f := range remove {
		r.git(r.dev, "rm", "--quiet", f)
	}
	r.git(r.dev, "add", ".")
	r.git(r.dev, "commit", "--quiet", "--allow-empty", "-m", "more on "+branch)
	r.git(r.dev, "push", "--quiet", "origin", branch)
	return r.git(r.dev, "rev-parse", "HEAD")
}

// carryRig is a branch qa and review judged, in a repository whose
// acceptance lets verdicts carry over docs/**.
func carryRig(t *testing.T) (*acceptRig, string) {
	t.Helper()
	r := newAcceptRig(t)
	judged := r.branch("t-2-adr", acceptConfig{Commands: []string{"true"}, VerdictCarryPaths: []string{"docs/**"}},
		map[string]string{"src/app.go": "package app\n", "docs/adr/0031-x.md": "# ADR\n"})
	return r, judged
}

func (r *acceptRig) acceptWith(commit string, sources ...carrySource) acceptOutcome {
	r.t.Helper()
	return runAcceptance(context.Background(), acceptSpec{Dir: r.work, Repo: r.origin, Commit: commit, Branch: "t-2-adr", Issue: "STT-2", Log: &strings.Builder{},
		Sleep: func(context.Context, time.Duration) error { return nil }, Carry: sources})
}

func TestAcceptCarriesVerdictsOverTheSameTree(t *testing.T) {
	r, judged := carryRig(t)
	// A rebase or a dropped trailer: another commit, the same tree.
	same := r.commitOn("t-2-adr", nil)
	out := r.acceptWith(same, carrySource{Step: "qa", Commit: judged, Pass: true}, carrySource{Step: "review", Commit: same, Pass: true})
	if out.Outcome != "pass" || out.Carry == nil || len(out.Carry.Carried) != 1 || out.Carry.Carried[0].Step != "qa" || len(out.Carry.Carried[0].Files) != 0 {
		t.Fatalf("same tree: %s %s %+v", out.Outcome, out.Reason, out.Carry)
	}
	if parent := r.git(r.origin, "rev-parse", "main^2"); parent != same {
		t.Fatalf("merged %s, not the accepted %s", parent, same)
	}
}

func TestAcceptCarriesVerdictsOverDocsOnly(t *testing.T) {
	r, judged := carryRig(t)
	renamed := r.commitOn("t-2-adr", map[string]string{"docs/adr/0032-x.md": "# ADR\n", "docs/reports-contract.md": "see 0032\n"}, "docs/adr/0031-x.md")
	out := r.acceptWith(renamed, carrySource{Step: "qa", Commit: judged, Pass: true}, carrySource{Step: "review", Commit: judged, Pass: true})
	if out.Outcome != "pass" || out.Carry == nil || len(out.Carry.Carried) != 2 {
		t.Fatalf("docs only: %s %s %+v", out.Outcome, out.Reason, out.Carry)
	}
	want := []string{"docs/adr/0031-x.md", "docs/adr/0032-x.md", "docs/reports-contract.md"}
	if got := out.Carry.Carried[0].Files; !reflect.DeepEqual(got, want) {
		t.Fatalf("carried files %v, want %v", got, want)
	}
	if !reflect.DeepEqual(out.Carry.Paths, []string{"docs/**"}) {
		t.Fatalf("paths %v", out.Carry.Paths)
	}
	res := acceptResult(out, r.origin, renamed)
	if c, ok := res["carry"].(map[string]any); !ok || c["carried"] == nil || !strings.Contains(res["report"].(string), "Вердикт qa перенесён") {
		t.Fatalf("result %+v", res)
	}
}

func TestAcceptSendsCodeBackToTheVerifier(t *testing.T) {
	r, judged := carryRig(t)
	before := r.main()
	for name, tc := range map[string]struct {
		files  map[string]string
		remove []string
		want   []string
	}{
		"code":               {files: map[string]string{"src/app.go": "package app\n\nvar x = 1\n", "docs/a.md": "a\n"}, want: []string{"src/app.go"}},
		"code moved to docs": {files: map[string]string{"docs/app.go": "package app\n"}, remove: []string{"src/app.go"}, want: []string{"src/app.go"}},
		// The accepted commit may not widen the list it is judged by.
		"own carry list": {files: map[string]string{acceptanceFile: `{"commands":["true"],"verdict_carry_paths":["**"]}`}, want: []string{acceptanceFile}},
	} {
		t.Run(name, func(t *testing.T) {
			r.git(r.dev, "checkout", "--quiet", "-B", "t-2-adr", judged)
			r.git(r.dev, "push", "--quiet", "--force", "origin", "t-2-adr")
			changed := r.commitOn("t-2-adr", tc.files, tc.remove...)
			out := r.acceptWith(changed, carrySource{Step: "qa", Commit: judged, Pass: true}, carrySource{Step: "review", Commit: judged, Pass: true})
			if out.Outcome != "changes_requested" || out.Carry == nil || out.Carry.Reverify != "qa" || !reflect.DeepEqual(out.Carry.Files, tc.want) {
				t.Fatalf("%s %s %+v", out.Outcome, out.Reason, out.Carry)
			}
			if r.main() != before {
				t.Fatalf("main moved")
			}
		})
	}
}

func TestAcceptSendsBackAVerdictThatWasNotAPass(t *testing.T) {
	r, judged := carryRig(t)
	same := r.commitOn("t-2-adr", nil)
	out := r.acceptWith(same, carrySource{Step: "qa", Commit: judged, Pass: true}, carrySource{Step: "review", Commit: judged, Pass: false})
	if out.Outcome != "changes_requested" || out.Carry == nil || out.Carry.Reverify != "review" {
		t.Fatalf("%s %s %+v", out.Outcome, out.Reason, out.Carry)
	}
}

func TestMatchCarryPath(t *testing.T) {
	for _, tc := range []struct {
		pattern, file string
		want          bool
	}{
		{"docs/**", "docs/adr/0032-x.md", true},
		{"docs/**", "docs.md", false},
		{"docs/*", "docs/adr/x.md", false},
		{"**/*.md", "README.md", true},
		{"**/*.md", "a/b/c.md", true},
		{"*.md", "a/b.md", false},
		{"/docs/**", "docs/x", true},
		{"", "x", false},
	} {
		if got := matchCarryPath(tc.pattern, tc.file); got != tc.want {
			t.Errorf("%q ~ %q = %v, want %v", tc.pattern, tc.file, got, tc.want)
		}
	}
}

// The accept run reads the latest verdict of each verifier since the route
// was enabled, and checks them only when the route owed it as a carry check.
func TestCarrySourcesReadTheJournal(t *testing.T) {
	journal := func(carry bool) []map[string]any {
		return []map[string]any{
			{"seq": 1, "kind": "result", "idx": 1, "action": "pass", "details": map[string]any{"artifact": map[string]any{"commit": "old0000"}}},
			{"seq": 2, "kind": "state", "action": "enable"},
			{"seq": 3, "kind": "result", "idx": 1, "action": "pass", "details": map[string]any{"artifact": map[string]any{"commit": "AAAA111"}}},
			{"seq": 4, "kind": "result", "idx": 2, "action": "changes_requested", "details": map[string]any{"artifact": map[string]any{"commit": "aaaa111"}}},
			{"seq": 5, "kind": "result", "idx": 2, "action": "pass", "details": map[string]any{"artifact": map[string]any{"commit": "bbbb222"}}},
			{"seq": 6, "kind": "action", "idx": 3, "action": "run_step", "task_id": "task-1", "details": map[string]any{"carry": carry}},
		}
	}
	view := acceptRouteView{}
	for _, s := range []string{"dev", "qa", "review", "accept", "close"} {
		view.Route = append(view.Route, struct {
			Step string `json:"step"`
		}{s})
	}
	for _, carry := range []bool{true, false} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			events := journal(carry)
			if r.URL.Query().Get("cursor") != "0" {
				events = nil
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"events": events, "next_cursor": "6"})
		}))
		client := cli.NewAPIClient(srv.URL, "ws-1", "token")
		got, err := carrySources(context.Background(), client, "/api/issues/x", view, "task-1")
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		want := []carrySource{{Step: "qa", Commit: "aaaa111", Pass: true}, {Step: "review", Commit: "bbbb222", Pass: true}}
		if !carry {
			want = nil
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("carry=%v: %+v, want %+v", carry, got, want)
		}
	}
}

// A run's result names the repository's carry paths read from its commit, so
// the route may send the next work straight to acceptance.
func TestResultHintsTheCarryPaths(t *testing.T) {
	r, judged := carryRig(t)
	r.git(r.dev, "checkout", "--quiet", judged)
	body := json.RawMessage(`{"schema_version":1,"outcome":"pass","artifact":{"repo":"` + r.dev + `","commit":"` + judged + `"},"report":"ok"}`)
	var got struct {
		Carry struct {
			Paths []string `json:"paths"`
		} `json:"carry"`
	}
	if err := json.Unmarshal(withCarryHint(context.Background(), body), &got); err != nil || !reflect.DeepEqual(got.Carry.Paths, []string{"docs/**"}) {
		t.Fatalf("hint: %v %+v", err, got)
	}
	main := r.git(r.dev, "rev-parse", "origin/main")
	plain := json.RawMessage(`{"artifact":{"repo":"` + r.dev + `","commit":"` + main + `"}}`)
	if out := withCarryHint(context.Background(), plain); string(out) != string(plain) {
		t.Fatalf("a commit without carry paths changed the result: %s", out)
	}
}
