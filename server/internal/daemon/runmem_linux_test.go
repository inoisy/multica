//go:build linux

package daemon

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestParseProcStatus(t *testing.T) {
	st, ok := parseProcStatus([]byte("Name:\tnext-server\nUid:\t1000\t1000\t1000\t1000\nVmRSS:\t  9000 kB\nRssAnon:\t  2048 kB\nRssFile:\t 100 kB\nRssShmem:\t  1024 kB\n"))
	if !ok || st.name != "next-server" || st.uid != 1000 || st.anon != 3072<<10 {
		t.Fatalf("got %+v ok=%v", st, ok)
	}
	st, _ = parseProcStatus([]byte("Name:\tx\nUid:\t1\t1\t1\t1\nVmRSS:\t 500 kB\n"))
	if st.anon != 500<<10 {
		t.Fatalf("VmRSS fallback: %+v", st)
	}
}

func TestReadStatStartHandlesParenthesesInComm(t *testing.T) {
	path := t.TempDir() + "/stat"
	// pid (comm) state ppid pgrp session tty tpgid flags minflt cminflt majflt cmajflt utime stime cutime cstime priority nice threads itrealvalue starttime
	if err := os.WriteFile(path, []byte("42 (a) b (c)) S 7 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 12345 0 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	start, ppid, ok := readStatStart(path)
	if !ok || start != "12345" || ppid != 7 {
		t.Fatalf("start=%q ppid=%d ok=%v", start, ppid, ok)
	}
}

// A run that balloons is cut down inside its own run: its biggest process dies,
// the sibling run and the "daemon" (this test process) are untouched.
func TestGuardKillsBloatedProcessOfOneRunOnly(t *testing.T) {
	if os.Getenv("RUNMEM_HELPER_MB") != "" {
		mb, _ := strconv.Atoi(os.Getenv("RUNMEM_HELPER_MB"))
		buf := make([]byte, mb<<20)
		for i := range buf {
			buf[i] = 1
		}
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	start := func(task string, mb int) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=TestGuardKillsBloatedProcessOfOneRunOnly")
		cmd.Env = append(os.Environ(), "MULTICA_TASK_ID="+task, "RUNMEM_HELPER_MB="+strconv.Itoa(mb))
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		return cmd
	}
	hog := start("task-hog-"+t.Name(), 300)
	calm := start("task-calm-"+t.Name(), 20)

	g := newRunMemoryGuard(RunMemoryConfig{Interval: 100 * time.Millisecond, MaxBytes: 150 << 20, HighBytes: 100 << 20}, nil)
	g.logger = discardLogger()
	g.unit = nil
	// The test may itself run inside an agent run (MULTICA_TASK_ID set); only
	// the helper processes started above may be seen, or the guard would shoot
	// the surrounding session.
	g.scanner = onlyTasks{inner: g.scanner, prefix: "task-"}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go g.Run(ctx)

	hogDone := make(chan error, 1)
	go func() { hogDone <- hog.Wait() }()
	select {
	case <-hogDone:
	case <-ctx.Done():
		t.Fatalf("bloated process was not killed; snapshot=%+v", g.Snapshot())
	}
	if err := calm.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("calm run's process died: %v", err)
	}
	st := g.Snapshot()
	if st == nil || st.KillCount != 1 || st.LastKill.Reason != "run_limit" || st.LastKill.TaskID != "task-hog-"+t.Name() {
		t.Fatalf("snapshot = %+v", st)
	}
}

type onlyTasks struct {
	inner  procScanner
	prefix string
}

func (o onlyTasks) Scan() ([]procInfo, error) {
	all, err := o.inner.Scan()
	var out []procInfo
	for _, p := range all {
		if strings.HasPrefix(p.TaskID, o.prefix) {
			out = append(out, p)
		}
	}
	return out, err
}
