# Skill: distill — Project Knowledge Base

Use this skill when you need to persist notes, ingested documentation, or
findings across sessions, and later retrieve them by keyword, regex, or
vector similarity, instead of re-reading source files or re-deriving facts
each time.

---

## When to invoke

- "remember this for later" / "save this finding to the knowledge base"
- "what do we already know about X" / "search past notes for X"
- "ingest this doc/PDF/README into the knowledge base"
- "build a knowledge base for this project"
- any task requiring durable, queryable memory scoped to one project

---

## Mental model

One project = one SQLite file (default `.knowledge/docs.sqlite`). Three
backing tables: `docs` (source of truth), `docs_fts` (full-text index, kept
in sync via triggers), `docs_vec` (optional embedding per doc). For vectors,
either pass `--embed-model <ollama-model>` (auto-embeds at add + search) or
supply precomputed vectors via `--embedding`; without either, it runs FTS-only.

Full reference: [docs/DISTILL.md](../docs/DISTILL.md).

---

## Phase 1 — Init (once per project)

```bash
distill --db .knowledge/docs.sqlite init
```

Idempotent — safe to call at the start of every session.

---

## Phase 2 — Corpus setup (optional, once)

Some `add` flags are **corpus invariants** — how *this* base is chunked and
cleaned should be constant across every ingest, not decided per call. Set them
once instead of repeating on every `add`:

```bash
distill config --chunk-size 500 --strip-runes Ω --type manual --author ruslan
distill config   # no flags → print the current settings
```

An explicit flag on `add` still overrides the stored setting for that one
call (flag > setting > built-in default). Skip this phase entirely for casual
use — `add` works fine with just built-in defaults.

---

## Phase 3 — Add knowledge

### A single note or finding
```bash
distill add --title "<short title>" --content "<the actual text>" \
  --type general --json
```

Use `--type` to tag what kind of entry this is — `general`, `tool_usage`,
`error`, `scenario`, or any project-specific tag. It's free-form and only
used later for `--filter-type`.

### A whole file (chunked automatically)
```bash
distill add --file README.md --type general --json
distill add --file spec.pdf  --type general --json
```

Supports `.txt`, `.md`, `.pdf`. One call = one file — to ingest a directory,
loop over it:
```bash
for f in docs/*.md; do
  distill add --file "$f" --type general --json
done
```

**PDF note**: bad-OCR scans are rejected outright (`{"error":"bad_ocr","score":...}`,
exit code 2) rather than polluting the knowledge base — if you hit this, the
source PDF needs re-OCR'ing (e.g. `ocrmypdf`), not a retry.

### A documentation website (crawled)
```bash
distill add --url https://pkg.go.dev/net/http --max-pages 200 --json
```

Crawls a docs site into the knowledge base — stays on the same host + path
prefix (so a start URL of `…/net/http` won't wander into `…/os` or off-domain),
dedups identical/versioned pages, and extracts `<main>`/`<article>` text while
skipping nav/header/footer chrome. Use it to make an external library's docs
searchable offline. `--max-pages` (default 200) bounds the crawl.

### A past Claude Code session (transcript)
```bash
distill add --session ~/.claude/projects/<proj>/<id>.jsonl \
  --embed-model qwen3-embedding:0.6b --json
```

Turns a conversation into searchable memory. Each turn is typed by role —
`user`, `assistant`, `thinking` — so you can search a single slice: `search
--filter-type thinking` recalls *why* something was decided, `--filter-type
assistant` recalls *what* was answered. Real timestamps are preserved, so
`--recency-window` ranks recent turns higher.

---

## Phase 4 — Search

```bash
# Keyword search (fast, no embedding needed)
distill search --query "<keywords>" --mode fts --limit 5 --json

# Default: hybrid (FTS + vector if you have embeddings; degrades to FTS alone if not)
distill search --query "<keywords>" --json

# Structural/pattern match
distill search --query "<regex>" --mode regex --json

# Semantic search — auto-embed the query via Ollama (docs must have been
# added with the SAME --embed-model)
distill search --query "<natural language>" --mode vec --embed-model qwen3-embedding:0.6b --json

# Semantic search with a precomputed vector (BYO)
distill search --embedding "0.1,0.2,..." --mode vec --json

# Graph-aware: annotate hits with typed relations + fold duplicates (needs Phase 6)
distill search --query "<keywords>" --graph 4 --cluster --json
```

`--mode fts` (or the `hybrid` default without an embedding) covers the
overwhelming majority of "do we already know X" lookups. Reach for `regex`
when FTS tokenization won't match what you need (e.g. an exact error code).
Reach for `vec`/`hybrid` with embeddings only when you have a real embedding
model wired into the session — otherwise it's a no-op. `--graph N` warns when
a hit is `⚠ superseded`/`⚠ contradicted` by another doc — check this before
citing a hit as current truth; `--cluster` collapses near-duplicate hits so
the top-N isn't several copies of one fact.

---

## Phase 5 — Sanity check

```bash
distill count --json
```

Confirms the knowledge base isn't empty before relying on search results.

---

## Phase 6 — Knowledge graph (optional, needs a local LLM)

Once a base has vectors and enough related docs, build the **L2 typed graph** so
you can ask *how* documents relate, not just which are similar:

```bash
# Classify near-neighbor pairs into typed relations (supersedes/elaborates/...).
# Incremental & resumable — re-running only re-asks docs whose content changed.
distill digest --model gemma4:12b

# Read one doc's typed relations as chains (structure, not prose).
distill graph DOC-7 --json
```

Use this to surface supersession ("is this note obsoleted by a newer one?") and
contradictions before trusting a search hit. Edges are `proposed` until
confirmed. `distilld` runs the same pass continuously as a daemon.

---

## Golden Rule

```
init (once) → add (notes + files as you learn things) → search (before re-deriving anything)
```

Treat the knowledge base as cheaper than re-reading files or re-running
analysis: search it first, only fall back to fresh investigation (e.g. via
the `funcfinder` skill) when the knowledge base comes up empty — then add
what you find back in.

---

## Scaling up: distill-server + MCP

`distill` is one local SQLite file — perfect for a single agent's per-project
memory. When a **team of agents (and humans)** must ground against *the same*
knowledge, and that knowledge must be **versioned and reproducible**, graduate
to `distill-server`: the same hybrid-search engine, wrapped in a versioned
"truth server". Full spec: [docs/distill-server/](../docs/distill-server/).

**Mental model** — CQRS. Truth flows into a live write-log; `publish` snapshots
it into an immutable `truth-YYYY.MM` release; channels `stable`/`testing`/
`unstable` are atomic pointers at releases. An agent pins to a channel/release
and grounds against it — the same query returns the same answer forever, while
that release is retained (unlike a single mutable `distill` file).

**MCP is the primary agent interface.** The server speaks JSON-RPC 2.0 over
stdio (MCP protocol `2024-11-05`); register it once and its tools become
available to the agent — no hand-driving JSON-RPC:

```jsonc
// .mcp.json (or your Claude Code MCP config)
{
  "mcpServers": {
    "truth": { "command": "distill-server", "args": ["--root", ".distill", "mcp"] }
  }
}
```

**14 tools, split by side** (never ground and mutate through the same call):

- **Readonly (grounding)** — `search`, `recall`, `suggest_terms`, `context`
  (truth-under-role), `provenance` (who/when/against-which-release),
  `diff_releases`, `list_releases`, `channels`. Each takes an optional
  `release` so you ground against a pinned snapshot.
- **Rewrite (truth in)** — `ingest`, `record` (changelog/task/decision),
  `publish` (also `code_dir`: bake a funcfinder code map into the release —
  see the `funcfinder` skill), `freeze`, `prune` (retention), `set_channel`.

**Same operations from the CLI** for episodic/admin use and CI:
`distill-server --root .distill <ingest|publish|search|serve-http|…>`.
For a network read layer, `serve-http` / `serve` expose the readonly side over
HTTP/TCP with zero-downtime channel hot-swap.

**When to use which**: reach for single-file `distill` for your own scratch
memory; reach for `distill-server` when the answer must be shared, versioned,
and identical for every consumer.
