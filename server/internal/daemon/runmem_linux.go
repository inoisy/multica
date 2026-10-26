//go:build linux

package daemon

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func selfPID() int { return os.Getpid() }

func killProcess(pid int) error { return syscall.Kill(pid, syscall.SIGKILL) }

type linuxProcScanner struct {
	root string
	uid  int
	// tags caches MULTICA_TASK_ID per process, keyed by pid and start time so
	// a reused pid is read again.
	tags map[procKey]string
}

type procKey struct {
	pid   int
	start string
}

func newProcScanner() procScanner {
	return &linuxProcScanner{root: "/proc", uid: os.Getuid(), tags: map[procKey]string{}}
}

func (s *linuxProcScanner) Scan() ([]procInfo, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	seen := make(map[procKey]struct{}, len(entries))
	var out []procInfo
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		dir := filepath.Join(s.root, e.Name())
		status, err := os.ReadFile(filepath.Join(dir, "status"))
		if err != nil {
			continue
		}
		info, ok := parseProcStatus(status)
		if !ok || info.uid != s.uid {
			continue
		}
		start, ppid, ok := readStatStart(filepath.Join(dir, "stat"))
		if !ok {
			continue
		}
		key := procKey{pid: pid, start: start}
		seen[key] = struct{}{}
		tag, cached := s.tags[key]
		if !cached {
			tag = readTaskTag(filepath.Join(dir, "environ"))
			s.tags[key] = tag
		}
		out = append(out, procInfo{PID: pid, PPID: ppid, Comm: info.name, AnonBytes: info.anon, TaskID: tag})
	}
	for k := range s.tags {
		if _, ok := seen[k]; !ok {
			delete(s.tags, k)
		}
	}
	return out, nil
}

type procStatus struct {
	name string
	uid  int
	anon uint64
}

// parseProcStatus reads Name, Uid and the anonymous footprint
// (RssAnon+RssShmem; VmRSS when the kernel does not split it).
func parseProcStatus(b []byte) (procStatus, bool) {
	var st procStatus
	var anon, shmem, rss uint64
	var haveUID, haveAnon bool
	for _, line := range strings.Split(string(b), "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch key {
		case "Name":
			st.name = val
		case "Uid":
			if f := strings.Fields(val); len(f) > 0 {
				if n, err := strconv.Atoi(f[0]); err == nil {
					st.uid, haveUID = n, true
				}
			}
		case "RssAnon":
			anon, haveAnon = kbToBytes(val), true
		case "RssShmem":
			shmem = kbToBytes(val)
		case "VmRSS":
			rss = kbToBytes(val)
		}
	}
	if !haveUID {
		return st, false
	}
	if haveAnon {
		st.anon = anon + shmem
	} else {
		st.anon = rss
	}
	return st, true
}

func kbToBytes(v string) uint64 {
	f := strings.Fields(v)
	if len(f) == 0 {
		return 0
	}
	n, err := strconv.ParseUint(f[0], 10, 64)
	if err != nil {
		return 0
	}
	return n << 10
}

// readStatStart returns the start time (field 22) and ppid (field 4). The comm
// field may contain spaces and parentheses, so parsing starts after the last ')'.
func readStatStart(path string) (start string, ppid int, ok bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", 0, false
	}
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return "", 0, false
	}
	f := strings.Fields(string(b[i+1:]))
	// f[0] is state (field 3); field N is f[N-3].
	if len(f) < 20 {
		return "", 0, false
	}
	p, err := strconv.Atoi(f[1])
	if err != nil {
		return "", 0, false
	}
	return f[19], p, true
}

var taskTagPrefix = []byte("MULTICA_TASK_ID=")

func readTaskTag(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, kv := range bytes.Split(b, []byte{0}) {
		if bytes.HasPrefix(kv, taskTagPrefix) {
			return string(kv[len(taskTagPrefix):])
		}
	}
	return ""
}

// readUnitMemory reads the cgroup v2 the daemon runs in. Anonymous memory is
// used instead of memory.current, which includes page cache that sits near
// memory.high on a healthy host.
func readUnitMemory() (unitMemory, bool) {
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return unitMemory{}, false
	}
	var rel string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "0::") {
			rel = strings.TrimPrefix(line, "0::")
		}
	}
	if rel == "" {
		return unitMemory{}, false
	}
	dir := filepath.Join("/sys/fs/cgroup", rel)
	stat, err := os.ReadFile(filepath.Join(dir, "memory.stat"))
	if err != nil {
		return unitMemory{}, false
	}
	var u unitMemory
	for _, line := range strings.Split(string(stat), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && (f[0] == "anon" || f[0] == "shmem") {
			if n, err := strconv.ParseUint(f[1], 10, 64); err == nil {
				u.AnonBytes += n
			}
		}
	}
	u.MaxBytes = readCgroupLimit(filepath.Join(dir, "memory.max"))
	u.HighBytes = readCgroupLimit(filepath.Join(dir, "memory.high"))
	if u.HighBytes == 0 {
		u.HighBytes = u.MaxBytes
	}
	return u, true
}

// readCgroupLimit returns 0 for "max" (unlimited) or an unreadable file.
func readCgroupLimit(path string) uint64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return 0
	}
	return n
}
