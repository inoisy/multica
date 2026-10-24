import fcntl
import hashlib
import importlib.util
from pathlib import Path
import subprocess
import tempfile
import unittest


spec = importlib.util.spec_from_file_location(
    "builder", Path(__file__).with_name("build-offline-cli-installer.py")
)
builder = importlib.util.module_from_spec(spec)
spec.loader.exec_module(builder)
COMMIT = "e3aa778f25cd2eab18606b5220b2505003a3f801"
OLD = b"#!/bin/sh\necho old-cli\n"
NEW = b"#!/bin/sh\necho 'multica test (commit: e3aa778f2, built: test)'\n"


class OfflineInstallerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="offline-cli-test-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.destination = self.root / "bin with spaces"
        self.destination.mkdir()
        self.target = self.destination / "multica"
        self.target.write_bytes(OLD)
        self.target.chmod(0o755)
        self.binary = self.root / "payload"
        self.script = self.root / "deploy.sh"
        self.package(NEW)

    def package(self, content):
        self.binary.write_bytes(content)
        builder.build(
            self.binary, self.script, COMMIT, hashlib.sha256(content).hexdigest()
        )

    def run_script(self, *args, ok=True):
        result = subprocess.run(
            ["bash", str(self.script), "--target", str(self.target), *args],
            text=True, capture_output=True, timeout=20,
        )
        if ok:
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        return result

    def backups(self):
        return sorted(self.destination.glob(".multica-cli-backups/*"))

    def test_check_does_not_replace_or_backup(self):
        self.run_script("--check")
        self.assertEqual(self.target.read_bytes(), OLD)
        self.assertEqual(self.backups(), [])
        self.assertFalse((self.destination / ".multica-cli-deploy.lock").exists())

    def test_install_repeat_and_manual_rollback(self):
        self.run_script()
        self.assertEqual(self.target.read_bytes(), NEW)
        self.assertEqual(self.target.stat().st_mode & 0o777, 0o755)
        copies = self.backups()
        self.assertEqual(len(copies), 1)
        self.assertEqual((copies[0] / "multica").read_bytes(), OLD)
        self.assertEqual(copies[0].stat().st_mode & 0o777, 0o700)
        self.run_script()
        self.assertEqual(self.backups(), copies)
        self.run_script("--rollback", str(copies[0]))
        self.assertEqual(self.target.read_bytes(), OLD)
        self.run_script("--rollback", str(copies[0]))

    def test_payload_corruption_is_rejected_before_execution(self):
        content = self.script.read_text()
        marker = "\n__MULTICA_CLI_PAYLOAD__\n"
        header = content.split(marker)[0]
        self.script.write_text(header + marker + "not-base64\n")
        self.run_script(ok=False)
        self.assertEqual(self.target.read_bytes(), OLD)
        self.assertEqual(self.backups(), [])

    def test_wrong_pinned_checksum_is_rejected(self):
        digest = hashlib.sha256(NEW).hexdigest()
        self.script.write_text(self.script.read_text().replace(digest, "0" * 64))
        self.run_script(ok=False)
        self.assertEqual(self.target.read_bytes(), OLD)

    def test_failed_health_restores_old_bytes(self):
        self.package(
            b'#!/bin/sh\ncase "$0" in */multica) exit 9;; esac\n'
            b"echo 'multica test (commit: e3aa778f2, built: test)'\n"
        )
        result = self.run_script(ok=False)
        self.assertEqual(self.target.read_bytes(), OLD)
        self.assertIn("Восстановлен предыдущий CLI", result.stdout)
        self.assertEqual(len(self.backups()), 1)
        self.assertEqual(list(self.destination.glob(".multica-cli-deploy.*[!k]")), [])

    def test_failed_fresh_install_removes_only_new_file(self):
        self.target.unlink()
        self.package(
            b'#!/bin/sh\ncase "$0" in */multica) exit 9;; esac\n'
            b"echo 'multica test (commit: e3aa778f2, built: test)'\n"
        )
        self.run_script(ok=False)
        self.assertFalse(self.target.exists())
        self.assertEqual(self.backups(), [])

    def test_corrupted_backup_is_not_restored(self):
        self.run_script()
        (self.backups()[0] / "multica").write_bytes(b"corrupt")
        self.run_script("--rollback", str(self.backups()[0]), ok=False)
        self.assertEqual(self.target.read_bytes(), NEW)

    def test_later_release_is_not_overwritten_by_rollback(self):
        self.run_script()
        self.target.write_bytes(b"later-release")
        self.run_script("--rollback", str(self.backups()[0]), ok=False)
        self.assertEqual(self.target.read_bytes(), b"later-release")

    def test_symlink_target_is_not_replaced(self):
        real = self.destination / "actual"
        self.target.rename(real)
        self.target.symlink_to(real)
        self.run_script(ok=False)
        self.assertTrue(self.target.is_symlink())
        self.assertEqual(real.read_bytes(), OLD)

    def test_parallel_installer_is_refused(self):
        with (self.destination / ".multica-cli-deploy.lock").open("w") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            self.run_script(ok=False)
        self.assertEqual(self.target.read_bytes(), OLD)
        self.assertEqual(self.backups(), [])

    def test_builder_checks_the_input_hash(self):
        with self.assertRaisesRegex(ValueError, "does not match"):
            builder.build(self.binary, self.script, COMMIT, "0" * 64)


if __name__ == "__main__":
    unittest.main()
