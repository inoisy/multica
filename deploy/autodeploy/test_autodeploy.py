import hashlib
import hmac
import importlib.util
import json
import tempfile
import unittest
from pathlib import Path
from unittest import mock

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


SHA_OLD, SHA_NEW = "a" * 40, "b" * 40
MCP_OLD = "ghcr.io/inoisy/multica-tasks-mcp:" + "1" * 40
MCP_NEW = "ghcr.io/inoisy/multica-tasks-mcp:" + "2" * 40


class McpPinTests(unittest.TestCase):
    def test_pin_accepts_only_immutable_refs(self):
        cfg = dict(ad.DEFAULTS)
        self.assertEqual(ad.parse_mcp_pin(cfg, MCP_NEW + "\n"), MCP_NEW)
        digest = "ghcr.io/inoisy/multica-tasks-mcp@sha256:" + "f" * 64
        self.assertEqual(ad.parse_mcp_pin(cfg, digest), digest)
        for bad in ("", "ghcr.io/inoisy/multica-tasks-mcp:latest", "ghcr.io/inoisy/multica-tasks-mcp:main",
                    "ghcr.io/other/multica-tasks-mcp:" + "1" * 40, MCP_NEW + "\nextra: 1"):
            with self.assertRaises(ValueError):
                ad.parse_mcp_pin(cfg, bad)

    def test_override_carries_mcp_only_when_given(self):
        cfg = dict(ad.DEFAULTS, mcp_service="multica-tasks-mcp")
        self.assertIn(f"  multica-tasks-mcp:\n    image: {MCP_NEW}", ad.render_override(cfg, SHA_NEW, MCP_NEW))
        self.assertNotIn("multica-tasks-mcp", ad.render_override(cfg, SHA_NEW))
        prev = {"backend": "b:old", "frontend": "f:old", "multica-tasks-mcp": MCP_OLD}
        self.assertIn(MCP_OLD, ad.render_override(cfg, prev))


class McpComposeTests(unittest.TestCase):
    @mock.patch.object(ad.subprocess, "run")
    def test_context_inserts_root_addon_before_image_override(self, run):
        cfg = dict(ad.DEFAULTS, mcp_service="multica-tasks-mcp")
        labels = {"com.docker.compose.project": "multica", "com.docker.compose.project.working_dir": "/opt/multica",
                  "com.docker.compose.project.config_files": "/opt/multica/base.yml,/opt/multica/docker-compose.autodeploy.yml"}
        run.side_effect = [mock.Mock(stdout="backend-id\n"),
                           mock.Mock(stdout=json.dumps([{"Config": {"Labels": labels, "Image": "backend:old"}}]))]
        ctx = ad.Deployer(cfg, dry_run=False).compose_context()
        self.assertEqual(ctx["files"], ["/opt/multica/base.yml", cfg["mcp_addon_file"], ctx["override"]])

    @mock.patch.object(ad.subprocess, "run")
    def test_current_images_records_mcp_in_the_platform_project(self, run):
        cfg = dict(ad.DEFAULTS, mcp_service="multica-tasks-mcp")
        images = {"backend": "backend:old", "frontend": "web:old", "multica-tasks-mcp": MCP_OLD}
        run.side_effect = [result for service, image in images.items()
                           for result in (mock.Mock(stdout=service), mock.Mock(stdout=json.dumps([{"Config": {"Image": image}}])))]
        self.assertEqual(ad.Deployer(cfg, dry_run=False).current_images({"project": "multica"}), images)
        for call in run.call_args_list[::2]:
            self.assertIn("label=com.docker.compose.project=multica", call.args[0])


class FakeDeployer(ad.Deployer):
    """Deployer with docker, GitHub and the platform replaced by a scripted world."""

    def __init__(self, cfg, pin, previous, health_script, mcp_script):
        super().__init__(cfg, dry_run=False)
        self.pin, self.previous = pin, previous
        self.health_script, self.mcp_script = health_script, mcp_script
        self.compose_calls, self.overrides, self.running = [], [], dict(previous)

    def health(self, url=None):
        if url:
            return {"status": "ok"}
        return self.health_script(self)

    def mcp_state(self, ctx):
        image = self.running.get(self.cfg["mcp_service"])
        return None if image is None else {"image": image, "running": True, "health": self.mcp_script(self)}

    def compose_context(self):
        return {"project": "p", "workdir": "/w", "files": [], "override": "/w/o.yml"}

    def current_images(self, ctx):
        return dict(self.previous)

    def write_override(self, ctx, content):
        self.overrides.append(content)

    def compose(self, ctx, *args, mutate=True):
        self.compose_calls.append(args)
        if args[0] == "up":
            images = {}
            for line in self.overrides[-1].splitlines():
                if line.startswith("  ") and line.endswith(":"):
                    name = line.strip()[:-1]
                elif line.strip().startswith("image:"):
                    images[name] = line.split("image:")[1].strip()
            self.running.update({k: v for k, v in images.items() if k in args})
        if args[0] == "stop":
            self.running.pop(args[1], None)
        return ""

    def raw_file(self, path, ref):
        return self.pin

    def target_commit(self):
        return SHA_NEW

    def migration_check(self, current, target):
        return [], []

    def backup(self, ctx, label):
        return "/backup.dump"

    def update_daemon(self, sha):
        return "current"

    def notify(self, text):
        pass

    def save_state(self, state):
        self.saved = state


class McpDeployTests(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.addon = Path(tmp.name) / "docker-compose.mcp.yml"
        self.addon.write_text("services: {}\n")
        self.cfg = dict(ad.DEFAULTS, mcp_service="multica-tasks-mcp", mcp_addon_file=str(self.addon), dry_run=False,
                        log_path=str(Path(tmp.name) / "log.jsonl"), state_path=str(Path(tmp.name) / "state.json"),
                        health_timeout_s=1)
        self.old = {"backend": f"ghcr.io/inoisy/multica-backend:{SHA_OLD}", "frontend": f"ghcr.io/inoisy/multica-web:{SHA_OLD}",
                    "multica-tasks-mcp": MCP_OLD}
        patcher = mock.patch.object(ad.time, "sleep", lambda s: None)
        patcher.start()
        self.addCleanup(patcher.stop)
        # Platform /health reports the commit whose images run now.
        self.platform = lambda d: {"status": "ok", "commit": SHA_NEW if d.running.get("backend", "").endswith(SHA_NEW) else SHA_OLD}

    def deployer(self, pin=MCP_NEW, previous=None, mcp_health=lambda d: "healthy", platform=None):
        previous = self.old if previous is None else previous
        return FakeDeployer(self.cfg, pin, previous, platform or self.platform, mcp_health)

    def test_release_switches_three_services_and_records_previous(self):
        d = self.deployer()
        self.assertEqual(d.deploy(), 0)
        up = [c for c in d.compose_calls if c[0] == "up"][0]
        self.assertEqual(up[-3:], ("backend", "frontend", "multica-tasks-mcp"))
        self.assertIn(("pull", "backend", "frontend", "multica-tasks-mcp"), d.compose_calls)
        self.assertEqual(d.saved["previous_images"], self.old)
        self.assertEqual(d.saved["mcp"], MCP_NEW)
        self.assertFalse(d.saved["mcp_added"])

    def test_unhealthy_mcp_rolls_back_all_three_images(self):
        d = self.deployer(mcp_health=lambda d: "healthy" if d.running["multica-tasks-mcp"] == MCP_OLD else "unhealthy")
        self.assertEqual(d.deploy(), 4)
        self.assertEqual(d.running, self.old)
        self.assertIn(MCP_OLD, d.overrides[-1])
        self.assertNotIn("stop", [c[0] for c in d.compose_calls])

    def test_first_release_with_mcp_stops_it_on_rollback(self):
        previous = {k: v for k, v in self.old.items() if k != "multica-tasks-mcp"}
        d = self.deployer(previous=previous, mcp_health=lambda d: "unhealthy")
        self.assertEqual(d.deploy(), 4)
        self.assertEqual(d.running, previous)
        self.assertIn(("stop", "multica-tasks-mcp"), d.compose_calls)

    def test_invalid_pin_aborts_before_any_change(self):
        d = self.deployer(pin="ghcr.io/inoisy/multica-tasks-mcp:latest")
        self.assertEqual(d.deploy(), 6)
        self.assertEqual(d.compose_calls, [])
        self.assertEqual(d.overrides, [])

    def test_missing_addon_file_aborts(self):
        self.addon.unlink()
        d = self.deployer()
        self.assertEqual(d.deploy(), 6)
        self.assertEqual(d.compose_calls, [])

    def test_release_without_pin_aborts_before_any_change(self):
        d = self.deployer(pin=None)
        self.assertEqual(d.deploy(), 6)
        self.assertEqual(d.running, self.old)
        self.assertEqual(d.compose_calls, [])
        self.assertEqual(d.overrides, [])

    def test_enabling_mcp_after_platform_deployed_reconciles_same_commit(self):
        previous = {k: v for k, v in self.old.items() if k != "multica-tasks-mcp"}
        d = self.deployer(previous=previous)
        d.target_commit = lambda: SHA_OLD
        self.assertEqual(d.deploy(), 0)
        self.assertEqual(d.running["multica-tasks-mcp"], MCP_NEW)
        self.assertEqual(d.saved["previous_images"], previous)
        self.assertTrue(d.saved["mcp_added"])

    def test_already_running_release_does_not_overwrite_rollback_state(self):
        previous = dict(self.old, **{"multica-tasks-mcp": MCP_NEW})
        d = self.deployer(previous=previous)
        d.target_commit = lambda: SHA_OLD
        self.assertEqual(d.deploy(), 0)
        self.assertEqual(d.compose_calls, [])
        self.assertFalse(hasattr(d, "saved"))

    def test_platform_failure_rolls_back_three_images(self):
        d = self.deployer(platform=lambda d: {"status": "error"} if d.running["backend"].endswith(SHA_NEW) else self.platform(d))
        self.assertEqual(d.deploy(), 4)
        self.assertEqual(d.running, self.old)
        self.assertEqual(d.events[-1]["event"], "rollback_done")
        self.assertTrue(d.events[-1]["ok"])

    def test_partial_start_failure_rolls_back_three_images(self):
        d = self.deployer()
        compose = d.compose

        def fail_first_up(ctx, *args):
            result = compose(ctx, *args)
            if args[0] == "up" and d.running["backend"].endswith(SHA_NEW):
                raise ad.subprocess.CalledProcessError(1, "docker compose up")
            return result

        d.compose = fail_first_up
        self.assertEqual(d.deploy(), 4)
        self.assertEqual(d.running, self.old)
        self.assertTrue(d.events[-1]["ok"])

    def test_failed_pull_preserves_running_images_and_override(self):
        d = self.deployer()
        d.compose = mock.Mock(side_effect=ad.subprocess.CalledProcessError(1, "docker compose pull"))
        self.assertEqual(d.deploy(), 5)
        self.assertEqual(d.running, self.old)
        self.assertIn(MCP_OLD, d.overrides[-1])

    def test_failed_mcp_endpoint_rolls_back_three_images(self):
        d = self.deployer()
        health = d.health
        d.health = lambda url=None: ({"status": "error"} if d.running["multica-tasks-mcp"] == MCP_NEW else {"status": "ok"}) if url else health()
        self.assertEqual(d.deploy(), 4)
        self.assertEqual(d.running, self.old)

    def test_container_inspection_failure_is_unhealthy(self):
        d = self.deployer()
        d.mcp_state = mock.Mock(side_effect=ad.subprocess.CalledProcessError(1, "docker inspect"))
        self.assertFalse(d.mcp_ready({}, MCP_NEW)[0])

    def test_container_without_healthcheck_is_not_ready(self):
        d = self.deployer(mcp_health=lambda d: "none")
        self.assertFalse(d.mcp_ready({}, MCP_OLD)[0])

    def test_disabled_mcp_keeps_platform_only_release(self):
        self.cfg["mcp_service"] = ""
        d = self.deployer(previous={k: v for k, v in self.old.items() if k != "multica-tasks-mcp"})
        self.assertEqual(d.deploy(), 0)
        self.assertIn(("pull", "backend", "frontend"), d.compose_calls)
        self.assertNotIn("multica-tasks-mcp", d.overrides[0])

    def test_stale_container_image_is_not_ready(self):
        # The old systemd MCP answers /healthz, but the compose container must run the pinned image.
        d = self.deployer()
        d.running["multica-tasks-mcp"] = MCP_OLD
        ready, _ = d.mcp_ready({}, MCP_NEW)
        self.assertFalse(ready)

    def test_manual_rollback_restores_previous_and_stops_new_mcp(self):
        state = {"previous": SHA_OLD, "previous_images": {k: v for k, v in self.old.items() if k != "multica-tasks-mcp"}, "mcp_added": True}
        state_path = Path(self.cfg["state_path"])
        state_path.write_text(json.dumps(state))
        d = self.deployer(previous=self.old, platform=lambda d: {"status": "ok", "commit": SHA_OLD})
        d.running = dict(self.old, backend=f"ghcr.io/inoisy/multica-backend:{SHA_NEW}")
        self.assertEqual(d.rollback(), 0)
        self.assertNotIn("multica-tasks-mcp", d.running)
        self.assertTrue(d.running["backend"].endswith(SHA_OLD))

    def test_manual_rollback_restores_three_previous_images(self):
        Path(self.cfg["state_path"]).write_text(json.dumps({"previous": SHA_OLD, "previous_images": self.old}))
        d = self.deployer()
        d.running = {"backend": f"ghcr.io/inoisy/multica-backend:{SHA_NEW}", "frontend": f"ghcr.io/inoisy/multica-web:{SHA_NEW}",
                     "multica-tasks-mcp": MCP_NEW}
        self.assertEqual(d.rollback(), 0)
        self.assertEqual(d.running, self.old)


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
