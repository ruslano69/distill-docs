package knowledge

import (
	"flag"
	"path/filepath"
	"testing"
)

func TestSettings_Roundtrip(t *testing.T) {
	db := openDB(t)

	if _, ok, _ := GetSetting(db, SettingChunkSize); ok {
		t.Fatal("empty store should report no setting")
	}
	if err := SetSetting(db, SettingChunkSize, "500"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	v, ok, err := GetSetting(db, SettingChunkSize)
	if err != nil || !ok || v != "500" {
		t.Fatalf("GetSetting = %q ok=%v err=%v", v, ok, err)
	}

	// Upsert overwrites.
	SetSetting(db, SettingChunkSize, "800")
	v, _, _ = GetSetting(db, SettingChunkSize)
	if v != "800" {
		t.Errorf("upsert should overwrite, got %q", v)
	}

	SetSetting(db, SettingStripRunes, "Ω")
	all, err := AllSettings(db)
	if err != nil {
		t.Fatal(err)
	}
	if all[SettingChunkSize] != "800" || all[SettingStripRunes] != "Ω" {
		t.Errorf("AllSettings = %v", all)
	}
}

// TestSettings_TableSurvivesReopen mimics a DB created before this feature: the
// settings table must be present after Open (it is a plain CREATE IF NOT EXISTS,
// so reopening an existing file adds it with no migration).
func TestSettings_TableSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reopen.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	SetSetting(db, SettingAuthor, "ruslan")
	db.Close()

	db2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	v, ok, _ := GetSetting(db2, SettingAuthor)
	if !ok || v != "ruslan" {
		t.Fatalf("setting not persisted across reopen: %q ok=%v", v, ok)
	}
}

func TestFlagResolver_Precedence(t *testing.T) {
	db := openDB(t)
	SetSetting(db, SettingChunkSize, "500")
	SetSetting(db, SettingType, "spec")
	// SettingAuthor deliberately unset → flag default should stand.

	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	chunk := fs.Int("chunk-size", 800, "")
	docType := fs.String("type", "general", "")
	author := fs.String("author", "", "")
	// Explicitly pass only --chunk-size; --type and --author left to resolve.
	if err := fs.Parse([]string{"--chunk-size", "900"}); err != nil {
		t.Fatal(err)
	}

	r, err := NewFlagResolver(db, fs)
	if err != nil {
		t.Fatal(err)
	}
	// chunk-size was passed explicitly → flag wins over the stored 500.
	if got := r.Int("chunk-size", SettingChunkSize, *chunk); got != 900 {
		t.Errorf("explicit flag should win: got %d want 900", got)
	}
	// type not passed → stored setting "spec" wins over the default "general".
	if got := r.Str("type", SettingType, *docType); got != "spec" {
		t.Errorf("stored setting should win: got %q want spec", got)
	}
	// author not passed and unset → flag default "" stands.
	if got := r.Str("author", SettingAuthor, *author); got != "" {
		t.Errorf("unset setting should leave default: got %q", got)
	}
}
