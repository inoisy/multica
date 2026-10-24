package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(context.Background()); err != nil {
		t.Skip(err)
	}
	return pool
}

// TestSnapshotCoversContextsAndDoesNotWrite builds a workspace with an agent
// on its own list, an archived agent, an open issue and a queued run, and
// checks that every context is exported, repeatably, from a read-only read.
func TestSnapshotCoversContextsAndDoesNotWrite(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	boot := testutil.New(pool, "", "")
	suffix := time.Now().UnixNano()
	owner := boot.User(t, fmt.Sprintf("mcc-owner-%d", suffix), fmt.Sprintf("mcc-owner-%d@example.com", suffix))
	ws := boot.Workspace(t, fmt.Sprintf("mcc-%d", suffix), fmt.Sprintf("mcc-%d", suffix), testutil.Cols{"issue_prefix": "MCC"})
	fx := testutil.New(pool, ws, owner)
	fx.Member(t, ws, owner, "owner")
	rt := fx.Runtime(t, "mcc-runtime", testutil.Cols{"provider": "claude", "visibility": "public"})
	live := fx.Agent(t, "mcc-live", rt, testutil.Cols{"model": "plain"})
	archived := fx.Agent(t, "mcc-archived", rt, testutil.Cols{"archived_at": testutil.Raw("now()")})
	issue := fx.Issue(t, "mcc issue", testutil.Cols{"assignee_type": "agent", "assignee_id": live})
	task := fx.Task(t, live, testutil.Cols{"runtime_id": rt, "issue_id": issue})
	fx.Cleanup(t, "DELETE FROM agent_task_queue WHERE agent_id IN (SELECT id FROM agent WHERE workspace_id=$1)", ws)
	steps, err := json.Marshal([]map[string]any{{"status_key": "in_progress", "handler": map[string]string{"type": "agent", "id": live}}})
	if err != nil {
		t.Fatal(err)
	}
	wf := fx.Insert(t, "issue_workflow", testutil.Cols{"workspace_id": ws, "name": "comparison workflow", "initial_status_key": "todo", "steps": steps})
	project := fx.Project(t, "comparison project", testutil.Cols{"workflow_id": wf})
	profile := fx.Insert(t, "model_profile", testutil.Cols{"workspace_id": ws, "name": "workflow issue models"})
	fx.InsertNoID(t, "model_profile_option", testutil.Cols{"workspace_id": ws, "profile_id": profile, "position": 1, "runtime_id": rt, "model": "workflow-model"}, "profile_id=$1", profile)
	workflowIssue := fx.Issue(t, "workflow issue without an agent assignee", testutil.Cols{"project_id": project, "status": "in_progress", "model_profile_id": profile})

	now := time.Now().UTC()
	take := func() snapshot {
		t.Helper()
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		s, err := collect(ctx, tx, util.MustParseUUID(ws), now)
		if err != nil {
			t.Fatal(err)
		}
		finishManifest(&s, manifest{Now: now.Format(time.RFC3339Nano), WorkspaceID: ws})
		return s
	}
	first := take()
	want := map[string]bool{
		"agent/" + live: true, "agent/" + archived: true, "issue/" + issue: true, "queued_run/" + task: true,
	}
	got := map[string]contextResult{}
	for _, c := range first.Contexts {
		got[c.Key] = c
		if c.Error != "" {
			t.Errorf("%s: %s", c.Key, c.Error)
		}
	}
	for key := range want {
		if _, ok := got[key]; !ok {
			t.Errorf("missing context %s; have %v", key, first.Manifest.Counts)
		}
	}
	if !got["agent/"+archived].Archived {
		t.Error("archived agent is not marked")
	}
	if c := got["agent/"+live].Choice; c == nil || c.Source != "agent_fields" || c.Chosen == nil || c.Chosen.RuntimeID != rt {
		t.Errorf("live agent choice: %+v", c)
	}
	second := take()
	if diffs, _ := compareSnapshots(first, second); len(diffs) != 0 {
		t.Errorf("two snapshots of the same data differ: %v", diffs)
	}
	key := "workflow_step/" + wf + "/in_progress/" + workflowIssue
	if c := got[key].Choice; c == nil || c.Source != "issue_profile" || c.Chosen == nil || c.Chosen.Model != "workflow-model" {
		t.Fatalf("active workflow context lost the issue's profile: %+v", got[key])
	}
}
