#!/usr/bin/env python3
"""Build a readable static page from docs/stt/product/*.md.

The Markdown files stay the source of truth; the page is a derived artifact.
Rendering happens in the browser (marked + mermaid), so the build needs only
the Python standard library.

    python3 tools/product/site.py build [--out DIR]
    python3 tools/product/site.py check [--out DIR]
"""
from __future__ import annotations

import argparse
import html
import json
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
DOCS = ROOT / "docs" / "stt" / "product"
DEFAULT_OUT = ROOT / ".product-site"
TRACKER = "https://multica.yakutov.com/stt/issues/"
REPO_BLOB = "https://github.com/inoisy/multica/blob/main/"

# Reading order for people; anything else found in DOCS is appended.
ORDER = [
    ("roadmap.md", "Роадмап"),
    ("README.md", "О продукте"),
    ("decisions.md", "Решения"),
    ("market.md", "Рынок и аналоги"),
    ("problems.md", "Проблемы"),
    ("users.md", "Пользователи"),
    ("map.md", "Карта продукта"),
    ("ui.md", "Интерфейс"),
    ("upstream.md", "Апстрим"),
    ("roadmap-inventory.md", "Реестр задач"),
    ("roadmap-upstream-check.md", "Проверка апстрима"),
    ("acceptance.md", "Приёмка базы"),
    ("sources.md", "Источники"),
]


def collect() -> list[dict]:
    files = sorted(p.relative_to(DOCS).as_posix() for p in DOCS.rglob("*.md"))
    titles = dict(ORDER)
    ordered = [name for name, _ in ORDER if name in files]
    ordered += [f for f in files if f not in ordered]
    docs = []
    for name in ordered:
        text = (DOCS / name).read_text(encoding="utf-8")
        heading = re.search(r"^#\s+(.+)$", text, re.M)
        fallback = Path(name).stem.replace("-", " ")
        if "/" in name:
            fallback = Path(name).parent.name + ": " + fallback
        title = titles.get(name) or (heading.group(1).strip() if heading else fallback)
        docs.append({"id": name, "title": title, "md": text})
    return docs


def git_sha() -> str:
    try:
        return subprocess.run(
            ["git", "-C", str(ROOT), "rev-parse", "--short", "HEAD"],
            capture_output=True, text=True, check=True,
        ).stdout.strip()
    except (OSError, subprocess.CalledProcessError):
        return "unknown"


def build(out: Path) -> Path:
    docs = collect()
    meta = {"sha": git_sha(), "tracker": TRACKER, "repoBlob": REPO_BLOB,
            "docsPath": DOCS.relative_to(ROOT).as_posix() + "/"}
    page = TEMPLATE.replace("__DATA__", json.dumps(
        {"docs": docs, "meta": meta}, ensure_ascii=False).replace("</", "<\\/"))
    page = page.replace("__TITLE__", html.escape("Multica — роадмап и база продукта"))
    out.mkdir(parents=True, exist_ok=True)
    target = out / "index.html"
    target.write_text(page, encoding="utf-8")
    return target


def check(out: Path) -> list[str]:
    """Return problems: missing docs in the page or links to absent docs."""
    problems = []
    target = out / "index.html"
    if not target.exists():
        return [f"{target} not built"]
    page = target.read_text(encoding="utf-8")
    names = {d["id"] for d in collect()}
    for name in names:
        if json.dumps(name) not in page:
            problems.append(f"{name}: not in page")
    for name in names:
        text = (DOCS / name).read_text(encoding="utf-8")
        for link in re.findall(r"\]\(([^)#\s]+\.md)(?:#[^)]*)?\)", text):
            resolved = (DOCS / Path(name).parent / link).resolve()
            if resolved.is_relative_to(DOCS) and not resolved.exists():
                problems.append(f"{name}: broken link {link}")
    return problems


TEMPLATE = r"""<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>__TITLE__</title>
<style>
:root{--bg:#fff;--fg:#1d1f23;--muted:#6b7280;--line:#e5e7eb;--side:#f7f7f8;--accent:#2f6fed;--code:#f3f4f6;--sel:#e8effd}
:root[data-theme=dark]{--bg:#16181d;--fg:#e6e7ea;--muted:#9aa0aa;--line:#2c3038;--side:#1c1f25;--accent:#7aa2ff;--code:#22262e;--sel:#24324f}
@media (prefers-color-scheme:dark){:root:not([data-theme=light]){--bg:#16181d;--fg:#e6e7ea;--muted:#9aa0aa;--line:#2c3038;--side:#1c1f25;--accent:#7aa2ff;--code:#22262e;--sel:#24324f}}
*{box-sizing:border-box}
html,body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.55 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
a{color:var(--accent);text-decoration:none}a:hover{text-decoration:underline}
.layout{display:grid;grid-template-columns:260px minmax(0,1fr);min-height:100vh}
aside{background:var(--side);border-right:1px solid var(--line);padding:16px 12px;position:sticky;top:0;height:100vh;overflow:auto}
aside h1{font-size:15px;margin:0 8px 4px}
aside .sha{color:var(--muted);font-size:12px;margin:0 8px 12px}
nav a{display:block;padding:6px 8px;border-radius:6px;color:var(--fg)}
nav a.on{background:var(--sel);font-weight:600}
nav a:hover{text-decoration:none;background:var(--sel)}
main{padding:20px 32px 64px;max-width:1100px;width:100%;min-width:0}
.bar{display:flex;gap:8px;align-items:center;justify-content:flex-end;margin-bottom:8px;flex-wrap:wrap}
.seg{display:inline-flex;border:1px solid var(--line);border-radius:8px;overflow:hidden}
.seg button,.btn{height:32px;padding:0 12px;border:0;background:transparent;color:var(--fg);font:inherit;font-size:13px;cursor:pointer}
.seg button.on{background:var(--sel);font-weight:600}
.btn{border:1px solid var(--line);border-radius:8px}
.menu{display:none}
.doc table{border-collapse:collapse;display:block;overflow-x:auto;max-width:100%;margin:12px 0}
.doc th,.doc td{border:1px solid var(--line);padding:6px 10px;vertical-align:top;text-align:left;min-width:90px}
.doc th{background:var(--side)}
.doc code{background:var(--code);padding:1px 5px;border-radius:4px;font-size:.9em;overflow-wrap:anywhere}
.doc pre{background:var(--code);padding:12px;border-radius:8px;overflow:auto}
.doc pre code{padding:0;background:none}
.doc h1{font-size:26px;margin:8px 0 12px}.doc h2{font-size:20px;margin-top:32px;border-bottom:1px solid var(--line);padding-bottom:4px}
.doc h3{font-size:17px}
.doc img{max-width:100%}
.doc blockquote{margin:0;padding:0 12px;border-left:3px solid var(--line);color:var(--muted)}
.src{white-space:pre-wrap;background:var(--code);padding:16px;border-radius:8px;font:13px/1.5 ui-monospace,Menlo,monospace;overflow-wrap:anywhere}
.mermaid{background:var(--bg)}
@media (max-width:800px){
  .layout{grid-template-columns:minmax(0,1fr)}
  aside{position:static;height:auto;border-right:0;border-bottom:1px solid var(--line)}
  aside nav{display:none}aside.open nav{display:block}
  .menu{display:inline-block}
  main{padding:12px 14px 48px}
  .bar{justify-content:space-between}
}
</style>
</head>
<body>
<div class="layout">
<aside id="side"><h1>Multica: база продукта</h1><div class="sha" id="sha"></div><nav id="nav"></nav></aside>
<main>
<div class="bar">
  <button class="btn menu" id="menu" aria-expanded="false">Разделы</button>
  <span style="flex:1"></span>
  <div class="seg" role="group" aria-label="Вид"><button id="vDoc" class="on">Документ</button><button id="vSrc">Markdown</button></div>
  <button class="btn" id="theme" aria-label="Сменить тему">Тема</button>
</div>
<article class="doc" id="doc"></article>
</main>
</div>
<script id="data" type="application/json">__DATA__</script>
<script src="https://cdn.jsdelivr.net/npm/marked@12.0.2/marked.min.js"></script>
<script src="https://cdn.jsdelivr.net/npm/mermaid@10.9.1/dist/mermaid.min.js"></script>
<script>
const {docs, meta} = JSON.parse(document.getElementById('data').textContent);
const byId = Object.fromEntries(docs.map(d => [d.id, d]));
let view = 'doc';
document.getElementById('sha').textContent = 'Собрано из ' + meta.sha;
const nav = document.getElementById('nav');
for (const d of docs) {
  const a = document.createElement('a'); a.href = '#/' + d.id; a.textContent = d.title; a.dataset.id = d.id; nav.appendChild(a);
}
function resolve(base, href) {
  const parts = base.split('/').slice(0, -1);
  for (const p of href.split('/')) { if (p === '..') parts.pop(); else if (p !== '.') parts.push(p); }
  return parts.join('/');
}
function rewrite(root, id) {
  for (const a of root.querySelectorAll('a[href]')) {
    const href = a.getAttribute('href');
    if (/^(https?:|mailto:)/.test(href)) { a.target = '_blank'; a.rel = 'noopener'; continue; }
    if (href.startsWith('#')) { a.href = '#/' + id + '/' + href.slice(1); continue; }
    const [path, frag] = href.split('#');
    const target = resolve(id, path);
    if (byId[target]) a.href = '#/' + target + (frag ? '/' + frag : '');
    else {
      const full = resolve(meta.docsPath + id, path);
      a.href = meta.repoBlob + full + (frag ? '#' + frag : ''); a.target = '_blank'; a.rel = 'noopener';
    }
  }
  for (const h of root.querySelectorAll('h1,h2,h3,h4')) {
    if (!h.id) h.id = h.textContent.trim().toLowerCase().replace(/[^\p{L}\p{N}\s-]/gu, '').replace(/\s+/g, '-');
  }
}
function route() {
  const m = location.hash.match(/^#\/([^/]+(?:\/[^/]+)*?\.md)(?:\/(.+))?$/);
  const id = m && byId[m[1]] ? m[1] : docs[0].id;
  const frag = m && m[2] ? decodeURIComponent(m[2]) : null;
  const d = byId[id];
  const el = document.getElementById('doc');
  document.title = d.title + ' — Multica';
  if (view === 'src') {
    el.innerHTML = ''; const pre = document.createElement('div'); pre.className = 'src'; pre.textContent = d.md; el.appendChild(pre);
  } else {
    el.innerHTML = marked.parse(d.md, {gfm: true});
    for (const code of el.querySelectorAll('pre code.language-mermaid')) {
      const div = document.createElement('div'); div.className = 'mermaid'; div.textContent = code.textContent; code.parentElement.replaceWith(div);
    }
    rewrite(el, id);
    try { mermaid.run({nodes: el.querySelectorAll('.mermaid')}); } catch (e) {}
  }
  for (const a of nav.children) a.classList.toggle('on', a.dataset.id === id);
  document.getElementById('side').classList.remove('open');
  const target = frag && (document.getElementById(frag) || document.querySelector('[name="' + CSS.escape(frag) + '"]'));
  if (target) target.scrollIntoView(); else window.scrollTo(0, 0);
}
function dark() {
  const t = document.documentElement.dataset.theme;
  return t ? t === 'dark' : matchMedia('(prefers-color-scheme: dark)').matches;
}
function initMermaid() { mermaid.initialize({startOnLoad: false, theme: dark() ? 'dark' : 'default'}); }
const saved = localStorage.getItem('product-site-theme'); if (saved) document.documentElement.dataset.theme = saved;
document.getElementById('theme').onclick = () => {
  const next = dark() ? 'light' : 'dark';
  document.documentElement.dataset.theme = next; localStorage.setItem('product-site-theme', next); initMermaid(); route();
};
document.getElementById('menu').onclick = (e) => {
  const s = document.getElementById('side'); s.classList.toggle('open'); e.currentTarget.setAttribute('aria-expanded', s.classList.contains('open'));
};
for (const [btn, v] of [['vDoc', 'doc'], ['vSrc', 'src']]) document.getElementById(btn).onclick = () => {
  view = v; document.getElementById('vDoc').classList.toggle('on', v === 'doc'); document.getElementById('vSrc').classList.toggle('on', v === 'src'); route();
};
initMermaid();
addEventListener('hashchange', route);
route();
</script>
</body>
</html>
"""


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("command", choices=["build", "check"])
    parser.add_argument("--out", type=Path, default=DEFAULT_OUT)
    args = parser.parse_args()
    if args.command == "build":
        print(build(args.out))
    problems = check(args.out)
    for p in problems:
        print(p, file=sys.stderr)
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
