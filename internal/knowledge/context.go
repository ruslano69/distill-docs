package knowledge

import "database/sql"

// ByRole is the FR-9 "context(role)" primitive: a view-filter over the same
// corpus by role tag — one database, different lenses, instead of a
// role-specific copy. role_tags is a comma-separated list (see metaJSON in
// cmd/distill-server), so membership is tested by wrapping both sides in
// delimiters ("," + role_tags + "," LIKE "%,role,%") rather than a plain
// substring match, which would also match "backend" inside "not-backend".
// Newest first: context is "what does my role need to know right now".
func ByRole(db *sql.DB, role string, limit int) ([]Doc, error) {
	return ByRoleTopic(db, role, "", limit)
}

// ByRoleTopic is ByRole additionally scoped to a topic facet (empty topic =
// role only). Two lenses over one corpus — role AND topic — so an agent pinned
// to (role, topic) gets exactly its slice.
func ByRoleTopic(db *sql.DB, role, topic string, limit int) ([]Doc, error) {
	q := `SELECT id, title, content, type, created_at, metadata,
	        COALESCE(author,''), COALESCE(role_tags,''), COALESCE(source_version,'')
	   FROM docs
	  WHERE (',' || role_tags || ',') LIKE ('%,' || ? || ',%')`
	args := []any{role}
	if topic != "" {
		q += " AND topic = ?"
		args = append(args, topic)
	}
	q += " ORDER BY created_at DESC, id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	return scanDocs(rows)
}
