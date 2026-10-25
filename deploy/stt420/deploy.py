#!/usr/bin/env python3
"""Root operator: check, install, status or rollback the pinned backend/web."""
import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.request
from datetime import datetime, timezone


def require(ok, message):
    if not ok:
        raise RuntimeError(message)


def sha(path):
    with open(path, "rb") as f:
        return hashlib.file_digest(f, "sha256").hexdigest()


def utc():
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


class Release:
    def __init__(self, package, compose, api, web):
        self.package = Path(package).resolve()
        self.manifest = json.loads((self.package / "manifest.json").read_text())
        self.compose_files = [str(Path(compose).resolve())]
        self.project = None
        self.workdir = str(Path(compose).resolve().parent)
        self.api, self.web = api.rstrip("/"), web.rstrip("/")
        self.state = None
        self.data = {}

    def run(self, *args, input=None, output=None, cwd=None):
        result = subprocess.run(args, input=input, stdout=output or subprocess.PIPE,
                                stderr=subprocess.STDOUT if output else subprocess.PIPE, cwd=cwd, timeout=3600)
        if result.returncode:
            if self.state:
                with (self.state / "command-errors.log").open("ab") as f:
                    f.write(result.stderr or b"see build log\n")
            # Compose can include secrets in diagnostics; keep them root-only.
            raise RuntimeError(f"{args[0]} failed ({result.returncode}); see root-only command-errors.log")
        return (result.stdout or b"").decode().strip() if not output else ""

    def compose(self, *args, override=None, **kw):
        prefix = ["docker", "compose", "--project-directory", self.workdir]
        if self.project:
            prefix += ["-p", self.project]
        for f in self.compose_files + ([str(override)] if override else []):
            prefix += ["-f", f]
        return self.run(*prefix, *args, **kw)

    def inspect(self, cid):
        return json.loads(self.run("docker", "inspect", cid))[0]

    def cid(self, service):
        ids = self.compose("ps", "-q", service).split()
        require(len(ids) == 1, "expected one running " + service)
        return ids[0]

    def health(self):
        with urllib.request.urlopen(self.api + "/health", timeout=5) as r:
            data = json.load(r)
        require(data.get("status") == "ok", "backend health is not ok")
        return data

    def wait(self, revision):
        for _ in range(60):
            try:
                h = self.health()
                require(len(h.get("commit", "")) >= 9 and revision.startswith(h["commit"]), "wrong backend revision")
                with urllib.request.urlopen(self.web + "/login", timeout=5) as r:
                    require(r.status == 200, "web login failed")
                with urllib.request.urlopen(self.web + "/profiles", timeout=5) as r:
                    require(r.status == 200, "web profiles failed")
                return h
            except Exception:
                time.sleep(2)
        raise RuntimeError("backend/web smoke timeout")

    def psql(self, query):
        return self.compose("exec", "-T", "postgres", "sh", "-c",
                            'exec psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc "$1"', "_", query)

    def versions(self):
        return self.psql("SELECT version FROM schema_migrations ORDER BY version").splitlines()

    def protected(self):
        pg = self.inspect(self.cid("postgres"))
        return {"postgres_id": pg["Id"], "postgres_started": pg["State"]["StartedAt"],
                "daemon_pid": self.run("systemctl", "show", "multica-agents", "-p", "MainPID", "--value")}

    def verify_bundle(self):
        require(os.uname().machine == "x86_64", "bundle requires linux/amd64")
        required = {"source.tar.gz", "deploy.py", "install-cli.py", "snapshot.py", "README.md",
                    "bin/server", "bin/migrate", "bin/multica", "bin/choice-before", "bin/choice-after"}
        require(set(self.manifest["checksums"]) == required, "incomplete or unexpected bundle files")
        for name, checksum in self.manifest["checksums"].items():
            path = (self.package / name).resolve()
            require(path.is_relative_to(self.package) and sha(path) == checksum, "bundle checksum mismatch: " + name)
        revision = self.manifest["revision"]
        require(len(revision) == 40 and all(c in "0123456789abcdef" for c in revision), "revision must be SHA40")
        require(self.manifest["source_schema"] == 596 and self.manifest["target_schema"] == 612, "unsupported migration range")

    def check(self):
        self.verify_bundle()
        backend = self.inspect(self.cid("backend"))
        labels = backend["Config"]["Labels"]
        self.project = labels["com.docker.compose.project"]
        self.workdir = labels["com.docker.compose.project.working_dir"]
        self.compose_files = labels["com.docker.compose.project.config_files"].split(",")
        require(self.cid("backend") == backend["Id"], "Compose labels point at another stack")
        services = set(self.compose("config", "--services").split())
        require(services == {"backend", "frontend", "postgres"}, "unreviewed services in production stack")
        self.cid("frontend")
        self.cid("postgres")
        expected = [v for v in self.manifest["migrations"] if int(v.split("_")[0]) <= 596]
        require(self.versions() == expected, "production schema differs from the reviewed 596 baseline")
        h = self.health()
        require(len(h.get("commit", "")) >= 9 and self.manifest["previous_revision"].startswith(h["commit"]), "production backend drift")
        require(self.protected()["daemon_pid"] not in ("", "0"), "cannot identify protected daemon")
        self.run("docker", "buildx", "inspect", "stand-build")
        require(shutil.disk_usage("/var/lib/docker").free > 10 * 1024**3, "less than 10 GiB free for images")
        db_size = int(self.psql("SELECT pg_database_size(current_database())"))
        require(shutil.disk_usage("/var/backups").free > max(2 * 1024**3, db_size * 4), "insufficient backup/rehearsal space")
        return h

    def save(self):
        self.data["updated_at"] = utc()
        temp = self.state / "state.json.new"
        temp.write_text(json.dumps(self.data, indent=2) + "\n")
        temp.replace(self.state / "state.json")

    def new_state(self):
        return Path(tempfile.mkdtemp(prefix="multica-stt420.", dir="/var/backups"))

    def override(self, name, images):
        path = self.state / name
        path.write_text(json.dumps({"services": {s: {"image": image} for s, image in images.items()}}))
        return path

    def build(self):
        src = self.state / "source"
        src.mkdir()
        with tarfile.open(self.package / "source.tar.gz") as tar:
            tar.extractall(src, filter="data")
        # The web build needed this heap limit in STT-424; retain the source Dockerfile.
        webfile = src / "Dockerfile.stt420.web"
        webfile.write_text((src / "Dockerfile.web").read_text().replace(
            "RUN pnpm --filter @multica/web build", "ENV NODE_OPTIONS=--max-old-space-size=6144\nRUN pnpm --filter @multica/web build"))
        images = {}
        for service, dockerfile in (("backend", "Dockerfile"), ("frontend", webfile.name)):
            tag = "multica-" + service + ":stt420-" + self.manifest["revision"][:12]
            with (self.state / (service + "-build.log")).open("wb") as log:
                self.run("docker", "buildx", "build", "--builder", "stand-build", "--platform", "linux/amd64", "--load",
                         "--label", "org.opencontainers.image.revision=" + self.manifest["revision"],
                         "--build-arg", "COMMIT=" + self.manifest["revision"],
                         "--build-arg", "NEXT_PUBLIC_APP_VERSION=stt420-" + self.manifest["revision"][:12],
                         "-t", tag, "-f", str(src / dockerfile), str(src), output=log)
            images[service] = json.loads(self.run("docker", "image", "inspect", tag))[0]["Id"]
        self.data["new_images"] = images
        self.override("new-images.json", images)
        limited = self.state / "down/migrations"
        limited.mkdir(parents=True)
        for f in (src / "server/migrations").glob("*.sql"):
            if 597 <= int(f.name.split("_")[0]) <= 612:
                shutil.copy2(f, limited / f.name)
        self.save()

    def backup(self):
        self.data["snapshot_time"] = utc()
        path = self.state / "database.dump"
        with path.open("wb") as f:
            self.compose("exec", "-T", "postgres", "sh", "-c",
                         'exec pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" --format=custom --serializable-deferrable', output=f)
        self.data["backup"] = {"path": str(path), "sha256": sha(path), "time": self.data["snapshot_time"],
                               "versions": self.versions(), "server_version": self.psql("SHOW server_version"),
                               "extensions": self.psql("SELECT extname || ':' || extversion FROM pg_extension ORDER BY extname"),
                               "source_revision": self.manifest["previous_revision"]}
        with path.open("rb") as f:
            subprocess.run(self.pg_exec("pg_restore", "--list"), stdin=f, stdout=subprocess.DEVNULL, check=True)
        self.save()

    def pg_exec(self, *args):
        prefix = ["docker", "compose", "--project-directory", self.workdir, "-p", self.project]
        for f in self.compose_files:
            prefix += ["-f", f]
        return prefix + ["exec", "-T", "postgres", *args]

    def rehearse(self):
        name = "stt420-copy-" + self.state.name.rsplit(".", 1)[-1].lower()
        self.data["rehearsal_container"] = name
        self.save()
        image = self.inspect(self.cid("postgres"))["Image"]
        pgdata = self.state / "rehearsal-data"
        pgdata.mkdir()
        self.run("docker", "run", "-d", "--name", name, "--network", "none",
                 "--mount", f"type=bind,src={pgdata},dst=/var/lib/postgresql/data",
                 "-e", "POSTGRES_USER=multica", "-e", "POSTGRES_DB=copy",
                 "-e", "POSTGRES_HOST_AUTH_METHOD=trust", image)
        try:
            for i in range(60):
                try:
                    self.run("docker", "exec", name, "pg_isready", "-U", "multica", "-d", "copy")
                    break
                except RuntimeError:
                    require(i < 59, "isolated PostgreSQL readiness timeout")
                    time.sleep(2)
            with (self.state / "database.dump").open("rb") as f:
                subprocess.run(["docker", "exec", "-i", name, "pg_restore", "-U", "multica", "-d", "copy", "--no-owner", "--no-acl", "--exit-on-error"], stdin=f, stdout=subprocess.DEVNULL, check=True)
            self.run("docker", "exec", name, "mkdir", "-p", "/tmp/stt420/up/migrations", "/tmp/stt420/down")
            self.run("docker", "cp", str(self.package / "bin") + "/.", name + ":/tmp/stt420/")
            self.run("docker", "cp", str(self.state / "source/server/migrations") + "/.", name + ":/tmp/stt420/up/migrations/")
            self.run("docker", "cp", str(self.state / "down/migrations"), name + ":/tmp/stt420/down/")

            def tool(binary, *args, direction="up"):
                return self.run("docker", "exec", "-w", "/tmp/stt420/" + direction,
                                "-e", "DATABASE_URL=postgres://multica@127.0.0.1/copy?sslmode=disable",
                                name, "/tmp/stt420/" + binary, *args)

            def snapshot(binary, filename):
                tool(binary, "snapshot", "--workspace", "STT", "--now", self.data["snapshot_time"], "--out", "/tmp/stt420/" + filename)
                self.run("docker", "cp", name + ":/tmp/stt420/" + filename, str(self.state / filename))

            def schema():
                text = self.run("docker", "exec", name, "pg_dump", "-U", "multica", "-d", "copy", "--schema-only", "--no-owner", "--no-acl")
                return "\n".join(line for line in text.splitlines() if not line.startswith(("\\restrict ", "\\unrestrict ")))

            def diff(binary, after, key, *accept):
                # The report is anonymized; keep it in state whatever the verdict.
                args = ["docker", "exec", "-w", "/tmp/stt420/up", name, "/tmp/stt420/" + binary, "diff", *accept,
                        "--before", "/tmp/stt420/before.json", "--after", "/tmp/stt420/" + after]
                result = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=3600)
                self.data.setdefault("rehearsal", {})[key] = result.stdout.decode()
                self.save()
                require(result.returncode == 0, key + " has unaccepted PreviewChoice differences; see state.json")

            schema_before = schema()
            snapshot("choice-before", "before.json")
            tool("migrate", "up")
            tool("migrate", "up")
            snapshot("choice-after", "after.json")
            # The only accepted change: a window at 100 % without the provider's
            # limit flag is spent (spec 3.4). Every other difference stops here.
            diff("choice-after", "after.json", "preview_diff", "--accept", "provider-full-window")
            tool("migrate", "down", direction="down")
            snapshot("choice-before", "rollback.json")
            diff("choice-before", "rollback.json", "rollback_preview_diff")
            require(schema() == schema_before, "copy schema differs after down 612..597")
            self.data["rehearsal"].update(time=utc(), schema_roundtrip=True)
            self.save()
        finally:
            self.run("docker", "rm", "-f", name)

    def migrate(self, direction):
        args = ["run", "--rm", "--no-deps"]
        if direction == "down":
            args += ["-v", str(self.state / "down") + ":/release:ro", "-w", "/release"]
        self.compose(*args, "--entrypoint", "/app/migrate", "backend", direction, override=self.state / "new-images.json")

    def install(self):
        h = self.check()
        self.state = self.new_state()
        shutil.copytree(self.package, self.state / "package")
        self.package = self.state / "package"
        self.data = {"revision": self.manifest["revision"], "started_at": utc(), "health_before": h,
                     "compose_files": self.compose_files, "project": self.project, "workdir": self.workdir,
                     "api": self.api, "web": self.web, "protected": self.protected(), "phase": "building",
                     "old_containers": {s: self.cid(s) for s in ("backend", "frontend", "postgres")},
                     "old_images": {s: self.inspect(self.cid(s))["Image"] for s in ("backend", "frontend")},
                     "config_checksums": {f: sha(f) for f in self.compose_files}}
        for service, image in self.data["old_images"].items():
            self.run("docker", "tag", image, f"multica-{service}:before-{self.state.name}")
        for i, f in enumerate(self.compose_files):
            shutil.copy2(f, self.state / f"compose-before-{i}.yml")
        if (Path(self.workdir) / ".env").is_file():
            shutil.copy2(Path(self.workdir) / ".env", self.state / "compose.env.private")
        self.override("old-images.json", self.data["old_images"])
        self.save()
        print("STATE=" + str(self.state), flush=True)
        self.build()
        # No production writer is stopped until both images have built.
        self.check()
        self.data["phase"] = "stopping"
        self.save()
        try:
            self.compose("stop", "frontend", "backend")
            self.data["phase"] = "stopped"
            self.save()
            self.backup()
            self.rehearse()
            require(self.protected() == self.data["protected"], "PostgreSQL or daemon changed during rehearsal")
            self.data["phase"] = "migrating"
            self.save()
            self.migrate("up")
            require(self.versions() == self.manifest["migrations"], "target migrations are incomplete")
            self.compose("up", "-d", "--no-deps", "--no-build", "--pull", "never", "backend", "frontend", override=self.state / "new-images.json")
            self.data["health_after"] = self.wait(self.manifest["revision"])
            require(self.protected() == self.data["protected"], "protected service changed")
            for service, image in self.data["new_images"].items():
                require(self.inspect(self.cid(service))["Image"] == image, "wrong deployed image: " + service)
            self.data["phase"] = "installed"
            self.data["deployed_at"] = utc()
            self.save()
        except BaseException:
            self.rollback(automatic=True)
            raise
        print("INSTALLED=" + self.manifest["revision"])
        print("ROLLBACK=python3 " + str(self.package / "deploy.py") + " rollback --state " + str(self.state))

    def load(self, state):
        self.state = Path(state).resolve()
        require(self.state.stat().st_uid == 0 and self.state.stat().st_mode & 0o077 == 0, "state must be root-owned 0700")
        self.data = json.loads((self.state / "state.json").read_text())
        self.package = self.state / "package"
        self.manifest = json.loads((self.package / "manifest.json").read_text())
        self.verify_bundle()
        self.compose_files = self.data["compose_files"]
        self.project, self.workdir = self.data["project"], self.data["workdir"]
        self.api, self.web = self.data["api"], self.data["web"]

    def rollback(self, automatic=False):
        require(self.data.get("phase") != "rolled_back", "already rolled back")
        for f, checksum in self.data["config_checksums"].items():
            require(sha(f) == checksum, "production Compose config drift; inspect before rollback")
        if not automatic:
            require(self.data["phase"] == "installed", "manual rollback requires installed state")
            for service, image in self.data["new_images"].items():
                require(self.inspect(self.cid(service))["Image"] == image, "a newer release is installed")
            require(self.versions() == self.manifest["migrations"], "schema drift; rollback stopped")
        self.compose("stop", "frontend", "backend", override=self.state / "new-images.json")
        if self.data["phase"] in ("migrating", "installed"):
            # Down projects current personal profiles/pools/caps into the old schema.
            # Never restore the pre-release dump over newer live writes automatically.
            self.migrate("down")
        require(self.versions() == self.data.get("backup", {}).get("versions", self.versions()), "rollback schema mismatch")
        self.compose("up", "-d", "--no-deps", "--no-build", "--pull", "never", "backend", "frontend", override=self.state / "old-images.json")
        self.wait(self.manifest["previous_revision"])
        require(self.protected() == self.data["protected"], "protected service drift after rollback")
        self.data["phase"] = "rolled_back"
        self.save()


def require_acceptance(path, revision):
    require(path is not None, "install requires --acceptance after successful QA and final review")
    evidence = json.loads(Path(path).read_text())
    require(evidence.get("revision") == revision, "acceptance must pin the bundle SHA")
    for step in ("qa", "review"):
        result = evidence.get(step, {})
        require(result.get("passed") is True and result.get("report_url", "").startswith("https://"),
                "successful " + step + " report is required")


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("action", choices=("check", "install", "status", "rollback"))
    p.add_argument("--state", type=Path)
    p.add_argument("--compose", default="/opt/multica/docker-compose.selfhost.yml")
    p.add_argument("--api", default="http://127.0.0.1:8080")
    p.add_argument("--web", default="http://127.0.0.1:3000")
    p.add_argument("--acceptance", type=Path)
    args = p.parse_args()
    require(os.geteuid() == 0, "run as root; this program does not elevate privileges")
    os.umask(0o077)
    with open("/run/lock/multica-stt420.lock", "w") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        release = Release(Path(__file__).resolve().parent, args.compose, args.api, args.web)
        if args.action in ("rollback", "status"):
            require(args.state, "--state is required")
            release.load(args.state)
        if args.action == "check":
            print(json.dumps(release.check()))
        elif args.action == "install":
            require_acceptance(args.acceptance, release.manifest["revision"])
            release.install()
        elif args.action == "rollback":
            release.rollback()
        else:
            print(json.dumps({"phase": release.data["phase"], "revision": release.data["revision"], "health": release.health(), "protected": release.protected()}))


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        print("STT420: " + str(exc), file=sys.stderr)
        sys.exit(1)
