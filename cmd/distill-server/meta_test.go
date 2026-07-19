package main

import (
	"flag"
	"testing"
)

func TestRegisterRankFlags(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	rank := registerRankFlags(fs)
	if err := fs.Parse([]string{"--topic", "auth", "--priority", "2.5", "--pinned", "--supersedes", "7"}); err != nil {
		t.Fatal(err)
	}
	dm := docMeta{Author: "ruslan"} // command-specific field set separately
	rank.apply(&dm)
	if dm.Author != "ruslan" || dm.Topic != "auth" || dm.Priority != 2.5 || !dm.Pinned || dm.Supersedes != 7 {
		t.Fatalf("apply produced %+v", dm)
	}

	// Defaults leave the ranking fields zero (so metaJSON omits them).
	fs2 := flag.NewFlagSet("t2", flag.ContinueOnError)
	rank2 := registerRankFlags(fs2)
	fs2.Parse(nil)
	var dm2 docMeta
	rank2.apply(&dm2)
	if dm2.Topic != "" || dm2.Priority != 0 || dm2.Pinned || dm2.Supersedes != 0 {
		t.Errorf("zero defaults not preserved: %+v", dm2)
	}
}

func TestMetaFromArgs(t *testing.T) {
	dm := metaFromArgs(map[string]any{
		"author": "r", "role_tags": "backend", "source_version": "v1", "source_ref": "T-9",
		"topic": "auth", "priority": 3.0, "pinned": true, "supersedes": float64(4),
	})
	if dm.Author != "r" || dm.RoleTags != "backend" || dm.SourceVersion != "v1" || dm.SourceRef != "T-9" ||
		dm.Topic != "auth" || dm.Priority != 3.0 || !dm.Pinned || dm.Supersedes != 4 {
		t.Fatalf("metaFromArgs produced %+v", dm)
	}

	// An empty arg map yields an all-zero docMeta → metaJSON emits "{}".
	if got := metaJSON(metaFromArgs(map[string]any{})); got != "{}" {
		t.Errorf("empty args should marshal to {}, got %s", got)
	}
}
