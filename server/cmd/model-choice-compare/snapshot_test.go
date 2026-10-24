package main

import (
	"context"
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
}
