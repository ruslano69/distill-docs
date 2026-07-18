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
	// Relations holds the typed L2 knowledge-graph edges incident to this hit,
	// populated only when SearchOpts.GraphExpand > 0 (Stage-3 graph-aware
	// retrieval). Each is oriented relative to this result (see Relation.Outgoing),
	// so a caller can warn that a hit is superseded/contradicted before grounding
	// on it. Nil when graph expansion is off.
	Relations []Relation
}

// Relation is one typed L2 edge incident to a Result, oriented relative to that
// result: Outgoing means the result is the subject (result <kind> target);
// otherwise the target is the subject (target <kind> result) — the direction
// that surfaces "superseded by" / "contradicted by" warnings.
type Relation struct {
	Kind       string  // supersedes | contradicts | elaborates | depends_on | duplicates | same_topic
	Outgoing   bool    // true: result → target; false: target → result
	TargetID   int64   // the doc on the other end
	TargetSlug string  // its dense id (e.g. SPEC-12), precomputed
	Weight     float64 // edge confidence
	Status     string  // "proposed" | "confirmed"
}

// Superseded reports whether some other doc supersedes this hit (an incoming
// supersedes edge) — a "don't ground on this, it's obsolete" signal. Only
// meaningful when the result was graph-expanded.
func (r Result) Superseded() bool { return r.hasIncoming("supersedes") }

// Contradicted reports whether some other doc contradicts this hit.
func (r Result) Contradicted() bool { return r.hasIncoming("contradicts") }

func (r Result) hasIncoming(kind string) bool {
	for _, rel := range r.Relations {
		if !rel.Outgoing && rel.Kind == kind {
			return true
		}
	}
	return false
}
