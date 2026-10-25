// The long-range usage read: every request of one window joined with the node it ran on,
// shaped for the usage aggregator.
package history

import (
	"context"
	"fmt"

	"github.com/MuhmdRaouf/huddle/internal/radar/usage"
)

// UsageRows answers every request whose ts sits inside [fromMs, toMs], each joined with
// its node's project, kind and label, in time order. One query; the session a request
// names wins over its node's.
func (h *History) UsageRows(ctx context.Context, fromMs, toMs int64) ([]usage.Row, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("read the usage rows: %w", err)
	}
	rows, err := h.db.All(
		"SELECT r.ts, r.model, r.upstream, r.provider, r.session_id AS requestSession, "+
			"n.session_id AS nodeSession, n.project, n.kind, n.label, "+
			"r.input, r.output, r.cache_read AS cacheRead, r.cache_write AS cacheWrite, "+
			"r.cache_write_1h AS cacheWrite1h, r.speed, r.geo, r.service_tier, "+
			"r.harness, r.cost_usd AS costUsd "+
			"FROM requests r JOIN nodes n ON n.id = r.node_id "+
			"WHERE r.ts >= ? AND r.ts <= ? ORDER BY r.ts, r.id", fromMs, toMs)
	if err != nil {
		return nil, fmt.Errorf("read the usage rows: %w", err)
	}
	out := make([]usage.Row, 0, len(rows))
	for _, row := range rows {
		out = append(out, usage.Row{
			TS:           derep(rowInt64(row, "ts"), 0),
			Model:        rowTextString(row, "model"),
			Upstream:     rowTextString(row, "upstream"),
			Provider:     rowTextString(row, "provider"),
			Project:      rowTextString(row, "project"),
			SessionID:    rowTextString(row, "requestSession"),
			Kind:         rowTextString(row, "kind"),
			Label:        rowTextString(row, "label"),
			Input:        derep(rowInt64(row, "input"), 0),
			Output:       derep(rowInt64(row, "output"), 0),
			CacheRead:    derep(rowInt64(row, "cacheRead"), 0),
			CacheWrite:   derep(rowInt64(row, "cacheWrite"), 0),
			CacheWrite1h: derep(rowInt64(row, "cacheWrite1h"), 0),
			Speed:        rowText(row, "speed"),
			Geo:          rowText(row, "geo"),
			ServiceTier:  rowText(row, "service_tier"),
			Harness:      rowTextString(row, "harness"),
			CostUSD:      rowFloat64(row, "costUsd"),
		})
		// a request that never carried a session reads as its node's
		if out[len(out)-1].SessionID == "" {
			out[len(out)-1].SessionID = rowTextString(row, "nodeSession")
		}
	}
	return out, nil
}

// rowTextString answers the text a nullable column holds, empty when it holds none.
func rowTextString(row map[string]SqlValue, name string) string {
	if text := rowText(row, name); text != nil {
		return *text
	}
	return ""
}
