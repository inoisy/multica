import importlib.util
from pathlib import Path
import sys
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).parent))
spec = importlib.util.spec_from_file_location("product_index", Path(__file__).with_name("index.py"))
index = importlib.util.module_from_spec(spec)
spec.loader.exec_module(index)


class IndexEvidenceTests(unittest.TestCase):
    def test_nested_chi_routes_ignore_comments_and_quoted_braces(self):
        text = '''package main
func routes() {
 r.Route("/api/pools", func(r chi.Router) {
  // r.Get("/fake", h.Secret)
  marker := "}"
  r.Group(func(r chi.Router) {
   r.Get("/{id}", h.GetPool)
  })
 })
 r.Post("/other", h.Other)
 r.Route(dynamicPrefix, func(r chi.Router) { r.Get("/x", h.Dynamic) })
}'''
        routes = index.routes("router.go", text)
        self.assertEqual([r["name"] for r in routes], ["GET /api/pools/{id}", "POST /other", "GET /${dynamicPrefix}/x"])
        self.assertEqual(routes[0]["handler"], "GetPool")
        self.assertEqual(routes[2]["resolution"], "dynamic")

    def test_history_is_not_live_schema_and_deletes_are_not_selects(self):
        records = index.sql_inventory({
            "server/migrations/1_a.up.sql": "CREATE TABLE agent_model_option (id int); CREATE TABLE agent(id int);",
            "server/migrations/2_b.up.sql": "DROP TABLE agent_model_option;",
            "server/pkg/db/queries/example.sql": "-- name: Cleanup :exec\nWITH x AS (DELETE FROM agent_model_option RETURNING *) DELETE FROM agent;\n-- name: ReadAgent :one\nSELECT 'agent_model_option' AS label FROM agent;",
        })
        cleanup = next(r for r in records if r["name"] == "Cleanup")
        self.assertEqual(cleanup["writes"], ["agent", "agent_model_option"])
        self.assertEqual(cleanup["reads"], [])
        self.assertEqual(next(r for r in records if r["name"] == "ReadAgent")["tables"], ["agent"])
        self.assertTrue(next(r for r in records if r["kind"] == "table")["history_only"])

    def test_writers_distinguish_set_columns_from_where_columns(self):
        records = index.sql_inventory({
            "server/migrations/1_a.up.sql": "CREATE TABLE agent (id int, model text, runtime_id int);",
            "server/pkg/db/queries/example.sql": "-- name: ChangeModel :exec\nUPDATE agent SET model = $1 WHERE runtime_id = $2;\n-- name: CreateAgent :one\nINSERT INTO agent (id, runtime_id) VALUES ($1,$2) RETURNING *;",
        })
        change = next(r for r in records if r["name"] == "ChangeModel")
        self.assertEqual(change["write_fields"], {"agent": ["model"]})
        create = next(r for r in records if r["name"] == "CreateAgent")
        self.assertEqual(create["write_fields"], {"agent": ["id", "runtime_id"]})

    def test_stale_sha_and_dirty_fingerprint_both_refuse_reads(self):
        class File:
            def read_text(self):
                return '{"source_sha":"old","source_fingerprint":"clean"}'
        with patch.object(index, "git", return_value="new"):
            with self.assertRaisesRegex(ValueError, "Stale index"):
                index.load_index(File(), "HEAD")
        with patch.object(index, "git", return_value="old"), patch.object(index, "source_files", return_value=[]), patch.object(index, "fingerprint", return_value="dirty"):
            with self.assertRaisesRegex(ValueError, "Stale index"):
                index.load_index(File(), "HEAD")

    def test_factory_local_cobra_commands_are_not_lost(self):
        declarations = index.cli_commands("pool.go", '''func newPool() {
 pool := &cobra.Command{Use: "pool"}
 set := &cobra.Command{Use: "set <pool>", RunE: runPoolSet}
 pool.AddCommand(set)
}''')
        self.assertEqual([r["name"] for r in declarations], ["pool", "set <pool>", "pool.AddCommand"])


if __name__ == "__main__":
    unittest.main()
