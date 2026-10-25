#!/usr/bin/env python3
"""Continue a stopped pre-migration installation using its immutable images."""
import argparse
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import urllib.request

sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("stt420_deploy", Path(__file__).with_name("deploy.py"))
deploy = importlib.util.module_from_spec(spec)
spec.loader.exec_module(deploy)


def installed(release):
    deploy.require(release.versions() == release.manifest["migrations"], "installed schema drift")
    for service, image in release.data["new_images"].items():
        deploy.require(release.inspect(release.cid(service))["Image"] == image, "installed image drift: " + service)
    return release.wait(release.manifest["revision"])


def validate_images(release):
    images = release.data.get("new_images", {})
    deploy.require(set(images) == {"backend", "frontend"}, "both completed images are required; build was interrupted")
    expected = {"services": {s: {"image": image} for s, image in images.items()}}
    deploy.require(json.loads((release.state / "new-images.json").read_text()) == expected, "image override drift")
    for service, image in images.items():
        info = json.loads(release.run("docker", "image", "inspect", image))[0]
        deploy.require(info["Id"] == image and
                       info["Config"]["Labels"].get("org.opencontainers.image.revision") == release.manifest["revision"],
                       "image revision mismatch: " + service)
    # A resumed down must use exactly the migrations from the pinned source.
    limited = release.state / "down/migrations"
    with tarfile.open(release.package / "source.tar.gz") as archive:
        expected_files = {}
        for member in archive.getmembers():
            path = Path(member.name)
            if member.isfile() and path.parent.as_posix() == "server/migrations" and path.suffix == ".sql":
                if 597 <= int(path.name.split("_")[0]) <= 612:
                    expected_files[path.name] = archive.extractfile(member).read()
        deploy.require(expected_files and {p.name for p in limited.iterdir()} == set(expected_files), "rollback migration set drift")
        for name, content in expected_files.items():
            deploy.require((limited / name).read_bytes() == content, "rollback migration content drift: " + name)


def phase(release, value):
    release.data["phase"] = value
    release.save()
    print("PHASE=" + value, flush=True)


def resume(release):
    if release.data["phase"] == "installed":
        installed(release)
        print("ALREADY_INSTALLED=" + release.manifest["revision"], flush=True)
        return
    deploy.require(release.data["phase"] == "building", "only completed-build state can resume automatically; inspect status/rollback")
    deploy.require(not release.data.get("backup") and not release.data.get("rehearsal"),
                   "unexpected backup/rehearsal in building state; inspect before resuming")
    validate_images(release)
    saved = (release.compose_files[:], release.project, release.workdir)
    release.check()
    deploy.require((release.compose_files, release.project, release.workdir) == saved, "Compose stack drift")
    for path, checksum in release.data["config_checksums"].items():
        deploy.require(deploy.sha(path) == checksum, "Compose file drift")
    deploy.require(release.protected() == release.data["protected"], "PostgreSQL or daemon changed")
    for service, container in release.data["old_containers"].items():
        deploy.require(release.cid(service) == container, "baseline container changed: " + service)
    phase(release, "stopping")
    try:
        release.compose("stop", "frontend", "backend")
        phase(release, "stopped")
        print("STEP=backup", flush=True)
        release.backup()
        print("STEP=rehearsal", flush=True)
        release.rehearse()
        deploy.require(release.protected() == release.data["protected"], "protected service changed during rehearsal")
        phase(release, "migrating")
        release.migrate("up")
        deploy.require(release.versions() == release.manifest["migrations"], "target migrations incomplete")
        release.compose("up", "-d", "--no-deps", "--no-build", "--pull", "never", "backend", "frontend",
                        override=release.state / "new-images.json")
        release.data["health_after"] = installed(release)
        deploy.require(release.protected() == release.data["protected"], "protected service changed")
        release.data["deployed_at"] = deploy.utc()
        phase(release, "installed")
    except BaseException as exc:
        print("STT420 FAILED, rolling back: " + str(exc), file=sys.stderr, flush=True)
        release.rollback(automatic=True)
        raise
    print("INSTALLED=" + release.manifest["revision"], flush=True)


def reset_after_rollback(release):
    """Return a state whose automatic rollback restored 596 to the completed-build phase."""
    data = release.data
    deploy.require(data["phase"] in ("migrating", "rolled_back") and data.get("backup"),
                   "retry needs a state that reached backup and was rolled back")
    deploy.require(release.versions() == data["backup"]["versions"], "schema is not the pre-release one; inspect before retry")
    current = {}
    for service, image in data["old_images"].items():
        current[service] = release.cid(service)
        deploy.require(release.inspect(current[service])["Image"] == image, "old image is not running: " + service)
    release.wait(release.manifest["previous_revision"])
    deploy.require(release.protected() == data["protected"], "PostgreSQL or daemon changed")
    attempt = len(data.get("attempts", [])) + 1
    record = {"phase": data["phase"], "failed_after": data.get("updated_at"), "backup": data.pop("backup"),
              "rehearsal": data.pop("rehearsal", None), "snapshot_time": data.pop("snapshot_time", None)}
    # The isolated copy is scratch; the pre-attempt dump is kept beside the new one.
    shutil.rmtree(release.state / "rehearsal-data", ignore_errors=True)
    dump = release.state / "database.dump"
    if dump.exists():
        record["backup"]["path"] = str(dump.rename(release.state / f"database.attempt-{attempt}.dump"))
    data.pop("rehearsal_container", None)
    data.setdefault("attempts", []).append(record)
    data["old_containers"].update(current)
    data["phase"] = "building"
    release.save()
    print("RESET_AFTER_ROLLBACK=" + str(attempt), flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--state", required=True, type=Path)
    parser.add_argument("--revision", required=True)
    parser.add_argument("--update-daemon", action="store_true")
    parser.add_argument("--force-daemon", action="store_true")
    parser.add_argument("--retry-after-rollback", action="store_true")
    args = parser.parse_args()
    deploy.require(os.geteuid() == 0, "run as root")
    deploy.require(not args.force_daemon or args.update_daemon, "--force-daemon requires --update-daemon")
    os.umask(0o077)
    with open("/run/lock/multica-stt420.lock", "w") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        release = deploy.Release(args.state.resolve() / "package", "/opt/multica/docker-compose.selfhost.yml", "", "")
        release.load(args.state)
        deploy.require(release.manifest["revision"] == args.revision == release.data["revision"], "release SHA mismatch")
        deploy.require_acceptance(release.package / "acceptance.json", args.revision)
        print("STATE=" + str(release.state), flush=True)
        if args.retry_after_rollback and release.data["phase"] in ("migrating", "rolled_back"):
            reset_after_rollback(release)
        resume(release)
    # The daemon installer takes the same lock and has its own PID/health checks.
    if args.update_daemon:
        if release.data.get("cli", {}).get("phase") == "installed":
            with urllib.request.urlopen("http://127.0.0.1:19514/health", timeout=5) as response:
                health = json.load(response)
            pid = int(release.run("systemctl", "show", "multica-agents", "-p", "MainPID", "--value"))
            deploy.require(health.get("status") == "running" and health.get("pid") == pid
                           and health.get("cli_commit") == args.revision, "recorded daemon revision/health drift")
            print("CLI_ALREADY_INSTALLED=" + args.revision, flush=True)
        else:
            command = [sys.executable, str(Path(__file__).with_name("install-cli.py")), "--state", str(args.state)]
            if args.force_daemon:
                command.append("--force")
            subprocess.run(command, check=True)


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        print("STT420 RESUME: " + str(exc), file=sys.stderr)
        sys.exit(1)
