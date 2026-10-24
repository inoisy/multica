"""Deployment and rollback tests; all host commands are isolated fakes."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("deploy.sh")
OLD = "sha256:" + "a" * 64
NEW = "sha256:" + "b" * 64

FAKE = r'''#!/usr/bin/env python3
import io,json,os,pathlib,sys,tarfile,time
name=pathlib.Path(sys.argv[0]).name
a=sys.argv[1:]
root=pathlib.Path(os.environ['FAKE_ROOT'])
p=root/'state.json'
s=json.loads(p.read_text())
with (root/'calls.jsonl').open('a') as f: f.write(json.dumps([name,*a])+'\n')
old='sha256:'+'a'*64
new='sha256:'+'b'*64
def save(): p.write_text(json.dumps(s))
if name=='id': print(0)
elif name=='flock': pass
elif name=='systemctl': print(123)
elif name=='sleep': time.sleep(.15)
elif name=='git':
    cmd=a[2]
    if cmd=='rev-parse': print(a[-1].removesuffix('^{commit}'))
    elif cmd=='diff': print('server/unsafe.go' if os.getenv('BACKEND_DIFF') else 'packages/views/dashboard/page.tsx')
    elif cmd=='archive':
        with tarfile.open(fileobj=sys.stdout.buffer,mode='w|') as t:
            data=b'FROM scratch\n'
            info=tarfile.TarInfo('Dockerfile.web'); info.size=len(data)
            t.addfile(info,io.BytesIO(data))
elif name=='curl':
    if '-w' in a:
        print('503' if os.getenv('FAIL_HEALTH') and s['current']==new else '200',end='')
    else: print(json.dumps({'status':'ok','commit':os.getenv('BACKEND_COMMIT','aebbec787')}))
elif name=='docker':
    if a[0]=='compose':
        if '-p' in a:
            assert a[a.index('-p')+1]=='prod', a
        else:
            assert a[1:3]==['-f',os.environ['COMPOSE']], a
        if 'config' in a:
            print('backend\npostgres\nfrontend'+('\nweb' if os.getenv('AMBIGUOUS') else ''))
        elif 'ps' in a: print(a[-1]+'-cid')
        elif 'up' in a:
            assert a[-1]=='frontend', a
            assert '--no-deps' in a and '--no-build' in a and '--pull' in a, a
            s['current']=s['live_tag']; save()
    elif a[0]=='inspect':
        fmt=a[a.index('-f')+1]; cid=a[-1]
        if 'config_files' in fmt: print(os.environ['COMPOSE'])
        elif 'working_dir' in fmt: print(os.environ['PROD_DIR'])
        elif 'com.docker.compose.project' in fmt: print('prod')
        elif '.Config.Image' in fmt: print('multica-web:live')
        elif '.State.StartedAt' in fmt: print(s['backend_start'] if cid=='backend-cid' else 'original-start')
        elif '.Image' in fmt: print(s['current'] if cid=='frontend-cid' else 'sha256:protected')
        else: raise Exception(a)
    elif a[:2]==['image','inspect']: print(new)
    elif a[0]=='tag':
        if a[-1]=='multica-web:live': s['live_tag']=a[-2]; save()
    elif a[:2]==['buildx','build']:
        if os.getenv('FAIL_BUILD'): sys.exit(1)
        if os.getenv('STACK_RACE'): s['backend_start']='restarted'; save()
    elif a[:2]!=['buildx','inspect']: raise Exception(a)
else: raise Exception(name)
'''


class DeployTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        bin_dir = self.root / "bin"
        bin_dir.mkdir()
        for command in ["docker", "git", "curl", "id", "flock", "systemctl", "sleep"]:
            target = bin_dir / command
            target.write_text(FAKE)
            target.chmod(0o755)
        production = self.root / "production stack"
        production.mkdir()
        compose = production / "compose.yml"
        compose.write_text("services: {}\n")
        fork = self.root / "fork"
        (fork / ".git").mkdir(parents=True)
        backups = self.root / "backups"
        backups.mkdir()
        self.write_state({"current": OLD, "live_tag": OLD, "backend_start": "original-start"})
        self.env = {
            **os.environ, "PATH": f"{bin_dir}:{os.environ['PATH']}",
            "FAKE_ROOT": str(self.root), "FORK": str(fork), "COMPOSE": str(compose),
            "PROD_DIR": str(production), "BACKUP_ROOT": str(backups),
            "LOCK_FILE": str(self.root / "lock"), "TMPDIR": str(self.root),
            "HEALTH_WAIT_SECONDS": "1",
        }

    def write_state(self, value):
        (self.root / "state.json").write_text(json.dumps(value))

    def state(self):
        return json.loads((self.root / "state.json").read_text())

    def run_script(self, *args, **extra):
        return subprocess.run(["bash", str(SCRIPT), *args], env={**self.env, **extra}, text=True, capture_output=True, timeout=20)

    def calls(self):
        return [json.loads(line) for line in (self.root / "calls.jsonl").read_text().splitlines()]

    def test_check_does_not_build_tag_or_restart(self):
        result = self.run_script("--check")
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertFalse(any(c[:2] == ["docker", "tag"] or c[:3] == ["docker", "buildx", "build"] or "up" in c for c in self.calls()))
        self.assertFalse(any(c[:2] == ["docker", "ps"] for c in self.calls()))

    def test_success_and_manual_rollback_only_replace_web(self):
        result = self.run_script()
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertEqual(self.state()["current"], NEW)
        backup = next((self.root / "backups").iterdir())
        self.assertTrue((backup / "DEPLOYED").exists())
        result = self.run_script("--rollback", str(backup))
        self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertEqual(self.state()["current"], OLD)
        self.assertTrue((backup / "ROLLED_BACK").exists())
        self.assertEqual([c[-1] for c in self.calls() if "up" in c], ["frontend", "frontend"])
        self.assertEqual(self.state()["backend_start"], "original-start")

    def test_failed_health_rolls_back_and_returns_failure(self):
        result = self.run_script(FAIL_HEALTH="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.state()["current"], OLD)
        self.assertIn("откат выполнен", result.stdout)

    def test_ambiguous_web_services_stop_before_mutation(self):
        result = self.run_script(AMBIGUOUS="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(any(c[:2] == ["docker", "tag"] for c in self.calls()))

    def test_backend_changes_or_wrong_version_are_rejected(self):
        for extra in [{"BACKEND_DIFF": "1"}, {"BACKEND_COMMIT": "old-version"}]:
            with self.subTest(extra=extra):
                result = self.run_script(**extra)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(self.state()["current"], OLD)

    def test_build_failure_keeps_live_web(self):
        result = self.run_script(FAIL_BUILD="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.state()["current"], OLD)
        self.assertFalse(any("up" in c for c in self.calls()))

    def test_stack_change_during_build_prevents_switch(self):
        result = self.run_script(STACK_RACE="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.state()["current"], OLD)
        self.assertFalse(any("up" in c for c in self.calls()))

    def test_rollback_does_not_overwrite_a_later_release(self):
        self.assertEqual(self.run_script().returncode, 0)
        state = self.state()
        state["current"] = "sha256:later-release"
        self.write_state(state)
        backup = next((self.root / "backups").iterdir())
        result = self.run_script("--rollback", str(backup))
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.state()["current"], "sha256:later-release")


if __name__ == "__main__":
    unittest.main()
