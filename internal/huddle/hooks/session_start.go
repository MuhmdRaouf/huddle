// The SessionStart hook: when this project names a channel (HUDDLE_CHANNEL + HUDDLE_AS), join
// it and put the picture (who is here, the turn, the pause, the inbox, unread events or a brief,
// the next task, the latest shared knowledge) into the session's context. When no channel is
// configured it stays silent, but still starts the server when nothing answers: the hub, the
// Now list and the reports have no session to speak for them. The hook input's source picks the
// join context (the cli package's ContextFor): resume joins with sync, clear or compact with
// fresh, startup leaves it to the server; HUDDLE_CONTEXT overrides it. Autostart is on unless
// turned off (HUDDLE_AUTOSTART=0, or "autostart": false in the project's huddle.json): when
// nothing answers, it starts the bundled server (`huddle up`) — with or without a channel — and
// joins; otherwise it says how to start it.
//
// Being in a channel takes a credential (the auth package), kept per session and per project
// (the cli package's credentials), so the next session in a project that joined is in too. A
// session that holds no credential gets one line with the way back in, and stays out. Every
// start also puts the plugin's bin/ on the PATH of the session's Bash commands
// (CLAUDE_ENV_FILE), so `huddle …` works there and in subagents. The whole hook runs on a 3 s
// budget (each network wait takes what is left of it), so a slow or mute Huddle costs the
// session start at most that; anything that fails ends in silence (quiet.go).
package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
)

// joinAnswer is one join's outcome: the HTTP status and the op's answer.
type joinAnswer struct {
	Status int
	Error  string
	Text   string
}

// SessionStart joins this project's channel and puts the picture in the session's context.
func SessionStart(ctx context.Context, w *cli.World, in io.Reader, out io.Writer) {
	Run(ctx, w, "session-start", func() error {
		t0 := nowOf(w)
		left := func() int {
			return max(1, startBudgetMS-int(nowOf(w).Sub(t0).Milliseconds()))
		}
		var input struct {
			Source    string `json:"source"`
			SessionID string `json:"session_id"`
		}
		readInput(in, startStdinMS, &input)
		// the one-time import's notice is said here, once, whatever the project configures
		if notices := home.PendingImportNotices(cli.HomeOf(ctx, w)); len(notices) > 0 {
			writeContext(out, "SessionStart", strings.Join(notices, "\n"))
		}
		onPath(w)
		id := cli.Identify(ctx, w, input.SessionID)
		if id.Channel == "" || id.As == "" {
			// no channel to join: the server still comes up, because the hub, the Now list and
			// the reports have no joined session to start it for them; the start's answer stays
			// unsaid (nothing to join, nothing to report), and a server still starting when the
			// budget ends keeps starting detached
			if id.Autostart && !cli.Healthy(ctx, id.URL, min(1000, left())) {
				cli.Up(ctx, w, id.URL, left(), input.SessionID)
			}
			return nil
		}
		joinContext := cli.ContextFor(input.Source, id.Context)
		join := func(rawURL string, ms int) *joinAnswer {
			if rawURL == "" {
				return nil // this project's server has never started: no port yet
			}
			body, err := domain.MarshalCompactJSON(struct {
				Role          string `json:"role"`
				Task          string `json:"task"`
				Context       string `json:"context,omitempty"`
				ClaudeSession string `json:"claude_session,omitempty"`
			}{id.Role, "session started", joinContext, input.SessionID})
			if err != nil {
				return nil
			}
			callURL := fmt.Sprintf("%s/api/c/%s/op/join?as=%s", rawURL, url.PathEscape(id.Channel), url.PathEscape(id.As))
			resp, err := cli.HFetch(ctx, w, http.MethodPost, callURL,
				map[string]string{"content-type": "application/json"}, body,
				time.Duration(ms)*time.Millisecond, input.SessionID)
			if err != nil {
				return nil
			}
			defer resp.Body.Close()
			var fields struct {
				Error string `json:"error"`
				Text  string `json:"text"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&fields); err != nil {
				return nil
			}
			return &joinAnswer{Status: resp.StatusCode, Error: fields.Error, Text: fields.Text}
		}
		say := func(text string) { writeContext(out, "SessionStart", text) }
		// with autostart, the first try leaves room to start the server and join it
		firstMS := startJoinMS
		if id.Autostart {
			firstMS = 1000
		}
		serverURL := id.URL
		j := join(serverURL, min(firstMS, left()))
		started := ""
		if j == nil && id.Autostart && left() > 300 {
			up := cli.Up(ctx, w, serverURL, left(), input.SessionID)
			if up.OK {
				port := ""
				if parsed, err := url.Parse(up.URL); err == nil {
					port = parsed.Port()
				}
				started = "Huddle started (port " + port + ")" // a first start picks the port
			} else {
				started = up.Msg
			}
			serverURL = up.URL
			if up.OK && left() > 50 {
				j = join(serverURL, left())
			}
		}
		switch {
		case j == nil:
			address := serverURL
			if address == "" {
				address = "its address (none yet: huddle up picks a port)"
			}
			note := ""
			if started != "" {
				note = " (" + started + ")"
			}
			say(`Huddle channel "` + id.Channel + `" is configured but the service at ` + address + ` does not answer` + note +
				". Treat yourself as paused for shared work: tell the user to start it (/huddle:setup, or `huddle up`), then call status.")
		case j.Status == 401:
			say(`Huddle "` + id.Channel + `" runs at ` + serverURL + ", but this session holds no credential for it" +
				" (it was never invited here, or it was kicked). To get in: in a session that is in it, the user runs" +
				" /huddle:invite and pastes its join line here (/huddle:join …); if no such session is left," +
				" /huddle:setup --restart in the project that started it. Until then, leave Huddle alone.")
		case j.Error != "":
			say(fmt.Sprintf("Huddle: joining channel %s as %s failed: %s. Tell the owner; do not work around it.",
				id.Channel, id.As, j.Error))
		default:
			// from here on, the listen hook brings each new message into this session (the join
			// shows the rest)
			_ = cli.Feed(ctx, w, cli.Identify(ctx, w, input.SessionID), cli.Home(ctx, w), input.SessionID,
				cli.FeedOptions{Start: true, MS: left()})
			startedPart := ""
			if started != "" {
				startedPart = " " + started + "."
			}
			extra := ""
			if len(id.Listen) > 0 {
				extra = " and in " + strings.Join(id.Listen, ", ")
			}
			say("You are in Huddle channel \"" + id.Channel + "\" as \"" + id.As +
				"\" (already joined: call status to refresh, join only to change your role)." + startedPart +
				" Use the huddle skill: tools mcp__plugin_huddle_huddle__*, or `huddle` from Bash (on PATH;" +
				" subagents use it with HUDDLE_AS=" + id.As + ".<role>). New messages in the channel" + extra +
				" arrive in your context after each tool call, also those between other sessions" +
				" (overheard: knowledge, not yours to answer). Joined just now:\n" + j.Text)
		}
		return nil
	})
}

// onPath puts the plugin's bin/ on the PATH of this session's Bash commands (and its
// subagents'), once per session: Claude Code points CLAUDE_ENV_FILE at the file it sources for
// them and sets CLAUDE_PLUGIN_ROOT to the plugin it runs this hook from.
func onPath(w *cli.World) {
	file := getenvOf(w, "CLAUDE_ENV_FILE")
	root := getenvOf(w, "CLAUDE_PLUGIN_ROOT")
	if file == "" || root == "" {
		return
	}
	line := `export PATH="` + filepath.Join(root, "bin") + `:$PATH"`
	if raw, err := os.ReadFile(file); err == nil && strings.Contains(string(raw), line) {
		return
	}
	envFile, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer envFile.Close()
	_, _ = envFile.WriteString(line + "\n")
}
