# distill

A standalone knowledge-base CLI bundled with the funcfinder toolkit. It stores
documents in a single SQLite file and searches them with full-text search
(FTS5), vector similarity, regex, or a hybrid of FTS + vector — useful for
building a persistent memory/knowledge store an agent can query across
sessions (notes, runbooks, ingested docs, error logs, etc.).

Binary: `distill` (built from `cmd/distill`).

---

## Storage model

Everything lives in one SQLite file (default `.knowledge/docs.sqlite`,
override with `--db <path>`). `init` creates it if missing; `add` and `search`
also auto-create it via `Open`, so an explicit `init` is optional but cheap
and useful for scripting.

Schema (`internal/knowledge/db.go`):

| Table | Purpose |
|-------|---------|
| `docs` | `id, title, content, type, created_at, metadata` — the source of truth |
| `docs_fts` | FTS5 virtual table, kept in sync with `docs` via `AFTER INSERT/UPDATE/DELETE` triggers |
| `docs_vec` | `doc_id, dim, embedding (BLOB)` — optional vector per doc |

`type` is a free-form string (`general`, `tool_usage`, `error`, `scenario`,
...) used only for `--filter-type` pre-filtering; it isn't validated against
a fixed enum.

---

## Actions

```
distill [--db <path>] init
distill [--db <path>] add    --title <t> --content <c> [--type <t>] [--author --topic --priority --pinned --supersedes] [--meta <json>] [--embedding <floats>] [--json]
distill [--db <path>] add    --file <path.txt|md|pdf>  [--type <t>] [--author --topic --priority --pinned --supersedes] [--chunk-size N] [--chunk-overlap N] [--json]
distill [--db <path>] add    --session <transcript.jsonl> [--embed-model <m>] [--json]   (Claude Code session, per-turn, historical timestamps)
distill [--db <path>] search --query <q>               [--embedding <floats>] [--mode fts|vec|hybrid|regex] [--metric cosine|l2] [--filter-type <type>] [--limit N] [--json]
distill [--db <path>] count  [--json]
distill --version
```

The `--db` flag must come **before** the action word (`init`/`add`/`search`/`count`);
everything after the action word is parsed by that action's own flag set.

### init

```bash
distill --db .knowledge/docs.sqlite init
```

Creates the parent directory and the SQLite file/schema. Idempotent — safe to
call every time a script starts.

### add — single document

```bash
distill add --title "Go error handling" \
  --content "Errors are values. Use errors.Is/errors.As to check types." \
  --type tool_usage --topic go --pinned --author ruslan --json
# {"id":1}
```

`--title` and `--content` are required unless `--file` is used. `--embedding`
accepts comma-separated floats, optionally wrapped in `[...]`.

**Metadata flags** (shared with `distill-server` via `internal/docmeta`):
`--author`, `--topic`, `--priority`, `--pinned`, `--supersedes` set the
provenance/ranking fields the `search` ranking layer reads. `--meta '{...}'`
remains a raw escape hatch for arbitrary keys; the structured flags overlay it,
so `--meta '{"source":"manual"}' --topic go` composes into
`{"source":"manual","topic":"go"}`.

### add — file ingestion (chunked)

```bash
distill add --file notes.md --type general --chunk-size 800 --chunk-overlap 80 --json
# {"chunks":2,"ids":[3,4]}
```

Supported extensions: `.txt`, `.md`, `.pdf` (dispatch by extension in
`internal/knowledge/ingest.go`). The file is split into `docSection`s, then
into `Chunk`s:

- **`.md`** — split on top-level headings (`splitMDSections`); each section
  becomes its own chunk boundary (chunks never cross section boundaries).
- **`.txt`** — converted to UTF-8 (`toUTF8`) then split into paragraphs.
- **`.pdf`** — see [PDF ingestion](#pdf-ingestion-and-ocr-quality-gate) below.

Within a section, paragraphs are merged greedily up to `--chunk-size` runes;
oversized sections are split at paragraph boundaries with `--chunk-overlap`
runes of overlap between consecutive chunks so context isn't lost at the
seam. Chunks that look like filler (repeated runs of punctuation — tables of
contents, `"--------"`, `". . . . 374"`) are dropped (`hasRepetitiveRuns`).

Each chunk is inserted via `--type`/`--meta` you supplied; embeddings are not
auto-generated for file ingestion — add them yourself in a follow-up pass if
needed (vector search requires an embedding per doc; see [Vector search](#vector-search--embeddings)).

#### PDF ingestion and OCR quality gate

PDF text is extracted by position (`extractPageText`): elements are grouped
into lines by Y-coordinate and spaced by X-gap, because the underlying
`ledongthuc/pdf` library's plain-text extraction glues words together when a
PDF has no explicit space glyphs. If the position-based result still looks
glued (`looksGlued` — few tokens or average word length > 15 chars), it falls
back to the library's `GetPlainText`.

Before committing to a full parse, `distill` samples up to 10 evenly-spaced
pages (skipping the first/last ~5%, which are often covers/indices) and
scores text quality (`pageTextQuality`: letter ratio, word-likeness ratio,
penalty for excessive single-character tokens — a signature of spaced-out
OCR like `"T e x t"`). If the average score is below `0.45`, ingestion is
**rejected** with an `OCRQualityError` rather than polluting the knowledge
base with garbage:

```bash
distill add --file scan.pdf --json
# {"error":"bad_ocr","score":0.31,"file":"scan.pdf"}   (exit code 2)
```

Without `--json` it prints a human-readable warning to stderr suggesting
`ocrmypdf` to fix the source file. This is a hard rejection, not a soft
warning — fix the PDF and re-run.

### add — web crawler (`--url`)

```bash
distill add --url https://pkg.go.dev/net/http --max-pages 200 --json
# {"pages":37,"chunks":412,"ids":[...]}
```

Crawls a documentation site and ingests every page's text (`internal/knowledge/web.go`).
The crawl is deliberately scoped and polite:

- **Stays on-site** — only follows links with the same host *and* the start
  URL's path as a prefix (`isSameSite`), so `…/net/http` won't wander into
  `…/os` or off to another domain. Query-string variants (tab/pagination
  links) are skipped unless the start URL itself carries a query.
- **Deduplicates** — pages with a byte-identical body are indexed once
  (content-hash set), and versioned URL variants (`@go1.21`, `@v1.2.3`) are
  normalized to a single canonical path (`normalizeURL`) so mirrored version
  trees don't multiply the corpus.
- **Extracts real content** — pulls text from `<main>`/`<article>`/`role="main"`
  when present, structured by headings/paragraphs, skipping chrome
  (`script`/`style`/`nav`/`header`/`footer`/`aside`). Non-HTML responses and
  non-200s are skipped, not fatal; the body is capped at 4 MB per page.
- **Bounded** — `--max-pages` (default 200) caps the crawl; progress prints to
  stderr (`crawling [N fetched, M queued] …`) unless `--json`.

Extracted text flows through the same chunker as file ingestion
(`--chunk-size`/`--chunk-overlap`, `hasRepetitiveRuns` filtering). Embeddings
are not auto-generated — add them in a follow-up pass if you need vector search.

### add — session transcript (`--session`)

Ingest a **Claude Code session transcript** (`.jsonl`) into a searchable
knowledge base — turn a conversation into durable, queryable memory:

```bash
distill add --session ~/.claude/projects/<proj>/<id>.jsonl \
  --embed-model qwen3-embedding:0.6b
# ingested 2378 docs from 1694 turns (session.jsonl)
#   user       261
#   assistant  927
#   thinking   1190
#   span       2026-07-17 11:05 → 2026-07-18 19:05
```

Each conversational turn becomes one or more docs (long turns are split by the
shared chunker), typed by **role** so a single transcript yields three separable
*slices of knowledge*:

| `type` | slice |
|--------|-------|
| `user` | your prompts / instructions |
| `assistant` | the assistant's answers |
| `thinking` | the assistant's reasoning blocks |

Because the role is the doc `type`, you filter the slice you want:
`--filter-type thinking` searches only reasoning, `--filter-type assistant`
only answers (thinking dropped). Provenance (uuid, git branch, model) lands in
metadata, and the branch doubles as a coarse `topic`.

**Timestamps are preserved.** Each doc's `created_at` is the turn's real time
(via `AddAt`), not import time — so recency ranking (`--recency-window`) and
chronology reflect when things were actually said. What's *not* imported:
`tool_use`/`tool_result` blocks (actions and bulky outputs, not knowledge) and
the base64 `signature` on thinking blocks (an opaque crypto signature, not text).

### search

```bash
distill search --query "error handling" --mode fts --limit 5 --json
```

| Flag | Default | Meaning |
|------|---------|---------|
| `--query` | — | FTS/regex query text (required unless `--embedding` given for `vec` mode) |
| `--embedding` | — | comma-separated floats for `vec`/`hybrid` modes |
| `--mode` | `hybrid` | `fts` \| `vec` \| `hybrid` \| `regex` |
| `--metric` | `cosine` | `cosine` \| `l2` — distance metric for `vec`/`hybrid` |
| `--filter-type` | — | restrict to one `type` before scoring (vec/regex modes) |
| `--limit` | `10` | max results |
| `--prefix` | `true` | auto-append `*` to FTS tokens (`call` → `call*`) so partial words match |
| `--graph` | `0` | graph-aware: annotate each hit with up to N typed L2 relations (0 = off) |
| `--cluster` | `false` | fold duplicate hits into their top-ranked representative |
| `--json` | `false` | structured output instead of the text snippet view |

#### Graph-aware retrieval (`--graph`, `--cluster`)

Once the L2 graph exists (`distill digest`), search can *read* it — surfacing
how a hit relates to the rest of the corpus, not just that it matched:

```bash
distill search --query "authentication" --graph 4
# [SPEC-1] Auth v1  (spec)
#     Authentication uses static API keys …
#       → supersedes → SPEC-2  [proposed, 0.95]
# [SPEC-2] Auth v2  (spec)  ⚠ superseded
#       ← supersedes ← SPEC-1  [proposed, 0.95]
```

- **`--graph N`** annotates each hit with up to N typed relations, oriented
  relative to the hit (`→` outgoing, `←` incoming). An **incoming** `supersedes`
  or `contradicts` raises a `⚠ superseded` / `⚠ contradicted` banner — a
  "don't ground on this, it's obsolete/disputed" signal *before* the agent cites
  it. Off by default; the result order is unchanged.
- **`--cluster`** folds hits the graph marks `duplicates` of a higher-ranked
  result into that result (`⊕ folds SPEC-9 (duplicates)`), so a cluster of
  near-identical docs takes one slot in the top-N instead of crowding it out.

> Graph-aware retrieval is only as trustworthy as the edges the digester wrote:
> a local model can mis-judge a relation (e.g. invert a `supersedes` direction).
> The rendering is always faithful to the stored edge — treat `proposed` edges as
> suggestions to confirm, not ground truth.

#### Search modes

- **`fts`** — SQLite FTS5 full-text search over `title`+`content`
  (`BuildFTSQuery`/`SearchFTS`). Fast, no embedding needed. Best for keyword
  lookups.
- **`vec`** — pure vector similarity against `docs_vec` using the chosen
  `--metric`. Requires `--embedding`; only docs that have a stored embedding
  are searchable. Best when you have a real embedding model and want
  semantic recall.
- **`regex`** — Go regex match over `content` performed in application code
  (`SearchRegex`), with optional `--filter-type`. Use for structural lookups
  FTS can't express (e.g. matching a specific error code pattern).
- **`hybrid`** (default) — runs both FTS and vector scoring and combines them
  into `HybridScore` (`SearchHybrid`). If you don't pass `--embedding`, it
  degenerates gracefully to FTS-only ranking — so `hybrid` is a safe default
  even before you have embeddings wired up.

JSON output fields are `id, title, content, type, created_at, metadata`, plus
whichever of `fts_rank` / `vec_dist` / `hybrid_score` the mode populated
(zero-value fields are omitted — e.g. an exact vector match has `vec_dist: 0`
and won't show the key at all).

### count

```bash
distill count --json   # {"count":4}
```

---

## Vector search & embeddings

Two ways to get vectors into the index:

- **Auto (`--embed-model`)** — point `distill` at a local Ollama model and it
  embeds text for you at both `add` and `search` time:

  ```bash
  distill add    --file guide.md --embed-model qwen3-embedding:0.6b
  distill search --query "how do I authenticate" --embed-model qwen3-embedding:0.6b
  ```

  `--embed-url` overrides the endpoint (default `http://localhost:11434/api/embed`).
  File/crawl ingests embed all chunks in one batch request. **Use the same model
  for `add` and `search`** — mixing models mixes vector spaces and makes cosine
  meaningless. If the endpoint is unreachable, it warns and degrades to FTS
  rather than failing (ingest stores without vectors; search runs keyword-only).

- **BYO (`--embedding`)** — pass a precomputed float vector directly (overrides
  `--embed-model`); generate it with whatever model you like. The tool then only
  stores/compares — it does not call out.

Distance is computed in SQL: `internal/knowledge/vector.go` registers
`vec_distance_cosine`/`vec_distance_l2` over the raw float32 BLOB, so ranking
happens inside the query, not in Go.

With neither `--embed-model` nor `--embedding`, `add` stores FTS-only and
`hybrid`/`vec` have no vectors to match — stick to `--mode fts` (or the
`hybrid` default, which falls back to FTS).

---

## Knowledge graph (L2 digester)

Search ranks *primary information*. The **digester** turns it into *knowledge*:
it asks a local LLM which near-neighbor documents are actually related, and how,
recording each as a typed, weighted, provenance-stamped edge.

It works in two layers over the same SQLite file:

- **L1 (geometry, deterministic)** — `distill digest` first (re)builds the
  cosine-kNN graph from stored vectors. Free, no LLM. This bounds the LLM to
  plausible pairs (each doc's nearest neighbors), so a pass is O(n·k), not O(n²).
- **L2 (typed, LLM)** — for each candidate pair the model classifies the relation
  into a tight taxonomy and the edge is written as `proposed`:

  | kind | meaning |
  |------|---------|
  | `supersedes`  | subject makes object obsolete (directed; review before trusting) |
  | `contradicts` | the two make conflicting claims |
  | `elaborates`  | subject adds detail/depth to object |
  | `depends_on`  | subject requires object to hold |
  | `duplicates`  | subject restates object |
  | `same_topic`  | same subject, no stronger relation |

### digest

```bash
# Needs vectors already stored (ingest with --embed-model) and a generate model.
distill digest --model gemma4:12b
# digest: 463 candidates, 0 skipped (clean), 6 classified, 6 edges written
#   elaborates   3
#   same_topic   3
```

| Flag | Default | Meaning |
|------|---------|---------|
| `--model` | *(required)* | Ollama generate model for classification (e.g. `gemma4:12b`) |
| `--llm-url` | `http://localhost:11434/api/generate` | generate endpoint |
| `--k` | `5` | neighbors per doc considered as candidates |
| `--min-confidence` | `0.5` | drop proposed edges below this confidence |
| `--limit` | `0` | stop after classifying this many pairs (0 = all) |
| `--rebuild-knn` | `true` | rebuild the kNN geometry before digesting |

A pass is **incremental and resumable**: each classified pair is stamped with a
content fingerprint, so re-running skips unchanged pairs (edit a doc and only its
pairs are re-asked). Transient LLM failures stay unstamped and retry next pass.

### graph

Render one doc's typed relations as chains — structure instead of prose:

```bash
distill graph DOC-7
# DOC-7  Architecture (roadmap)
#   → elaborates   → DOC-1 distill-docs  [proposed, conf 0.95]
#       "Document B provides a detailed architectural breakdown ..."

distill graph DOC-7 --json   # {"slug":"DOC-7","relations":[...]}
```

### distilld (daemon)

`distilld` loops the same digester on an interval, keeping the graph warm as
documents change (steady-state passes are near-free — only dirty pairs cost an
LLM call):

```bash
distilld --db .knowledge/docs.sqlite --model gemma4:12b --interval 5m
distilld --model gemma4:12b --interval 0   # one pass, then exit (= distill digest)
```

Edges are **proposed**, not law: the LLM suggests, and confirming the
irreversible ones (`supersedes`) is left to policy/human review (the `status`
column). Run `distill eval` after a digest to confirm retrieval didn't regress.

---

## Quick reference

```bash
# Build (part of build.ps1 / build.sh)
go build -o distill.exe ./cmd/distill

# One-time setup
distill --db .knowledge/docs.sqlite init

# Add a manual note
distill add --title "..." --content "..." --type general

# Ingest a doc, chunked
distill add --file README.md --type general

# Crawl a documentation site
distill add --url https://pkg.go.dev/net/http --max-pages 200

# Search (defaults to hybrid)
distill search --query "your question" --limit 5

# Build the L2 typed knowledge graph (needs vectors + a generate model)
distill digest --model gemma4:12b

# Inspect one doc's typed relations
distill graph DOC-7

# Check size
distill count
```
