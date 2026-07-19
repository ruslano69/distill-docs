package main

import "testing"

// TestMetaFromArgs covers the MCP arg-map binder (the flag/Merge binders live in
// internal/docmeta and are tested there).
func TestMetaFromArgs(t *testing.T) {
	dm := metaFromArgs(map[string]any{
		"author": "r", "role_tags": "backend", "source_version": "v1", "source_ref": "T-9",
		"topic": "auth", "priority": 3.0, "pinned": true, "supersedes": float64(4),
	})
	if dm.Author != "r" || dm.RoleTags != "backend" || dm.SourceVersion != "v1" || dm.SourceRef != "T-9" ||
		dm.Topic != "auth" || dm.Priority != 3.0 || !dm.Pinned || dm.Supersedes != 4 {
		t.Fatalf("metaFromArgs produced %+v", dm)
	}

	// An empty arg map yields an all-zero Meta → JSON emits "{}".
	if got := metaFromArgs(map[string]any{}).JSON(); got != "{}" {
		t.Errorf("empty args should marshal to {}, got %s", got)
	}
}
