import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


class InstallEntryTest(unittest.TestCase):
    def run_entry(self, *, tamper=False, acceptance=True, preflight=0):
        tmp = Path(self.enterContext(tempfile.TemporaryDirectory()))
        bundle = tmp / "bundle with spaces"
        bundle.mkdir()
        shutil.copy2(Path(__file__).with_name("install.sh"), bundle / "install.sh")
        (bundle / "deploy.py").write_text("# test fixture\n")
        digest = hashlib.sha256((bundle / "deploy.py").read_bytes()).hexdigest()
        (bundle / "SHA256SUMS").write_text(f"{digest}  deploy.py\n")
        if tamper:
            (bundle / "deploy.py").write_text("# changed after packaging\n")
        if acceptance:
            (bundle / "acceptance.json").write_text("{}")
        fakebin = tmp / "bin"
        fakebin.mkdir()
        for name, body in {
            "id": "#!/bin/sh\nprintf '0\\n'\n",
            "python3": '#!/bin/sh\nprintf "%s\\n" "$@" >> "$ENTRY_CALLS"\n'
                       f'if [ "$2" = check ]; then exit {preflight}; fi\n',
        }.items():
            path = fakebin / name
            path.write_text(body)
            path.chmod(0o755)
        calls = tmp / "calls"
        result = subprocess.run(
            ["sh", str(bundle / "install.sh"), "--compose", "/host path/compose.yml"],
            env={**os.environ, "PATH": f"{fakebin}:{os.environ['PATH']}", "ENTRY_CALLS": str(calls)},
            capture_output=True, text=True,
        )
        return result, calls.read_text().splitlines() if calls.exists() else [], bundle

    def test_verifies_then_runs_preflight_and_install_with_the_same_options(self):
        result, calls, bundle = self.run_entry()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(calls, [
            str(bundle / "deploy.py"), "check", "--compose", "/host path/compose.yml",
            str(bundle / "deploy.py"), "install", "--acceptance", str(bundle / "acceptance.json"),
            "--compose", "/host path/compose.yml",
        ])

    def test_tampered_bundle_and_missing_report_never_reach_python(self):
        for options in ({"tamper": True}, {"acceptance": False}):
            with self.subTest(options=options):
                result, calls, _ = self.run_entry(**options)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(calls, [])

    def test_preflight_failure_prevents_install(self):
        result, calls, _ = self.run_entry(preflight=7)
        self.assertEqual(result.returncode, 7)
        self.assertNotIn("install", calls)
