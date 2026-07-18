# Changelog

## Unreleased — session transcript ingest

- **`distill add --session <transcript.jsonl>`** — ingest a Claude Code session
  transcript as durable, queryable memory. Each turn becomes one or more docs
  (long turns chunked), typed by **role** (`user`/`assistant`/`thinking`) so a
  single conversation yields three separable *slices of knowledge*: search only
  answers (`--filter-type assistant`), isolate reasoning (`--filter-type
  thinking`), or drop thinking entirely. `tool_use`/`tool_result` blocks and the
  opaque base64 thinking `signature` are excluded (actions/crypto, not content).
- **Historical timestamps** — new `knowledge.AddAt` inserts with an explicit
  `created_at`, so each turn's doc carries its **real** conversation time, not
  import time; recency ranking and chronology stay honest. Provenance (uuid, git
  branch, model) is stored in metadata; the branch doubles as a `topic` facet.
- `internal/knowledge/session.go` — transcript parser (`ParseSession`,
  polymorphic string/blocks content), per-turn chunking, and metadata rendering.

## v0.3.0 — Stage 2 (L2 typed knowledge graph)

Turn the anonymous L1 geometry into *knowledge*: a local LLM classifies which
near-neighbor pairs are actually related, and how. "Doc 42 is near doc 12"
becomes "SPEC-42 supersedes SPEC-12" — a typed, weighted, provenance-stamped
edge you can rank and reason over.

- **`internal/digest` — the L2 digester.** A tight relation taxonomy (~6, the
  Episteme lesson — not 201): `supersedes`, `contradicts`, `elaborates`,
  `depends_on`, `duplicates`, `same_topic`, plus first-class `none`.
  `Classify(a,b)` runs a temperature-0 JSON prompt and returns a validated kind,
  direction, clamped confidence, and one-line rationale. A pass draws candidates
  **only from the kNN geometry** (O(n·k) LLM calls, not O(n²)), collapses them to
  undirected pairs, and is **incremental and resumable**: a per-pair content
  fingerprint in `digest_state` means unchanged pairs are skipped, edited docs
  re-dirty their pairs, and transient LLM failures retry next pass. Edges land as
  `proposed` (policy/human confirms the irreversible ones); stale relations are
  cleared on re-digest.
- **`internal/llm`** — provider-agnostic Ollama `/api/generate` JSON client,
  disabled-when-unconfigured like `internal/embed`.
- **Edge provenance** — `edges` gains `status`/`rationale`/`model`/`updated_at`
  (migration backfills old tables); `weight` carries LLM confidence.
- **`distill digest --model <ollama>`** — one-shot pass (build kNN, classify,
  report per-kind tally). **`distill graph <SLUG>`** — graph-response view: a
  doc's typed relations as chains (`→ supersedes → SPEC-1 [proposed, conf 0.88]`),
  text or `--json`. **`distilld`** — background daemon looping the digester on an
  interval with graceful shutdown, sharing the engine with `distill digest`.

## v0.2.0 — Stage 1 (ranking + L1 graph)

The knowledge-layer foundation: turn retrieval into a re-scorable, filterable
index with a deterministic connectivity graph — the substrate the L2 digester
(distilld) will later enrich.

- **Ranking signals** — new indexed generated columns `priority`/`topic`/
  `pinned`/`supersedes`. A unified `Search(SearchOpts)` re-scores retrieval with
  a facet `Filter` (type/role/topic) and `RankOpts`: linear-window recency,
  numeric priority, per-type priority, pinned boost, role-affinity, and
  supersession drop. Zero options ⇒ order identical to plain retrieval (opt-in).
  Wired through every read surface: `distill`/`distill-server` CLI `search`, the
  MCP `search` tool, and the HTTP `/search` endpoint. `context` gained a topic
  facet.
- **Write side** — `ingest`/`record` (CLI + MCP) accept `--topic`/`--priority`/
  `--pinned`/`--supersedes`; structured JSON records carry them too.
- **Dense IDs** — every doc has a stable, legible slug (`SPEC-42`) for
  structured/graph responses.
- **L1 connectivity graph** — `BuildKNNEdges` compiles a deterministic
  cosine-kNN graph into an `edges` table (`Neighbors()` reads it); typed L2
  edges will layer on later.
- **`distill eval`** — score a golden query set (hit@k + MRR): the retrieval
  regression net to run before shipping an index and after any ranking change.

## v0.1.0 - 2026-07-18

Initial release. distill-docs is extracted from the
[funcfinder](https://github.com/ruslano69/funcfinder) monorepo (through v1.10.1)
into its own product, keeping full commit history for the moved code. Version
resets to 0.1.0 to avoid confusing users with the funcfinder version line.

- **`distill`** — knowledge-base CLI: SQLite FTS5 + vector hybrid search, regex,
  file/PDF/web (crawler) ingest, and auto-embedding via Ollama (`--embed-model`).
- **`distill-server`** — versioned truth server: immutable `truth-YYYY.MM`
  releases + `stable`/`testing`/`unstable` channels, provenance, retention,
  structured JSON ingest, funcfinder code-ingest, and three interfaces
  (MCP over stdio, HTTP/JSON, TCP), with zero-downtime channel hot-swap.
- Depends on `github.com/ruslano69/funcfinder` for code mapping via its public
  `analyze` API (funcfinder core stays out of this module's `internal`).

Feature history predating the split is preserved in the commit log (inherited
from funcfinder through v1.10.1).
