#!/usr/bin/env python3
"""Product inventory built on Codebase Memory and Python's standard library."""

import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess
import sys
import time
from cbm import backend

ROOT = Path(__file__).resolve().parents[2]
DEFAULT = ROOT / "docs/stt/product/generated/index.json"
SOURCE_ROOTS = ("server/migrations", "server/pkg/db/queries", "server/internal",
                "server/cmd", "packages/core", "packages/views", "apps/web/app",
                "apps/desktop/src/renderer/src")


def git(*args):
    return subprocess.check_output(["git", "-C", str(ROOT), *args], text=True).strip()


def location(path, text, offset=0):
    return {"file": str(path), "line": text.count("\n", 0, offset) + 1}


def mask(text, strings=True):
    # Preserve offsets and newlines while hiding comments and quoted braces.
    pattern = r'//[^\n]*|/\*[\s\S]*?\*/|"(?:\\.|[^"\\])*"|\x27(?:\\.|[^\x27\\])*\x27|`[^`]*`'
    def replace(m):
        if not strings and not m[0].startswith(("//", "/*")):
            return m[0]
        return re.sub(r"[^\n]", " ", m[0])
    return re.sub(pattern, replace, text)


def closing(text, start, left="{", right="}"):
    level = 0
    for i in range(start, len(text)):
        if text[i] == left:
            level += 1
        elif text[i] == right:
            level -= 1
            if not level:
                return i
    raise ValueError(f"Unbalanced {left} at {start}")


def source_files():
    return sorted(p for base in SOURCE_ROOTS for p in (ROOT / base).rglob("*")
                  if p.is_file() and p.suffix in (".go", ".sql", ".ts", ".tsx", ".json")
                  and not any(x in p.parts for x in ("node_modules", ".next", "generated"))
                  and not re.search(r"(?:_test\.go|\.(?:test|spec)\.tsx?)$", p.name))


def fingerprint(files):
    digest = hashlib.sha256()
    for p in files:
        digest.update(str(p.relative_to(ROOT)).encode() + b"\0" + p.read_bytes() + b"\0")
    return digest.hexdigest()


def routes(path, text):
    hidden = mask(text)
    clean = mask(text, strings=False)
    scopes, result = [], []
    # Route callbacks provide lexical scope, including nested Group/With calls.
    for m in re.finditer(r'\.Route\(\s*("[^"\n]*"|[\w.]+)\s*,\s*func\([^)]*\)\s*\{', clean):
        opening = clean.index("{", m.end() - 1)
        value = m[1][1:-1] if m[1].startswith('"') else "${" + m[1] + "}"
        scopes.append((opening, closing(hidden, opening), value))
    pattern = r'\.(Get|Post|Put|Patch|Delete|Head|Options|Handle|HandleFunc|Mount)\(\s*("[^"\n]*"|[\w.]+)\s*,\s*([^\n]+)'
    for m in re.finditer(pattern, clean):
        value = m[2][1:-1] if m[2].startswith('"') else "${" + m[2] + "}"
        prefix = [s[2] for s in sorted(scopes) if s[0] < m.start() < s[1]]
        route = "/" + "/".join(s.strip("/") for s in [*prefix, value] if s.strip("/"))
        handler = re.match(r"(?:\w+\.)?(\w+)", m[3])
        result.append({"kind": "route", "name": m[1].upper() + " " + route,
                       "method": m[1].upper(), "path": route,
                       "handler": handler[1] if handler else "unresolved",
                       "resolution": "dynamic" if "${" in route or m[1] == "Mount" else "literal",
                       **location(path, text, m.start())})
    return result


def go_functions(path, text, native):
    result = []
    for n in native:
        if n["file"] != path or n["label"] not in ("Function", "Method"):
            continue
        body = "\n".join(text.splitlines()[n["line"] - 1:n["end_line"]])
        # sqlc bridge also covers unresolved receivers in the native graph.
        calls = sorted(set(n.get("calls", [])) | set(re.findall(r"\.(\w+)\s*\(", mask(body))))
        result.append({"kind": "go_function", "name": n["name"], "calls": calls,
                       "file": path, "line": n["line"], "end_line": n["end_line"]})
    return result


def cli_commands(path, text):
    result = []
    hidden = mask(text)
    # Includes factory-local commands, not just package globals.
    for m in re.finditer(r"(\w+)\s*(?::=|=)\s*&cobra\.Command\s*\{", text):
        opening = m.end() - 1
        body = text[opening:closing(hidden, opening) + 1]
        use = re.search(r'\bUse:\s*"([^"]+)"', body)
        if use:
            run = re.search(r"\bRunE?:\s*(\w+)", body)
            result.append({"kind": "cli", "name": use[1], "variable": m[1],
                           "handler": run[1] if run else None,
                           **location(path, text, m.start())})
    # Registration sites are separate evidence; local variable names are not
    # globally unique, so never invent a full command path from those names.
    for m in re.finditer(r"\b(\w+)\.AddCommand\(([^\n]*)\)", text):
        result.append({"kind": "cli_registration", "name": m[1] + ".AddCommand",
                       "children": m[2].strip(), **location(path, text, m.start())})
    return result


def sql_inventory(sources):
    records, table_names = [], set()
    for path, text in sources.items():
        if path.startswith("server/migrations/") and path.endswith(".up.sql"):
            table_names.update(re.findall(r'\bCREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?"?(\w+)', text, re.I))
    table_pattern = re.compile(r"\b(" + "|".join(sorted(table_names, key=len, reverse=True)) + r")\b")
    for path, text in sources.items():
        if not path.endswith(".sql"):
            continue
        if path.startswith("server/migrations/"):
            actions = [{"operation": m[1].upper(), "object": m[2].lower(), "name": m[3]}
                       for m in re.finditer(r'\b(CREATE|ALTER|DROP)\s+(TABLE|INDEX)\s+(?:CONCURRENTLY\s+)?(?:IF\s+(?:NOT\s+)?EXISTS\s+)?"?(\w+)', text, re.I)]
            records.append({"kind": "migration", "name": Path(path).name,
                            "direction": "up" if ".up." in path else "down",
                            "tables": sorted(set(table_pattern.findall(text))), "actions": actions,
                            **location(path, text)})
        else:
            matches = list(re.finditer(r"^-- name:\s*(\w+)\s*:(\w+)", text, re.M))
            for i, m in enumerate(matches):
                body = text[m.end():matches[i + 1].start() if i + 1 < len(matches) else len(text)]
                body = re.sub(r"--[^\n]*", "", body)
                read_body = re.sub(r"\bDELETE\s+FROM\b", "DELETE_TARGET", body, flags=re.I)
                read = sorted(set(re.findall(r'\b(?:FROM|JOIN)\s+"?(\w+)', read_body, re.I)) & table_names)
                write = sorted(set(re.findall(r'\b(?:UPDATE|INTO|DELETE\s+FROM)\s+"?(\w+)', body, re.I)) & table_names)
                tables = sorted(set(read) | set(write))
                write_fields = {}
                for w in re.finditer(r"\bINSERT\s+INTO\s+(\w+)\s*\(([^)]+)\)", body, re.I):
                    write_fields.setdefault(w[1], set()).update(re.findall(r"\b\w+\b", w[2]))
                for w in re.finditer(r"\bUPDATE\s+(\w+)(?:\s+(?:AS\s+)?\w+)?\s+SET\s+([\s\S]*?)(?:\bWHERE\b|\bRETURNING\b|;|$)", body, re.I):
                    write_fields.setdefault(w[1], set()).update(re.findall(r"\b(\w+)\s*=", w[2]))
                records.append({"kind": "query", "name": m[1], "result": m[2],
                                "tables": tables, "reads": read, "writes": write,
                                "write_fields": {k: sorted(v) for k, v in write_fields.items()},
                                "fields": sorted(set(re.findall(r"\b[a-z][a-z_]*\b", body)) - {"sqlc"}),
                                **location(path, text, m.start())})
    for table in sorted(table_names):
        records.append({"kind": "table", "name": table, "history_only": True,
                        "migrations": [r["name"] for r in records if r["kind"] == "migration" and table in r["tables"]]})
    return records


def ts_inventory(sources, native):
    records, methods, symbols, views = [], {}, {}, {}
    def module_file(module, importer):
        if module.startswith("."):
            base = (Path(importer).parent / module).as_posix()
            import posixpath
            base = posixpath.normpath(base)
        elif module.startswith("@multica/"):
            base = module.replace("@multica/", "packages/", 1)
        else:
            return None
        for candidate in (base, base + ".ts", base + ".tsx", base + "/index.ts", base + "/index.tsx"):
            if candidate in sources:
                return candidate
        return None

    def export_file(path, name, seen=None):
        seen = set() if seen is None else seen
        if not path or (path, name) in seen:
            return None
        seen = seen | {(path, name)}
        text = sources[path]
        if re.search(r"\bexport\s+(?:default\s+)?(?:async\s+)?(?:function|const|class)\s+" + re.escape(name) + r"\b", text):
            return path
        for m in re.finditer(r'export\s+(\*|\{[^}]+\})\s+from\s+["\x27]([^"\x27]+)["\x27]', text):
            names = m[1]
            if names != "*" and not re.search(r"\b" + re.escape(name) + r"\b", names):
                continue
            found = export_file(module_file(m[2], path), name, seen)
            if found:
                return found
        return None
    client = sources["packages/core/api/client.ts"]
    starts = list(re.finditer(r"^  (?:private )?async (\w+)\(", client, re.M))
    for i, m in enumerate(starts):
        body = client[m.start():starts[i + 1].start() if i + 1 < len(starts) else len(client)]
        fetch = re.search(r'this\.fetch(?:<[^;\n]*?>)?\(\s*(["`])(/api/[^"`]+)\1', body)
        if fetch:
            method = re.search(r'method:\s*"(\w+)"', body[fetch.end():])
            url = re.sub(r"\$\{[^}]+\}", "{param}", fetch[2]).split("?")[0].rstrip("/")
            record = {"kind": "api_client", "name": m[1], "path": url,
                      "method": method[1] if method else "GET",
                      **location("packages/core/api/client.ts", client, m.start())}
            methods[m[1]] = record
            records.append(record)
    for path, text in sources.items():
        if not path.endswith((".ts", ".tsx")):
            continue
        defs = list(re.finditer(r"^(?:export\s+)?(?:default\s+)?(?:async\s+)?(?:function|const|class)\s+(\w+)", text, re.M))
        if path.startswith("packages/core/") and path != "packages/core/api/client.ts":
            for i, m in enumerate(defs):
                parsed = [n for n in native if n["file"] == path and n["name"] == m[1]
                          and n["line"] == text.count("\n", 0, m.start()) + 1]
                end = max((n["end_line"] for n in parsed), default=0)
                body = "\n".join(text.splitlines()[parsed[0]["line"] - 1:end]) if parsed else text[m.start():defs[i + 1].start() if i + 1 < len(defs) else len(text)]
                symbols.setdefault(m[1], []).append({"kind": "core_symbol", "name": m[1],
                    "api_methods": sorted(set(re.findall(r"\bapi\.(\w+)\s*\(", body)) & methods.keys()),
                    "calls": set(re.findall(r"\b(\w+)\s*\(", mask(body))),
                    **location(path, text, m.start())})
        if path.startswith(("packages/views/", "apps/web/app/", "apps/desktop/src/renderer/src/")):
            imports = []
            for m in re.finditer(r'import\s+(?!type\b)([^;]+?)\s+from\s+["\x27]([^"\x27]+)["\x27]', text):
                items = m[1].replace("{", "").replace("}", "").split(",")
                names = [item.strip().split(" as ")[0].strip() for item in items
                         if item.strip() and not item.strip().startswith(("type ", "*"))]
                imports.append({"module": m[2], "symbols": names,
                                "targets": {n: export_file(module_file(m[2], path), n) for n in names},
                                "line": text.count("\n", 0, m.start()) + 1})
            view = {"kind": "view", "name": path, "symbols": [m[1] for m in defs],
                    "imports": imports,
                    "api_methods": sorted(set(re.findall(r"\bapi\.(\w+)\s*\(", text)) & methods.keys()),
                    **location(path, text)}
            views[path] = view
    # Symbol-based propagation avoids treating an import of the API barrel as
    # a dependency on every endpoint. Ambiguous names remain candidates.
    for _ in range(12):
        changed = False
        for candidates in symbols.values():
            for symbol in candidates:
                inherited = {a for call in symbol["calls"] for target in symbols.get(call, []) for a in target["api_methods"]}
                combined = sorted(set(symbol["api_methods"]) | inherited)
                changed |= combined != symbol["api_methods"]
                symbol["api_methods"] = combined
        if not changed:
            break
    by_name = {}
    for v in views.values():
        for name in v["symbols"]:
            by_name.setdefault(name, []).append(v)
    for _ in range(12):
        changed = False
        for v in views.values():
            inherited = set(v["api_methods"])
            for imp in v["imports"]:
                if not (imp["module"].startswith((".", "@multica/core", "@multica/views"))):
                    continue
                for name in imp["symbols"]:
                    target_path = imp["targets"].get(name)
                    candidates = [s for s in symbols.get(name, []) if s["file"] == target_path] if imp["module"].startswith("@multica/core") else [views[target_path]] if target_path in views else []
                    inherited.update(a for target in candidates for a in target["api_methods"])
            combined = sorted(inherited)
            changed |= combined != v["api_methods"]
            v["api_methods"] = combined
        if not changed:
            break
    for candidates in symbols.values():
        for s in candidates:
            s.pop("calls")
            if s["api_methods"]:
                records.append(s)
    records.extend(views.values())
    return records


def build(ref):
    files = source_files()
    if git("diff", ref, "--", *SOURCE_ROOTS):
        raise ValueError("Indexed source files differ from --ref; commit them and use --ref HEAD.")
    if git("ls-files", "--others", "--exclude-standard", "--", *SOURCE_ROOTS):
        raise ValueError("Untracked indexed source files; commit or move them before building.")
    sources = {str(p.relative_to(ROOT)): p.read_text(errors="replace") for p in files}
    native, engine = backend(ROOT, ref)
    native_by_file = {}
    for node in native:
        native_by_file.setdefault(node["file"], []).append(node)
    records = sql_inventory(sources)
    scoped_native = [n for n in native if n["file"] in sources]
    records.extend({"kind": "symbol", **{k: v for k, v in n.items() if k != "calls"}}
                   for n in scoped_native if n["label"] in ("Function", "Method", "Class", "Struct", "Interface", "Field", "Type"))
    records.extend(ts_inventory(sources, scoped_native))
    for path, text in sources.items():
        if path.endswith(".go"):
            records.extend(go_functions(path, text, native_by_file.get(path, [])))
            if path.startswith("server/cmd/server/"):
                records.extend(routes(path, text))
            if path.startswith("server/cmd/multica/"):
                records.extend(cli_commands(path, text))
        if path.startswith("packages/views/locales/ru/") and path.endswith(".json"):
            def flatten(value, keys):
                if isinstance(value, dict):
                    for key, child in value.items():
                        flatten(child, [*keys, key])
                else:
                    records.append({"kind": "locale", "name": Path(path).stem + "." + ".".join(keys),
                                    "value": value, **location(path, text)})
            flatten(json.loads(text), [])
    queries = {r["name"]: r for r in records if r["kind"] == "query"}
    for r in records:
        if r["kind"] == "go_function":
            r["queries"] = sorted(set(r["calls"]) & queries.keys())
    counts = {k: sum(r["kind"] == k for r in records) for k in sorted({r["kind"] for r in records})}
    return {"source_sha": git("rev-parse", ref), "source_ref": ref,
            "source_fingerprint": fingerprint(files), "format_version": 1, "engine": engine,
            "scope": list(SOURCE_ROOTS), "counts": counts,
            "limitations": ["Codebase Memory AST/semantic symbols plus Multica lexical bridges, not a live database schema. Native graph edges can contain false positives.",
                "Table inventory includes historical migrations and down paths.",
                "View/API edges resolve named imports and re-exports; default aliases and dynamic imports need source review. File edges include conditional components.",
                "Go traversal combines native call names with sqlc selectors; query callers are candidates until their receiver is checked.",
                "Dynamic route prefixes and mounts are explicitly marked; middleware is not an authorization model.",
                "Cobra Use declarations and AddCommand sites are indexed separately; command paths are not guessed."],
            "records": records}


def load_index(path, ref):
    data = json.loads(path.read_text())
    expected = git("rev-parse", ref)
    if data["source_sha"] != expected or data["source_fingerprint"] != fingerprint(source_files()):
        raise ValueError(f"Stale index: source={data['source_sha']}, expected={expected}. Run build.")
    return data


def describe(r, selected=None):
    loc = f"{r['file']}:{r['line']}" if "file" in r else "history"
    detail = ""
    if r["kind"] == "query":
        detail = f" read={','.join(r['reads']) or '-'} write={','.join(r['writes']) or '-'}"
    if r["kind"] == "route":
        detail = " -> " + r["handler"]
    if r.get("api_methods"):
        detail = " -> " + ", ".join(a for a in r["api_methods"] if selected is None or a in selected)
    return f"{r['kind']:16} {loc} {r['name']}{detail}"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--index", type=Path, default=DEFAULT)
    parser.add_argument("--ref", default="origin/main", help="Required source revision for reads")
    subs = parser.add_subparsers(dest="command", required=True)
    subs.add_parser("build")
    subs.add_parser("check")
    search = subs.add_parser("find")
    search.add_argument("text")
    search.add_argument("--kind")
    search.add_argument("--limit", type=int, default=40)
    uses = subs.add_parser("uses")
    uses.add_argument("table")
    uses.add_argument("--limit", type=int, default=80)
    writers = subs.add_parser("writers")
    writers.add_argument("table")
    writers.add_argument("field")
    writers.add_argument("--limit", type=int, default=40)
    screen = subs.add_parser("screens")
    screen.add_argument("api", help="API client method name or path fragment")
    screen.add_argument("--limit", type=int, default=80)
    trace = subs.add_parser("trace")
    trace.add_argument("api", help="Exact API client method name")
    trace.add_argument("--limit", type=int, default=30)
    args = parser.parse_args()
    if args.command == "build":
        start = time.monotonic()
        data = build(args.ref)
        args.index.parent.mkdir(parents=True, exist_ok=True)
        args.index.write_text(json.dumps(data, ensure_ascii=False, separators=(",", ":")) + "\n")
        print(json.dumps({"source_sha": data["source_sha"], "seconds": round(time.monotonic() - start, 3), "counts": data["counts"]}))
        return
    data = load_index(args.index, args.ref)
    print(f"source_sha={data['source_sha']} source_fingerprint={data['source_fingerprint']}")
    records = data["records"]
    if args.command == "check":
        print(json.dumps(data["counts"]))
        return
    if args.command == "find":
        needle = args.text.casefold()
        matches = [r for r in records if (not args.kind or r["kind"] == args.kind)
                   and needle in json.dumps(r, ensure_ascii=False).casefold()]
    elif args.command == "uses":
        queries = {r["name"] for r in records if r["kind"] == "query" and args.table in r["tables"]}
        matches = [r for r in records if args.table in r.get("tables", [])
                   or queries.intersection(r.get("queries", []))]
    elif args.command == "writers":
        matches = [r for r in records if r["kind"] == "query"
                   and args.field in r.get("write_fields", {}).get(args.table, [])]
    elif args.command == "screens":
        methods = {r["name"] for r in records if r["kind"] == "api_client"
                   and (args.api in r["name"] or args.api in r["path"])}
        matches = [r for r in records if r["kind"] in ("view", "core_symbol")
                   and methods.intersection(r.get("api_methods", []))]
    else:
        methods = [r for r in records if r["kind"] == "api_client" and r["name"] == args.api]
        if not methods:
            raise ValueError("Unknown API method")
        normalize = lambda p: re.sub(r"\{[^}]+\}", "{}", p).rstrip("/")
        matched_routes = [r for r in records if r["kind"] == "route" and any(
            r["method"] == a["method"] and normalize(r["path"]) == normalize(a["path"]) for a in methods)]
        functions = {}
        for r in records:
            if r["kind"] == "go_function" and r["file"].startswith("server/internal/"):
                functions.setdefault(r["name"], []).append(r)
        queries = {r["name"]: r for r in records if r["kind"] == "query"}
        matches = [*methods, *matched_routes]
        queue = [(r["handler"], "", 0) for r in matched_routes]
        seen, used = set(), set()
        while queue:
            name, caller, depth = queue.pop(0)
            candidates = functions.get(name, [])
            nearby = [f for f in candidates if f["file"] == caller]
            handler = [f for f in candidates if f["file"].startswith("server/internal/handler/")] if not caller else []
            candidates = nearby or handler or candidates
            if len(candidates) != 1 or depth > 8:
                continue
            function = candidates[0]
            key = (function["file"], name)
            if key in seen:
                continue
            seen.add(key)
            if function["queries"]:
                matches.append(function)
                used.update(function["queries"])
            queue.extend((call, function["file"], depth + 1) for call in function["calls"] if call not in queries)
        matches.extend(queries[q] for q in sorted(used))
        print("Candidate Go call graph (untyped, depth <= 8); SQL queries below are source evidence, not authorization or execution proof.")
    for r in matches[:args.limit]:
        print(describe(r, methods if args.command == "screens" else None))
    print(f"matches={len(matches)} shown={min(len(matches), args.limit)} (static references; inspect evidence)")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, subprocess.CalledProcessError) as exc:
        print(str(exc), file=sys.stderr)
        sys.exit(1)
