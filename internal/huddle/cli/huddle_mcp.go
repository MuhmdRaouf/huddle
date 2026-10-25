// Huddle's MCP server over stdio, for a harness that spawns it: one JSON-RPC message per line
// in, one per line out. What needs no service (initialize, tools/list, the two tools a session
// outside a huddle has) is answered here, so the bridge connects in every state; everything
// else is forwarded to this session's Huddle (POST /mcp/<channel>?as=<you>) and the answer is
// written out as it came. While connected it follows the channel's live stream and writes each
// event meant for this session (addressed to it, an ask, or a pushed topic) as a
// notifications/claude/channel push, so an idle session wakes up without polling; the same
// stream drives the standard MCP resource surface: the channel's events and topics read as
// resources, a subscription told with notifications/resources/updated, and the poll tool for
// clients without subscriptions. stdout is the protocol; diagnostics go to stderr.
package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/safego"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/server"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/routerd"
)

// worldEnv is the world's variables as the lookup the router's words read.
func worldEnv(w *World) domain.EnvLookup {
	entries := w.environ()
	env := make(domain.EnvLookup, len(entries))
	for _, entry := range entries {
		key, value, _ := strings.Cut(entry, "=")
		env[key] = value
	}
	return env
}

// notInYet is what a session that is not in a huddle yet is told, with the two ways in.
const notInYet = "not in a huddle yet: run /huddle:setup in this project (the first session), or paste the /huddle:join line that /huddle:invite shows in a session that is in one"

// noPortYet is what a session in a channel whose Huddle has no url is told.
const noPortYet = "huddle: this project's Huddle has no port yet: start it with huddle up (it picks one and saves it). Treat yourself as paused and retry."

// mcpJoinURL accepts the host:port (or full url) a join line carries: scheme, host, port.
var mcpJoinURL = regexp.MustCompile(`^https?://[\w.-]+:\d+$`)

// mcpProtocolVersion is the MCP revision the bridge speaks when the client names none.
const mcpProtocolVersion = "2025-06-18"

// Poll and resource-read bounds: one read answers at most this many events, and a poll waits at
// most mcpPollMaxMS, and mcpPollDefaultMS when the caller names no timeout.
const (
	mcpReadLimit     = 100
	mcpPollMaxMS     = 60000
	mcpPollDefaultMS = 25000
	// mcpPollRecheck paces a poll whose wait woke without anything after the cursor.
	mcpPollRecheck = 250 * time.Millisecond
)

// McpStdio serves one Claude Code session over stdin and stdout until stdin ends, and answers
// the process's exit code (0: a session served).
func McpStdio(ctx context.Context, w *World, version string, stdin io.Reader, stdout, stderr io.Writer) int {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	id := Identify(runCtx, w, "")
	b := &mcpBridge{
		w: w, version: version, stdout: stdout, stderr: stderr,
		url: id.URL, channelName: id.Channel, me: id.As,
		pushed:   map[int64]bool{},
		subs:     map[string]bool{},
		knownTop: map[string]bool{},
		wait:     id.Wait,
		callMS:   int(jsNumber(firstNonEmpty(w.getenv("HUDDLE_CALL_TIMEOUT_MS"), "10000"))),
		pushOff:  len(id.Push) > 0 && id.Push[0] == "off",
	}
	for _, glob := range id.Push {
		b.pushGlobs = append(b.pushGlobs, regexp.MustCompile("^(?:"+mcpGlobExpr(glob)+")$"))
	}
	safego.Go(func(line string) { b.logf("%s", line) }, func() { b.watchList(runCtx) })
	// The session's watchdog: while this bridge serves, a dead or hung huddle-router comes back
	// on its own (HR-WATCHDOG-MCP). Its own goroutine, so no MCP reply ever waits on it;
	// HUDDLE_ROUTER_WATCH=0 turns it off for a bridge that does not want it.
	if w.getenv("HUDDLE_ROUTER_WATCH") != "0" {
		safego.Go(func(line string) { b.logf("%s", line) }, func() {
			routerd.Watch(runCtx, routerd.WatchOptions{Env: worldEnv(w)})
		})
	}
	data := []byte{}
	chunk := make([]byte, 4096)
	for {
		n, err := stdin.Read(chunk)
		data = append(data, chunk[:n]...)
		for {
			at := bytes.IndexByte(data, '\n')
			if at < 0 {
				break
			}
			line := jsfmt.Trim(string(data[:at]))
			data = data[at+1:]
			if line == "" {
				continue
			}
			msg, perr := jsfmt.DecodeValue([]byte(line))
			if perr != nil {
				b.write(mcpParseError())
				continue
			}
			if mcpMethod(msg) == "notifications/initialized" {
				b.mu.Lock()
				b.initialized = true
				b.mu.Unlock()
				b.startPush(runCtx)
			}
			b.forward(runCtx, msg)
		}
		if err != nil {
			return 0
		}
	}
}

// mcpBridge is one stdio session: its identity, its push stream and its stdout, all under one
// mutex that also keeps each written line whole.
type mcpBridge struct {
	w       *World
	version string
	stdout  io.Writer
	stderr  io.Writer

	mu          sync.Mutex
	url         string
	channelName string
	me          string
	listed      int8 // what the last tools/list showed: 0 none yet, 1 the short list, 2 every tool
	initialized bool
	pushing     bool
	lastSeq     int64
	pushed      map[int64]bool
	subs        map[string]bool // resource uris this client subscribed to
	knownTop    map[string]bool // topics a resources/list has shown
	resListed   bool            // the client listed resources: a new topic is worth list_changed
	streamUp    bool            // the live stream answered its hello: events now arrive as they happen
	subBase     int64           // the channel's last seq when the first subscription landed before the stream
	subBaseSet  bool

	pushGlobs []*regexp.Regexp
	pushOff   bool
	wait      int
	callMS    int
}

// what the last tools/list showed
const (
	mcpListNone int8 = 0
	mcpListOut  int8 = 1
	mcpListIn   int8 = 2
)

// mcpState is one reading of the bridge's identity fields.
type mcpState struct {
	url         string
	channelName string
	me          string
}

// state answers the current identity fields.
func (b *mcpBridge) state() mcpState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return mcpState{url: b.url, channelName: b.channelName, me: b.me}
}

// refresh re-reads who this session is: a join or a session start elsewhere changes it.
func (b *mcpBridge) refresh(ctx context.Context) {
	id := Identify(ctx, b.w, "")
	b.mu.Lock()
	b.url, b.channelName, b.me = id.URL, id.Channel, id.As
	b.mu.Unlock()
}

// isIn says whether this session is in a huddle: a channel, a name, an address and a credential.
func (b *mcpBridge) isIn(ctx context.Context) bool {
	s := b.state()
	return s.url != "" && s.channelName != "" && s.me != "" && TokenFor(ctx, b.w, s.url, "") != ""
}

// write puts one message on stdout as one line.
func (b *mcpBridge) write(msg *jsfmt.Value) {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, _ = io.WriteString(b.stdout, msg.EncodeJSON()+"\n")
}

// logf puts one diagnostic on stderr, never stdout.
func (b *mcpBridge) logf(format string, args ...any) {
	fmt.Fprintf(b.stderr, "huddle-mcp: "+format+"\n", args...)
}

// mcpID answers the message's id and whether it has one at all (a notification has none; an
// explicit null still gets its reply with id null, the way the bridge writes it).
func mcpID(msg *jsfmt.Value) (*jsfmt.Value, bool) {
	id, ok := fieldOf(msg, "id")
	if !ok {
		return nil, false
	}
	return id, true
}

// mcpReply shapes one JSON-RPC reply.
func mcpReply(id, result *jsfmt.Value) *jsfmt.Value {
	return jsfmt.Object([]string{"jsonrpc", "id", "result"},
		[]*jsfmt.Value{jsfmt.String("2.0"), id, result})
}

// mcpNotification shapes one JSON-RPC notification that carries nothing but its method.
func mcpNotification(method string) *jsfmt.Value {
	return jsfmt.Object([]string{"jsonrpc", "method"},
		[]*jsfmt.Value{jsfmt.String("2.0"), jsfmt.String(method)})
}

// mcpNull is the JSON null.
func mcpNull() *jsfmt.Value {
	v, _ := jsfmt.DecodeValue([]byte("null"))
	return v
}

// mcpParseError is the reply to a line that is not JSON.
func mcpParseError() *jsfmt.Value {
	err := jsfmt.Object([]string{"code", "message"},
		[]*jsfmt.Value{jsfmt.Number(-32700), jsfmt.String("parse error")})
	return jsfmt.Object([]string{"jsonrpc", "id", "error"},
		[]*jsfmt.Value{jsfmt.String("2.0"), mcpNull(), err})
}

// textResult shapes one tool result: a text, and whether it is an error.
func textResult(id *jsfmt.Value, text string, isErr bool) *jsfmt.Value {
	content := jsfmt.Object([]string{"type", "text"}, []*jsfmt.Value{jsfmt.String("text"), jsfmt.String(text)})
	keys := []string{"content"}
	fields := []*jsfmt.Value{jsfmt.Array([]*jsfmt.Value{content})}
	if isErr {
		keys = append(keys, "isError")
		fields = append(fields, jsfmt.Bool(true))
	}
	return mcpReply(id, jsfmt.Object(keys, fields))
}

// replyText answers a message with a tool result, when it has an id to echo.
func (b *mcpBridge) replyText(msg *jsfmt.Value, text string, isErr bool) {
	id, ok := mcpID(msg)
	if !ok {
		return
	}
	b.write(textResult(id, text, isErr))
}

// forward serves one message: what is local or needs no service here, the rest at the service.
func (b *mcpBridge) forward(ctx context.Context, msg *jsfmt.Value) {
	b.refresh(ctx)
	if s := b.state(); b.isInitialized() && s.url != "" && s.channelName != "" && s.me != "" {
		b.startPush(ctx)
	}
	if b.local(ctx, msg) {
		return
	}
	if b.outside(ctx, msg) {
		return
	}
	s := b.state()
	if s.channelName == "" || s.me == "" {
		b.replyText(msg, "huddle: "+notInYet, true)
		return
	}
	if s.url == "" {
		b.replyText(msg, noPortYet, true)
		return
	}
	sent := msg
	method := mcpMethod(msg)
	name := mcpTextOf(mcpFieldOf(msg, "params"), "name")
	if method == "tools/call" && name == "poll" {
		params, _ := fieldOf(msg, "params")
		args, _ := fieldOf(params, "arguments")
		if id, hasID := mcpID(msg); hasID {
			safego.Go(func(line string) { b.logf("%s", line) }, func() { b.pollTool(ctx, id, mcpObjectOf(args)) })
		}
		return
	}
	ms := b.callMS
	// The table's blocking ops wait for the channel to answer, so they get the long stdio
	// timeout unless the caller chose one.
	if method == "tools/call" && channel.Blocks(name) {
		params, _ := fieldOf(msg, "params")
		args, _ := fieldOf(params, "arguments")
		timeout, hasTimeout := fieldOf(args, "timeout")
		if !hasTimeout {
			value := float64(b.wait)
			if name == "gate" {
				value = 0
			}
			args = mcpWithField(mcpObjectOf(args), "timeout", jsfmt.Number(value))
			params = mcpWithField(mcpObjectOf(params), "arguments", args)
			sent = mcpWithField(msg, "params", params)
			timeout = jsfmt.Number(value)
		}
		secs := mcpNumberOf(timeout)
		if secs == 0 || math.IsNaN(secs) {
			secs = 3600
		}
		if secs > 3600 {
			secs = 3600
		}
		ms = int((secs + 30) * 1000)
	}
	id, hasID := mcpID(msg)
	safego.Go(func(line string) { b.logf("%s", line) }, func() { b.exchange(ctx, s, sent, ms, id, hasID) })
}

// isInitialized says whether the client finished initializing.
func (b *mcpBridge) isInitialized() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.initialized
}

// exchange runs one forwarded call at the service and writes its answer.
func (b *mcpBridge) exchange(ctx context.Context, s mcpState, sent *jsfmt.Value, ms int, id *jsfmt.Value, hasID bool) {
	reply := func(text string) {
		if hasID {
			b.write(textResult(id, text, true))
		}
	}
	resp, err := HFetch(ctx, b.w, http.MethodPost,
		s.url+"/mcp/"+s.channelName+"?as="+queryEscape(s.me),
		map[string]string{"content-type": "application/json", "accept": "application/json"},
		[]byte(sent.EncodeJSON()), time.Duration(ms)*time.Millisecond, "")
	if err != nil {
		b.transportFailure(s.url, ms, err, reply)
		return
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusAccepted:
		return
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		raw, _ := io.ReadAll(resp.Body)
		reason := fmt.Sprintf("HTTP %d", resp.StatusCode)
		if answer, perr := jsfmt.DecodeValue(raw); perr == nil {
			if text := mcpTextOf(answer, "error"); text != "" {
				reason = text
			}
		}
		reply("huddle: " + reason)
	default:
		raw, _ := io.ReadAll(resp.Body)
		answer, perr := jsfmt.DecodeValue(raw)
		if perr != nil {
			b.transportFailure(s.url, ms, nil, reply)
			return
		}
		b.write(answer)
	}
}

// transportFailure writes what a stalled or absent service means for the caller.
func (b *mcpBridge) transportFailure(rawURL string, ms int, err error, reply func(string)) {
	late := err != nil && mcpIsTimeout(err)
	if late {
		reply("huddle: no answer from " + rawURL + " within " + jsfmt.JSNumber(float64(ms)/1000) +
			" s. Treat yourself as paused and retry.")
		return
	}
	reply("huddle: service unreachable at " + rawURL + " (start it: huddle up). Treat yourself as paused and retry.")
}

// local answers what needs no service, so the bridge connects in every state; handled says
// whether it did.
func (b *mcpBridge) local(ctx context.Context, msg *jsfmt.Value) bool {
	id, hasID := mcpID(msg)
	ok := func(result *jsfmt.Value) bool {
		if hasID {
			b.write(mcpReply(id, result))
		}
		return true
	}
	params, _ := fieldOf(msg, "params")
	s := b.state()
	switch mcpMethod(msg) {
	case "initialize":
		protocol := jsfmt.String(mcpProtocolVersion)
		if given, present := fieldOf(params, "protocolVersion"); present && given.Kind() != jsfmt.KindNull {
			protocol = given
		}
		capabilities := jsfmt.Object(
			[]string{"tools", "resources", "experimental"},
			[]*jsfmt.Value{
				jsfmt.Object([]string{"listChanged"}, []*jsfmt.Value{jsfmt.Bool(true)}),
				jsfmt.Object([]string{"subscribe", "listChanged"}, []*jsfmt.Value{jsfmt.Bool(true), jsfmt.Bool(true)}),
				jsfmt.Object([]string{"claude/channel"}, []*jsfmt.Value{jsfmt.Object(nil, nil)})})
		serverInfo := jsfmt.Object([]string{"name", "version"},
			[]*jsfmt.Value{jsfmt.String("huddle"), jsfmt.String(b.version)})
		instructions := jsfmt.String("Huddle: this session is " + notInYet +
			". Its tools (mcp__plugin_huddle_huddle__*) are listed once it is in.")
		if s.channelName != "" && s.me != "" {
			instructions = jsfmt.String(server.Instructions(s.channelName, s.me))
		}
		return ok(jsfmt.Object(
			[]string{"protocolVersion", "capabilities", "serverInfo", "instructions"},
			[]*jsfmt.Value{protocol, capabilities, serverInfo, instructions}))
	case "ping":
		return ok(jsfmt.Object(nil, nil))
	case "tools/list":
		in := b.isIn(ctx)
		b.mu.Lock()
		if in {
			b.listed = mcpListIn
		} else {
			b.listed = mcpListOut
		}
		b.mu.Unlock()
		tools := mcpOutTools()
		if in {
			tools = mcpAllTools()
		}
		return ok(jsfmt.Object([]string{"tools"}, []*jsfmt.Value{jsfmt.Array(tools)}))
	case "resources/list":
		if !hasID {
			return true
		}
		if !b.isIn(ctx) {
			return ok(jsfmt.Object([]string{"resources"}, []*jsfmt.Value{jsfmt.Array(nil)}))
		}
		safego.Go(func(line string) { b.logf("%s", line) }, func() { b.resourceList(ctx, id) })
		return true
	case "resources/read":
		if hasID {
			safego.Go(func(line string) { b.logf("%s", line) }, func() { b.resourceRead(ctx, id, mcpTextOf(params, "uri")) })
		}
		return true
	case "resources/subscribe":
		uri := mcpTextOf(params, "uri")
		b.mu.Lock()
		early := !b.streamUp && !b.subBaseSet
		b.mu.Unlock()
		base := int64(0)
		if early {
			// The stream does not replay what came before it connected, so a subscription taken
			// before the first hello records the channel's seq now and catches the gap up later.
			if items, err := b.opList(ctx, "events", jsfmt.Object(
				[]string{"limit"}, []*jsfmt.Value{jsfmt.Number(1)}), b.callMS); err == nil && len(items) > 0 {
				base = int64(mcpNumberOf(mcpFieldOf(items[len(items)-1], "seq")))
			}
		}
		b.mu.Lock()
		if early && !b.subBaseSet {
			b.subBase, b.subBaseSet = base, true
		}
		b.subs[uri] = true
		b.mu.Unlock()
		return ok(jsfmt.Object(nil, nil))
	case "resources/unsubscribe":
		b.mu.Lock()
		delete(b.subs, mcpTextOf(params, "uri"))
		b.mu.Unlock()
		return ok(jsfmt.Object(nil, nil))
	case "prompts/list":
		return ok(jsfmt.Object([]string{"prompts"}, []*jsfmt.Value{jsfmt.Array(nil)}))
	}
	return false
}

// mcpAllTools lists every channel operation as a tool, plus the bridge's own poll.
func mcpAllTools() []*jsfmt.Value {
	defs := channel.ToolDefs()
	tools := make([]*jsfmt.Value, 0, len(defs)+1)
	for _, d := range defs {
		tools = append(tools, jsfmt.Object(
			[]string{"name", "description", "inputSchema"},
			[]*jsfmt.Value{jsfmt.String(d.Name), jsfmt.String(d.Description), d.Schema}))
	}
	return append(tools, mcpPollDef())
}

// mcpPollDef is the poll tool as the tool list shows it: the long poll for agents whose harness
// cannot subscribe to resources.
func mcpPollDef() *jsfmt.Value {
	return jsfmt.Object([]string{"name", "description", "inputSchema"}, []*jsfmt.Value{
		jsfmt.String("poll"),
		jsfmt.String("Long poll: the channel's events after a cursor, waiting up to timeout_ms for the topics (globs) to produce one. Returns the events as a JSON array (newest last, at most 100), or [] on timeout."),
		jsfmt.Object([]string{"type", "properties"}, []*jsfmt.Value{
			jsfmt.String("object"),
			jsfmt.Object([]string{"after", "topics", "timeout_ms"}, []*jsfmt.Value{
				jsfmt.Object([]string{"type", "description"}, []*jsfmt.Value{
					jsfmt.String("integer"), jsfmt.String("the last seq you saw; events after it come back")}),
				jsfmt.Object([]string{"type", "items", "description"}, []*jsfmt.Value{
					jsfmt.String("array"), jsfmt.Object([]string{"type"}, []*jsfmt.Value{jsfmt.String("string")}),
					jsfmt.String("topic globs, e.g. build.*; omit for any")}),
				jsfmt.Object([]string{"type", "description"}, []*jsfmt.Value{
					jsfmt.String("integer"), jsfmt.String("how long to wait, at most 60000")}),
			}),
		}),
	})
}

// mcpOutTools lists the two tools a session that is not in a huddle has.
func mcpOutTools() []*jsfmt.Value {
	return []*jsfmt.Value{
		jsfmt.Object([]string{"name", "description", "inputSchema"}, []*jsfmt.Value{
			jsfmt.String("status"),
			jsfmt.String("Is this session in a Huddle? (Until it is, only this and join are listed.)"),
			jsfmt.Object([]string{"type", "properties"}, []*jsfmt.Value{
				jsfmt.String("object"), jsfmt.Object(nil, nil)}),
		}),
		jsfmt.Object([]string{"name", "description", "inputSchema"}, []*jsfmt.Value{
			jsfmt.String("join"),
			jsfmt.String("Join a Huddle with an invite: the host:port and token from the /huddle:join line the user pasted."),
			jsfmt.Object([]string{"type", "properties", "required"}, []*jsfmt.Value{
				jsfmt.String("object"),
				jsfmt.Object([]string{"host_port", "token", "as"}, []*jsfmt.Value{
					jsfmt.Object([]string{"type", "description"}, []*jsfmt.Value{
						jsfmt.String("string"), jsfmt.String("e.g. 127.0.0.1:41873")}),
					jsfmt.Object([]string{"type", "description"}, []*jsfmt.Value{
						jsfmt.String("string"), jsfmt.String("<id>.<secret> from the join line")}),
					jsfmt.Object([]string{"type", "description"}, []*jsfmt.Value{
						jsfmt.String("string"), jsfmt.String("your name (default: the project folder's)")}),
				}),
				jsfmt.Array([]*jsfmt.Value{jsfmt.String("host_port"), jsfmt.String("token")}),
			}),
		}),
	}
}

// outside answers the tools a session that is not in a huddle has; handled says whether it did.
func (b *mcpBridge) outside(ctx context.Context, msg *jsfmt.Value) bool {
	if mcpMethod(msg) != "tools/call" || b.isIn(ctx) {
		return false
	}
	params, _ := fieldOf(msg, "params")
	args, _ := fieldOf(params, "arguments")
	name := mcpTextOf(params, "name")
	if name != "join" {
		b.replyText(msg, "huddle: "+notInYet, name != "status")
		return true
	}
	safego.Go(func(line string) { b.logf("%s", line) }, func() { b.join(ctx, msg, mcpObjectOf(args)) })
	return true
}

// join runs the one tool that works outside a huddle: joining with an invite.
func (b *mcpBridge) join(ctx context.Context, msg *jsfmt.Value, args *jsfmt.Value) {
	host := jsfmt.Trim(mcpStringOf(mcpFieldOf(args, "host_port")))
	rawURL := host
	if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
		rawURL = "http://" + host
	}
	rawURL = strings.TrimSuffix(rawURL, "/")
	token, hasToken := fieldOf(args, "token")
	if !mcpJoinURL.MatchString(rawURL) || !hasToken || token.Kind() != jsfmt.KindString {
		b.replyText(msg, "huddle: join needs host_port (host:port) and token", true)
		return
	}
	asValue, _ := fieldOf(args, "as")
	s := b.state()
	wish := ""
	if mcpTruthy(asValue) {
		wish = mcpStringOf(asValue)
	} else if s.me != "" {
		wish = strings.SplitN(s.me, ".", 2)[0]
	} else {
		wish = Slug(firstNonEmpty(b.w.getenv("CLAUDE_PROJECT_DIR"), b.w.dir()), "session")
	}
	body := jsfmt.Object(
		[]string{"token", "name", "unique"},
		[]*jsfmt.Value{token, jsfmt.String(wish), jsfmt.Bool(!mcpTruthy(asValue))})
	if s.channelName != "" {
		body = mcpWithField(body, "channel", jsfmt.String(s.channelName))
	}
	client := &http.Client{Timeout: time.Duration(b.callMS) * time.Millisecond}
	joinReq, err := http.NewRequest(http.MethodPost, rawURL+"/api/join", strings.NewReader(body.EncodeJSON()))
	if err != nil {
		b.replyText(msg, "huddle: Huddle unreachable at "+rawURL, true)
		return
	}
	joinReq.Header.Set("content-type", "application/json")
	joinReq.Header.Set(server.ActionHeader, "/api/join")
	resp, err := client.Do(joinReq)
	if err != nil {
		b.replyText(msg, "huddle: Huddle unreachable at "+rawURL, true)
		return
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	answer, perr := jsfmt.DecodeValue(raw)
	if perr != nil {
		answer = jsfmt.Object(nil, nil)
	}
	credential := mcpTextOf(answer, "credential")
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || credential == "" {
		reason := fmt.Sprintf("HTTP %d", resp.StatusCode)
		if text := mcpTextOf(answer, "error"); text != "" {
			reason = text
		}
		b.replyText(msg, "huddle: "+reason, true)
		return
	}
	channelName := firstNonEmpty(mcpTextOf(answer, "channel"), s.channelName)
	if channelName == "" {
		b.replyText(msg, "huddle: joined, but the invite names no channel: ask for an invite made in a channel", true)
		return
	}
	if err := SaveCred(ctx, b.w, Cred{
		URL: rawURL, Channel: channelName, As: mcpTextOf(answer, "name"), Credential: credential,
	}, ""); err != nil {
		b.replyText(msg, "huddle: Huddle unreachable at "+rawURL, true)
		return
	}
	b.refresh(ctx)
	// in the channel, too, like `huddle join … --token` (the server keeps who is here)
	if after := b.state(); after.url != "" && after.channelName != "" && after.me != "" {
		if resp, err := HFetch(ctx, b.w, http.MethodPost,
			after.url+"/api/c/"+after.channelName+"/op/join?as="+queryEscape(after.me),
			map[string]string{"content-type": "application/json"}, []byte("{}"),
			time.Duration(b.callMS)*time.Millisecond, ""); err == nil {
			_ = resp.Body.Close()
		}
	}
	b.mu.Lock()
	b.listed = mcpListNone
	b.mu.Unlock()
	b.write(mcpNotification("notifications/tools/list_changed"))
	link := Dashboard(ctx, b.w, b.state().url, "", 1500)
	tail := " (huddle open, or /huddle:setup, prints the user a dashboard link that signs their browser in.)"
	if link != "" {
		tail = "\ndashboard: " + link + "   (signs your browser in once, within 5 min)"
	}
	s = b.state()
	b.replyText(msg, "joined "+rawURL+", channel "+s.channelName+", as "+s.me+
		"; this project's next sessions are in it too. Every Huddle tool is listed now: call status for the picture."+tail, false)
}

// watchList announces the full tool list once a session that listed the short one is in: it may
// have joined outside this bridge (a session start, /huddle:join from Bash).
func (b *mcpBridge) watchList(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.mu.Lock()
			watch := b.listed == mcpListOut
			b.mu.Unlock()
			if !watch {
				continue
			}
			b.refresh(ctx)
			if b.isIn(ctx) {
				b.mu.Lock()
				b.listed = mcpListNone
				b.mu.Unlock()
				b.write(mcpNotification("notifications/tools/list_changed"))
			}
		}
	}
}

// startPush starts the channel's live stream once per session.
func (b *mcpBridge) startPush(ctx context.Context) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pushing || b.pushOff || b.url == "" || b.channelName == "" || b.me == "" {
		return
	}
	b.pushing = true
	safego.Go(func(line string) { b.logf("%s", line) }, func() { b.pushLoop(ctx) })
}

// pushLoop follows the channel's live stream, reconnecting with backoff, until the session ends.
func (b *mcpBridge) pushLoop(ctx context.Context) {
	backoff := time.Second
	for {
		err, connected := b.streamOnce(ctx)
		b.mu.Lock()
		b.streamUp = false
		b.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if connected {
			backoff = time.Second
		}
		b.logf("live stream: %v retry in %ds", err, int(backoff/time.Second))
		if !mcpSleep(ctx, backoff) {
			return
		}
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
}

// streamOnce reads the live stream until it ends or breaks; connected says it ever answered.
func (b *mcpBridge) streamOnce(ctx context.Context) (error, bool) {
	s := b.state()
	resp, err := HFetch(ctx, b.w, http.MethodGet,
		s.url+"/api/c/"+s.channelName+"/live?as="+queryEscape(s.me),
		map[string]string{"accept": "text/event-stream"}, nil, 0, "")
	if err != nil {
		return err, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode), false
	}
	frameErr := error(nil)
	connected := true
	data := []byte{}
	chunk := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(chunk)
		data = append(data, chunk[:n]...)
		for frameErr == nil {
			at := bytes.Index(data, []byte("\n\n"))
			if at < 0 {
				break
			}
			frame := string(data[:at])
			data = data[at+2:]
			frameErr = b.frame(ctx, frame)
		}
		if frameErr != nil {
			return frameErr, true
		}
		if rerr != nil {
			return errors.New("stream ended"), connected
		}
	}
}

// frame handles one server-sent event frame; an error breaks the stream. A frame with no data
// is the stream's heartbeat: the bridge acks on it, so its proof stays fresh while the stream
// is up even when nothing flows.
func (b *mcpBridge) frame(ctx context.Context, frame string) error {
	data := ""
	for _, line := range strings.Split(frame, "\n") {
		if strings.HasPrefix(line, "data: ") {
			data = line[len("data: "):]
			break
		}
	}
	if data == "" {
		b.ack(ctx)
		return nil
	}
	msg, err := jsfmt.DecodeValue([]byte(data))
	if err != nil {
		return err
	}
	switch mcpTextOf(msg, "type") {
	case "hello":
		b.catchUp(ctx, int64(mcpNumberOf(mcpFieldOf(mcpFieldOf(msg, "data"), "last"))))
		b.mu.Lock()
		b.streamUp = true
		b.mu.Unlock()
		b.ack(ctx)
		return nil
	case "event":
		event, _ := fieldOf(msg, "data")
		b.resource(event)
		b.deliver(event, true)
		return nil
	}
	return nil
}

// ack tells the registry this bridge's channel pushes flow (SPEC 6.4): while an ack is fresh,
// the hub delivers the owner's words by push instead of waiting for the session's next prompt.
// Best effort: an ack that cannot land costs one line on stderr and the next heartbeat's retry.
func (b *mcpBridge) ack(ctx context.Context) {
	s := b.state()
	if s.url == "" || s.channelName == "" || s.me == "" {
		return
	}
	body := []byte(jsfmt.Object(
		[]string{"channel", "as"},
		[]*jsfmt.Value{jsfmt.String(s.channelName), jsfmt.String(s.me)}).EncodeJSON())
	resp, err := HFetch(ctx, b.w, http.MethodPost, s.url+"/api/registry/push-ack",
		map[string]string{"content-type": "application/json"}, body, 2*time.Second, "")
	if err != nil {
		b.logf("push ack: %v", err)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
}

// catchUp delivers what the stream cannot replay: on the first connect the asks already waiting
// plus the events a subscription missed while the stream was still down, after a reconnect
// everything since the last pushed seq.
func (b *mcpBridge) catchUp(ctx context.Context, last int64) {
	b.mu.Lock()
	known := b.lastSeq > 0
	base, haveBase := b.subBase, b.subBaseSet
	b.mu.Unlock()
	if err := func() error {
		if known {
			events, err := b.opList(ctx, "events", jsfmt.Object(
				[]string{"after", "limit"},
				[]*jsfmt.Value{jsfmt.Number(float64(b.lastSeqLocked())), jsfmt.Number(500)}), b.callMS)
			if err != nil {
				return err
			}
			for _, event := range events {
				b.resource(event)
				b.deliver(event, true)
			}
			return nil
		}
		// a subscription taken before the stream was up misses what was published in between
		if haveBase && last > base {
			events, err := b.opList(ctx, "events", jsfmt.Object(
				[]string{"after", "limit"},
				[]*jsfmt.Value{jsfmt.Number(float64(base)), jsfmt.Number(500)}), b.callMS)
			if err != nil {
				return err
			}
			for _, event := range events {
				b.resource(event)
			}
		}
		events, err := b.opList(ctx, "inbox", jsfmt.Object(nil, nil), b.callMS)
		if err != nil {
			return err
		}
		for _, event := range events {
			b.deliver(event, false)
		}
		return nil
	}(); err != nil {
		b.logf("catch-up: %v", err)
	}
	b.mu.Lock()
	if last > b.lastSeq {
		b.lastSeq = last
	}
	b.mu.Unlock()
}

// lastSeqLocked answers the last pushed seq.
func (b *mcpBridge) lastSeqLocked() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastSeq
}

// opList runs one channel operation and answers its result list; ms is the call's timeout.
func (b *mcpBridge) opList(ctx context.Context, name string, args *jsfmt.Value, ms int) ([]*jsfmt.Value, error) {
	s := b.state()
	resp, err := HFetch(ctx, b.w, http.MethodPost,
		s.url+"/api/c/"+s.channelName+"/op/"+name+"?as="+queryEscape(s.me),
		map[string]string{"content-type": "application/json"}, []byte(args.EncodeJSON()),
		time.Duration(ms)*time.Millisecond, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	answer, perr := jsfmt.DecodeValue(raw)
	if perr != nil {
		return nil, perr
	}
	result, ok := fieldOf(answer, "result")
	if !ok || result.Kind() != jsfmt.KindArray {
		return nil, errors.New("no result list")
	}
	return result.Items(), nil
}

// opWait runs the blocking wait op and answers only whether the service answered: its result (a
// hit or a timeout) carries nothing poll needs, and it is an object, not a list.
func (b *mcpBridge) opWait(ctx context.Context, args *jsfmt.Value, ms int) error {
	s := b.state()
	resp, err := HFetch(ctx, b.w, http.MethodPost,
		s.url+"/api/c/"+s.channelName+"/op/wait?as="+queryEscape(s.me),
		map[string]string{"content-type": "application/json"}, []byte(args.EncodeJSON()),
		time.Duration(ms)*time.Millisecond, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// ── resources, subscriptions and poll ───────────────────────────────────────────────────────

// mcpResource is one entry of a resources/list reply.
type mcpResource struct {
	URI         string
	Name        string
	Description string
}

// value renders the resource the way the list answers.
func (r mcpResource) value() *jsfmt.Value {
	return jsfmt.Object([]string{"uri", "name", "description", "mimeType"},
		[]*jsfmt.Value{jsfmt.String(r.URI), jsfmt.String(r.Name), jsfmt.String(r.Description),
			jsfmt.String("application/json")})
}

// mcpResourceURI names one channel resource: the whole event log, or one topic's events.
func mcpResourceURI(channelName, topic string) string {
	if topic == "" {
		return "huddle://" + channelName + "/events"
	}
	return "huddle://" + channelName + "/topic/" + topic
}

// mcpResourceRef is one parsed resource uri: the topic (empty for the whole log) and the cursor
// its after query names.
type mcpResourceRef struct {
	Topic string
	After int64
}

// mcpParseResource reads one resource uri of this channel; ok is false for any other. Only the
// channel this bridge sits in is served: another channel's uri is not a resource here.
func mcpParseResource(uri, channelName string) (mcpResourceRef, bool) {
	ref := mcpResourceRef{}
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Scheme != "huddle" || parsed.Host != channelName {
		return ref, false
	}
	switch {
	case parsed.Path == "/events":
	case strings.HasPrefix(parsed.Path, "/topic/"):
		ref.Topic = strings.TrimPrefix(parsed.Path, "/topic/")
		if ref.Topic == "" {
			return ref, false
		}
	default:
		return ref, false
	}
	if after := parsed.Query().Get("after"); after != "" {
		ref.After = int64(jsNumber(after))
	}
	return ref, true
}

// mcpError shapes one JSON-RPC error reply for the resource methods.
func mcpError(id *jsfmt.Value, code float64, message string) *jsfmt.Value {
	err := jsfmt.Object([]string{"code", "message"}, []*jsfmt.Value{jsfmt.Number(code), jsfmt.String(message)})
	return jsfmt.Object([]string{"jsonrpc", "id", "error"}, []*jsfmt.Value{jsfmt.String("2.0"), id, err})
}

// resourceList answers resources/list for a session in a huddle: the channel's event log and one
// resource per topic the recent history shows.
func (b *mcpBridge) resourceList(ctx context.Context, id *jsfmt.Value) {
	s := b.state()
	resources := []mcpResource{{
		URI: mcpResourceURI(s.channelName, ""), Name: s.channelName + " events",
		Description: "every event of the channel, oldest last; read one with ?after=<seq>",
	}}
	items, err := b.opList(ctx, "events", jsfmt.Object(nil, nil), b.callMS)
	if err != nil {
		b.logf("resources/list: %v", err)
	} else {
		seen := map[string]bool{}
		for _, ev := range items {
			topic := mcpTextOf(ev, "topic")
			if topic == "" || seen[topic] {
				continue
			}
			seen[topic] = true
			resources = append(resources, mcpResource{
				URI: mcpResourceURI(s.channelName, topic), Name: s.channelName + " topic " + topic,
				Description: "events on " + topic + ", oldest last; read one with ?after=<seq>",
			})
		}
		b.mu.Lock()
		for topic := range seen {
			b.knownTop[topic] = true
		}
		b.resListed = true
		b.mu.Unlock()
	}
	values := make([]*jsfmt.Value, len(resources))
	for i, r := range resources {
		values[i] = r.value()
	}
	b.write(mcpReply(id, jsfmt.Object([]string{"resources"}, []*jsfmt.Value{jsfmt.Array(values)})))
}

// resourceRead answers resources/read: the events after the uri's cursor as one JSON text, at
// most mcpReadLimit of them.
func (b *mcpBridge) resourceRead(ctx context.Context, id *jsfmt.Value, uri string) {
	s := b.state()
	if s.url == "" || s.channelName == "" || s.me == "" {
		b.write(mcpError(id, -32002, "huddle: "+notInYet))
		return
	}
	ref, ok := mcpParseResource(uri, s.channelName)
	if !ok {
		b.write(mcpError(id, -32002, "huddle: unknown resource: "+uri))
		return
	}
	keys := []string{"after", "limit"}
	fields := []*jsfmt.Value{jsfmt.Number(float64(ref.After)), jsfmt.Number(mcpReadLimit)}
	if ref.Topic != "" {
		keys = append(keys, "topic")
		fields = append(fields, jsfmt.String(ref.Topic))
	}
	items, err := b.opList(ctx, "events", jsfmt.Object(keys, fields), b.callMS)
	if err != nil {
		b.write(mcpError(id, -32000, "huddle: "+err.Error()))
		return
	}
	content := jsfmt.Object([]string{"uri", "mimeType", "text"}, []*jsfmt.Value{
		jsfmt.String(uri), jsfmt.String("application/json"), jsfmt.String(jsfmt.Array(items).EncodeJSON())})
	b.write(mcpReply(id, jsfmt.Object([]string{"contents"}, []*jsfmt.Value{jsfmt.Array([]*jsfmt.Value{content})})))
}

// resource tells a subscriber that an event touched its resource: one resources/updated per
// subscribed uri the event matches, and a resources/list_changed once a shown list gains a topic.
func (b *mcpBridge) resource(event *jsfmt.Value) {
	if event == nil || event.Kind() != jsfmt.KindObject {
		return
	}
	topic := mcpTextOf(event, "topic")
	channelName := b.state().channelName
	uris := []string{mcpResourceURI(channelName, "")}
	if topic != "" {
		uris = append(uris, mcpResourceURI(channelName, topic))
	}
	b.mu.Lock()
	subscribed := []string{}
	for _, uri := range uris {
		if b.subs[uri] {
			subscribed = append(subscribed, uri)
		}
	}
	listChanged := b.resListed && topic != "" && !b.knownTop[topic]
	if listChanged {
		b.knownTop[topic] = true
	}
	b.mu.Unlock()
	for _, uri := range subscribed {
		b.write(jsfmt.Object([]string{"jsonrpc", "method", "params"}, []*jsfmt.Value{
			jsfmt.String("2.0"), jsfmt.String("notifications/resources/updated"),
			jsfmt.Object([]string{"uri"}, []*jsfmt.Value{jsfmt.String(uri)})}))
	}
	if listChanged {
		b.write(mcpNotification("notifications/resources/list_changed"))
	}
}

// mcpPollArgs is the poll tool's arguments: what came after a cursor, waiting up to timeout_ms
// for the topics.
type mcpPollArgs struct {
	After     int64
	Topics    []string
	TimeoutMS int64
}

// pollTool runs the poll tool: read what came after the cursor; when nothing is there, one wait
// for the topics, then a second read. The answer is the events as one JSON array, [] on timeout.
func (b *mcpBridge) pollTool(ctx context.Context, id *jsfmt.Value, args *jsfmt.Value) {
	in := mcpPollArgs{
		After:     int64(mcpNumberOf(mcpFieldOf(args, "after"))),
		TimeoutMS: int64(mcpNumberOf(mcpFieldOf(args, "timeout_ms"))),
	}
	if items := mcpFieldOf(args, "topics"); items != nil && items.Kind() == jsfmt.KindArray {
		for _, item := range items.Items() {
			if item.Kind() == jsfmt.KindString && item.Text() != "" {
				in.Topics = append(in.Topics, item.Text())
			}
		}
	}
	if in.TimeoutMS <= 0 {
		in.TimeoutMS = mcpPollDefaultMS
	}
	if in.TimeoutMS > mcpPollMaxMS {
		in.TimeoutMS = mcpPollMaxMS
	}
	// The server's wait wakes on any unread event, including ones before the caller's cursor, so
	// one wait can return at once with nothing new; poll re-checks until its deadline instead of
	// handing a harness an early empty answer it would spin on.
	deadline := time.Now().Add(time.Duration(in.TimeoutMS) * time.Millisecond)
	events := b.eventsAfter(ctx, in.After, in.Topics)
	for len(events) == 0 {
		left := time.Until(deadline)
		if left <= 0 || ctx.Err() != nil {
			break
		}
		secs := int64((left + time.Second - 1) / time.Second)
		started := time.Now()
		if err := b.opWait(ctx, jsfmt.Object(
			[]string{"topics", "timeout"},
			[]*jsfmt.Value{mcpStrings(in.Topics), jsfmt.Number(float64(secs))}),
			b.callMS+int(secs)*1000); err != nil {
			b.logf("poll wait: %v", err)
		}
		events = b.eventsAfter(ctx, in.After, in.Topics)
		if len(events) == 0 && time.Since(started) < mcpPollRecheck {
			select {
			case <-ctx.Done():
			case <-time.After(min(mcpPollRecheck, time.Until(deadline))):
			}
		}
	}
	b.write(textResult(id, jsfmt.Array(events).EncodeJSON(), false))
}

// mcpStrings is a JSON array of strings.
func mcpStrings(items []string) *jsfmt.Value {
	out := make([]*jsfmt.Value, len(items))
	for i, item := range items {
		out[i] = jsfmt.String(item)
	}
	return jsfmt.Array(out)
}

// eventsAfter reads the channel's events after a cursor that match the topic globs.
func (b *mcpBridge) eventsAfter(ctx context.Context, after int64, topics []string) []*jsfmt.Value {
	items, err := b.opList(ctx, "events", jsfmt.Object(
		[]string{"after", "limit"},
		[]*jsfmt.Value{jsfmt.Number(float64(after)), jsfmt.Number(mcpReadLimit)}), b.callMS)
	if err != nil {
		b.logf("poll events: %v", err)
		return []*jsfmt.Value{}
	}
	out := []*jsfmt.Value{}
	for _, ev := range items {
		if channel.GlobMatch(mcpTextOf(ev, "topic"), topics) {
			out = append(out, ev)
		}
	}
	return out
}

// deliver writes one channel event meant for this session as a claude/channel push; advance
// moves the cursor (a catch-up from the inbox does not: what it shows may be superseded live).
func (b *mcpBridge) deliver(event *jsfmt.Value, advance bool) {
	if event == nil || event.Kind() != jsfmt.KindObject {
		return
	}
	seq := int64(mcpNumberOf(mcpFieldOf(event, "seq")))
	b.mu.Lock()
	if b.pushed[seq] || (advance && seq <= b.lastSeq) {
		b.mu.Unlock()
		return
	}
	if advance {
		b.lastSeq = seq
	} else {
		b.pushed[seq] = true
	}
	from := mcpTextOf(event, "from")
	to := mcpTextOf(event, "to")
	topic := mcpTextOf(event, "topic")
	needsReply := false
	if flag := mcpFieldOf(event, "needs_reply"); flag != nil {
		needsReply = flag.EncodeJSON() == "true"
	}
	ref := mcpTextOf(event, "ref")
	text := mcpTextOf(event, "msg")
	channelName, me := b.channelName, b.me
	b.mu.Unlock()
	if from == me {
		return
	}
	overheard := to != "" && to != me
	if overheard {
		if topic != "msg" && topic != "ask" && topic != "reply" {
			return
		}
	} else if !(to == me || needsReply || b.matches(topic)) {
		return
	}
	addressee := to
	if addressee == "" {
		addressee = "all"
	}
	note := ""
	if ref != "" {
		note = " (" + ref + ")"
	}
	suffix := ""
	switch {
	case overheard:
		suffix = "\n(overheard: between other sessions, for your knowledge)"
	case needsReply:
		suffix = fmt.Sprintf("\n(needs your reply: tool reply seq=%d)", seq)
	}
	content := fmt.Sprintf("%s → %s · %s #%d%s: %s%s", from, addressee, topic, seq, note, text, suffix)
	meta := jsfmt.Object(
		[]string{"channel", "seq", "topic", "from"},
		[]*jsfmt.Value{
			jsfmt.String(channelName), jsfmt.String(strconv.FormatInt(seq, 10)),
			jsfmt.String(strings.ReplaceAll(topic, ".", "_")), jsfmt.String(strings.ReplaceAll(from, ".", "_")),
		})
	b.write(jsfmt.Object(
		[]string{"jsonrpc", "method", "params"},
		[]*jsfmt.Value{jsfmt.String("2.0"), jsfmt.String("notifications/claude/channel"),
			jsfmt.Object([]string{"content", "meta"}, []*jsfmt.Value{jsfmt.String(content), meta})}))
}

// matches says whether one topic is in the pushed globs.
func (b *mcpBridge) matches(topic string) bool {
	if b.pushOff {
		return false
	}
	for _, pattern := range b.pushGlobs {
		if pattern.MatchString(topic) {
			return true
		}
	}
	return false
}

// mcpGlobExpr turns one topic glob into a regular expression body: everything but * is literal.
func mcpGlobExpr(glob string) string {
	var out strings.Builder
	for _, r := range glob {
		switch r {
		case '.', '+', '^', '$', '{', '}', '(', ')', '|', '[', ']', '\\':
			out.WriteByte('\\')
			out.WriteRune(r)
		case '*':
			out.WriteString(".*")
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}

// mcpSleep waits for d, or until the session ends; false says it ended.
func mcpSleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// mcpIsTimeout says whether an HTTP failure was a deadline.
func mcpIsTimeout(err error) bool {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Timeout()
	}
	return errors.Is(err, context.DeadlineExceeded)
}

// mcpMethod answers a message's method.
func mcpMethod(msg *jsfmt.Value) string {
	return mcpTextOf(msg, "method")
}

// mcpFieldOf answers one field of a value, nil when absent.
func mcpFieldOf(v *jsfmt.Value, key string) *jsfmt.Value {
	field, _ := fieldOf(v, key)
	return field
}

// mcpTextOf answers one field's string text, empty for any other shape.
func mcpTextOf(v *jsfmt.Value, key string) string {
	if v == nil {
		return ""
	}
	field := mcpFieldOf(v, key)
	if field == nil || field.Kind() != jsfmt.KindString {
		return ""
	}
	return field.Text()
}

// mcpObjectOf answers v as an object, an empty one for any other shape (or nil).
func mcpObjectOf(v *jsfmt.Value) *jsfmt.Value {
	if v != nil && v.Kind() == jsfmt.KindObject {
		return v
	}
	return jsfmt.Object(nil, nil)
}

// mcpStringOf prints a value the way JavaScript's String() does, for the shapes a name can be.
func mcpStringOf(v *jsfmt.Value) string {
	if v == nil {
		return ""
	}
	switch v.Kind() {
	case jsfmt.KindString:
		return v.Text()
	case jsfmt.KindNumber:
		return jsfmt.JSNumber(mcpNumberOf(v))
	case jsfmt.KindBool:
		return v.EncodeJSON()
	default:
		return v.EncodeJSON()
	}
}

// mcpNumberOf reads a value as a number: strings parse, anything unparsable is 0.
func mcpNumberOf(v *jsfmt.Value) float64 {
	if v == nil {
		return 0
	}
	switch v.Kind() {
	case jsfmt.KindNumber:
		f, _ := strconv.ParseFloat(v.EncodeJSON(), 64)
		return f
	case jsfmt.KindString:
		return jsNumber(v.Text())
	case jsfmt.KindBool:
		if v.EncodeJSON() == "true" {
			return 1
		}
		return 0
	default:
		return 0
	}
}

// mcpTruthy reads a value the way JavaScript's if does.
func mcpTruthy(v *jsfmt.Value) bool {
	if v == nil || v.Kind() == jsfmt.KindNull {
		return false
	}
	if v.Kind() == jsfmt.KindBool {
		return v.EncodeJSON() == "true"
	}
	if v.Kind() == jsfmt.KindNumber {
		return v.EncodeJSON() != "0"
	}
	if v.Kind() == jsfmt.KindString {
		return v.Text() != ""
	}
	return true
}

// mcpWithField answers a copy of an object with one field replaced or appended.
func mcpWithField(obj *jsfmt.Value, key string, value *jsfmt.Value) *jsfmt.Value {
	keys := append([]string{}, obj.Keys()...)
	fields := append([]*jsfmt.Value{}, obj.Fields()...)
	for i, name := range keys {
		if name == key {
			fields[i] = value
			return jsfmt.Object(keys, fields)
		}
	}
	return jsfmt.Object(append(keys, key), append(fields, value))
}
