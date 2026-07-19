package docmeta

import (
	"encoding/json"
	"flag"
	"testing"
)

func TestJSON_OmitsEmptyPinnedAsOne(t *testing.T) {
	if got := (Meta{}).JSON(); got != "{}" {
		t.Errorf("empty Meta = %s, want {}", got)
	}
	got := Meta{Author: "r", Topic: "auth", Priority: 2.5, Pinned: true, Supersedes: 7}.JSON()
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatal(err)
	}
	if m["author"] != "r" || m["topic"] != "auth" || m["priority"] != 2.5 || m["pinned"] != float64(1) || m["supersedes"] != float64(7) {
		t.Errorf("JSON round-trip wrong: %v", m)
	}
	if _, ok := m["role_tags"]; ok {
		t.Error("empty role_tags should be omitted")
	}
}

func TestMerge_OverlaysOntoBase(t *testing.T) {
	// Base carries an arbitrary key; structured fields overlay it.
	got, err := Merge(`{"custom":"x","topic":"old"}`, Meta{Topic: "new", Pinned: true})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal([]byte(got), &m)
	if m["custom"] != "x" {
		t.Error("base custom key dropped")
	}
	if m["topic"] != "new" {
		t.Errorf("structured field should overlay base, got topic=%v", m["topic"])
	}
	if m["pinned"] != float64(1) {
		t.Errorf("pinned not applied: %v", m)
	}

	// Default base "{}" merges to just the structured fields.
	got, _ = Merge("{}", Meta{Topic: "auth"})
	if got != `{"topic":"auth"}` {
		t.Errorf("empty-base merge = %s", got)
	}

	// Malformed base is a hard error.
	if _, err := Merge("{not json", Meta{}); err == nil {
		t.Error("malformed --meta should error")
	}
}

func TestRegisterRankFlags(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	rank := RegisterRankFlags(fs)
	if err := fs.Parse([]string{"--topic", "auth", "--priority", "2.5", "--pinned", "--supersedes", "7"}); err != nil {
		t.Fatal(err)
	}
	m := Meta{Author: "ruslan"} // command-specific field set separately
	rank.Apply(&m)
	if m.Author != "ruslan" || m.Topic != "auth" || m.Priority != 2.5 || !m.Pinned || m.Supersedes != 7 {
		t.Fatalf("Apply produced %+v", m)
	}

	// Defaults leave ranking fields zero (so JSON omits them).
	fs2 := flag.NewFlagSet("t2", flag.ContinueOnError)
	rank2 := RegisterRankFlags(fs2)
	fs2.Parse(nil)
	var m2 Meta
	rank2.Apply(&m2)
	if m2.Topic != "" || m2.Priority != 0 || m2.Pinned || m2.Supersedes != 0 {
		t.Errorf("zero defaults not preserved: %+v", m2)
	}
}
