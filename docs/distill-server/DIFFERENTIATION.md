# distill-server — Differentiators

Status: draft v0.3 · 2026-07-17
Source: measured and prototyped during the design session (see `benchmarks/`,
`internal/truth`, `internal/knowledge`, `cmd/distill-server`).

This document complements §15 of the specification ("Related work"). That
section maps the layers and identifies who we do *not* compete with; this one
lists the concrete differentiating features and **the evidence behind each**.

---

## Positioning in one line

> **SQLite : Postgres = git : Confluence.**
> Onyx / Glean / Mintlify are "the organization's brain as a platform you
> deploy." distill-server is "git for truth": a versioned, file-based,
> reproducible artifact of a project's truth that you *commit alongside the code*.

This is not "a lighter-weight RAG platform" — that framing loses to incumbents'
Lite tiers — but a **different category**. The nine features below are what hold
that category together.

---

## 1. Reproducible grounding (immutable releases)

**What:** truth is published into an immutable, named release (`truth-2026.07`).
Pin to a release and you get an **identical result forever**, for as long as the
release is retained.

**Why it's a moat:** RAG platforms (Onyx, Glean) keep the index **live** — it
continuously re-indexes sources. Grounding against a moving target is **not
reproducible**: the same query returns different context tomorrow, an agent bug
can't be reproduced, and audit is impossible. Our release is frozen with a
`VACUUM INTO` snapshot.

**Evidence:** `TestReproducibility_ReleaseFrozenAfterMoreWrites` — writes to the
write-log after `publish` are **not visible** to the release; `unstable` sees
both. A point release (`2026.07.1`) does not mutate its predecessor; the consumer
opts in by channel.

Because a release is a file, **comparing two states of truth** becomes an
ordinary SQL join rather than a diff over a live index: `diff_releases(from, to)`
matches documents by id (id is stable across `VACUUM INTO`) and hashes
title+content+type+metadata, returning added/removed/changed. `freeze(release)`
provides an explicit "from here, only unstable" point before stabilization.
Neither operation is meaningful against a competitor's moving index.

**Evidence:** `TestDiffDocs` — added/removed/changed are correctly separated over
a synthetic pair of releases; `TestFreeze` — re-freezing, and freezing a
nonexistent release, are both rejected.

---

## 2. Zero-infra: one cgo-free binary plus a file

**What:** the entire server is a single binary and a set of SQLite files. No
Vespa, no Postgres, no Redis, no workers, no Docker/K8s/Helm.

**Why it's a moat:** Onyx is a cluster (Vespa + Postgres + Redis + workers).
Vector databases (Pinecone/Qdrant/Chroma) are infrastructure you must provision
and operate. We cross-compile for linux/darwin/windows with no toolchains
(`modernc.org/sqlite`, pure Go), and the truth lives **in the project's
repository**.

**Evidence:** the vector arm, FTS5, the HTTP server, and MCP all work with no
external dependency beyond Go modules; a release is a single `.sqlite` file that
can be copied to N nodes.

---

## 3. Zero-downtime release day (channel hot-swap)

**What:** read nodes switch to a new release atomically, under live load, with no
interruption (`atomic.Pointer` to the active release; the old one is closed after
draining).

**Why it's a moat:** a "release model" is worthless if changing versions means
downtime. For us it is a single atomic operation.

**Evidence (measured):** 64 connections, **500,000 requests**, and mid-load a
`publish 2026.07.1` + `set-channel stable` → **0 dial errors, 0 read errors**,
throughput unchanged (~40K RPS), with `>> hot-swapped 2026.07 → 2026.07.1` in the
log.

---

## 4. Agent-first (MCP-native), not chat-first

**What:** the primary interface is an **MCP server** (13 tools, strictly CQRS:
read-only grounding vs. the rewrite loop). The CLI is for humans and CI.

**Why it's a moat:** Onyx/Glean are **chat-first** — a UI for people, with agents
bolted on the side. We are a primitive for a swarm of agents:
`search/recall/suggest_terms` (grounding), `ingest/record/publish/set_channel`
(truth in).

**Evidence:** a full MCP handshake plus `tools/call` end-to-end; tool errors →
`isError` (the agent adapts), protocol errors → JSON-RPC `-32601`.

---

## 5. FTS vocabulary control — "the key and the eyes to search" ⭐

**What:** `suggest(prefix)` returns the **corpus's actual terms** with their
frequencies, for free, from the existing FTS index (`fts5vocab`). The agent
**sees what to search for** instead of guessing.

**Why this is the main moat:** no RAG platform exposes the *corpus vocabulary* as
a grounding primitive. They all offer either a search box or opaque embeddings.
Against the niche/legacy APIs this is built for, an agent **does not know** the
canonical terms and hallucinates — whereas `suggest` shows them directly,
including foreign-language and inflected forms.

**Evidence (measured):** the query `сортировка` returned **0 hits** (the corpus is
dominated by the forms `сортировки`/`сортируемого`/`сортировать`); `suggest
--prefix сорт` surfaces all 12 forms of the one root — Russian inflection
quantified, not guessed.

---

## 6. Honest grounding hygiene (corpus-aware signals)

A bundle of features competitors lack because they hide everything behind an
opaque embedding:

- **IDF weak-key** — flags a weak key (a high-frequency term that leads away from
  the answer). Corpus-relative: `the` has idf 0.14 (weak) vs. `sort` at idf 3.25
  (sharp), where a flat "df > 30" cutoff would wrongly reject the domain term
  `sort`.
- **Partition-relative IDF** — on a heterogeneous (multi-language / multi-source)
  corpus, global IDF is inflated by dilution. **Measured:** `данных` is idf 2.29
  `weak=false` globally → idf 1.33 `weak=true` within the Russian partition (the
  misclassification is corrected).
- **Numeric filter** — page/line numbers (`1`, `15`, `374`) rank high by frequency
  but are useless as keys; they are filtered out of suggestions (they remain in
  the index — searching for a literal value still works).
- **Index-time normalization** — always cleans unambiguous garbage (U+FFFD,
  C0/C1); a corpus-specific OCR artifact is handled via `--strip-runes`.
  **Measured:** the phantom `sortω` (count 53) merges back into `sort` (214→235),
  and 14K junk omegas disappear.

**Why it's a moat:** this is "transparent" grounding — you can see *why* a term is
good or bad, and an operator can fix it. With competing platforms, retrieval
quality is a black box.

---

## 7. BYO embeddings, plus the finding that "you often don't need a vector"

**What:** the server is provider-agnostic (BYO embeddings; `--embed-model` points
at any Ollama-compatible endpoint), and the default is `hybrid` with graceful
degradation to FTS.

**Why it's a moat:** competitors pull you into their embedding stack. We do not —
**and we measured when a vector is actually needed:**

- FTS is **strictly language-locked** (an EN query never hits RU chunks), so a
  vector is mandatory for cross-language retrieval;
- on a single-language canonical corpus, a large model finds the **same** chunks
  (no payoff); on hard or cross-language queries it finds **better** ones (payoff);
- the cheapest path is **agent rewrite → FTS5** (free, if the rewriter is
  competent — and the calling agent is; a weak local model is not).

The conclusion is not "buy our vector" but "FTS plus vocabulary control covers
the majority, and a 0.6B vector is cheap semantic insurance for the tail."

---

## 8. Governance over truth: provenance, role-scoped context

**What:** metadata (FR-3: `author` / `role_tags` / `source_version`) is not a JSON
blob but `GENERATED ... VIRTUAL` columns — indexed and queryable directly in SQL.
This unlocks two primitives without duplicating the index or standing up a
separate per-role database: `provenance(record_id)` — who entered a record, when,
and against which reference; and `context(role)` — the same corpus filtered by a
role tag (`,role_tags,` LIKE `%,role,%`, so `backend` does not match `backend2` or
`frontend-backend-liaison`).

**Why it's a moat:** RAG platforms either don't store author/role at all, or keep
them outside the search index — so "show me only what concerns backend" requires a
separate index or post-hoc filtering in the application. For us it is a single
`WHERE` on a column that already exists.

**Evidence:** `TestProvenance` — the author/time/source_ref trail is read back by
id; `TestByRole` / `TestByRole_Limit` — the filter does not match on substrings
and honors the limit, returning newer documents first.

---

## 9. Code as truth: the funcfinder compiler (spec FR-22, §7.4)

**What:** `publish --code-dir <path>` runs the funcfinder core over the given
source tree (all 15 languages, auto-detected per file, `.gitignore` respected) and
bakes it into the release as one document per file — a map of functions and types
(`name: start-end`), with `source_version` set to the tree's git commit SHA.
"Where is X defined" is answered by an exact index, not an approximate vector over
source blobs. A repeat `publish --code-dir` **replaces** the previous map rather
than accumulating stale snapshots — it is a regenerable build artifact, not human
truth that should compound.

**Why it's a moat:** no RAG platform compiles code into a structural index as part
of the release cycle — either code isn't indexed at all, or it's embedded as a
text blob (approximate, not cheap, and with no guarantee that "where is X defined"
won't surface a stale version of the function).

**Evidence:** a dogfood run of `publish --code-dir internal/` against funcfinder
itself — 74 files; `search --query BuildShardGraph` returned the exact file and
line range (`importresolver.go: 74-124`) on the first query. Re-publishing the
same tree again produced exactly 74 documents in the write-log (not 148),
confirming replacement rather than accumulation. An uncommitted tree is correctly
flagged with a warning (otherwise the `source_version` provenance would mislead).
Tests: `internal/codemap/codemap_test.go`.

**Boundary (stated honestly, as in spec §7.4):** this is the skeleton, not the
semantics — the "why" is still supplied by people (context, decisions,
docstrings). funcfinder is one ingest stage, not a replacement for the rest of the
truth.

---

## Summary: feature × us vs. neighbors

| Feature | distill-server | Onyx/Glean/Mintlify | Vector DBs |
|---|---|---|---|
| Grounding reproducibility | **pin to release = forever** | live index (target moves) | — |
| Deployment | **1 binary + file** | cluster (Vespa+PG+Redis) | infra service |
| Changing the truth version | **hot-swap, 0 downtime** | re-indexing | — |
| Center of gravity | **agent-first (MCP)** | chat-first | API primitive |
| Vocabulary as a primitive | **suggest + IDF** | no | no |
| Quality transparency | **IDF / partitions / normalization** | black box | black box |
| Embeddings | **BYO, often unneeded** | own stack | this *is* the product |
| Code as truth | **funcfinder compiler, part of release** | blob embedding | — |
| Feedback loop | **truth compounds** | read-mostly connectors | — |
| Release diff / role lens | **SQL join by id / indexed column** | no | — |

---

## Honesty boundary

The benchmark this was measured on (PureBasic + COBOL manuals) is
single-language and canonical — the "easy end," and it flatters FTS. On a truly
multi-language, dirty, inflected corpus, the balance shifts toward semantics and
toward larger multilingual embedders. The **direction** of these conclusions
carries over; the **balance point** is corpus-dependent. That is exactly why the
default is `hybrid`, not "FTS is enough."
