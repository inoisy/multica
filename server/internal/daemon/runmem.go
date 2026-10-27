package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Per-run memory guard.
//
// A run that starts a heavy dev stack can push the whole service cgroup to
// MemoryHigh. The kernel then throttles every process in it, the daemon
// included, while the host still has free RAM, so host-level killers such as
// earlyoom never fire.
//
// The guard works from user space and needs no cgroup delegation: every
// process the daemon starts for a run inherits MULTICA_TASK_ID, so a scan of
// /proc attributes anonymous memory to runs. A run above its limit loses its
// heaviest process (what the kernel OOM killer would do inside a per-run
// cgroup), well before the unit-wide MemoryHigh. When the unit as a whole gets
// close to MemoryHigh, the heaviest process of the heaviest run is killed
// instead. The scan runs on its own goroutine and publishes a snapshot, so
// /health only copies it and never touches /proc.

const (
	// DefaultRunMemoryCheckInterval is how often /proc is scanned.
	DefaultRunMemoryCheckInterval = 5 * time.Second
	// DefaultRunMemoryHighMB is where a run starts being logged as heavy.
	DefaultRunMemoryHighMB = 3072
	// DefaultRunMemoryMaxMB is where a run loses its heaviest process.
	DefaultRunMemoryMaxMB = 5120
	// DefaultRunNodeMaxOldSpaceMB caps the V8 old generation of Node
	// processes (next dev, tsc, vite) started by a run.
	DefaultRunNodeMaxOldSpaceMB = 3072
	// DefaultUnitMemoryGuardPercent is the share of the unit's MemoryHigh
	// (anonymous memory) at which the guard sheds the heaviest process.
	DefaultUnitMemoryGuardPercent = 90

	runMemoryKillCooldown = 20 * time.Second
	runMemoryTopRuns      = 10
)

// RunMemoryConfig is the guard's tuning. A zero Interval disables the guard.
type RunMemoryConfig struct {
	Interval          time.Duration
	HighBytes         uint64 // log a run as heavy; 0 = off
	MaxBytes          uint64 // kill the run's heaviest process; 0 = off
	UnitGuardPercent  int    // 0 = off
	NodeMaxOldSpaceMB int    // NODE_OPTIONS default for runs; 0 = off
}

func runMemoryConfigFromEnv() (RunMemoryConfig, error) {
	interval, err := durationFromEnv("MULTICA_RUN_MEMORY_CHECK_INTERVAL", DefaultRunMemoryCheckInterval)
	if err != nil {
		return RunMemoryConfig{}, err
	}
	high, err := intFromEnv("MULTICA_RUN_MEMORY_HIGH_MB", DefaultRunMemoryHighMB)
	if err != nil {
		return RunMemoryConfig{}, err
	}
	max, err := intFromEnv("MULTICA_RUN_MEMORY_MAX_MB", DefaultRunMemoryMaxMB)
	if err != nil {
		return RunMemoryConfig{}, err
	}
	guard, err := intFromEnv("MULTICA_UNIT_MEMORY_GUARD_PERCENT", DefaultUnitMemoryGuardPercent)
	if err != nil {
		return RunMemoryConfig{}, err
	}
	node, err := intFromEnv("MULTICA_RUN_NODE_MAX_OLD_SPACE_MB", DefaultRunNodeMaxOldSpaceMB)
	if err != nil {
		return RunMemoryConfig{}, err
	}
	cfg := RunMemoryConfig{
		Interval:          interval,
		NodeMaxOldSpaceMB: max0(node),
		UnitGuardPercent:  clampPercent(guard),
	}
	if high > 0 {
		cfg.HighBytes = uint64(high) << 20
	}
	if max > 0 {
		cfg.MaxBytes = uint64(max) << 20
	}
	return cfg, nil
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

func clampPercent(n int) int {
	if n < 0 {
		return 0
	}
	if n > 100 {
		return 100
	}
	return n
}

// withNodeHeapLimit returns NODE_OPTIONS for a run: the daemon's own value plus
// --max-old-space-size, unless the value already sets one (an operator or the
// agent's custom_env then keeps control).
func withNodeHeapLimit(existing string, mb int) string {
	if mb <= 0 || strings.Contains(existing, "--max-old-space-size") {
		return existing
	}
	limit := "--max-old-space-size=" + strconv.Itoa(mb)
	if strings.TrimSpace(existing) == "" {
		return limit
	}
	return strings.TrimSpace(existing) + " " + limit
}

// procInfo is one process as seen by a scan.
type procInfo struct {
	PID       int
	PPID      int
	Comm      string
	AnonBytes uint64 // RssAnon + RssShmem: memory that cannot simply be dropped
	TaskID    string // MULTICA_TASK_ID of the process itself, "" when untagged
}

// procScanner lists the processes of the daemon's user.
type procScanner interface {
	Scan() ([]procInfo, error)
}

// unitMemory is the cgroup the daemon lives in.
type unitMemory struct {
	AnonBytes uint64
	HighBytes uint64 // memory.high, or memory.max when high is unlimited; 0 = no limit
	MaxBytes  uint64
}

// unitMemoryReader reports the daemon's cgroup; ok is false when unavailable.
type unitMemoryReader func() (unitMemory, bool)

// RunMemoryProcess is a process named in /health.
type RunMemoryProcess struct {
	PID   int    `json:"pid"`
	Comm  string `json:"comm"`
	Bytes uint64 `json:"bytes"`
}

// RunMemoryUsage is one run's memory in /health.
type RunMemoryUsage struct {
	TaskID    string             `json:"task_id"`
	Bytes     uint64             `json:"bytes"`
	Processes int                `json:"processes"`
	Top       []RunMemoryProcess `json:"top"`
	Heavy     bool               `json:"heavy,omitempty"`
}

// RunMemoryKill records a process the guard stopped.
type RunMemoryKill struct {
	At      string `json:"at"`
	TaskID  string `json:"task_id"`
	PID     int    `json:"pid"`
	Comm    string `json:"comm"`
	Bytes   uint64 `json:"bytes"`
	Reason  string `json:"reason"`
	RunSize uint64 `json:"run_bytes"`
}

// RunMemoryStatus is the guard's part of /health and `daemon status`.
type RunMemoryStatus struct {
	UpdatedAt      string           `json:"updated_at"`
	RunHighBytes   uint64           `json:"run_high_bytes,omitempty"`
	RunMaxBytes    uint64           `json:"run_max_bytes,omitempty"`
	UnitAnonBytes  uint64           `json:"unit_anon_bytes,omitempty"`
	UnitHighBytes  uint64           `json:"unit_high_bytes,omitempty"`
	NodeHeapMB     int              `json:"node_max_old_space_mb,omitempty"`
	Runs           []RunMemoryUsage `json:"runs"`
	KillCount      int64            `json:"kill_count"`
	LastKill       *RunMemoryKill   `json:"last_kill,omitempty"`
	ScanError      string           `json:"scan_error,omitempty"`
	TotalRunsBytes uint64           `json:"total_runs_bytes"`
}

type runMemoryGuard struct {
	cfg      RunMemoryConfig
	scanner  procScanner
	unit     unitMemoryReader
	kill     func(pid int) error
	selfPID  int
	logger   *slog.Logger
	now      func() time.Time
	mu       sync.Mutex
	status   RunMemoryStatus
	heavy    map[string]bool
	lastKill time.Time
}

func newRunMemoryGuard(cfg RunMemoryConfig, logger *slog.Logger) *runMemoryGuard {
	return &runMemoryGuard{
		cfg:     cfg,
		scanner: newProcScanner(),
		unit:    readUnitMemory,
		kill:    killProcess,
		selfPID: selfPID(),
		logger:  logger,
		now:     time.Now,
		heavy:   map[string]bool{},
	}
}

// Run scans until ctx ends.
func (g *runMemoryGuard) Run(ctx context.Context) {
	if g.cfg.Interval <= 0 {
		g.logger.Info("run memory guard: disabled")
		return
	}
	if g.scanner == nil {
		g.logger.Info("run memory guard: not supported on this platform")
		return
	}
	g.logger.Info("run memory guard: started",
		"interval", g.cfg.Interval,
		"run_high_mb", g.cfg.HighBytes>>20,
		"run_max_mb", g.cfg.MaxBytes>>20,
		"unit_guard_percent", g.cfg.UnitGuardPercent,
		"node_max_old_space_mb", g.cfg.NodeMaxOldSpaceMB,
	)
	ticker := time.NewTicker(g.cfg.Interval)
	defer ticker.Stop()
	for {
		g.tick()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Snapshot returns a copy of the last scan, nil before the first one.
func (g *runMemoryGuard) Snapshot() *RunMemoryStatus {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.status.UpdatedAt == "" {
		return nil
	}
	out := g.status
	out.Runs = append([]RunMemoryUsage(nil), g.status.Runs...)
	if g.status.LastKill != nil {
		k := *g.status.LastKill
		out.LastKill = &k
	}
	return &out
}

type runAccount struct {
	id    string
	bytes uint64
	procs []procInfo
}

func (g *runMemoryGuard) tick() {
	procs, err := g.scanner.Scan()
	now := g.now()

	g.mu.Lock()
	defer g.mu.Unlock()
	g.status.UpdatedAt = now.UTC().Format(time.RFC3339)
	g.status.RunHighBytes = g.cfg.HighBytes
	g.status.RunMaxBytes = g.cfg.MaxBytes
	g.status.NodeHeapMB = g.cfg.NodeMaxOldSpaceMB
	if err != nil {
		g.status.ScanError = err.Error()
		return
	}
	g.status.ScanError = ""

	runs := groupRuns(procs, g.selfPID)
	var total uint64
	for _, r := range runs {
		total += r.bytes
	}
	g.status.TotalRunsBytes = total

	// Per-run limit: shed the heaviest process of each run above MaxBytes.
	killed := false
	for _, r := range runs {
		if g.cfg.MaxBytes > 0 && r.bytes > g.cfg.MaxBytes {
			if g.shed(r, "run_limit", now) {
				killed = true
			}
		}
		g.noteHeavy(r)
	}
	// Forget finished runs so the heavy set does not grow with every task.
	for id := range g.heavy {
		if !hasRun(runs, id) {
			delete(g.heavy, id)
		}
	}

	// Unit-wide guard: the sum of runs is about to reach the unit's MemoryHigh.
	g.status.UnitAnonBytes, g.status.UnitHighBytes = 0, 0
	if g.unit != nil {
		if u, ok := g.unit(); ok {
			g.status.UnitAnonBytes, g.status.UnitHighBytes = u.AnonBytes, u.HighBytes
			if !killed && g.cfg.UnitGuardPercent > 0 && u.HighBytes > 0 && len(runs) > 0 &&
				u.AnonBytes*100 >= u.HighBytes*uint64(g.cfg.UnitGuardPercent) &&
				now.Sub(g.lastKill) >= runMemoryKillCooldown {
				g.shed(runs[0], "unit_pressure", now)
			}
		}
	}

	g.status.Runs = g.status.Runs[:0]
	for i, r := range runs {
		if i >= runMemoryTopRuns {
			break
		}
		g.status.Runs = append(g.status.Runs, r.usage(g.heavy[r.id]))
	}
}

// noteHeavy logs a run once when it crosses HighBytes and again after it has
// fallen well below, so a steady heavy run does not flood the log.
func (g *runMemoryGuard) noteHeavy(r *runAccount) {
	if g.cfg.HighBytes == 0 {
		return
	}
	switch {
	case r.bytes >= g.cfg.HighBytes && !g.heavy[r.id]:
		g.heavy[r.id] = true
		g.logger.Warn("run memory guard: run uses a lot of memory",
			"task_id", r.id, "anon_mb", r.bytes>>20, "processes", len(r.procs),
			"top", describeTop(r.top(3)))
	case r.bytes < g.cfg.HighBytes/5*4 && g.heavy[r.id]:
		delete(g.heavy, r.id)
	}
}

// shed kills the heaviest process of a run. Direct children of the daemon (the
// agent CLI or its wrapper) are spared while the run has anything else to lose.
func (g *runMemoryGuard) shed(r *runAccount, reason string, now time.Time) bool {
	victim, ok := r.victim(g.selfPID)
	if !ok {
		return false
	}
	if err := g.kill(victim.PID); err != nil {
		g.logger.Warn("run memory guard: kill failed", "task_id", r.id, "pid", victim.PID, "error", err)
		return false
	}
	g.lastKill = now
	g.status.KillCount++
	g.status.LastKill = &RunMemoryKill{
		At: now.UTC().Format(time.RFC3339), TaskID: r.id, PID: victim.PID, Comm: victim.Comm,
		Bytes: victim.AnonBytes, Reason: reason, RunSize: r.bytes,
	}
	g.logger.Warn("run memory guard: killed the heaviest process of a run",
		"reason", reason, "task_id", r.id, "pid", victim.PID, "comm", victim.Comm,
		"process_mb", victim.AnonBytes>>20, "run_mb", r.bytes>>20,
		"unit_anon_mb", g.status.UnitAnonBytes>>20, "unit_high_mb", g.status.UnitHighBytes>>20)
	return true
}

func hasRun(runs []*runAccount, id string) bool {
	for _, r := range runs {
		if r.id == id {
			return true
		}
	}
	return false
}

func (r *runAccount) top(n int) []procInfo {
	procs := append([]procInfo(nil), r.procs...)
	sort.Slice(procs, func(i, j int) bool { return procs[i].AnonBytes > procs[j].AnonBytes })
	if len(procs) > n {
		procs = procs[:n]
	}
	return procs
}

func (r *runAccount) victim(daemonPID int) (procInfo, bool) {
	var fallback *procInfo
	for _, p := range r.top(len(r.procs)) {
		p := p
		if p.PID <= 1 || p.PID == daemonPID {
			continue
		}
		if p.PPID == daemonPID {
			if fallback == nil {
				fallback = &p
			}
			continue
		}
		return p, true
	}
	if fallback != nil {
		return *fallback, true
	}
	return procInfo{}, false
}

func (r *runAccount) usage(heavy bool) RunMemoryUsage {
	u := RunMemoryUsage{TaskID: r.id, Bytes: r.bytes, Processes: len(r.procs), Heavy: heavy}
	for _, p := range r.top(3) {
		u.Top = append(u.Top, RunMemoryProcess{PID: p.PID, Comm: p.Comm, Bytes: p.AnonBytes})
	}
	return u
}

func describeTop(procs []procInfo) string {
	parts := make([]string, 0, len(procs))
	for _, p := range procs {
		parts = append(parts, fmt.Sprintf("%s[%d]=%dMB", p.Comm, p.PID, p.AnonBytes>>20))
	}
	return strings.Join(parts, " ")
}

// groupRuns attributes processes to runs and returns them heaviest first. A
// process without its own tag takes the tag of its nearest tagged ancestor, so
// a child that scrubbed its environment is still charged to its run.
func groupRuns(procs []procInfo, daemonPID int) []*runAccount {
	byPID := make(map[int]*procInfo, len(procs))
	for i := range procs {
		byPID[procs[i].PID] = &procs[i]
	}
	taskOf := func(p *procInfo) string {
		for depth := 0; p != nil && depth < 64; depth++ {
			if p.TaskID != "" {
				return p.TaskID
			}
			if p.PPID == daemonPID || p.PPID <= 1 {
				return ""
			}
			p = byPID[p.PPID]
		}
		return ""
	}
	accounts := map[string]*runAccount{}
	for i := range procs {
		p := &procs[i]
		if p.PID == daemonPID {
			continue
		}
		id := taskOf(p)
		if id == "" {
			continue
		}
		a := accounts[id]
		if a == nil {
			a = &runAccount{id: id}
			accounts[id] = a
		}
		a.bytes += p.AnonBytes
		a.procs = append(a.procs, *p)
	}
	out := make([]*runAccount, 0, len(accounts))
	for _, a := range accounts {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].bytes > out[j].bytes })
	return out
}
