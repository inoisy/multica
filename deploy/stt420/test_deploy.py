import importlib.util
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location("deploy", Path(__file__).with_name("deploy.py"))
deploy = importlib.util.module_from_spec(spec)
spec.loader.exec_module(deploy)
REVISION = "a" * 40
MIGRATIONS = ["000596_base", "000612_target"]

ACCEPTED = ("compared 156 contexts, 0 normalized (agent_list -> agent_profile + profile_personal), 14 differences\n"
            "accepted provider-full-window: 2 contexts, 14 differences; 0 differences remain\n"
            "ACCEPTED agent/id:0650752b choice.rows[0].state: before=\"available\" after=\"limited\"\n")
REJECTED = ("compared 156 contexts, 0 normalized (agent_list -> agent_profile + profile_personal), 15 differences\n"
            "accepted provider-full-window: 2 contexts, 14 differences; 1 differences remain\n"
            "DIFF agent/id:1d2c3b4a choice.rows[0].model: before=\"m\" after=\"n\"\n")


class InstallRehearsalTest(unittest.TestCase):
    """install on a stubbed host: only the PreviewChoice verdict differs."""

    def install(self, after_diff, rollback_diff="compared 156 contexts, 0 differences\n"):
        tmp = Path(self.enterContext(tempfile.TemporaryDirectory()))
        package = tmp / "package"
        package.mkdir()
        (tmp / "compose.yml").write_text("services: {}\n")
        (package / "manifest.json").write_text(json.dumps({
            "revision": REVISION, "previous_revision": "b" * 40, "migrations": MIGRATIONS}))
        release = deploy.Release(package, tmp / "compose.yml", "http://api", "http://web")
        release.project = "multica"
        diffs, rolled_back = [], []

        def docker(args, **kwargs):
            args = list(args)
            if args[:2] == ["docker", "exec"] and "diff" in args:
                binary = next(a for a in args if a.startswith("/tmp/stt420/choice-"))
                diffs.append(args)
                text, code = (after_diff, 0 if "0 differences remain" in after_diff else 1) \
                    if binary.endswith("choice-after") else (rollback_diff, 1 if "DIFF" in rollback_diff else 0)
                return SimpleNamespace(returncode=code, stdout=text.encode(), stderr=b"")
            out = b"CREATE TABLE fixture;\n" if "--schema-only" in args else b""
            return SimpleNamespace(returncode=0, stdout=out, stderr=b"")

        def new_state():
            state = tmp / "state"
            state.mkdir()
            (state / "database.dump").write_bytes(b"fixture dump")
            return state

        def fake_rollback(automatic=False):
            rolled_back.append(automatic)
            release.data["phase"] = "rolled_back"
            release.save()

        protected = {"postgres_id": "pg", "postgres_started": "t", "daemon_pid": "42"}
        with patch.object(deploy.subprocess, "run", side_effect=docker), \
             patch.object(release, "check", return_value={"status": "ok"}), \
             patch.object(release, "new_state", side_effect=new_state), \
             patch.object(release, "build", side_effect=lambda: release.data.update(new_images={"backend": "new-b", "frontend": "new-f"})), \
             patch.object(release, "backup", side_effect=lambda: release.data.update(snapshot_time="2026-10-25T08:26:44Z")), \
             patch.object(release, "migrate"), \
             patch.object(release, "compose", return_value=""), \
             patch.object(release, "cid", side_effect=lambda service: service), \
             patch.object(release, "inspect", side_effect=lambda cid: {"Image": {"backend": "new-b", "frontend": "new-f"}.get(cid, "pg-image")}), \
             patch.object(release, "protected", return_value=protected), \
             patch.object(release, "versions", return_value=MIGRATIONS), \
             patch.object(release, "wait", return_value={"status": "ok", "commit": REVISION[:9]}), \
             patch.object(release, "rollback", side_effect=fake_rollback), \
             patch.object(deploy.shutil, "copytree"):
            try:
                release.install()
                error = None
            except RuntimeError as exc:
                error = exc
        state = json.loads((tmp / "state/state.json").read_text())
        return state, error, diffs, rolled_back

    def test_accepted_full_window_contexts_reach_installed_and_keep_the_report(self):
        state, error, diffs, rolled_back = self.install(ACCEPTED)
        self.assertIsNone(error)
        self.assertEqual(state["phase"], "installed")
        self.assertEqual(rolled_back, [])
        self.assertEqual(state["rehearsal"]["preview_diff"], ACCEPTED)
        self.assertTrue(state["rehearsal"]["schema_roundtrip"])
        # The accepted change is asked for explicitly, and only of the new algorithm.
        after, rollback = diffs
        self.assertIn("/tmp/stt420/choice-after", after)
        self.assertEqual(after[after.index("--accept") + 1], "provider-full-window")
        self.assertIn("/tmp/stt420/choice-before", rollback)
        self.assertNotIn("--accept", rollback)

    def test_any_other_difference_rolls_back_before_live_migration(self):
        state, error, diffs, rolled_back = self.install(REJECTED)
        self.assertRegex(str(error), "preview_diff has unaccepted PreviewChoice differences")
        self.assertEqual(rolled_back, [True])
        self.assertEqual(state["phase"], "rolled_back")
        self.assertEqual(state["rehearsal"]["preview_diff"], REJECTED)
        self.assertEqual(len(diffs), 1, "down must not run after a rejected diff")

    def test_difference_after_down_rolls_back(self):
        state, error, _, rolled_back = self.install(ACCEPTED, rollback_diff="DIFF agent/x choice.reason\n")
        self.assertRegex(str(error), "rollback_preview_diff has unaccepted")
        self.assertEqual(rolled_back, [True])
        self.assertEqual(state["rehearsal"]["rollback_preview_diff"], "DIFF agent/x choice.reason\n")


if __name__ == "__main__":
    unittest.main()
