package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ruslano69/distill-docs/internal/knowledge"
	"github.com/ruslano69/distill-docs/internal/truth"
)

// TestRunDigestServer_WritesEdgesToWriteLog proves the server-side digester
// classifies write-log candidates and writes typed edges there (so a later
// publish carries them into a release).
func TestRunDigestServer_WritesEdgesToWriteLog(t *testing.T) {
	dir := t.TempDir()
	store, err := truth.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Seed two docs and a kNN candidate directly (no embedder needed).
	wl, err := knowledge.Open(store.WriteLogPath())
	if err != nil {
		t.Fatal(err)
	}
	knowledge.Add(wl, "Old", "the original design", "spec", "{}", nil)    // 1
	knowledge.Add(wl, "New", "the replacement design", "spec", "{}", nil) // 2
	wl.Exec(`INSERT INTO edges(src,dst,weight,kind,status) VALUES(1,2,0.9,'knn','confirmed')`)
	wl.Close()

	// Fake LLM: SPEC-2 supersedes SPEC-1.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"response": `{"kind":"supersedes","direction":"b_to_a","confidence":0.9,"rationale":"newer"}`,
		})
	}))
	defer ts.Close()

	runDigestServer(store, []string{"--model", "fake", "--llm-url", ts.URL, "--rebuild-knn=false"})

	// The typed edge must now be in the write-log.
	wl2, err := knowledge.Open(store.WriteLogPath())
	if err != nil {
		t.Fatal(err)
	}
	defer wl2.Close()
	typed, err := knowledge.TypedNeighbors(wl2, 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(typed) != 1 || typed[0].Kind != "supersedes" || typed[0].Dst != 1 {
		t.Fatalf("want SPEC-2 supersedes SPEC-1 in write-log, got %+v", typed)
	}
	if typed[0].Status != "proposed" {
		t.Errorf("digester edge should be proposed, got %q", typed[0].Status)
	}
}
