// What the owner sent this session and it has not read yet: the listen hook
// takes it on the session's next prompt, as the second delivery route when no channel push
// carried it. One bounded POST, silent on any failure, never blocking the hook.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// TakeMessages answers the owner's messages waiting for one session, as the session reads them,
// oldest first; nil when the server is down, refuses the session's credential, or answers
// nothing shaped like a take.
func TakeMessages(ctx context.Context, w *World, id Identity, session string, ms int) []string {
	if id.URL == "" || session == "" {
		return nil
	}
	if ms <= 0 {
		ms = 1500
	}
	callURL := fmt.Sprintf("%s/api/sessions/%s/messages", id.URL, url.PathEscape(session))
	resp, err := HFetch(ctx, w, http.MethodPost, callURL,
		map[string]string{"content-type": "application/json"}, []byte("{}"),
		time.Duration(ms)*time.Millisecond, session)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	var answer struct {
		Messages []string `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return nil
	}
	return answer.Messages
}
