// The TaskCreated hook's channel half: a task Claude Code creates through TaskCreate — a lead
// splitting work for its team, or any session planning its own — is said on the session's
// channel as `task: <subject>`, so every other session sees the work as it is split. A session in
// no channel, a payload without a subject, a Huddle that is down, mute or refuses, and any other
// failure end in silence (quiet.go).
package hooks

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
)

// taskInput is the part of Claude Code's TaskCreated payload this hook reads.
type taskInput struct {
	SessionID   string `json:"session_id"`
	TaskSubject string `json:"task_subject"`
}

// TaskCreated says the created task's subject on the session's channel.
func TaskCreated(ctx context.Context, w *cli.World, in io.Reader, out io.Writer) {
	Run(ctx, w, "task", func() error {
		var input taskInput
		readInput(in, taskStdinMS, &input)
		subject := strings.TrimSpace(input.TaskSubject)
		if subject == "" {
			return nil
		}
		id := cli.Identify(ctx, w, input.SessionID)
		if id.URL == "" || id.Channel == "" || id.As == "" {
			return nil
		}
		body, err := domain.MarshalCompactJSON(struct {
			Msg string `json:"msg"`
		}{"task: " + subject})
		if err != nil {
			return err
		}
		callURL := fmt.Sprintf("%s/api/c/%s/op/send?as=%s", id.URL, url.PathEscape(id.Channel), url.QueryEscape(id.As))
		resp, err := cli.HFetch(ctx, w, http.MethodPost, callURL,
			map[string]string{"content-type": "application/json"}, body,
			time.Duration(taskSendMS)*time.Millisecond, input.SessionID)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("send the task to %s: %d", id.Channel, resp.StatusCode)
		}
		return nil
	})
}
