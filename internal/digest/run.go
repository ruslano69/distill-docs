package digest

import (
	"context"
	"database/sql"
	"sort"

	"github.com/ruslano69/distill-docs/internal/knowledge"
)

// candidatePairs reads the kNN geometry and returns the distinct undirected
// pairs it connects, sorted (lo,hi) for deterministic, resumable iteration.
func candidatePairs(db *sql.DB) ([][2]int64, error) {
	rows, err := db.Query(`SELECT src, dst FROM edges WHERE kind='knn'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[[2]int64]bool{}
	for rows.Next() {
		var s, d int64
		if err := rows.Scan(&s, &d); err != nil {
			return nil, err
		}
		lo, hi := pairKey(s, d)
		if lo != hi {
			seen[[2]int64{lo, hi}] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	pairs := make([][2]int64, 0, len(seen))
	for p := range seen {
		pairs = append(pairs, p)
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i][0] != pairs[j][0] {
			return pairs[i][0] < pairs[j][0]
		}
		return pairs[i][1] < pairs[j][1]
	})
	return pairs, nil
}

// Classifier is the subset of behavior the run loop needs — an interface so
// tests can drive Run with a stub instead of a fake HTTP server. LLMClassifier
// is the production implementation.
type Classifier interface {
	Enabled() bool
	Model() string
	Classify(ctx context.Context, a, b knowledge.Doc) (Relation, error)
}

// Run executes one digest pass: it (optionally) rebuilds the kNN geometry, then
// for every undirected candidate pair whose content fingerprint has changed
// since last time, asks the LLM to classify the relation and writes the typed
// edge (as "proposed") plus a digest_state stamp. Idempotent and resumable:
// re-running with no content change is all skips and no LLM calls.
func Run(ctx context.Context, db *sql.DB, c Classifier, opts Options) (Report, error) {
	if opts.K <= 0 {
		opts.K = 5
	}
	if opts.MinConfidence == 0 {
		opts.MinConfidence = 0.5
	}
	now := opts.Now
	if now == nil {
		now = func() int64 { return timeNow() }
	}
	rep := Report{ByKind: map[string]int{}}

	if !c.Enabled() {
		return rep, errDisabled
	}
	if opts.EnsureKNN {
		if _, err := knowledge.BuildKNNEdges(db, opts.K); err != nil {
			return rep, err
		}
	}

	pairs, err := candidatePairs(db)
	if err != nil {
		return rep, err
	}
	rep.Candidates = len(pairs)

	for _, p := range pairs {
		lo, hi := p[0], p[1]
		a, err := knowledge.DocByID(db, lo)
		if err == sql.ErrNoRows {
			continue // doc deleted since the geometry was built
		} else if err != nil {
			return rep, err
		}
		b, err := knowledge.DocByID(db, hi)
		if err == sql.ErrNoRows {
			continue
		} else if err != nil {
			return rep, err
		}

		fp := knowledge.PairFingerprint(a, b)
		if prev, ok, err := knowledge.DigestFingerprint(db, lo, hi); err != nil {
			return rep, err
		} else if ok && prev == fp {
			rep.Skipped++
			continue
		}

		rel, err := c.Classify(ctx, a, b)
		if err != nil {
			// Transient LLM/parse failure: leave the pair unstamped so the next
			// pass retries it, and keep going rather than aborting the batch.
			rep.Errors++
			continue
		}
		rep.Classified++

		if err := writePair(db, a, b, rel, opts.MinConfidence, c.Model(), now(), &rep); err != nil {
			return rep, err
		}

		if opts.Limit > 0 && rep.Classified >= opts.Limit {
			break
		}
	}
	return rep, nil
}

// writePair applies one classification atomically: clear any stale typed edge
// between the pair, write the new one if it clears the confidence bar, and stamp
// the pair as digested — all in a single transaction so a crash never leaves a
// stamped-but-edgeless (or edged-but-unstamped) pair.
func writePair(db *sql.DB, a, b knowledge.Doc, rel Relation, minConf float64, model string, now int64, rep *Report) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// A re-digest may have found a *different* (or no) relation than before;
	// drop whatever typed edge previously connected this pair, either direction.
	if _, err := tx.Exec(
		`DELETE FROM edges WHERE kind!='knn' AND ((src=? AND dst=?) OR (src=? AND dst=?))`,
		a.ID, b.ID, b.ID, a.ID); err != nil {
		return err
	}

	if rel.Kind != KindNone && rel.Confidence >= minConf {
		src, dst := a.ID, b.ID
		if rel.Direction == "b_to_a" {
			src, dst = b.ID, a.ID
		}
		e := knowledge.Edge{
			Src: src, Dst: dst, Weight: rel.Confidence, Kind: rel.Kind,
			Status: "proposed", Rationale: rel.Rationale, Model: model, UpdatedAt: now,
		}
		if err := knowledge.UpsertTypedEdge(tx, e); err != nil {
			return err
		}
		rep.EdgesWritten++
		rep.ByKind[rel.Kind]++
	}

	lo, hi := pairKey(a.ID, b.ID)
	if err := knowledge.MarkDigested(tx, lo, hi, knowledge.PairFingerprint(a, b), now); err != nil {
		return err
	}
	return tx.Commit()
}
