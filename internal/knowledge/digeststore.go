package knowledge

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
)

// DocByID loads one document (with its ranking/curation generated columns) by
// id. Returns sql.ErrNoRows if absent. This is the digester's read primitive:
// it needs a pair's full content and topic to classify their relationship.
func DocByID(db *sql.DB, id int64) (Doc, error) {
	var d Doc
	var topic sql.NullString
	var priority sql.NullFloat64
	var pinned, supersedes sql.NullInt64
	err := db.QueryRow(
		`SELECT id, title, content, type, created_at, metadata,
		        COALESCE(author,''), COALESCE(role_tags,''), COALESCE(source_version,''),
		        topic, priority, pinned, supersedes
		   FROM docs WHERE id = ?`, id).
		Scan(&d.ID, &d.Title, &d.Content, &d.Type, &d.CreatedAt, &d.Metadata,
			&d.Author, &d.RoleTags, &d.SourceVersion,
			&topic, &priority, &pinned, &supersedes)
	if err != nil {
		return d, err
	}
	d.Topic = topic.String
	d.Priority = priority.Float64
	d.Pinned = pinned.Int64 != 0
	d.Supersedes = supersedes.Int64
	return d, nil
}

// PairFingerprint is a content hash of an ordered doc pair. If either doc's
// content changes, the fingerprint changes and the digester re-classifies the
// pair — this is what makes a re-digest incremental rather than all-or-nothing.
func PairFingerprint(a, b Doc) string {
	h := sha256.New()
	h.Write([]byte(a.Content))
	h.Write([]byte{0})
	h.Write([]byte(b.Content))
	return hex.EncodeToString(h.Sum(nil))
}

// DigestFingerprint returns the recorded fingerprint for a (src,dst) pair and
// whether a row exists. A miss (ok=false) or a fingerprint mismatch means the
// pair is dirty and must be re-classified.
func DigestFingerprint(db *sql.DB, src, dst int64) (fp string, ok bool, err error) {
	err = db.QueryRow(`SELECT fingerprint FROM digest_state WHERE src=? AND dst=?`, src, dst).Scan(&fp)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return fp, true, nil
}

// MarkDigested records that (src,dst) was classified at fingerprint fp and time
// now (unix seconds), via the given transaction. Idempotent on the (src,dst)
// key. Written even when the classification produced no edge — "we looked and
// there's no relation" must also be remembered, or the digester re-asks the LLM
// the same dead-end pair on every pass.
func MarkDigested(tx *sql.Tx, src, dst int64, fp string, now int64) error {
	_, err := tx.Exec(
		`INSERT OR REPLACE INTO digest_state(src,dst,fingerprint,digested_at) VALUES(?,?,?,?)`,
		src, dst, fp, now)
	return err
}
