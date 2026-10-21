package store

import (
	"strconv"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// str is a string pointer for a record field a test wants set.
func str(text string) *string { return &text }

// i64 is an int64 pointer for a record field a test wants set.
func i64(value int64) *int64 { return &value }

// held lifts records to the pointers the store's slices carry, so a test builds values.
func held(records []shared.RequestRecord) []*shared.RequestRecord {
	out := make([]*shared.RequestRecord, len(records))
	for i := range records {
		out[i] = &records[i]
	}
	return out
}

// heldTools lifts tool calls the same way.
func heldTools(tools []shared.ToolCallRecord) []*shared.ToolCallRecord {
	out := make([]*shared.ToolCallRecord, len(tools))
	for i := range tools {
		out[i] = &tools[i]
	}
	return out
}

// showInt renders an optional number the way a failure message wants to read it.
func showInt(value *int64) string {
	if value == nil {
		return "null"
	}
	return strconv.FormatInt(*value, 10)
}

// makeRequest is the suite's base request, each case reshaping through shape.
func makeRequest(shape func(*shared.RequestRecord)) shared.RequestRecord {
	r := shared.RequestRecord{
		ID:        "req-1",
		SessionID: "s1",
		AgentID:   "main",
		Model:     "claude-sonnet-5-5",
		Upstream:  "https://api.anthropic.com",
		Ts:        1_000,
		LatencyMs: i64(100),
		Tokens:    shared.Tokens{Input: 10, Output: 5},
		Provider:  "Anthropic",
	}
	if shape != nil {
		shape(&r)
	}
	return r
}

// makeTool is the suite's base tool call, each case reshaping through shape.
func makeTool(shape func(*shared.ToolCallRecord)) shared.ToolCallRecord {
	tool := shared.ToolCallRecord{
		ID:         "tool-1",
		SessionID:  "s1",
		AgentID:    str("main"),
		Name:       "Bash",
		StartedAt:  1_000,
		DurationMs: i64(50),
		Ok:         true,
	}
	if shape != nil {
		shape(&tool)
	}
	return tool
}

// makeAgentView is the suite's base agent row, each case reshaping through shape.
func makeAgentView(shape func(*shared.AgentView)) shared.AgentView {
	view := shared.AgentView{
		ID:        "main",
		SessionID: "s1",
		Kind:      shared.KindMain,
		Name:      str("main"),
		Tokens:    shared.ZeroTokens,
		Live:      true,
	}
	if shape != nil {
		shape(&view)
	}
	return view
}

// makeSessionView is the suite's base session card, each case reshaping through shape.
func makeSessionView(shape func(*shared.SessionView)) shared.SessionView {
	view := shared.SessionView{
		ID:        "s1",
		Cwd:       str("/Users/smoke/work/app"),
		Project:   str("app"),
		StartedAt: i64(1_000),
		Live:      true,
		Activity: shared.Activity{
			BucketMs: shared.ActivityLiveBucketMs,
			Counts:   make([]int64, shared.ActivityBuckets),
			Models:   make([]string, shared.ActivityBuckets),
		},
		Model:     str("claude-sonnet-5-5"),
		Upstream:  "https://api.anthropic.com",
		CcVersion: str("2.0.0"),
		Tokens:    shared.ZeroTokens,
		Agents:    []shared.AgentView{},
		External:  false,
	}
	if shape != nil {
		shape(&view)
	}
	return view
}
