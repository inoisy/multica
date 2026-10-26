"""Pinned Codebase Memory backend; no client installation or config changes."""
import hashlib
import io
import json
import os
from pathlib import Path
import platform
import sqlite3
import subprocess
import tarfile
import urllib.request

VERSION = "0.12.1"
ARCHIVE_SHA256 = "8453015145f072c1b9b019cbdd02cea8cc6f18cc5e116036dd20992631c638c7"
ARCHIVE = "codebase-memory-mcp-linux-amd64.tar.gz"
RELEASE = f"https://github.com/DeusData/codebase-memory-mcp/releases/download/v{VERSION}"


def backend(root, ref):
    cache = Path(os.environ.get("PRODUCT_CACHE_DIR", root / "tools/product/.cache")).resolve()
    cache.mkdir(parents=True, exist_ok=True, mode=0o700)
    binary = Path(os.environ.get("PRODUCT_CBM_BIN", cache / "codebase-memory-mcp"))
    if not binary.exists():
        if platform.system() != "Linux" or platform.machine() not in ("x86_64", "amd64"):
            raise ValueError("Set PRODUCT_CBM_BIN to a verified Codebase Memory 0.12.1 executable for this platform.")
        with urllib.request.urlopen(RELEASE + "/" + ARCHIVE, timeout=60) as response:
            archive = response.read()
        if hashlib.sha256(archive).hexdigest() != ARCHIVE_SHA256:
            raise ValueError("Codebase Memory release checksum mismatch")
        with tarfile.open(fileobj=io.BytesIO(archive)) as bundle:
            member = next(m for m in bundle.getmembers() if m.isfile() and Path(m.name).name == "codebase-memory-mcp")
            binary.write_bytes(bundle.extractfile(member).read())
            binary.chmod(0o700)
    version = subprocess.check_output([str(binary), "--version"], text=True).strip()
    if version != "codebase-memory-mcp " + VERSION:
        raise ValueError("Expected pinned Codebase Memory " + VERSION + ", got " + version)
    # Immutable git input prevents a concurrent editor invalidating a full index.
    sha = subprocess.check_output(["git", "-C", str(root), "rev-parse", ref], text=True).strip()
    snapshot = cache / ("source-" + sha)
    if not snapshot.exists():
        snapshot.mkdir(mode=0o700)
        archive = subprocess.check_output(["git", "-C", str(root), "archive", ref])
        with tarfile.open(fileobj=io.BytesIO(archive)) as bundle:
            for member in bundle.getmembers():
                target = snapshot / member.name
                if member.isfile() and target.resolve().is_relative_to(snapshot):
                    target.parent.mkdir(parents=True, exist_ok=True)
                    target.write_bytes(bundle.extractfile(member).read())
    env = {**os.environ, "CBM_CACHE_DIR": str(cache / "graph"), "CBM_WORKERS": "4", "CBM_MEM_BUDGET_MB": "2048"}
    # Runtime sockets keep the backend default short path; deeply nested
    # workspace paths exceed the Unix socket filename limit.
    result = subprocess.run([str(binary), "cli", "--quiet", "index_repository", "--repo-path", str(snapshot),
                             "--name", "product-index", "--mode", "full", "--persistence", "false", "--doc-candidates", "false"],
                            env=env, text=True, capture_output=True, timeout=110)
    if result.returncode:
        raise ValueError("Codebase Memory failed: " + result.stdout[-1500:] + result.stderr[-1500:])
    report = json.loads(result.stdout)
    if report.get("status") != "indexed":
        raise ValueError("Codebase Memory did not publish a complete index: " + str(report.get("status")))
    # This exporter is version-pinned and opens only the local tool's database,
    # read-only. It never accesses the Multica application database.
    with sqlite3.connect((cache / "graph/product-index.db").as_uri() + "?mode=ro", uri=True) as conn:
        rows = conn.execute("SELECT id,label,name,qualified_name,file_path,start_line,end_line FROM nodes ORDER BY file_path,start_line,qualified_name").fetchall()
        nodes = {r[0]: {"label": r[1], "name": r[2], "qualified_name": r[3].removeprefix("product-index."),
                       "file": r[4], "line": r[5], "end_line": r[6]} for r in rows}
        for source, target in conn.execute("SELECT source_id,target_id FROM edges WHERE type IN ('CALLS','CALL_REFERENCE') ORDER BY source_id,target_id"):
            if source in nodes and target in nodes:
                nodes[source].setdefault("calls", set()).add(nodes[target]["name"])
    return list(nodes.values()), {"name": "codebase-memory-mcp", "version": VERSION,
        "archive_sha256": ARCHIVE_SHA256, "nodes": report["nodes"], "edges": report["edges"],
        "parse_partial_count": report.get("parse_partial_count", 0),
        "parse_unusable_count": report.get("parse_unusable_count", 0),
        "not_indexed_files_count": report.get("not_indexed_files_count", 0)}
