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

// TestViewRelations_BatchResolvesDistinctAndDuplicateTargets covers the
// batched fetch directly: multiple edges (including two pointing at the same
// target, and one pointing at a nonexistent doc) resolved in one call.
func TestViewRelations_BatchResolvesDistinctAndDuplicateTargets(t *testing.T) {
	db := openDB(t)
	Add(db, "Source", "x", "spec", "{}", nil)  // 1
	Add(db, "TargetA", "y", "spec", "{}", nil) // 2
	Add(db, "TargetB", "z", "spec", "{}", nil) // 3

	edges := []Edge{
		{Src: 1, Dst: 2, Kind: "elaborates", Weight: 0.9},
		{Src: 1, Dst: 3, Kind: "same_topic", Weight: 0.8},
		{Src: 1, Dst: 2, Kind: "duplicates", Weight: 0.7},   // second edge to the SAME target
		{Src: 1, Dst: 999, Kind: "same_topic", Weight: 0.5}, // target doesn't exist
	}
	views := ViewRelations(db, edges)
	if len(views) != 4 {
		t.Fatalf("want 4 views (one per edge), got %d", len(views))
	}
	if views[0].TargetSlug != "SPEC-2" || views[0].TargetTitle != "TargetA" {
		t.Errorf("views[0] = %+v", views[0])
	}
	if views[1].TargetSlug != "SPEC-3" || views[1].TargetTitle != "TargetB" {
		t.Errorf("views[1] = %+v", views[1])
	}
	// Same target resolved consistently for both edges pointing at it.
	if views[2].TargetSlug != "SPEC-2" || views[2].TargetTitle != "TargetA" {
		t.Errorf("views[2] (duplicate target) = %+v", views[2])
	}
	// Nonexistent target falls back to the id-N label, empty title.
	if views[3].TargetSlug != "id-999" || views[3].TargetTitle != "" {
		t.Errorf("views[3] (missing target) = %+v", views[3])
	}

	// Empty input is a no-op, not a query.
	if got := ViewRelations(db, nil); len(got) != 0 {
		t.Errorf("ViewRelations(nil) = %v, want empty", got)
	}
}
