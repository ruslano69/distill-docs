package knowledge

import (
	"database/sql"
	"testing"
)

// seedGraph builds three docs and typed edges: SPEC-1 supersedes SPEC-2, and
// NOTE-3 contradicts SPEC-1.
func seedGraph(t *testing.T) *sql.DB {
	t.Helper()
	db := openDB(t)
	Add(db, "old", "alpha shared token", "spec", "{}", nil)  // 1
	Add(db, "new", "alpha shared token", "spec", "{}", nil)  // 2
	Add(db, "note", "alpha shared token", "note", "{}", nil) // 3
	tx, _ := db.Begin()
	UpsertTypedEdge(tx, Edge{Src: 1, Dst: 2, Weight: 0.9, Kind: "supersedes", Status: "proposed"})
	UpsertTypedEdge(tx, Edge{Src: 3, Dst: 1, Weight: 0.8, Kind: "contradicts", Status: "proposed"})
	tx.Commit()
	return db
}

func resultByID(rs []Result, id int64) *Result {
	for i := range rs {
		if rs[i].ID == id {
			return &rs[i]
		}
	}
	return nil
}

func TestSearch_GraphExpandAnnotatesRelations(t *testing.T) {
	db := seedGraph(t)

	// GraphExpand off → no relations (identical to today).
	off, err := Search(db, SearchOpts{Query: "alpha", Mode: "fts", Prefix: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range off {
		if r.Relations != nil {
			t.Fatalf("GraphExpand=0 must leave Relations nil, got %+v", r.Relations)
		}
	}

	// GraphExpand on → hits annotated, oriented relative to each hit.
	on, err := Search(db, SearchOpts{Query: "alpha", Mode: "fts", Prefix: true, Limit: 10, GraphExpand: 8})
	if err != nil {
		t.Fatal(err)
	}

	// SPEC-1: outgoing supersedes→SPEC-2, incoming contradicts from NOTE-3.
	r1 := resultByID(on, 1)
	if r1 == nil || len(r1.Relations) != 2 {
		t.Fatalf("SPEC-1 want 2 relations, got %+v", r1)
	}
	var sawOutSupersedes, sawInContradicts bool
	for _, rel := range r1.Relations {
		if rel.Kind == "supersedes" && rel.Outgoing && rel.TargetSlug == "SPEC-2" {
			sawOutSupersedes = true
		}
		if rel.Kind == "contradicts" && !rel.Outgoing && rel.TargetSlug == "NOTE-3" {
			sawInContradicts = true
		}
	}
	if !sawOutSupersedes || !sawInContradicts {
		t.Errorf("SPEC-1 orientation wrong: %+v", r1.Relations)
	}
	if r1.Superseded() {
		t.Error("SPEC-1 is not superseded (its supersedes edge is outgoing)")
	}
	if !r1.Contradicted() {
		t.Error("SPEC-1 should report Contradicted() (incoming contradicts)")
	}

	// SPEC-2: incoming supersedes from SPEC-1 → obsolete.
	r2 := resultByID(on, 2)
	if r2 == nil || !r2.Superseded() {
		t.Fatalf("SPEC-2 should be Superseded(), got %+v", r2)
	}
	for _, rel := range r2.Relations {
		if rel.Kind == "supersedes" && (rel.Outgoing || rel.TargetSlug != "SPEC-1") {
			t.Errorf("SPEC-2 supersedes edge should be incoming from SPEC-1: %+v", rel)
		}
	}
}
