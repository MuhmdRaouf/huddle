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
// session that holds no credential gets one line with the way back in, and stays out. A join that
// gets no answer is tried again inside the budget (session_join.go); when no try lands, the
// session gets one line naming the reason, and the session's MCP bridge keeps trying (the cli
// package's keepJoined). Every start also puts the plugin's bin/ on the PATH of the session's
// Bash commands (CLAUDE_ENV_FILE), so `huddle …` works there and in subagents, and hands radar
// the huddle commit Claude Code's plugin cache holds beside the binary's (radar's plugin_behind
// alert; two local file reads, and the plugin cache is never written). The whole hook runs on a
// 3 s budget (each network wait takes what is left of it), so a slow or mute Huddle costs the
// session start at most that; a join that did not land is one line in hooks.log, and anything
// else that fails ends in silence (quiet.go).
package hooks

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
)

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
		onPath(w)
		_ = cli.RecordPluginSkew(ctx, w)  // report only: a failure costs the alert, never the start
		_ = cli.EnsureSpinnerTips(ctx, w) // names the file the hub rewrites: a failure costs the tips, never the start
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
		// the merged hook process drops a part's output when the part outlives its deadline
		// (hookplan), so the joins and the feed end startSayMarginMS short of the budget and the
		// line about them is still said inside it
		spare := func() int { return max(1, left()-startSayMarginMS) }
		attempt := sessionJoin{w: w, id: id, context: cli.ContextFor(input.Source, id.Context), session: input.SessionID}
		say := func(text string) { writeContext(out, "SessionStart", text) }
		// with autostart, the first try leaves room to start the server and join it
		serverURL := id.URL
		var try joinTry
		started := ""
		if id.Autostart {
			try = attempt.try(ctx, serverURL, min(startAutostartTryMS, spare()))
		} else {
			try = attempt.retry(ctx, serverURL, startJoinTryMS, spare)
		}
		if try.Answer == nil && id.Autostart && spare() > 300 {
			up := cli.Up(ctx, w, serverURL, spare(), input.SessionID)
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
			if up.OK && spare() > 50 {
				try = attempt.retry(ctx, serverURL, spare(), spare)
			}
		}
		switch {
		case try.Answer != nil && try.Answer.Status == 401:
			say(`Huddle "` + id.Channel + `" runs at ` + serverURL + ", but this session holds no credential for it" +
				" (it was never invited here, or it was kicked). To get in: in a session that is in it, the user runs" +
				" /huddle:invite and pastes its join line here (/huddle:join …); if no such session is left," +
				" /huddle:setup --restart in the project that started it. Until then, leave Huddle alone.")
			return errors.New("join " + id.Channel + " as " + id.As + ": 401 no credential for " + serverURL)
		case try.Answer == nil || !try.Answer.joined():
			reason := reasonOf(try)
			say(attempt.failure(reason))
			// the line in hooks.log is all that is left of the start once the session moves on
			note := reason
			if started != "" {
				note += " (" + started + ")"
			}
			return errors.New("join " + id.Channel + " as " + id.As + ": " + note)
		}
		// from here on, the listen hook brings each new message into this session (the join
		// shows the rest)
		_ = cli.Feed(ctx, w, cli.Identify(ctx, w, input.SessionID), cli.Home(ctx, w), input.SessionID,
			cli.FeedOptions{Start: true, MS: spare()})
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
			" (overheard: knowledge, not yours to answer). Joined just now:\n" + try.Answer.Text)
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
