# Product code index and screenshots

Run from the repository root. Product code and production data are read-only.
The generated inventory lives in `docs/stt/product/generated/index.json`.

## Ready-made backend

The symbol parser and local code graph are provided by
[Codebase Memory MCP](https://github.com/DeusData/codebase-memory-mcp), pinned to
[0.12.1](https://github.com/DeusData/codebase-memory-mcp/releases/tag/v0.12.1).
It supports Go and TypeScript, ships its parsers in a native executable, and
requires no hosted service, model API, language server installation or Docker.
Its standalone CLI finishes indexing before returning; we do not install its
agent integrations or keep a watcher running.

| Candidate | Evaluation |
| --- | --- |
| Codebase Memory 0.12.1 | Actually run on the full immutable Multica source: 33.1 s for native indexing; 46.7 s including export/bridges; 52.4 s for the default command including its first download; repeated build 14.6 s. Selected for AST symbol ranges and a reusable local graph |
| [ast-grep](https://ast-grep.github.io/guide/tools/json) | Documentation reviewed: ready structural search and JSON matches, but a persistent cross-language dependency graph would still need a separate implementation. Not benchmarked here |
| [Universal Ctags](https://docs.ctags.io/en/latest/man/ctags.1.html) | Documentation reviewed: symbol/tag inventory is useful, but does not supply the required screen/API/sqlc mapping. Not benchmarked here |

The 48.6 MB release archive is a deliberate tool dependency, outside application
dependencies. `cbm.py` verifies its pinned SHA256 before execution. A verified
platform-specific 0.12.1 executable can be supplied through `PRODUCT_CBM_BIN`.
The ready-made backend owns generic parsing; the local adapters extract the
Multica-specific chi/Cobra conventions, sqlc blocks, migration history, Russian
locale keys, and API-client/query-hook bridges. No embeddings or model calls.

## Build, verify, search

Prerequisites: Python 3.10+, Git; Node 18+ only for screenshots. First build on
Linux x86_64 downloads the pinned backend; subsequent builds are offline.

```sh
git fetch origin main
python3 tools/product/index.py build
python3 tools/product/index.py check
python3 tools/product/index.py find runtime_id --kind query --limit 10
python3 tools/product/index.py find AgentDetailPage --kind symbol
python3 tools/product/index.py writers agent runtime_id
python3 tools/product/index.py uses subscription_pool_member
python3 tools/product/index.py screens /api/pools
python3 tools/product/index.py trace listPools
```

The first JSON fields are `source_sha`, `source_ref` and `source_fingerprint`.
The default ref is `origin/main`. Build refuses modified/untracked
product source; it indexes an immutable `git archive` snapshot. Read commands
refuse a stale SHA or a changed working-tree fingerprint. Fetch explicitly to
compare against the remote's newest head; the tool does not fetch silently.
Docs/tools commits above an unchanged product snapshot do not invalidate it.
For another committed branch: `python3 tools/product/index.py --ref HEAD build`
and use the same `--ref HEAD` on subsequent reads.

Cache: `tools/product/.cache/` (ignored), or `PRODUCT_CACHE_DIR`. It contains the
backend, source snapshot and local graph. It may be deleted while no build is
running. The exported JSON contains relative code locations, symbols and locale
text, not a credential dump or host configuration. The complete native graph
stays local. For advanced graph queries use the backend CLI with the same
`CBM_CACHE_DIR=<PRODUCT_CACHE_DIR>/graph` and project `product-index`; consult
`cli search_graph --help` / `cli query_graph --help`. Never point it at the
Multica application database.

## Evidence limits

- `table` means seen in migration history, including removed tables. This is
  not a reconstructed live schema; use `direction` and migration source.
- SQL queries expose separate FROM/JOIN and mutation targets. CTE definitions
  are not promoted into real tables. Legacy cleanup queries can remain after
  the business reader is gone.
- Native graph accuracy is bounded. The benchmark reported 369 partial parses
  and 11 unusable parses; its edges can also
  resolve a same-name call incorrectly. `engine` preserves counts. We retain
  our independent SQL inventory and explicit API/chi evidence for that reason.
- `screens` includes conditional component imports and route shells; it lists
  possible callers, not requests observed at runtime. Named re-exports are
  resolved; default import aliases, dynamic imports and renamed re-exports can
  be missed. Core symbol-name propagation and Go call-name traversal are
  conservative candidates, not a type checker or proof of authorization.
- `trace` traverses candidates up to eight Go call levels, skips ambiguous
  names, and prints original sqlc locations. It can miss interface dispatch,
  dynamic SQL and callbacks. Inspect the evidence before a deletion.
- Cobra `Use` declarations and `AddCommand` sites are separate records. Factory
  locals are indexed; a complete executable command tree is not guessed.
- chi literal nesting is resolved; dynamic prefixes and mounts are marked.
  Middleware is not an inferred permissions model.
- External repositories, running services and manually changed data are outside
  the index. Planned migrations do not become records until committed.

## Screenshots

```sh
node tools/product/capture.mjs capture ./private-config.json ./private-shots
node tools/product/capture.mjs archive ./acceptance-shots ./private-shots ./provenance.json
```

Copy `capture.example.json` into a private working file and supply the branch
preview URL, actual deployed SHA, page-ready selectors and optional actions
(`{"click":"selector"}` or `{"select":"selector","value":"value"}`). Each
page is captured in light/dark at 1440/375. HTTP failures, missing ready state or
failed actions fail the command; screenshots retain pageerror/overflow/control
audits in the manifest. Audits supplement visual inspection and do not pass UI
acceptance on their own. Open the actual dropdown via an action for its own page
entry. No automatic clicking of save/delete actions is performed.

Install/use Playwright in the operator's environment, outside this tool's source.
`PRODUCT_PLAYWRIGHT_MODULE` can name an existing module and
`PRODUCT_CHROMIUM_EXECUTABLE` an existing compatible Chromium binary. A test
user's private state file may be supplied as `PRODUCT_STORAGE_STATE`; it is not
copied into the output. `theme_storage_key` is configurable per application.
Screenshots can show sensitive data: keep config/state/output out of Git.

Archive mode needs no browser. Its provenance JSON must identify the issue,
`evidence_kind` and known source revision (or explicitly unknown per-file SHA).
It copies PNG/WebP files and creates a SHA256 manifest without pretending the
images were freshly captured. Both modes were exercised in STT-436; the internal
visual evidence inventory is in `docs/stt/product/ui.md`.

## Checks

```sh
python3 -m unittest discover -s tools/product -p 'test_*.py' -v
python3 tools/product/index.py check
node --check tools/product/capture.mjs
git diff --check
```

Tests cover nested chi scopes and comments, SET versus WHERE columns, migration history versus SQL mutation
targets, stale/dirty source refusal, and factory-local Cobra declarations.
Product code is unchanged; application test suites are not needed for this tool.

## Readable page of the product base

`site.py` renders `docs/stt/product/*.md` into one static `index.html` for
people: section menu, light/dark theme, 375 px layout, Mermaid, and a
«Документ / Markdown» switch. The Markdown files remain the source of truth;
the page is a derived artifact and is not committed (`.product-site/` is ignored).

```sh
python3 tools/product/site.py build          # writes .product-site/index.html and checks links
python3 tools/product/site.py check          # re-checks an existing build
python3 -m unittest tools/product/test_site.py
```

Publish the directory as a stand artifact under the stable name
`art-multica-product-e7d8b8` so the link stays the same
(https://art-multica-product-e7d8b8.stand.yakutov.com). Rendering uses
`marked` and `mermaid` from jsDelivr in the reader's browser.
