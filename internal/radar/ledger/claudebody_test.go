package ledger

import (
	"fmt"
	"strings"
)

// The real-shaped request the split cases use: what a Claude Code session sends to the
// messages endpoint, built by hand from filler text. It has the members that make the shape
// real: a system prompt in blocks with a cache marker, tools with nested schemas, a
// conversation of tool_use and tool_result messages, and the object-valued members metadata
// and thinking.

// claudeKeyOrders are the two top-level member orders the cases run: the conversation first,
// and the system prompt first.
var claudeKeyOrders = map[string][]string{
	"messages first": {"model", "messages", "system", "tools", "metadata", "max_tokens", "thinking", "stream"},
	"system first":   {"model", "system", "tools", "messages", "metadata", "max_tokens", "thinking", "stream"},
}

// claudeCodeBody builds the request of the given turn: the opening prompt, then one
// assistant tool_use and one user tool_result per turn, the last message carrying the cache
// marker. Every message, system block and tool is unique, so no two spans of a body match.
func claudeCodeBody(order []string, turns int) []byte {
	members := map[string]string{
		"model":      `"claude-x"`,
		"messages":   claudeMessages(turns),
		"system":     claudeSystem(),
		"tools":      claudeTools(),
		"metadata":   `{"user_id":"user-filler"}`,
		"max_tokens": `32000`,
		"thinking":   `{"type":"enabled","budget_tokens":8000}`,
		"stream":     `true`,
	}
	parts := make([]string, 0, len(order))
	for _, key := range order {
		parts = append(parts, jsonString(key)+":"+members[key])
	}
	return []byte("{" + strings.Join(parts, ",") + "}")
}

func claudeSystem() string {
	return `[{"type":"text","text":` + jsonString("billing line "+strings.Repeat("b", 60)) + `},` +
		`{"type":"text","text":` + jsonString("system rules "+strings.Repeat("s", 4000)) +
		`,"cache_control":{"type":"ephemeral","ttl":"1h"}}]`
}

func claudeTools() string {
	tools := make([]string, 0, 5)
	for at := 0; at < 5; at++ {
		tools = append(tools, fmt.Sprintf(
			`{"name":"tool%d","description":%s,"input_schema":{"type":"object","properties":{"a":{"type":"string"}},"required":["a"]}}`,
			at, jsonString(fmt.Sprintf("tool %d ", at)+strings.Repeat("d", 300))))
	}
	return "[" + strings.Join(tools, ",") + "]"
}

func claudeMessages(turns int) string {
	messages := []string{
		`{"role":"user","content":[{"type":"text","text":` + jsonString("opening prompt "+strings.Repeat("p", 500)) + `}]}`,
	}
	for turn := 1; turn <= turns; turn++ {
		marker := ""
		if turn == turns {
			marker = `,"cache_control":{"type":"ephemeral"}`
		}
		messages = append(messages,
			fmt.Sprintf(`{"role":"assistant","content":[{"type":"text","text":"step %d"},`+
				`{"type":"tool_use","id":"toolu_%02d","name":"tool0","input":{"a":"value %d"}}]}`, turn, turn, turn),
			fmt.Sprintf(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_%02d","content":%s%s}]}`,
				turn, jsonString(fmt.Sprintf("result %d ", turn)+strings.Repeat("r", 3000)), marker))
	}
	return "[" + strings.Join(messages, ",") + "]"
}
