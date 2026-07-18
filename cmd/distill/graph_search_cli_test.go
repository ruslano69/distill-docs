package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruslano69/distill-docs/internal/knowledge"
)

// TestRunSearch_GraphFlag proves the --graph flag renders typed relations and
// the superseded/contradicted warning banner in the text output.
func TestRunSearch_GraphFlag(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "g.sqlite")
	db, err := knowledge.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	knowledge.Add(db, "old spec", "alpha shared token", "spec", "{}", nil)  // SPEC-1
	knowledge.Add(db, "new spec", "alpha shared token", "spec", "{}", nil)  // SPEC-2
	tx, _ := db.Begin()
	knowledge.UpsertTypedEdge(tx, knowledge.Edge{Src: 2, Dst: 1, Weight: 0.9, Kind: "supersedes", Status: "proposed"})
	tx.Commit()
	db.Close()

	// Without --graph: no relations, no banner.
	plain := captureStdout(t, func() {
		runSearch(dbPath, []string{"--query", "alpha", "--mode", "fts"})
	})
	if strings.Contains(plain, "supersedes") || strings.Contains(plain, "⚠") {
		t.Errorf("plain search should not render graph:\n%s", plain)
	}

	// With --graph: SPEC-1 is flagged superseded and the relation chain shows.
	g := captureStdout(t, func() {
		runSearch(dbPath, []string{"--query", "alpha", "--mode", "fts", "--graph", "8"})
	})
	if !strings.Contains(g, "⚠ superseded") {
		t.Errorf("SPEC-1 should carry the superseded banner:\n%s", g)
	}
	if !strings.Contains(g, "supersedes") || !strings.Contains(g, "SPEC-2") {
		t.Errorf("graph relation chain missing:\n%s", g)
	}
}
