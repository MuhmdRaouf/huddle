// Claude Code's OpenTelemetry export, read as rows. Claude Code posts its metrics as an OTLP/HTTP
// ExportMetricsServiceRequest, protobuf (application/x-protobuf) or its JSON mapping
// (application/json). The request's one field, resource_metrics = 1, is MetricsData's one field
// too, so the body decodes as MetricsData: the same wire bytes and JSON keys, without the
// collector package's gRPC gateway. Radar keeps claude_code.token.usage and
// claude_code.cost.usage; every other metric of the batch is skipped.
package ingest

import (
	"mime"

	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// The two encodings OTLP/HTTP defines.
const (
	OTLPProtobuf = "application/x-protobuf"
	OTLPJSON     = "application/json"
)

// UnsupportedOTLPType is a body whose content type names neither OTLP/HTTP encoding.
type UnsupportedOTLPType struct{ ContentType string }

func (e *UnsupportedOTLPType) Error() string {
	return "OTLP takes " + OTLPProtobuf + " or " + OTLPJSON + ", not " + e.ContentType
}

// OTLPMediaType answers the encoding one Content-Type header names, its parameters dropped.
func OTLPMediaType(contentType string) (string, error) {
	media, _, err := mime.ParseMediaType(contentType)
	if err != nil || (media != OTLPProtobuf && media != OTLPJSON) {
		return "", &UnsupportedOTLPType{ContentType: contentType}
	}
	return media, nil
}

// DecodeOTLPMetrics reads one export body into rows, in the order the batch carries its points.
func DecodeOTLPMetrics(body []byte, contentType string) ([]shared.OtelRow, error) {
	media, err := OTLPMediaType(contentType)
	if err != nil {
		return nil, err
	}
	var data metricsv1.MetricsData
	if media == OTLPJSON {
		err = protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(body, &data)
	} else {
		err = proto.Unmarshal(body, &data)
	}
	if err != nil {
		return nil, err
	}
	rows := []shared.OtelRow{}
	for _, resource := range data.GetResourceMetrics() {
		for _, scope := range resource.GetScopeMetrics() {
			for _, metric := range scope.GetMetrics() {
				rows = appendMetric(rows, metric)
			}
		}
	}
	return rows, nil
}

// appendMetric adds one kept metric's points; Claude Code exports both as monotonic sums.
func appendMetric(rows []shared.OtelRow, metric *metricsv1.Metric) []shared.OtelRow {
	name := metric.GetName()
	if name != shared.OtelTokenUsage && name != shared.OtelCostUsage {
		return rows
	}
	sum := metric.GetSum()
	if sum == nil {
		return rows
	}
	cumulative := sum.GetAggregationTemporality() == metricsv1.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE
	for _, point := range sum.GetDataPoints() {
		attributes := point.GetAttributes()
		row := shared.OtelRow{
			Source:     shared.SourceOtel,
			Metric:     name,
			SessionID:  stringAttribute(attributes, "session.id"),
			Model:      stringAttribute(attributes, "model"),
			Value:      pointValue(point),
			StartTs:    int64(point.GetStartTimeUnixNano() / 1e6),
			Ts:         int64(point.GetTimeUnixNano() / 1e6),
			Cumulative: cumulative,
		}
		if name == shared.OtelTokenUsage {
			row.Type = stringAttribute(attributes, "type")
		}
		rows = append(rows, row)
	}
	return rows
}

// pointValue reads a point's number, whichever of the two value fields carries it.
func pointValue(point *metricsv1.NumberDataPoint) float64 {
	if _, isInt := point.GetValue().(*metricsv1.NumberDataPoint_AsInt); isInt {
		return float64(point.GetAsInt())
	}
	return point.GetAsDouble()
}

// stringAttribute answers one attribute's string value, "" when the point does not carry it.
func stringAttribute(attributes []*commonv1.KeyValue, key string) string {
	for _, attribute := range attributes {
		if attribute.GetKey() == key {
			return attribute.GetValue().GetStringValue()
		}
	}
	return ""
}
