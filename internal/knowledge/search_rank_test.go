package knowledge

import (
	"database/sql"
	"strconv"
	"testing"
	"time"
)

// seedRankDoc adds a doc and forces its created_at (Add defaults to now).
func seedRankDoc(t *testing.T, db *sql.DB, title, content, typ, meta string, createdAt int64) int64 {
	t.Helper()
	id, err := Add(db, title, content, typ, meta, nil)
	if err != nil {
		t.Fatalf("Add %q: %v", title, err)
	}
	if _, err := db.Exec(`UPDATE docs SET created_at=? WHERE id=?`, createdAt, id); err != nil {
		t.Fatalf("set created_at: %v", err)
	}
	return id
}

func rankIDs(rs []Result) []int64 {
	out := make([]int64, len(rs))
	for i, r := range rs {
		out[i] = r.ID
	}
	return out
}

func TestSearch_ZeroRankMatchesRetrievalOrder(t *testing.T) {
	db := openDB(t)
	now := int64(1_000_000)
	seedRankDoc(t, db, "A", "alpha one", "note", "{}", now-10)
	seedRankDoc(t, db, "B", "alpha two", "note", "{}", now-20)
	seedRankDoc(t, db, "C", "alpha three", "note", "{}", now-30)

	got, err := Search(db, SearchOpts{Query: "alpha", Mode: "fts", Prefix: true, Now: now})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	want, _ := searchFTSFiltered(db, "alpha", 50, true, Filter{})
	if len(got) != len(want) || len(got) != 3 {
		t.Fatalf("len got=%d want=%d", len(got), len(want))
	}
	for i := range got {
		if got[i].ID != want[i].ID {
			t.Fatalf("zero-rank order differs at %d: got %v want %v", i, rankIDs(got), rankIDs(want))
		}
	}
}

func TestSearch_RecencyWindowLiftsRecent(t *testing.T) {
	db := openDB(t)
	now := int64(1_000_000)
	day := int64(86400)
	seedRankDoc(t, db, "Old1", "alpha content here", "note", "{}", now-100*day)
	seedRankDoc(t, db, "Old2", "alpha content here", "note", "{}", now-60*day)
	fresh := seedRankDoc(t, db, "Fresh", "alpha content here", "note", "{}", now-1*day)

	got, err := Search(db, SearchOpts{
		Query: "alpha", Mode: "fts", Prefix: true, Now: now,
		Rank: RankOpts{RecencyWindow: 30 * 24 * time.Hour, RecencyWeight: 3},
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got[0].ID != fresh {
		t.Errorf("recency: want Fresh(%d) first, got %v", fresh, rankIDs(got))
	}
}

func TestSearch_PriorityAndPinnedBoost(t *testing.T) {
	now := int64(1_000_000)

	db := openDB(t)
	seedRankDoc(t, db, "Plain", "alpha content", "note", "{}", now)
	hi := seedRankDoc(t, db, "HiPrio", "alpha content", "note", `{"priority":1.0}`, now)
	got, _ := Search(db, SearchOpts{Query: "alpha", Mode: "fts", Prefix: true, Now: now,
		Rank: RankOpts{PriorityWeight: 5}})
	if got[0].ID != hi {
		t.Errorf("priority: want HiPrio(%d) first, got %v", hi, rankIDs(got))
	}

	db2 := openDB(t)
	seedRankDoc(t, db2, "Plain", "beta content", "note", "{}", now)
	pin := seedRankDoc(t, db2, "Pinned", "beta content", "note", `{"pinned":1}`, now)
	got2, _ := Search(db2, SearchOpts{Query: "beta", Mode: "fts", Prefix: true, Now: now,
		Rank: RankOpts{PinnedBoost: 2}})
	if got2[0].ID != pin {
		t.Errorf("pinned: want Pinned(%d) first, got %v", pin, rankIDs(got2))
	}
}

func TestSearch_FilterScopes(t *testing.T) {
	db := openDB(t)
	now := int64(1_000_000)
	seedRankDoc(t, db, "AuthDoc", "gamma content", "spec", `{"topic":"auth","role_tags":"backend"}`, now)
	seedRankDoc(t, db, "OpsDoc", "gamma content", "runbook", `{"topic":"deploy","role_tags":"ops"}`, now)

	for _, c := range []struct {
		name   string
		filter Filter
		want   string
	}{
		{"topic", Filter{Topic: "auth"}, "AuthDoc"},
		{"role", Filter{Role: "ops"}, "OpsDoc"},
		{"type", Filter{Type: "spec"}, "AuthDoc"},
	} {
		got, _ := Search(db, SearchOpts{Query: "gamma", Mode: "fts", Prefix: true, Now: now, Filter: c.filter})
		if len(got) != 1 || got[0].Title != c.want {
			t.Errorf("%s filter: want [%s], got %v", c.name, c.want, rankIDs(got))
		}
	}
}

func TestSearch_ExcludeSuperseded(t *testing.T) {
	db := openDB(t)
	now := int64(1_000_000)
	oldDoc := seedRankDoc(t, db, "OldTruth", "delta content", "decision", "{}", now-100)
	meta := `{"supersedes":` + strconv.FormatInt(oldDoc, 10) + `}`
	seedRankDoc(t, db, "NewTruth", "delta content", "decision", meta, now)

	got, _ := Search(db, SearchOpts{Query: "delta", Mode: "fts", Prefix: true, Now: now,
		Rank: RankOpts{ExcludeSuperseded: true}})
	for _, r := range got {
		if r.ID == oldDoc {
			t.Errorf("superseded doc %d should be excluded; got %v", oldDoc, rankIDs(got))
		}
	}
	if len(got) != 1 {
		t.Errorf("want only current truth, got %v", rankIDs(got))
	}
}
