package report

// Structure is the payload an insights run sends to the model: numbers and short labels about
// where tokens went, what blocked the owner and what was wasted. It holds no prompt, response or
// tool text and no path; every repo and session is a label from Labels. A window whose sessions
// would not fit under MaxInputTokens is folded: Sessions is empty and Days holds each day's
// totals and busiest sessions.
type Structure struct {
	Window    WindowView   `json:"window"`
	Totals    Totals       `json:"totals"`
	Sessions  []SessionRow `json:"sessions"`
	Models    []ModelRow   `json:"models"`
	Tools     []ToolRow    `json:"tools"`
	Requests  RequestStats `json:"requests"`
	Blocked   Blocked      `json:"blocked"`
	Waste     Waste        `json:"waste"`
	Subagents Subagents    `json:"subagents"`
	Days      []DaySummary `json:"days,omitempty"`
}

// WindowView names the window: Days for a time window, Session (a label) for one session.
type WindowView struct {
	Days    int    `json:"days,omitempty"`
	Session string `json:"session,omitempty"`
}

// Totals is the window in four numbers.
type Totals struct {
	Sessions int     `json:"sessions"`
	Requests int     `json:"requests"`
	Tokens   Tokens  `json:"tokens"`
	CostUSD  float64 `json:"costUsd"`
}

// SessionRow is one session's share, ranked by tokens.
type SessionRow struct {
	Label     string  `json:"label"`
	Repo      string  `json:"repo,omitempty"`
	Requests  int     `json:"requests"`
	Tokens    Tokens  `json:"tokens"`
	CostUSD   float64 `json:"costUsd"`
	ToolCalls int     `json:"toolCalls"`
	Subagents int     `json:"subagents"`
}

// ModelRow is one model on one lane, ranked by tokens.
type ModelRow struct {
	Model    string  `json:"model"`
	Provider string  `json:"provider"`
	Requests int     `json:"requests"`
	Tokens   Tokens  `json:"tokens"`
	CostUSD  float64 `json:"costUsd"`
}

// ToolRow is one tool's calls, ranked by count.
type ToolRow struct {
	Name     string `json:"name"`
	Calls    int    `json:"calls"`
	Failures int    `json:"failures"`
	TotalMs  int64  `json:"totalMs"`
	MaxMs    int64  `json:"maxMs"`
}

// RequestStats is how the window's requests ended and how long they took.
type RequestStats struct {
	Count      int      `json:"count"`
	Outcomes   Outcomes `json:"outcomes"`
	DurationMs Timing   `json:"durationMs"`
}

// Outcomes counts requests by how the upstream answered.
type Outcomes struct {
	OK          int `json:"ok"`
	RateLimited int `json:"rateLimited"` // 429
	ServerError int `json:"serverError"` // 5xx
	OtherError  int `json:"otherError"`
	NoAnswer    int `json:"noAnswer"` // status 0
}

// Timing summarises the requests that have a duration.
type Timing struct {
	Count  int   `json:"count"`
	MeanMs int64 `json:"meanMs"`
	P95Ms  int64 `json:"p95Ms"`
	MaxMs  int64 `json:"maxMs"`
}

// Blocked is what held the owner up.
type Blocked struct {
	Asks        AskStats `json:"asks"`
	FailedTools int      `json:"failedTools"`
	ReReads     int      `json:"reReads"` // extra Reads of an input already read in the same session
	Retries     int      `json:"retries"` // requests answered 429
}

// AskStats counts the questions and permission prompts that stopped a session.
type AskStats struct {
	Count              int    `json:"count"`
	Answered           int    `json:"answered"`
	Unanswered         int    `json:"unanswered"`
	Waited             int    `json:"waited"` // answered asks that waited at least askWaited
	TotalWaitMs        int64  `json:"totalWaitMs"`
	LongestWaitMs      int64  `json:"longestWaitMs"`
	LongestWaitSession string `json:"longestWaitSession,omitempty"`
}

// Waste is work that did not move anything forward.
type Waste struct {
	RepeatedCalls int   `json:"repeatedCalls"` // extra calls of a non-Read tool with an input already used in the session
	IdleSessions  int   `json:"idleSessions"`
	IdleMs        int64 `json:"idleMs"` // time in quiet stretches of at least idleGap
	Compactions   int   `json:"compactions"`
	Denied        int   `json:"denied"` // asks the owner denied
}

// Subagents counts the subagents sessions started.
type Subagents struct {
	Count      int `json:"count"`
	Unfinished int `json:"unfinished"` // no stop seen
}
