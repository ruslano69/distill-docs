package knowledge

import (
	"fmt"
	"strconv"
	"strings"
)

// Doc is a single knowledge base entry.
type Doc struct {
	ID        int64
	Title     string
	Content   string
	Type      string
	CreatedAt int64
	Metadata  string
	// Author, RoleTags, and SourceVersion are queryable columns generated
	// from Metadata (TZ FR-3) — empty string if the metadata JSON omits them.
	Author        string
	RoleTags      string
	SourceVersion string
	// Topic/Priority/Pinned/Supersedes are the Stage-1 ranking/curation
	// signals (also metadata-generated columns). Populated by the search
	// primitives that feed the Search re-scoring layer; zero-valued elsewhere.
	Topic      string
	Priority   float64
	Pinned     bool
	Supersedes int64
}

// Slug is the stable, legible dense identifier for a doc — its type in caps
// plus its id (e.g. "SPEC-42", "DECISION-7"). A pure function of type+id, so
// it needs no stored column; it's what structured/graph responses reference
// instead of dumping text.
func (d Doc) Slug() string {
	return strings.ToUpper(d.Type) + "-" + strconv.FormatInt(d.ID, 10)
}

// ParseSlugID extracts the numeric id from a slug ("SPEC-42" -> 42). The type
// prefix is decorative; the id after the last '-' is the key.
func ParseSlugID(slug string) (int64, error) {
	i := strings.LastIndex(slug, "-")
	if i < 0 || i == len(slug)-1 {
		return 0, fmt.Errorf("malformed slug %q", slug)
	}
	return strconv.ParseInt(slug[i+1:], 10, 64)
}

// Result wraps Doc with search scores populated during retrieval.
type Result struct {
	Doc
	FTSRank     float64
	VecDist     float64
	HybridScore float64
	// Score is the final rank from the Search re-scoring layer: the base
	// retrieval score adjusted by recency/priority/pinned/role signals.
	Score float64
	// Snippet is a keyword-in-context excerpt around the matched term(s),
	// populated by FTS searches via SQLite's snippet() (empty for vec/regex
	// results, which have no FTS match to center on — callers fall back to
	// Content in that case).
	Snippet string
}
