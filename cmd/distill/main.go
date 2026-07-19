package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ruslano69/distill-docs/internal/digest"
	"github.com/ruslano69/distill-docs/internal/docmeta"
	"github.com/ruslano69/distill-docs/internal/embed"
	"github.com/ruslano69/distill-docs/internal/knowledge"
	"github.com/ruslano69/distill-docs/internal/llm"
	"github.com/ruslano69/distill-docs/internal/version"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	// Global --db flag before the action.
	globalFS := flag.NewFlagSet("distill", flag.ContinueOnError)
	dbPath := globalFS.String("db", ".knowledge/docs.sqlite", "path to SQLite knowledge base")
	globalFS.Usage = printUsage

	// Handle --version before action parsing.
	for _, a := range os.Args[1:] {
		if a == "--version" || a == "-version" {
			version.PrintVersion("distill")
			return
		}
	}

	// Collect args up to (not including) the action word.
	var preAction, postAction []string
	foundAction := false
	actions := map[string]bool{"init": true, "add": true, "search": true, "count": true, "eval": true, "digest": true, "graph": true}
	for i, a := range os.Args[1:] {
		if actions[a] {
			preAction = os.Args[1 : i+1]
			postAction = os.Args[i+2:]
			foundAction = true
			break
		}
	}
	if !foundAction {
		printUsage()
		os.Exit(1)
	}
	action := os.Args[len(preAction)+1]

	if err := globalFS.Parse(preAction); err != nil {
		os.Exit(1)
	}

	switch action {
	case "init":
		runInit(*dbPath)
	case "add":
		runAdd(*dbPath, postAction)
	case "search":
		runSearch(*dbPath, postAction)
	case "count":
		runCount(*dbPath, postAction)
	case "eval":
		runEval(*dbPath, postAction)
	case "digest":
		runDigest(*dbPath, postAction)
	case "graph":
		runGraph(*dbPath, postAction)
	default:
		fatalf("unknown action %q", action)
	}
}

// runEval scores a golden query set (JSON: {"k":N,"queries":[{"query":...,
// "expect":["SPEC-3",...]}]}) against the knowledge base — the retrieval
// regression net. Run it before shipping an index and after any ranking/graph
// change; a drop in hit@k / MRR is the alarm.
func runEval(dbPath string, args []string) {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	golden := fs.String("golden", "", "path to the golden eval set (JSON, required)")
	mode := fs.String("mode", "hybrid", "search mode to evaluate: fts|vec|hybrid")
	embedModel := fs.String("embed-model", "", "auto-embed queries via an Ollama model (for vec/hybrid)")
	embedURL := fs.String("embed-url", embed.DefaultURL, "embeddings endpoint")
	jsonOut := fs.Bool("json", false, "output JSON")
	fs.Parse(args)

	if *golden == "" {
		fatalf("--golden required")
	}
	set, err := knowledge.LoadEvalSet(*golden)
	if err != nil {
		fatalf("%v", err)
	}
	db, err := knowledge.Open(dbPath)
	if err != nil {
		fatalf("open db: %v", err)
	}
	defer db.Close()

	ec := embed.New(*embedURL, *embedModel)
	run := func(q string, k int) ([]knowledge.Result, error) {
		var emb []float32
		if *mode == "vec" || *mode == "hybrid" {
			emb = embedOne(ec, q)
		}
		return knowledge.Search(db, knowledge.SearchOpts{
			Query: q, Embedding: emb, Mode: *mode, Metric: knowledge.MetricCosine, Limit: k, Prefix: true,
		})
	}
	rep, err := knowledge.Evaluate(set, run)
	if err != nil {
		fatalf("evaluate: %v", err)
	}

	if *jsonOut {
		json.NewEncoder(os.Stdout).Encode(map[string]any{
			"queries": rep.N, "k": set.K, "hit_at_k": rep.HitAtK, "mrr": rep.MRR})
		return
	}
	fmt.Printf("eval: %d queries @k=%d  hit@k=%.4f  MRR=%.4f  (mode=%s)\n",
		rep.N, set.K, rep.HitAtK, rep.MRR, *mode)
	for _, qs := range rep.Per {
		mark := "miss"
		if qs.Hit {
			mark = fmt.Sprintf("hit rr=%.3f", qs.ReciprocalRk)
		}
		fmt.Printf("  [%-4s] %s\n", mark, qs.Query)
	}
}

// runDigest runs the L2 knowledge-graph digester: (re)build the kNN geometry,
// then ask a local LLM to classify each candidate pair into a typed relation
// (supersedes/contradicts/...). Incremental and resumable — a pair whose
// content is unchanged since last digest is skipped. Requires --model (the
// Ollama generate model). Vectors must already be stored (digest reads the kNN
// geometry, which comes from docs_vec); if none exist there are no candidates.
func runDigest(dbPath string, args []string) {
	fs := flag.NewFlagSet("digest", flag.ExitOnError)
	model := fs.String("model", "", "Ollama generate model for classification (e.g. gemma4:12b) — required")
	llmURL := fs.String("llm-url", llm.DefaultURL, "generate endpoint")
	k := fs.Int("k", 5, "neighbors per doc considered as relation candidates")
	minConf := fs.Float64("min-confidence", 0.5, "drop proposed edges below this confidence")
	limit := fs.Int("limit", 0, "stop after classifying this many pairs (0 = all)")
	rebuildKNN := fs.Bool("rebuild-knn", true, "rebuild the kNN geometry before digesting")
	jsonOut := fs.Bool("json", false, "output JSON")
	fs.Parse(args)

	if *model == "" {
		fatalf("--model required (e.g. --model gemma4:12b)")
	}

	db, err := knowledge.Open(dbPath)
	if err != nil {
		fatalf("open db: %v", err)
	}
	defer db.Close()

	client := digest.LLMClassifier{Client: llm.New(*llmURL, *model)}
	if !*jsonOut {
		fmt.Fprintf(os.Stderr, "digesting with %s (k=%d, min-confidence=%.2f)…\n", *model, *k, *minConf)
	}
	rep, err := digest.Run(context.Background(), db, client, digest.Options{
		K: *k, MinConfidence: *minConf, EnsureKNN: *rebuildKNN, Limit: *limit,
	})
	if err != nil {
		fatalf("digest: %v", err)
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]any{
			"candidates": rep.Candidates, "skipped": rep.Skipped, "classified": rep.Classified,
			"edges_written": rep.EdgesWritten, "errors": rep.Errors, "by_kind": rep.ByKind,
		})
		return
	}
	fmt.Printf("digest: %d candidates, %d skipped (clean), %d classified, %d edges written",
		rep.Candidates, rep.Skipped, rep.Classified, rep.EdgesWritten)
	if rep.Errors > 0 {
		fmt.Printf(", %d errors", rep.Errors)
	}
	fmt.Println()
	for _, kind := range digest.Kinds {
		if n := rep.ByKind[kind]; n > 0 {
			fmt.Printf("  %-12s %d\n", kind, n)
		}
	}
	if rep.Candidates == 0 {
		fmt.Fprintln(os.Stderr, "note: no candidates — the kNN geometry is empty (ingest with --embed-model to store vectors)")
	}
}

// runGraph prints one doc's typed L2 relations as legible chains — the
// graph-response view that trades prose for structure. Resolve a doc by slug
// (SPEC-42) and show its outgoing typed edges (supersedes/elaborates/...) with
// confidence, status, and the digester's rationale.
func runGraph(dbPath string, args []string) {
	fs := flag.NewFlagSet("graph", flag.ExitOnError)
	slug := fs.String("slug", "", "doc slug to inspect, e.g. SPEC-42 (or pass as a bare arg)")
	limit := fs.Int("limit", 20, "max relations to show")
	jsonOut := fs.Bool("json", false, "output JSON")

	// Accept the slug as a bare positional in any position. Go's flag package
	// stops parsing at the first non-flag token, so `graph SPEC-42 --json` would
	// otherwise silently drop --json. Pull the first bare token out as the slug
	// and flag-parse the remainder.
	var positional string
	rest := make([]string, 0, len(args))
	for _, a := range args {
		if positional == "" && a != "" && a[0] != '-' {
			positional = a
			continue
		}
		rest = append(rest, a)
	}
	fs.Parse(rest)

	target := *slug
	if target == "" {
		target = positional
	}
	if target == "" {
		fatalf("--slug required (e.g. distill graph SPEC-42)")
	}
	id, err := knowledge.ParseSlugID(target)
	if err != nil {
		fatalf("%v", err)
	}

	db, err := knowledge.Open(dbPath)
	if err != nil {
		fatalf("open db: %v", err)
	}
	defer db.Close()

	doc, err := knowledge.DocByID(db, id)
	if err != nil {
		fatalf("load %s: %v", target, err)
	}
	edges, err := knowledge.TypedNeighbors(db, id, *limit)
	if err != nil {
		fatalf("graph: %v", err)
	}

	if *jsonOut {
		type rel struct {
			Kind, Status, Target, Rationale, Model string
			Confidence                             float64
		}
		rels := make([]rel, 0, len(edges))
		for _, e := range edges {
			dst, _ := knowledge.DocByID(db, e.Dst)
			rels = append(rels, rel{
				Kind: e.Kind, Status: e.Status, Target: dst.Slug(),
				Rationale: e.Rationale, Model: e.Model, Confidence: e.Weight,
			})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]any{"slug": doc.Slug(), "title": doc.Title, "relations": rels})
		return
	}

	fmt.Printf("%s  %s\n", doc.Slug(), doc.Title)
	if len(edges) == 0 {
		fmt.Println("  (no typed relations — run `distill digest --model <m>` to build the L2 graph)")
		return
	}
	for _, e := range edges {
		dst, err := knowledge.DocByID(db, e.Dst)
		dstLabel := fmt.Sprintf("id-%d", e.Dst)
		if err == nil {
			dstLabel = fmt.Sprintf("%s %s", dst.Slug(), truncate(dst.Title, 40))
		}
		fmt.Printf("  → %-12s → %s  [%s, conf %.2f]\n", e.Kind, dstLabel, e.Status, e.Weight)
		if e.Rationale != "" {
			fmt.Printf("      %q\n", truncate(e.Rationale, 100))
		}
	}
}

func runInit(dbPath string) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		fatalf("mkdir: %v", err)
	}
	db, err := knowledge.Open(dbPath)
	if err != nil {
		fatalf("open: %v", err)
	}
	db.Close()
	fmt.Fprintf(os.Stderr, "knowledge base ready: %s\n", dbPath)
}

func runAdd(dbPath string, args []string) {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	title := fs.String("title", "", "document title (required without --file)")
	content := fs.String("content", "", "document content (required without --file)")
	file := fs.String("file", "", "ingest a .txt/.md/.pdf file (splits into chunks)")
	session := fs.String("session", "", "ingest a Claude Code transcript .jsonl (per-turn, historical timestamps)")
	urlFlag := fs.String("url", "", "crawl a documentation website and ingest all pages")
	maxPages := fs.Int("max-pages", 200, "max pages to crawl (with --url)")
	chunkSize := fs.Int("chunk-size", 800, "max chunk size in runes (with --file)")
	chunkOverlap := fs.Int("chunk-overlap", 80, "overlap runes between chunks (with --file)")
	docType := fs.String("type", "general", "document type: general|tool_usage|error|scenario")
	meta := fs.String("meta", "{}", "raw metadata JSON base; the structured flags below overlay it")
	author := fs.String("author", "", "author (provenance)")
	rank := docmeta.RegisterRankFlags(fs)
	embeddingRaw := fs.String("embedding", "", "comma-separated float32 values (single doc only; overrides --embed-model)")
	embedModel := fs.String("embed-model", "", "auto-embed ingested text via an Ollama model (e.g. qwen3-embedding:0.6b); enables vec/hybrid search. Empty = BYO/FTS only")
	embedURL := fs.String("embed-url", embed.DefaultURL, "embeddings endpoint (with --embed-model)")
	jsonOut := fs.Bool("json", false, "output JSON")
	fs.Parse(args)

	// Structured ranking/provenance flags overlay the raw --meta base, so
	// `--topic auth --pinned` and `--meta '{"custom":"x"}'` compose (the shared
	// docmeta binder — same metadata contract as distill-server).
	dm := docmeta.Meta{Author: *author}
	rank.Apply(&dm)
	metaStr, err := docmeta.Merge(*meta, dm)
	if err != nil {
		fatalf("%v", err)
	}

	ec := embed.New(*embedURL, *embedModel)

	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		fatalf("mkdir: %v", err)
	}
	db, err := knowledge.Open(dbPath)
	if err != nil {
		fatalf("open db: %v", err)
	}
	defer db.Close()

	if *urlFlag != "" {
		opts := knowledge.CrawlOpts{
			MaxPages:  *maxPages,
			ChunkOpts: knowledge.ChunkOpts{MaxRunes: *chunkSize, OverlapRunes: *chunkOverlap},
		}
		var fetched int
		progress := func(done, queued int, pageURL string) {
			fetched = done
			if !*jsonOut {
				fmt.Fprintf(os.Stderr, "\r  crawling [%d fetched, %d queued] %s", done, queued, truncate(pageURL, 60))
			}
		}
		chunks, err := knowledge.IngestWeb(*urlFlag, opts, progress)
		if !*jsonOut {
			fmt.Fprintln(os.Stderr) // newline after progress line
		}
		if err != nil {
			fatalf("crawl: %v", err)
		}
		vecs := embedChunks(ec, chunks)
		var ids []int64
		for i, ch := range chunks {
			id, err := knowledge.Add(db, ch.Title, ch.Content, *docType, metaStr, chunkVec(vecs, i))
			if err != nil {
				fatalf("add chunk: %v", err)
			}
			ids = append(ids, id)
		}
		if *jsonOut {
			json.NewEncoder(os.Stdout).Encode(map[string]any{"ids": ids, "chunks": len(ids), "pages": fetched})
		} else {
			fmt.Fprintf(os.Stderr, "ingested %d chunks from %d pages (%s)\n", len(ids), fetched, *urlFlag)
		}
		return
	}

	if *file != "" {
		opts := knowledge.ChunkOpts{MaxRunes: *chunkSize, OverlapRunes: *chunkOverlap}
		chunks, err := knowledge.IngestFile(*file, opts)
		if err != nil {
			var ocrErr *knowledge.OCRQualityError
			if errors.As(err, &ocrErr) {
				if *jsonOut {
					json.NewEncoder(os.Stdout).Encode(map[string]any{
						"error": "bad_ocr",
						"score": ocrErr.Score,
						"file":  ocrErr.Path,
					})
					os.Exit(2)
				}
				fmt.Fprintf(os.Stderr, "skipped: bad OCR quality in %s (score %.2f)\n", ocrErr.Path, ocrErr.Score)
				fmt.Fprintf(os.Stderr, "hint: run OCR correction (e.g. ocrmypdf) before indexing\n")
				os.Exit(2)
			}
			fatalf("ingest: %v", err)
		}
		vecs := embedChunks(ec, chunks)
		var ids []int64
		for i, ch := range chunks {
			id, err := knowledge.Add(db, ch.Title, ch.Content, *docType, metaStr, chunkVec(vecs, i))
			if err != nil {
				fatalf("add chunk: %v", err)
			}
			ids = append(ids, id)
		}
		if *jsonOut {
			json.NewEncoder(os.Stdout).Encode(map[string]any{"ids": ids, "chunks": len(ids)})
		} else {
			fmt.Fprintf(os.Stderr, "ingested %d chunks from %s\n", len(ids), *file)
		}
		return
	}

	if *session != "" {
		runAddSession(db, ec, *session, knowledge.ChunkOpts{MaxRunes: *chunkSize, OverlapRunes: *chunkOverlap}, *jsonOut)
		return
	}

	if *title == "" || *content == "" {
		fatalf("--title and --content are required (or use --file)")
	}
	emb := parseEmbedding(*embeddingRaw)
	if len(emb) == 0 {
		emb = embedOne(ec, *content)
	}
	id, err := knowledge.Add(db, *title, *content, *docType, metaStr, emb)
	if err != nil {
		fatalf("add: %v", err)
	}
	if *jsonOut {
		json.NewEncoder(os.Stdout).Encode(map[string]any{"id": id})
	} else {
		fmt.Fprintf(os.Stderr, "added id=%d\n", id)
	}
}

// runAddSession ingests a Claude Code session transcript (.jsonl): each turn
// (user prompt, assistant reply, assistant thinking) becomes one or more docs,
// typed by role so the three "slices of knowledge" stay separable — `--type
// thinking` isolates reasoning, `--type assistant` the answers. Crucially it
// preserves each turn's real timestamp (AddAt), so recency ranking and
// chronology reflect when things were actually said, not import time.
func runAddSession(db *sql.DB, ec *embed.Client, path string, opts knowledge.ChunkOpts, jsonOut bool) {
	turns, err := knowledge.ParseSession(path)
	if err != nil {
		fatalf("%v", err)
	}
	chunks := knowledge.ChunkSessionTurns(turns, opts)
	if len(chunks) == 0 {
		fatalf("no conversational turns found in %s", path)
	}

	// Reuse the batched embedder: it degrades to no-vectors on failure.
	kc := make([]knowledge.Chunk, len(chunks))
	for i, c := range chunks {
		kc[i] = knowledge.Chunk{Content: c.Content}
	}
	vecs := embedChunks(ec, kc)

	byKind := map[string]int{}
	var minTS, maxTS int64
	ids := make([]int64, 0, len(chunks))
	for i, c := range chunks {
		id, err := knowledge.AddAt(db, c.Title, c.Content, c.Turn.Kind, c.Turn.Metadata(), chunkVec(vecs, i), c.Turn.Timestamp)
		if err != nil {
			fatalf("add turn: %v", err)
		}
		ids = append(ids, id)
		byKind[c.Turn.Kind]++
		if ts := c.Turn.Timestamp; ts > 0 {
			if minTS == 0 || ts < minTS {
				minTS = ts
			}
			if ts > maxTS {
				maxTS = ts
			}
		}
	}

	if jsonOut {
		json.NewEncoder(os.Stdout).Encode(map[string]any{
			"docs": len(ids), "turns": len(turns), "by_kind": byKind,
			"from": minTS, "to": maxTS,
		})
		return
	}
	fmt.Fprintf(os.Stderr, "ingested %d docs from %d turns (%s)\n", len(ids), len(turns), filepath.Base(path))
	for _, k := range []string{"user", "assistant", "thinking"} {
		if n := byKind[k]; n > 0 {
			fmt.Fprintf(os.Stderr, "  %-10s %d\n", k, n)
		}
	}
	if minTS > 0 {
		fmt.Fprintf(os.Stderr, "  span       %s → %s\n",
			time.Unix(minTS, 0).UTC().Format("2006-01-02 15:04"),
			time.Unix(maxTS, 0).UTC().Format("2006-01-02 15:04"))
	}
}

func runSearch(dbPath string, args []string) {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	query := fs.String("query", "", "FTS/regex query string")
	embeddingRaw := fs.String("embedding", "", "comma-separated float32 values (overrides --embed-model)")
	embedModel := fs.String("embed-model", "", "auto-embed the query via an Ollama model (e.g. qwen3-embedding:0.6b) for vec/hybrid. Empty = BYO/FTS only")
	embedURL := fs.String("embed-url", embed.DefaultURL, "embeddings endpoint (with --embed-model)")
	mode := fs.String("mode", "hybrid", "search mode: fts|vec|hybrid|regex")
	metricRaw := fs.String("metric", "cosine", "distance metric: cosine|l2 (vec/hybrid modes)")
	filterType := fs.String("filter-type", "", "pre-filter by document type before vector or regex search")
	limit := fs.Int("limit", 10, "maximum results")
	prefix := fs.Bool("prefix", true, "auto-append wildcard to FTS tokens (e.g. call → call*)")
	// Stage-1 facet filters + ranking signals; all optional, zero = off.
	topic := fs.String("topic", "", "scope to a topic facet")
	role := fs.String("role", "", "scope to (and, with --role-affinity, boost) a role")
	recencyWindow := fs.Duration("recency-window", 0, "linear recency window, e.g. 720h (0 = off)")
	recencyWeight := fs.Float64("recency-weight", 0, "recency boost weight")
	priorityWeight := fs.Float64("priority-weight", 0, "weight on the numeric priority attribute")
	pinnedBoost := fs.Float64("pinned-boost", 0, "additive boost for pinned docs")
	roleAffinity := fs.Float64("role-affinity", 0, "additive boost when role_tags include --role")
	excludeSuperseded := fs.Bool("exclude-superseded", false, "drop docs another doc supersedes")
	graph := fs.Int("graph", 0, "graph-aware: annotate each hit with up to N typed L2 relations (0 = off)")
	cluster := fs.Bool("cluster", false, "collapse duplicate hits into their top-ranked representative")
	jsonOut := fs.Bool("json", false, "output JSON")
	fs.Parse(args)

	if *query == "" && *embeddingRaw == "" {
		fatalf("--query or --embedding required")
	}

	metric := knowledge.MetricCosine
	if *metricRaw == "l2" {
		metric = knowledge.MetricL2
	}

	db, err := knowledge.Open(dbPath)
	if err != nil {
		fatalf("open db: %v", err)
	}
	defer db.Close()

	emb := parseEmbedding(*embeddingRaw)
	if len(emb) == 0 && *query != "" {
		emb = embedOne(embed.New(*embedURL, *embedModel), *query)
	}
	var results []knowledge.Result
	if *mode == "regex" {
		if *query == "" {
			fatalf("--query required for regex mode")
		}
		results, err = knowledge.SearchRegex(db, *query, *limit, *filterType)
	} else {
		if *mode == "vec" && len(emb) == 0 {
			fatalf("--embedding (or --embed-model) required for vec mode")
		}
		results, err = knowledge.Search(db, knowledge.SearchOpts{
			Query: *query, Embedding: emb, Mode: *mode, Metric: metric,
			Limit: *limit, Prefix: *prefix,
			Filter: knowledge.Filter{Type: *filterType, Role: *role, Topic: *topic},
			Rank: knowledge.RankOpts{
				RecencyWindow: *recencyWindow, RecencyWeight: *recencyWeight,
				PriorityWeight: *priorityWeight, PinnedBoost: *pinnedBoost,
				RoleAffinity: *roleAffinity, ExcludeSuperseded: *excludeSuperseded,
			},
			GraphExpand: *graph,
			Cluster:     *cluster,
		})
	}
	if err != nil {
		fatalf("search: %v", err)
	}
	printResults(results, *jsonOut)
}

func runCount(dbPath string, args []string) {
	fs := flag.NewFlagSet("count", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "output JSON")
	fs.Parse(args)

	db, err := knowledge.Open(dbPath)
	if err != nil {
		fatalf("open db: %v", err)
	}
	defer db.Close()

	n, err := knowledge.Count(db)
	if err != nil {
		fatalf("count: %v", err)
	}
	if *jsonOut {
		json.NewEncoder(os.Stdout).Encode(map[string]any{"count": n})
	} else {
		fmt.Println(n)
	}
}

// embedChunks embeds every chunk's content in one batch request when a model
// is configured. On any failure it warns once and returns nil, so ingestion
// degrades to stored-without-vectors (FTS still works) rather than aborting a
// large crawl/file over a transient embedder hiccup.
// embedBatchSize bounds how many chunks go in one embedding request, so a
// large ingest (a whole PDF = thousands of chunks) doesn't send one giant
// request that blows the embedder's per-request timeout. A failed batch
// degrades just its own chunks to no-vector (FTS still works for them),
// leaving the rest embedded — resilient rather than all-or-nothing.
const embedBatchSize = 128

func embedChunks(ec *embed.Client, chunks []knowledge.Chunk) [][]float32 {
	if !ec.Enabled() || len(chunks) == 0 {
		return nil
	}
	vecs := make([][]float32, len(chunks))
	warned := false
	for start := 0; start < len(chunks); start += embedBatchSize {
		end := start + embedBatchSize
		if end > len(chunks) {
			end = len(chunks)
		}
		texts := make([]string, end-start)
		for i := start; i < end; i++ {
			texts[i-start] = chunks[i].Content
		}
		batch, err := ec.EmbedBatch(texts)
		if err != nil {
			if !warned {
				fmt.Fprintf(os.Stderr, "warn: embedding batch failed (%v); those chunks stored without vectors (FTS only)\n", err)
				warned = true
			}
			continue // leave vecs[start:end] nil
		}
		copy(vecs[start:end], batch)
	}
	return vecs
}

// chunkVec safely indexes a (possibly nil) batch-embedding result.
func chunkVec(vecs [][]float32, i int) []float32 {
	if vecs == nil || i >= len(vecs) {
		return nil
	}
	return vecs[i]
}

// embedOne embeds a single text when a model is configured, degrading to nil
// (no vector / FTS fallback) with a warning on failure rather than crashing.
func embedOne(ec *embed.Client, text string) []float32 {
	if !ec.Enabled() {
		return nil
	}
	v, err := ec.Embed(text)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: embedding failed (%v); falling back to FTS\n", err)
		return nil
	}
	return v
}

func parseEmbedding(raw string) []float32 {
	raw = strings.TrimSpace(strings.Trim(strings.TrimSpace(raw), "[]"))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]float32, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		f, err := strconv.ParseFloat(p, 32)
		if err != nil {
			fatalf("invalid embedding value %q: %v", p, err)
		}
		out = append(out, float32(f))
	}
	return out
}

func printResults(results []knowledge.Result, asJSON bool) {
	if asJSON {
		type jsonRelation struct {
			Kind     string  `json:"kind"`
			Outgoing bool    `json:"outgoing"`
			Target   string  `json:"target"`
			Weight   float64 `json:"weight"`
			Status   string  `json:"status"`
		}
		type jsonResult struct {
			ID           int64          `json:"id"`
			Slug         string         `json:"slug"`
			Title        string         `json:"title"`
			Content      string         `json:"content"`
			Type         string         `json:"type"`
			CreatedAt    int64          `json:"created_at"`
			Metadata     string         `json:"metadata"`
			Snippet      string         `json:"snippet,omitempty"`
			FTSRank      float64        `json:"fts_rank,omitempty"`
			VecDist      float64        `json:"vec_dist,omitempty"`
			HybridScore  float64        `json:"hybrid_score,omitempty"`
			Score        float64        `json:"score,omitempty"`
			Superseded   bool           `json:"superseded,omitempty"`
			Contradicted bool           `json:"contradicted,omitempty"`
			Relations    []jsonRelation `json:"relations,omitempty"`
			Folded       []string       `json:"folded,omitempty"`
		}
		out := make([]jsonResult, len(results))
		for i, r := range results {
			var rels []jsonRelation
			for _, rel := range r.Relations {
				rels = append(rels, jsonRelation{rel.Kind, rel.Outgoing, rel.TargetSlug, rel.Weight, rel.Status})
			}
			out[i] = jsonResult{
				ID:           r.ID,
				Slug:         r.Slug(),
				Title:        r.Title,
				Content:      r.Content,
				Type:         r.Type,
				CreatedAt:    r.CreatedAt,
				Metadata:     r.Metadata,
				Snippet:      r.Snippet,
				FTSRank:      r.FTSRank,
				VecDist:      r.VecDist,
				HybridScore:  r.HybridScore,
				Score:        r.Score,
				Superseded:   r.Superseded(),
				Contradicted: r.Contradicted(),
				Relations:    rels,
				Folded:       r.Folded,
			}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(out)
		return
	}
	for _, r := range results {
		// A "don't ground on this" banner when the graph says the hit is
		// obsolete or disputed (only populated when --graph is on).
		var warn string
		if r.Superseded() {
			warn += "  ⚠ superseded"
		}
		if r.Contradicted() {
			warn += "  ⚠ contradicted"
		}
		if n := len(r.Folded); n > 0 {
			warn += fmt.Sprintf("  ⊕ %d dup", n)
		}
		// Preview() shows the keyword-in-context snippet when present (rune-safe,
		// never a byte-slice that could split a multi-byte character), falling
		// back to a rune-safe content head otherwise.
		fmt.Printf("[%s] %s  (%s)%s\n    %s\n", r.Slug(), r.Title, r.Type, warn, r.Preview(120))
		if len(r.Folded) > 0 {
			fmt.Printf("      ⊕ folds %s (duplicates)\n", strings.Join(r.Folded, ", "))
		}
		for _, rel := range r.Relations {
			arrow := fmt.Sprintf("→ %s → %s", rel.Kind, rel.TargetSlug)
			if !rel.Outgoing {
				arrow = fmt.Sprintf("← %s ← %s", rel.Kind, rel.TargetSlug)
			}
			fmt.Printf("      %s  [%s, %.2f]\n", arrow, rel.Status, rel.Weight)
		}
		fmt.Println()
	}
}

func printUsage() {
	fmt.Fprint(os.Stderr, `distill — knowledge base with FTS5 + vector hybrid search

Actions:
  init    Create or verify the knowledge base
  add     Add a document
  search  Search documents
  count   Print total document count
  digest  Build the L2 typed knowledge graph (LLM classifies kNN neighbors)
  graph   Show a doc's typed relations (supersedes/elaborates/...)

Usage:
  distill [--db <path>] init
  distill [--db <path>] add    --title <t> --content <c> [--type <t>] [--meta <json>] [--embedding <floats> | --embed-model <m>] [--json]
  distill [--db <path>] add    --file <path.txt|md|pdf>  [--type <t>] [--chunk-size N] [--chunk-overlap N] [--embed-model <m>] [--json]
  distill [--db <path>] add    --url  <https://...>      [--type <t>] [--chunk-size N] [--max-pages N] [--embed-model <m>] [--json]
  distill [--db <path>] search --query <q>               [--embedding <floats> | --embed-model <m>] [--mode fts|vec|hybrid|regex] [--type --topic --role] [--recency-window --recency-weight --priority-weight --pinned-boost --role-affinity --exclude-superseded] [--limit N] [--json]
  distill [--db <path>] count  [--json]
  distill [--db <path>] eval   --golden <set.json>       [--mode fts|vec|hybrid] [--embed-model <m>] [--json]   (retrieval regression: hit@k + MRR)
  distill [--db <path>] digest --model <ollama-model>    [--k N] [--min-confidence F] [--limit N] [--rebuild-knn] [--json]   (L2 typed graph)
  distill [--db <path>] graph  <SLUG>                    [--limit N] [--json]   (typed relations of one doc)

Default --db: .knowledge/docs.sqlite
Embedding format: comma-separated float32 values, e.g. "0.1,0.2,0.3" or "[0.1,0.2,0.3]"
Auto-embedding: --embed-model <ollama-model> (e.g. qwen3-embedding:0.6b) embeds text at add/search
  time via --embed-url (default `+embed.DefaultURL+`); enables real vec/hybrid without hand-passing
  --embedding. Embed add and search with the SAME model. Degrades to FTS if the endpoint is down.
`)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
