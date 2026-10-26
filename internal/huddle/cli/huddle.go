// The huddle command: the shell face of Huddle, the same operations the MCP tools offer.
// Background subagents have Bash but no MCP tools: this is how they join their parent's channel.
//
// The line splits into --flags (dashes to underscores, the next word the value unless it is a
// flag too) and plain words; the first plain word is the command, the rest its arguments. Exit:
// 0 ok, 2 usage or the server's refusal, 3 a message for this session, 4 paused, 5 Huddle
// unreachable, 124 a wait that timed out.
package cli

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/adapters/safego"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	channelpkg "github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/server"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// helpLines is the command block `huddle help` prints: the usage the command carries.
var helpLines = []string{
	"Background subagents have Bash but no MCP tools: this is how they join their parent's channel.",
	"",
	"  huddle join [--role \"…\"] [--fresh|--sync]  join (or resume); prints the picture (fresh: a brief, not the backlog)",
	"  huddle wait [glob…] [--timeout s]        block: 0 event · 3 message for you · 124 timeout",
	"  huddle ack <seq>                         handled everything up to seq",
	"  huddle listen [--after seq] [--state f] [--all]   stream the channel and its \"listen\" channels: one JSON",
	"                                           line per event from the others, also between other sessions",
	"                                           (--state: resume where the last listen stopped; --all: presence too)",
	"  huddle gate [--no-block]                 wait while paused (4 = paused, with --no-block)",
	"  huddle send [<to>] <msg…> [--ask]        message one session (or everyone)",
	"  huddle reply <seq> <msg…>                answer an ask",
	"  huddle pub <topic> <ref> <msg…> [--to s] publish an event",
	"  huddle next | task <id> | tasks [--owner s]",
	"  huddle doing|done|blocked|skipped <id> [note…]",
	"  huddle new <title…> [--owner s] [--after a,b]   create a task (for another session too)",
	"  huddle wait-task <id>                    block until a task is done",
	"  huddle start [id]                       gate check + take the task (or your next) + doing; prints it",
	"  huddle finish <evidence…> [--id x] [--result \"…\"]   done + share its output + what it released",
	"  huddle depend <session> <title…>        make a task for them, block yours on it, wait until released",
	"  huddle handoff <to> <msg…> [--title t]  (task for them +) pass the turn + wait",
	"  huddle ask <session> <question…>        ask and block until they answer (prints the answer)",
	"  huddle remember <kind> <title> <body…> [--tags a,b]   share knowledge",
	"  huddle recall [words…] | kb <id>          verified entries first; each shows its age and \"may be stale\"",
	"  huddle remember … [--scope server] [--force]   server: for every channel on this server; force: even if similar",
	"  huddle verify <id> [--undo] | share <id>  vouch for an entry · make it one for every channel on this server",
	"  huddle knowledge export [--verified]    the knowledge as Markdown, grouped by kind, to paste into CLAUDE.md",
	"  huddle pause|resume <session> [why…] | turn | pass <to> [msg…] | sessions | status | leave [summary…]",
	"  huddle reserve <path…> [--ttl 60] [--note …]   reserve files you are about to change; others are warned",
	"                                           before they edit one (never blocked); re-reserving extends",
	"  huddle release [path…]                   release reservations (all of yours when none are named)",
	"  huddle reservations [--mine]             the channel's live reservations, newest first",
	"  huddle map                              the big picture: phases, who does what, the critical path",
	"  huddle digest [--since 24h]             what happened since then, per session: finished, notes, knowledge,",
	"                                           blocked, open questions (and the estimated cost when Radar runs)",
	"  huddle assign <session> --fresh|--sync|--default [--task id]   orchestrator: how it rejoins (+ a task)",
	"  huddle brief <session> <text…>          orchestrator: what its next fresh join gets",
	"  huddle join <host:port> --token <id.secret> [--as name] [--channel c]   join a huddle with an invite: this",
	"                                           session gets its own credential (kept per session, never the invite)",
	"  huddle token create [--ttl 24h|0] [--single-use] [--can-invite] [--print-join-command] | token list | token delete <id>",
	"                                           (--print-join-command prints the join: line to paste into the other session)",
	"  huddle members | kick <name>            who holds a credential; revoke one (the creator only)",
	"  huddle channel rm <name>                remove a channel (the creator only); a session online refuses",
	"                                           it, the history stays under channels/.removed/",
	"  huddle harness list | install <name> | remove <name>   wire huddle's MCP server, and the",
	"                                           agent's own lifecycle hooks where it has them, into",
	"                                           another coding agent: codex, gemini, qwen, opencode, crush",
	"  huddle statusline [--install|--remove] [--color]   one line for Claude Code's status bar:",
	"                                           channel, who is online, today's spend, the 5h and",
	"                                           week plan limits off its own snapshot; --install",
	"                                           wires it in keeping your own, --remove restores it",
	"  huddle open                             a dashboard link that signs your browser in once (5 min), for any session",
	"                                           that joined",
	"  huddle pair [--name n]                  a one-use link (5 min) that pairs a device with a passkey; devices rm",
	"                                           revokes it and ends its sessions",
	"  huddle totp setup                       set up sign-in with an authenticator app (QR + one-time recovery",
	"                                           codes); totp off removes it and ends its sessions",
	"  huddle pair --satellite                 a one-use invite (10 min) that joins another machine as this hub's satellite",
	"  huddle join-hub <url> <invite> [--name n]   on the satellite: keeps its credential and sets its role (a restart links it)",
	"  huddle up [--port n] | down | server   run the server bundled with the plugin (files in .agents/huddle/; its",
	"                                           port is random per project, saved there, and shown by up and server)",
	"  huddle setup [show] [--port n] [--autostart|--no-autostart] [--listen a,b] [--no-notify] [--start] [--channel c --as s]   set up this machine and project",
	"  huddle <op> --key value …               any operation by name (huddle ops lists them)",
	"Identity: env HUDDLE_URL/HUDDLE_CHANNEL/HUDDLE_AS, else the nearest .agents/huddle/huddle.json up from the cwd",
	"(huddle whoami). A subagent runs with HUDDLE_AS=<parent>.<role>, or passes --as <parent>.<role>. Exit: 0 ok · 2 usage/error · 3 message · 4 paused · 5 Huddle unreachable · 124 timeout.",
}

// noPort and notInAHuddle are the two ways a command cannot even ask: no server yet, or no
// channel and name to ask as.
const (
	noPort       = "Huddle has no port here yet: start it with huddle up (it takes 18860, then 18861 to 18869, and saves the choice)"
	notInAHuddle = "not in a huddle: in a session that is in one, run /huddle:invite and paste its join line here (/huddle:join …); or run /huddle:setup to start one"
)

// nullValue builds the JSON null: a field the server expects even when it carries nothing.
func nullValue() *jsfmt.Value { return &jsfmt.Value{} }

// hostPort matches the <host:port> (or a full http url) an invite join names.
var hostPort = regexp.MustCompile(`^(?:https?://)?[\w.-]+:\d+/?$`)

// httpPrefix matches an address that already carries its scheme.
var httpPrefix = regexp.MustCompile(`^https?://`)

// sessionName matches a name a message may be addressed to.
var sessionName = regexp.MustCompile(`^[a-z][a-z0-9_.-]*$`)

// ttlShape matches the number-and-unit a ttl is.
var ttlShape = regexp.MustCompile(`^(\d+)([smhd]?)$`)

// wholeNumber matches the integers an operation by name parses into numbers.
var wholeNumber = regexp.MustCompile(`^-?\d+$`)

// flag is one --flag: its key with dashes as underscores, and either the next word as its value
// or, when no value follows, just true.
type flag struct {
	key    string
	value  string
	isText bool
}

// flags is a command line's --flags in the order they were given.
type flags struct{ order []flag }

// parseFlags splits a command line the way the CLI reads it: a --flag takes the next word as its
// value unless that word is a flag too (then the flag is just true); every other word is plain.
func parseFlags(argv []string) (*flags, []string) {
	parsed := &flags{}
	var pos []string
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if !strings.HasPrefix(a, "--") {
			pos = append(pos, a)
			continue
		}
		key := strings.ReplaceAll(a[2:], "-", "_")
		if i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "--") {
			i++
			parsed.order = append(parsed.order, flag{key: key, value: argv[i], isText: true})
			continue
		}
		parsed.order = append(parsed.order, flag{key: key})
	}
	return parsed, pos
}

// has answers whether the flag was given.
func (f *flags) has(key string) bool {
	if f == nil {
		return false
	}
	for _, one := range f.order {
		if one.key == key {
			return true
		}
	}
	return false
}

// value answers the flag itself, if it was given.
func (f *flags) value(key string) (flag, bool) {
	if f == nil {
		return flag{}, false
	}
	for _, one := range f.order {
		if one.key == key {
			return one, true
		}
	}
	return flag{}, false
}

// text answers the flag's value when it took one.
func (f *flags) text(key string) (string, bool) {
	one, ok := f.value(key)
	if !ok || !one.isText {
		return "", false
	}
	return one.value, true
}

// str answers the flag as JavaScript's String(v): its value, or "true" for a valueless flag.
func (f *flags) str(key string) (string, bool) {
	one, ok := f.value(key)
	if !ok {
		return "", false
	}
	if one.isText {
		return one.value, true
	}
	return "true", true
}

// truthy answers whether the flag was given with a value JavaScript would count as set: a
// non-empty string or a valueless true.
func (f *flags) truthy(key string) bool {
	one, ok := f.value(key)
	if !ok {
		return false
	}
	return !one.isText || one.value != ""
}

// bare answers whether the flag was given with no value at all (it reads as the boolean true).
func (f *flags) bare(key string) bool {
	one, ok := f.value(key)
	return ok && !one.isText
}

// without answers the same flags minus the named ones: --as and --channel belong to this
// command, not to the operation it forwards.
func (f *flags) without(keys ...string) *flags {
	out := &flags{}
	for _, one := range f.order {
		drop := false
		for _, key := range keys {
			if one.key == key {
				drop = true
			}
		}
		if !drop {
			out.order = append(out.order, one)
		}
	}
	return out
}

// cli is one huddle command run: its world, its parsed line, its session and its writers.
type cli struct {
	w       *World
	stdout  io.Writer
	stderr  io.Writer
	opt     *flags // the operation's flags: --as and --channel are taken out
	raw     *flags // the flags as given, for the branches that read their own
	pos     []string
	command string
	id      Identity
	url     string
	ch      string
	me      string
	as      string // --as: this one operation runs as that session
	pretty  bool
}

// die says why the command cannot go on; the returned int is its exit code.
func (c *cli) die(msg string, code int) int {
	fmt.Fprintf(c.stderr, "huddle: %s\n", msg)
	return code
}

// say prints one line to stdout.
func (c *cli) say(s string) { fmt.Fprintln(c.stdout, s) }

// rest joins the plain words left, space separated.
func (c *cli) rest() string { return strings.Join(c.pos, " ") }

// shift takes the next plain word; ok is false when there is none.
func (c *cli) shift() (string, bool) {
	if len(c.pos) == 0 {
		return "", false
	}
	first := c.pos[0]
	c.pos = c.pos[1:]
	return first, true
}

// Huddle runs the huddle command (argv without the program name) in w's world and answers the
// exit code.
func Huddle(ctx context.Context, w *World, argv []string, stdout, stderr io.Writer) int {
	if w == nil {
		w = &World{}
	}
	opt, pos := parseFlags(argv)
	id := Identify(ctx, w, "")
	c := &cli{w: w, stdout: stdout, stderr: stderr, opt: opt, raw: opt, pos: pos, id: id, url: id.URL}
	if v, ok := opt.str("channel"); ok {
		c.ch = v
	} else {
		c.ch = id.Channel
	}
	if v, ok := opt.str("as_session"); ok {
		c.me = v
	} else {
		c.me = id.As
	}
	if v, ok := opt.str("as"); ok && v != "" {
		c.as = v
	}
	c.opt = opt.without("as", "channel")
	c.pretty = opt.has("pretty")
	cmd := "help"
	if len(c.pos) > 0 {
		cmd = c.pos[0]
		c.pos = c.pos[1:]
	}
	c.command = cmd

	if cmd == "help" || cmd == "-h" {
		c.say(strings.Join(helpLines, "\n"))
		return 0
	}
	if cmd == "setup" {
		sub := ""
		if next, ok := c.shift(); ok {
			sub = next
		}
		return setup(ctx, w, sub, c.raw, id.URL, stdout, stderr)
	}
	if cmd == "up" || cmd == "down" || cmd == "server" {
		// the whole line: up's --port keeps its place and its meaning wherever it stands
		return ServerCommand(ctx, w, cmd, id.URL, argv, stdout, stderr)
	}
	if cmd == "token" {
		return c.token(ctx)
	}
	if cmd == "members" {
		return c.members(ctx)
	}
	if cmd == "kick" {
		return c.kick(ctx)
	}
	if cmd == "channel" {
		return c.channel(ctx)
	}
	if cmd == "open" {
		return c.open(ctx)
	}
	if cmd == "pair" {
		// the whole line: pair's --name keeps its place and its meaning wherever it stands
		return Pair(ctx, w, argv, stdout, stderr)
	}
	if cmd == "join-hub" {
		// the whole line: a machine joins its hub with no channel of its own
		return JoinHub(ctx, w, argv, stdout, stderr)
	}
	if cmd == "devices" {
		return Devices(ctx, w, argv, stdout, stderr)
	}
	if cmd == "totp" {
		return TOTPCommand(ctx, w, argv, stdout, stderr)
	}
	if cmd == "join" && (c.raw.has("token") || (len(c.pos) > 0 && hostPort.MatchString(c.pos[0]))) {
		if code := c.joinWithInvite(ctx); code != 0 {
			return code
		}
	}
	if cmd == "whoami" {
		if raw, err := domain.MarshalCompactJSON(id); err == nil {
			c.say(string(raw))
		}
		return 0
	}
	if cmd == "harness" {
		return c.harness(ctx)
	}
	if cmd == "statusline" {
		return c.statusline(ctx)
	}
	if c.ch == "" || c.me == "" {
		return c.die(notInAHuddle, 2)
	}
	if c.url == "" {
		return c.die(noPort, 5)
	}

	switch cmd {
	case "listen":
		return c.listen(ctx)
	case "join":
		args := newBody().textAlways("role", flagOr(c.opt, "role", c.id.Role))
		args.flagText(c.opt, "label")
		if task, ok := c.opt.text("task"); ok {
			args.textAlways("task", task)
		} else if rest := c.rest(); rest != "" {
			args.textAlways("task", rest)
		}
		if c.opt.has("fresh") {
			args.textAlways("context", "fresh")
		} else if c.opt.has("sync") {
			args.textAlways("context", "sync")
		} else if c.id.Context == "sync" || c.id.Context == "fresh" {
			args.textAlways("context", c.id.Context)
		}
		return c.op(ctx, "join", args)
	case "map":
		return c.op(ctx, "map", newBody().flagValue(c.opt, "json"))
	case "assign":
		session, ok := c.shift()
		if !ok {
			return c.die("assign <session> --fresh|--sync|--default [--task id]", 2)
		}
		args := newBody().textAlways("session", session)
		switch {
		case c.opt.has("fresh"):
			args.textAlways("context", "fresh")
		case c.opt.has("sync"):
			args.textAlways("context", "sync")
		default:
			args.raw("context", nullValue())
		}
		args.flagText(c.opt, "task")
		return c.op(ctx, "assign", args)
	case "brief":
		session, ok := c.shift()
		if !ok {
			return c.die("brief <session> <text>", 2)
		}
		msg := c.rest()
		if msg == "" {
			return c.die("brief <session> <text>", 2)
		}
		return c.op(ctx, "brief", newBody().textAlways("session", session).textAlways("msg", msg))
	case "wait":
		return c.wait(ctx)
	case "wait-task":
		idArg, ok := c.shift()
		if !ok {
			return c.die("wait-task <id>", 2)
		}
		for {
			answer, code := c.call(ctx, "wait_task", newBody().textAlways("id", idArg).number("timeout", 600))
			if code != 0 {
				return code
			}
			if kindOf(answer.result) != "timeout" {
				c.say(c.resultJSON(answer))
				return 0
			}
			if ctx.Err() != nil {
				return 0
			}
		}
	case "gate":
		return c.gate(ctx)
	case "ack":
		seq, ok := c.shift()
		if !ok {
			return c.die("ack <seq>", 2)
		}
		return c.op(ctx, "ack", newBody().raw("seq", jsNumberOrNull(seq)))
	case "send":
		to := ""
		if len(c.pos) > 1 && sessionName.MatchString(c.pos[0]) && !c.opt.has("all") {
			to, _ = c.shift()
		}
		args := newBody()
		if flagTo, ok := c.opt.text("to"); ok {
			args.textAlways("to", flagTo)
		} else if to != "" {
			args.textAlways("to", to)
		}
		args.textAlways("msg", c.rest()).bool("ask", c.opt.has("ask"))
		args.flagText(c.opt, "task")
		return c.op(ctx, "send", args)
	case "reply":
		seq, ok := c.shift()
		if !ok {
			return c.die("reply <seq> <msg>", 2)
		}
		msg := c.rest()
		if msg == "" {
			msg = "ack"
		}
		return c.op(ctx, "reply", newBody().raw("seq", jsNumberOrNull(seq)).textAlways("msg", msg))
	case "pub":
		topic, topicOK := c.shift()
		ref, refOK := c.shift()
		if !topicOK || !refOK || len(c.pos) == 0 {
			return c.die("pub <topic> <ref> <msg>", 2)
		}
		args := newBody().textAlways("topic", topic).textAlways("ref", ref).textAlways("msg", strings.Join(c.pos, " "))
		args.flagText(c.opt, "to")
		args.bool("ask", c.opt.has("ask"))
		args.flagText(c.opt, "key")
		return c.op(ctx, "publish", args)
	case "doing", "done", "blocked", "skipped", "todo":
		idArg, ok := c.shift()
		if !ok {
			return c.die(cmd+" <task id> [note]", 2)
		}
		return c.op(ctx, "task_status", newBody().textAlways("id", idArg).textAlways("status", cmd).textAlways("note", c.rest()))
	case "new":
		args := newBody().textAlways("title", c.rest())
		args.flagText(c.opt, "owner")
		args.flagList(c.opt, "after")
		args.flagText(c.opt, "id")
		args.flagText(c.opt, "what")
		return c.op(ctx, "task_create", args)
	case "start":
		args := newBody()
		if idArg, ok := c.shift(); ok {
			args.textAlways("id", idArg)
		}
		return c.op(ctx, "start", args)
	case "finish":
		note := c.rest()
		if note == "" {
			return c.die("finish <evidence>", 2)
		}
		args := newBody()
		args.flagText(c.opt, "id")
		args.textAlways("note", note)
		args.flagText(c.opt, "result")
		args.flagText(c.opt, "title")
		return c.op(ctx, "finish", args)
	case "depend":
		owner, ok := c.shift()
		if !ok {
			return c.die("depend <session> <title>", 2)
		}
		title := c.rest()
		if title == "" {
			return c.die("depend <session> <title>", 2)
		}
		args := newBody().textAlways("owner", owner).textAlways("title", title)
		args.flagText(c.opt, "what")
		args.flagText(c.opt, "id")
		args.number("timeout", 600)
		answer, code := c.call(ctx, "depend", args)
		if code != 0 {
			return code
		}
		c.say(c.resultJSON(answer))
		woke, _ := fieldOf(answer.result, "woke")
		if woke == nil || woke.Kind() == jsfmt.KindNull || kindOf(woke) == "event" {
			return 0
		}
		return 124
	case "handoff":
		to, ok := c.shift()
		if !ok {
			return c.die("handoff <to> <msg>", 2)
		}
		msg := c.rest()
		if msg == "" {
			return c.die("handoff <to> <msg>", 2)
		}
		args := newBody().textAlways("to", to).textAlways("msg", msg)
		args.flagText(c.opt, "title")
		args.flagList(c.opt, "after")
		if c.opt.has("no_wait") {
			args.raw("wait", jsfmt.Bool(false))
		}
		args.number("timeout", 600)
		answer, code := c.call(ctx, "handoff", args)
		if code != 0 {
			return code
		}
		c.say(c.resultJSON(answer))
		woke, _ := fieldOf(answer.result, "woke")
		if woke == nil || woke.Kind() == jsfmt.KindNull || kindOf(woke) == "event" {
			return 0
		}
		if kindOf(woke) == "message" {
			return 3
		}
		return 124
	case "ask":
		to, ok := c.shift()
		if !ok {
			return c.die("ask <session> <question>", 2)
		}
		msg := c.rest()
		if msg == "" {
			return c.die("ask <session> <question>", 2)
		}
		timeout := 600.0
		if v, ok := c.opt.text("timeout"); ok {
			timeout = jsNumber(v)
		}
		answer, code := c.call(ctx, "ask_wait", newBody().textAlways("to", to).textAlways("msg", msg).number("timeout", timeout))
		if code != 0 {
			return code
		}
		if reply, present := fieldOf(answer.result, "answer"); present {
			if reply != nil && reply.Kind() == jsfmt.KindString {
				c.say(reply.Text())
			} else if reply == nil || reply.Kind() == jsfmt.KindNull {
				c.say("null")
			} else {
				c.say(reply.EncodeJSON())
			}
			return 0
		}
		c.say(c.resultJSON(answer))
		return 124
	case "remember":
		kind, kindOK := c.shift()
		title, titleOK := c.shift()
		if !kindOK || !titleOK || len(c.pos) == 0 {
			return c.die("remember <kind> <title> <body>", 2)
		}
		args := newBody().textAlways("kind", kind).textAlways("title", title).textAlways("body", strings.Join(c.pos, " "))
		args.flagList(c.opt, "tags")
		args.flagList(c.opt, "refs")
		args.flagText(c.opt, "task")
		if v, ok := c.opt.text("supersedes"); ok && v != "" {
			args.raw("supersedes", jsNumberOrNull(v))
		}
		args.flagText(c.opt, "scope")
		args.bool("force", c.opt.has("force"))
		return c.op(ctx, "remember", args)
	case "verify":
		idArg, ok := c.shift()
		if !ok {
			return c.die("verify <id> [--undo]", 2)
		}
		return c.op(ctx, "verify", newBody().raw("id", jsNumberOrNull(idArg)).bool("undo", c.opt.has("undo")))
	case "share":
		idArg, ok := c.shift()
		if !ok {
			return c.die("share <id>", 2)
		}
		return c.op(ctx, "share", newBody().raw("id", jsNumberOrNull(idArg)))
	case "knowledge":
		if first, ok := c.shift(); !ok || first != "export" {
			return c.die("knowledge export [--verified]", 2)
		}
		return c.knowledge(ctx)
	case "recall":
		args := newBody().textAlways("q", c.rest())
		args.flagText(c.opt, "kind")
		args.flagText(c.opt, "tag")
		if v, ok := c.opt.text("limit"); ok && v != "" {
			args.raw("limit", jsNumberOrNull(v))
		}
		return c.op(ctx, "recall", args)
	case "kb":
		idArg, ok := c.shift()
		if !ok {
			return c.die("kb <id>", 2)
		}
		return c.op(ctx, "kb", newBody().raw("id", jsNumberOrNull(idArg)))
	case "task":
		idArg, ok := c.shift()
		if !ok {
			return c.die("task <id>", 2)
		}
		return c.op(ctx, "task", newBody().textAlways("id", idArg).flagValue(c.opt, "json"))
	case "pause", "resume":
		target, ok := c.shift()
		if !ok {
			return c.die(cmd+" <session>", 2)
		}
		return c.op(ctx, cmd, newBody().textAlways("target", target).textAlways("why", c.rest()))
	case "pass":
		to, ok := c.shift()
		if !ok {
			return c.die("pass <to> [msg]", 2)
		}
		return c.op(ctx, "pass", newBody().textAlways("to", to).textAlways("msg", c.rest()))
	case "leave":
		return c.op(ctx, "leave", newBody().textAlways("summary", c.rest()))
	case "reserve":
		if len(c.pos) == 0 {
			return c.die("reserve <path…> [--ttl 60] [--note …]", 2)
		}
		targets := make([]Target, 0, len(c.pos))
		for _, given := range c.pos {
			targets = append(targets, TargetOf(ctx, c.w, given, c.w.dir(), originBudgetMS))
		}
		code := 0
		for _, g := range groupsOf(targets) {
			args := newBody().rawList("paths", g.Paths)
			if v, ok := c.opt.text("ttl_minutes"); ok {
				args.raw("ttl_minutes", jsNumberOrNull(v))
			} else if v, ok := c.opt.text("ttl"); ok {
				args.raw("ttl_minutes", jsNumberOrNull(v))
			}
			args.flagText(c.opt, "note")
			if g.Repo != "" {
				args.textAlways("repo", g.Repo)
				args.textAlways("root", g.Root)
			}
			if got := c.op(ctx, "reserve", args); got != 0 {
				code = got
			}
		}
		return code
	case "release":
		args := newBody()
		if len(c.pos) > 0 {
			paths := make([]string, 0, len(c.pos))
			for _, given := range c.pos {
				paths = append(paths, resolveIn(c.w.dir(), given))
			}
			args.rawList("paths", paths)
		}
		return c.op(ctx, "release", args)
	case "reservations":
		args := newBody().bool("mine", c.opt.has("mine"))
		return c.op(ctx, "reservations", args)
	case "ops":
		return c.ops(ctx)
	default:
		return c.op(ctx, strings.ReplaceAll(cmd, "-", "_"), c.defaultArgs())
	}
}

// flagOr answers the flag's value when it took one, else the fallback.
func flagOr(f *flags, key, fallback string) string {
	if v, ok := f.text(key); ok {
		return v
	}
	return fallback
}

// body builds one operation's JSON body in the field order the command spells them.
type body struct {
	keys   []string
	fields []*jsfmt.Value
}

// newBody starts an empty body.
func newBody() *body { return &body{} }

// text appends a string field when the value is not empty.
func (b *body) text(key, value string) *body {
	if value == "" {
		return b
	}
	return b.textAlways(key, value)
}

// textAlways appends a string field, even when the value is empty.
func (b *body) textAlways(key, value string) *body {
	b.keys = append(b.keys, key)
	b.fields = append(b.fields, jsfmt.String(value))
	return b
}

// bool appends a boolean field, whatever it is.
func (b *body) bool(key string, value bool) *body {
	b.keys = append(b.keys, key)
	b.fields = append(b.fields, jsfmt.Bool(value))
	return b
}

// number appends a number field, whatever it is.
func (b *body) number(key string, value float64) *body {
	b.keys = append(b.keys, key)
	b.fields = append(b.fields, jsfmt.Number(value))
	return b
}

// raw appends any value, even null or false: the server expects the key.
func (b *body) raw(key string, value *jsfmt.Value) *body {
	b.keys = append(b.keys, key)
	b.fields = append(b.fields, value)
	return b
}

// rawList appends an array field, even empty.
func (b *body) rawList(key string, values []string) *body {
	items := make([]*jsfmt.Value, 0, len(values))
	for _, value := range values {
		items = append(items, jsfmt.String(value))
	}
	return b.raw(key, jsfmt.Array(items))
}

// flagText appends the flag's value when it took one.
func (b *body) flagText(f *flags, key string) *body {
	if v, ok := f.text(key); ok {
		return b.textAlways(key, v)
	}
	return b
}

// flagList appends the flag's comma list when it took one.
func (b *body) flagList(f *flags, key string) *body {
	if v, ok := f.text(key); ok {
		return b.rawList(key, splitList(v))
	}
	return b
}

// flagValue appends the flag as the operation's own --json style: true when bare, its text when
// it took one.
func (b *body) flagValue(f *flags, key string) *body {
	one, ok := f.value(key)
	if !ok {
		return b
	}
	if one.isText {
		return b.textAlways(key, one.value)
	}
	return b.raw(key, jsfmt.Bool(true))
}

// build answers the body as a JSON value.
func (b *body) build() *jsfmt.Value { return jsfmt.Object(b.keys, b.fields) }

// answer is one operation's answer: its result and its one-line text, if any.
type answer struct {
	result  *jsfmt.Value
	text    string
	hasText bool
}

// op runs one operation and prints what it answers; the returned int is the exit code.
func (c *cli) op(ctx context.Context, op string, args *body) int {
	got, code := c.call(ctx, op, args)
	if code != 0 {
		return code
	}
	c.printAnswer(got)
	return 0
}

// call posts one channel operation, exactly as the CLI would: this session's channel, its own
// name, and --as merged last. A transport failure or the server's refusal ends the command.
func (c *cli) call(ctx context.Context, op string, args *body) (answer, int) {
	if args == nil {
		args = newBody()
	}
	if c.as != "" {
		args = &body{keys: append(append([]string{}, args.keys...), "as"),
			fields: append(append([]*jsfmt.Value{}, args.fields...), jsfmt.String(c.as))}
	}
	callURL := fmt.Sprintf("%s/api/c/%s/op/%s?as=%s", c.url, url.PathEscape(c.ch), op, queryEscape(c.me))
	resp, err := HFetch(ctx, c.w, http.MethodPost, callURL,
		map[string]string{"content-type": "application/json"}, []byte(args.build().EncodeJSON()), 0, "")
	if err != nil {
		return answer{}, c.die(fmt.Sprintf("Huddle unreachable at %s (start it: huddle up)", c.url), 5)
	}
	got, failure := decodeAnswer(resp)
	if failure != "" {
		return answer{}, c.die(c.refusal(op, failure, resp.StatusCode), exitOf(resp.StatusCode))
	}
	return got, 0
}

// decodeAnswer reads one response the CLI called itself: its result and text, or the failure.
func decodeAnswer(resp *http.Response) (answer, string) {
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	value, err := jsfmt.DecodeValue(raw)
	if err != nil {
		value = jsfmt.Object(nil, nil)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return answer{}, errorMessage(value, resp.StatusCode)
	}
	if failure, ok := strField(value, "error"); ok {
		return answer{}, failure
	}
	got := answer{}
	if result, ok := fieldOf(value, "result"); ok {
		got.result = result
	}
	if text, ok := fieldOf(value, "text"); ok && text.Kind() == jsfmt.KindString {
		got.text, got.hasText = text.Text(), true
	}
	return got, ""
}

// refusal spells one server refusal: not knowing the operation is the CLI not knowing the
// command.
func (c *cli) refusal(op, failure string, status int) string {
	if strings.HasPrefix(failure, "no operation ") && op == strings.ReplaceAll(c.command, "-", "_") {
		return fmt.Sprintf("no command or operation %q (huddle help lists them)", c.command)
	}
	return failure
}

// errorMessage answers what the server said, or the bare status.
func errorMessage(value *jsfmt.Value, status int) string {
	if failure, ok := strField(value, "error"); ok {
		return failure
	}
	return fmt.Sprintf("HTTP %d", status)
}

// exitOf maps a status to the CLI's exit code: 423 is a paused channel.
func exitOf(status int) int {
	if status == http.StatusLocked {
		return 4
	}
	return 2
}

// printAnswer prints the operation's one line, or its result as JSON.
func (c *cli) printAnswer(a answer) {
	if a.hasText {
		c.say(a.text)
		return
	}
	c.say(c.resultJSON(a))
}

// resultJSON renders an answer's result the way JSON.stringify would, compact (or indented one
// space with --pretty).
func (c *cli) resultJSON(a answer) string {
	if a.result == nil || a.result.Kind() == jsfmt.KindNull {
		return "null"
	}
	if c.pretty {
		return indentJSONWidth(a.result, 1)
	}
	return a.result.EncodeJSON()
}

// admin calls one of the creator's admin endpoints, with this session's credential.
func (c *cli) admin(ctx context.Context, method, path string, body []byte) (*jsfmt.Value, int) {
	if c.url == "" {
		return nil, c.die(noPort, 5)
	}
	resp, err := HFetch(ctx, c.w, method, c.url+path, map[string]string{"content-type": "application/json"}, body, 0, "")
	if err != nil {
		return nil, c.die(fmt.Sprintf("Huddle unreachable at %s (start it: huddle up)", c.url), 5)
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

// token runs the token create, list and delete branches.
func (c *cli) token(ctx context.Context) int {
	sub := "list"
	if next, ok := c.shift(); ok {
		sub = next
	}
	switch sub {
	case "create":
		return c.tokenCreate(ctx)
	case "list":
		value, code := c.admin(ctx, http.MethodGet, "/api/tokens", nil)
		if code != 0 {
			return code
		}
		items := arrayItems(value)
		if len(items) == 0 {
			c.say("no tokens")
			return 0
		}
		for _, item := range items {
			c.say(tokenLine(item))
		}
		return 0
	case "delete":
		idArg, ok := c.shift()
		if !ok {
			return c.die("token delete <id>", 2)
		}
		id := strings.SplitN(idArg, ".", 2)[0]
		if _, code := c.admin(ctx, http.MethodDelete, "/api/tokens/"+queryEscape(id), nil); code != 0 {
			return code
		}
		c.say("token " + id + " deleted")
		return 0
	default:
		return c.die("token create|list|delete", 2)
	}
}

// tokenCreate mints an invite, or prints the line another session pastes in to join with it.
func (c *cli) tokenCreate(ctx context.Context) int {
	if c.url == "" {
		return c.die(noPort, 5)
	}
	o := InviteOptions{SingleUse: c.opt.has("single_use"), CanInvite: c.opt.has("can_invite")}
	if v, ok := c.opt.text("ttl"); ok {
		seconds, problem := secondsOf(v)
		if problem != "" {
			return c.die(problem, 2)
		}
		ttl := int(seconds)
		o.TTL = &ttl
	}
	if c.ch != "" {
		o.Channel = c.ch
	}
	if v, ok := c.opt.text("description"); ok {
		o.Description = v
	}
	if c.opt.has("print_join_command") {
		line := JoinLine(ctx, c.w, c.url, o, "")
		if !line.OK {
			return c.die(line.Error, 2)
		}
		valid := "forever"
		if line.Expires != "" {
			valid = "until " + line.Expires
		}
		single := ""
		if o.SingleUse {
			single = ", single use"
		}
		c.say(fmt.Sprintf("join: %s   (valid %s%s)", line.Line, valid, single))
		return 0
	}
	invite := MintInvite(ctx, c.w, c.url, o, "")
	if !invite.OK {
		return c.die(invite.Error, 2)
	}
	expires := "never"
	if invite.Expires != "" {
		expires = invite.Expires
	}
	c.say(fmt.Sprintf("%s  (id %s, expires %s)", invite.Token, invite.ID, expires))
	return 0
}

// secondsOf parses a ttl: 24h, 90m, 3600, 0 = never. The string is the error message.
func secondsOf(v string) (float64, string) {
	m := ttlShape.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return 0, fmt.Sprintf("--ttl %s: a number with s, m, h or d (0 = never expires)", v)
	}
	n, _ := strconv.ParseFloat(m[1], 64)
	switch m[2] {
	case "m":
		return n * 60, ""
	case "h":
		return n * 3600, ""
	case "d":
		return n * 86400, ""
	default:
		return n, ""
	}
}

// tokenLine renders one listed token.
func tokenLine(item *jsfmt.Value) string {
	expires := textField(item, "expires")
	if expires == "" {
		expires = "never"
	}
	uses := "multi-use"
	if boolFieldText(item, "single_use") == "true" {
		uses = "single-use"
	}
	line := fmt.Sprintf("%s  expires %s  %s  uses %s", textField(item, "id"), expires, uses, scalarField(item, "uses"))
	if boolFieldText(item, "can_invite") == "true" {
		line += "  can-invite"
	}
	if channel := textField(item, "channel"); channel != "" {
		line += "  channel " + channel
	}
	if description := textField(item, "description"); description != "" {
		line += "  " + description
	}
	return line
}

// members lists who holds a credential.
func (c *cli) members(ctx context.Context) int {
	value, code := c.admin(ctx, http.MethodGet, "/api/members", nil)
	if code != 0 {
		return code
	}
	items := arrayItems(value)
	if len(items) == 0 {
		c.say("no members (the creator holds the root credential)")
		return 0
	}
	for _, item := range items {
		line := fmt.Sprintf("%s  since %s  last seen %s  invite %s",
			textField(item, "name"), textField(item, "since"), textField(item, "seen"), textField(item, "invite"))
		if boolFieldText(item, "can_invite") == "true" {
			line += "  can-invite"
		}
		c.say(line)
	}
	return 0
}

// kick revokes one member's credential.
func (c *cli) kick(ctx context.Context) int {
	name, ok := c.shift()
	if !ok {
		return c.die("kick <name>", 2)
	}
	if _, code := c.admin(ctx, http.MethodDelete, "/api/members/"+queryEscape(name), nil); code != 0 {
		return code
	}
	c.say(name + " kicked: its credential no longer works")
	return 0
}

// channel runs the channel remove branch.
func (c *cli) channel(ctx context.Context) int {
	sub, ok := c.shift()
	if !ok || sub != "rm" {
		return c.die("channel rm <name>", 2)
	}
	return c.channelRemove(ctx)
}

// channelRemove takes one channel off the hub (the creator only): its history stays on disk.
// Not found exits 4, any other refusal 1.
func (c *cli) channelRemove(ctx context.Context) int {
	name, ok := c.shift()
	if !ok {
		return c.die("channel rm <name>", 2)
	}
	if c.url == "" {
		return c.die(noPort, 5)
	}
	resp, err := HFetch(ctx, c.w, http.MethodDelete, c.url+"/api/channels/"+queryEscape(name),
		map[string]string{"content-type": "application/json"}, nil, 0, "")
	if err != nil {
		return c.die(fmt.Sprintf("Huddle unreachable at %s (start it: huddle up)", c.url), 5)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	value, decodeErr := jsfmt.DecodeValue(raw)
	if decodeErr != nil {
		value = jsfmt.Object(nil, nil)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		code := 1
		if resp.StatusCode == 404 {
			code = 4
		}
		return c.die(errorMessage(value, resp.StatusCode), code)
	}
	c.say(fmt.Sprintf("%s removed; its history is kept at %s", name, textField(value, "kept")))
	return 0
}

// open prints the dashboard link that signs a browser in once.
func (c *cli) open(ctx context.Context) int {
	if c.url == "" {
		return c.die(noPort, 5)
	}
	link := Dashboard(ctx, c.w, c.url, "", 1500)
	if link == "" {
		return c.die("this session is not in a huddle here: join with the command its owner gives you (/huddle:join <host:port> --token …)", 2)
	}
	c.say(fmt.Sprintf("dashboard: %s   (signs your browser in once, within 5 min)", link))
	return 0
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

// ops lists the server's tools, the name padded.
func (c *cli) ops(ctx context.Context) int {
	callURL := fmt.Sprintf("%s/mcp/%s?as=%s", c.url, url.PathEscape(c.ch), queryEscape(c.me))
	resp, err := HFetch(ctx, c.w, http.MethodPost, callURL,
		map[string]string{"content-type": "application/json"},
		[]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`), 0, "")
	if err != nil {
		return c.die("unreachable", 5)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	value, decodeErr := jsfmt.DecodeValue(raw)
	if decodeErr != nil {
		return c.die("unreachable", 5)
	}
	result, _ := fieldOf(value, "result")
	tools, _ := fieldOf(result, "tools")
	for _, tool := range arrayItems(tools) {
		c.say(fmt.Sprintf("%-12s %s", textField(tool, "name"), textField(tool, "description")))
	}
	return 0
}

// knowledge export writes the markdown as it came.
func (c *cli) knowledge(ctx context.Context) int {
	path := fmt.Sprintf("%s/api/c/%s/knowledge.md", c.url, url.PathEscape(c.ch))
	if c.opt.has("verified") {
		path += "?verified=1"
	}
	resp, err := HFetch(ctx, c.w, http.MethodGet, path, nil, nil, 0, "")
	if err != nil {
		return c.die(fmt.Sprintf("Huddle unreachable at %s (start it: huddle up)", c.url), 5)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		value, decodeErr := jsfmt.DecodeValue(raw)
		message := fmt.Sprintf("HTTP %d", resp.StatusCode)
		if decodeErr == nil {
			message = errorMessage(value, resp.StatusCode)
		}
		return c.die(message, 2)
	}
	_, _ = c.stdout.Write(raw)
	return 0
}

// wait blocks until an event, a message for this session, or the timeout: forever means
// successive bounded waits, so no proxy or idle timer can cut one silently.
func (c *cli) wait(ctx context.Context) int {
	timeout := 0.0
	if v, ok := c.opt.text("timeout"); ok {
		timeout = jsNumber(v)
	}
	topics := c.pos
	if len(topics) == 0 {
		if v, ok := c.opt.text("topics"); ok {
			topics = splitList(v)
		} else {
			topics = []string{}
		}
	}
	forever := !(timeout > 0)
	var end time.Time
	if !forever {
		end = c.w.now().Add(time.Duration(timeout * float64(time.Second)))
	}
	for {
		left := 600.0
		if !forever {
			left = math.Max(1, math.Ceil(end.Sub(c.w.now()).Seconds()))
		}
		got, code := c.call(ctx, "wait", newBody().rawList("topics", topics).number("timeout", math.Min(600, left)))
		if code != 0 {
			return code
		}
		if kindOf(got.result) == "timeout" && (forever || c.w.now().Before(end)) {
			if ctx.Err() != nil {
				return 0
			}
			continue
		}
		c.say(c.resultJSON(got))
		switch kindOf(got.result) {
		case "event":
			return 0
		case "message":
			return 3
		default:
			return 124
		}
	}
}

// gate waits while the channel is paused. With --no-block it answers at once; without it, an
// unreachable Huddle counts as paused (fail closed) and the wait goes on.
func (c *cli) gate(ctx context.Context) int {
	if c.opt.has("no_block") {
		got, code := c.call(ctx, "gate", newBody().bool("block", false))
		if code != 0 {
			return code
		}
		c.say(c.resultJSON(got))
		if textField(got.result, "control") == "run" {
			return 0
		}
		return 4
	}
	args := newBody().number("timeout", 600)
	if c.as != "" {
		args.textAlways("as", c.as)
	}
	callURL := fmt.Sprintf("%s/api/c/%s/op/gate?as=%s", c.url, url.PathEscape(c.ch), queryEscape(c.me))
	for {
		var value *jsfmt.Value
		if resp, err := HFetch(ctx, c.w, http.MethodPost, callURL,
			map[string]string{"content-type": "application/json"}, []byte(args.build().EncodeJSON()), 0, ""); err == nil {
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if decoded, decodeErr := jsfmt.DecodeValue(raw); decodeErr == nil {
				value = decoded
			}
		}
		if value != nil {
			if result, ok := fieldOf(value, "result"); ok {
				if textField(result, "control") == "run" {
					c.say(result.EncodeJSON())
					return 0
				}
			}
			if failure, ok := strField(value, "error"); ok {
				return c.die(failure, 2)
			}
		} else {
			fmt.Fprintln(c.stderr, "huddle: unreachable; treated as paused, retrying in 5 s")
		}
		select {
		case <-ctx.Done():
			return 0
		case <-time.After(5 * time.Second):
		}
	}
}

// defaultArgs builds the body of an operation called by name: --key value pairs, numbers and
// booleans parsed, the list fields split, the plain words as the message.
func (c *cli) defaultArgs() *body {
	args := newBody()
	for _, one := range c.opt.order {
		switch {
		case !one.isText:
			args.raw(one.key, jsfmt.Bool(true))
		case wholeNumber.MatchString(one.value):
			args.raw(one.key, jsfmt.Number(jsNumber(one.value)))
		case one.value == "true":
			args.raw(one.key, jsfmt.Bool(true))
		case one.value == "false":
			args.raw(one.key, jsfmt.Bool(false))
		default:
			args.textAlways(one.key, one.value)
		}
	}
	for _, key := range []string{"topics", "after", "tags", "refs", "members"} {
		if at := indexOf(args.keys, key); at >= 0 && args.fields[at].Kind() == jsfmt.KindString {
			args.fields[at] = jsfmt.Array(listValues(args.fields[at].Text()))
		}
	}
	if indexOf(args.keys, "msg") < 0 && len(c.pos) > 0 {
		args.textAlways("msg", c.rest())
	}
	return args
}

// listValues answers the strings of one comma list.
func listValues(v string) []*jsfmt.Value {
	items := []*jsfmt.Value{}
	for _, part := range splitList(v) {
		items = append(items, jsfmt.String(part))
	}
	return items
}

// jsNumberOrNull parses a number the way Number() does; anything unparsable is NaN, which
// JSON.stringify writes as null.
func jsNumberOrNull(v string) *jsfmt.Value {
	n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return nullValue()
	}
	return jsfmt.Number(n)
}

// kindOf answers a result's kind field.
func kindOf(result *jsfmt.Value) string {
	return textField(result, "kind")
}

// textField answers one string field.
func textField(value *jsfmt.Value, key string) string {
	if value == nil {
		return ""
	}
	text, _ := strField(value, key)
	return text
}

// boolFieldText answers one boolean field as its word.
func boolFieldText(value *jsfmt.Value, key string) string {
	if value == nil {
		return ""
	}
	field, ok := fieldOf(value, key)
	if !ok || field.Kind() != jsfmt.KindBool {
		return ""
	}
	return field.EncodeJSON()
}

// arrayItems answers an array value's items, or none.
func arrayItems(value *jsfmt.Value) []*jsfmt.Value {
	if value == nil || value.Kind() != jsfmt.KindArray {
		return nil
	}
	return value.Items()
}

// — listen — the channel and its "listen" channels, live and reconnecting.

// listen streams the channel and its listen channels until the context ends.
func (c *cli) listen(ctx context.Context) int {
	channels := []string{c.ch}
	if !c.raw.truthy("channel") {
		for _, extra := range c.id.Listen {
			if extra != c.ch {
				channels = append(channels, extra)
			}
		}
	}
	stateFile := ""
	if v, ok := c.opt.text("state"); ok {
		stateFile = v
	}
	state := loadListenState(stateFile)
	after := int64(0)
	if v, ok := c.opt.text("after"); ok {
		after = int64(jsNumber(v))
	}
	all := c.opt.has("all")
	// the followers write one stdout at once, and listen returns only once every one has
	// stopped: nothing writes to the caller's stdout after it answers
	followers := *c
	followers.stdout = &lockedWriter{w: c.stdout}
	ctx, stop := context.WithCancel(ctx)
	fatal := make(chan int, len(channels))
	var wg sync.WaitGroup
	defer wg.Wait()
	defer stop()
	for _, channel := range channels {
		wg.Add(1)
		safego.Go(func(line string) { fmt.Fprintln(c.stderr, line) }, func() {
			defer wg.Done()
			if code := followers.follow(ctx, channel, channels, after, state, all); code != 0 {
				fatal <- code
			}
		})
	}
	done := make(chan struct{})
	safego.Go(func(line string) { fmt.Fprintln(c.stderr, line) }, func() { wg.Wait(); close(done) })
	select {
	case code := <-fatal:
		return code
	case <-done:
		// a follower that failed and then stopped readies both cases at once; its code wins
		select {
		case code := <-fatal:
			return code
		default:
			return 0
		}
	case <-ctx.Done():
		return 0
	}
}

// lockedWriter lets several goroutines write one writer, a line at a time.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// follow keeps one channel's events coming: a live stream, a replay of what was missed, and a
// periodic re-read, reconnecting when the stream goes quiet. An owner's message to another
// member is never printed.
func (c *cli) follow(ctx context.Context, channel string, channels []string, after int64, state *listenState, all bool) int {
	var last int64
	if have, ok := state.get(channel); ok {
		last = have
	} else if len(channels) == 1 {
		last = after
	}
	connected := false
	print := func(event *jsfmt.Value) {
		seq := int64(numOf(event, "seq"))
		if seq <= last {
			return
		}
		last = seq
		state.set(channel, last)
		from := textField(event, "from")
		if from == c.me && !all {
			return
		}
		if c.me != "" && channelpkg.OwnerPrivate(from, textField(event, "to"), c.me) {
			return
		}
		fmt.Fprintln(c.stdout, withChannel(channel, event).EncodeJSON())
	}
	// several channels and no --after: each starts from its newest event
	if last == 0 && len(channels) > 1 && !c.opt.has("after") {
		if events, ok := c.rawTimeline(ctx, channel, 0, 1); ok && len(events) > 0 {
			last = int64(numOf(events[len(events)-1], "seq"))
		}
	}
	replay := func() {
		if last == 0 {
			return
		}
		if events, ok := c.rawTimeline(ctx, channel, last, 2000); ok {
			for _, event := range events {
				print(event)
			}
		}
	}
	resync := time.NewTicker(time.Duration(c.listenMS("HUDDLE_LISTEN_RESYNC_MS", 30_000)) * time.Millisecond)
	defer resync.Stop()
	safego.Go(func(line string) { fmt.Fprintln(c.stderr, line) }, func() {
		for {
			select {
			case <-resync.C:
				if connected {
					replay()
				}
			case <-ctx.Done():
				return
			}
		}
	})
	quiet := time.Duration(c.listenMS("HUDDLE_LISTEN_QUIET_MS", 45_000)) * time.Millisecond
	for {
		attemptCtx, cancel := context.WithCancel(ctx)
		var timer *time.Timer
		arm := func() {
			if timer == nil {
				timer = time.AfterFunc(quiet, cancel)
				return
			}
			timer.Reset(quiet)
		}
		err := c.stream(attemptCtx, channel, all, arm, print, replay, &connected)
		if timer != nil {
			timer.Stop()
		}
		cancel()
		if ctx.Err() != nil {
			return 0
		}
		// a stream that never connected means no Huddle to listen to
		if err != nil && !connected {
			return c.die(fmt.Sprintf("Huddle unreachable at %s (start it: huddle up)", c.url), 5)
		}
		select {
		case <-ctx.Done():
			return 0
		case <-time.After(2 * time.Second):
		}
	}
}

// listenMS reads one of listen's millisecond knobs.
func (c *cli) listenMS(key string, fallback int) int {
	if v := c.w.getenv(key); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

// stream reads one live stream until it ends, printing its events. arm re-arms the quiet timer
// on every chunk: a stream can go quiet without closing.
func (c *cli) stream(ctx context.Context, channel string, all bool, arm func(), print func(*jsfmt.Value), replay func(), connected *bool) error {
	live := fmt.Sprintf("%s/api/c/%s/live", c.url, url.PathEscape(channel))
	if !all {
		live += "?as=" + queryEscape(c.me)
	}
	resp, err := HFetch(ctx, c.w, http.MethodGet, live, map[string]string{"accept": "text/event-stream"}, nil, 0, "")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	*connected = true
	arm()
	reader := bufio.NewReaderSize(resp.Body, 8192)
	var block []string
	for {
		line, err := reader.ReadString('\n')
		arm()
		if content := strings.TrimRight(line, "\r\n"); content != "" || len(line) > 0 {
			if content == "" { // the blank line ends one event block
				c.sseBlock(strings.Join(block, "\n"), all, print, replay)
				block = nil
			} else {
				block = append(block, content)
			}
		}
		if err != nil {
			if len(block) > 0 {
				c.sseBlock(strings.Join(block, "\n"), all, print, replay)
			}
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

// sseBlock handles one server-sent event block: its data lines joined, hello replaying what was
// missed, an event printed, anything else printed only with --all.
func (c *cli) sseBlock(block string, all bool, print func(*jsfmt.Value), replay func()) {
	data := []string{}
	for _, line := range strings.Split(block, "\n") {
		if strings.HasPrefix(line, "data: ") {
			data = append(data, strings.TrimPrefix(line, "data: "))
		}
	}
	if len(data) == 0 {
		return
	}
	value, err := jsfmt.DecodeValue([]byte(strings.Join(data, "\n")))
	if err != nil {
		return
	}
	switch textField(value, "type") {
	case "hello":
		replay()
	case "event":
		if event, ok := fieldOf(value, "data"); ok {
			print(event)
		}
	default:
		if all {
			fmt.Fprintln(c.stdout, value.EncodeJSON())
		}
	}
}

// rawTimeline reads a channel's events after a seq, as they came.
func (c *cli) rawTimeline(ctx context.Context, channel string, after int64, limit int) ([]*jsfmt.Value, bool) {
	callURL := fmt.Sprintf("%s/api/c/%s/timeline?after=%d&limit=%d", c.url, url.PathEscape(channel), after, limit)
	resp, err := HFetch(ctx, c.w, http.MethodGet, callURL, nil, nil, 0, "")
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false
	}
	raw, _ := io.ReadAll(resp.Body)
	value, err := jsfmt.DecodeValue(raw)
	if err != nil || value.Kind() != jsfmt.KindArray {
		return nil, false
	}
	return value.Items(), true
}

// numOf answers one number field of a value.
func numOf(value *jsfmt.Value, key string) float64 {
	if value == nil {
		return 0
	}
	field, ok := fieldOf(value, key)
	if !ok || field.Kind() != jsfmt.KindNumber {
		return 0
	}
	return numberValue(field)
}

// withChannel prefixes an event with the channel it came from, the way a spread keeps the
// event's own keys and order (an event naming its own channel keeps that value, in front).
func withChannel(channel string, event *jsfmt.Value) *jsfmt.Value {
	if event == nil || event.Kind() != jsfmt.KindObject {
		return jsfmt.Object([]string{"channel"}, []*jsfmt.Value{jsfmt.String(channel)})
	}
	keys := []string{"channel"}
	fields := []*jsfmt.Value{jsfmt.String(channel)}
	for i, key := range event.Keys() {
		if key == "channel" {
			fields[0] = event.Fields()[i]
			continue
		}
		keys = append(keys, key)
		fields = append(fields, event.Fields()[i])
	}
	return jsfmt.Object(keys, fields)
}

// listenState is where each channel's last shown seq is kept, so a restarted listen resumes
// where the last one stopped (--state <file>). The keys keep their arrival order.
type listenState struct {
	// one follower per channel reads and writes the state at once
	mu   sync.Mutex
	file string
	keys []string
	seqs map[string]int64
}

// loadListenState reads the state file; anything unreadable starts empty.
func loadListenState(file string) *listenState {
	state := &listenState{file: file, seqs: map[string]int64{}}
	if file == "" {
		return state
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return state
	}
	value, err := jsfmt.DecodeValue(raw)
	if err != nil || value.Kind() != jsfmt.KindObject {
		return state
	}
	for i, key := range value.Keys() {
		if field := value.Fields()[i]; field.Kind() == jsfmt.KindNumber {
			state.keys = append(state.keys, key)
			state.seqs[key] = int64(numberValue(field))
		}
	}
	return state
}

// get answers one channel's last shown seq.
func (s *listenState) get(channel string) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seq, ok := s.seqs[channel]
	return seq, ok
}

// set records one channel's last shown seq and writes the state file.
func (s *listenState) set(channel string, seq int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, known := s.seqs[channel]; !known {
		s.keys = append(s.keys, channel)
	}
	s.seqs[channel] = seq
	if s.file == "" {
		return
	}
	keys := append([]string{}, s.keys...)
	fields := make([]*jsfmt.Value, 0, len(keys))
	for _, key := range keys {
		fields = append(fields, jsfmt.Number(float64(s.seqs[key])))
	}
	_ = os.WriteFile(s.file, []byte(jsfmt.Object(keys, fields).EncodeJSON()), 0o644)
}
