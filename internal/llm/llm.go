// Package llm is an optional, provider-agnostic text-generation client for the
// distill digester (Stage 2). Where internal/embed turns text into vectors,
// this turns a prompt into a completion: the L2 knowledge-graph digester uses
// it to classify the relationship between two documents (supersedes,
// contradicts, elaborates, ...).
//
// It speaks the Ollama /api/generate wire format
// ({"model":..,"prompt":..,"format":"json","stream":false} -> {"response":..}),
// which local models like gemma4:12b serve. A zero/empty Client (no model
// configured) is disabled: the digester then does nothing, exactly like a
// missing embedding model degrades search to FTS.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// DefaultURL is the standard local Ollama generate endpoint.
const DefaultURL = "http://localhost:11434/api/generate"

// Client calls an Ollama-compatible /api/generate endpoint.
type Client struct {
	URL   string
	Model string
	HTTP  *http.Client
}

// New returns a client for the given model at url (DefaultURL if url is empty).
// An empty model yields a disabled client (Enabled reports false). The timeout
// is generous: a 12B model classifying a pair can take tens of seconds on CPU.
func New(url, model string) *Client {
	if url == "" {
		url = DefaultURL
	}
	return &Client{URL: url, Model: model, HTTP: &http.Client{Timeout: 120 * time.Second}}
}

// Enabled reports whether generation is configured. When false, the digester
// skips all LLM work (no typed edges are proposed).
func (c *Client) Enabled() bool { return c != nil && c.Model != "" }

type generateRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	System string `json:"system,omitempty"`
	Stream bool   `json:"stream"`
	// Format is Ollama's structured-output control: the bare string "json"
	// only constrains output to be syntactically valid JSON (a model can still
	// omit fields it judges "answered enough" — the missing-fields fix that
	// motivated Schema below). A full JSON Schema object instead grammar-
	// constrains generation so every field in the schema's "required" list is
	// forced into the output, regardless of the model's own judgment.
	Format  json.RawMessage `json:"format,omitempty"`
	Options map[string]any  `json:"options,omitempty"`
}

type generateResponse struct {
	Response string `json:"response"`
	Error    string `json:"error,omitempty"`
}

var jsonFormat = json.RawMessage(`"json"`)

// GenerateJSON runs the prompt (with an optional system preamble) at
// temperature 0, returning the raw response string for the caller to
// unmarshal. Deterministic (temp 0) so a re-digest of unchanged content is
// reproducible. schema is optional: nil constrains output to merely-valid JSON
// (the caller must tolerate missing fields); a non-nil JSON Schema object (with
// a "required" list) grammar-constrains the endpoint to include every required
// field, which loose "json" mode does not guarantee — some models end the
// object early once they consider the answer "complete enough".
func (c *Client) GenerateJSON(ctx context.Context, system, prompt string, schema json.RawMessage) (string, error) {
	if !c.Enabled() {
		return "", fmt.Errorf("llm disabled (no model configured)")
	}
	format := jsonFormat
	if len(schema) > 0 {
		format = schema
	}
	body, err := json.Marshal(generateRequest{
		Model:   c.Model,
		Prompt:  prompt,
		System:  system,
		Stream:  false,
		Format:  format,
		Options: map[string]any{"temperature": 0},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("generate request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("generate endpoint returned %s", resp.Status)
	}
	var gr generateResponse
	if err := json.NewDecoder(resp.Body).Decode(&gr); err != nil {
		return "", fmt.Errorf("decode generate response: %w", err)
	}
	if gr.Error != "" {
		return "", fmt.Errorf("generate endpoint error: %s", gr.Error)
	}
	return gr.Response, nil
}
