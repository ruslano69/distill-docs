package knowledge

import (
	"strings"
	"testing"
)

// A compact synthetic transcript covering the shapes the parser must handle:
// a bare-string user prompt, an assistant record mixing thinking+text+tool_use,
// a tool_result user record, and a non-conversational record to skip.
const sampleTranscript = `{"type":"user","timestamp":"2026-07-18T10:00:00.000Z","uuid":"u1","gitBranch":"main","cwd":"/repo","message":{"role":"user","content":"build the thing"}}
{"type":"assistant","timestamp":"2026-07-18T10:00:05.000Z","uuid":"a1","gitBranch":"main","message":{"role":"assistant","model":"claude-opus-4-8","content":[{"type":"thinking","thinking":"Let me reason about the approach.","signature":"AAAABBBBCCCCDDDD"},{"type":"text","text":"Here is the plan."},{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}
{"type":"user","timestamp":"2026-07-18T10:00:07.000Z","uuid":"u2","message":{"role":"user","content":[{"type":"tool_result","content":"file listing output"}]}}
{"type":"user","timestamp":"2026-07-18T10:00:09.000Z","uuid":"u3","gitBranch":"main","message":{"role":"user","content":[{"type":"text","text":"looks good, ship it"}]}}
{"type":"system","timestamp":"2026-07-18T10:00:10.000Z","subtype":"hook","message":null}
{"type":"mode","mode":"default","sessionId":"s"}`

func TestParseSessionBytes(t *testing.T) {
	turns, err := ParseSessionBytes([]byte(sampleTranscript))
	if err != nil {
		t.Fatalf("ParseSessionBytes: %v", err)
	}
	// Expected: u1 (user), a1 thinking, a1 text, u3 text. The tool_result and
	// tool_use blocks, and the system/mode records, are dropped.
	if len(turns) != 4 {
		t.Fatalf("want 4 turns, got %d: %+v", len(turns), turns)
	}
	kinds := []string{turns[0].Kind, turns[1].Kind, turns[2].Kind, turns[3].Kind}
	want := []string{"user", "thinking", "assistant", "user"}
	for i := range want {
		if kinds[i] != want[i] {
			t.Errorf("turn %d kind = %q, want %q", i, kinds[i], want[i])
		}
	}
	if turns[0].Text != "build the thing" {
		t.Errorf("user text = %q", turns[0].Text)
	}
	if turns[1].Text != "Let me reason about the approach." {
		t.Errorf("thinking text = %q", turns[1].Text)
	}
	if turns[2].Text != "Here is the plan." || turns[2].Model != "claude-opus-4-8" {
		t.Errorf("assistant text/model = %q / %q", turns[2].Text, turns[2].Model)
	}
	// Timestamps parsed to unix seconds, monotonically increasing.
	if turns[0].Timestamp == 0 || turns[0].Timestamp >= turns[3].Timestamp {
		t.Errorf("timestamps not parsed/ordered: %d .. %d", turns[0].Timestamp, turns[3].Timestamp)
	}
	if turns[0].GitBranch != "main" || turns[0].UUID != "u1" {
		t.Errorf("provenance not captured: %+v", turns[0])
	}
}

func TestParseSession_ToolResultDropped(t *testing.T) {
	// A record whose only content is a tool_result yields no turns.
	line := `{"type":"user","timestamp":"2026-07-18T10:00:00Z","message":{"role":"user","content":[{"type":"tool_result","content":"big output"}]}}`
	turns, err := ParseSessionBytes([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 0 {
		t.Errorf("tool_result-only record should yield no turns, got %d", len(turns))
	}
}

func TestChunkSessionTurns_SplitsLongPreservesProvenance(t *testing.T) {
	long := strings.Repeat("paragraph one is here.\n\n", 40) // well over one chunk
	turns := []SessionTurn{
		{Kind: "user", Text: "short prompt", Timestamp: 100, UUID: "u1"},
		{Kind: "assistant", Text: long, Timestamp: 200, UUID: "a1"},
	}
	chunks := ChunkSessionTurns(turns, ChunkOpts{MaxRunes: 200, OverlapRunes: 20})
	// short → 1 chunk; long → many.
	var shortN, longN int
	for _, c := range chunks {
		switch c.Turn.UUID {
		case "u1":
			shortN++
			if c.Turn.Timestamp != 100 {
				t.Errorf("short chunk lost timestamp: %d", c.Turn.Timestamp)
			}
		case "a1":
			longN++
			if c.Turn.Timestamp != 200 || c.Turn.Kind != "assistant" {
				t.Errorf("long chunk lost provenance: %+v", c.Turn)
			}
			if !strings.Contains(c.Title, "/") {
				t.Errorf("multi-part chunk title should carry (i/n): %q", c.Title)
			}
		}
	}
	if shortN != 1 || longN < 2 {
		t.Errorf("want 1 short + >=2 long chunks, got short=%d long=%d", shortN, longN)
	}
}

func TestSessionTurn_Metadata(t *testing.T) {
	turn := SessionTurn{Kind: "thinking", Timestamp: 1_752_832_800, UUID: "x", GitBranch: "feature/x", Model: "m"}
	m := turn.Metadata()
	for _, want := range []string{`"role":"thinking"`, `"source":"session"`, `"git_branch":"feature/x"`, `"topic":"feature/x"`, `"role_tags":"thinking"`, `"timestamp":"2025-`} {
		if !strings.Contains(m, want) {
			t.Errorf("metadata missing %q in %s", want, m)
		}
	}
}

func TestAddAt_HistoricalCreatedAt(t *testing.T) {
	db := openDB(t)
	const ts = 1_600_000_000 // 2020-09-13, well in the past
	id, err := AddAt(db, "old", "historical content", "assistant", "{}", nil, ts)
	if err != nil {
		t.Fatalf("AddAt: %v", err)
	}
	var got int64
	if err := db.QueryRow(`SELECT created_at FROM docs WHERE id=?`, id).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != ts {
		t.Errorf("created_at = %d, want %d (historical timestamp not preserved)", got, ts)
	}

	// createdAt=0 falls back to now (not the epoch).
	id2, _ := AddAt(db, "new", "content", "user", "{}", nil, 0)
	db.QueryRow(`SELECT created_at FROM docs WHERE id=?`, id2).Scan(&got)
	if got < 1_700_000_000 {
		t.Errorf("createdAt=0 should default to now, got %d", got)
	}
}
