package plugins

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// The usage counters every fake answer carries, as the Anthropic messages stream reports them:
// message_start holds the input and cache counters, message_delta the final output count.
const (
	fakeInputTokens     = 1200
	fakeCacheReadTokens = 300
	fakeOutputTokens    = 42
)

// fakeProvider is one provider's upstream as its catalog entry describes it: the messages path
// under the base URL's own path, the key in the header the catalog's auth style names, and a
// streamed answer with the usage fields the router spools. Any other path, or a request without
// the key, gets the status the real provider gives.
type fakeProvider struct {
	server *httptest.Server
	// url is the provider's base URL on the fake: the server's address plus the catalog's path.
	url string
}

// newFakeProvider starts the fake for the provider, accepting only key.
func newFakeProvider(t *testing.T, provider domain.Provider, key string) *fakeProvider {
	t.Helper()
	if provider.Auth != domain.AuthBearer {
		t.Fatalf("%s: the fake knows the bearer auth style only, the catalog names %q", provider.Name, provider.Auth)
	}
	base, err := url.Parse(provider.BaseURL)
	if err != nil {
		t.Fatalf("%s: the catalog's base URL %q: %v", provider.Name, provider.BaseURL, err)
	}
	messages := base.Path + "/v1/messages"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != messages {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+key {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid api key"}}`)
			return
		}
		var request struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "the body is not JSON", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		writeFakeStream(w, request.Model)
	}))
	t.Cleanup(server.Close)
	return &fakeProvider{server: server, url: server.URL + base.Path}
}

// writeFakeStream writes one short Anthropic messages stream for the model.
func writeFakeStream(w io.Writer, model string) {
	events := []struct {
		name string
		data any
	}{
		{"message_start", map[string]any{"type": "message_start", "message": map[string]any{
			"id": "msg_fake", "type": "message", "role": "assistant", "model": model, "content": []any{},
			"usage": map[string]any{
				"input_tokens":            fakeInputTokens,
				"cache_read_input_tokens": fakeCacheReadTokens,
				"output_tokens":           1,
			},
		}}},
		{"content_block_start", map[string]any{"type": "content_block_start", "index": 0,
			"content_block": map[string]any{"type": "text", "text": ""}}},
		{"content_block_delta", map[string]any{"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": "ok"}}},
		{"content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}},
		{"message_delta", map[string]any{"type": "message_delta",
			"delta": map[string]any{"stop_reason": "end_turn"},
			"usage": map[string]any{"output_tokens": fakeOutputTokens}}},
		{"message_stop", map[string]any{"type": "message_stop"}},
	}
	for _, event := range events {
		data, _ := json.Marshal(event.data)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.name, data)
	}
}
