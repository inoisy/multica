#!/usr/bin/env python3
"""Build an immutable operator bundle without production access."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile

BASE = "aebbec7876b9f187bd3f5d78ef357e6770c33b63"
COMPARE = "40a8b5ff6c4ba5cec81d29650ceff63e0cb67763"


def sha(path):
    with open(path, "rb") as f:
        return hashlib.file_digest(f, "sha256").hexdigest()


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--revision", required=True)
    p.add_argument("--out", required=True, type=Path)
    args = p.parse_args()
    repo = Path(__file__).resolve().parents[2]
    git = lambda *a: subprocess.check_output(["git", "-C", str(repo), *a], text=True).strip()
    revision = git("rev-parse", args.revision + "^{commit}")
    subprocess.run(["git", "-C", str(repo), "merge-base", "--is-ancestor", BASE, revision], check=True)
    out = args.out.resolve()
    out.mkdir(mode=0o700)  # Refuse to overwrite any existing bundle.
    subprocess.run(["git", "-C", str(repo), "archive", "--format=tar.gz", "-o", str(out / "source.tar.gz"), revision], check=True)
    for name in ("deploy.py", "install.sh", "install-cli.py", "snapshot.py", "README.md"):
        (out / name).write_text(git("show", f"{revision}:deploy/stt420/{name}") + "\n")
    (out / "bin").mkdir()
    env = {**os.environ, "CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": "amd64"}
    with tempfile.TemporaryDirectory(prefix="stt420-build-") as tmp:
        src = Path(tmp) / "src"
        src.mkdir()
        with tarfile.open(out / "source.tar.gz") as tar:
            tar.extractall(src, filter="data")
        for cmd, name in (("server", "server"), ("migrate", "migrate"), ("multica", "multica"), ("model-choice-compare", "choice-after")):
            subprocess.run(["go", "build", "-trimpath", "-ldflags", f"-s -w -X main.commit={revision} -X main.buildCommit={revision} -X main.version=stt420-{revision[:12]}", "-o", str(out / "bin" / name), "./cmd/" + cmd], cwd=src / "server", env=env, check=True)
        migrations = sorted(f.stem.removesuffix(".up") for f in (src / "server/migrations").glob("*.up.sql"))
        old = Path(tmp) / "before"
        old.mkdir()
        archive = Path(tmp) / "before.tar.gz"
        subprocess.run(["git", "-C", str(repo), "archive", "--format=tar.gz", "-o", str(archive), COMPARE, "server"], check=True)
        with tarfile.open(archive) as tar:
            tar.extractall(old, filter="data")
        subprocess.run(["go", "build", "-trimpath", "-ldflags", "-s -w", "-o", str(out / "bin/choice-before"), "./cmd/model-choice-compare"], cwd=old / "server", env=env, check=True)
    manifest = {
        "revision": revision, "previous_revision": BASE, "baseline_exporter": COMPARE,
        "repository": "https://github.com/inoisy/multica.git", "architecture": "linux/amd64",
        "source_schema": 596, "target_schema": 612, "migrations": migrations,
        "go": subprocess.check_output(["go", "version"], text=True).strip(),
        "checksums": {str(f.relative_to(out)): sha(f) for f in sorted(out.rglob("*")) if f.is_file()},
    }
    (out / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    (out / "SHA256SUMS").write_text("".join(f"{sha(f)}  {f.relative_to(out)}\n" for f in sorted(out.rglob("*")) if f.is_file()))
    print(json.dumps({"bundle": str(out), "revision": revision, "source_sha256": sha(out / "source.tar.gz")}))


if __name__ == "__main__":
    main()
