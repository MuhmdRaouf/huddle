package hooks

// Claude Code's agent-team events: a TaskCreated payload puts the task's subject on the session's
// channel, and a TeammateIdle payload's report marks the teammate idle in the registry. The
// payloads are the samples from Claude Code's hooks reference, plus a field the hooks do not read.

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// taskCreatedPayload is the TaskCreated sample from Claude Code's hooks reference, with one field
// no hook reads.
const taskCreatedPayload = `{"session_id":"abc123","transcript_path":"/nowhere.jsonl","cwd":"/nowhere",` +
	`"hook_event_name":"TaskCreated","task_id":"task-001","task_subject":"Implement user authentication",` +
	`"task_description":"Add login and signup endpoints","teammate_name":"implementer",` +
	`"team_name":"session-a1b2c3d4","unknown_field":{"nested":true}}`

// teammateIdlePayload is the TeammateIdle sample from Claude Code's hooks reference.
const teammateIdlePayload = `{"session_id":"lead-session","transcript_path":"/nowhere.jsonl","cwd":"/nowhere",` +
	`"permission_mode":"default","hook_event_name":"TeammateIdle","teammate_name":"researcher",` +
	`"team_name":"session-a1b2c3d4","unknown_field":7}`

// channelMessages reads the channel's history as one member and answers each plain message's
// sender and text (topic msg: no join or status lines).
func channelMessages(t *testing.T, s *huddleServer, channel, as string) [][2]string {
	t.Helper()
	code, body := op(t, s, channel, "events", as, "{}")
	if code != 200 {
		t.Fatalf("events: %d %s", code, body)
	}
	var answer struct {
		Result []struct {
			From  string `json:"from"`
			Topic string `json:"topic"`
			Msg   string `json:"msg"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		t.Fatalf("decode events %s: %v", body, err)
	}
	var out [][2]string
	for _, ev := range answer.Result {
		if ev.Topic == "msg" {
			out = append(out, [2]string{ev.From, ev.Msg})
		}
	}
	return out
}

func TestTheTaskCreatedHookPostsTheTaskToTheChannel(t *testing.T) {
	s := startHuddle(t)
	for _, name := range []string{"lead", "watcher"} {
		if code, body := op(t, s, "teams", "join", name, "{}"); code != 200 {
			t.Fatalf("join %s: %d %s", name, code, body)
		}
	}
	stdout, elapsed, _ := runHook(t, "task-created", taskCreatedPayload,
		map[string]string{"HUDDLE_URL": s.base, "HUDDLE_CHANNEL": "teams", "HUDDLE_AS": "lead"})
	if stdout != "" {
		t.Errorf("the hook printed %q, want silence", stdout)
	}
	if elapsed > 2*time.Second {
		t.Errorf("the hook took %s, want it inside its 2 s part", elapsed)
	}
	var said []string
	for _, m := range channelMessages(t, s, "teams", "watcher") {
		if m[0] == "lead" {
			said = append(said, m[1])
		}
	}
	if len(said) != 1 || said[0] != "task: Implement user authentication" {
		t.Errorf("lead said %q on the channel, want exactly [task: Implement user authentication]", said)
	}
}

func TestTheTaskCreatedHookStaysSilentOutsideAChannelOrWithoutASubject(t *testing.T) {
	cases := map[string]struct {
		input string
		env   map[string]string
	}{
		"huddle down":  {taskCreatedPayload, map[string]string{"HUDDLE_URL": downURL(t), "HUDDLE_CHANNEL": "teams", "HUDDLE_AS": "lead"}},
		"huddle mute":  {taskCreatedPayload, map[string]string{"HUDDLE_URL": muteURL(t), "HUDDLE_CHANNEL": "teams", "HUDDLE_AS": "lead"}},
		"no channel":   {taskCreatedPayload, map[string]string{"HUDDLE_URL": downURL(t)}},
		"no subject":   {`{"session_id":"abc123","hook_event_name":"TaskCreated","task_id":"1"}`, map[string]string{"HUDDLE_URL": downURL(t), "HUDDLE_CHANNEL": "teams", "HUDDLE_AS": "lead"}},
		"not JSON":     {"<html>", map[string]string{"HUDDLE_URL": downURL(t), "HUDDLE_CHANNEL": "teams", "HUDDLE_AS": "lead"}},
		"wrong shapes": {`{"task_subject":42,"session_id":[]}`, map[string]string{"HUDDLE_URL": downURL(t), "HUDDLE_CHANNEL": "teams", "HUDDLE_AS": "lead"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			stdout, elapsed, _ := runHook(t, "task-created", c.input, c.env)
			if stdout != "" {
				t.Errorf("the hook printed %q, want silence", stdout)
			}
			if elapsed > 2*time.Second {
				t.Errorf("the hook took %s, want it inside its 2 s part", elapsed)
			}
		})
	}
}

func TestTheTeammateIdleReportMarksTheTeammateIdleInTheRegistry(t *testing.T) {
	s := startHuddle(t)
	stdout, _, _ := runHook(t, "report", teammateIdlePayload, map[string]string{"HUDDLE_URL": s.base})
	if stdout != "" {
		t.Errorf("the report printed %q, want silence", stdout)
	}
	req, err := http.NewRequest(http.MethodGet, s.base+"/api/registry/sessions", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-huddle-token", s.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("read the registry: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var listed struct {
		Sessions []struct {
			SessionID string `json:"session_id"`
			LastEvent string `json:"last_event"`
			Teammates []struct {
				Name  string `json:"name"`
				State string `json:"state"`
			} `json:"teammates"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatalf("decode the registry %s: %v", raw, err)
	}
	for _, row := range listed.Sessions {
		if row.SessionID != "lead-session" {
			continue
		}
		if row.LastEvent != "TeammateIdle" {
			t.Errorf("last_event = %q, want TeammateIdle", row.LastEvent)
		}
		if len(row.Teammates) != 1 || row.Teammates[0].Name != "researcher" || row.Teammates[0].State != "idle" {
			t.Errorf("teammates = %+v, want [researcher idle]", row.Teammates)
		}
		return
	}
	t.Fatalf("the registry lists no lead-session: %s", strings.TrimSpace(string(raw)))
}
