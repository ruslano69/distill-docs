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
	"io"
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
// (Not itself the JSON decode target — see rawRelation in Classify.)
type Relation struct {
	Kind       string  // one of Kinds, or "none"
	Direction  string  // "a_to_b" | "b_to_a" (ignored when kind=none)
	Confidence float64 // 0..1
	Rationale  string  // one line, human-readable
}

// rawRelation is the wire shape of the LLM's answer, with Confidence as a
// pointer so Classify can tell "field absent" from "field explicitly 0" — a
// distinction that matters because a missing confidence must not silently
// become a real 0 (which would make a correct classification vanish below any
// confidence threshold). relationSchema's "required" list is the primary
// defense (it grammar-constrains the endpoint to always include the field);
// this pointer is the fallback in case an endpoint honors "required" loosely.
type rawRelation struct {
	Rationale  string   `json:"rationale"`
	Kind       string   `json:"kind"`
	Direction  string   `json:"direction"`
	Confidence *float64 `json:"confidence"`
}

// relationSchema is the JSON Schema sent as Ollama's structured-output format
// (see llm.Client.GenerateJSON), built from Kinds so the taxonomy has one
// source of truth. Unlike the bare "json" mode, a schema's "required" list
// grammar-constrains generation to include every listed field — the fix for
// the observed failure mode where a model, under loose "json" mode, ended its
// answer after "direction" and never emitted "confidence" at all.
var relationSchema = buildRelationSchema()

func buildRelationSchema() json.RawMessage {
	kinds := make([]string, 0, len(Kinds)+1)
	kinds = append(kinds, Kinds...)
	kinds = append(kinds, KindNone)
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"rationale":  map[string]any{"type": "string"},
			"kind":       map[string]any{"type": "string", "enum": kinds},
			"direction":  map[string]any{"type": "string", "enum": []string{"a_to_b", "b_to_a"}},
			"confidence": map[string]any{"type": "number"},
		},
		"required": []string{"rationale", "kind", "direction", "confidence"},
	}
	b, err := json.Marshal(schema)
	if err != nil {
		panic("digest: relationSchema must marshal: " + err.Error()) // unreachable: static input
	}
	return b
}

const systemPrompt = `You are a knowledge-graph relation classifier. Given two documents A and B — each shown with the date it was added to the knowledge base — decide whether one has a specific relationship to the other, using ONLY this vocabulary:
- supersedes: one document replaces the other or renders it obsolete. Base this ONLY on explicit textual evidence in the SUPERSEDING document — words like "deprecated", "no longer used", "replaces X", "now use Y instead", a decision explicitly overriding an earlier one. The document that CONTAINS that deprecation/replacement language is the one that supersedes; the other is superseded. The added-date is a secondary tiebreaker only — never the primary signal, and a later date alone is NOT evidence of supersedence.
- contradicts: the two documents make conflicting claims
- elaborates: one document adds detail, depth, or examples to the other
- depends_on: one document requires the other to hold or make sense
- duplicates: the two documents state essentially the same thing
- same_topic: they concern the same subject but with no stronger relation
- none: no meaningful relationship

Example: if Document A says "Sort accepts a comparator argument" and Document B says "Sort's comparator argument is removed; use SortBy instead", B supersedes A because B contains the removal language:
{"rationale":"B explicitly says the comparator argument is removed and replaced by SortBy","kind":"supersedes","direction":"b_to_a","confidence":0.9}

Think it through, then respond with STRICT JSON only, in exactly this field order:
{"rationale":"<one sentence: which document is the subject and the specific textual evidence for it>","kind":"<one of the above>","direction":"a_to_b"|"b_to_a","confidence":<0..1>}

"direction" names the subject→object order (a_to_b means "A <kind> B") and MUST match the document you named as the subject in your rationale — never assert a direction that contradicts your own rationale. For symmetric kinds (contradicts, duplicates, same_topic) use a_to_b. Prefer "none" when unsure; be especially conservative with "supersedes" — only assert it when your rationale can cite explicit replacement/deprecation language.`

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

// formatAddedDate renders a doc's CreatedAt as a short date for the prompt's
// secondary temporal signal (see systemPrompt: content evidence is primary, the
// date only breaks ties). A zero timestamp (no CreatedAt available) renders as
// "unknown" rather than the 1970 epoch, so the model doesn't mistake it for data.
func formatAddedDate(unixSeconds int64) string {
	if unixSeconds <= 0 {
		return "unknown"
	}
	return time.Unix(unixSeconds, 0).UTC().Format("2006-01-02")
}

func buildPrompt(a, b knowledge.Doc) string {
	return fmt.Sprintf("Document A (%s, type=%s, added=%s):\nTitle: %s\n%s\n\nDocument B (%s, type=%s, added=%s):\nTitle: %s\n%s",
		a.Slug(), a.Type, formatAddedDate(a.CreatedAt), a.Title, truncate(a.Content, docBudget),
		b.Slug(), b.Type, formatAddedDate(b.CreatedAt), b.Title, truncate(b.Content, docBudget))
}

// Classify asks the LLM for the relation between ordered docs A and B. It
// validates the kind against the taxonomy (unknown → none), clamps confidence,
// and normalizes direction. A transport/parse failure — including a asserted
// relation (kind != none) with no confidence field — is returned as an error so
// the caller retries the pair rather than recording a wrong or absent answer as
// final (see Run: an error leaves the pair unstamped in digest_state).
func Classify(ctx context.Context, client *llm.Client, a, b knowledge.Doc) (Relation, error) {
	raw, err := client.GenerateJSON(ctx, systemPrompt, buildPrompt(a, b), relationSchema)
	if err != nil {
		return Relation{}, err
	}
	var rr rawRelation
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &rr); err != nil {
		return Relation{}, fmt.Errorf("parse relation JSON %q: %w", truncate(raw, 200), err)
	}
	kind := strings.ToLower(strings.TrimSpace(rr.Kind))
	if kind == "" || kind == KindNone || !validKind(kind) {
		return Relation{Kind: KindNone}, nil
	}
	if rr.Confidence == nil {
		return Relation{}, fmt.Errorf("relation %q missing required confidence field in %q", kind, truncate(raw, 200))
	}
	direction := rr.Direction
	if direction != "b_to_a" {
		direction = "a_to_b"
	}
	confidence := *rr.Confidence
	if confidence < 0 {
		confidence = 0
	}
	if confidence > 1 {
		confidence = 1
	}
	return Relation{Kind: kind, Direction: direction, Confidence: confidence, Rationale: rr.Rationale}, nil
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

// PrintReport renders a Report the way every CLI face of the digester
// (single-file `distill digest`, `distill-server digest`) does: JSON to w when
// jsonOut, otherwise a one-line summary plus a per-kind tally. Extracted so
// both binaries share one rendering instead of two copies drifting apart —
// callers may print additional command-specific notes (e.g. a hint about
// `publish`) after calling this.
func PrintReport(w io.Writer, rep Report, jsonOut bool) {
	if jsonOut {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]any{
			"candidates": rep.Candidates, "skipped": rep.Skipped, "classified": rep.Classified,
			"edges_written": rep.EdgesWritten, "errors": rep.Errors, "by_kind": rep.ByKind,
		})
		return
	}
	fmt.Fprintf(w, "digest: %d candidates, %d skipped (clean), %d classified, %d edges written",
		rep.Candidates, rep.Skipped, rep.Classified, rep.EdgesWritten)
	if rep.Errors > 0 {
		fmt.Fprintf(w, ", %d errors", rep.Errors)
	}
	fmt.Fprintln(w)
	for _, kind := range Kinds {
		if n := rep.ByKind[kind]; n > 0 {
			fmt.Fprintf(w, "  %-12s %d\n", kind, n)
		}
	}
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
