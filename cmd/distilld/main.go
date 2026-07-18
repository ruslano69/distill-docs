// Command distilld is the background L2 knowledge-graph digester daemon. It is a
// thin loop around internal/digest.Run: on an interval it re-digests the
// knowledge base, classifying kNN-neighbor pairs into typed relations
// (supersedes/contradicts/...) with a local LLM. Because a pass is incremental
// and resumable (unchanged pairs are skipped via digest_state), a steady-state
// tick is nearly free — real work happens only after docs are added or edited.
//
// It shares its engine with `distill digest`; use the daemon when you want the
// graph kept continuously warm, the one-shot CLI for an on-demand build.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ruslano69/distill-docs/internal/digest"
	"github.com/ruslano69/distill-docs/internal/knowledge"
	"github.com/ruslano69/distill-docs/internal/llm"
	"github.com/ruslano69/distill-docs/internal/version"
)

func main() {
	for _, a := range os.Args[1:] {
		if a == "--version" || a == "-version" {
			version.PrintVersion("distilld")
			return
		}
	}

	fs := flag.NewFlagSet("distilld", flag.ExitOnError)
	dbPath := fs.String("db", ".knowledge/docs.sqlite", "path to SQLite knowledge base")
	model := fs.String("model", "", "Ollama generate model for classification (e.g. gemma4:12b) — required")
	llmURL := fs.String("llm-url", llm.DefaultURL, "generate endpoint")
	k := fs.Int("k", 5, "neighbors per doc considered as relation candidates")
	minConf := fs.Float64("min-confidence", 0.5, "drop proposed edges below this confidence")
	interval := fs.Duration("interval", 5*time.Minute, "pause between passes (0 = run one pass and exit)")
	rebuildKNN := fs.Bool("rebuild-knn", true, "rebuild the kNN geometry before each pass")
	fs.Parse(os.Args[1:])

	if *model == "" {
		fmt.Fprintln(os.Stderr, "error: --model required (e.g. --model gemma4:12b)")
		os.Exit(1)
	}

	db, err := knowledge.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: open db: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	client := digest.LLMClassifier{Client: llm.New(*llmURL, *model)}
	opts := digest.Options{K: *k, MinConfidence: *minConf, EnsureKNN: *rebuildKNN}

	// Graceful stop: Ctrl-C / SIGTERM cancels the in-flight pass and exits.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Fprintf(os.Stderr, "distilld: db=%s model=%s interval=%s\n", *dbPath, *model, *interval)
	for {
		start := time.Now()
		rep, err := digest.Run(ctx, db, client, opts)
		if err != nil {
			// A cancelled context surfaces here mid-pass; treat it as a clean stop.
			if ctx.Err() != nil {
				fmt.Fprintln(os.Stderr, "distilld: stopped")
				return
			}
			fmt.Fprintf(os.Stderr, "distilld: pass error: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "distilld: %d candidates, %d skipped, %d classified, %d edges (%s)\n",
				rep.Candidates, rep.Skipped, rep.Classified, rep.EdgesWritten, time.Since(start).Round(time.Millisecond))
		}

		if *interval <= 0 {
			return // one-shot mode
		}
		select {
		case <-ctx.Done():
			fmt.Fprintln(os.Stderr, "distilld: stopped")
			return
		case <-time.After(*interval):
		}
	}
}
