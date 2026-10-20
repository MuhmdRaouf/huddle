// The plan-limits snapshots: what Claude Code hands its statusline command (the 5-hour and
// weekly windows' used percentage and reset time) reaches the server as a POST from the
// statusline word, the newest overall lands in the radar home's quota.json, and the Overview's
// Plan limits card reads it back. The snapshot is the only source of plan limits — no
// credential is ever read or sent.
package server

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
	"github.com/MuhmdRaouf/huddle/internal/radar/settings"
)

// QuotaSnapshot is one session's report: the two windows' used percentages and reset times,
// unix seconds, any of them absent when the session did not know. The zero value reads as the
// `{}` the read answers when nothing is stored.
type QuotaSnapshot struct {
	SessionID        string   `json:"session_id,omitempty"`
	FiveHour         *float64 `json:"five_hour,omitempty"`
	FiveHourResetsAt *int64   `json:"five_hour_resets_at,omitempty"`
	SevenDay         *float64 `json:"seven_day,omitempty"`
	SevenDayResetsAt *int64   `json:"seven_day_resets_at,omitempty"`
	At               int64    `json:"at,omitempty"`
}

// Quotas holds the snapshots the sessions posted: the latest one per session in memory, the
// newest overall in the state dir's quota.json, read back on a restart so the card survives one.
type Quotas struct {
	mu     sync.Mutex
	env    domain.EnvLookup
	latest map[string]QuotaSnapshot
	now    func() int64
}

// CreateQuotas builds the store and reads what an earlier run left in quota.json.
func CreateQuotas(env domain.EnvLookup) *Quotas {
	q := &Quotas{env: env, latest: map[string]QuotaSnapshot{}, now: func() int64 { return time.Now().Unix() }}
	if raw, err := os.ReadFile(paths.QuotaPath(context.Background(), env)); err == nil {
		if snapshot, ok := decodeQuota(raw); ok {
			q.latest[snapshot.SessionID] = snapshot
		}
	}
	return q
}

// Record holds one snapshot: the session's latest in memory, the newest overall on disk. An
// older report of a session never replaces a newer one it holds.
func (q *Quotas) Record(ctx context.Context, snapshot QuotaSnapshot) (QuotaSnapshot, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if held, ok := q.latest[snapshot.SessionID]; ok && held.At > snapshot.At {
		snapshot = held
	}
	q.latest[snapshot.SessionID] = snapshot
	return snapshot, settings.WriteJSONAtomic(paths.QuotaPath(ctx, q.env), q.newestLocked())
}

// Newest answers the newest snapshot held, false when there is none.
func (q *Quotas) Newest() (QuotaSnapshot, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.newestLocked(), len(q.latest) > 0
}

// newestLocked answers the snapshot with the largest At, the caller's mutex held; a tie keeps
// the one already held.
func (q *Quotas) newestLocked() QuotaSnapshot {
	newest := QuotaSnapshot{}
	for _, one := range q.latest {
		if one.At > newest.At {
			newest = one
		}
	}
	return newest
}

// quotaWriteAnswer is the write's answer: the snapshot now held.
type quotaWriteAnswer struct {
	Quota QuotaSnapshot `json:"quota"`
}

// HandleQuotaWrite routes POST /api/quota: the body's fields read as the snapshot, the store
// records it, the answer is what is held now.
func HandleQuotaWrite(ctx context.Context, quotas *Quotas, body *jsfmt.Value) ApiResponse {
	if quotas == nil {
		return errorOf(503, "not available")
	}
	if body == nil || body.Kind() != jsfmt.KindObject {
		return errorOf(400, "the snapshot as a JSON object")
	}
	snapshot := QuotaSnapshot{
		SessionID:        quotaText(body, "session_id"),
		FiveHour:         quotaNumber(body, "five_hour"),
		FiveHourResetsAt: quotaSeconds(body, "five_hour_resets_at"),
		SevenDay:         quotaNumber(body, "seven_day"),
		SevenDayResetsAt: quotaSeconds(body, "seven_day_resets_at"),
		At:               quotaAt(body, quotas.now()),
	}
	held, err := quotas.Record(ctx, snapshot)
	if err != nil {
		return errorOf(500, err.Error())
	}
	return jsonOf(200, quotaWriteAnswer{Quota: held})
}

// HandleQuotaRead routes GET /api/quota: the newest snapshot, or the empty object.
func HandleQuotaRead(quotas *Quotas) ApiResponse {
	if quotas == nil {
		return errorOf(503, "not available")
	}
	newest, ok := quotas.Newest()
	if !ok {
		return jsonOf(200, struct{}{})
	}
	return jsonOf(200, newest)
}

// quotaText answers one string field, empty when it is not one.
func quotaText(body *jsfmt.Value, key string) string {
	if field := fieldOf(body, key); field != nil && field.Kind() == jsfmt.KindString {
		return field.Text()
	}
	return ""
}

// quotaNumber answers one number field, nil when it is not a number (a null is absent).
func quotaNumber(body *jsfmt.Value, key string) *float64 {
	if value, ok := quotaNumberField(body, key); ok {
		return &value
	}
	return nil
}

// quotaSeconds answers one unix-seconds field, nil when it is not a number.
func quotaSeconds(body *jsfmt.Value, key string) *int64 {
	if value, ok := quotaNumberField(body, key); ok {
		whole := int64(value)
		return &whole
	}
	return nil
}

// quotaNumberField answers one number field the way numberFieldOf reads it.
func quotaNumberField(body *jsfmt.Value, key string) (float64, bool) {
	if field := fieldOf(body, key); field != nil {
		return numberFieldOf(field)
	}
	return 0, false
}

// quotaAt answers the report's own at, the server's now when it did not carry one.
func quotaAt(body *jsfmt.Value, fallback int64) int64 {
	if value, ok := quotaNumberField(body, "at"); ok {
		return int64(value)
	}
	return fallback
}

// decodeQuota reads quota.json's bytes back into a snapshot; false when they hold none.
func decodeQuota(raw []byte) (QuotaSnapshot, bool) {
	value, err := jsfmt.DecodeValue(raw)
	if err != nil || value.Kind() != jsfmt.KindObject {
		return QuotaSnapshot{}, false
	}
	snapshot := QuotaSnapshot{
		SessionID:        quotaText(value, "session_id"),
		FiveHour:         quotaNumber(value, "five_hour"),
		FiveHourResetsAt: quotaSeconds(value, "five_hour_resets_at"),
		SevenDay:         quotaNumber(value, "seven_day"),
		SevenDayResetsAt: quotaSeconds(value, "seven_day_resets_at"),
		At:               quotaAt(value, 0),
	}
	if snapshot.SessionID == "" && snapshot.At == 0 {
		return QuotaSnapshot{}, false
	}
	return snapshot, true
}
