package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruslano69/distill-docs/internal/knowledge"
)

// TestConfig_SingleFileHonorsSettings proves `distill config` persists corpus
// defaults that a later `distill add --file` picks up (chunk-size splits the
// file; type comes from the setting), and that an explicit flag overrides.
func TestConfig_SingleFileHonorsSettings(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "docs.sqlite")

	runConfig(dbPath, []string{"--chunk-size", "120", "--chunk-overlap", "10", "--type", "manual"})

	db, err := knowledge.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok, _ := knowledge.GetSetting(db, knowledge.SettingChunkSize); !ok || v != "120" {
		t.Fatalf("chunk_size setting = %q ok=%v, want 120", v, ok)
	}
	db.Close()

	f := filepath.Join(dir, "doc.txt")
	body := strings.Repeat("One paragraph of prose about the system here.\n\n", 40)
	if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	// No --chunk-size / --type → both resolve from settings.
	runAdd(dbPath, []string{"--file", f})

	db2, err := knowledge.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	var chunks, typed int
	db2.QueryRow(`SELECT COUNT(*) FROM docs`).Scan(&chunks)
	db2.QueryRow(`SELECT COUNT(*) FROM docs WHERE type='manual'`).Scan(&typed)
	if chunks < 2 {
		t.Errorf("120-rune chunk setting should split into >1 chunk, got %d", chunks)
	}
	if typed != chunks {
		t.Errorf("every chunk should take the 'manual' type from settings: %d/%d", typed, chunks)
	}
}
