// model-choice-compare proves that moving data between schemas leaves the
// model choice alone. It runs only on an isolated copy of the production
// database, never on the live one:
//
//	model-choice-compare snapshot --workspace STT --now <RFC3339> --out before.json
//	model-choice-compare diff --before before.json --after after.json
//
// The snapshot asks the server's own choice code what every agent, open
// issue, workflow step and queued run would get at the fixed time
// --now. The same command is built from the commit before the choice is
// switched and from the commit after, and the two files are diffed.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "snapshot":
		err = runSnapshot(os.Args[2:])
	case "diff":
		var same bool
		same, err = runDiff(os.Args[2:])
		if err == nil && !same {
			os.Exit(1)
		}
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "model-choice-compare:", err)
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: model-choice-compare snapshot --workspace <slug|id> --now <RFC3339> --out <file> [--recompute-pool-verdicts] [--allow-remote]\n"+
		"       model-choice-compare diff --before <file> --after <file>")
	os.Exit(2)
}

func isLocal(databaseURL string) bool {
	u, err := url.Parse(databaseURL)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return u.Query().Get("host") == "" || strings.HasPrefix(u.Query().Get("host"), "/")
	}
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || strings.HasPrefix(host, "/")
}

func runSnapshot(args []string) error {
	fs := flag.NewFlagSet("snapshot", flag.ExitOnError)
	workspace := fs.String("workspace", "", "workspace slug, name or id")
	nowArg := fs.String("now", "", "the fixed time T, RFC3339; every time condition is judged at it")
	out := fs.String("out", "", "output file")
	recompute := fs.Bool("recompute-pool-verdicts", false, "rewrite the pool verdict rows of runtime_unavailable at T before the snapshot (second snapshot of the old algorithm)")
	allowRemote := fs.Bool("allow-remote", false, "allow a database that is not on this host; it must still be a copy")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *workspace == "" || *nowArg == "" || *out == "" {
		return fmt.Errorf("--workspace, --now and --out are required")
	}
	now, err := time.Parse(time.RFC3339Nano, *nowArg)
	if err != nil {
		return fmt.Errorf("--now: %w", err)
	}
	now = now.UTC()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return fmt.Errorf("DATABASE_URL is not set")
	}
	if !isLocal(databaseURL) && !*allowRemote {
		return fmt.Errorf("DATABASE_URL is not on this host: run on an isolated copy, or pass --allow-remote for a remote copy")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if *recompute {
		if err := recomputePoolVerdicts(ctx, pool, now); err != nil {
			return err
		}
	}
	// One read-only snapshot transaction: every query sees the same data.
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	ws, err := resolveWorkspace(ctx, tx, *workspace)
	if err != nil {
		return err
	}
	snap, err := collect(ctx, tx, ws, now)
	if err != nil {
		return err
	}
	var schema string
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(version), '') FROM schema_migrations`).Scan(&schema); err != nil {
		return err
	}
	finishManifest(&snap, manifest{Now: now.Format(time.RFC3339Nano), WorkspaceID: uuidString(ws), Schema: schema, Recomputed: *recompute})
	raw, err := json.MarshalIndent(snap, "", " ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, raw, 0o600); err != nil {
		return err
	}
	fmt.Printf("snapshot: now=%s schema=%s recomputed=%t counts=%v contexts_sha256=%s\n",
		snap.Manifest.Now, snap.Manifest.Schema, snap.Manifest.Recomputed, snap.Manifest.Counts, snap.Manifest.ContextSetSHA)
	return nil
}
