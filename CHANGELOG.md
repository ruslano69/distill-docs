# Changelog

## Unreleased — CLI/graph duplication cleanup

A funcfinder-driven duplication audit of `cmd/distill` vs `cmd/distill-server`
found four near-identical function pairs (~330 lines, half of it copy-pasted)
plus a fourth and fifth copy of the same relation-rendering logic in the MCP
`graph` tool and HTTP `/graph` handler. Consolidated into shared packages so a
fix or feature now lands once instead of being replicated per call site:

- **New `internal/cliutil`** — `SplitPositionalFlag` (the "accept a bare
  positional before flag-parsing" trick used by both `graph` subcommands),
  `ParseEmbedding` (now error-returning instead of calling `fatalf` inline),
  `Truncate`.
- **`digest.PrintReport`** — the JSON/text digest-report renderer, shared by
  `distill digest` and `distill-server digest` (previously two ~50-line copies).
- **`knowledge.ApplySettingFlags` / `PrintSettings`** — the `config`
  write/print logic, shared by both `config` subcommands.
- **`knowledge.RelationsView` / `ViewRelations`** — load-doc-and-resolve-typed-
  relations, now shared by `distill graph`, `distill-server graph`, the MCP
  `graph` tool, and the HTTP `/graph` endpoint (previously **four** separate
  copies of the same DocByID-per-edge loop). `ViewRelations` is split out
  separately so the HTTP handler can still return 404 vs 500 for "doc not
  found" vs "edge query failed", which the unified fetch can't distinguish.
- **Fixed a discrepancy the audit surfaced**: `distill-server graph`'s text
  output never truncated a long target title or rationale (`distill graph`
  did); both now truncate consistently (title 40 chars, rationale 100).
- `cmd/distill/main.go` and `cmd/distill-server/main.go` shrink by ~150 net
  lines; behavior is unchanged (all existing tests pass, including the
  graph/digest/config end-to-end CLI, MCP, and HTTP tests) aside from the
  truncation fix above.

## v0.4.1 — 2026-07-19

### Digester direction/confidence fix

- **Fixed two related digester correctness bugs** found live against
  `gemma4:12b` on a real supersedes case: (1) the classify prompt could invert
  the `supersedes` direction (asserting the older doc replaces the newer one,
  so the `⚠ superseded` banner landed on the current spec instead of the
  obsolete one); (2) under loose `"json"` mode the model sometimes produced
  syntactically valid JSON that omitted `confidence` entirely — silently
  decoded as `0`, this discarded an otherwise-correct classification below any
  confidence threshold and permanently marked the pair digested.
- **Direction:** the classify prompt now requires content evidence for
  `supersedes` (explicit deprecation/replacement language), a worked example of
  correct direction encoding, and reasoning (`rationale`) ordered before
  `kind`/`direction` so the model commits to its evidence first. Each
  document's added-date is now included as a secondary (never primary) signal.
- **Confidence:** `internal/llm.Client.GenerateJSON` gained a `schema` param —
  passing a JSON Schema with a `required` list (Ollama's structured-output
  mode) grammar-constrains generation to include every required field, instead
  of the bare `"json"` string that only guarantees syntactic validity.
  `digest.Classify` now also treats a missing `confidence` on an asserted
  relation as an error (pair retried next pass), not a silent 0.
- Re-verified live on the exact scenario that surfaced the bug: the digester
  now correctly identifies the newer OAuth spec as superseding the older
  static-key spec, with confidence populated, and `search --graph` renders the
  `⚠ superseded` banner on the right document.

### Shared metadata binder + per-corpus settings

- **`internal/docmeta`** — one home for a document's provenance + Stage-1 ranking
  metadata (`Meta`, `JSON`, `Merge`, `RegisterRankFlags`). Both binaries now bind
  it from a single place instead of hand-rolling per command/tool.
- **`distill add` gains the structured flags** `--author`/`--topic`/`--priority`/
  `--pinned`/`--supersedes` (previously only reachable via a hand-written
  `--meta '{...}'`). `--meta` remains a raw base that the structured flags overlay
  (`--meta '{"source":"manual"}' --topic go` → `{"source":"manual","topic":"go"}`),
  so single-file `distill` and `distill-server` now share one metadata contract.
- **Per-corpus ingest settings** — a `settings` table in the knowledge DB stores
  ingest defaults so corpus invariants (`chunk-size`/`chunk-overlap`/`strip-runes`)
  and batch defaults (`type`/`role-tags`/`author`/`source-version`) are set once
  instead of repeated on every ingest — keeping the index homogeneous. New
  `distill config` / `distill-server config` set or print them; an explicit
  `add`/`ingest` flag still overrides per call (flag > setting > default, via
  `knowledge.FlagResolver`). Settings ride into releases through VACUUM INTO as
  provenance. Single-file `distill` file/web/single ingest now also applies
  index-time normalization (`--strip-runes`), matching `distill-server`.

## v0.4.0 — 2026-07-19 · Stage 3 + server parity (the graph, everywhere)

Wire the L2 knowledge graph into retrieval, and bring the whole graph surface
(build + read) to the agent-facing `distill-server` — until now it existed only
in the single-file `distill` CLI.

### Graph-aware retrieval (Stage 3)

- **`SearchOpts.GraphExpand N`** — annotate each returned `Result` with up to N
  typed relations incident to it (`Result.Relations`), oriented relative to the
  hit (`Relation.Outgoing`). `Result.Superseded()`/`Contradicted()` derive a
  "don't ground on this" signal from incoming supersedes/contradicts edges.
  Default 0 leaves output byte-identical to before (regression-safe).
- **`SearchOpts.Cluster`** — fold hits the graph marks `duplicates` of a
  higher-ranked result into that result (`Result.Folded`), de-crowding the top-N.
- **`distill search --graph N [--cluster]`** renders relation chains
  (`→ supersedes → SPEC-2`), a `⚠ superseded`/`⚠ contradicted` banner, and
  `⊕ folds SPEC-X (duplicates)`; JSON gains `relations`/`superseded`/
  `contradicted`/`folded`. Only returned docs are expanded (O(limit) lookups).

### distill-server parity

- **Read:** MCP `search` / HTTP `/search` gain `graph`/`cluster` params (inputs
  advertised in the tool schema); new `graph` MCP tool and `/graph?slug=` HTTP
  endpoint return a doc's typed relations from a release — mirror of `distill
  graph`. A typed edge seeded on the write-log survives `publish` (VACUUM INTO)
  into the immutable release and surfaces through all three interfaces.
- **Build:** `distill-server digest --model <ollama>` runs the L2 digester over
  the write-log, so `publish` bakes the graph into the release; `distill-server
  graph <SLUG>` inspects it. tools/list count 14 → 15.

- Refreshed `internal/knowledge/AGENTS.md` to the current package surface.

## v0.3.0 — 2026-07-18

The knowledge graph goes live, and any conversation becomes a knowledge base.
This release folds in Stage 1 (ranking + L1 geometry, first cut here) — no
`v0.2.0` was published separately.

### L2 typed knowledge graph (Stage 2)

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

### Session transcript ingest

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

### Stage 1 — ranking + L1 graph (first published here)

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
