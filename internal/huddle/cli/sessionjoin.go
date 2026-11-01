// The channel join a session makes for itself, sent by the SessionStart hook and, when the hook's
// did not land, by the session's MCP bridge: one URL and one body, so both say the same thing.
package cli

import (
	"fmt"
	"net/url"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// SessionJoinCall answers the URL and the body of this session's join of its channel at the hub
// serverURL. joinContext is the join mode (ContextFor); claudeSession is the Claude Code session
// id the hub links to the member. A role the project does not name is left out, so a join never
// blanks the role an earlier one stored.
func SessionJoinCall(id Identity, serverURL, joinContext, claudeSession string) (string, []byte, error) {
	body, err := domain.MarshalCompactJSON(struct {
		Role          string `json:"role,omitempty"`
		Task          string `json:"task"`
		Context       string `json:"context,omitempty"`
		ClaudeSession string `json:"claude_session,omitempty"`
	}{id.Role, "session started", joinContext, claudeSession})
	if err != nil {
		return "", nil, err
	}
	callURL := fmt.Sprintf("%s/api/c/%s/op/join?as=%s", serverURL, url.PathEscape(id.Channel), url.PathEscape(id.As))
	return callURL, body, nil
}
