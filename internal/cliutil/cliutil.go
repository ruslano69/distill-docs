// Package cliutil holds small, generic CLI-input-parsing and display helpers
// shared by cmd/distill and cmd/distill-server. Before this package existed,
// each binary carried its own near-identical copy of these — a discrepancy
// (distill-server's graph output never truncated a doc title, distill's did)
// was found via a duplication audit; the fix is to have exactly one copy.
package cliutil

import (
	"fmt"
	"strconv"
	"strings"
)

// SplitPositionalFlag pulls the first bare (non-flag) token out of args and
// returns it plus the remaining args, meant for a subsequent fs.Parse. Go's
// flag package stops parsing at the first non-flag token, so a command like
// `graph SPEC-42 --json` would otherwise silently drop --json; calling this
// before fs.Parse lets a CLI accept a value as a bare positional in any
// position while flags after it still parse normally.
func SplitPositionalFlag(args []string) (positional string, rest []string) {
	rest = make([]string, 0, len(args))
	for _, a := range args {
		if positional == "" && a != "" && a[0] != '-' {
			positional = a
			continue
		}
		rest = append(rest, a)
	}
	return positional, rest
}

// ParseEmbedding parses a comma-separated float32 vector, optionally wrapped
// in "[...]" (e.g. "0.1,0.2,0.3" or "[0.1,0.2,0.3]"). An empty input returns
// (nil, nil) — BYO embeddings are optional. Returns an error rather than
// exiting, so the caller (which knows whether it's mid-JSON-output or not)
// controls how a bad value gets reported.
func ParseEmbedding(raw string) ([]float32, error) {
	raw = strings.Trim(strings.TrimSpace(raw), "[]")
	if raw == "" {
		return nil, nil
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
			return nil, fmt.Errorf("invalid embedding value %q: %w", p, err)
		}
		out = append(out, float32(f))
	}
	return out, nil
}

// Truncate shortens s to at most n bytes, appending "..." when cut. For
// single-line CLI display (e.g. a doc title next to a graph relation) where a
// long value would break the line-oriented output format. Byte-based, not
// rune-safe — callers use it only for display of already-mostly-ASCII titles,
// not for content that must survive re-parsing.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
