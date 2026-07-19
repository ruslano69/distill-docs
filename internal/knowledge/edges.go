package knowledge

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
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

// RelationView is one typed relation resolved for display: the edge plus its
// target doc's slug, ready to render as text or JSON. This is the shared shape
// behind the graph-response view — `distill graph`, `distill-server graph`,
// the MCP `graph` tool, and the HTTP `/graph` endpoint all render the same
// data; before RelationsView existed each of those four rebuilt it by hand.
type RelationView struct {
	Kind        string
	Status      string
	TargetSlug  string
	TargetTitle string
	Rationale   string
	Model       string
	Confidence  float64
}

// ViewRelations resolves each edge's target to a display-ready slug/title —
// split out from RelationsView so a caller that needs to distinguish "subject
// doc not found" (404) from "edge query failed" (500), like the HTTP /graph
// endpoint, can still share this step instead of rebuilding its own
// DocByID-per-edge loop. Resolves every distinct target in one batch query
// rather than one DocByID call per edge (an N+1 pattern with a real if small
// cost even in-process — see relationsFor for the single-source JOIN
// equivalent this can't use directly, since edges here may span multiple
// source docs).
func ViewRelations(db *sql.DB, edges []Edge) []RelationView {
	views := make([]RelationView, 0, len(edges))
	if len(edges) == 0 {
		return views
	}
	targets := batchLoadDocs(db, uniqueDstIDs(edges))
	for _, e := range edges {
		// A target whose doc was deleted after the edge was written (or the
		// batch query itself failed) falls back to a bare "id-N" label rather
		// than an empty/wrong slug.
		slug, title := fmt.Sprintf("id-%d", e.Dst), ""
		if dst, ok := targets[e.Dst]; ok {
			slug, title = dst.Slug(), dst.Title
		}
		views = append(views, RelationView{
			Kind: e.Kind, Status: e.Status, TargetSlug: slug, TargetTitle: title,
			Rationale: e.Rationale, Model: e.Model, Confidence: e.Weight,
		})
	}
	return views
}

func uniqueDstIDs(edges []Edge) []int64 {
	ids := make([]int64, 0, len(edges))
	seen := make(map[int64]bool, len(edges))
	for _, e := range edges {
		if !seen[e.Dst] {
			seen[e.Dst] = true
			ids = append(ids, e.Dst)
		}
	}
	return ids
}

// batchLoadDocs fetches every doc in ids with a single `IN (...)` query,
// returning a partial (possibly empty) map on failure — callers degrade
// gracefully (a missing entry means "not found or query failed", the same
// fallback DocByID's per-call error already meant before this batched).
func batchLoadDocs(db *sql.DB, ids []int64) map[int64]Doc {
	out := make(map[int64]Doc, len(ids))
	if len(ids) == 0 {
		return out
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := db.Query(`SELECT id, type, title FROM docs WHERE id IN (`+placeholders+`)`, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var d Doc
		if rows.Scan(&d.ID, &d.Type, &d.Title) != nil {
			continue
		}
		out[d.ID] = d
	}
	return out
}

// RelationsView loads doc id and its typed L2 relations, resolving each
// edge's target to a slug in the same call — the convenience path for callers
// (CLI, MCP) that don't need to distinguish which of the two lookups failed.
func RelationsView(db *sql.DB, id int64, limit int) (Doc, []RelationView, error) {
	doc, err := DocByID(db, id)
	if err != nil {
		return Doc{}, nil, err
	}
	edges, err := TypedNeighbors(db, id, limit)
	if err != nil {
		return doc, nil, err
	}
	return doc, ViewRelations(db, edges), nil
}

// relationsFor returns the typed (non-knn) edges incident to doc id — both
// outgoing (id is subject) and incoming (id is object) — strongest first,
// oriented relative to id and with the other end's slug resolved in the same
// query. This is the read primitive behind Stage-3 graph-aware retrieval: it
// lets Search annotate a hit with "supersedes X" / "superseded by Y" without a
// second round-trip per neighbor.
func relationsFor(db *sql.DB, id int64, limit int) ([]Relation, error) {
	if limit <= 0 {
		limit = 8
	}
	rows, err := db.Query(
		`SELECT e.kind, e.weight, e.status,
		        CASE WHEN e.src = ? THEN 1 ELSE 0 END AS outgoing,
		        d.id, d.type
		   FROM edges e
		   JOIN docs d ON d.id = CASE WHEN e.src = ? THEN e.dst ELSE e.src END
		  WHERE (e.src = ? OR e.dst = ?) AND e.kind != 'knn'
		  ORDER BY e.weight DESC
		  LIMIT ?`,
		id, id, id, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Relation
	for rows.Next() {
		var rel Relation
		var outgoing int
		var targetType string
		if err := rows.Scan(&rel.Kind, &rel.Weight, &rel.Status, &outgoing, &rel.TargetID, &targetType); err != nil {
			return nil, err
		}
		rel.Outgoing = outgoing == 1
		rel.TargetSlug = Doc{ID: rel.TargetID, Type: targetType}.Slug()
		out = append(out, rel)
	}
	return out, rows.Err()
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
