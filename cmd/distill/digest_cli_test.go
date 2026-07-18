package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruslano69/distill-docs/internal/knowledge"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what it
// wrote — the run* commands print results there.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	defer func() { os.Stdout = orig }()
	fn()
	w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

func TestRunDigestAndGraph_EndToEnd(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cli.sqlite")
	db, err := knowledge.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	knowledge.Add(db, "Old spec", "the original design", "spec", "{}", nil)    // SPEC-1
	knowledge.Add(db, "New spec", "the replacement design", "spec", "{}", nil) // SPEC-2
	// Seed the kNN candidate directly so the test needs no vectors/embedder.
	db.Exec(`INSERT INTO edges(src,dst,weight,kind,status) VALUES(1,2,0.9,'knn','confirmed')`)
	db.Close()

	// Fake LLM: SPEC-2 supersedes SPEC-1 (b_to_a on the (1,2) candidate).
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"response": `{"kind":"supersedes","direction":"b_to_a","confidence":0.88,"rationale":"newer replaces older"}`,
		})
	}))
	defer ts.Close()

	out := captureStdout(t, func() {
		runDigest(dbPath, []string{"--model", "fake", "--llm-url", ts.URL, "--rebuild-knn=false"})
	})
	if !strings.Contains(out, "1 edges written") {
		t.Errorf("digest output missing edge count:\n%s", out)
	}
	if !strings.Contains(out, "supersedes") {
		t.Errorf("digest output missing kind tally:\n%s", out)
	}

	// graph on SPEC-2 should render the supersedes relation to SPEC-1.
	g := captureStdout(t, func() {
		runGraph(dbPath, []string{"SPEC-2"})
	})
	if !strings.Contains(g, "supersedes") || !strings.Contains(g, "SPEC-1") {
		t.Errorf("graph output missing SPEC-2 → supersedes → SPEC-1:\n%s", g)
	}
	if !strings.Contains(g, "proposed") {
		t.Errorf("graph output should mark the edge proposed:\n%s", g)
	}

	// --json AFTER the bare slug must still be honored (position-independent slug).
	gj := captureStdout(t, func() {
		runGraph(dbPath, []string{"SPEC-2", "--json"})
	})
	var payload struct {
		Slug      string `json:"slug"`
		Relations []struct {
			Kind, Target string
		} `json:"relations"`
	}
	if err := json.Unmarshal([]byte(gj), &payload); err != nil {
		t.Fatalf("graph --json not valid JSON (slug-before-flag dropped --json?): %v\n%s", err, gj)
	}
	if payload.Slug != "SPEC-2" || len(payload.Relations) != 1 || payload.Relations[0].Kind != "supersedes" {
		t.Errorf("graph --json payload wrong: %+v", payload)
	}

	// A re-digest with no content change is all skips (resumable).
	out2 := captureStdout(t, func() {
		runDigest(dbPath, []string{"--model", "fake", "--llm-url", ts.URL, "--rebuild-knn=false"})
	})
	if !strings.Contains(out2, "1 skipped") {
		t.Errorf("second digest should skip the clean pair:\n%s", out2)
	}
}
