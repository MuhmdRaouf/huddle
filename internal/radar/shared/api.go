package shared

import (
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// The view shapes the history reads answer with: tree nodes, roots with their roll-ups,
// the rebuilt conversation, and the token-flow series. Field order matches the JSON the
// dashboard reads.

// NodeKind is which kind of node a history tree holds: a main session, a subagent or a job.
type NodeKind string

const (
	NodeMain     NodeKind = "main"
	NodeSubagent NodeKind = "subagent"
	NodeJob      NodeKind = "job"
)

// NodeFields is the columns every tree node carries, joined with the roll-ups a read adds.
type NodeFields struct {
	ID              string   `json:"id"`
	Kind            NodeKind `json:"kind"`
	ParentID        *string  `json:"parentId"`
	SessionID       string   `json:"sessionId"`
	AgentID         string   `json:"agentId"`
	Label           *string  `json:"label"`
	AgentType       *string  `json:"agentType"`
	Description     *string  `json:"description"`
	Project         *string  `json:"project"`
	Cwd             *string  `json:"cwd"`
	Model           *string  `json:"model"`
	Provider        *string  `json:"provider"`
	ToolUseID       *string  `json:"toolUseId"`
	SpawnDepth      *int64   `json:"spawnDepth"`
	JobState        *string  `json:"jobState"`
	Repo            *string  `json:"repo"`
	Branch          *string  `json:"branch"`
	Name            *string  `json:"name"`
	ParentSessionID *string  `json:"parentSessionId"`
	StartedAt       *int64   `json:"startedAt"`
	EndedAt         *int64   `json:"endedAt"`
	LastAt          int64    `json:"lastAt"`
}

// RootSummary is a top-level node with roll-ups over its whole tree.
type RootSummary struct {
	NodeFields
	Requests  int64    `json:"requests"`
	Tokens    Tokens   `json:"tokens"`
	Nodes     int64    `json:"nodes"`
	LiveNodes int64    `json:"liveNodes"`
	Activity  Activity `json:"activity"`
}

// TreeNodeRow is every node of one tree, each with its own request roll-up and live flag.
type TreeNodeRow struct {
	NodeFields
	Live     bool   `json:"live"`
	Requests int64  `json:"requests"`
	Tokens   Tokens `json:"tokens"`
}

// FlowSeries is the flow a chart draws: one request count and one series per token kind,
// zero-filled, oldest bucket first. A request past the last bucket (the window's right
// edge, or a cap) counts in that last bucket.
type FlowSeries struct {
	From     int64     `json:"from"`
	To       int64     `json:"to"`
	BucketMs int64     `json:"bucketMs"`
	Requests []int64   `json:"requests"`
	Kinds    FlowKinds `json:"kinds"`
}

// FlowKinds is one token-kind series per bucket, the same buckets FlowSeries.Requests
// counts.
type FlowKinds struct {
	Input      []int64 `json:"input"`
	Output     []int64 `json:"output"`
	CacheRead  []int64 `json:"cacheRead"`
	CacheWrite []int64 `json:"cacheWrite"`
}

// ContextMessage is one turn of the conversation as the model saw it: one stored side of
// one request.
type ContextMessage struct {
	Role      string `json:"role"`
	Kind      string `json:"kind"`
	RequestID string `json:"requestId"`
	Ts        int64  `json:"ts"`
	// Bytes is the stored side's own byte size, the way Content counts it.
	Bytes   int64  `json:"bytes"`
	Preview string `json:"preview"`
	// Blocks is the side's blocks, wire shapes as stored (their texts are capped at
	// capture).
	Blocks []JSON `json:"blocks"`
}

// ContextAnswer is a rebuilt conversation: every message up to and including the request's
// input, newest last.
type ContextAnswer struct {
	RequestID string           `json:"requestId"`
	NodeID    string           `json:"nodeId"`
	Model     string           `json:"model"`
	Totals    ContextTotals    `json:"totals"`
	Messages  []ContextMessage `json:"messages"`
	// Next is the cursor of the page before this one ("<ts>:<requestId>:<side>"); null at
	// the conversation's start.
	Next *string `json:"next"`
}

// ContextTotals is the header numbers of a rebuilt conversation.
type ContextTotals struct {
	Messages int64 `json:"messages"`
	// ApproxTokens is the whole conversation's bytes divided by four, the usual
	// characters-per-token rule of thumb.
	ApproxTokens int64 `json:"approxTokens"`
	// Usage is the request's own reported usage; null when the provider sent none.
	Usage *ContextUsage `json:"usage"`
	// CacheTokens is how much of that usage was cache (reads plus writes); 0 without a
	// usage report.
	CacheTokens int64 `json:"cacheTokens"`
}

// ContextUsage is the usage one request reported for itself.
type ContextUsage struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
}

// CaptureAnswer is one request's captured prompt: the system prompt and tool definitions
// the request carried, read back decompressed, with the plain size the capture weighed and
// the response headers the route stored.
type CaptureAnswer struct {
	Hash   string `json:"hash"`
	System JSON   `json:"system"`
	Tools  JSON   `json:"tools"`
	Bytes  int64  `json:"bytes"`
	// Headers are the response headers the route stored, as the pairs in the order its
	// line carried them; null when it stored none.
	Headers *Headers `json:"headers"`
}

// JSON is a decoded JSON value (a capture's system prompt, a message's blocks) that
// re-encodes exactly as it arrived: keys keep their order, numbers print the way
// JavaScript prints them.
type JSON struct {
	Value *jsfmt.Value
}

// MarshalJSON encodes the value as it arrived; an absent one encodes as null.
func (j JSON) MarshalJSON() ([]byte, error) {
	if j.Value == nil {
		return []byte("null"), nil
	}
	return []byte(j.Value.EncodeJSON()), nil
}
