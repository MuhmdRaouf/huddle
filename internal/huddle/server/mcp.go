// Huddle as an MCP server (Streamable HTTP, JSON responses), at POST /mcp/<channel>?as=<session>
// (or headers x-huddle-channel / x-huddle-as). Tools are the channel ops; a blocking tool (wait,
// gate, wait_task) is cancelled when the client sends notifications/cancelled or drops the
// request. Server-to-client pushes (claude/channel wake-ups) go through the stdio bridge, which
// reads the channel's live stream.
package server

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

// Instructions is the system prompt a channel's sessions start with.
func Instructions(name, me string) string {
	return fmt.Sprintf(`You are "%s" in Huddle channel "%s": every session (and subagent) in this channel sees the same events, tasks and shared knowledge, so act as one team.
Protocol (the huddle skill has the full version):
1. You are already joined (the session start did it): call status to refresh; join only to change your role or context. Tools are mcp__plugin_huddle_huddle__<op>; from Bash, `+"`huddle <op>`"+`.
2. recall before reading files or re-deriving anything; remember what you learn (fact, lesson, decision, context, result, howto).
3. gate before any change (paused = stop). Answer every inbox message with reply.
4. Work: start (takes your next task, or the id you name) → do it → finish "<command + result>" (releases what waited on it, names your next). When start says the task waits on another session's: wait with topics ["task.ready"] (or wait_task) instead of polling or guessing.
5. Need someone else: send (ask=true for a reply), or task_create with owner and after. Ping-pong work: only the turn holder acts; pass the turn when done.
6. Idle or blocked on others: wait (it returns kind message | event | timeout; on event, handle it and its skipped list, then ack its seq; on timeout, wait again).
7. Subagents you start: they join as "%s.<role>" (pass as), share results with remember, and leave when done.`, me, name, me)
}

// Mcp answers JSON-RPC against a hub's channels, tracking the calls a cancellation can end.
// Version is the plugin's own version, named in every initialize answer.
type Mcp struct {
	version  string
	mu       sync.Mutex
	inflight map[string]context.CancelFunc
	// afterOp runs after every op a tool call carried through, so the server can fan a channel's
	// broadcast posts out to its linked sessions (links.go); nil runs nothing
	afterOp func(ctx context.Context, channelName, from, op string, args *jsfmt.Value)
}

// NewMcp answers an empty call registry.
func NewMcp(version string) *Mcp {
	return &Mcp{version: version, inflight: map[string]context.CancelFunc{}}
}

func jstr(text string) *jsfmt.Value { return jsfmt.String(text) }

func jnullValue() *jsfmt.Value {
	v, _ := jsfmt.DecodeValue([]byte("null"))
	return v
}

func jnumValue(n int64) *jsfmt.Value {
	v, _ := jsfmt.DecodeValue([]byte(strconv.FormatInt(n, 10)))
	return v
}

func jarray(items []*jsfmt.Value) *jsfmt.Value { return jsfmt.Array(items) }

// rpcValue echoes one member the caller sent, or the fallback when it is not there.
func rpcValue(params *jsfmt.Value, key, fallback string) *jsfmt.Value {
	if v := fieldOf(params, key); v != nil && v.Kind() != jsfmt.KindNull {
		return v
	}
	return jstr(fallback)
}

// msgID reads one message's id as the reply must echo it (null for a notification).
func msgID(msg *jsfmt.Value) *jsfmt.Value {
	if id := fieldOf(msg, "id"); id != nil && id.Kind() != jsfmt.KindNull {
		return id
	}
	return jnullValue()
}

func rpcReply(id *jsfmt.Value, result *jsfmt.Value) *jsfmt.Value {
	return jsfmt.Object([]string{"jsonrpc", "id", "result"}, []*jsfmt.Value{jstr("2.0"), id, result})
}

func rpcFail(id *jsfmt.Value, code int64, message string) *jsfmt.Value {
	err := jsfmt.Object([]string{"code", "message"}, []*jsfmt.Value{jnumValue(code), jstr(message)})
	return jsfmt.Object([]string{"jsonrpc", "id", "error"}, []*jsfmt.Value{jstr("2.0"), id, err})
}

// evLine renders one event the way the one-line answers quote it.
func evLine(e channel.Ev) string {
	to := "all"
	if e.To != nil {
		to = *e.To
	}
	msg := ""
	if e.Msg != nil {
		msg = *e.Msg
	}
	return fmt.Sprintf("#%d %s → %s (%s): %s", e.Seq, e.From, to, e.Topic, jsscalar.UTF16Slice(msg, 0, 400))
}

// shortLine is one op's one-line answer; "" means the whole JSON object is the answer.
func shortLine(name string, result any) string {
	switch name {
	case "send", "publish", "reply":
		e, ok := result.(channel.Ev)
		if !ok || e.Seq == 0 {
			return ""
		}
		to := "everyone"
		if e.To != nil {
			to = *e.To
		}
		switch name {
		case "send":
			ask := ""
			if e.NeedsReply {
				ask = " (an ask: it waits for a reply)"
			}
			return fmt.Sprintf("#%d sent to %s%s", e.Seq, to, ask)
		case "publish":
			return fmt.Sprintf("#%d published (%s) to %s", e.Seq, e.Topic, to)
		default:
			return fmt.Sprintf("#%d replied", e.Seq)
		}
	case "remember":
		r, ok := result.(channel.KBShape)
		if !ok || r.ID == 0 {
			return ""
		}
		return fmt.Sprintf("#%d remembered: %s", r.ID, r.Title)
	case "task_create":
		r, ok := result.(channel.CreateTaskResult)
		if !ok || r.Task == nil || r.Task.ID == "" {
			return ""
		}
		owner := "unowned"
		if r.Task.Owner != nil {
			owner = *r.Task.Owner
		}
		if r.Task.Ready {
			return fmt.Sprintf("created %s → %s (ready)", r.Task.ID, owner)
		}
		unmet := make([]string, 0, len(r.Task.Unmet))
		for _, u := range r.Task.Unmet {
			unmet = append(unmet, u.ID)
		}
		return fmt.Sprintf("created %s → %s (waits on %s)", r.Task.ID, owner, strings.Join(unmet, ", "))
	case "finish":
		r, ok := result.(channel.FinishResult)
		if !ok || r.Done == "" {
			return ""
		}
		released := "none"
		if len(r.Released) > 0 {
			parts := make([]string, 0, len(r.Released))
			for _, x := range r.Released {
				to := "?"
				if x.To != nil {
					to = *x.To
				}
				task := ""
				if x.Task != nil {
					task = *x.Task
				}
				parts = append(parts, task+"→"+to)
			}
			released = strings.Join(parts, ", ")
		}
		knowledge := ""
		if r.Knowledge != nil {
			knowledge = fmt.Sprintf("; result #%d", *r.Knowledge)
		}
		next := "none"
		if r.Next != nil {
			waits := ""
			if !r.Next.Ready {
				waits = fmt.Sprintf(" (waits on %s)", strings.Join(r.Next.Unmet, ", "))
			}
			next = fmt.Sprintf("%s %s%s", r.Next.ID, r.Next.Title, waits)
		}
		return fmt.Sprintf("done %s; released: %s%s; next: %s", r.Done, released, knowledge, next)
	case "wait":
		r, ok := result.(channel.WaitHit)
		if !ok {
			return ""
		}
		if r.Kind == "timeout" {
			return "timeout: call wait again"
		}
		if r.Kind == "" || r.Seq == 0 {
			return ""
		}
		e := channel.Ev{
			Seq: r.Seq, From: r.From, To: r.To, Topic: r.Topic, Msg: r.Msg, NeedsReply: r.NeedsReply,
		}
		line := fmt.Sprintf("%s: %s", r.Kind, evLine(e))
		if r.NeedsReply {
			line += fmt.Sprintf(" [answer: reply seq=%d]", r.Seq)
		}
		if r.More > 0 {
			line += fmt.Sprintf(" (+%d more waiting)", r.More)
		}
		line += fmt.Sprintf(" — ack %d once handled", r.Seq)
		lines := []string{line}
		for _, s := range r.Skipped {
			sk := channel.Ev{Seq: s.Seq, From: s.From, To: s.To, Topic: s.Topic, Msg: s.Msg}
			lines = append(lines, "  skipped "+evLine(sk))
		}
		if r.SkippedTotal > len(r.Skipped) {
			lines = append(lines, fmt.Sprintf("  (%d older skipped: events)", r.SkippedTotal-len(r.Skipped)))
		}
		return strings.Join(lines, "\n")
	}
	return ""
}

// stringifyIndent1 renders a result the way JSON.stringify(v, null, 1) does: one space per level.
func stringifyIndent1(v any) string {
	compact, err := domain.MarshalCompactJSON(v)
	if err != nil {
		return ""
	}
	value, err := jsfmt.DecodeValue([]byte(compact))
	if err != nil {
		return ""
	}
	var build func(value *jsfmt.Value, depth int) string
	build = func(value *jsfmt.Value, depth int) string {
		pad := strings.Repeat(" ", depth)
		inner := strings.Repeat(" ", depth+1)
		switch value.Kind() {
		case jsfmt.KindObject:
			if len(value.Keys()) == 0 {
				return "{}"
			}
			parts := make([]string, 0, len(value.Keys()))
			for i, k := range value.Keys() {
				parts = append(parts, inner+strconv.Quote(k)+": "+build(value.Fields()[i], depth+1))
			}
			return "{\n" + strings.Join(parts, ",\n") + "\n" + pad + "}"
		case jsfmt.KindArray:
			if len(value.Items()) == 0 {
				return "[]"
			}
			parts := make([]string, 0, len(value.Items()))
			for _, item := range value.Items() {
				parts = append(parts, inner+build(item, depth+1))
			}
			return "[\n" + strings.Join(parts, ",\n") + "\n" + pad + "]"
		default:
			return value.EncodeJSON()
		}
	}
	return build(value, 0)
}

// Handle answers one JSON-RPC message; nil means no response (a notification). A cancellation
// (notifications/cancelled) or a dropped request ends the tool call it names.
func (m *Mcp) Handle(ctx context.Context, h *channel.Hub, channelName, conn string, msg *jsfmt.Value) *jsfmt.Value {
	if msg == nil || msg.Kind() != jsfmt.KindObject {
		return nil
	}
	id := msgID(msg)
	method := textOf(fieldOf(msg, "method"))
	params := fieldOf(msg, "params")
	key := func() string {
		requestID := msgID(msg)
		if v := fieldOf(params, "requestId"); v != nil && v.Kind() != jsfmt.KindNull {
			requestID = v
		}
		return channelName + "/" + conn + "/" + requestID.EncodeJSON()
	}
	if method == "notifications/cancelled" {
		m.mu.Lock()
		cancel, ok := m.inflight[key()]
		m.mu.Unlock()
		if ok {
			cancel()
		}
		return nil
	}
	if fieldOf(msg, "id") == nil || fieldOf(msg, "id").Kind() == jsfmt.KindNull {
		return nil // other notifications
	}
	switch method {
	case "initialize":
		tools := jsfmt.Object(nil, nil)
		experiment := jsfmt.Object([]string{"claude/channel"}, []*jsfmt.Value{jsfmt.Object(nil, nil)})
		capabilities := jsfmt.Object(
			[]string{"tools", "experimental"}, []*jsfmt.Value{tools, experiment})
		serverInfo := jsfmt.Object([]string{"name", "version"},
			[]*jsfmt.Value{jstr("huddle"), jstr(m.version)})
		result := jsfmt.Object(
			[]string{"protocolVersion", "capabilities", "serverInfo", "instructions"},
			[]*jsfmt.Value{rpcValue(params, "protocolVersion", "2025-06-18"), capabilities, serverInfo,
				jstr(Instructions(channelName, conn))})
		return rpcReply(id, result)
	case "ping":
		return rpcReply(id, jsfmt.Object(nil, nil))
	case "tools/list":
		defs := channel.ToolDefs()
		tools := make([]*jsfmt.Value, 0, len(defs))
		for _, d := range defs {
			tools = append(tools, jsfmt.Object(
				[]string{"name", "description", "inputSchema"},
				[]*jsfmt.Value{jstr(d.Name), jstr(d.Description), d.Schema}))
		}
		return rpcReply(id, jsfmt.Object([]string{"tools"}, []*jsfmt.Value{jarray(tools)}))
	case "tools/call":
		return m.toolCall(ctx, h, channelName, conn, id, params, key())
	case "resources/list":
		return rpcReply(id, jsfmt.Object([]string{"resources"}, []*jsfmt.Value{jarray(nil)}))
	case "prompts/list":
		return rpcReply(id, jsfmt.Object([]string{"prompts"}, []*jsfmt.Value{jarray(nil)}))
	}
	return rpcFail(id, -32601, "method not found: "+method)
}

// noOperationRe names the one 404 a tool call answers as JSON-RPC's own invalid-params error.
var noOperationRe = regexp.MustCompile(`no operation`)

func (m *Mcp) toolCall(ctx context.Context, h *channel.Hub, channelName, conn string, id *jsfmt.Value,
	params *jsfmt.Value, key string) *jsfmt.Value {
	name := textOf(fieldOf(params, "name"))
	arguments := fieldOf(params, "arguments")
	args := jsfmt.Object(nil, nil)
	if arguments != nil && arguments.Kind() == jsfmt.KindObject {
		args = jsfmt.Object(arguments.Keys(), arguments.Fields())
	}
	// The table's blocking ops wait by design: an agent that passes a short timeout burns a turn
	// per wake-up, so the tool waits at least a minute.
	if channel.Blocks(name) {
		if t := fieldOf(args, "timeout"); t != nil && t.Kind() != jsfmt.KindNull {
			seconds := 0.0
			switch t.Kind() {
			case jsfmt.KindNumber:
				seconds, _ = strconv.ParseFloat(t.EncodeJSON(), 64)
			case jsfmt.KindString:
				seconds, _ = strconv.ParseFloat(t.Text(), 64)
			}
			if seconds < 60 {
				seconds = 60
			}
			timeout, _ := jsfmt.DecodeValue([]byte(strconv.FormatFloat(seconds, 'f', -1, 64)))
			fields := append([]*jsfmt.Value(nil), args.Fields()...)
			for i, k := range args.Keys() {
				if k == "timeout" {
					fields[i] = timeout
				}
			}
			args = jsfmt.Object(args.Keys(), fields)
		}
	}
	callCtx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.inflight[key] = cancel
	m.mu.Unlock()
	defer func() {
		cancel()
		m.mu.Lock()
		delete(m.inflight, key)
		m.mu.Unlock()
	}()
	textResult := func(text string, isErr bool) *jsfmt.Value {
		content := jsfmt.Object([]string{"type", "text"}, []*jsfmt.Value{jstr("text"), jstr(text)})
		keys := []string{"content"}
		fields := []*jsfmt.Value{jarray([]*jsfmt.Value{content})}
		if isErr {
			keys = append(keys, "isError")
			fields = append(fields, mustBool(true))
		}
		return rpcReply(id, jsfmt.Object(keys, fields))
	}
	ch, err := h.Get(callCtx, channelName)
	if err != nil {
		var he *channel.HuddleError
		if errors.As(err, &he) {
			return textResult("huddle: "+he.Msg, true)
		}
		return textResult("huddle: "+err.Error(), true)
	}
	result, err := channel.RunOp(callCtx, name, ch, conn, args)
	if err != nil {
		var he *channel.HuddleError
		if errors.As(err, &he) && he.Status == 404 && noOperationRe.MatchString(he.Msg) {
			return rpcFail(id, -32602, he.Msg)
		}
		message := err.Error()
		if errors.As(err, &he) {
			message = he.Msg
		}
		return textResult("huddle: "+message, true)
	}
	if m.afterOp != nil {
		m.afterOp(callCtx, channelName, conn, name, args)
	}
	short := ""
	if result.Text != nil {
		short = *result.Text
	}
	if short == "" && !truthy(fieldOf(args, "json")) {
		short = shortLine(name, result.Result)
	}
	if short == "" {
		short = stringifyIndent1(result.Result)
	}
	return textResult(short, false)
}

func mustBool(b bool) *jsfmt.Value {
	v, _ := jsfmt.DecodeValue([]byte("true"))
	if !b {
		v, _ = jsfmt.DecodeValue([]byte("false"))
	}
	return v
}
