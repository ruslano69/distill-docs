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
