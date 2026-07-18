package knowledge

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// SessionTurn is one addressable "slice of knowledge" pulled from a Claude Code
// session transcript (a .jsonl file): a single user prompt, an assistant reply,
// or an assistant reasoning (thinking) block. Kind is carried through to the
// doc's `type` so the three slices stay separable at query time — search only
// `assistant` answers, isolate `thinking` reasoning, or drop thinking when the
// main turns suffice.
type SessionTurn struct {
	Kind      string // "user" | "assistant" | "thinking" (becomes the doc type)
	Text      string
	Timestamp int64  // unix seconds, parsed from the record's ISO-8601 timestamp
	UUID      string // the record's uuid (stable provenance handle)
	GitBranch string
	Model     string // assistant records only
	Cwd       string
}

// rawSessionRecord is the subset of a transcript line we care about. The file
// interleaves many record types (user, assistant, system, attachment, mode,
// titles, queue-operation); only user/assistant carry conversational content.
type rawSessionRecord struct {
	Type      string         `json:"type"`
	Timestamp string         `json:"timestamp"`
	UUID      string         `json:"uuid"`
	GitBranch string         `json:"gitBranch"`
	Cwd       string         `json:"cwd"`
	Message   *rawSessionMsg `json:"message"`
}

type rawSessionMsg struct {
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"` // either a string or an array of blocks
}

type rawSessionBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Thinking string `json:"thinking"`
}

// ParseSession reads a Claude Code transcript (.jsonl) and returns its
// conversational turns in file order. See ParseSessionBytes for the extraction
// rules.
func ParseSession(path string) ([]SessionTurn, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open session %s: %w", path, err)
	}
	defer f.Close()

	var turns []SessionTurn
	sc := bufio.NewScanner(f)
	// Transcript lines can be large (a single assistant turn with a big tool
	// result); raise the scanner's line cap well above the 64K default.
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		b := sc.Bytes()
		if len(strings.TrimSpace(string(b))) == 0 {
			continue
		}
		rec, err := parseSessionLine(b)
		if err != nil {
			return nil, fmt.Errorf("session line %d: %w", line, err)
		}
		turns = append(turns, rec...)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read session: %w", err)
	}
	return turns, nil
}

// ParseSessionBytes extracts turns from an in-memory transcript (newline-
// delimited JSON), the same way ParseSession does — exposed for tests.
//
// Extraction rules:
//   - Only user/assistant records contribute; everything else is skipped.
//   - A user record yields one "user" turn (its content, whether a bare string
//     or the text blocks of an array).
//   - An assistant record yields one "assistant" turn per text block and one
//     "thinking" turn per thinking block, so reasoning is a separately typed
//     slice. tool_use / tool_result blocks are dropped (they are actions and
//     bulky outputs, not knowledge); the base64 `signature` on thinking blocks
//     is likewise ignored (it is an opaque crypto signature, not content).
//   - Empty/whitespace-only turns are dropped.
func ParseSessionBytes(data []byte) ([]SessionTurn, error) {
	var turns []SessionTurn
	for i, ln := range strings.Split(string(data), "\n") {
		if len(strings.TrimSpace(ln)) == 0 {
			continue
		}
		rec, err := parseSessionLine([]byte(ln))
		if err != nil {
			return nil, fmt.Errorf("session line %d: %w", i+1, err)
		}
		turns = append(turns, rec...)
	}
	return turns, nil
}

func parseSessionLine(b []byte) ([]SessionTurn, error) {
	var rec rawSessionRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return nil, fmt.Errorf("parse record: %w", err)
	}
	if rec.Type != "user" && rec.Type != "assistant" {
		return nil, nil
	}
	if rec.Message == nil {
		return nil, nil
	}

	ts := parseSessionTime(rec.Timestamp)
	base := SessionTurn{Timestamp: ts, UUID: rec.UUID, GitBranch: rec.GitBranch, Cwd: rec.Cwd}

	// Content is polymorphic: a bare string, or an array of typed blocks.
	var asString string
	if err := json.Unmarshal(rec.Message.Content, &asString); err == nil {
		if t := strings.TrimSpace(asString); t != "" {
			turn := base
			turn.Kind = rec.Type
			turn.Text = t
			return []SessionTurn{turn}, nil
		}
		return nil, nil
	}

	var blocks []rawSessionBlock
	if err := json.Unmarshal(rec.Message.Content, &blocks); err != nil {
		// Unknown content shape — skip rather than fail the whole import.
		return nil, nil
	}

	var out []SessionTurn
	for _, bl := range blocks {
		switch bl.Type {
		case "text":
			if t := strings.TrimSpace(bl.Text); t != "" {
				turn := base
				turn.Kind = rec.Type // "user" or "assistant"
				turn.Model = rec.Message.Model
				turn.Text = t
				out = append(out, turn)
			}
		case "thinking":
			if t := strings.TrimSpace(bl.Thinking); t != "" {
				turn := base
				turn.Kind = "thinking"
				turn.Model = rec.Message.Model
				turn.Text = t
				out = append(out, turn)
			}
			// tool_use / tool_result and any other block types are intentionally dropped.
		}
	}
	return out, nil
}

// SessionChunk is one insertable unit: a (possibly split) piece of a turn,
// carrying the turn's provenance so every chunk keeps the right timestamp,
// role, and uuid. A long turn produces several parts; a short one, exactly one.
type SessionChunk struct {
	Turn    SessionTurn
	Title   string
	Content string
}

// ChunkSessionTurns splits each turn's text with the shared chunker (so a long
// assistant answer is broken at paragraph boundaries with overlap, same as file
// ingest) while stamping every resulting chunk with the originating turn's
// provenance. Chunks never cross turn boundaries.
func ChunkSessionTurns(turns []SessionTurn, opts ChunkOpts) []SessionChunk {
	var out []SessionChunk
	for _, t := range turns {
		title := turnTitle(t)
		sec := docSection{title: title, paras: splitParagraphs(t.Text)}
		chunks := sectionsToChunks([]docSection{sec}, "session", opts)
		multi := len(chunks) > 1
		for i, ch := range chunks {
			ct := title
			if multi {
				ct = fmt.Sprintf("%s (%d/%d)", title, i+1, len(chunks))
			}
			out = append(out, SessionChunk{Turn: t, Title: ct, Content: ch.Content})
		}
	}
	return out
}

// turnTitle builds a short, legible title: the role plus the first non-empty
// line of the text, truncated.
func turnTitle(t SessionTurn) string {
	first := ""
	for _, ln := range strings.Split(t.Text, "\n") {
		if s := strings.TrimSpace(ln); s != "" {
			first = s
			break
		}
	}
	r := []rune(first)
	if len(r) > 70 {
		first = string(r[:70]) + "…"
	}
	return t.Kind + " · " + first
}

// Metadata renders the turn's provenance as the JSON blob stored on the doc, so
// the generated columns (role_tags, source_version, ...) and any later filtering
// have structured fields to read. role also lands in the doc `type`.
func (t SessionTurn) Metadata() string {
	m := map[string]string{
		"role":   t.Kind,
		"source": "session",
	}
	if t.UUID != "" {
		m["uuid"] = t.UUID
	}
	if t.GitBranch != "" {
		m["git_branch"] = t.GitBranch
		m["topic"] = t.GitBranch // branch doubles as a coarse topic facet
	}
	if t.Model != "" {
		m["model"] = t.Model
	}
	if t.Timestamp > 0 {
		m["timestamp"] = time.Unix(t.Timestamp, 0).UTC().Format(time.RFC3339)
	}
	// role_tags mirrors role so the generated role_tags column (used by
	// context(role) and role-affinity ranking) is populated for session docs.
	m["role_tags"] = t.Kind
	b, _ := json.Marshal(m)
	return string(b)
}

// parseSessionTime parses the transcript's ISO-8601 timestamp to unix seconds,
// returning 0 (→ default now at insert) if it is missing or unparseable.
func parseSessionTime(s string) int64 {
	if s == "" {
		return 0
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.Unix()
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Unix()
	}
	return 0
}
