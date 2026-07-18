# Changelog

## Unreleased — Stage 1 (ranking + L1 graph)

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
