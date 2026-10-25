package shared

// SourceOtel tags a row that came from Claude Code's OpenTelemetry export, a source radar keeps
// beside the transcripts and never sums into them.
const SourceOtel = "otel"

// The two Claude Code metrics radar keeps from the export.
const (
	OtelTokenUsage = "claude_code.token.usage"
	OtelCostUsage  = "claude_code.cost.usage"
)

// OtelRow is one data point of Claude Code's OpenTelemetry export: a token count of one kind or
// a cost in USD, for one session and model, over the interval from StartTs to Ts (epoch ms).
// A delta point counts its interval only; a cumulative point counts everything since StartTs,
// so the newest point of a series is its whole total.
type OtelRow struct {
	Source    string `json:"source"`
	Metric    string `json:"metric"`
	SessionID string `json:"sessionId"`
	Model     string `json:"model"`
	// Type is the token kind Claude Code names: input, output, cacheRead or cacheCreation;
	// empty on a cost point.
	Type       string  `json:"type,omitempty"`
	Value      float64 `json:"value"`
	StartTs    int64   `json:"startTs"`
	Ts         int64   `json:"ts"`
	Cumulative bool    `json:"cumulative"`
}
