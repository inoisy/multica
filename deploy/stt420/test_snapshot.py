import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch


def load(name):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


snapshot = load("snapshot")
deploy = load("deploy")
REVISION = "a" * 40


class SnapshotTest(unittest.TestCase):
    def simulate(self, directory, *, migrations=None, health_revision=REVISION, ready=True):
        metadata = {"snapshot": "00000003-0000001B-1", "T": "2026-10-25T08:00:00.123456Z",
                    "database": "fixture", "postgres_version": "17.5", "extensions": {"pg_bigm": "1.2"},
                    "migrations": migrations if migrations is not None else ["000596_fixture"]}
        class Session:
            stdin = io.StringIO()
            stdout = io.StringIO(json.dumps(metadata) + "\n")
            done = False
            terminated = False

            def wait(self, **kwargs):
                self.done = True
                return 0

            def poll(self):
                return 0 if self.done else None

            def terminate(self):
                self.terminated = True

        session = Session()
        commands = []

        def docker(*args):
            commands.append(args)
            if args[:2] == ("docker", "inspect"):
                return json.dumps([{"Image": "sha256:" + args[2], "Config": {"Labels": {
                    "com.docker.compose.project": "multica",
                    "com.docker.compose.project.working_dir": "/opt/multica",
                    "com.docker.compose.project.config_files": "/opt/multica/compose.yml,/opt/multica/override.yml"}}}])
            self.assertEqual(args[-3:-1], ("ps", "-q"))
            return args[-1] + "-container"

        def dump(args, **kwargs):
            commands.append(tuple(args))
            self.assertIn('--snapshot="$1"', args[5])
            self.assertEqual(args[-1], metadata["snapshot"])
            self.assertIn("READ ONLY", session.stdin.getvalue())
            self.assertNotIn("COMMIT", session.stdin.getvalue())
            kwargs["stdout"].write(b"fixture dump")
            return SimpleNamespace(returncode=0)

        response = io.StringIO(json.dumps({"status": "ok", "commit": health_revision[:9]}))
        with patch.object(snapshot, "run", side_effect=docker), \
             patch.object(snapshot.urllib.request, "urlopen", return_value=response), \
             patch.object(snapshot.subprocess, "Popen", return_value=session), \
             patch.object(snapshot.subprocess, "run", side_effect=dump), \
             patch.object(snapshot.select, "select", return_value=([session.stdout] if ready else [], [], [])), \
             patch.object(snapshot.pwd, "getpwnam", return_value=SimpleNamespace(pw_uid=1000, pw_gid=1000)), \
             patch.object(snapshot.os, "chown"):
            try:
                result = snapshot.export("/opt/multica/compose.yml", directory, "agents", REVISION, "http://fixture")
                return result, commands, session
            finally:
                self.commands = commands
                self.session = session

    def test_dump_and_manifest_share_exported_snapshot_and_database_time(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp) / "copy"
            result, commands, session = self.simulate(out)
            manifest = json.loads((out / "manifest.json").read_text())
            self.assertEqual(manifest["T"], "2026-10-25T08:00:00.123456Z")
            self.assertEqual(manifest["sha256"], hashlib.sha256(b"fixture dump").hexdigest())
            self.assertEqual(result["sha256"], manifest["sha256"])
            self.assertEqual(manifest["source_revision"], REVISION)
            self.assertFalse(manifest["production_mutated"])
            self.assertIn("COMMIT", session.stdin.getvalue())
            self.assertFalse(session.terminated)
            self.assertEqual(out.stat().st_mode & 0o777, 0o700)
            self.assertEqual((out / "database.dump").stat().st_mode & 0o777, 0o600)
            # Only inspect/ps and the read-only dump are allowed in this path.
            self.assertFalse(any(word in cmd for cmd in commands for word in ("stop", "up", "restart", "pg_restore", "migrate")))

    def test_schema_drift_stops_before_dump_and_releases_snapshot_session(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp) / "copy"
            with self.assertRaisesRegex(RuntimeError, "source schema 596"):
                self.simulate(out, migrations=["000612_fixture"])
            self.assertFalse((out / "database.dump").exists())
            self.assertTrue(self.session.terminated)

    def test_backend_drift_stops_before_docker_access(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp) / "copy"
            with self.assertRaisesRegex(RuntimeError, "source revision"):
                self.simulate(out, health_revision="b" * 40)
            self.assertEqual(self.commands, [])
            self.assertFalse(out.exists())

    def test_timeout_stops_before_dump_and_closes_own_child(self):
        with tempfile.TemporaryDirectory() as tmp:
            out = Path(tmp) / "copy"
            with self.assertRaisesRegex(RuntimeError, "timeout"):
                self.simulate(out, ready=False)
            self.assertTrue(self.session.terminated)
            self.assertFalse((out / "manifest.json").exists())


class AcceptanceTest(unittest.TestCase):
    def test_install_requires_successful_qa_and_review_of_exact_revision(self):
        with self.assertRaisesRegex(RuntimeError, "acceptance"):
            deploy.require_acceptance(None, REVISION)
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "acceptance.json"
            evidence = {"revision": REVISION, "qa": {"passed": True, "report_url": "https://fixture/qa"},
                        "review": {"passed": True, "report_url": "https://fixture/review"}}
            path.write_text(json.dumps(evidence))
            deploy.require_acceptance(path, REVISION)
            with self.assertRaisesRegex(RuntimeError, "pin"):
                deploy.require_acceptance(path, "b" * 40)
            evidence["review"]["passed"] = False
            path.write_text(json.dumps(evidence))
            with self.assertRaisesRegex(RuntimeError, "review"):
                deploy.require_acceptance(path, REVISION)


class BundleTest(unittest.TestCase):
    def test_incomplete_manifest_and_tampered_binary_are_rejected(self):
        names = ("source.tar.gz", "deploy.py", "install.sh", "install-cli.py", "snapshot.py", "README.md",
                 "bin/server", "bin/migrate", "bin/multica", "bin/choice-before", "bin/choice-after")
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            for name in names:
                path = root / name
                path.parent.mkdir(exist_ok=True)
                path.write_bytes(b"fixture")
            manifest = {"revision": REVISION, "source_schema": 596, "target_schema": 612,
                        "checksums": {name: deploy.sha(root / name) for name in names}}
            (root / "manifest.json").write_text(json.dumps(manifest))
            release = deploy.Release(root, "/fixture/compose.yml", "", "")
            release.verify_bundle()
            (root / "bin/server").write_bytes(b"changed")
            with self.assertRaisesRegex(RuntimeError, "checksum"):
                release.verify_bundle()
            release.manifest["checksums"] = {}
            with self.assertRaisesRegex(RuntimeError, "incomplete"):
                release.verify_bundle()


if __name__ == "__main__":
    unittest.main()
