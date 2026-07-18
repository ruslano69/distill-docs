# Changelog

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
