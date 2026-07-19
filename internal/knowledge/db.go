package knowledge

import (
	"database/sql"
	_ "modernc.org/sqlite"
)

const schema = `
PRAGMA journal_mode=WAL;
PRAGMA foreign_keys=ON;

-- author/role_tags/source_version are generated from metadata (TZ FR-3): the
-- JSON blob stays the single source of truth (one write path, no dual-write
-- drift), while these give queryable, indexable SQL columns over the fields
-- that actually need filtering (role_tags for FR-9 context(role), etc).
CREATE TABLE IF NOT EXISTS docs (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    title          TEXT    NOT NULL,
    content        TEXT    NOT NULL,
    type           TEXT    NOT NULL DEFAULT 'general',
    created_at     INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    metadata       TEXT    NOT NULL DEFAULT '{}',
    author         TEXT    GENERATED ALWAYS AS (json_extract(metadata, '$.author')) VIRTUAL,
    role_tags      TEXT    GENERATED ALWAYS AS (json_extract(metadata, '$.role_tags')) VIRTUAL,
    source_version TEXT    GENERATED ALWAYS AS (json_extract(metadata, '$.source_version')) VIRTUAL,
    -- ranking/curation signals (Stage 1): numeric priority, topic facet,
    -- authoritative flag, and the id of a doc this one supersedes.
    priority       REAL    GENERATED ALWAYS AS (json_extract(metadata, '$.priority')) VIRTUAL,
    topic          TEXT    GENERATED ALWAYS AS (json_extract(metadata, '$.topic')) VIRTUAL,
    pinned         INTEGER GENERATED ALWAYS AS (json_extract(metadata, '$.pinned')) VIRTUAL,
    supersedes     INTEGER GENERATED ALWAYS AS (json_extract(metadata, '$.supersedes')) VIRTUAL
);
-- Indexes on author/role_tags/source_version are created by
-- migrateMetadataColumns below, not here: on a docs table that predates these
-- columns, an unconditional CREATE INDEX in this same multi-statement exec
-- would run before the migration adds them and fail with "no such column".

CREATE VIRTUAL TABLE IF NOT EXISTS docs_fts USING fts5(
    title,
    content,
    content='docs',
    content_rowid='id'
);

-- Read-only view over the FTS5 index exposing (term, doc-frequency, count).
-- Free from the existing index; carried into releases by VACUUM INTO. Powers
-- Suggest() — "which terms actually exist to search for".
CREATE VIRTUAL TABLE IF NOT EXISTS docs_vocab USING fts5vocab('docs_fts', 'row');

CREATE TABLE IF NOT EXISTS docs_vec (
    doc_id    INTEGER PRIMARY KEY REFERENCES docs(id) ON DELETE CASCADE,
    dim       INTEGER NOT NULL,
    embedding BLOB    NOT NULL
);

-- settings is a per-corpus key/value store for ingest defaults that must be
-- constant across every ingest (chunk size/overlap, OCR strip glyph) plus batch
-- defaults (type, role_tags, author, source_version). Setting them once here —
-- instead of repeating flags per command — keeps the index homogeneous; an
-- explicit CLI flag still overrides per call. Rides into releases via VACUUM INTO
-- (provenance: how this corpus was formatted).
CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);

-- edges is the knowledge-connectivity graph (L1/L2). Stage 1 populates
-- kind='knn' edges deterministically from vector cosine similarity; the L2
-- digester (Stage 2) adds typed edges (supersedes/contradicts/...) with
-- provenance: weight = confidence, status = proposed|confirmed, rationale = the
-- LLM's one-line reason, model = which model proposed it, updated_at = when. No
-- cross-row FK: edges are a regenerable projection of docs, rebuilt not
-- integrity-enforced. status lets policy/humans confirm irreversible edges
-- (supersedes) before ranking trusts them; knn edges are 'confirmed' by nature.
CREATE TABLE IF NOT EXISTS edges (
    src        INTEGER NOT NULL,
    dst        INTEGER NOT NULL,
    weight     REAL    NOT NULL DEFAULT 0,
    kind       TEXT    NOT NULL DEFAULT 'knn',
    status     TEXT    NOT NULL DEFAULT 'confirmed',
    rationale  TEXT    NOT NULL DEFAULT '',
    model      TEXT    NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (src, dst, kind)
);
CREATE INDEX IF NOT EXISTS edges_src_idx ON edges(src, kind);
CREATE INDEX IF NOT EXISTS edges_dst_idx ON edges(dst, kind);

-- digest_state is the L2 digester's dirty-marking / resume ledger: one row per
-- candidate (src,dst) pair the digester has already classified, stamped with a
-- fingerprint of both docs' content. On the next pass, a pair whose fingerprint
-- still matches is skipped (incremental, resumable); an edited doc changes the
-- fingerprint and re-dirties every pair it touches. Not integrity-enforced —
-- like edges, a regenerable projection of docs.
CREATE TABLE IF NOT EXISTS digest_state (
    src         INTEGER NOT NULL,
    dst         INTEGER NOT NULL,
    fingerprint TEXT    NOT NULL,
    digested_at INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (src, dst)
);

CREATE TRIGGER IF NOT EXISTS docs_fts_ai AFTER INSERT ON docs BEGIN
    INSERT INTO docs_fts(rowid, title, content)
    VALUES (new.id, new.title, new.content);
END;

CREATE TRIGGER IF NOT EXISTS docs_fts_ad AFTER DELETE ON docs BEGIN
    INSERT INTO docs_fts(docs_fts, rowid, title, content)
    VALUES ('delete', old.id, old.title, old.content);
END;

CREATE TRIGGER IF NOT EXISTS docs_fts_au AFTER UPDATE ON docs BEGIN
    INSERT INTO docs_fts(docs_fts, rowid, title, content)
    VALUES ('delete', old.id, old.title, old.content);
    INSERT INTO docs_fts(rowid, title, content)
    VALUES (new.id, new.title, new.content);
END;
`

// Open opens (or creates) a knowledge base at path, applies the schema, and
// migrates any pre-existing docs table (created before FR-3's generated
// columns existed) to add them.
func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err = db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	if err = migrateMetadataColumns(db); err != nil {
		db.Close()
		return nil, err
	}
	if err = migrateEdgeColumns(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// edgeProvenanceColumns are the Stage-2 columns added to a pre-existing edges
// table (one created before the L2 digester's provenance fields existed).
var edgeProvenanceColumns = []struct{ name, decl string }{
	{"status", "TEXT NOT NULL DEFAULT 'confirmed'"},
	{"rationale", "TEXT NOT NULL DEFAULT ''"},
	{"model", "TEXT NOT NULL DEFAULT ''"},
	{"updated_at", "INTEGER NOT NULL DEFAULT 0"},
}

// migrateEdgeColumns adds the provenance columns to an edges table that predates
// them. edges has no generated columns, so plain table_info is sufficient (and
// the DEFAULTs backfill existing knn rows to a sane 'confirmed'/empty state).
func migrateEdgeColumns(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(edges)`)
	if err != nil {
		return err
	}
	existing := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull int
		var dflt sql.NullString
		var pk int
		if err = rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		existing[name] = true
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, c := range edgeProvenanceColumns {
		if existing[c.name] {
			continue
		}
		if _, err = db.Exec(`ALTER TABLE edges ADD COLUMN ` + c.name + ` ` + c.decl); err != nil {
			return err
		}
	}
	return nil
}

// metadataGeneratedColumns are the FR-3 columns generated from docs.metadata.
// CREATE TABLE IF NOT EXISTS is a no-op against an already-existing docs
// table, so a store created before these columns existed needs them added
// via ALTER TABLE instead.
var metadataGeneratedColumns = []struct{ name, expr, sqltype string }{
	{"author", "json_extract(metadata, '$.author')", "TEXT"},
	{"role_tags", "json_extract(metadata, '$.role_tags')", "TEXT"},
	{"source_version", "json_extract(metadata, '$.source_version')", "TEXT"},
	{"priority", "json_extract(metadata, '$.priority')", "REAL"},
	{"topic", "json_extract(metadata, '$.topic')", "TEXT"},
	{"pinned", "json_extract(metadata, '$.pinned')", "INTEGER"},
	{"supersedes", "json_extract(metadata, '$.supersedes')", "INTEGER"},
}

func migrateMetadataColumns(db *sql.DB) error {
	// table_xinfo, not table_info: plain table_info omits generated columns
	// entirely, so it would report author/role_tags/source_version as always
	// missing and re-add them on every open.
	rows, err := db.Query(`PRAGMA table_xinfo(docs)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	existing := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull int
		var dflt sql.NullString
		var pk, hidden int
		if err = rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk, &hidden); err != nil {
			return err
		}
		existing[name] = true
	}
	if err = rows.Err(); err != nil {
		return err
	}

	for _, c := range metadataGeneratedColumns {
		if !existing[c.name] {
			if _, err = db.Exec(
				`ALTER TABLE docs ADD COLUMN ` + c.name + ` ` + c.sqltype + ` GENERATED ALWAYS AS (` + c.expr + `) VIRTUAL`,
			); err != nil {
				return err
			}
		}
		// Idempotent and cheap either way: covers both a freshly created
		// table (column existed, index did not yet) and a just-migrated one.
		if _, err = db.Exec(
			`CREATE INDEX IF NOT EXISTS docs_` + c.name + `_idx ON docs(` + c.name + `)`,
		); err != nil {
			return err
		}
	}
	return nil
}
