import importlib.util
import io
import json
import builtins
from pathlib import Path
import tarfile
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import MagicMock, mock_open, patch


spec = importlib.util.spec_from_file_location("resume", Path(__file__).with_name("resume.py"))
resume = importlib.util.module_from_spec(spec)
spec.loader.exec_module(resume)
spec = importlib.util.spec_from_file_location("install_cli", Path(__file__).with_name("install-cli.py"))
cli = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cli)
REV = "a" * 40


class ResumeTest(unittest.TestCase):
    def release(self, phase="building"):
        state = Path(self.enterContext(tempfile.TemporaryDirectory()))
        config = state / "compose.yml"
        config.write_text("services: {}")
        r = SimpleNamespace(
            state=state, package=state, compose_files=[str(config)], project="multica", workdir=str(state),
            manifest={"revision": REV, "migrations": ["596", "612"]},
            data={"phase": phase, "protected": {"daemon_pid": "42"},
                  "old_containers": {"backend": "backend", "frontend": "frontend", "postgres": "postgres"},
                  "config_checksums": {str(config): resume.deploy.sha(config)},
                  "new_images": {"backend": "new-b", "frontend": "new-f"}},
        )
        for name in ("check", "save", "backup", "rehearse", "migrate", "compose", "rollback"):
            setattr(r, name, MagicMock())
        r.protected = MagicMock(return_value=r.data["protected"])
        r.cid = MagicMock(side_effect=lambda service: service)
        r.inspect = MagicMock(side_effect=lambda service: {"Image": r.data["new_images"][service]})
        r.versions = MagicMock(return_value=r.manifest["migrations"])
        r.wait = MagicMock(return_value={"status": "ok", "commit": REV})
        return r

    def test_completed_build_resumes_without_build_or_new_state(self):
        r = self.release()
        with patch.object(resume, "validate_images") as validate:
            resume.resume(r)
        validate.assert_called_once_with(r)
        self.assertEqual(r.data["phase"], "installed")
        r.backup.assert_called_once()
        r.rehearse.assert_called_once()
        r.migrate.assert_called_once_with("up")
        r.rollback.assert_not_called()
        self.assertIn("deployed_at", r.data)

    def test_rejected_rehearsal_rolls_back_before_live_migration(self):
        r = self.release()
        r.rehearse.side_effect = RuntimeError("unaccepted PreviewChoice")
        with patch.object(resume, "validate_images"), self.assertRaisesRegex(RuntimeError, "PreviewChoice"):
            resume.resume(r)
        r.migrate.assert_not_called()
        r.rollback.assert_called_once_with(automatic=True)

    def test_smoke_failure_after_migration_rolls_back(self):
        r = self.release()
        r.wait.side_effect = RuntimeError("smoke failed")
        with patch.object(resume, "validate_images"), self.assertRaisesRegex(RuntimeError, "smoke failed"):
            resume.resume(r)
        r.migrate.assert_called_once_with("up")
        r.rollback.assert_called_once_with(automatic=True)

    def test_in_flight_or_rolled_back_phases_never_change_services(self):
        for phase in ("stopping", "stopped", "migrating", "rolled_back"):
            with self.subTest(phase=phase):
                r = self.release(phase)
                with self.assertRaisesRegex(RuntimeError, "inspect status/rollback"):
                    resume.resume(r)
                r.compose.assert_not_called()

    def test_installed_is_verified_without_repeating_migrations(self):
        r = self.release("installed")
        resume.resume(r)
        r.wait.assert_called_once_with(REV)
        r.migrate.assert_not_called()
        r.backup.assert_not_called()
        r.compose.assert_not_called()
        r.versions.return_value = ["596"]
        with self.assertRaisesRegex(RuntimeError, "schema drift"):
            resume.resume(r)

    def test_config_or_protected_service_drift_stops_before_stop(self):
        for drift in ("config", "daemon"):
            with self.subTest(drift=drift):
                r = self.release()
                if drift == "config":
                    Path(r.compose_files[0]).write_text("changed")
                else:
                    r.protected.return_value = {"daemon_pid": "99"}
                with patch.object(resume, "validate_images"), self.assertRaises(RuntimeError):
                    resume.resume(r)
                r.compose.assert_not_called()

    def test_busy_release_lock_never_loads_or_starts_a_second_installation(self):
        with patch.object(resume.os, "geteuid", return_value=0), \
             patch("builtins.open", mock_open()), \
             patch.object(resume.fcntl, "flock", side_effect=BlockingIOError("busy")), \
             patch.object(resume.deploy, "Release") as release, \
             patch.object(resume.sys, "argv", ["resume.py", "--state", "/fixture", "--revision", REV]):
            with self.assertRaises(BlockingIOError):
                resume.main()
        release.assert_not_called()


class RetryAfterRollbackTest(unittest.TestCase):
    def release(self, versions=("596",), running="old"):
        state = Path(self.enterContext(tempfile.TemporaryDirectory()))
        (state / "rehearsal-data").mkdir()
        (state / "database.dump").write_bytes(b"attempt 1")
        old = {"backend": "old-b", "frontend": "old-f"}
        r = SimpleNamespace(
            state=state, manifest={"revision": REV, "previous_revision": "b" * 40},
            data={"phase": "migrating", "protected": {"daemon_pid": "42"}, "old_images": old,
                  "old_containers": {"backend": "c1", "frontend": "c2", "postgres": "pg"},
                  "backup": {"versions": ["596"], "path": str(state / "database.dump")},
                  "rehearsal": {"schema_roundtrip": True}, "snapshot_time": "t", "rehearsal_container": "copy"},
            save=MagicMock(), wait=MagicMock(), versions=MagicMock(return_value=list(versions)),
            protected=MagicMock(return_value={"daemon_pid": "42"}),
            cid=MagicMock(side_effect=lambda service: "new-" + service),
            inspect=MagicMock(side_effect=lambda cid: {"Image": old[cid[4:]] if running == "old" else "new"}),
        )
        return r

    def test_verified_rollback_returns_to_completed_build(self):
        r = self.release()
        resume.reset_after_rollback(r)
        self.assertEqual(r.data["phase"], "building")
        self.assertNotIn("backup", r.data)
        self.assertNotIn("rehearsal", r.data)
        self.assertEqual(r.data["old_containers"], {"backend": "new-backend", "frontend": "new-frontend", "postgres": "pg"})
        self.assertFalse((r.state / "rehearsal-data").exists())
        self.assertEqual((r.state / "database.attempt-1.dump").read_bytes(), b"attempt 1")
        self.assertEqual(r.data["attempts"][0]["backup"]["path"], str(r.state / "database.attempt-1.dump"))
        r.wait.assert_called_once_with("b" * 40)
        r.save.assert_called_once()

    def test_unverified_rollback_changes_nothing(self):
        for kwargs in ({"versions": ("596", "612")}, {"running": "new"}):
            with self.subTest(**kwargs):
                r = self.release(**kwargs)
                with self.assertRaises(RuntimeError):
                    resume.reset_after_rollback(r)
                self.assertEqual(r.data["phase"], "migrating")
                self.assertTrue((r.state / "database.dump").exists())
                r.save.assert_not_called()

    def test_failure_is_logged_before_rollback(self):
        r = ResumeTest.release(self, "building")
        r.migrate.side_effect = RuntimeError("migration 607 failed")
        with patch.object(resume, "validate_images"), patch("sys.stderr", new_callable=io.StringIO) as err, \
             self.assertRaises(RuntimeError):
            resume.resume(r)
        self.assertIn("rolling back: migration 607 failed", err.getvalue())
        r.rollback.assert_called_once_with(automatic=True)


class ImageValidationTest(unittest.TestCase):
    def test_saved_images_and_rollback_files_must_match_the_pinned_source(self):
        state = Path(self.enterContext(tempfile.TemporaryDirectory()))
        limited = state / "down/migrations"
        limited.mkdir(parents=True)
        name, content = "597_fixture.down.sql", b"DROP TABLE fixture;"
        (limited / name).write_bytes(content)
        with tarfile.open(state / "source.tar.gz", "w:gz") as archive:
            member = tarfile.TarInfo("server/migrations/" + name)
            member.size = len(content)
            archive.addfile(member, io.BytesIO(content))
        images = {"backend": "sha256:backend", "frontend": "sha256:frontend"}
        override = {"services": {s: {"image": image} for s, image in images.items()}}
        (state / "new-images.json").write_text(json.dumps(override))
        run = MagicMock(side_effect=lambda *args: json.dumps([{
            "Id": args[-1], "Config": {"Labels": {"org.opencontainers.image.revision": REV}},
        }]))
        r = SimpleNamespace(state=state, package=state, manifest={"revision": REV},
                            data={"new_images": images}, run=run)
        resume.validate_images(r)
        (limited / name).write_text("tampered")
        with self.assertRaisesRegex(RuntimeError, "content drift"):
            resume.validate_images(r)
        (limited / name).write_bytes(content)
        run.return_value = json.dumps([{"Id": "sha256:wrong", "Config": {"Labels": {}}}])
        run.side_effect = None
        with self.assertRaisesRegex(RuntimeError, "revision mismatch"):
            resume.validate_images(r)
        r.data["new_images"] = {"backend": "sha256:backend"}
        with self.assertRaisesRegex(RuntimeError, "both completed images"):
            resume.validate_images(r)


class DaemonForceTest(unittest.TestCase):
    def test_busy_or_missing_counts_require_explicit_force(self):
        idle = {"active_task_count": 0, "pending_terminal_report_count": 0, "failed_terminal_report_count": 0}
        cli.require_idle(idle)
        for health in ({}, {**idle, "active_task_count": 1}, {**idle, "pending_terminal_report_count": 1}):
            with self.subTest(health=health):
                with self.assertRaisesRegex(RuntimeError, "explicitly authorized"):
                    cli.require_idle(health)
                cli.require_idle(health, force=True)

    def run_installer(self, force=False, wrong_pid=False, fail_new_health=False, replaced=False,
                      foreign_binary=False, prior=None, keep=None):
        root = keep or Path(self.enterContext(tempfile.TemporaryDirectory()))
        package = root / "package"
        (package / "bin").mkdir(parents=True)
        (package / "bin/multica").write_bytes(b"new binary")
        (package / "bin/foreign").write_bytes(b"other binary")
        target = root / "multica"
        target.write_bytes(b"old binary")
        unit_pid = [42]
        r = SimpleNamespace(
            state=root, package=package, manifest={"revision": REV},
            data={"phase": "installed", "protected": {"daemon_pid": "42"}, **(prior or {})},
            load=MagicMock(), save=MagicMock(), compose=MagicMock(), wait=MagicMock(), psql=MagicMock(),
        )

        def run(*args):
            if args[:2] == ("systemctl", "show"):
                return str(unit_pid[0])
            if args[:2] == ("systemctl", "start"):
                unit_pid[0] += 1
            return ""

        r.run = MagicMock(side_effect=run)

        def health(*args, **kwargs):
            return io.BytesIO(json.dumps({
                "status": "starting" if fail_new_health and unit_pid[0] > 42 else "running",
                "pid": 99 if wrong_pid else unit_pid[0],
                "cli_commit": "unknown", "active_task_count": 1,
                "pending_terminal_report_count": 0, "failed_terminal_report_count": 0,
            }).encode())

        def path(value):
            if str(value) == "/proc/42/exe":
                return target
            if str(value).startswith("/proc/") and str(value).endswith("/exe"):
                return target if unit_pid[0] > 42 and not foreign_binary else package / "bin/foreign"
            if str(value) == "/proc/42":
                return root / "stopped-process"
            return Path(value)

        real_open = builtins.open
        def open_file(file, *args, **kwargs):
            if str(file) == "/run/lock/multica-stt420.lock":
                return real_open(root / "lock", *args, **kwargs)
            return real_open(file, *args, **kwargs)
        argv = ["install-cli.py", "--state", str(root)] + (["--force"] if force else [])
        with patch.object(cli.os, "geteuid", return_value=0), \
             patch.object(resume.fcntl, "flock"), \
             patch("builtins.open", side_effect=open_file), \
             patch.object(cli.deploy, "Release", return_value=r), \
             patch.object(cli.urllib.request, "urlopen", side_effect=health), \
             patch.object(cli, "Path", side_effect=path), \
             patch.object(cli.sys, "argv", argv), \
             patch.object(cli.os, "readlink", return_value=str(target) + (" (deleted)" if replaced else "")), \
             patch.object(cli.time, "sleep"):
            try:
                cli.main()
                error = None
            except RuntimeError as exc:
                error = exc
        return r, target, error

    def test_force_updates_exact_daemon_and_records_the_permission(self):
        r, target, error = self.run_installer(force=True)
        self.assertIsNone(error)
        self.assertEqual(target.read_bytes(), b"new binary")
        self.assertEqual((r.state / "multica-before").read_bytes(), b"old binary")
        self.assertEqual(r.data["cli"]["phase"], "installed")
        self.assertTrue(r.data["cli"]["force"])
        self.assertEqual(r.data["protected"]["daemon_pid"], "43")
        r.psql.assert_not_called()
        self.assertIn(("systemctl", "stop", "multica-agents"), [call.args for call in r.run.call_args_list])

    def test_daemon_running_another_binary_is_not_accepted(self):
        r, target, error = self.run_installer(force=True, foreign_binary=True)
        self.assertRegex(str(error), "readiness timeout")
        self.assertEqual(target.read_bytes(), b"old binary")

    def test_retry_after_returned_old_binary(self):
        root = Path(self.enterContext(tempfile.TemporaryDirectory()))
        target = root / "multica"
        target.write_bytes(b"old binary")
        (root / "multica-before").write_bytes(b"old binary")
        failed = {"phase": "failed_returned_old_binary", "target": str(target),
                  "old_sha256": cli.deploy.sha(target)}
        r, target, error = self.run_installer(force=True, prior={"cli": failed}, keep=root)
        self.assertIsNone(error)
        self.assertEqual(target.read_bytes(), b"new binary")
        self.assertEqual(r.data["cli_attempts"], [failed])
        self.assertEqual((root / "multica-before.attempt-1").read_bytes(), b"old binary")
        self.assertEqual(r.data["cli"]["phase"], "installed")

    def test_binary_replaced_on_disk_after_start_is_updated(self):
        r, target, error = self.run_installer(force=True, replaced=True)
        self.assertIsNone(error)
        self.assertEqual(target.read_bytes(), b"new binary")
        self.assertEqual((r.state / "multica-before").read_bytes(), b"old binary")

    def test_busy_daemon_without_force_and_pid_mismatch_even_with_force_never_stop(self):
        for force, wrong_pid in ((False, False), (True, True)):
            with self.subTest(force=force, wrong_pid=wrong_pid):
                r, target, error = self.run_installer(force=force, wrong_pid=wrong_pid)
                self.assertIsNotNone(error)
                self.assertEqual(target.read_bytes(), b"old binary")
                self.assertNotIn("cli", r.data)
                r.compose.assert_not_called()

    def test_failed_new_daemon_restores_old_binary_and_updates_protected_pid(self):
        r, target, error = self.run_installer(force=True, fail_new_health=True)
        self.assertRegex(str(error), "readiness timeout")
        self.assertEqual(target.read_bytes(), b"old binary")
        self.assertEqual(r.data["cli"]["phase"], "failed_returned_old_binary")
        self.assertEqual(r.data["protected"]["daemon_pid"], "44")
