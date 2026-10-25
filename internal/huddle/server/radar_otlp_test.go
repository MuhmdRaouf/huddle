package server

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Claude Code's OpenTelemetry export reaches the Radar tab on huddle's own port, behind huddle's
// credential: an OTLP exporter posts protobuf or JSON and names no action, so the guard takes
// those two encodings on the intake path alone; everything else about a write still holds.

func TestRadarOTLPIntake(t *testing.T) {
	srv := startServer(t, nil)
	fixture := func(name string) string {
		raw, err := os.ReadFile(filepath.Join("..", "..", "radar", "ingest", "testdata", "otlp", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	// the export is built here, not by srv.request, which names an action on every write: an
	// exporter names none
	post := func(body, contentType string, headers map[string]string) (int, string) {
		t.Helper()
		request, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
			srv.base+"/api/radar/otlp/v1/metrics", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", contentType)
		for key, value := range headers {
			request.Header.Set(key, value)
		}
		client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 30 * time.Second}
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("the export: %v", err)
		}
		defer func() { _ = response.Body.Close() }()
		answer, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(answer)
	}
	credential := map[string]string{"x-huddle-token": srv.token}

	if code, body := post(fixture("claude-code-metrics.pb"), "application/x-protobuf", nil); code != 401 {
		t.Fatalf("an export with no credential = %d %s, want 401", code, body)
	}
	if code, body := post(fixture("claude-code-metrics.pb"), "application/x-protobuf", credential); code != 200 {
		t.Fatalf("the protobuf export = %d %s, want 200", code, body)
	}
	if code, body := post(fixture("claude-code-metrics.json"), "application/json", credential); code != 200 || body != "{}" {
		t.Fatalf("the JSON export = %d %q, want 200 {}", code, body)
	}
	// the exemption is the encoding OTLP defines, nothing wider
	if code, _ := post(fixture("claude-code-metrics.json"), "text/plain", credential); code != 403 {
		t.Fatalf("a plain-text export = %d, want the guard's 403", code)
	}
	if code, _ := post(fixture("claude-code-metrics.json"), "application/json",
		map[string]string{"x-huddle-token": srv.token, "Origin": "https://evil.example"}); code != 403 {
		t.Fatalf("an export from a foreign origin = %d, want 403", code)
	}

	code, _, body, err := srv.request(context.Background(), http.MethodGet, "/api/radar/api/otel", "", credential)
	if err != nil || code != 200 || !strings.Contains(body, `"source":"otel"`) || !strings.Contains(body, `"points":12`) {
		t.Fatalf("the otel rows = %d %s (%v)", code, body, err)
	}
}
