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


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--state", required=True)
    p.add_argument("--health-port", type=int, default=19514)
    args = p.parse_args()
    deploy.require(os.geteuid() == 0, "run as root")
    os.umask(0o077)
    import fcntl
    with open("/run/lock/multica-stt420.lock", "w") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        r = deploy.Release(Path(__file__).parent, "/opt/multica/docker-compose.selfhost.yml", "", "")
        r.load(args.state)
        deploy.require(r.data["phase"] == "installed", "backend/web release must be installed first")
        deploy.require("cli" not in r.data, "CLI operation already recorded; inspect its state")

        def health():
            with urllib.request.urlopen(f"http://127.0.0.1:{args.health_port}/health", timeout=5) as response:
                return json.load(response)

        def idle():
            h = health()
            pid = int(r.run("systemctl", "show", "multica-agents", "-p", "MainPID", "--value"))
            deploy.require(h.get("pid") == pid and pid > 1, "daemon PID/unit mismatch")
            deploy.require(h.get("active_task_count") == 0 and h.get("pending_terminal_report_count") == 0
                           and h.get("failed_terminal_report_count") == 0, "daemon is busy or has unsent reports; retry after this run ends")
            return pid

        pid = idle()
        deploy.require(r.psql("SELECT count(*) FROM agent_task_queue WHERE status IN ('running', 'dispatched')") == "0", "active server runs; leave daemon unchanged")
        target = Path(f"/proc/{pid}/exe").resolve(strict=True)
        deploy.require(target.is_file() and not str(target).endswith(" (deleted)"), "daemon binary cannot be replaced safely")
        old = r.state / "multica-before"
        deploy.require(not old.exists(), "existing CLI backup must be inspected")
        shutil.copy2(target, old)
        r.data["cli"] = {"target": str(target), "old_pid": pid, "old_sha256": deploy.sha(old),
                         "health_before": health(), "phase": "prepared"}
        r.save()
        override = r.state / "new-images.json"
        # The backend barrier prevents a new claim between the final idle check and stop.
        r.compose("stop", "backend", override=override)
        stopped = False
        changed = False
        try:
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
                    deploy.require(h.get("status") == "running" and h.get("cli_commit") == r.manifest["revision"], "new daemon health/revision mismatch")
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
