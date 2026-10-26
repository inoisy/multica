#!/usr/bin/env python3
"""Pull-based deploy of the fork's main branch onto the self-hosted server.

One run: find the newest commit on main whose CI and STT workflows succeeded,
and if it is ahead of what the server runs, back up the database, switch the
backend/web images to that commit, wait for /health to report it, and roll back
to the previous images if it does not. The daemon binary is swapped only while
the daemon is idle. Destructive migrations stop the run until an operator
passes --allow-destructive.

Runs as root from systemd (timer, webhook trigger). Stdlib only.
"""
import argparse
import datetime as dt
import fcntl
import json
import os
import re
import shutil
import subprocess
import sys
import time
import urllib.request
from pathlib import Path

CONFIG_PATH = Path(os.environ.get("AUTODEPLOY_CONFIG", "/etc/multica-autodeploy/config.json"))
DEFAULTS = {
    "repo": "inoisy/multica",
    "branch": "main",
    "required_workflows": ["CI", "STT"],
    "image_prefix": "ghcr.io/inoisy/multica-",
    "health_url": "https://multica.yakutov.com/health",
    "health_timeout_s": 240,
    "compose_workdir": "/opt/multica",
    "compose_project": "",
    "backend_service": "backend",
    "frontend_service": "frontend",
    "postgres_service": "postgres",
    "override_name": "docker-compose.autodeploy.yml",
    "backup_dir": "/var/backups/multica-autodeploy",
    "backups_keep": 5,
    "state_path": "/var/lib/multica-autodeploy/state.json",
    "log_path": "/var/log/multica-autodeploy.jsonl",
    "daemon_unit": "multica-agents",
    "daemon_user": "agents",
    "daemon_binary": "/usr/local/bin/multica",
    "daemon_idle_wait_s": 600,
    "notify_issue": "",
    "dry_run": True,
}

# Statements that cannot be undone by running the previous app version.
DESTRUCTIVE = re.compile(
    r"\b(DROP\s+(TABLE|COLUMN|SCHEMA|TYPE|VIEW|FUNCTION)|"
    r"ALTER\s+TABLE\b[^;]*?\bDROP\s+(COLUMN|CONSTRAINT)\b|"
    r"RENAME\s+(COLUMN|TO)\b|"
    r"ALTER\s+COLUMN\s+\S+\s+(SET\s+DATA\s+)?TYPE\b|"
    r"TRUNCATE\b|DELETE\s+FROM\b)",
    re.IGNORECASE,
)


def load_config():
    cfg = dict(DEFAULTS)
    if CONFIG_PATH.exists():
        cfg.update(json.loads(CONFIG_PATH.read_text()))
    return cfg


def strip_sql_comments(sql):
    sql = re.sub(r"/\*.*?\*/", "", sql, flags=re.S)
    return re.sub(r"--[^\n]*", "", sql)


def destructive_statements(sql):
    return [m.group(0) for m in DESTRUCTIVE.finditer(strip_sql_comments(sql))]


def classify_migrations(files):
    """files: list of {"filename", "status", "content"} from the compare.

    Returns (problems, added). Only newly added *.up.sql files are allowed;
    edits to existing migrations and destructive statements are problems.
    """
    problems, added = [], []
    for f in files:
        name = f["filename"]
        if not name.startswith("server/migrations/"):
            continue
        if f["status"] != "added":
            problems.append(f"{name}: existing migration {f['status']}")
            continue
        if not name.endswith(".up.sql"):
            continue
        added.append(name)
        hits = destructive_statements(f.get("content", ""))
        if hits:
            kinds = sorted({" ".join(h.split()[:2]).upper() for h in hits})
            problems.append(f"{name}: {', '.join(kinds)}")
    return problems, added


def pick_target(commits, runs_by_sha, required):
    """Newest commit (commits are newest first) whose required workflows all succeeded."""
    for sha in commits:
        runs = runs_by_sha.get(sha, {})
        if all(runs.get(name) == "success" for name in required):
            return sha
    return None


def render_override(cfg, sha_or_images):
    if isinstance(sha_or_images, str):
        images = {
            cfg["backend_service"]: f"{cfg['image_prefix']}backend:{sha_or_images}",
            cfg["frontend_service"]: f"{cfg['image_prefix']}web:{sha_or_images}",
        }
    else:
        images = sha_or_images
    lines = ["# Managed by multica-autodeploy. Do not edit: the next run rewrites it.", "services:"]
    for service, image in images.items():
        lines += [f"  {service}:", f"    image: {image}"]
    return "\n".join(lines) + "\n"


class Deployer:
    def __init__(self, cfg, dry_run, allow_destructive=None):
        self.cfg = cfg
        self.dry_run = dry_run
        self.allow_destructive = allow_destructive
        self.events = []

    # ---- io helpers -------------------------------------------------------
    def log(self, event, **fields):
        rec = {"time": dt.datetime.now(dt.timezone.utc).isoformat(timespec="seconds"), "event": event, "dry_run": self.dry_run, **fields}
        self.events.append(rec)
        print(json.dumps(rec, ensure_ascii=False), flush=True)
        try:
            Path(self.cfg["log_path"]).parent.mkdir(parents=True, exist_ok=True)
            with open(self.cfg["log_path"], "a") as fh:
                fh.write(json.dumps(rec, ensure_ascii=False) + "\n")
        except OSError:
            pass

    def http_json(self, url):
        req = urllib.request.Request(url, headers={"Accept": "application/vnd.github+json", "User-Agent": "multica-autodeploy"})
        token = os.environ.get("GITHUB_TOKEN")
        if token:
            req.add_header("Authorization", f"Bearer {token}")
        with urllib.request.urlopen(req, timeout=20) as r:
            return json.loads(r.read().decode())

    def gh(self, path):
        return self.http_json(f"https://api.github.com/repos/{self.cfg['repo']}{path}")

    def run(self, *cmd, check=True, capture=True, mutate=True):
        if mutate and self.dry_run:
            self.log("dry_run_skip", cmd=" ".join(cmd))
            return ""
        res = subprocess.run(cmd, check=check, text=True, capture_output=capture)
        return res.stdout if capture else ""

    def health(self):
        try:
            with urllib.request.urlopen(self.cfg["health_url"], timeout=5) as r:
                return json.loads(r.read().decode())
        except Exception as exc:  # noqa: BLE001 - any failure means "not healthy"
            return {"status": "error", "error": str(exc)}

    # ---- compose ----------------------------------------------------------
    def compose_context(self):
        """Project, working dir and config files of the running backend container."""
        filters = ["--filter", f"label=com.docker.compose.service={self.cfg['backend_service']}",
                   "--filter", f"label=com.docker.compose.project.working_dir={self.cfg['compose_workdir']}"]
        if self.cfg.get("compose_project"):
            filters += ["--filter", f"label=com.docker.compose.project={self.cfg['compose_project']}"]
        cid = subprocess.run(["docker", "ps", "-q", *filters], check=True, text=True, capture_output=True).stdout.split()
        if len(cid) != 1:
            seen = subprocess.run(
                ["docker", "ps", "--filter", f"label=com.docker.compose.service={self.cfg['backend_service']}", "--format",
                 '{{.Names}} project={{.Label "com.docker.compose.project"}} dir={{.Label "com.docker.compose.project.working_dir"}} image={{.Image}}'],
                text=True, capture_output=True).stdout.strip()
            raise SystemExit(f"expected one running backend container in {self.cfg['compose_workdir']}, found {len(cid)}. "
                             f"Set compose_project in {CONFIG_PATH}. Running backends:\n{seen}")
        labels = json.loads(subprocess.run(["docker", "inspect", cid[0]], check=True, text=True, capture_output=True).stdout)[0]
        lab = labels["Config"]["Labels"]
        files = lab["com.docker.compose.project.config_files"].split(",")
        workdir = lab["com.docker.compose.project.working_dir"]
        override = str(Path(workdir) / self.cfg["override_name"])
        if override not in files:
            files.append(override)
        return {"project": lab["com.docker.compose.project"], "workdir": workdir, "files": files, "override": override,
                "backend_image": labels["Config"]["Image"], "backend_container": cid[0]}

    def compose(self, ctx, *args, mutate=True):
        cmd = ["docker", "compose", "-p", ctx["project"], "--project-directory", ctx["workdir"]]
        for f in ctx["files"]:
            if Path(f).exists() or f == ctx["override"]:
                cmd += ["-f", f]
        return self.run(*cmd, *args, mutate=mutate)

    def current_images(self, ctx):
        images = {}
        for service in (self.cfg["backend_service"], self.cfg["frontend_service"]):
            cid = subprocess.run(["docker", "ps", "-q", "--filter", f"label=com.docker.compose.service={service}",
                                  "--filter", f"label=com.docker.compose.project={ctx['project']}"],
                                 check=True, text=True, capture_output=True).stdout.split()
            if cid:
                images[service] = json.loads(subprocess.run(["docker", "inspect", cid[0]], check=True, text=True,
                                                            capture_output=True).stdout)[0]["Config"]["Image"]
        return images

    def write_override(self, ctx, content):
        if self.dry_run:
            self.log("dry_run_override", path=ctx["override"], content=content)
            return
        tmp = ctx["override"] + ".tmp"
        Path(tmp).write_text(content)
        os.replace(tmp, ctx["override"])

    # ---- steps ------------------------------------------------------------
    def target_commit(self):
        commits = [c["sha"] for c in self.gh(f"/commits?sha={self.cfg['branch']}&per_page=20")]
        runs_by_sha = {}
        for run in self.gh(f"/actions/runs?branch={self.cfg['branch']}&event=push&per_page=50")["workflow_runs"]:
            per = runs_by_sha.setdefault(run["head_sha"], {})
            if run["status"] == "completed":
                per.setdefault(run["name"], run["conclusion"])
        return pick_target(commits, runs_by_sha, self.cfg["required_workflows"])

    def migration_check(self, current, target):
        cmp = self.gh(f"/compare/{current}...{target}")
        if cmp["status"] not in ("ahead", "identical"):
            return [f"target is {cmp['status']} relative to the running commit"], []
        if len(cmp.get("files", [])) >= 300:
            # The compare API truncates at 300 files; an unseen migration must not slip through.
            return ["change too large to verify migrations (300+ files)"], []
        files = []
        for f in cmp.get("files", []):
            if f["filename"].startswith("server/migrations/"):
                entry = {"filename": f["filename"], "status": f["status"]}
                if f["status"] == "added" and f["filename"].endswith(".up.sql"):
                    with urllib.request.urlopen(f"https://raw.githubusercontent.com/{self.cfg['repo']}/{target}/{f['filename']}", timeout=20) as r:
                        entry["content"] = r.read().decode()
                files.append(entry)
        return classify_migrations(files)

    def backup(self, ctx, label):
        out_dir = Path(self.cfg["backup_dir"])
        stamp = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
        path = out_dir / f"{stamp}-{label[:12]}.dump"
        if self.dry_run:
            self.log("dry_run_backup", path=str(path))
            return str(path)
        out_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
        cmd = ["docker", "compose", "-p", ctx["project"], "--project-directory", ctx["workdir"]]
        for f in ctx["files"]:
            if Path(f).exists():
                cmd += ["-f", f]
        cmd += ["exec", "-T", self.cfg["postgres_service"], "sh", "-c", 'pg_dump -Fc -U "$POSTGRES_USER" "$POSTGRES_DB"']
        with open(path, "wb") as fh:
            subprocess.run(cmd, check=True, stdout=fh)
        os.chmod(path, 0o600)
        dumps = sorted(out_dir.glob("*.dump"))
        for old in dumps[: max(0, len(dumps) - self.cfg["backups_keep"])]:
            old.unlink()
        return str(path)

    def wait_health(self, sha):
        deadline = time.time() + self.cfg["health_timeout_s"]
        last = {}
        while time.time() < deadline:
            last = self.health()
            if last.get("status") == "ok" and last.get("commit") == sha:
                return True, last
            time.sleep(5)
        return False, last

    def daemon_status(self):
        out = subprocess.run(["runuser", "-u", self.cfg["daemon_user"], "--", self.cfg["daemon_binary"], "daemon", "status", "--output", "json"],
                             text=True, capture_output=True)
        try:
            return json.loads(out.stdout)
        except ValueError:
            return {}

    def update_daemon(self, sha):
        status = self.daemon_status()
        if sha[:12] in (status.get("cli_version", "") + status.get("cli_commit", "")):
            return "current"
        image = f"{self.cfg['image_prefix']}backend:{sha}"
        new = self.cfg["daemon_binary"] + ".new"
        if self.dry_run:
            self.log("dry_run_daemon", image=image, binary=self.cfg["daemon_binary"])
            return "dry_run"
        cid = subprocess.run(["docker", "create", image], check=True, text=True, capture_output=True).stdout.strip()
        try:
            subprocess.run(["docker", "cp", f"{cid}:/app/multica", new], check=True)
        finally:
            subprocess.run(["docker", "rm", cid], capture_output=True)
        os.chmod(new, 0o755)
        deadline = time.time() + self.cfg["daemon_idle_wait_s"]
        while time.time() < deadline:
            st = self.daemon_status()
            if st and st.get("running_task_count", 1) == 0 and st.get("active_task_count", 1) == 0:
                shutil.copy2(self.cfg["daemon_binary"], self.cfg["daemon_binary"] + ".prev")
                os.replace(new, self.cfg["daemon_binary"])
                subprocess.run(["systemctl", "restart", self.cfg["daemon_unit"]], check=True)
                return "updated"
            time.sleep(15)
        return "busy"  # binary stays staged in .new; the next run retries

    def notify(self, text):
        issue = self.cfg.get("notify_issue")
        if not issue:
            return
        body = Path("/tmp") / f"multica-autodeploy-{os.getpid()}.md"
        body.write_text(text)
        os.chmod(body, 0o644)
        try:
            subprocess.run(["runuser", "-u", self.cfg["daemon_user"], "--", self.cfg["daemon_binary"], "issue", "comment", "add", issue,
                            "--content-file", str(body)], capture_output=True, timeout=60)
        finally:
            body.unlink(missing_ok=True)

    # ---- main -------------------------------------------------------------
    def deploy(self):
        state_path = Path(self.cfg["state_path"])
        state = json.loads(state_path.read_text()) if state_path.exists() else {}
        health = self.health()
        current = health.get("commit")
        if health.get("status") != "ok" or not current:
            self.log("abort", reason="server unhealthy before deploy", health=health)
            return 2
        target = self.allow_destructive or self.target_commit()
        if not target:
            self.log("noop", reason="no commit with green CI and STT on main")
            return 0
        if target == current:
            if state.get("daemon") == "busy" and not self.dry_run:
                state["daemon"] = self.update_daemon(target)
                self.save_state(state)
            self.log("noop", reason="already running", commit=current)
            return 0
        if state.get("blocked") == target and self.allow_destructive != target:
            self.log("noop", reason="waiting for operator", commit=target)
            return 0
        problems, added = self.migration_check(current, target)
        if problems and self.allow_destructive != target:
            self.log("blocked", commit=target, problems=problems)
            state["blocked"] = target
            self.save_state(state)
            self.notify(f"Автовыкатка остановлена на `{target[:9]}`: {'; '.join(problems)}.\n\n"
                        f"Выкатить вручную после проверки: `sudo multica-autodeploy run --allow-destructive {target}`.")
            return 3
        ctx = self.compose_context()
        previous = self.current_images(ctx)
        self.log("start", current=current, target=target, migrations=added, previous=previous)
        dump = self.backup(ctx, current)
        self.write_override(ctx, render_override(self.cfg, target))
        try:
            self.compose(ctx, "pull", self.cfg["backend_service"], self.cfg["frontend_service"])
        except subprocess.CalledProcessError:
            self.write_override(ctx, render_override(self.cfg, previous))
            self.log("abort", reason="image pull failed", target=target)
            return 5
        self.compose(ctx, "up", "-d", "--no-deps", self.cfg["backend_service"], self.cfg["frontend_service"])
        if self.dry_run:
            self.log("dry_run_done", target=target)
            self.notify(f"Автовыкатка (пробный режим): выкатила бы `{target[:9]}` вместо `{current[:9]}`, миграций: {len(added)}.")
            return 0
        ok, last = self.wait_health(target)
        if not ok:
            self.log("rollback", target=target, health=last, previous=previous)
            self.write_override(ctx, render_override(self.cfg, previous))
            self.compose(ctx, "up", "-d", "--no-deps", self.cfg["backend_service"], self.cfg["frontend_service"])
            back, after = self.wait_health(current)
            self.notify(f"Автовыкатка `{target[:9]}` не прошла health, откат на `{current[:9]}`: "
                        f"{'успешно' if back else 'НЕ поднялся, нужен человек'}. Бэкап БД: `{dump}`.")
            return 4
        daemon = self.update_daemon(target)
        state.update({"deployed": target, "previous": current, "previous_images": previous, "backup": dump, "daemon": daemon})
        state.pop("blocked", None)
        self.save_state(state)
        self.log("done", commit=target, daemon=daemon)
        self.notify(f"Выкачено `{target[:9]}` (было `{current[:9]}`), миграций: {len(added)}, демон: {daemon}. "
                    f"Бэкап БД: `{dump}`. Откат: `sudo multica-autodeploy rollback`.")
        return 0

    def rollback(self):
        state = json.loads(Path(self.cfg["state_path"]).read_text())
        ctx = self.compose_context()
        self.write_override(ctx, render_override(self.cfg, state["previous_images"]))
        self.compose(ctx, "up", "-d", "--no-deps", self.cfg["backend_service"], self.cfg["frontend_service"])
        ok, last = self.wait_health(state["previous"])
        self.log("manual_rollback", ok=ok, health=last)
        return 0 if ok else 4

    def save_state(self, state):
        if self.dry_run:
            return
        path = Path(self.cfg["state_path"])
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(state, indent=1))


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    sub = p.add_subparsers(dest="cmd", required=True)
    r = sub.add_parser("run")
    r.add_argument("--dry-run", action="store_true", help="plan only, change nothing")
    r.add_argument("--allow-destructive", metavar="SHA", help="deploy this exact commit despite blocked migrations")
    sub.add_parser("rollback")
    args = p.parse_args(argv)
    cfg = load_config()
    dry = args.cmd == "run" and (args.dry_run or cfg["dry_run"])
    lock = open("/run/multica-autodeploy.lock", "w")
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        print("another run is in progress")
        return 0
    d = Deployer(cfg, dry_run=dry, allow_destructive=getattr(args, "allow_destructive", None))
    return d.deploy() if args.cmd == "run" else d.rollback()


if __name__ == "__main__":
    sys.exit(main())
