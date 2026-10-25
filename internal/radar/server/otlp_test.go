package server

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// The OTLP intake over real loopback http: Claude Code's export lands as rows under source
// otel, the /api/otel read answers them with their totals, and the transcripts' own store
// never sees one of them. The fixtures are ingest's recorded export, JSON and protobuf.

func otlpFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "ingest", "testdata", "otlp", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func readOtel(t *testing.T, port int) OtelAnswer {
	t.Helper()
	reply := call(t, port, "GET", OtelPath, "", nil)
	if reply.status != 200 {
		t.Fatalf("GET %s: %d %s", OtelPath, reply.status, reply.body)
	}
	var answer OtelAnswer
	if err := json.Unmarshal([]byte(reply.body), &answer); err != nil {
		t.Fatalf("decode %s: %v", reply.body, err)
	}
	return answer
}

func modelTotals(t *testing.T, answer OtelAnswer, model string) store.OtelModelTotals {
	t.Helper()
	for _, totals := range answer.ByModel {
		if totals.Model == model {
			return totals
		}
	}
	t.Fatalf("no totals for %s in %+v", model, answer.ByModel)
	return store.OtelModelTotals{}
}

func TestOTLPIntake(t *testing.T) {
	st := store.CreateStore(func() time.Time { return time.UnixMilli(1792922600000) })
	_, port := serve(t, HttpOptions{Store: st, Version: "0.0.1", StartedAt: 1, Otel: store.NewOtelRows()})

	reply := call(t, port, "POST", OTLPMetricsPath, otlpFixture(t, "claude-code-metrics.json"),
		map[string]string{"Content-Type": "application/json"})
	if reply.status != 200 || reply.body != "{}" || !strings.HasPrefix(reply.contentType, "application/json") {
		t.Fatalf("JSON export: %d %q (%s)", reply.status, reply.body, reply.contentType)
	}
	answer := readOtel(t, port)
	if answer.Source != shared.SourceOtel || answer.Points != 6 || len(answer.Rows) != 6 {
		t.Fatalf("after the JSON export: %+v", answer)
	}
	for _, row := range answer.Rows {
		if row.Source != shared.SourceOtel {
			t.Fatalf("a row outside source otel: %+v", row)
		}
	}
	sonnet := modelTotals(t, answer, "claude-sonnet-4-5")
	if sonnet.Input != 1200 || sonnet.Output != 340 || sonnet.CacheRead != 56000 ||
		sonnet.CacheCreation != 2048 || sonnet.CostUsd != 0.0315 || sonnet.Sessions != 1 {
		t.Fatalf("sonnet totals: %+v", sonnet)
	}

	// the same batch again, protobuf this time: the delta points add, the cumulative series
	// keeps its newest point as its total
	reply = call(t, port, "POST", OTLPMetricsPath, otlpFixture(t, "claude-code-metrics.pb"),
		map[string]string{"Content-Type": "application/x-protobuf"})
	if reply.status != 200 || reply.body != "" || reply.contentType != "application/x-protobuf" {
		t.Fatalf("protobuf export: %d %q (%s)", reply.status, reply.body, reply.contentType)
	}
	answer = readOtel(t, port)
	if answer.Points != 12 {
		t.Fatalf("points after both exports: %d", answer.Points)
	}
	if sonnet := modelTotals(t, answer, "claude-sonnet-4-5"); sonnet.Input != 2400 || sonnet.CostUsd != 0.063 {
		t.Fatalf("sonnet after both: %+v", sonnet)
	}
	if haiku := modelTotals(t, answer, "claude-haiku-4-5"); haiku.CostUsd != 0.0042 || haiku.Sessions != 1 {
		t.Fatalf("haiku after both: %+v", haiku)
	}
	if answer.ByModel[0].Model != "claude-sonnet-4-5" {
		t.Fatalf("costliest model first: %+v", answer.ByModel)
	}

	// a gzipped body, the compression OTLP/HTTP exporters may use
	var zipped bytes.Buffer
	writer := gzip.NewWriter(&zipped)
	_, _ = writer.Write([]byte(otlpFixture(t, "claude-code-metrics.json")))
	_ = writer.Close()
	reply = call(t, port, "POST", OTLPMetricsPath, zipped.String(),
		map[string]string{"Content-Type": "application/json", "Content-Encoding": "gzip"})
	if reply.status != 200 {
		t.Fatalf("gzipped export: %d %s", reply.status, reply.body)
	}
	if answer = readOtel(t, port); answer.Points != 18 {
		t.Fatalf("points after the gzipped export: %d", answer.Points)
	}

	// never summed into the transcripts: the store radar's views read holds no request
	if requests := call(t, port, "GET", "/api/requests", "", nil); requests.body != "{\"requests\":[]}\n" {
		t.Fatalf("the transcripts' requests: %s", requests.body)
	}
}

func TestOTLPIntakeRefusals(t *testing.T) {
	st := store.CreateStore(time.Now)
	_, port := serve(t, HttpOptions{Store: st, Version: "0.0.1", StartedAt: 1, Otel: store.NewOtelRows()})
	body := otlpFixture(t, "claude-code-metrics.json")
	cases := []struct {
		name    string
		method  string
		body    string
		headers map[string]string
		status  int
	}{
		{"a read of the write path", "GET", "", nil, 405},
		{"a form post", "POST", body, map[string]string{"Content-Type": "text/plain"}, 415},
		{"a foreign origin", "POST", body,
			map[string]string{"Content-Type": "application/json", "Origin": "http://evil.test"}, 403},
		{"a body that does not parse", "POST", "{nope", map[string]string{"Content-Type": "application/json"}, 400},
		{"a body past the limit", "POST", strings.Repeat(" ", maxOTLPBody+1),
			map[string]string{"Content-Type": "application/json"}, 413},
	}
	for _, c := range cases {
		if reply := call(t, port, c.method, OTLPMetricsPath, c.body, c.headers); reply.status != c.status {
			t.Errorf("%s: %d %s, want %d", c.name, reply.status, reply.body, c.status)
		}
	}
	if answer := readOtel(t, port); answer.Points != 0 {
		t.Fatalf("a refused export landed: %+v", answer)
	}

	// a server with no otel rows takes no export and answers no read
	_, bare := serve(t, HttpOptions{Store: st, Version: "0.0.1", StartedAt: 1})
	if reply := call(t, bare, "POST", OTLPMetricsPath, body, map[string]string{"Content-Type": "application/json"}); reply.status != 503 {
		t.Fatalf("export without rows: %d", reply.status)
	}
	if reply := call(t, bare, "GET", OtelPath, "", nil); reply.status != 503 {
		t.Fatalf("read without rows: %d", reply.status)
	}
}
