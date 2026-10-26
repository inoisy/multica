package daemon

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

type fakeScanner struct{ procs []procInfo }

func (f *fakeScanner) Scan() ([]procInfo, error) { return append([]procInfo(nil), f.procs...), nil }

func newTestGuard(cfg RunMemoryConfig, procs []procInfo) (*runMemoryGuard, *[]int) {
	var killed []int
	g := &runMemoryGuard{
		cfg:     cfg,
		scanner: &fakeScanner{procs: procs},
		kill:    func(pid int) error { killed = append(killed, pid); return nil },
		selfPID: 100,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		now:     time.Now,
		heavy:   map[string]bool{},
	}
	return g, &killed
}

const mb = 1 << 20

func TestWithNodeHeapLimit(t *testing.T) {
	cases := []struct {
		existing string
		mb       int
		want     string
	}{
		{"", 2048, "--max-old-space-size=2048"},
		{"--trace-warnings", 2048, "--trace-warnings --max-old-space-size=2048"},
		{"--max-old-space-size=8192", 2048, "--max-old-space-size=8192"},
		{"--trace-warnings", 0, "--trace-warnings"},
		{"", 0, ""},
	}
	for _, c := range cases {
		if got := withNodeHeapLimit(c.existing, c.mb); got != c.want {
			t.Errorf("withNodeHeapLimit(%q, %d) = %q, want %q", c.existing, c.mb, got, c.want)
		}
	}
}

func TestGroupRunsChargesUntaggedChildToItsRun(t *testing.T) {
	procs := []procInfo{
		{PID: 100, PPID: 1, Comm: "multica", AnonBytes: 500 * mb},
		{PID: 200, PPID: 100, Comm: "claude", AnonBytes: 300 * mb, TaskID: "task-a"},
		{PID: 201, PPID: 200, Comm: "sh", AnonBytes: 1 * mb},
		{PID: 202, PPID: 201, Comm: "next-server", AnonBytes: 900 * mb}, // env scrubbed
		{PID: 300, PPID: 100, Comm: "claude", AnonBytes: 400 * mb, TaskID: "task-b"},
		{PID: 400, PPID: 1, Comm: "unrelated", AnonBytes: 9000 * mb},
	}
	runs := groupRuns(procs, 100)
	if len(runs) != 2 || runs[0].id != "task-a" || runs[0].bytes != 1201*mb || runs[1].id != "task-b" {
		t.Fatalf("unexpected runs: %+v", runs)
	}
}

func TestRunOverLimitLosesHeaviestProcessOnly(t *testing.T) {
	procs := []procInfo{
		{PID: 200, PPID: 100, Comm: "claude", AnonBytes: 900 * mb, TaskID: "hog"},
		{PID: 201, PPID: 200, Comm: "next-server", AnonBytes: 700 * mb, TaskID: "hog"},
		{PID: 202, PPID: 200, Comm: "tsc", AnonBytes: 200 * mb, TaskID: "hog"},
		{PID: 300, PPID: 100, Comm: "claude", AnonBytes: 400 * mb, TaskID: "calm"},
	}
	g, killed := newTestGuard(RunMemoryConfig{MaxBytes: 1500 * mb, HighBytes: 1000 * mb}, procs)
	g.tick()
	if len(*killed) != 1 || (*killed)[0] != 201 {
		t.Fatalf("killed = %v, want only next-server 201 (the agent CLI 200 is a daemon child and is spared)", *killed)
	}
	st := g.Snapshot()
	if st == nil || st.KillCount != 1 || st.LastKill.TaskID != "hog" || st.LastKill.Reason != "run_limit" {
		t.Fatalf("snapshot = %+v", st)
	}
	if len(st.Runs) != 2 || st.Runs[0].TaskID != "hog" || !st.Runs[0].Heavy {
		t.Fatalf("runs = %+v", st.Runs)
	}
}

func TestRunUnderLimitIsLeftAlone(t *testing.T) {
	procs := []procInfo{{PID: 200, PPID: 100, Comm: "claude", AnonBytes: 100 * mb, TaskID: "a"}}
	g, killed := newTestGuard(RunMemoryConfig{MaxBytes: 1500 * mb}, procs)
	g.tick()
	if len(*killed) != 0 {
		t.Fatalf("killed = %v", *killed)
	}
}

func TestLoneAgentProcessIsKilledWhenItAloneExceedsLimit(t *testing.T) {
	procs := []procInfo{{PID: 200, PPID: 100, Comm: "claude", AnonBytes: 2000 * mb, TaskID: "a"}}
	g, killed := newTestGuard(RunMemoryConfig{MaxBytes: 1500 * mb}, procs)
	g.tick()
	if len(*killed) != 1 || (*killed)[0] != 200 {
		t.Fatalf("killed = %v", *killed)
	}
}

func TestDaemonIsNeverKilled(t *testing.T) {
	procs := []procInfo{
		{PID: 100, PPID: 1, Comm: "multica", AnonBytes: 5000 * mb, TaskID: "self"},
	}
	g, killed := newTestGuard(RunMemoryConfig{MaxBytes: 10 * mb}, procs)
	g.tick()
	if len(*killed) != 0 {
		t.Fatalf("killed = %v", *killed)
	}
}

func TestUnitPressureShedsHeaviestRunOnceWithCooldown(t *testing.T) {
	procs := []procInfo{
		{PID: 201, PPID: 100, Comm: "a-cli", AnonBytes: 100 * mb, TaskID: "a"},
		{PID: 202, PPID: 201, Comm: "chrome", AnonBytes: 1200 * mb, TaskID: "a"},
		{PID: 301, PPID: 100, Comm: "b-cli", AnonBytes: 900 * mb, TaskID: "b"},
	}
	g, killed := newTestGuard(RunMemoryConfig{MaxBytes: 5000 * mb, UnitGuardPercent: 90}, procs)
	g.unit = func() (unitMemory, bool) { return unitMemory{AnonBytes: 950 * mb, HighBytes: 1000 * mb}, true }
	g.tick()
	g.tick() // within the cooldown: no second kill
	if len(*killed) != 1 || (*killed)[0] != 202 {
		t.Fatalf("killed = %v, want [202]", *killed)
	}
	if g.Snapshot().LastKill.Reason != "unit_pressure" {
		t.Fatalf("reason = %q", g.Snapshot().LastKill.Reason)
	}
}

func TestUnitBelowThresholdKillsNothing(t *testing.T) {
	procs := []procInfo{{PID: 202, PPID: 100, Comm: "chrome", AnonBytes: 1200 * mb, TaskID: "a"}}
	g, killed := newTestGuard(RunMemoryConfig{UnitGuardPercent: 90}, procs)
	g.unit = func() (unitMemory, bool) { return unitMemory{AnonBytes: 800 * mb, HighBytes: 1000 * mb}, true }
	g.tick()
	if len(*killed) != 0 {
		t.Fatalf("killed = %v", *killed)
	}
}
