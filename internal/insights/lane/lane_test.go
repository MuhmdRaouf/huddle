package lane

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAskSendsOneMessagesRequestWithoutCredentials(t *testing.T) {
	var path, method string
	var header http.Header
	var body struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		System    string `json:"system"`
		Stream    bool   `json:"stream"`
		Messages  []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, method, header = r.URL.Path, r.Method, r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("the request body is not JSON: %v", err)
		}
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"one "},{"type":"text","text":"two"}],"usage":{"input_tokens":120,"output_tokens":7}}`)
	}))
	t.Cleanup(server.Close)

	reply, err := New(server.URL).Ask(context.Background(), "glm-5.3", "be brief", `{"totals":1}`)
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if method != http.MethodPost || path != "/v1/messages" {
		t.Errorf("%s %s, want POST /v1/messages", method, path)
	}
	if body.Model != "glm-5.3" || body.System != "be brief" || body.Stream || body.MaxTokens != MaxOutputTokens ||
		len(body.Messages) != 1 || body.Messages[0].Role != "user" || body.Messages[0].Content != `{"totals":1}` {
		t.Errorf("request body = %+v, want the model, the system text and one user message", body)
	}
	for _, name := range []string{"Authorization", "X-Api-Key"} {
		if header.Get(name) != "" {
			t.Errorf("the request carries %s; the router adds the provider's key itself", name)
		}
	}
	if reply.Text != "one two" || reply.InputTokens != 120 || reply.OutputTokens != 7 {
		t.Errorf("reply = %+v, want the joined text and the usage", reply)
	}
}

func TestAskReportsAnErrorStatusWithoutTheBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":{"message":"secret-ish upstream text"}}`)
	}))
	t.Cleanup(server.Close)

	_, err := New(server.URL).Ask(context.Background(), "glm-5.3", "s", "u")
	if err == nil || !strings.Contains(err.Error(), "502") || strings.Contains(err.Error(), "secret-ish") {
		t.Fatalf("Ask = %v, want an error naming status 502 and none of the body", err)
	}
}

func TestAskNamesAnUnreachableRouter(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()

	_, err := New(url).Ask(context.Background(), "glm-5.3", "s", "u")
	if err == nil || !strings.Contains(err.Error(), "huddle-router") {
		t.Fatalf("Ask = %v, want an error that names huddle-router", err)
	}
}
