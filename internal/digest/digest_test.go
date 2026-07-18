package digest

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/ruslano69/distill-docs/internal/knowledge"
)

// stub is a Classifier that returns canned relations and counts calls, so the
// run loop can be tested without an LLM or HTTP.
type stub struct {
	calls int
	fn    func(a, b knowledge.Doc) (Relation, error)
}

func (s *stub) Enabled() bool { return true }
func (s *stub) Model() string { return "stub-model" }
func (s *stub) Classify(_ context.Context, a, b knowledge.Doc) (Relation, error) {
	s.calls++
	return s.fn(a, b)
}

func seedDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := knowledge.Open(filepath.Join(t.TempDir(), "d.sqlite"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// knnEdge inserts a raw kNN candidate edge (bypassing vector math) so tests
// control the candidate set directly.
func knnEdge(t *testing.T, db *sql.DB, src, dst int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT OR REPLACE INTO edges(src,dst,weight,kind,status) VALUES(?,?,0.9,'knn','confirmed')`, src, dst); err != nil {
		t.Fatal(err)
	}
}

func fixedNow() int64 { return 1_700_000_000 }

func TestRun_ClassifiesAndWritesEdges(t *testing.T) {
	db := seedDB(t)
	knowledge.Add(db, "Old spec", "the original design", "spec", "{}", nil)    // 1
	knowledge.Add(db, "New spec", "the replacement design", "spec", "{}", nil) // 2
	knowledge.Add(db, "Unrelated", "cooking recipes", "note", "{}", nil)       // 3
	knnEdge(t, db, 1, 2)
	knnEdge(t, db, 2, 1) // reverse direction collapses to the same candidate
	knnEdge(t, db, 1, 3)

	s := &stub{fn: func(a, b knowledge.Doc) (Relation, error) {
		if a.ID == 1 && b.ID == 2 {
			return Relation{Kind: "supersedes", Direction: "b_to_a", Confidence: 0.9, Rationale: "2 replaces 1"}, nil
		}
		return Relation{Kind: KindNone}, nil
	}}

	rep, err := Run(context.Background(), db, s, Options{Now: fixedNow})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Candidates != 2 {
		t.Errorf("Candidates = %d, want 2 (1-2 collapsed, 1-3)", rep.Candidates)
	}
	if rep.Classified != 2 || s.calls != 2 {
		t.Errorf("Classified=%d calls=%d, want 2/2", rep.Classified, s.calls)
	}
	if rep.EdgesWritten != 1 || rep.ByKind["supersedes"] != 1 {
		t.Errorf("edges=%d bykind=%v, want 1 supersedes", rep.EdgesWritten, rep.ByKind)
	}

	// b_to_a on candidate (1,2) means the edge is 2 supersedes 1.
	typed, _ := knowledge.TypedNeighbors(db, 2, 10)
	if len(typed) != 1 || typed[0].Kind != "supersedes" || typed[0].Dst != 1 {
		t.Fatalf("want SPEC-2 supersedes SPEC-1, got %+v", typed)
	}
	if typed[0].Status != "proposed" || typed[0].Model != "stub-model" || typed[0].Rationale != "2 replaces 1" {
		t.Errorf("provenance wrong: %+v", typed[0])
	}
}

func TestRun_ResumableSkipsCleanPairs(t *testing.T) {
	db := seedDB(t)
	knowledge.Add(db, "A", "alpha", "note", "{}", nil)
	knowledge.Add(db, "B", "beta", "note", "{}", nil)
	knnEdge(t, db, 1, 2)

	s := &stub{fn: func(a, b knowledge.Doc) (Relation, error) {
		return Relation{Kind: "same_topic", Direction: "a_to_b", Confidence: 0.7}, nil
	}}
	if _, err := Run(context.Background(), db, s, Options{Now: fixedNow}); err != nil {
		t.Fatal(err)
	}
	if s.calls != 1 {
		t.Fatalf("first pass: calls=%d want 1", s.calls)
	}
	// Second pass: nothing changed → all skipped, no LLM calls.
	rep, err := Run(context.Background(), db, s, Options{Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	if s.calls != 1 {
		t.Errorf("second pass re-called LLM: calls=%d want still 1", s.calls)
	}
	if rep.Skipped != 1 || rep.Classified != 0 {
		t.Errorf("want 1 skip / 0 classify, got skip=%d classify=%d", rep.Skipped, rep.Classified)
	}
}

func TestRun_ReclassifiesOnContentChange(t *testing.T) {
	db := seedDB(t)
	knowledge.Add(db, "A", "alpha", "note", "{}", nil)
	knowledge.Add(db, "B", "beta", "note", "{}", nil)
	knnEdge(t, db, 1, 2)

	s := &stub{fn: func(a, b knowledge.Doc) (Relation, error) {
		return Relation{Kind: "same_topic", Direction: "a_to_b", Confidence: 0.7}, nil
	}}
	Run(context.Background(), db, s, Options{Now: fixedNow})

	// Edit B's content → the pair's fingerprint changes → it must be re-asked.
	if _, err := db.Exec(`UPDATE docs SET content='beta rewritten' WHERE id=2`); err != nil {
		t.Fatal(err)
	}
	rep, err := Run(context.Background(), db, s, Options{Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Classified != 1 || s.calls != 2 {
		t.Errorf("content change should re-classify: classify=%d calls=%d", rep.Classified, s.calls)
	}
}

func TestRun_BelowThresholdRecordsNoEdge(t *testing.T) {
	db := seedDB(t)
	knowledge.Add(db, "A", "alpha", "note", "{}", nil)
	knowledge.Add(db, "B", "beta", "note", "{}", nil)
	knnEdge(t, db, 1, 2)

	s := &stub{fn: func(a, b knowledge.Doc) (Relation, error) {
		return Relation{Kind: "elaborates", Direction: "a_to_b", Confidence: 0.3}, nil // below default 0.5
	}}
	rep, err := Run(context.Background(), db, s, Options{Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	if rep.EdgesWritten != 0 {
		t.Errorf("below-threshold edge should be dropped, got %d", rep.EdgesWritten)
	}
	// But the pair is still stamped, so it isn't re-asked.
	if _, ok, _ := knowledge.DigestFingerprint(db, 1, 2); !ok {
		t.Error("below-threshold pair must still be marked digested")
	}
}

func TestRun_StaleEdgeClearedWhenRelationVanishes(t *testing.T) {
	db := seedDB(t)
	knowledge.Add(db, "A", "alpha", "note", "{}", nil)
	knowledge.Add(db, "B", "beta", "note", "{}", nil)
	knnEdge(t, db, 1, 2)

	kind := "duplicates"
	s := &stub{fn: func(a, b knowledge.Doc) (Relation, error) {
		return Relation{Kind: kind, Direction: "a_to_b", Confidence: 0.8}, nil
	}}
	Run(context.Background(), db, s, Options{Now: fixedNow})
	if typed, _ := knowledge.TypedNeighbors(db, 1, 10); len(typed) != 1 {
		t.Fatalf("setup: want 1 edge, got %d", len(typed))
	}

	// Re-digest after a content change now finds no relation → edge must go.
	db.Exec(`UPDATE docs SET content='beta v2' WHERE id=2`)
	kind = KindNone
	Run(context.Background(), db, s, Options{Now: fixedNow})
	if typed, _ := knowledge.TypedNeighbors(db, 1, 10); len(typed) != 0 {
		t.Errorf("stale edge not cleared, still %d", len(typed))
	}
}

func TestRun_DisabledClientErrors(t *testing.T) {
	db := seedDB(t)
	disabled := &stubDisabled{}
	if _, err := Run(context.Background(), db, disabled, Options{}); err == nil {
		t.Error("disabled classifier should error")
	}
}

type stubDisabled struct{}

func (stubDisabled) Enabled() bool { return false }
func (stubDisabled) Model() string { return "" }
func (stubDisabled) Classify(context.Context, knowledge.Doc, knowledge.Doc) (Relation, error) {
	return Relation{}, nil
}

func TestClassify_ValidatesKindAndClampsConfidence(t *testing.T) {
	tests := []struct {
		raw      string
		wantKind string
		wantConf float64
	}{
		{`{"kind":"supersedes","direction":"a_to_b","confidence":0.8}`, "supersedes", 0.8},
		{`{"kind":"UNKNOWN","confidence":0.9}`, KindNone, 0},          // invalid kind → none
		{`{"kind":"contradicts","confidence":1.7}`, "contradicts", 1}, // clamp high
		{`{"kind":"depends_on","confidence":-0.5}`, "depends_on", 0},  // clamp low
		{`{"kind":"none"}`, KindNone, 0},
	}
	for _, tc := range tests {
		client := fakeLLM(t, tc.raw)
		rel, err := Classify(context.Background(), client, knowledge.Doc{ID: 1, Type: "spec"}, knowledge.Doc{ID: 2, Type: "spec"})
		if err != nil {
			t.Fatalf("%s: %v", tc.raw, err)
		}
		if rel.Kind != tc.wantKind || rel.Confidence != tc.wantConf {
			t.Errorf("%s → kind=%q conf=%v, want %q/%v", tc.raw, rel.Kind, rel.Confidence, tc.wantKind, tc.wantConf)
		}
	}
}
