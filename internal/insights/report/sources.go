package report

import (
	"context"
	"time"
)

// Window is the slice of history a report covers: the last days, or one session. A source
// returns only rows that fall inside it.
type Window struct {
	From, To time.Time
	Session  string // the session id of a one-session window; "" for a time window
}

// LastDays is the window of the given number of days that ends now.
func LastDays(now time.Time, days int) Window {
	return Window{From: now.AddDate(0, 0, -days), To: now}
}

// OneSession is the window of one session's whole life.
func OneSession(id string) Window { return Window{Session: id} }

// Tokens is the four token counts a request is priced on.
type Tokens struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
}

// Total is every token the counts name.
func (t Tokens) Total() int64 { return t.Input + t.Output + t.CacheRead + t.CacheWrite }

func (t Tokens) plus(o Tokens) Tokens {
	return Tokens{t.Input + o.Input, t.Output + o.Output, t.CacheRead + o.CacheRead, t.CacheWrite + o.CacheWrite}
}

// The row types below are what the builder reads. Each holds identifiers, numbers and times and
// no message or body text, so a reader that fills them cannot pass text through. Repo is the one
// path; the builder turns it into a label.

// Request is one model request from the ledger.
type Request struct {
	Session    string
	Model      string
	Provider   string // the lane that served it
	At         time.Time
	DurationMs int64 // 0 when unknown
	Status     int   // the answer's HTTP status; 0 when the upstream never answered
	Tokens     Tokens
	CostUSD    float64
}

// Session is one session's life from the registry.
type Session struct {
	ID    string
	Repo  string // the repo root it ran in; "" when unknown
	Start time.Time
	End   time.Time // zero while it runs
}

// ToolRun is one tool call from the transcript-derived store.
type ToolRun struct {
	Session    string
	Tool       string // the tool's name
	InputKey   string // a hash of the tool and its input: equal keys are the same call; "" when unknown
	At         time.Time
	DurationMs int64 // 0 when unknown
	OK         bool
}

// Compaction is one context compaction a session went through.
type Compaction struct {
	Session string
	At      time.Time
}

// SubagentRun is one subagent a session started.
type SubagentRun struct {
	Session string
	Started time.Time
	Stopped time.Time // zero while no stop was seen
}

// Ask is one question or permission prompt that stopped a session until the owner answered.
type Ask struct {
	Session   string
	Decision  string // "allow", "deny", or "" while unanswered
	CreatedAt time.Time
	DecidedAt time.Time // zero while unanswered
}

// RequestSource reads the ledger and the session registry.
type RequestSource interface {
	Requests(ctx context.Context, w Window) ([]Request, error)
	Sessions(ctx context.Context, w Window) ([]Session, error)
}

// ToolSource reads tool calls.
type ToolSource interface {
	ToolRuns(ctx context.Context, w Window) ([]ToolRun, error)
}

// TranscriptSource reads the structure events a transcript holds besides tool calls.
type TranscriptSource interface {
	Compactions(ctx context.Context, w Window) ([]Compaction, error)
	Subagents(ctx context.Context, w Window) ([]SubagentRun, error)
}

// AskSource reads the hub's needs-you history.
type AskSource interface {
	Asks(ctx context.Context, w Window) ([]Ask, error)
}

// Sources is every store a report reads; all four are required.
type Sources struct {
	Requests    RequestSource
	Tools       ToolSource
	Transcripts TranscriptSource
	Asks        AskSource
}
