package knowledge

import (
	"database/sql"
	"sort"
)

// Edge is one directed link in the knowledge-connectivity graph.
type Edge struct {
	Src, Dst  int64
	Weight    float64 // cosine similarity for kind="knn"; LLM confidence for typed edges
	Kind      string  // "knn" (L1 geometry) | supersedes|contradicts|... (L2 typed)
	Status    string  // "confirmed" (knn / human-approved) | "proposed" (LLM, awaiting confirm)
	Rationale string  // L2 only: the digester's one-line reason for the edge
	Model     string  // L2 only: the model that proposed it
	UpdatedAt int64   // unix seconds the edge was last written (L2)
}

// BuildKNNEdges recomputes the deterministic L1 geometry graph: for every doc
// that has a stored vector, its top-k cosine-nearest neighbors become
// kind="knn" edges (weight = cosine similarity). This is the "compile the
// geometric graph from L0" step — regenerable and idempotent (it replaces the
// previous knn edges wholesale). O(n^2), fine at micro-scale. Returns the edge
// count written.
//
// The L2 digester (later) enriches this anonymous geometry with typed edges
// (supersedes/contradicts/...) under different kinds, leaving the knn layer
// intact.
func BuildKNNEdges(db *sql.DB, k int) (int, error) {
	if k <= 0 {
		k = 5
	}

	rows, err := db.Query(`SELECT doc_id, embedding FROM docs_vec`)
	if err != nil {
		return 0, err
	}
	type vec struct {
		id int64
		v  []float32
	}
	var vs []vec
	for rows.Next() {
		var id int64
		var blob []byte
		if err := rows.Scan(&id, &blob); err != nil {
			rows.Close()
			return 0, err
		}
		vs = append(vs, vec{id, blobToFloat32Slice(blob)})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM edges WHERE kind='knn'`); err != nil {
		return 0, err
	}
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO edges(src,dst,weight,kind) VALUES(?,?,?,'knn')`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	type nb struct {
		id  int64
		sim float64
	}
	count := 0
	for i := range vs {
		nbs := make([]nb, 0, len(vs)-1)
		for j := range vs {
			if i == j {
				continue
			}
			nbs = append(nbs, nb{vs[j].id, 1 - cosineDistance(vs[i].v, vs[j].v)})
		}
		sort.Slice(nbs, func(a, b int) bool { return nbs[a].sim > nbs[b].sim })
		if len(nbs) > k {
			nbs = nbs[:k]
		}
		for _, n := range nbs {
			if _, err := stmt.Exec(vs[i].id, n.id, n.sim); err != nil {
				return 0, err
			}
			count++
		}
	}
	if err := tx.Commit(); err != nil {
		return count, err
	}
	return count, nil
}

// Neighbors returns a doc's outgoing edges, strongest first, optionally scoped
// to one kind ("" = all kinds). This is the read primitive behind
// "nearest knowledge" / graph responses.
func Neighbors(db *sql.DB, id int64, kind string, limit int) ([]Edge, error) {
	if limit <= 0 {
		limit = 10
	}
	q := `SELECT src, dst, weight, kind, status, rationale, model, updated_at FROM edges WHERE src = ?`
	args := []any{id}
	if kind != "" {
		q += " AND kind = ?"
		args = append(args, kind)
	}
	q += " ORDER BY weight DESC LIMIT ?"
	args = append(args, limit)

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEdges(rows)
}

func scanEdges(rows *sql.Rows) ([]Edge, error) {
	var out []Edge
	for rows.Next() {
		var e Edge
		if err := rows.Scan(&e.Src, &e.Dst, &e.Weight, &e.Kind,
			&e.Status, &e.Rationale, &e.Model, &e.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// TypedNeighbors returns a doc's non-knn (typed L2) edges, strongest first —
// the semantic relations the digester found, excluding the anonymous kNN
// geometry. This is what graph-response mode renders as relation chains.
func TypedNeighbors(db *sql.DB, id int64, limit int) ([]Edge, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := db.Query(
		`SELECT src, dst, weight, kind, status, rationale, model, updated_at
		   FROM edges WHERE src = ? AND kind != 'knn' ORDER BY weight DESC LIMIT ?`,
		id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEdges(rows)
}

// UpsertTypedEdge writes (or replaces) one typed L2 edge with its provenance,
// via the given transaction. knn edges are written by BuildKNNEdges; this is the
// digester's write path. Replacing on the (src,dst,kind) key makes a re-digest
// of the same relation idempotent.
func UpsertTypedEdge(tx *sql.Tx, e Edge) error {
	_, err := tx.Exec(
		`INSERT OR REPLACE INTO edges(src,dst,weight,kind,status,rationale,model,updated_at)
		 VALUES(?,?,?,?,?,?,?,?)`,
		e.Src, e.Dst, e.Weight, e.Kind, e.Status, e.Rationale, e.Model, e.UpdatedAt)
	return err
}
