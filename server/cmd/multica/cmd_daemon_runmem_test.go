package main

import (
	"strings"
	"testing"
)

func TestRunMemoryRows(t *testing.T) {
	if rows := runMemoryRows(map[string]any{}); rows != nil {
		t.Fatalf("no run_memory must print nothing, got %v", rows)
	}
	rows := runMemoryRows(map[string]any{"run_memory": map[string]any{
		"total_runs_bytes": float64(3 << 30),
		"run_max_bytes":    float64(5 << 30),
		"runs": []any{map[string]any{
			"task_id": "0123456789abcdef", "bytes": float64(2 << 30),
			"top": []any{map[string]any{"comm": "next-server", "bytes": float64(1 << 30)}},
		}},
		"kill_count": float64(1),
		"last_kill":  map[string]any{"task_id": "0123456789abcdef", "comm": "next-server", "reason": "run_limit", "at": "t"},
	}})
	var joined []string
	for _, r := range rows {
		joined = append(joined, r[0]+"="+r[1])
	}
	got := strings.Join(joined, "\n")
	for _, want := range []string{"1 runs, 3072 MB (limit per run 5120 MB)", "01234567 2048 MB (next-server 1024 MB)", "Memory kills=1, last: next-server run_limit in 01234567"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}
