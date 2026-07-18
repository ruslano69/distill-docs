# internal/knowledge/

## Purpose

Persistent knowledge base backed by a single SQLite file. Combines FTS5 (BM25
keyword search) and cosine vector similarity with Reciprocal Rank Fusion for
hybrid retrieval, then re-scores results with a query-time ranking layer
(recency / priority / pinned / role / topic). Ingests `.txt` (charset-detected),
`.md`, `.pdf`, crawled documentation sites, structured JSON records, and Claude
Code session transcripts. On top of the raw corpus it maintains a
knowledge-connectivity graph: a deterministic L1 cosine-kNN layer and, via the
sibling `internal/digest` package, a typed L2 layer (supersedes / contradicts /
…). Designed for AI agents that accumulate and reason over durable, versioned
knowledge across sessions.

## Ownership

- All database logic, schema, search, ranking, and graph storage live here.
- Consumers: `cmd/distill/` (single-file CLI), `cmd/distilld/` (digester daemon),
  and — via `analyze`/`truth` layering — `cmd/distill-server/`. Consumers must
  not duplicate business logic.
- Siblings that build on this package: `internal/digest` (L2 typed-edge digester;
  reads/writes the `edges` and `digest_state` tables through helpers here) and
  `internal/llm` (Ollama generate client used by the digester).
- Embeddings are raw little-endian float32 BLOBs; cosine/L2 distance is computed
  in registered SQL functions (`vector.go`), no native extension required.

## Local Contracts

### Storage & lifecycle (`db.go`, `add.go`, `read.go`)
- `Open(path) (*sql.DB, error)` — open/create, apply schema, run migrations
  (metadata + edge provenance generated columns).
- `Add(db, title, content, type, metadata, embedding) (int64, error)` — insert;
  `created_at` defaults to now. `AddAt(..., createdAt int64)` — insert with an
  explicit historical timestamp (0 = now); used by session ingest so recency
  ranking stays honest.
- `Delete(db, id)`, `Count(db)`, `ReadRange(db, id, ctx)`, `ReadBySource(db, ver)`.

### Ingestion (`ingest.go`, `txt.go`, `md.go`, `pdf.go`, `web.go`, `records.go`, `session.go`)
- `IngestFile(path, ChunkOpts) ([]Chunk, error)` — dispatch by extension.
- `IngestURL(root, CrawlOpts, CrawlProgress)` (alias `IngestWeb`) — crawl a docs
  site into Chunks.
- `ParseRecords`/`ParseRecordsFile` + `AddRecords(db, []Record, embedFn)` —
  structured JSON records (changelog/task/decision) with per-record provenance.
- `ParseSession`/`ParseSessionBytes` → `ChunkSessionTurns(turns, ChunkOpts)` —
  Claude Code transcript ingest; each turn typed by role (`user`/`assistant`/
  `thinking`), provenance via `SessionTurn.Metadata()`.
- `ChunkOpts{MaxRunes, OverlapRunes}` (defaults 800 / 80); `NormalizeForIndex`.

### Retrieval & ranking (`search.go`, `doc.go`, `suggest.go`)
- `Search(db, SearchOpts) ([]Result, error)` — the unified entry point: retrieve
  via `Mode` (fts|vec|hybrid), RRF-fuse arms, drop superseded, then re-score by
  `RankOpts` and sort. `SearchOpts{Query, Embedding, Mode, Metric, Limit, Prefix,
  Filter, Rank, Now}`.
- `Filter{Type, Role, Topic}` (facet pre-filter, `where(prefix)`);
  `RankOpts{RecencyWindow, RecencyWeight, PriorityWeight, TypePriority,
  PinnedBoost, RoleAffinity, ExcludeSuperseded}` (all-zero ⇒ plain retrieval order).
- Primitives kept for direct use: `SearchFTS`, `SearchVec`, `SearchHybrid`,
  `SearchRegex`, `Enumerate`, `BuildFTSQuery`.
- `Doc.Slug()` → dense id (`SPEC-42`); `ParseSlugID(slug)`; `Result.Preview(n)`.
- `Suggest`/`SuggestRelativeTo` (corpus vocabulary via `fts5vocab`), `Weak` (IDF).

### Knowledge graph (`edges.go`, `digeststore.go`, `context.go`)
- `BuildKNNEdges(db, k) (int, error)` — deterministic L1 cosine-kNN graph
  (`kind='knn'`); idempotent, regenerable.
- `Neighbors(db, id, kind, limit)`, `TypedNeighbors(db, id, limit)` (non-knn L2),
  `UpsertTypedEdge(tx, Edge)` — the digester's write path.
- Digester storage: `DocByID`, `PairFingerprint`, `DigestFingerprint`,
  `MarkDigested` (incremental/resumable dirty-marking).
- `ByRole(db, role, limit)` / `ByRoleTopic(db, role, topic, limit)` —
  role/topic-scoped context.

### Governance & diff (`diff.go`, `eval.go`)
- `DiffDocs(a, b) Diff` — added/removed/changed between two corpora.
- `Evaluate(EvalSet, run)` / `LoadEvalSet(path)` — retrieval regression (hit@k, MRR).

- Schema applied idempotently on every `Open` (all `CREATE ... IF NOT EXISTS`);
  generated columns and edge provenance added by migration for pre-existing DBs.
- FTS index kept in sync via three SQL triggers (insert/delete/update on `docs`).

## Schema

```
docs         — id, title, content, type, created_at, metadata, +generated VIRTUAL
               columns from metadata: author, role_tags, source_version,
               priority(REAL), topic, pinned(INT), supersedes(INT)
docs_fts     — FTS5 virtual table (content='docs')
docs_vocab   — fts5vocab('docs_fts','row') — corpus term/frequency view
docs_vec     — doc_id, dim, embedding BLOB (float32 LE)
edges        — src, dst, weight, kind, status, rationale, model, updated_at
               (kind='knn' L1 geometry; typed kinds = L2 digester, 'proposed')
digest_state — src, dst, fingerprint, digested_at (digester resume ledger)
```

## Ingestion pipeline

```
IngestFile → ingestTXT / ingestMD / ingestPDF
               ↓
           docSection[]   (title + []paragraph)
               ↓
           sectionsToChunks  (chunk.go)
               ↓
           []Chunk  → caller calls Add()/AddAt() for each
```

- **TXT/MD/PDF/Web**: unchanged section-aware chunking (see `chunk.go`,
  `normalize.go`, `web.go`); greedy fill to `MaxRunes`, split at paragraph
  boundaries with `OverlapRunes` overlap.
- **Records/Session**: per-item metadata carried into the generated columns;
  session turns keep their real timestamp via `AddAt`.

## Knowledge-graph pipeline (L1 → L2)

```
docs_vec ──BuildKNNEdges(k)──▶ edges(kind='knn')            [deterministic, no LLM]
                                    │  candidate pairs
                                    ▼
                internal/digest.Run ──Classify(a,b)──▶ edges(typed, 'proposed')
                (uses internal/llm; digest_state dirty-marks)   +rationale/model
```

## Work Guidance

- Add a format: `ingestXYZ.go` + register the extension in `IngestFile`.
- Add a search mode/signal: implement in `search.go`; route through `Search`
  (the single re-scoring entry point) rather than adding parallel read paths.
- **Stage 3 note (graph-aware retrieval):** `Search` does *not* yet consume the
  graph — `TypedNeighbors`/`Neighbors` are read only by the `distill graph` CLI
  and the digester. Wiring edge traversal into `Search` (surface a hit's
  superseded/contradicting neighbors, cluster `same_topic`) is the open Stage-3
  work; the read primitives already exist here.
- Vector search is O(n) — fine to ~100k entries. Add an HNSW layer behind the
  API if scale demands.
- No CGO: the pure-Go `modernc.org/sqlite` driver is intentional.

## Verification

```bash
go test ./internal/knowledge/...
```
