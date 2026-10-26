package hub

// The audit log's reads and the owner alerts (SPEC 7.7). Pairing, sign-in and removal write
// their rows through pair.go's own transactions; Record is the one entry point the later
// decision routes (allow, deny, answer, send, link, rule changes) call, and RaiseAlert is the
// one an owner alert goes through. Nothing in the app updates or deletes an audit row; the
// alerts carry a read state that only moves forward.

import (
	"context"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

// The read's bounds: a page serves at most 500 rows, and a filter that names no limit takes 300.
const (
	auditLimitDefault = 300
	auditLimitMax     = 500
)

// AuditRow is one stored audit entry as the read serves it: the row's own moments as ISO
// timestamps, the detail the compact JSON the action handed over (nil when it carried none).
type AuditRow struct {
	ID     int64        `json:"id"`
	At     string       `json:"at"`
	Actor  string       `json:"actor"`
	Action string       `json:"action"`
	Target string       `json:"target"`
	Detail *jsfmt.Value `json:"detail"`
}

// AuditFilter reads the log: Before pages by row id (the cursor is the last id a page served),
// Limit bounds the page — a value outside 1 to 500 is pulled to the bound, never refused, so
// no caller can over-read the log or empty its own page by mistake.
type AuditFilter struct {
	Before *int64
	Limit  int
}

// Record appends one action to the audit log and answers its row id. atMS is the action's
// moment in whole milliseconds, the shape every audit row stores; actor names who did it (a
// paired device, a member, the owner, a terminal). Detail is stored as the compact JSON of what
// was handed over and must carry no credential, code, challenge or passkey material — the log
// keeps what it is given forever, and the server's callers strip this run's secrets first.
func (d *DB) Record(ctx context.Context, atMS int64, actor, action, target string, detail any) (int64, error) {
	var body any
	if detail != nil {
		raw, err := domain.MarshalCompactJSON(detail)
		if err != nil {
			return 0, err
		}
		body = string(raw)
	}
	row, err := d.st.Get(ctx,
		"INSERT INTO audit (at, actor, action, target, detail) VALUES (?, ?, ?, ?, ?) RETURNING id",
		atMS, actor, action, target, body)
	if err != nil {
		return 0, err
	}
	return intOfRow(row, "id"), nil
}

// Audit answers the log newest first (rows never move, so the newest carries the highest id),
// one page of at most 500 entries. A detail column that is not JSON refuses the read: a broken
// row is named, not served half.
func (d *DB) Audit(ctx context.Context, f AuditFilter) ([]AuditRow, error) {
	limit := boundedLimit(f.Limit)
	query := "SELECT id, at, actor, action, target, detail FROM audit"
	params := []any{}
	if f.Before != nil {
		query += " WHERE id < ?"
		params = append(params, *f.Before)
	}
	rows, err := d.st.All(ctx, query+" ORDER BY id DESC LIMIT ?", append(params, limit)...)
	if err != nil {
		return nil, err
	}
	out := make([]AuditRow, 0, len(rows))
	for _, stored := range rows {
		entry := AuditRow{
			ID:     intOfRow(stored, "id"),
			At:     isoOfRow(stored, "at"),
			Actor:  textOfRow(stored, "actor"),
			Action: textOfRow(stored, "action"),
			Target: textOfRow(stored, "target"),
		}
		if entry.Detail, err = jsonOfRow(stored, "detail"); err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, nil
}

// AlertRow is one stored owner alert as the read serves it: Kind is the event name itself
// (alert.device_paired, alert.remote_allow, …), Body the event's JSON payload, ReadAt nil
// until the owner reads it.
type AlertRow struct {
	ID     int64        `json:"id"`
	At     string       `json:"at"`
	Kind   string       `json:"kind"`
	Body   *jsfmt.Value `json:"json"`
	ReadAt *string      `json:"read_at"`
}

// AlertsFilter reads the alerts: UnreadOnly hides what the owner already read; Limit bounds the
// page the way AuditFilter's does.
type AlertsFilter struct {
	UnreadOnly bool
	Limit      int
}

// RaiseAlert appends one owner alert, unread, and answers its row id. A pairing raises its own
// through pair.go's transaction; this is the entry point the later producers (a satellite
// added, a rule changed, a remote allow) call, so every alert lands the one same shape.
func (d *DB) RaiseAlert(ctx context.Context, atMS int64, kind string, body any) (int64, error) {
	// the column holds the event's payload and is NOT NULL, so a raise with nothing to say
	// stores the empty object
	payload := any("{}")
	if body != nil {
		raw, err := domain.MarshalCompactJSON(body)
		if err != nil {
			return 0, err
		}
		payload = string(raw)
	}
	row, err := d.st.Get(ctx,
		"INSERT INTO alerts (at, kind, json) VALUES (?, ?, ?) RETURNING id", atMS, kind, payload)
	if err != nil {
		return 0, err
	}
	return intOfRow(row, "id"), nil
}

// Alerts answers the raised alerts newest first, the unread ones only when the filter says so.
// A body that is not JSON serves as nil: one broken payload never hides the rest.
func (d *DB) Alerts(ctx context.Context, f AlertsFilter) ([]AlertRow, error) {
	limit := boundedLimit(f.Limit)
	query := "SELECT id, at, kind, json, read_at FROM alerts"
	if f.UnreadOnly {
		query += " WHERE read_at IS NULL"
	}
	rows, err := d.st.All(ctx, query+" ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	out := make([]AlertRow, 0, len(rows))
	for _, stored := range rows {
		alert := AlertRow{
			ID:   intOfRow(stored, "id"),
			At:   isoOfRow(stored, "at"),
			Kind: textOfRow(stored, "kind"),
		}
		alert.Body, _ = jsonOfRow(stored, "json")
		if read := intOfRow(stored, "read_at"); read != 0 {
			at := jsscalar.ISOTime(time.UnixMilli(read))
			alert.ReadAt = &at
		}
		out = append(out, alert)
	}
	return out, nil
}

// MarkAlertsRead marks every alert up to and including id read at atMS, and answers how many
// it marked. An alert already read keeps its first marking: the read state moves forward only.
func (d *DB) MarkAlertsRead(ctx context.Context, through int64, atMS int64) (int64, error) {
	marked := int64(0)
	err := d.st.Tx(ctx, func(ctx context.Context, q store.Q) error {
		if err := q.Run(ctx,
			"UPDATE alerts SET read_at = ? WHERE id <= ? AND read_at IS NULL", atMS, through); err != nil {
			return err
		}
		// changes() and its UPDATE share one transaction on the store's one connection, so no
		// other statement can sit between them
		row, err := q.Get(ctx, "SELECT changes() AS marked")
		if err != nil {
			return err
		}
		marked = intOfRow(row, "marked")
		return nil
	})
	return marked, err
}

// boundedLimit pulls a page size into 1 to 500, with 0 taking the default.
func boundedLimit(limit int) int {
	if limit == 0 {
		return auditLimitDefault
	}
	if limit < 1 {
		return 1
	}
	if limit > auditLimitMax {
		return auditLimitMax
	}
	return limit
}

// jsonOfRow reads one column's stored JSON back exactly as it came; an empty column answers
// nil, and text that is not JSON answers the error.
func jsonOfRow(row map[string]any, col string) (*jsfmt.Value, error) {
	raw := textOfRow(row, col)
	if raw == "" {
		return nil, nil
	}
	return jsfmt.DecodeValue([]byte(raw))
}

// isoOfRow reads one column's whole milliseconds as the ISO timestamp every served row carries.
func isoOfRow(row map[string]any, col string) string {
	return jsscalar.ISOTime(time.UnixMilli(intOfRow(row, col)))
}

// intOfRow reads one column as an integer; NULL and other types read as 0.
func intOfRow(row map[string]any, col string) int64 {
	n, _ := row[col].(int64)
	return n
}
