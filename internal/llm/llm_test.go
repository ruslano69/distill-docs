package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeGenServer serves the Ollama /api/generate wire format, echoing a fixed
// JSON payload so tests need no real model. It also records the last request so
// assertions can check what the client actually sent.
func fakeGenServer(t *testing.T, response string, seen *generateRequest) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			json.NewDecoder(r.Body).Decode(seen)
		}
		json.NewEncoder(w).Encode(map[string]any{"response": response})
	}))
}

func TestGenerateJSON_ReturnsResponse(t *testing.T) {
	var seen generateRequest
	ts := fakeGenServer(t, `{"kind":"supersedes","confidence":0.9}`, &seen)
	defer ts.Close()

	c := New(ts.URL, "test-model")
	out, err := c.GenerateJSON(context.Background(), "you classify", "A vs B", nil)
	if err != nil {
		t.Fatalf("GenerateJSON: %v", err)
	}
	if out != `{"kind":"supersedes","confidence":0.9}` {
		t.Errorf("response = %q", out)
	}
	// A nil schema falls back to the bare "json" mode (merely-valid-JSON).
	if string(seen.Format) != `"json"` {
		t.Errorf("Format = %s, want \"json\"", seen.Format)
	}
	if seen.Stream {
		t.Errorf("Stream should be false")
	}
	if seen.Options["temperature"] != float64(0) {
		t.Errorf("temperature = %v, want 0", seen.Options["temperature"])
	}
	if seen.System != "you classify" || seen.Prompt != "A vs B" {
		t.Errorf("system/prompt not forwarded: %+v", seen)
	}
}

func TestGenerateJSON_WithSchema_SendsSchemaAsFormat(t *testing.T) {
	var seen generateRequest
	schema := json.RawMessage(`{"type":"object","required":["kind","confidence"]}`)
	ts := fakeGenServer(t, `{}`, &seen)
	defer ts.Close()

	c := New(ts.URL, "test-model")
	if _, err := c.GenerateJSON(context.Background(), "sys", "p", schema); err != nil {
		t.Fatalf("GenerateJSON: %v", err)
	}
	if string(seen.Format) != string(schema) {
		t.Errorf("Format = %s, want the schema forwarded verbatim: %s", seen.Format, schema)
	}
}

func TestGenerateJSON_DisabledClient(t *testing.T) {
	if _, err := New("", "").GenerateJSON(context.Background(), "", "x", nil); err == nil {
		t.Error("disabled client should error")
	}
}

func TestGenerateJSON_EndpointError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"error": "model not found"})
	}))
	defer ts.Close()
	if _, err := New(ts.URL, "m").GenerateJSON(context.Background(), "", "x", nil); err == nil {
		t.Error("endpoint error field should surface as an error")
	}
}

func TestEnabled(t *testing.T) {
	if New("", "").Enabled() {
		t.Error("empty model → disabled")
	}
	if !New("", "m").Enabled() {
		t.Error("model set → enabled")
	}
	var nilC *Client
	if nilC.Enabled() {
		t.Error("nil client → disabled")
	}
}
