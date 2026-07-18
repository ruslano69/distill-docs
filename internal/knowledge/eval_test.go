package knowledge

import (
	"database/sql"
	"testing"
)

func TestEvaluate(t *testing.T) {
	db := openDB(t)
	a, _ := Add(db, "Auth", "how users authenticate with oauth tokens", "spec", "{}", nil)
	d, _ := Add(db, "Deploy", "kubernetes rollout deployment runbook", "runbook", "{}", nil)
	// slugs: SPEC-<a>, RUNBOOK-<d>
	aSlug := Doc{ID: a, Type: "spec"}.Slug()
	dSlug := Doc{ID: d, Type: "runbook"}.Slug()

	run := func(db *sql.DB) func(string, int) ([]Result, error) {
		return func(q string, k int) ([]Result, error) {
			return Search(db, SearchOpts{Query: q, Mode: "fts", Prefix: true, Limit: k})
		}
	}

	// Perfect golden set: each query's expected doc is the top hit.
	perfect := EvalSet{K: 5, Queries: []EvalCase{
		{Query: "authenticate", Expect: []string{aSlug}},
		{Query: "kubernetes", Expect: []string{dSlug}},
	}}
	rep, err := Evaluate(perfect, run(db))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if rep.HitAtK != 1.0 || rep.MRR != 1.0 {
		t.Errorf("perfect set: hit@k=%.2f mrr=%.2f, want 1.0/1.0", rep.HitAtK, rep.MRR)
	}

	// Wrong expectations: no hits.
	miss := EvalSet{K: 5, Queries: []EvalCase{
		{Query: "authenticate", Expect: []string{dSlug}}, // auth query, expects deploy doc
	}}
	rep2, _ := Evaluate(miss, run(db))
	if rep2.HitAtK != 0 || rep2.MRR != 0 {
		t.Errorf("miss set: hit@k=%.2f mrr=%.2f, want 0/0", rep2.HitAtK, rep2.MRR)
	}
}
