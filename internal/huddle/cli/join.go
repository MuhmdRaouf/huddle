// huddle join, in its two shapes. `huddle join <host:port> --token <id.secret>` trades an invite
// for this session's own credential and joins the invite's channel. `huddle join <channel>` puts
// this session in a channel that exists on this machine's hub and saves the channel in the
// project's huddle.json, so the project's next sessions join it on start. A bare `huddle join`
// is the join operation of the channel the session is in.
package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/server"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// joinCall is how long a channel join may take before the hub is called unreachable.
const joinCall = 10 * time.Second

// joinShape is which of the join word's forms a command line holds.
type joinShape int

const (
	joinOperation joinShape = iota // no address and no channel: the join operation
	joinInvite                     // <host:port> --token: today's invite flow
	joinChannel                    // <channel>: a channel on this machine's hub
)

// joinShapeOf reads the command line: a token or a host:port is an invite, one plain word that
// is a channel name is a channel, anything else is the join operation.
func (c *cli) joinShapeOf() joinShape {
	switch {
	case c.raw.has("token") || (len(c.pos) > 0 && hostPort.MatchString(c.pos[0])):
		return joinInvite
	case len(c.pos) == 1 && channelName.MatchString(c.pos[0]):
		return joinChannel
	}
	return joinOperation
}

// joinChannelWord joins this session to the channel the command line names, which must exist: a
// join never creates one.
func (c *cli) joinChannelWord(ctx context.Context) int {
	name, _ := c.shift()
	if c.url == "" {
		return c.die(noPort, 5)
	}
	exists, code := c.channelExists(ctx, name)
	if code != 0 {
		return code
	}
	if !exists {
		return c.die(fmt.Sprintf("channel %s doesn't exist; create it with /huddle:channel create %s", name, name), 2)
	}
	return c.enterChannel(ctx, name)
}

// channelExists asks the hub whether it holds a channel of this name. The code is nonzero when
// the hub could not answer.
func (c *cli) channelExists(ctx context.Context, name string) (bool, int) {
	resp, err := HFetch(ctx, c.w, http.MethodGet, c.url+"/api/channels", nil, nil, joinCall, "")
	if err != nil {
		return false, c.die(fmt.Sprintf("Huddle unreachable at %s (start it: huddle up)", c.url), 5)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	value, decodeErr := jsfmt.DecodeValue(raw)
	if decodeErr != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, c.die(errorMessage(value, resp.StatusCode), 2)
	}
	for _, item := range arrayItems(value) {
		if textField(item, "name") == name {
			return true, 0
		}
	}
	return false, 0
}

// enterChannel joins this session to a channel that exists and, once the hub has taken it,
// saves the channel in the project's huddle.json.
func (c *cli) enterChannel(ctx context.Context, channel string) int {
	root := projectRoot(ctx, c.w)
	file := filepath.Join(root, FileRel)
	saved := readSettings(file)
	as := firstNonEmpty(c.as, textField(saved.value(), "as"), Slug(root, "huddle"))
	id := c.id
	id.URL, id.Channel, id.As = c.url, channel, as
	session := c.sessionID()
	callURL, payload, err := SessionJoinCall(id, c.url, ContextFor("", id.Context), session)
	if err != nil {
		return c.die(err.Error(), 2)
	}
	resp, err := HFetch(ctx, c.w, http.MethodPost, callURL, map[string]string{"content-type": "application/json"},
		payload, joinCall, session)
	if err != nil {
		return c.die(fmt.Sprintf("Huddle unreachable at %s (start it: huddle up)", c.url), 5)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		answer, _ := jsfmt.DecodeValue(raw)
		return c.die(errorMessage(answer, resp.StatusCode), exitOf(resp.StatusCode))
	}
	saved.set("channel", jsfmt.String(channel))
	saved.set("as", jsfmt.String(as))
	if !saved.has("autostart") {
		saved.set("autostart", jsfmt.Bool(true))
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return c.die(err.Error(), 1)
	}
	if err := os.WriteFile(file, []byte(indentJSON(saved.value())+"\n"), 0o644); err != nil {
		return c.die(err.Error(), 1)
	}
	KeepOutOfGit(ctx, c.w, filepath.Join(root, ".agents", "huddle"))
	c.say(fmt.Sprintf("joined channel %s as %s (saved in %s)", channel, as, file))
	return 0
}

// projectRoot answers the project this command line belongs to: where a huddle.json already is,
// else the main checkout of the repo, else Claude Code's project directory, else the cwd.
func projectRoot(ctx context.Context, w *World) string {
	if found := FindConfig(ctx, w, ""); found != nil && found.Root != "" {
		return found.Root
	}
	if root := MainCheckout(ctx, w, ""); root != "" {
		return root
	}
	return firstNonEmpty(w.getenv("CLAUDE_PROJECT_DIR"), w.dir())
}

// sessionID answers the Claude Code session id this command runs in, or empty.
func (c *cli) sessionID() string {
	for _, key := range []string{"HUDDLE_SESSION", "CLAUDE_CODE_SESSION_ID"} {
		if id := c.w.getenv(key); mcpSessionIDOK.MatchString(id) {
			return id
		}
	}
	return ""
}

// joinWithInvite joins a huddle with an invite: this session gets its own credential (never the
// invite), the invite's channel wins over this project's file, and the session then joins it.
// The answer is 0 to keep going with the join operation, or the exit code.
func (c *cli) joinWithInvite(ctx context.Context) int {
	host, ok := c.shift()
	if !ok {
		return c.die("join <host:port> --token <id.secret>", 2)
	}
	serverURL := host
	if !httpPrefix.MatchString(host) {
		serverURL = "http://" + host
	}
	serverURL = strings.TrimSuffix(serverURL, "/")
	token, isText := c.raw.text("token")
	if !isText {
		return c.die("join <host:port> --token <id.secret>", 2)
	}
	fallback := Slug(firstNonEmpty(c.w.getenv("CLAUDE_PROJECT_DIR"), c.w.dir()), "session")
	// a name given (--as) is kept; this session joining again keeps the one it has; else the
	// project's (or folder's) name is only a wish: the server makes it unique
	given, givenSet := c.raw.str("as")
	if !givenSet {
		if name, has := c.opt.str("name"); has {
			given, givenSet = name, true
		}
	}
	previousName := ""
	if prev := LoadCred(ctx, c.w, serverURL, ""); prev != nil && !prev.Root {
		previousName = prev.As
	}
	wish := given
	if !givenSet {
		switch {
		case previousName != "":
			wish = previousName
		case c.id.As != "":
			wish = strings.SplitN(c.id.As, ".", 2)[0]
		default:
			wish = fallback
		}
	}
	joinBody := newBody().textAlways("token", token).textAlways("name", wish).bool("unique", !givenSet && previousName == "")
	if c.id.Channel != "" {
		joinBody.textAlways("channel", c.id.Channel)
	}
	joined, code := c.postJoin(ctx, serverURL, joinBody)
	if code != 0 {
		return code
	}
	name := textField(joined, "name")
	if name == "" {
		name = wish
	}
	// the invite's channel wins over this project's file: the owner invited this session into it
	channel := ""
	if v, hadChannel := c.raw.str("channel"); hadChannel {
		channel = v
	} else {
		channel = firstNonEmpty(textField(joined, "channel"), c.id.Channel)
	}
	if channel == "" {
		return c.die("joined, but the invite names no channel: run join again with --channel <c>", 2)
	}
	if _, hadChannel := c.raw.value("channel"); !hadChannel {
		if invited := textField(joined, "channel"); invited != "" && c.id.Channel != "" && c.id.Channel != invited {
			fmt.Fprintf(c.stderr, "huddle: joined channel %s (the invite's); this project's file named %s\n", invited, c.id.Channel)
		}
	}
	_ = SaveCred(ctx, c.w, Cred{URL: serverURL, Channel: channel, As: name, Credential: textField(joined, "credential")}, "")
	fmt.Fprintf(c.stderr, "huddle: joined %s as %s; this project's next sessions are in it too (the invite is not kept)\n", serverURL, name)
	c.url, c.ch, c.me = serverURL, channel, name
	c.opt = c.opt.without("token", "name")
	if link := Dashboard(ctx, c.w, serverURL, "", 1500); link != "" {
		fmt.Fprintf(c.stderr, "dashboard: %s   (signs your browser in once, within 5 min)\n", link)
	}
	return 0
}

// postJoin sends the invite join itself: a plain request, no credential of this machine's.
func (c *cli) postJoin(ctx context.Context, serverURL string, joinBody *body) (*jsfmt.Value, int) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL+"/api/join",
		bytes.NewReader([]byte(joinBody.build().EncodeJSON())))
	if err != nil {
		return nil, c.die(fmt.Sprintf("Huddle unreachable at %s", serverURL), 5)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set(server.ActionHeader, "/api/join")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, c.die(fmt.Sprintf("Huddle unreachable at %s", serverURL), 5)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	value, decodeErr := jsfmt.DecodeValue(raw)
	if decodeErr != nil {
		value = jsfmt.Object(nil, nil)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, c.die(errorMessage(value, resp.StatusCode), 2)
	}
	return value, 0
}
