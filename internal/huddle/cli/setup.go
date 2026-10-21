// huddle setup: this project's .agents/huddle/huddle.json, kept out of git. A first setup asks
// nothing: the channel and the session are named after the project, and autostart is on (the
// SessionStart hook runs `huddle up` when nothing answers). --port saves a port of someone's
// choosing; --listen names more channels whose messages the hooks bring in; --start starts the
// server now; --no-notify turns the desktop notifications off. One Huddle per user is the usual
// shape: a first setup in a project with no settings looks for a Huddle this user already runs
// and joins it instead of starting a second one; --new starts a separate one anyway, and
// --restart stops this project's server first.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/outside"
	"github.com/MuhmdRaouf/huddle/internal/huddle/server"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// setup runs the setup command: sub is its only argument (show, or none), o the flags as given
// (--channel and --as are setup's own), and url the identity's address. The returned int is the
// exit code.
func setup(ctx context.Context, w *World, sub string, o *flags, url string, stdout, stderr io.Writer) int {
	say := func(s string) { fmt.Fprintln(stdout, s) }
	found := FindConfig(ctx, w, "")
	if sub == "show" {
		if found != nil {
			say("settings: " + found.File + " " + found.Cfg.EncodeJSON())
		} else {
			say("settings: none yet (run huddle setup)")
		}
		say("files:    " + Home(ctx, w))
		if choice, has := SavedPort(Home(ctx, w)); has {
			line := "port:     " + strconv.Itoa(choice.Port)
			if choice.Auto {
				line += " (picked by huddle up)"
			}
			say(line)
		} else {
			say("port:     none yet (huddle up picks 18860, then 18861 to 18869)")
		}
		onOff := "off"
		if outside.NewNotifier(DataDir(ctx, w)).Enabled() {
			onOff = "on"
		}
		say("notify:   desktop notifications " + onOff + " (huddle setup --no-notify | --notify)")
		say(Info(ctx, w, url, "").Msg)
		return 0
	}
	if sub != "" {
		return setupFailure(stderr, "unknown setup "+sub+": use show, or flags only")
	}
	// a setup writes the home (its config, soon its channels): the tree comes first
	if hh, ok := TheHome(ctx, w); ok {
		if err := hh.Ensure(ctx); err != nil {
			return setupFailure(stderr, err.Error())
		}
		refreshInstalledService(ctx, w, hh.Root)
	}
	if o.has("no_notify") || o.bare("notify") {
		say(notifySwitch(ctx, w, !o.has("no_notify"), url))
	}
	if found == nil && !o.truthy("new") && !o.has("port") {
		if code, joined := joinRunning(ctx, w, o, say); joined {
			return code
		}
	}
	if o.truthy("channel") != o.truthy("as") {
		return setupFailure(stderr, "--channel and --as go together")
	}
	port, portGiven := 0, false
	if _, has := o.value("port"); has {
		portGiven = true
		switch value, isText := o.text("port"); {
		case isText && wholeNumber.MatchString(strings.TrimSpace(value)):
			port, _ = strconv.Atoi(strings.TrimSpace(value))
		case isText:
			port = -1 // anything unparsable is refused below
		default:
			port = 1 // --port with no value reads as true, whose Number() is 1
		}
		if port <= 0 || port >= 65536 {
			shown, _ := o.str("port")
			return setupFailure(stderr, "--port "+shown+": a port number")
		}
	}
	// the project: where settings already are, else the main checkout of this repo, else the cwd
	root := ""
	if found != nil {
		root = found.Root
	}
	if root == "" {
		root = MainCheckout(ctx, w, "")
	}
	if root == "" {
		root = firstNonEmpty(w.getenv("CLAUDE_PROJECT_DIR"), w.dir())
	}
	file := filepath.Join(root, FileRel)
	legacy := filepath.Join(root, LegacyRel)
	next := mergeSettings(readSettings(legacy), readSettings(file))
	if o.truthy("channel") {
		channel, _ := o.str("channel")
		as, _ := o.str("as")
		next.set("channel", jsfmt.String(channel))
		next.set("as", jsfmt.String(as))
	} else if !next.hasText("channel") || !next.hasText("as") {
		// nothing to ask a first setup: the channel and this session are named after the project
		name := jsfmt.String(Slug(root, "huddle"))
		if !next.hasText("channel") {
			next.set("channel", name)
		}
		if !next.hasText("as") {
			next.set("as", name)
		}
	}
	if o.truthy("role") {
		role, _ := o.str("role")
		next.set("role", jsfmt.String(role))
	}
	if o.bare("autostart") || (!next.has("autostart") && !o.has("no_autostart")) {
		next.set("autostart", jsfmt.Bool(true))
	}
	if o.truthy("no_autostart") {
		next.set("autostart", jsfmt.Bool(false))
	}
	if listen, ok := o.text("listen"); ok {
		next.set("listen", jsfmt.Array(listValues(listen)))
	}
	if o.has("no_listen") {
		next.remove("listen")
	}
	_ = os.MkdirAll(filepath.Dir(file), 0o755)
	_ = os.WriteFile(file, []byte(indentJSON(next.value())+"\n"), 0o644)
	say(file + ": " + next.value().EncodeJSON())
	if _, err := os.Stat(legacy); err == nil {
		_ = os.Remove(legacy)
		say("folded " + legacy + " into it")
	}
	if excluded := KeepOutOfGit(ctx, w, filepath.Join(root, ".agents", "huddle")); excluded != "" {
		say("kept .agents/huddle/ out of git (" + excluded + ")")
	}
	say("channels: " + DataDir(ctx, w) + string(filepath.Separator) + "channels")
	home := Home(ctx, w)
	if portGiven {
		was := url
		say(fmt.Sprintf("port:     %d (saved in %s)", port, SavePort(ctx, w, port, false, home)))
		me := Identify(ctx, w, "") // HUDDLE_URL, huddle.json's url and HUDDLE_PORT still win over it
		switch me.Source {
		case "env", "config", "port":
			url = me.URL
		default:
			url = Local(port)
		}
		if url != Local(port) {
			source := "huddle.json's url"
			if me.Source == "port" {
				source = "HUDDLE_PORT"
			} else if me.Source == "env" {
				source = "HUDDLE_URL"
			}
			say("note:     " + source + " wins: the server stays at " + url)
		}
		if o.truthy("start") && PidOf(ctx, w) > 0 && was != "" && was != url {
			say(Down(ctx, w, was).Msg)
		}
	}
	if o.truthy("restart") && PidOf(ctx, w) > 0 {
		say(Down(ctx, w, url).Msg)
	}
	if o.truthy("restart") || o.truthy("start") {
		r := Up(ctx, w, url, 35_000, "")
		say(r.Msg)
		if !r.OK {
			return 5
		}
		url = r.URL
		enter(ctx, w, url, textField(next.value(), "channel"), textField(next.value(), "as"))
		// the line another Claude session pastes in, and the dashboard link, printed for the
		// user (the server is localhost-only; nothing here is kept from Claude), started or not
		channel := textField(next.value(), "channel")
		if line := JoinLine(ctx, w, url, InviteOptions{Channel: channel, Description: "made by huddle setup"}, ""); line.OK {
			valid := "forever"
			if line.Expires != "" {
				valid = "until " + line.Expires
			}
			say("join:     " + line.Line + "   (valid " + valid + ")")
		}
		if link := Dashboard(ctx, w, url, "", 1500); link != "" {
			say("dashboard: " + link)
		}
	} else if !Info(ctx, w, url, "").OK {
		say("start it with: huddle up")
	}
	return 0
}

// setupFailure says why setup refused; the returned int is its exit code.
func setupFailure(stderr io.Writer, msg string) int {
	fmt.Fprintf(stderr, "huddle setup: %s\n", msg)
	return 2
}

// settings is one settings file's JSON, rewritable in key order.
type settings struct {
	v *jsfmt.Value
}

// readSettings loads one settings file; missing or unreadable is empty.
func readSettings(path string) *settings {
	raw, err := os.ReadFile(path)
	if err != nil {
		return &settings{v: jsfmt.Object(nil, nil)}
	}
	value, err := jsfmt.DecodeValue(raw)
	if err != nil || value.Kind() != jsfmt.KindObject {
		return &settings{v: jsfmt.Object(nil, nil)}
	}
	return &settings{v: value}
}

// mergeSettings layers one file's settings over another's, keeping the base's key order and
// replacing values in place (the legacy file's keys come first, the current file wins).
func mergeSettings(base, over *settings) *settings {
	merged := &settings{v: base.v}
	for i, key := range over.v.Keys() {
		merged.set(key, over.v.Fields()[i])
	}
	return merged
}

// value answers the settings as a JSON value.
func (s *settings) value() *jsfmt.Value { return s.v }

// has answers whether one key is present.
func (s *settings) has(key string) bool {
	_, ok := fieldOf(s.v, key)
	return ok
}

// hasText answers whether one key holds a non-empty string.
func (s *settings) hasText(key string) bool {
	return textField(s.v, key) != ""
}

// set replaces one key's value in place, or appends it.
func (s *settings) set(key string, value *jsfmt.Value) {
	if at := indexOf(s.v.Keys(), key); at >= 0 {
		s.v.Fields()[at] = value
		return
	}
	s.v = jsfmt.Object(append(append([]string{}, s.v.Keys()...), key),
		append(append([]*jsfmt.Value{}, s.v.Fields()...), value))
}

// remove drops one key.
func (s *settings) remove(key string) {
	at := indexOf(s.v.Keys(), key)
	if at < 0 {
		return
	}
	keys := append([]string{}, s.v.Keys()...)
	fields := append([]*jsfmt.Value{}, s.v.Fields()...)
	s.v = jsfmt.Object(append(keys[:at], keys[at+1:]...), append(fields[:at], fields[at+1:]...))
}

// enter joins this session's channel now, so the channel exists before the next session start
// (the hooks join then): `huddle status` answers right after setup, also from a plain terminal.
func enter(ctx context.Context, w *World, url, channel, as string) {
	me := Identify(ctx, w, "")
	callURL := fmt.Sprintf("%s/api/c/%s/op/join?as=%s", url,
		neturl.PathEscape(firstNonEmpty(me.Channel, channel)), queryEscape(firstNonEmpty(me.As, as)))
	args := newBody()
	if me.Role != "" {
		args.textAlways("role", me.Role)
	}
	args.textAlways("task", "set up")
	withTimeout, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := HFetch(withTimeout, w, http.MethodPost, callURL,
		map[string]string{"content-type": "application/json"}, []byte(args.build().EncodeJSON()), 0, "")
	if err != nil {
		return
	}
	resp.Body.Close()
}

// serverNote is one entry of the machine's servers registry, in file order.
type serverNote struct {
	home  string
	entry ServerEntry
}

// serversInOrder answers the servers this user runs (`huddle up` notes each), in file order:
// setup joins the first live one before it starts a second.
func serversInOrder(ctx context.Context, w *World) []serverNote {
	raw, err := os.ReadFile(RegistryFile(w))
	if err != nil {
		return nil
	}
	value, err := jsfmt.DecodeValue(raw)
	if err != nil || value.Kind() != jsfmt.KindObject {
		return nil
	}
	notes := []serverNote{}
	for i, home := range value.Keys() {
		var entry ServerEntry
		if err := json.Unmarshal([]byte(value.Fields()[i].EncodeJSON()), &entry); err != nil {
			continue
		}
		notes = append(notes, serverNote{home: home, entry: entry})
	}
	return notes
}

// joinRunning joins another Huddle of this user, alive. say prints for the user; the bool says
// whether the answer is final (false: none found, go on and start this project's own).
func joinRunning(ctx context.Context, w *World, o *flags, say func(string)) (int, bool) {
	mine := projectOf(ctx, w)
	notes := serversInOrder(ctx, w)
	var live *serverNote
	for i := range notes {
		// one home serves every project of the user, so the entry to skip is this project's
		// own start, not every entry of this home
		if notes[i].entry.Project == mine || notes[i].entry.URL == "" || !Healthy(ctx, notes[i].entry.URL, 800) {
			continue
		}
		live = &notes[i]
		break
	}
	if live == nil {
		return 0, false
	}
	entry := live.entry
	where := firstNonEmpty(entry.Project, live.home)
	// a credential with no channel is the server's root key, which `huddle up` saves its
	// creator, not a membership: the project is not in yet
	if held := LoadCred(ctx, w, entry.URL, ""); held != nil && held.Channel != "" {
		say(fmt.Sprintf("This project is already in the Huddle at %s (channel %s, as %s). To start a separate one: /huddle:setup --new",
			entry.URL, firstNonEmpty(held.Channel, entry.Channel), firstNonEmpty(held.As, "?")))
		return 0, true
	}
	var project *Cred
	if entry.Project != "" {
		project = ProjectCred(ctx, w, entry.Project, entry.URL)
	}
	if project == nil {
		say(fmt.Sprintf("A Huddle already runs for %s at %s (channel %s). To join it, run /huddle:invite in a session there and paste its join line here (/huddle:join …). To start a separate one anyway: /huddle:setup --new",
			where, entry.URL, firstNonEmpty(entry.Channel, "?")))
		return 0, true
	}
	// the channel this project lands in: the flag's, the entry's own, else the project's own
	// channel on that hub, named the way a first setup names one (an entry a plain `huddle up`
	// wrote names none)
	root := MainCheckout(ctx, w, "")
	if root == "" {
		root = firstNonEmpty(w.getenv("CLAUDE_PROJECT_DIR"), w.dir())
	}
	channel := firstNonEmpty(strFlagOf(o, "channel"), entry.Channel, Slug(root, "huddle"))
	// a single-use invite from that project's credential, redeemed at once: an invite and a
	// join in one step. The project is its channel's first session, so it may invite the next
	// one; the hub grants that only when the minting credential is its root key.
	minted, ok := postJSON(ctx, entry.URL+"/api/tokens", map[string]string{auth.Header: project.Credential},
		newBody().number("ttl", 120).bool("single_use", true).bool("can_invite", true).text("channel", channel).text("description", "huddle setup in another project"), 5*time.Second)
	if !ok || textField(minted, "token") == "" {
		say(fmt.Sprintf("A Huddle already runs for %s at %s, but this machine holds no credential there that may invite. Run /huddle:invite in a session there and paste its join line here, or /huddle:setup --new for a separate one.", where, entry.URL))
		return 0, true
	}
	wish := Slug(root, "session")
	if o.truthy("as") {
		wish, _ = o.str("as")
	}
	joinBody := newBody().textAlways("token", textField(minted, "token")).textAlways("name", wish)
	joinBody.bool("unique", !o.truthy("as"))
	joinBody.text("channel", channel)
	joined, ok := postJSON(ctx, entry.URL+"/api/join", nil, joinBody, 0)
	if !ok || textField(joined, "credential") == "" {
		why := "unreachable"
		if ok {
			why = textField(joined, "error")
		}
		say(fmt.Sprintf("A Huddle already runs for %s at %s, but joining it failed (%s). /huddle:setup --new starts a separate one.", where, entry.URL, why))
		return 5, true
	}
	// the answer's channel is the token's: a hub that grants none grants no membership, so
	// nothing is saved and nothing is claimed
	got := textField(joined, "channel")
	if got == "" {
		say(fmt.Sprintf("A Huddle already runs for %s at %s, but joining it gave no channel. /huddle:setup --new starts a separate one.", where, entry.URL))
		return 5, true
	}
	name := textField(joined, "name")
	_ = SaveCred(ctx, w, Cred{URL: entry.URL, Channel: got, As: name, Credential: textField(joined, "credential")}, "")
	joinArgs := newBody()
	joinArgs.flagText(o, "role")
	joinArgs.textAlways("task", "set up")
	_, _ = postJSON(ctx, fmt.Sprintf("%s/api/c/%s/op/join?as=%s", entry.URL, neturl.PathEscape(got), queryEscape(name)),
		map[string]string{auth.Header: textField(joined, "credential")}, joinArgs, 5*time.Second)
	say(fmt.Sprintf("Joined the Huddle that already runs for %s: %s, channel %s, as %s. This project's sessions are in it from now on (no second Huddle started; /huddle:setup --new starts a separate one).",
		where, entry.URL, got, name))
	if link := Dashboard(ctx, w, entry.URL, "", 1500); link != "" {
		say("dashboard: " + link)
	}
	return 0, true
}

// strFlagOf answers one flag's value when it took one.
func strFlagOf(o *flags, key string) string {
	v, _ := o.text(key)
	return v
}

// postJSON posts one JSON body with optional headers and a timeout, and answers the decoded
// reply. ok is false when the call failed or the status is not a success; the reply may still
// carry a refusal the caller reads.
func postJSON(ctx context.Context, url string, headers map[string]string, args *body, timeout time.Duration) (*jsfmt.Value, bool) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(args.build().EncodeJSON()))
	if err != nil {
		return nil, false
	}
	req.Header.Set("content-type", "application/json")
	if parsed, err := neturl.Parse(url); err == nil {
		req.Header.Set(server.ActionHeader, parsed.Path)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, false
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	value, decodeErr := jsfmt.DecodeValue(raw)
	if decodeErr != nil {
		value = jsfmt.Object(nil, nil)
	}
	return value, resp.StatusCode >= 200 && resp.StatusCode < 300
}

// notifySwitch turns the desktop notifications on or off: in this home's data dir (read by the
// server it runs), and on the running server this session is in, if any (another project's
// Huddle keeps its own switch).
func notifySwitch(ctx context.Context, w *World, on bool, url string) string {
	outside.NewNotifier(DataDir(ctx, w)).Set(on)
	there := ""
	if url != "" {
		args := newBody().bool("notify", on)
		if resp, err := HFetch(ctx, w, http.MethodPost, url+"/api/settings",
			map[string]string{"content-type": "application/json"}, []byte(args.build().EncodeJSON()), 3*time.Second, ""); err == nil {
			ok := resp.StatusCode >= 200 && resp.StatusCode < 300
			resp.Body.Close()
			if ok {
				there = " (also on the running Huddle at " + url + ")"
			}
		}
	}
	word := "off"
	if on {
		word = "on"
	}
	return "notify:   desktop notifications " + word + there
}
