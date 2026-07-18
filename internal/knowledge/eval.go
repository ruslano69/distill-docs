package knowledge

import (
	"encoding/json"
	"fmt"
	"os"
)

// EvalCase is one golden query and the slugs (e.g. "SPEC-3") it should retrieve.
type EvalCase struct {
	Query  string   `json:"query"`
	Expect []string `json:"expect"`
}

// EvalSet is a golden regression set: queries with their expected results,
// scored at cutoff K (default 5).
type EvalSet struct {
	K       int        `json:"k"`
	Queries []EvalCase `json:"queries"`
}

// QueryScore is one query's outcome.
type QueryScore struct {
	Query        string
	Hit          bool    // at least one expected slug in the top-K
	ReciprocalRk float64 // 1/rank of the first expected hit, else 0
}

// EvalReport aggregates a run over an EvalSet.
type EvalReport struct {
	N      int
	HitAtK float64 // fraction of queries with a hit in top-K
	MRR    float64 // mean reciprocal rank of the first expected hit
	Per    []QueryScore
}

// LoadEvalSet reads a golden set from a JSON file.
func LoadEvalSet(path string) (EvalSet, error) {
	var s EvalSet
	data, err := os.ReadFile(path)
	if err != nil {
		return s, fmt.Errorf("read eval set %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("parse eval set: %w", err)
	}
	if s.K <= 0 {
		s.K = 5
	}
	if len(s.Queries) == 0 {
		return s, fmt.Errorf("eval set has no queries")
	}
	return s, nil
}

// Evaluate scores an EvalSet against a retrieval function, returning hit@K and
// MRR. run is the retrieval to test (a Search closure with whatever mode/rank
// the caller chose), so eval measures the exact configuration a release would
// ship. This is the regression net: run it before publishing an index, and
// again after the digester (or a ranking change) touches the graph — a drop in
// the score is the "we changed something and search got worse" alarm.
func Evaluate(set EvalSet, run func(query string, k int) ([]Result, error)) (EvalReport, error) {
	rep := EvalReport{N: len(set.Queries)}
	var hits, mrrSum float64
	for _, c := range set.Queries {
		results, err := run(c.Query, set.K)
		if err != nil {
			return rep, fmt.Errorf("query %q: %w", c.Query, err)
		}
		expect := make(map[string]bool, len(c.Expect))
		for _, e := range c.Expect {
			expect[e] = true
		}
		qs := QueryScore{Query: c.Query}
		for rank, r := range results {
			if rank >= set.K {
				break
			}
			if expect[r.Slug()] {
				qs.Hit = true
				qs.ReciprocalRk = 1.0 / float64(rank+1)
				break
			}
		}
		if qs.Hit {
			hits++
		}
		mrrSum += qs.ReciprocalRk
		rep.Per = append(rep.Per, qs)
	}
	if rep.N > 0 {
		rep.HitAtK = hits / float64(rep.N)
		rep.MRR = mrrSum / float64(rep.N)
	}
	return rep, nil
}
