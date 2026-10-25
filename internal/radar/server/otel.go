// Claude Code's OpenTelemetry export, taken in and read back. OTLPMetricsPath is the OTLP/HTTP
// metrics path an exporter posts to (huddle serves it at /api/radar/otlp/v1/metrics, behind its
// credential); OtelPath answers the rows with their per-model totals. The rows are a source of
// their own: they never reach the store the transcripts feed.
package server

import (
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/radar/ingest"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// The intake's write path and the rows' read path.
const (
	OTLPMetricsPath = "/otlp/v1/metrics"
	OtelPath        = "/api/otel"
)

// maxOTLPBody bounds one export, after any gzip: Claude Code's batches are a few KiB.
const maxOTLPBody = 4 << 20

// otelRowsShown is how many of the newest rows the read carries.
const otelRowsShown = 200

// OtelAnswer is the read: the source, how many points are held, the totals per model and the
// newest rows, newest first.
type OtelAnswer struct {
	Source  string                  `json:"source"`
	Points  int                     `json:"points"`
	ByModel []store.OtelModelTotals `json:"byModel"`
	Rows    []shared.OtelRow        `json:"rows"`
}

// otelRead answers the rows held.
func (h *HttpServer) otelRead(w http.ResponseWriter) {
	if h.options.Otel == nil {
		sendJSON(w, 503, errorBody{Error: "the OpenTelemetry intake is off"})
		return
	}
	rows := h.options.Otel.Rows()
	shown := slices.Clone(rows[max(0, len(rows)-otelRowsShown):])
	slices.Reverse(shown)
	sendJSON(w, 200, OtelAnswer{
		Source:  shared.SourceOtel,
		Points:  len(rows),
		ByModel: store.OtelTotals(rows),
		Rows:    shown,
	})
}

// otlpRoute takes one export. Its client is an OTLP exporter, not the dashboard: the body is
// protobuf or JSON — both need a preflight from another site, which nothing here answers — and
// a browser that names an origin must name ours. The answer is the empty
// ExportMetricsServiceResponse in the request's own encoding.
func (h *HttpServer) otlpRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendJSON(w, 405, errorBody{Error: "method not allowed"})
		return
	}
	if h.options.Otel == nil {
		sendJSON(w, 503, errorBody{Error: "the OpenTelemetry intake is off"})
		return
	}
	media, err := ingest.OTLPMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		sendJSON(w, 415, errorBody{Error: err.Error()})
		return
	}
	if len(r.Header.Values("Origin")) > 0 && !h.originAllowed(joinedHeader(r.Header, "Origin")) {
		sendJSON(w, 403, errorBody{Error: "exports come from this machine only"})
		return
	}
	body, err := readOTLPBody(r)
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		sendJSON(w, 413, errorBody{Error: "body too large"})
		return
	case err != nil:
		sendJSON(w, 400, errorBody{Error: "could not read the body: " + err.Error()})
		return
	}
	rows, err := ingest.DecodeOTLPMetrics(body, media)
	if err != nil {
		sendJSON(w, 400, errorBody{Error: "not an OTLP metrics export: " + err.Error()})
		return
	}
	h.options.Otel.Add(rows)
	if media == ingest.OTLPJSON {
		send(w, 200, "{}", map[string]string{"Content-Type": ingest.OTLPJSON})
		return
	}
	send(w, 200, "", map[string]string{"Content-Type": ingest.OTLPProtobuf})
}

// readOTLPBody reads one export whole, gunzipped when the exporter compressed it, never past
// the bound.
func readOTLPBody(r *http.Request) ([]byte, error) {
	defer func() { _ = r.Body.Close() }()
	var reader io.Reader = r.Body
	switch encoding := strings.ToLower(r.Header.Get("Content-Encoding")); encoding {
	case "", "identity":
	case "gzip":
		unzipped, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil, err
		}
		defer func() { _ = unzipped.Close() }()
		reader = unzipped
	default:
		return nil, errors.New("unsupported content encoding " + encoding)
	}
	body, err := io.ReadAll(io.LimitReader(reader, maxOTLPBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxOTLPBody {
		return nil, &http.MaxBytesError{Limit: maxOTLPBody}
	}
	return body, nil
}
