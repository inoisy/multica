package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

func readSnapshot(path string) (snapshot, error) {
	var s snapshot
	raw, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, fmt.Errorf("%s: %w", path, err)
	}
	if s.Manifest.Format != snapshotFormat {
		return s, fmt.Errorf("%s: unknown snapshot format %d", path, s.Manifest.Format)
	}
	return s, nil
}

// difference is one place where the two snapshots disagree.
type difference struct {
	Context string
	Path    string
	Before  string
	After   string
}

// runDiff reports whether the snapshots agree and prints every difference
// without names and with shortened ids.
func runDiff(args []string) (bool, error) {
	fs := flag.NewFlagSet("diff", flag.ExitOnError)
	beforePath := fs.String("before", "", "snapshot of the original algorithm")
	afterPath := fs.String("after", "", "snapshot of the new algorithm")
	if err := fs.Parse(args); err != nil {
		return false, err
	}
	if *beforePath == "" || *afterPath == "" {
		return false, fmt.Errorf("--before and --after are required")
	}
	before, err := readSnapshot(*beforePath)
	if err != nil {
		return false, err
	}
	after, err := readSnapshot(*afterPath)
	if err != nil {
		return false, err
	}
	diffs, normalized := compareSnapshots(before, after)
	fmt.Printf("before: schema=%s now=%s recomputed=%t counts=%v contexts_sha256=%s\n",
		before.Manifest.Schema, before.Manifest.Now, before.Manifest.Recomputed, before.Manifest.Counts, before.Manifest.ContextSetSHA)
	fmt.Printf("after:  schema=%s now=%s recomputed=%t counts=%v contexts_sha256=%s\n",
		after.Manifest.Schema, after.Manifest.Now, after.Manifest.Recomputed, after.Manifest.Counts, after.Manifest.ContextSetSHA)
	fmt.Printf("compared %d contexts, %d normalized (agent_list -> agent_profile + profile_personal), %d differences\n",
		len(before.Contexts), normalized, len(diffs))
	for _, d := range diffs {
		fmt.Printf("DIFF %s %s: before=%s after=%s\n", anonKey(d.Context), d.Path, d.Before, d.After)
	}
	return len(diffs) == 0, nil
}

func compareSnapshots(before, after snapshot) ([]difference, int) {
	var diffs []difference
	add := func(ctx, path string, b, a any) {
		diffs = append(diffs, difference{Context: ctx, Path: path, Before: anon(path, b), After: anon(path, a)})
	}
	if before.Manifest.Now != after.Manifest.Now {
		add("manifest", "now", before.Manifest.Now, after.Manifest.Now)
	}
	if before.Manifest.WorkspaceID != after.Manifest.WorkspaceID {
		add("manifest", "workspace_id", before.Manifest.WorkspaceID, after.Manifest.WorkspaceID)
	}
	if !reflect.DeepEqual(before.Manifest.Counts, after.Manifest.Counts) {
		add("manifest", "counts", before.Manifest.Counts, after.Manifest.Counts)
	}
	if before.Manifest.ContextSetSHA != after.Manifest.ContextSetSHA {
		add("manifest", "context_set_sha256", before.Manifest.ContextSetSHA, after.Manifest.ContextSetSHA)
	}
	byKey := func(s snapshot) map[string]contextResult {
		m := make(map[string]contextResult, len(s.Contexts))
		for _, c := range s.Contexts {
			m[c.Key] = c
		}
		return m
	}
	b, a := byKey(before), byKey(after)
	keys := map[string]bool{}
	for k := range b {
		keys[k] = true
	}
	for k := range a {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	normalized := 0
	for _, k := range sorted {
		bc, inBefore := b[k]
		ac, inAfter := a[k]
		switch {
		case !inBefore:
			add(k, "context", "absent", "present")
			continue
		case !inAfter:
			add(k, "context", "present", "absent")
			continue
		}
		if bc.Archived != ac.Archived {
			add(k, "archived", bc.Archived, ac.Archived)
		}
		if bc.Error != "" || ac.Error != "" {
			add(k, "error", bc.Error, ac.Error)
		}
		if bc.Choice == nil || ac.Choice == nil {
			if (bc.Choice == nil) != (ac.Choice == nil) {
				add(k, "choice", bc.Choice != nil, ac.Choice != nil)
			}
			continue
		}
		bch, ach := *bc.Choice, *ac.Choice
		if normalizePersonal(&bch, &ach) {
			normalized++
		}
		for _, d := range compareJSON("choice", bch, ach) {
			diffs = append(diffs, difference{Context: k, Path: d.Path, Before: d.Before, After: d.After})
		}
	}
	return diffs, normalized
}

// normalizePersonal is the one rewrite the specification allows: an agent's
// own list that became a personal profile. The source name, and the id and
// name of the profile that was created for it, are not a change of behavior.
// Everything else — rows, args, states, times — still has to match.
func normalizePersonal(before, after *choice) bool {
	if before.Source != "agent_list" || after.Source != "agent_profile" || !after.ProfilePersonal {
		return false
	}
	personalID, personalName := after.ProfileID, after.ProfileName
	after.Source = "agent_list"
	after.ProfileID, after.ProfileName, after.ProfilePersonal = "", "", false
	if len(after.ExhaustedProfiles) > 0 && after.ExhaustedProfiles[0] == personalName {
		after.ExhaustedProfiles = after.ExhaustedProfiles[1:]
	}
	rows := append([]row(nil), after.Rows...)
	for i := range rows {
		if rows[i].ProfileID == personalID {
			rows[i].ProfileID = ""
		}
	}
	after.Rows = rows
	return true
}

func generic(v any) any {
	raw, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(raw, &out)
	return out
}

// compareJSON walks two values field by field. null and an empty list are
// different values: custom_args NULL derives from the agent's disallowed
// tools, [] means none.
func compareJSON(path string, b, a any) []difference {
	return walk(path, generic(b), generic(a))
}

func walk(path string, b, a any) []difference {
	switch bv := b.(type) {
	case map[string]any:
		av, ok := a.(map[string]any)
		if !ok {
			return []difference{{Path: path, Before: anon(path, b), After: anon(path, a)}}
		}
		keys := map[string]bool{}
		for k := range bv {
			keys[k] = true
		}
		for k := range av {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		var out []difference
		for _, k := range sorted {
			out = append(out, walk(path+"."+k, bv[k], av[k])...)
		}
		return out
	case []any:
		av, ok := a.([]any)
		if !ok {
			return []difference{{Path: path, Before: anon(path, b), After: anon(path, a)}}
		}
		var out []difference
		if len(bv) != len(av) {
			out = append(out, difference{Path: path + ".length", Before: fmt.Sprint(len(bv)), After: fmt.Sprint(len(av))})
		}
		for i := 0; i < len(bv) && i < len(av); i++ {
			out = append(out, walk(fmt.Sprintf("%s[%d]", path, i), bv[i], av[i])...)
		}
		return out
	}
	if !reflect.DeepEqual(b, a) {
		return []difference{{Path: path, Before: anon(path, b), After: anon(path, a)}}
	}
	return nil
}

var uuidRe = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:8]
}

// anon renders a value for the report: ids are cut to a hash, names are
// dropped. The report never carries a name of a private workspace.
func anon(path string, v any) string {
	raw, _ := json.Marshal(v)
	s := string(raw)
	if strings.Contains(path, "profile_name") || strings.Contains(path, "exhausted_profiles") {
		return "name:" + shortHash(s)
	}
	return uuidRe.ReplaceAllStringFunc(s, func(id string) string { return "id:" + shortHash(strings.ToLower(id)) })
}

func anonKey(key string) string {
	return uuidRe.ReplaceAllStringFunc(key, func(id string) string { return "id:" + shortHash(strings.ToLower(id)) })
}
