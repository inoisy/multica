#!/usr/bin/env python3
"""Install the bundle's CLI/daemon only after an idle check and claim barrier."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import shutil
import sys
import time
import urllib.request

spec = importlib.util.spec_from_file_location("stt420_release", Path(__file__).with_name("deploy.py"))
deploy = importlib.util.module_from_spec(spec)
spec.loader.exec_module(deploy)


def require_idle(health, force=False):
    idle = all(health.get(key) == 0 for key in (
        "active_task_count", "pending_terminal_report_count", "failed_terminal_report_count"))
    deploy.require(idle or force, "daemon is busy or has unsent reports; retry after this run ends or use explicitly authorized --force")


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--state", required=True)
    p.add_argument("--health-port", type=int, default=19514)
    p.add_argument("--force", action="store_true", help="Operator-authorized restart even with active runs or unsent reports")
    args = p.parse_args()
    deploy.require(os.geteuid() == 0, "run as root")
    os.umask(0o077)
    import fcntl
    with open("/run/lock/multica-stt420.lock", "w") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        r = deploy.Release(Path(args.state).resolve() / "package", "/opt/multica/docker-compose.selfhost.yml", "", "")
        r.load(args.state)
        deploy.require(r.data["phase"] == "installed", "backend/web release must be installed first")
        old = r.state / "multica-before"
        previous = r.data.get("cli")
        if previous and previous.get("phase") == "failed_returned_old_binary":
            # The old binary is back in place; keep that attempt and start over.
            deploy.require(deploy.sha(previous["target"]) == previous["old_sha256"], "daemon binary changed after the failed attempt")
            attempt = len(r.data.get("cli_attempts", [])) + 1
            old.rename(r.state / f"multica-before.attempt-{attempt}")
            r.data.setdefault("cli_attempts", []).append(r.data.pop("cli"))
            r.save()
        deploy.require("cli" not in r.data, "CLI operation already recorded; inspect its state")
        release_sha = deploy.sha(r.package / "bin/multica")

        def health():
            with urllib.request.urlopen(f"http://127.0.0.1:{args.health_port}/health", timeout=5) as response:
                return json.load(response)

        def idle():
            h = health()
            pid = int(r.run("systemctl", "show", "multica-agents", "-p", "MainPID", "--value"))
            deploy.require(h.get("pid") == pid and pid > 1, "daemon PID/unit mismatch")
            require_idle(h, args.force)
            return pid

        pid = idle()
        if not args.force:
            deploy.require(r.psql("SELECT count(*) FROM agent_task_queue WHERE status IN ('running', 'dispatched')") == "0", "active server runs; leave daemon unchanged")
        else:
            print("FORCE_DAEMON: operator-authorized restart; active runs may be interrupted", flush=True)
        # A binary replaced on disk after the daemon started shows as "(deleted)";
        # the file at that path is what the unit starts next, so it is the one kept.
        target = Path(os.readlink(f"/proc/{pid}/exe").removesuffix(" (deleted)"))
        deploy.require(target.is_absolute() and target.is_file(), "daemon binary cannot be replaced safely")
        deploy.require(not old.exists(), "existing CLI backup must be inspected")
        shutil.copy2(target, old)
        r.data["cli"] = {"target": str(target), "old_pid": pid, "old_sha256": deploy.sha(old),
                         "health_before": health(), "phase": "prepared", "force": args.force}
        r.save()
        override = r.state / "new-images.json"
        # The backend barrier prevents a new claim between the final idle check and stop.
        stopped = False
        changed = False
        try:
            r.compose("stop", "backend", override=override)
            deploy.require(idle() == pid, "daemon changed before stop")
            r.run("systemctl", "stop", "multica-agents")
            stopped = True
            deploy.require(not Path(f"/proc/{pid}").exists(), "old daemon did not stop")
            temp = target.with_name(target.name + ".stt420-new")
            shutil.copy2(r.package / "bin/multica", temp)
            temp.chmod(0o755)
            temp.replace(target)
            changed = True
            r.compose("up", "-d", "--no-deps", "--no-build", "--pull", "never", "backend", override=override)
            r.wait(r.manifest["revision"])
            r.run("systemctl", "start", "multica-agents")
            for i in range(60):
                try:
                    h = health()
                    # The bundle CLI reports its SHA in cli_version only, so the
                    # running executable itself is compared with the bundle.
                    unit_pid = int(r.run("systemctl", "show", "multica-agents", "-p", "MainPID", "--value"))
                    deploy.require(h.get("status") == "running" and h.get("pid") == unit_pid
                                   and deploy.sha(Path(f"/proc/{unit_pid}/exe")) == release_sha,
                                   "new daemon health/binary mismatch")
                    break
                except Exception:
                    deploy.require(i < 59, "new daemon readiness timeout")
                    time.sleep(2)
            r.data["cli"].update(phase="installed", health_after=h, installed_at=deploy.utc())
            r.data["protected"]["daemon_pid"] = str(h["pid"])
            r.save()
        except BaseException:
            if stopped:
                r.run("systemctl", "stop", "multica-agents")
                if changed:
                    temp = target.with_name(target.name + ".stt420-rollback")
                    shutil.copy2(old, temp)
                    temp.replace(target)
            r.compose("up", "-d", "--no-deps", "--no-build", "--pull", "never", "backend", override=override)
            if stopped:
                r.run("systemctl", "start", "multica-agents")
                r.data["protected"]["daemon_pid"] = r.run("systemctl", "show", "multica-agents", "-p", "MainPID", "--value")
            r.data["cli"]["phase"] = "failed_returned_old_binary"
            r.save()
            raise
        print("CLI_INSTALLED=" + r.manifest["revision"])


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        print("STT420 CLI: " + str(exc), file=sys.stderr)
        sys.exit(1)
