import hashlib
import hmac
import importlib.util
import unittest
from pathlib import Path

HERE = Path(__file__).parent


def load(name):
    spec = importlib.util.spec_from_file_location(name, HERE / f"{name}.py")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


ad = load("autodeploy")
hook = load("hook")


class MigrationTests(unittest.TestCase):
    def test_additive_migrations_pass(self):
        files = [
            {"filename": "server/migrations/613_x.up.sql", "status": "added",
             "content": "ALTER TABLE agent ADD COLUMN IF NOT EXISTS x text;\n-- DROP TABLE old;"},
            {"filename": "server/migrations/613_x.down.sql", "status": "added", "content": "ALTER TABLE agent DROP COLUMN x;"},
            {"filename": "server/migrations/614_idx.up.sql", "status": "added",
             "content": "CREATE INDEX CONCURRENTLY IF NOT EXISTS i ON agent (x);"},
        ]
        problems, added = ad.classify_migrations(files)
        self.assertEqual(problems, [])
        self.assertEqual(added, ["server/migrations/613_x.up.sql", "server/migrations/614_idx.up.sql"])

    def test_destructive_and_edited_migrations_block(self):
        files = [
            {"filename": "server/migrations/615_drop.up.sql", "status": "added", "content": "DROP TABLE IF EXISTS agent_model_option;"},
            {"filename": "server/migrations/616_col.up.sql", "status": "added",
             "content": "ALTER TABLE IF EXISTS subscription DROP COLUMN IF EXISTS reserve;"},
            {"filename": "server/migrations/612_old.up.sql", "status": "modified"},
        ]
        problems, _ = ad.classify_migrations(files)
        self.assertEqual(len(problems), 3)
        self.assertIn("DROP TABLE", problems[0])

    def test_legacy_cleanup_migration_stops_the_autodeploy(self):
        name = "616_drop_legacy_model_schema.up.sql"
        content = (HERE.parent.parent / "server" / "migrations" / name).read_text()
        problems, added = ad.classify_migrations([{"filename": f"server/migrations/{name}", "status": "added", "content": content}])
        self.assertEqual(added, [f"server/migrations/{name}"])
        self.assertEqual(len(problems), 1)
        for statement in ("DROP TABLE", "ALTER TABLE", "DELETE FROM"):
            self.assertIn(statement, problems[0])

    def test_comment_only_destructive_text_is_ignored(self):
        self.assertEqual(ad.destructive_statements("/* DROP TABLE x */ SELECT 1; -- TRUNCATE y"), [])


class TargetTests(unittest.TestCase):
    def test_newest_green_commit_wins(self):
        commits = ["c3", "c2", "c1"]
        runs = {"c3": {"CI": "success"}, "c2": {"CI": "success", "STT": "success"}, "c1": {"CI": "success", "STT": "success"}}
        self.assertEqual(ad.pick_target(commits, runs, ["CI", "STT"]), "c2")

    def test_no_green_commit(self):
        self.assertIsNone(ad.pick_target(["c1"], {"c1": {"CI": "failure", "STT": "success"}}, ["CI", "STT"]))

    def test_override_render(self):
        cfg = dict(ad.DEFAULTS)
        text = ad.render_override(cfg, "abc")
        self.assertIn("image: ghcr.io/inoisy/multica-backend:abc", text)
        self.assertIn("image: ghcr.io/inoisy/multica-web:abc", text)
        back = ad.render_override(cfg, {"backend": "multica-backend:old"})
        self.assertIn("image: multica-backend:old", back)
        self.assertNotIn("frontend", back)


class HookTests(unittest.TestCase):
    def test_signature(self):
        body = b'{"a":1}'
        sig = "sha256=" + hmac.new(b"s3cret", body, hashlib.sha256).hexdigest()
        self.assertTrue(hook.valid_signature(b"s3cret", body, sig))
        self.assertFalse(hook.valid_signature(b"other", body, sig))
        self.assertFalse(hook.valid_signature(b"", body, sig))
        self.assertFalse(hook.valid_signature(b"s3cret", body, None))

    def test_trigger_filter(self):
        ok = {"action": "completed", "workflow_run": {"conclusion": "success", "head_branch": "main", "event": "push", "head_sha": "x"}}
        self.assertTrue(hook.should_trigger("workflow_run", ok))
        self.assertFalse(hook.should_trigger("push", ok))
        for key, val in (("conclusion", "failure"), ("head_branch", "feature"), ("event", "pull_request")):
            bad = {"action": "completed", "workflow_run": dict(ok["workflow_run"], **{key: val})}
            self.assertFalse(hook.should_trigger("workflow_run", bad))


if __name__ == "__main__":
    unittest.main()
