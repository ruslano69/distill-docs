package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ruslano69/distill-docs/internal/embed"
	"github.com/ruslano69/distill-docs/internal/knowledge"
)

const cliTranscript = `{"type":"user","timestamp":"2026-07-18T10:00:00Z","uuid":"u1","gitBranch":"main","message":{"role":"user","content":"add ranking"}}
{"type":"assistant","timestamp":"2026-07-18T10:00:05Z","uuid":"a1","message":{"role":"assistant","model":"m","content":[{"type":"thinking","thinking":"I will design the schema first."},{"type":"text","text":"Done, ranking added."}]}}`

func captureStdoutSession(t *testing.T, fn func()) string {
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

func TestRunAddSession_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	tr := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(tr, []byte(cliTranscript), 0o644); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "s.sqlite")
	db, err := knowledge.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	out := captureStdoutSession(t, func() {
		runAddSession(db, embed.New("", ""), tr, knowledge.ChunkOpts{MaxRunes: 800, OverlapRunes: 80}, true)
	})
	var rep struct {
		Docs   int            `json:"docs"`
		Turns  int            `json:"turns"`
		ByKind map[string]int `json:"by_kind"`
		From   int64          `json:"from"`
		To     int64          `json:"to"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("json report: %v\n%s", err, out)
	}
	if rep.Docs != 3 || rep.ByKind["user"] != 1 || rep.ByKind["assistant"] != 1 || rep.ByKind["thinking"] != 1 {
		t.Errorf("unexpected report: %+v", rep)
	}

	// The three roles are separately queryable slices, and thinking is isolable.
	th, _ := knowledge.Search(db, knowledge.SearchOpts{Query: "design", Mode: "fts", Prefix: true, Limit: 5, Filter: knowledge.Filter{Type: "thinking"}})
	if len(th) != 1 || th[0].Type != "thinking" {
		t.Errorf("thinking slice not isolable via --type thinking: %+v", th)
	}
	// A query that only appears in thinking must NOT surface when scoped to assistant.
	as, _ := knowledge.Search(db, knowledge.SearchOpts{Query: "design", Mode: "fts", Prefix: true, Limit: 5, Filter: knowledge.Filter{Type: "assistant"}})
	if len(as) != 0 {
		t.Errorf("assistant scope should exclude thinking-only content, got %d", len(as))
	}

	// Historical timestamp preserved on created_at (not import time).
	var createdAt int64
	db.QueryRow(`SELECT created_at FROM docs WHERE type='user'`).Scan(&createdAt)
	if createdAt < 1_700_000_000 || createdAt > 1_800_000_000 {
		t.Errorf("created_at not the transcript time: %d", createdAt)
	}
}
