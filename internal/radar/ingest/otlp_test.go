package ingest_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/radar/ingest"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// Claude Code's OpenTelemetry export: the fixture is one ExportMetricsServiceRequest written
// from the OTLP/HTTP spec (opentelemetry-proto, metrics/v1 and its JSON mapping: lowerCamelCase
// keys, 64-bit integers as strings, temporality as its enum number) with the metric names,
// units and attributes Claude Code's monitoring docs list. The .pb file is the same request in
// the protobuf encoding. Both read as the same rows: the token and cost points, never the
// session counter, each tagged source otel.

// otlpSession and otlpCumulativeSession are the two sessions the fixture names.
const (
	otlpSession           = "5b0c7f1e-2d4a-4c11-9e3b-7a6f00c0ffee"
	otlpCumulativeSession = "9e1d2c3b-0000-4a5b-8c7d-112233445566"
)

// otlpWant is every row the fixture holds, in the order the request carries them.
func otlpWant() []shared.OtelRow {
	token := func(kind string, value float64) shared.OtelRow {
		return shared.OtelRow{
			Source: shared.SourceOtel, Metric: shared.OtelTokenUsage, SessionID: otlpSession,
			Model: "claude-sonnet-4-5", Type: kind, Value: value,
			StartTs: 1792922400000, Ts: 1792922460000,
		}
	}
	return []shared.OtelRow{
		token("input", 1200),
		token("output", 340),
		token("cacheRead", 56000),
		token("cacheCreation", 2048),
		{
			Source: shared.SourceOtel, Metric: shared.OtelCostUsage, SessionID: otlpSession,
			Model: "claude-sonnet-4-5", Value: 0.0315, StartTs: 1792922400000, Ts: 1792922460000,
		},
		{
			Source: shared.SourceOtel, Metric: shared.OtelCostUsage, SessionID: otlpCumulativeSession,
			Model: "claude-haiku-4-5", Value: 0.0042, StartTs: 1792922000000, Ts: 1792922520000,
			Cumulative: true,
		},
	}
}

func readOTLPFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "otlp", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestOTLPDecodeJSON(t *testing.T) {
	rows, err := ingest.DecodeOTLPMetrics(readOTLPFixture(t, "claude-code-metrics.json"), "application/json")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if want := otlpWant(); !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows:\n got %+v\nwant %+v", rows, want)
	}
}

func TestOTLPDecodeProtobuf(t *testing.T) {
	rows, err := ingest.DecodeOTLPMetrics(readOTLPFixture(t, "claude-code-metrics.pb"), "application/x-protobuf")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if want := otlpWant(); !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows:\n got %+v\nwant %+v", rows, want)
	}
}

// A content type with parameters still names its encoding; one OTLP/HTTP does not define is
// refused as such, and a body that does not parse is an error, never an empty batch.
func TestOTLPDecodeRefusals(t *testing.T) {
	rows, err := ingest.DecodeOTLPMetrics(readOTLPFixture(t, "claude-code-metrics.json"), "application/json; charset=utf-8")
	if err != nil || len(rows) != len(otlpWant()) {
		t.Fatalf("json with a charset: %d rows, %v", len(rows), err)
	}
	var unsupported *ingest.UnsupportedOTLPType
	if _, err := ingest.DecodeOTLPMetrics([]byte("{}"), "text/plain"); !errors.As(err, &unsupported) {
		t.Fatalf("text/plain: %v, want UnsupportedOTLPType", err)
	}
	if _, err := ingest.DecodeOTLPMetrics([]byte("{not json"), "application/json"); err == nil {
		t.Fatal("broken JSON decoded")
	}
	if _, err := ingest.DecodeOTLPMetrics([]byte{0xff, 0xff, 0xff}, "application/x-protobuf"); err == nil {
		t.Fatal("broken protobuf decoded")
	}
}
