package digest

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
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

// TestClassify_MissingConfidenceIsErrorNotZero pins down a real failure mode
// found live against gemma4:12b: under loose "json" mode the model produced
// syntactically valid JSON that stopped after "direction" and never emitted
// "confidence" at all. Unmarshaled into a plain float64 field that silently
// zeroes, a genuinely correct classification (with a spot-on rationale) got
// discarded by the confidence threshold and the pair was marked digested —
// permanently losing a right answer. A missing confidence on an asserted
// relation must be an error (pair retried), not a silent 0. kind="none" needs
// no confidence at all (no relation is being asserted).
func TestClassify_MissingConfidenceIsErrorNotZero(t *testing.T) {
	client := fakeLLM(t, `{"rationale":"B deprecates A","kind":"supersedes","direction":"b_to_a"}`)
	if _, err := Classify(context.Background(), client, knowledge.Doc{ID: 1}, knowledge.Doc{ID: 2}); err == nil {
		t.Fatal("missing confidence on an asserted relation should error, not silently become confidence=0")
	}

	noneClient := fakeLLM(t, `{"kind":"none"}`)
	rel, err := Classify(context.Background(), noneClient, knowledge.Doc{ID: 1}, knowledge.Doc{ID: 2})
	if err != nil {
		t.Fatalf("kind=none with no confidence should not error: %v", err)
	}
	if rel.Kind != KindNone {
		t.Errorf("kind = %q, want none", rel.Kind)
	}
}

// TestFormatAddedDate covers the secondary temporal signal fed to the
// classifier: a real timestamp renders as a short date, an absent one (0) as
// "unknown" rather than the misleading 1970 epoch.
func TestFormatAddedDate(t *testing.T) {
	if got := formatAddedDate(0); got != "unknown" {
		t.Errorf("zero timestamp = %q, want unknown", got)
	}
	if got := formatAddedDate(-5); got != "unknown" {
		t.Errorf("negative timestamp = %q, want unknown", got)
	}
	if got := formatAddedDate(1_752_710_400); got != "2025-07-17" {
		t.Errorf("formatAddedDate(1752710400) = %q, want 2025-07-17", got)
	}
}

// TestBuildPrompt_IncludesAddedDatesAndSlugs locks in the prompt content the
// direction fix depends on: each document's slug/type and its added date (the
// secondary tiebreaker signal), so a future edit can't silently drop them.
func TestBuildPrompt_IncludesAddedDatesAndSlugs(t *testing.T) {
	a := knowledge.Doc{ID: 1, Type: "spec", Title: "Auth v1", Content: "static keys", CreatedAt: 1_752_710_400}
	b := knowledge.Doc{ID: 2, Type: "spec", Title: "Auth v2", Content: "oauth flow", CreatedAt: 1_752_796_800}
	got := buildPrompt(a, b)
	for _, want := range []string{"SPEC-1", "SPEC-2", "added=2025-07-17", "added=2025-07-18", "Auth v1", "Auth v2"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q:\n%s", want, got)
		}
	}
}

// TestSystemPrompt_HasDirectionSafeguards locks in the specific prompt-
// engineering fix for the observed direction bug (gemma4:12b once inverted a
// supersedes relation): content-cue-driven supersedes evidence, a worked
// direction example, and reasoning ("rationale") ordered before kind/direction
// so the model commits to its evidence before encoding the direction.
func TestSystemPrompt_HasDirectionSafeguards(t *testing.T) {
	for _, want := range []string{
		"deprecated", "replaces X", // content-cue vocabulary for supersedes
		`{"rationale":`, // rationale is the first requested field (reasoning before encoding)
		"MUST match the document you named as the subject", // direction/rationale consistency instruction
		"b_to_a", // the worked example's direction encoding
	} {
		if !strings.Contains(systemPrompt, want) {
			t.Errorf("systemPrompt missing %q", want)
		}
	}
}
