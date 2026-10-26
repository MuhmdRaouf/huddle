// What the hub knows about this session (the insights rules): the tips hook asks for it on the
// session's start and on every prompt, as one bounded GET. Silent on any failure, never blocking
// the hook.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// SessionTip is one tip the hub answers about a session.
type SessionTip struct {
	ID       string `json:"id"`
	Sentence string `json:"sentence"`
}

// SessionTips asks the hub for the tips about one session within ms; any failure answers none.
func SessionTips(ctx context.Context, w *World, id Identity, session string, ms int) []SessionTip {
	if id.URL == "" || session == "" {
		return nil
	}
	if ms <= 0 {
		ms = 1500
	}
	callURL := fmt.Sprintf("%s/api/insights?session=%s", id.URL, url.QueryEscape(session))
	resp, err := HFetch(ctx, w, http.MethodGet, callURL, nil, nil,
		time.Duration(ms)*time.Millisecond, session)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	var answer struct {
		Tips []SessionTip `json:"tips"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		return nil
	}
	return answer.Tips
}
