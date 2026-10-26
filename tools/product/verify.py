#!/usr/bin/env python3
"""Print reproducible evidence for STT-436's three acceptance questions."""
import json
from pathlib import Path
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[2]
start = time.monotonic()
commands = [
    ("1. Agent fields: writers, API read, page hook", [
        ["writers", "agent", "runtime_id"],
        ["writers", "agent", "model"],
        ["find", "SetAgentModelProfile", "--kind", "query"],
        ["trace", "getAgent", "--limit", "16"],
        ["find", "agentDetailOptions", "--kind", "core_symbol"],
        ["find", "AgentDetailInspector", "--kind", "symbol", "--limit", "3"],
    ]),
    ("2. Pool API surfaces and SQL evidence", [
        ["screens", "/api/pools"],
        ["trace", "listPools", "--limit", "30"],
    ]),
    ("3. Planned migration 615 and remaining legacy references", [
        ["find", "615", "--kind", "migration"],
        ["uses", "agent_model_option"],
        ["uses", "subscription_limit_backup"],
    ]),
]
for title, queries in commands:
    print("\n" + title, flush=True)
    for query in queries:
        print("$ python3 tools/product/index.py " + " ".join(query), flush=True)
        subprocess.run([sys.executable, str(ROOT / "tools/product/index.py"), *query], cwd=ROOT, check=True)
index = json.loads((ROOT / "docs/stt/product/generated/index.json").read_text())
print(f"\nsource_sha={index['source_sha']} query_seconds={time.monotonic() - start:.3f}")
print("Migration 615 is absent in this code snapshot. Its planned scope and authorization conditions live in STT-433, outside the code index.")
