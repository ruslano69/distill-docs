// Package digest is the Stage-2 L2 knowledge-graph digester. Where Stage 1's
// BuildKNNEdges draws the anonymous geometric graph (which docs are near which,
// by vector cosine), the digester asks a local LLM what those adjacencies
// *mean* — turning "doc 42 is near doc 12" into "SPEC-42 supersedes SPEC-12" —
// and records the answer as a typed, weighted, provenance-stamped edge.
//
// It is deliberately bounded and resumable: candidates come only from the kNN
// geometry (O(n·k) LLM calls, not O(n²)), and every classified pair is stamped
// in digest_state so an interrupted or repeated run re-asks nothing whose
// content is unchanged. The LLM only *proposes* (edges land as "proposed");
// confirming the irreversible ones (supersedes) is left to policy/human review.
package digest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ruslano69/distill-docs/internal/knowledge"
	"github.com/ruslano69/distill-docs/internal/llm"
)

// Kinds is the tight relation taxonomy — the Episteme lesson applied: typed
// edges beat anonymous geometry, but the vocabulary stays small (~6) and
// legible, not their 201. Multi-word kinds use underscores so they double as
// stable SQL/JSON tokens.
var Kinds = []string{
	"supersedes",  // subject makes object obsolete (directed, irreversible — review before trusting)
	"contradicts", // subject conflicts with object (surface both, don't silently drop)
	"elaborates",  // subject adds detail/depth to object
	"depends_on",  // subject requires object to make sense / to hold
	"duplicates",  // subject restates object (near-duplicate)
	"same_topic",  // same subject area, no stronger relation
}

// KindNone is the LLM's "these two aren't meaningfully related" answer — a
// first-class outcome that is still recorded (so the pair isn't re-asked).
const KindNone = "none"

var errDisabled = fmt.Errorf("digest: llm disabled (no model configured)")

func timeNow() int64 { return time.Now().Unix() }

// LLMClassifier is the production Classifier: it wraps an *llm.Client and runs
// this package's classification prompt.
type LLMClassifier struct{ Client *llm.Client }

func (l LLMClassifier) Enabled() bool { return l.Client.Enabled() }
func (l LLMClassifier) Model() string { return l.Client.Model }
func (l LLMClassifier) Classify(ctx context.Context, a, b knowledge.Doc) (Relation, error) {
	return Classify(ctx, l.Client, a, b)
}

func validKind(k string) bool {
	for _, v := range Kinds {
		if v == k {
			return true
		}
	}
	return false
}

// Relation is the digester's read on an ordered doc pair (A, B): what relation,
// in which direction, how confident, and why. It maps directly to a typed edge.
type Relation struct {
	Kind       string  `json:"kind"`       // one of Kinds, or "none"
	Direction  string  `json:"direction"`  // "a_to_b" | "b_to_a" (ignored when kind=none)
	Confidence float64 `json:"confidence"` // 0..1
	Rationale  string  `json:"rationale"`  // one line, human-readable
}

const systemPrompt = `You are a knowledge-graph relation classifier. Given two documents A and B, decide whether one has a specific relationship to the other, using ONLY this vocabulary:
- supersedes: one document makes the other obsolete or replaces it (e.g. a newer spec/decision overriding an older one)
- contradicts: the two documents make conflicting claims
- elaborates: one document adds detail, depth, or examples to the other
- depends_on: one document requires the other to hold or make sense
- duplicates: the two documents state essentially the same thing
- same_topic: they concern the same subject but with no stronger relation
- none: no meaningful relationship

Return STRICT JSON: {"kind":"<one of the above>","direction":"a_to_b"|"b_to_a","confidence":<0..1>,"rationale":"<short reason>"}.
"direction" names the subject→object order (a_to_b means "A <kind> B"). For symmetric kinds (contradicts, duplicates, same_topic) use a_to_b. Prefer "none" when unsure; be conservative with "supersedes".`

// docBudget caps how much of each document body goes into the prompt — chunks
// are already small, but a stray large record shouldn't blow the context.
const docBudget = 1500

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func buildPrompt(a, b knowledge.Doc) string {
	return fmt.Sprintf("Document A (%s, type=%s):\nTitle: %s\n%s\n\nDocument B (%s, type=%s):\nTitle: %s\n%s",
		a.Slug(), a.Type, a.Title, truncate(a.Content, docBudget),
		b.Slug(), b.Type, b.Title, truncate(b.Content, docBudget))
}

// Classify asks the LLM for the relation between ordered docs A and B. It
// validates the kind against the taxonomy (unknown → none), clamps confidence,
// and normalizes direction. A transport/parse failure is returned as an error
// so the caller can decide whether to abort the pass or skip the pair.
func Classify(ctx context.Context, client *llm.Client, a, b knowledge.Doc) (Relation, error) {
	raw, err := client.GenerateJSON(ctx, systemPrompt, buildPrompt(a, b))
	if err != nil {
		return Relation{}, err
	}
	var rel Relation
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &rel); err != nil {
		return Relation{}, fmt.Errorf("parse relation JSON %q: %w", truncate(raw, 200), err)
	}
	rel.Kind = strings.ToLower(strings.TrimSpace(rel.Kind))
	if rel.Kind == "" || rel.Kind == KindNone || !validKind(rel.Kind) {
		return Relation{Kind: KindNone}, nil
	}
	if rel.Direction != "b_to_a" {
		rel.Direction = "a_to_b"
	}
	if rel.Confidence < 0 {
		rel.Confidence = 0
	}
	if rel.Confidence > 1 {
		rel.Confidence = 1
	}
	return rel, nil
}

// Options tunes a digest pass.
type Options struct {
	K             int     // neighbors per doc to consider as candidates (default 5)
	MinConfidence float64 // edges below this are dropped (default 0.5)
	EnsureKNN     bool    // (re)build the kNN geometry first, so candidates exist
	Limit         int     // stop after classifying this many pairs (0 = no cap)
	Now           func() int64
}

// Report summarizes a digest pass.
type Report struct {
	Candidates   int            // distinct undirected pairs from the kNN geometry
	Skipped      int            // clean pairs (fingerprint unchanged) not re-asked
	Classified   int            // pairs sent to the LLM this pass
	EdgesWritten int            // typed edges created (kind != none, above threshold)
	ByKind       map[string]int // count per relation kind
	Errors       int            // pairs the LLM/parse failed on (skipped, not fatal)
}

// pairKey canonicalizes an undirected pair so (a,b) and (b,a) collapse to one
// candidate — the geometry is directed, but a relation between two docs should
// be classified once. lo is always the smaller id.
func pairKey(a, b int64) (lo, hi int64) {
	if a <= b {
		return a, b
	}
	return b, a
}
