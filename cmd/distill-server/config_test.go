package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ruslano69/distill-docs/internal/embed"
	"github.com/ruslano69/distill-docs/internal/knowledge"
	"github.com/ruslano69/distill-docs/internal/truth"
)

// TestConfig_SetsCorpusDefaultsHonoredByIngest proves `config` persists settings
// and a subsequent `ingest` (with no matching flag) picks them up.
func TestConfig_SetsCorpusDefaultsHonoredByIngest(t *testing.T) {
	dir := t.TempDir()
	store, err := truth.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Set a small chunk size + a default type via config.
	runConfig(store, []string{"--chunk-size", "120", "--chunk-overlap", "10", "--type", "manual"})

	db, err := knowledge.Open(store.WriteLogPath())
	if err != nil {
		t.Fatal(err)
	}
	if v, ok, _ := knowledge.GetSetting(db, knowledge.SettingChunkSize); !ok || v != "120" {
		t.Fatalf("chunk_size setting = %q ok=%v, want 120", v, ok)
	}
	db.Close()

	// A many-paragraph file; with the 120-rune setting it must split into
	// several chunks (a single 800-rune default would keep it as one).
	f := filepath.Join(dir, "doc.txt")
	body := strings.Repeat("This is one paragraph of prose about the system.\n\n", 40)
	if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	// Ingest WITHOUT --chunk-size / --type → both come from the settings.
	runIngest(store, embed.New("", ""), []string{"--file", f})

	db2, err := knowledge.Open(store.WriteLogPath())
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	var chunks, typed int
	db2.QueryRow(`SELECT COUNT(*) FROM docs`).Scan(&chunks)
	db2.QueryRow(`SELECT COUNT(*) FROM docs WHERE type='manual'`).Scan(&typed)
	if chunks < 2 {
		t.Errorf("small chunk-size setting should split into >1 chunk, got %d", chunks)
	}
	if typed != chunks {
		t.Errorf("every chunk should take the 'manual' type from settings: %d/%d", typed, chunks)
	}
}
