// Package docmeta is the single home for a document's provenance + Stage-1
// ranking metadata — the "config" that both binaries write onto every document.
// It models the shape once (Meta), serializes it once (JSON), and binds it from
// each input mechanism once: RegisterRankFlags for CLI flag sets, and Merge for
// the single-file CLI's raw --meta escape hatch. Previously this binding was
// copy-pasted across cmd/distill and cmd/distill-server; centralizing it means a
// new ranking signal is added in exactly one place.
package docmeta

import (
	"encoding/json"
	"flag"
	"fmt"
	"strings"
)

// Meta is the metadata blob attached to a document. Zero-valued fields are
// omitted from the JSON, so absent metadata reads as NULL in the knowledge
// layer's generated columns.
type Meta struct {
	Author        string
	RoleTags      string
	SourceVersion string
	SourceRef     string
	Topic         string
	Priority      float64
	Pinned        bool
	Supersedes    int64
}

// applyTo writes Meta's non-zero fields into obj using the on-disk JSON keys
// (the same keys the knowledge schema's json_extract generated columns read).
func (m Meta) applyTo(obj map[string]any) {
	if m.Author != "" {
		obj["author"] = m.Author
	}
	if m.RoleTags != "" {
		obj["role_tags"] = m.RoleTags
	}
	if m.SourceVersion != "" {
		obj["source_version"] = m.SourceVersion
	}
	if m.SourceRef != "" {
		obj["source_ref"] = m.SourceRef
	}
	if m.Topic != "" {
		obj["topic"] = m.Topic
	}
	if m.Priority != 0 {
		obj["priority"] = m.Priority
	}
	if m.Pinned {
		obj["pinned"] = 1
	}
	if m.Supersedes != 0 {
		obj["supersedes"] = m.Supersedes
	}
}

// JSON marshals Meta to the metadata blob (empty fields omitted).
func (m Meta) JSON() string {
	obj := map[string]any{}
	m.applyTo(obj)
	b, _ := json.Marshal(obj)
	return string(b)
}

// Merge overlays Meta's non-zero fields onto a raw base JSON object (the
// single-file CLI's --meta escape hatch), so `--meta '{"custom":"x"}'` and
// structured flags like --topic compose. A malformed base is a hard error.
func Merge(base string, m Meta) (string, error) {
	obj := map[string]any{}
	base = strings.TrimSpace(base)
	if base != "" && base != "{}" {
		if err := json.Unmarshal([]byte(base), &obj); err != nil {
			return "", fmt.Errorf("parse --meta JSON: %w", err)
		}
	}
	m.applyTo(obj)
	b, _ := json.Marshal(obj)
	return string(b), nil
}

// RankFlags binds the Stage-1 ranking/curation flags shared by every write path.
// Register them once instead of re-declaring the same four flags per command, so
// a new ranking signal is added in one place (this is how priority/pinned/
// supersedes first drifted between commands). Command-specific provenance
// (author, role_tags, source_ref, type) stays on the command, since its help
// text and semantics differ.
type RankFlags struct {
	topic      *string
	priority   *float64
	pinned     *bool
	supersedes *int64
}

// RegisterRankFlags binds the ranking flags on fs. Call Apply after fs.Parse.
func RegisterRankFlags(fs *flag.FlagSet) *RankFlags {
	return &RankFlags{
		topic:      fs.String("topic", "", "topic facet (Stage-1 ranking)"),
		priority:   fs.Float64("priority", 0, "numeric priority (Stage-1 ranking)"),
		pinned:     fs.Bool("pinned", false, "mark as authoritative/pinned (ranking boost)"),
		supersedes: fs.Int64("supersedes", 0, "id of a doc this one supersedes (drops the old one from results)"),
	}
}

// Apply folds the parsed ranking flags onto m.
func (r *RankFlags) Apply(m *Meta) {
	m.Topic = *r.topic
	m.Priority = *r.priority
	m.Pinned = *r.pinned
	m.Supersedes = *r.supersedes
}
