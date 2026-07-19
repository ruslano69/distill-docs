package knowledge

import "testing"

func TestBuildKNNEdges_And_Neighbors(t *testing.T) {
	db := openDB(t)
	// A and B are near-parallel (high cosine); C is orthogonal.
	a, _ := Add(db, "A", "a", "note", "{}", []float32{1, 0, 0})
	b, _ := Add(db, "B", "b", "note", "{}", []float32{0.98, 0.2, 0})
	c, _ := Add(db, "C", "c", "note", "{}", []float32{0, 0, 1})

	n, err := BuildKNNEdges(db, 1)
	if err != nil {
		t.Fatalf("BuildKNNEdges: %v", err)
	}
	if n != 3 { // k=1 per doc, 3 docs
		t.Fatalf("edge count = %d, want 3", n)
	}

	// A's single nearest neighbor must be B (not C).
	nb, err := Neighbors(db, a, "knn", 5)
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	if len(nb) != 1 || nb[0].Dst != b {
		t.Fatalf("A's nearest = %+v, want B(%d)", nb, b)
	}
	if nb[0].Weight < 0.9 {
		t.Errorf("A→B cosine weight = %.3f, want high (>0.9)", nb[0].Weight)
	}
	_ = c

	// Rebuild is idempotent (replaces, not accumulates).
	n2, _ := BuildKNNEdges(db, 1)
	if n2 != 3 {
		t.Errorf("rebuild edge count = %d, want 3 (no accumulation)", n2)
	}
}

func TestByRoleTopic(t *testing.T) {
	db := openDB(t)
	Add(db, "AuthSpec", "x", "spec", `{"role_tags":"backend","topic":"auth"}`, nil)
	Add(db, "DeployDoc", "y", "runbook", `{"role_tags":"backend","topic":"deploy"}`, nil)
	Add(db, "OpsDoc", "z", "runbook", `{"role_tags":"ops","topic":"deploy"}`, nil)

	// role only
	if docs, _ := ByRole(db, "backend", 10); len(docs) != 2 {
		t.Errorf("ByRole backend: want 2, got %d", len(docs))
	}
	// role + topic
	docs, err := ByRoleTopic(db, "backend", "auth", 10)
	if err != nil {
		t.Fatalf("ByRoleTopic: %v", err)
	}
	if len(docs) != 1 || docs[0].Title != "AuthSpec" {
		t.Errorf("ByRoleTopic backend/auth: want [AuthSpec], got %d docs", len(docs))
	}
}

func TestRelationsView(t *testing.T) {
	db := openDB(t)
	Add(db, "Old spec", "x", "spec", "{}", nil) // 1
	Add(db, "New spec", "y", "spec", "{}", nil) // 2
	tx, _ := db.Begin()
	UpsertTypedEdge(tx, Edge{Src: 2, Dst: 1, Weight: 0.9, Kind: "supersedes", Status: "proposed", Rationale: "newer", Model: "m"})
	tx.Commit()

	doc, views, err := RelationsView(db, 2, 10)
	if err != nil {
		t.Fatalf("RelationsView: %v", err)
	}
	if doc.Slug() != "SPEC-2" {
		t.Errorf("doc = %+v, want SPEC-2", doc)
	}
	if len(views) != 1 {
		t.Fatalf("want 1 relation, got %d", len(views))
	}
	v := views[0]
	if v.Kind != "supersedes" || v.TargetSlug != "SPEC-1" || v.TargetTitle != "Old spec" ||
		v.Status != "proposed" || v.Rationale != "newer" || v.Model != "m" || v.Confidence != 0.9 {
		t.Errorf("view = %+v", v)
	}

	// A doc with no typed relations returns an empty (not nil-error) slice.
	_, none, err := RelationsView(db, 1, 10)
	if err != nil {
		t.Fatalf("RelationsView (no relations): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("want 0 relations for SPEC-1 (only has an incoming edge), got %d", len(none))
	}
}
