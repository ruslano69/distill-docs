package knowledge

import (
	"database/sql"
	"flag"
	"strconv"
)

// Canonical setting keys for per-corpus ingest defaults (see the settings table
// in db.go). Values are stored as strings; the resolver parses them per flag.
const (
	SettingChunkSize     = "chunk_size"
	SettingChunkOverlap  = "chunk_overlap"
	SettingStripRunes    = "strip_runes"
	SettingType          = "type"
	SettingRoleTags      = "role_tags"
	SettingAuthor        = "author"
	SettingSourceVersion = "source_version"
)

// SetSetting upserts a corpus setting.
func SetSetting(db *sql.DB, key, value string) error {
	_, err := db.Exec(
		`INSERT INTO settings(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value)
	return err
}

// GetSetting returns a setting's value and whether it was found.
func GetSetting(db *sql.DB, key string) (string, bool, error) {
	var v string
	err := db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// AllSettings returns every stored setting as a map (empty if none).
func AllSettings(db *sql.DB) (map[string]string, error) {
	rows, err := db.Query(`SELECT key, value FROM settings ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// FlagResolver layers a CLI flag over a stored corpus setting over the flag's
// own default: an explicitly-passed flag always wins; otherwise a stored setting
// applies; otherwise the flag default stands. It is the single seam both the
// single-file and server ingest paths use so corpus defaults behave identically.
type FlagResolver struct {
	set map[string]bool   // flags explicitly passed on this invocation
	kv  map[string]string // stored corpus settings
}

// NewFlagResolver builds a resolver from the corpus settings and the set of
// flags the user explicitly passed (fs.Visit). Call after fs.Parse.
func NewFlagResolver(db *sql.DB, fs *flag.FlagSet) (*FlagResolver, error) {
	kv, err := AllSettings(db)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	return &FlagResolver{set: set, kv: kv}, nil
}

// Str resolves a string flag: cur (the parsed flag value) when flagName was
// explicitly passed, else the stored setting for key, else cur (the default).
func (r *FlagResolver) Str(flagName, key, cur string) string {
	if r.set[flagName] {
		return cur
	}
	if v, ok := r.kv[key]; ok {
		return v
	}
	return cur
}

// Int resolves an int flag the same way; an unparseable stored value falls back
// to cur so a corrupt setting can never crash an ingest.
func (r *FlagResolver) Int(flagName, key string, cur int) int {
	if r.set[flagName] {
		return cur
	}
	if v, ok := r.kv[key]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return cur
}
