package migrations

import (
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// forkMigrations are the migrations of the Loodsen fork that are already applied
// in production under these exact names (schema_migrations stores the full file
// name, so they cannot be renumbered). Their numbers 564-594 overlap with the
// upstream migrations of the same range; both sets build disjoint schema
// objects and give an identical schema in either order. They are unique among
// themselves; every other migration must be unique across the whole directory.
var forkMigrations = map[string]bool{
	"564_issue_conveyor":                         true,
	"565_agent_task_queue_conveyor_action":       true,
	"566_issue_conveyor_id_index":                true,
	"567_issue_conveyor_issue_index":             true,
	"568_issue_conveyor_event_id_index":          true,
	"569_issue_conveyor_event_input_index":       true,
	"570_issue_conveyor_event_action_index":      true,
	"571_issue_conveyor_event_seq_index":         true,
	"572_issue_conveyor_event_pending_index":     true,
	"573_issue_conveyor_event_task_index":        true,
	"574_agent_task_queue_conveyor_action_index": true,
	"575_model_profile":                          true,
	"576_model_profile_id_index":                 true,
	"577_model_profile_name_index":               true,
	"578_model_profile_option_index":             true,
	"579_agent_model_option_index":               true,
	"580_runtime_unavailable_index":              true,
	"581_issue_model_profile_index":              true,
	"582_agent_task_queue_model_choice_index":    true,
	"583_model_idle_episode":                     true,
	"584_model_idle_episode_open_index":          true,
	"585_agent_task_queue_model_routed_index":    true,
	"586_squad_route_template":                   true,
	"587_issue_stage_dispatch":                   true,
	"588_issue_stage_dispatch_index":             true,
	"589_subscription":                           true,
	"590_subscription_key_index":                 true,
	"591_subscription_usage_index":               true,
	"592_runtime_profile_share_key_index":        true,
	"593_subscription_host_state":                true,
	"594_subscription_host_state_index":          true,
}

func TestMigrationNumericPrefixesAreUnique(t *testing.T) {
	files := migrationFilesForLint(t, "*.up.sql")

	// Migrations through 128 contain historical duplicate numeric prefixes.
	// From 129 onward, keep the numeric sequence unique so release tooling and
	// operators can identify one schema change unambiguously by its number.
	const firstUniqueMigrationNumber = 129
	stemByNumber := make(map[int]string)
	forkStemByNumber := make(map[int]string)
	for _, file := range files {
		stem, _, ok := splitMigrationFilename(filepath.Base(file))
		if !ok {
			continue
		}
		prefix, _, ok := strings.Cut(stem, "_")
		if !ok {
			continue
		}
		number, err := strconv.Atoi(prefix)
		if err != nil || number < firstUniqueMigrationNumber {
			continue
		}
		byNumber := stemByNumber
		if forkMigrations[stem] {
			byNumber = forkStemByNumber
		}
		if previous, exists := byNumber[number]; exists {
			t.Errorf("migrations %s and %s share numeric prefix %s", previous, stem, prefix)
			continue
		}
		byNumber[number] = stem
	}
}

func TestMigrationFilesHaveMatchingDirections(t *testing.T) {
	files := migrationFilesForLint(t, "*.sql")

	directionsByStem := make(map[string]map[string]bool)
	for _, file := range files {
		stem, direction, ok := splitMigrationFilename(filepath.Base(file))
		if !ok {
			continue
		}
		if directionsByStem[stem] == nil {
			directionsByStem[stem] = make(map[string]bool)
		}
		directionsByStem[stem][direction] = true
	}

	for stem, directions := range directionsByStem {
		if !directions["up"] || !directions["down"] {
			t.Errorf("migration %s must have both .up.sql and .down.sql files", stem)
		}
	}
}

func migrationFilesForLint(t *testing.T, pattern string) []string {
	t.Helper()

	dir := realMigrationsDir(t)
	files, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatalf("no migration files matched %s in %s", pattern, dir)
	}
	sort.Strings(files)
	return files
}

func realMigrationsDir(t *testing.T) string {
	t.Helper()

	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve migration lint test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(self), "..", "..", "migrations"))
}

func splitMigrationFilename(name string) (stem, direction string, ok bool) {
	for _, candidateDirection := range []string{"up", "down"} {
		suffix := fmt.Sprintf(".%s.sql", candidateDirection)
		if strings.HasSuffix(name, suffix) {
			return strings.TrimSuffix(name, suffix), candidateDirection, true
		}
	}
	return "", "", false
}
