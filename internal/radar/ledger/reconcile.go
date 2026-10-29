package ledger

import (
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// ReconcileVersion is the version of Reconcile's rules. Every row is written with it; a
// change to the rules raises it, and the backfill rebuilds each older row from its facts.
const ReconcileVersion = 5

// Reconcile answers the row the sources agree on, false when no source knows the request
// (every pointer nil).
func Reconcile(s Sources) (Row, bool) {
	if s.Route == nil && s.Transcript == nil && s.OTel == nil {
		return Row{}, false
	}
	row := Row{InTranscript: s.Transcript != nil}
	ids(s, &row)
	request(s, &row)
	timing(s, &row)
	lane(s, &row)
	usage(s, &row)
	billing(s, &row)
	capture(s, &row)
	node(s, &row)
	outcome(s, &row)
	turn(s, &row)
	return row, true
}

// ids names the row: the request-id header when the answer carried one, else the route's
// prompt hash joined to its ts, else the transcript's request id.
func ids(s Sources, row *Row) {
	if s.Route != nil && s.Route.Event.Headers != nil {
		row.AnthropicID = (*s.Route.Event.Headers)["request-id"]
	}
	if row.AnthropicID == "" && s.Transcript != nil {
		row.AnthropicID = s.Transcript.RequestID
	}
	if row.AnthropicID == "" && s.OTel != nil {
		row.AnthropicID = s.OTel.RequestID
	}
	switch {
	case row.AnthropicID != "":
		row.ID = row.AnthropicID
	case s.Route != nil && s.Route.Event.PromptHash != nil:
		row.ID = *s.Route.Event.PromptHash + ":" + s.Route.Event.Ts
	case s.Transcript != nil:
		row.ID = s.Transcript.RequestID
	}
}

// request reads which model served, who sent it and where it sat in its turn, all the
// route's own facts; the transcript backs up the model and the prompt id when no route saw
// the request.
func request(s Sources, row *Row) {
	if s.Route == nil {
		if s.Transcript != nil {
			row.Model = s.Transcript.Model
			row.PromptID = s.Transcript.PromptID
		}
		return
	}
	event := s.Route.Event
	row.Model = event.Model
	row.FallbackFrom = textOf(event.Fallback)
	row.Upstream = event.Upstream
	row.SessionID = textOf(event.SessionID)
	row.AgentID = textOf(event.AgentID)
	row.ParentAgentID = textOf(event.ParentAgentID)
	row.PromptHash = textOf(event.PromptHash)
	row.PromptID = textOf(event.PromptID)
	row.PrevRequest = textOf(event.PrevRequest)
	row.TurnIndex = event.TurnIndex
	if row.PromptID == "" && s.Transcript != nil {
		row.PromptID = s.Transcript.PromptID
	}
}

// timing reads the clock: when the router received the request, how long the answer took,
// and when its first token came back. A request no route saw starts when its transcript
// says and still takes OTel's duration.
func timing(s Sources, row *Row) {
	if s.Route != nil {
		if started, err := time.Parse(time.RFC3339, s.Route.Event.Ts); err == nil {
			row.StartedAt = started.UnixMilli()
		}
		row.DurationMs = s.Route.Event.LatencyMs
		row.TtfbMs = s.Route.Event.TtfbMs
		row.TtftMs = s.Route.Event.TtftMs
	} else {
		if s.Transcript != nil && s.Transcript.Ts > 0 {
			row.StartedAt = s.Transcript.Ts
		}
		if s.OTel != nil {
			row.DurationMs = s.OTel.DurationMs
		}
	}
	if row.TtftMs == nil && s.OTel != nil {
		row.TtftMs = s.OTel.TtftMs
	}
}

// lane names the path the request took and the harness that sent it.
func lane(s Sources, row *Row) {
	row.Harness = "claude"
	if s.Route == nil {
		return
	}
	event := s.Route.Event
	switch {
	case event.Route == "anthropic":
		row.Lane = "anthropic"
	case event.Route == "provider":
		row.Lane = "provider:" + event.Plugin
	case event.Harness != "" && event.Via == "gateway":
		row.Lane = "gateway:" + event.Harness
	default:
		row.Lane = event.Route
	}
	if event.Harness != "" {
		row.Harness = event.Harness
	}
}

// chosenUsage answers the usage map the first source that carries one holds, named by its
// source: the router's report first, OTel and the transcript as backups.
func chosenUsage(s Sources) (domain.RouteUsage, string) {
	switch {
	case s.Route != nil && s.Route.Event.Usage != nil:
		return *s.Route.Event.Usage, "router"
	case s.OTel != nil && len(s.OTel.Usage) > 0:
		return s.OTel.Usage, "otel"
	case s.Transcript != nil && len(s.Transcript.Usage) > 0:
		return s.Transcript.Usage, "transcript"
	}
	return nil, ""
}

// usage reads the counters, the price and the limits: the router's report first, OTel and
// the transcript as backups.
func usage(s Sources, row *Row) {
	counters, source := chosenUsage(s)
	row.UsageSource = source
	row.Input = counterOf(counters, "input_tokens")
	row.Output = counterOf(counters, "output_tokens")
	row.CacheRead = counterOf(counters, "cache_read_input_tokens")
	row.CacheWrite = counterOf(counters, "cache_creation_input_tokens")
	if s.Route != nil {
		row.PriceUSD = s.Route.PriceUSD
		if limits := s.Route.Event.Utilization; limits != nil {
			if limits.FiveHour != nil {
				row.Ratelimit5h = &limits.FiveHour.Used
			}
			if limits.SevenDay != nil {
				row.Ratelimit7d = &limits.SevenDay.Used
			}
		}
	}
	if s.OTel != nil {
		row.HarnessCostUSD = s.OTel.HarnessCostUSD
	}
}

// outcome grades how the request ended, the router's verdict first, an OTel api_error as
// backup.
func outcome(s Sources, row *Row) {
	if s.Route != nil {
		event := s.Route.Event
		row.Status = event.Status
		if event.Error != nil {
			row.ErrorCode = *event.Error
		}
		switch {
		case event.Status >= 200 && event.Status < 300:
			row.Outcome = "ok"
		case event.Route == "refused":
			row.Outcome = "refused"
		case (event.Status == 0 || event.Status == 502 || event.Status == 503) && event.Error != nil:
			row.Outcome = "upstream_failed"
		default:
			row.Outcome = "http_error"
		}
		return
	}
	if s.OTel != nil && s.OTel.ErrorCode != "" {
		row.Outcome = "http_error"
		row.ErrorCode = s.OTel.ErrorCode
	}
}

// turn reads where the request sat in Claude Code's turn, as the transcript tells it.
func turn(s Sources, row *Row) {
	if s.Transcript == nil {
		return
	}
	row.QuerySource = s.Transcript.QuerySource
	row.StopReason = s.Transcript.StopReason
}

// counterOf answers one usage counter, accepting the float64 JSON decodes and the int64 a
// build in Go may hold, 0 for a missing key.
func counterOf(counters domain.RouteUsage, key string) int64 {
	switch value := counters[key].(type) {
	case float64:
		return int64(value)
	case int64:
		return value
	}
	return 0
}

// textOf reads a nullable route field, "" when it names none.
func textOf(text *string) string {
	if text == nil {
		return ""
	}
	return *text
}
