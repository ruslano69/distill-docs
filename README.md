# distill-docs

**Distill primary information into knowledge.**

What you ingest is *primary information* — raw, append-only, allowed to go stale
and contradict itself. **Knowledge** is what a correctly filtered, grouped, and
connected index yields over it. distill-docs is the pipeline that does the
distilling: collect once, index many ways, serve versioned truth to agents and
teams.

Extracted from the [funcfinder](https://github.com/ruslano69/funcfinder)
monorepo (≤ v1.10.1) into its own product; it still depends on funcfinder's
public `analyze` API for code-ingest.

## Binaries

| Binary | Role |
|--------|------|
| `distill` | Knowledge-base CLI: FTS5 + vector hybrid search, file/PDF/web ingest, auto-embedding via Ollama |
| `distill-server` | Versioned truth server: immutable releases + channels, provenance, MCP + TCP/HTTP read-servers, funcfinder code-ingest |
| `distilld` | *(planned)* background digester — LLM-built knowledge-connectivity graph |

## Build

```bash
./build.sh          # or .\build.ps1 on Windows
```

## Quick start

```bash
# local knowledge base
./distill --db .knowledge/docs.sqlite init
./distill add --file README.md --embed-model qwen3-embedding:0.6b
./distill search --query "how do I ingest a site" --embed-model qwen3-embedding:0.6b

# versioned truth server
./distill-server --root .distill ingest --title "Auth" --content "OAuth2 device flow" --type spec
./distill-server --root .distill publish --name 2026.07 --channel stable
./distill-server --root .distill mcp        # MCP over stdio (agent interface)
./distill-server --root .distill serve-http # HTTP/JSON read-server
```

## Architecture (roadmap)

Three layers over one corpus:

- **L0 — primary information**: raw docs + JSON derivatives. Immutable, append-only.
- **L1 — deterministic index** (synchronous, no LLM): vectors, time, tags, hashes,
  cosine-kNN edges. Pure math.
- **L2 — knowledge graph** (async, LLM `distilld`): typed weighted edges —
  *supersedes / contradicts / elaborates / same-topic* — with provenance.

Query-time ranking (recency, priority, role/topic) sits on top. See
[`docs/distill-server/`](docs/distill-server/) for the truth-server spec.

## License

MIT — see [LICENSE](LICENSE).
