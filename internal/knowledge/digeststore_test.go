package knowledge

import (
	"testing"
	"time"
)

func TestDocByID(t *testing.T) {
	db := openDB(t)
	id, _ := Add(db, "Auth", "how oauth tokens work", "spec", `{"topic":"auth","priority":2.5,"pinned":1,"supersedes":7}`, nil)

	d, err := DocByID(db, id)
	if err != nil {
		t.Fatalf("DocByID: %v", err)
	}
	if d.Title != "Auth" || d.Type != "spec" || d.Content != "how oauth tokens work" {
		t.Errorf("core fields wrong: %+v", d)
	}
	if d.Topic != "auth" || d.Priority != 2.5 || !d.Pinned || d.Supersedes != 7 {
		t.Errorf("generated columns not loaded: topic=%q pri=%v pin=%v sup=%d", d.Topic, d.Priority, d.Pinned, d.Supersedes)
	}
}

func TestPairFingerprint_ChangesWithContent(t *testing.T) {
	a := Doc{Content: "alpha"}
	b := Doc{Content: "beta"}
	base := PairFingerprint(a, b)
	if PairFingerprint(a, b) != base {
		t.Error("fingerprint not deterministic")
	}
	if PairFingerprint(b, a) == base {
		t.Error("fingerprint should be order-sensitive (directed pair)")
	}
	if PairFingerprint(a, Doc{Content: "beta!"}) == base {
		t.Error("editing dst content must change the fingerprint")
	}
}

func TestDigestState_Roundtrip(t *testing.T) {
	db := openDB(t)
	if _, ok, _ := DigestFingerprint(db, 1, 2); ok {
		t.Fatal("empty ledger should report no fingerprint")
	}

	tx, _ := db.Begin()
	if err := MarkDigested(tx, 1, 2, "fp-abc", time.Now().Unix()); err != nil {
		t.Fatalf("MarkDigested: %v", err)
	}
	tx.Commit()

	fp, ok, err := DigestFingerprint(db, 1, 2)
	if err != nil || !ok || fp != "fp-abc" {
		t.Fatalf("want fp-abc, got fp=%q ok=%v err=%v", fp, ok, err)
	}

	// Re-mark with a new fingerprint (content changed) → overwrites.
	tx2, _ := db.Begin()
	MarkDigested(tx2, 1, 2, "fp-xyz", time.Now().Unix())
	tx2.Commit()
	fp, _, _ = DigestFingerprint(db, 1, 2)
	if fp != "fp-xyz" {
		t.Errorf("re-mark should overwrite, got %q", fp)
	}
}

func TestUpsertTypedEdge_And_TypedNeighbors(t *testing.T) {
	db := openDB(t)
	now := time.Now().Unix()

	// A knn edge (should be excluded from TypedNeighbors) plus two typed edges.
	tx, _ := db.Begin()
	UpsertTypedEdge(tx, Edge{Src: 1, Dst: 2, Weight: 0.9, Kind: "knn", Status: "confirmed", UpdatedAt: now})
	UpsertTypedEdge(tx, Edge{Src: 1, Dst: 3, Weight: 0.8, Kind: "supersedes", Status: "proposed", Rationale: "newer spec", Model: "gemma4:12b", UpdatedAt: now})
	UpsertTypedEdge(tx, Edge{Src: 1, Dst: 4, Weight: 0.6, Kind: "elaborates", Status: "proposed", Rationale: "adds detail", Model: "gemma4:12b", UpdatedAt: now})
	tx.Commit()

	typed, err := TypedNeighbors(db, 1, 10)
	if err != nil {
		t.Fatalf("TypedNeighbors: %v", err)
	}
	if len(typed) != 2 {
		t.Fatalf("want 2 typed edges (knn excluded), got %d", len(typed))
	}
	// Strongest first.
	if typed[0].Kind != "supersedes" || typed[0].Dst != 3 {
		t.Errorf("want supersedes→3 first, got %+v", typed[0])
	}
	if typed[0].Rationale != "newer spec" || typed[0].Model != "gemma4:12b" || typed[0].Status != "proposed" {
		t.Errorf("provenance not persisted: %+v", typed[0])
	}

	// Upsert same (src,dst,kind) → replace, not duplicate.
	tx2, _ := db.Begin()
	UpsertTypedEdge(tx2, Edge{Src: 1, Dst: 3, Weight: 0.95, Kind: "supersedes", Status: "confirmed", Rationale: "human ok", Model: "gemma4:12b", UpdatedAt: now})
	tx2.Commit()
	typed, _ = TypedNeighbors(db, 1, 10)
	if len(typed) != 2 {
		t.Fatalf("upsert should replace, got %d edges", len(typed))
	}
	for _, e := range typed {
		if e.Dst == 3 && (e.Status != "confirmed" || e.Weight != 0.95) {
			t.Errorf("edge not replaced: %+v", e)
		}
	}
}

func TestMigrateEdgeColumns_BackfillsOldTable(t *testing.T) {
	db := openDB(t)
	// Simulate a pre-Stage-2 edges table: drop provenance columns by recreating
	// the table without them, then re-run the migration.
	if _, err := db.Exec(`DROP TABLE edges`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE edges (src INTEGER NOT NULL, dst INTEGER NOT NULL, weight REAL NOT NULL DEFAULT 0, kind TEXT NOT NULL DEFAULT 'knn', PRIMARY KEY(src,dst,kind))`); err != nil {
		t.Fatal(err)
	}
	db.Exec(`INSERT INTO edges(src,dst,weight,kind) VALUES(1,2,0.5,'knn')`)

	if err := migrateEdgeColumns(db); err != nil {
		t.Fatalf("migrateEdgeColumns: %v", err)
	}
	// Old row must survive and backfill to sane defaults.
	var status, model string
	var updated int64
	if err := db.QueryRow(`SELECT status, model, updated_at FROM edges WHERE src=1 AND dst=2`).Scan(&status, &model, &updated); err != nil {
		t.Fatalf("post-migration query: %v", err)
	}
	if status != "confirmed" || model != "" || updated != 0 {
		t.Errorf("backfill defaults wrong: status=%q model=%q updated=%d", status, model, updated)
	}
	// Idempotent second run.
	if err := migrateEdgeColumns(db); err != nil {
		t.Errorf("second migrate should be a no-op: %v", err)
	}
}
