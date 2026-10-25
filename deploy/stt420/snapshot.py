#!/usr/bin/env python3
"""Operator-only consistent production export; never stops or migrates services."""
import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import pwd
import select
import subprocess
import urllib.request


def run(*args):
    # Diagnostics from Docker/SQL may contain connection details.
    result = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
    if result.returncode:
        raise RuntimeError(f"{args[0]} failed ({result.returncode}); no changes made to production")
    return result.stdout.decode().strip()


def export(compose, out, owner, source_revision, api):
    out = Path(out).absolute()
    user = pwd.getpwnam(owner)
    if len(source_revision) != 40 or any(c not in "0123456789abcdef" for c in source_revision):
        raise ValueError("source revision must be SHA40")
    with urllib.request.urlopen(api.rstrip("/") + "/health", timeout=10) as response:
        health = json.load(response)
    commit = health.get("commit", "")
    if health.get("status") != "ok" or len(commit) < 9 or not source_revision.startswith(commit):
        raise RuntimeError("source revision does not match backend health")
    # Refuse reuse, symlinks and partial exports from previous attempts.
    out.mkdir(mode=0o700)
    prefix = ["docker", "compose", "--project-directory", str(Path(compose).resolve().parent), "-f", str(Path(compose).resolve())]
    backend = run(*prefix, "ps", "-q", "backend").split()
    if len(backend) != 1:
        raise RuntimeError("expected one production backend")
    inspected = json.loads(run("docker", "inspect", backend[0]))[0]
    labels = inspected["Config"]["Labels"]
    prefix = ["docker", "compose", "--project-directory", labels["com.docker.compose.project.working_dir"], "-p", labels["com.docker.compose.project"]]
    for file in labels["com.docker.compose.project.config_files"].split(","):
        prefix += ["-f", file]
    services = {}
    for service in ("backend", "frontend", "postgres"):
        ids = run(*prefix, "ps", "-q", service).split()
        if len(ids) != 1:
            raise RuntimeError("expected one running " + service)
        services[service] = ids[0]
    if services["backend"] != backend[0]:
        raise RuntimeError("Compose labels point at another stack")
    postgres = services["postgres"]
    pg = json.loads(run("docker", "inspect", postgres))[0]
    sql = """BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;
SELECT json_build_object(
 'snapshot', pg_export_snapshot(), 'T', to_char(transaction_timestamp() AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
 'database', current_database(), 'postgres_version', current_setting('server_version'),
 'migrations', (SELECT json_agg(version ORDER BY version) FROM schema_migrations),
 'extensions', (SELECT json_object_agg(extname, extversion) FROM pg_extension));
"""
    command = ["docker", "exec", "-i", postgres, "sh", "-c",
               'exec psql -X -qAt -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB"']
    started = datetime.now(timezone.utc).isoformat()
    session = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
    try:
        session.stdin.write(sql)
        session.stdin.flush()
        if not select.select([session.stdout], [], [], 30)[0]:
            raise RuntimeError("snapshot session timeout")
        metadata = json.loads(session.stdout.readline())
        migrations = metadata.get("migrations") or []
        if not migrations or max(int(v.split("_")[0]) for v in migrations) != 596:
            raise RuntimeError("expected source schema 596; no migration or export attempted")
        dump = out / "database.dump"
        with dump.open("xb") as output:
            result = subprocess.run(["docker", "exec", postgres, "sh", "-c",
                'exec pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc --snapshot="$1"', "_", metadata["snapshot"]],
                stdout=output, stderr=subprocess.DEVNULL, timeout=3600)
        if result.returncode:
            raise RuntimeError("pg_dump failed; incomplete directory must not be used")
        session.stdin.write("COMMIT;\n\\q\n")
        session.stdin.flush()
        if session.wait(timeout=30):
            raise RuntimeError("snapshot session failed")
        for service, expected in services.items():
            if run(*prefix, "ps", "-q", service) != expected:
                raise RuntimeError("service changed during export; repeat snapshot")
        with dump.open("rb") as stream:
            checksum = hashlib.file_digest(stream, "sha256").hexdigest()
        manifest = {**metadata, "kind": "production-snapshot", "dump": "database.dump", "sha256": checksum,
                    "source_backend_image": inspected["Image"],
                    "source_revision": source_revision,
                    "postgres_image": pg["Image"], "started_at": started,
                    "completed_at": datetime.now(timezone.utc).isoformat(), "production_mutated": False}
        (out / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
        for path in (dump, out / "manifest.json"):
            path.chmod(0o600)
            os.chown(path, user.pw_uid, user.pw_gid)
        os.chown(out, user.pw_uid, user.pw_gid)
        return {"directory": str(out), "manifest": str(out / "manifest.json"), "sha256": checksum, "T": metadata["T"]}
    finally:
        if session.poll() is None:
            session.terminate()
            session.wait(timeout=30)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--compose", default="/opt/multica/docker-compose.selfhost.yml")
    parser.add_argument("--out", required=True, type=Path)
    parser.add_argument("--owner", default="agents")
    parser.add_argument("--source-revision", required=True)
    parser.add_argument("--api", default="http://127.0.0.1:8080")
    args = parser.parse_args()
    if os.geteuid() != 0:
        parser.error("run as operator/root; this script does not elevate privileges")
    os.umask(0o077)
    print(json.dumps(export(args.compose, args.out, args.owner, args.source_revision, args.api)))


if __name__ == "__main__":
    main()
