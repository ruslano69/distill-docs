package digest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ruslano69/distill-docs/internal/llm"
)

// fakeLLM stands up an Ollama-/api/generate-shaped server that always replies
// with the given raw JSON in the "response" field, and returns a client wired
// to it. Closed automatically at test end.
func fakeLLM(t *testing.T, response string) *llm.Client {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"response": response})
	}))
	t.Cleanup(ts.Close)
	return llm.New(ts.URL, "test-model")
}
