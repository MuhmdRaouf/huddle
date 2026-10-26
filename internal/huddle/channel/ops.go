// Every operation a session (or the owner) can perform, once. The HTTP API, the MCP tools and
// the huddle CLI are all this table, so they cannot drift. Each op names whether it can block,
// whether it is the owner's or the orchestrator's, and how it renders as text for an agent's
// context.
package channel

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

// OpResult is one op's outcome: its result, and its text for an agent's context (nil when the
// op renders none or the caller asked for JSON, so the wire carries null and the caller falls
// back to the result).
type OpResult struct {
	Result any
	Text   *string
}

// StartResult is the start workflow's answer.
type StartResult struct {
	Started bool      `json:"started"`
	Paused  bool      `json:"paused,omitempty"`
	Done    bool      `json:"done,omitempty"`
	Hint    string    `json:"hint,omitempty"`
	Task    *TaskView `json:"task,omitempty"`
}

// ReleasedItem is one task a finish released.
type ReleasedItem struct {
	Task *string `json:"task"`
	To   *string `json:"to"`
}

// FinishResult is the finish workflow's answer.
type FinishResult struct {
	Done      string         `json:"done"`
	Released  []ReleasedItem `json:"released"`
	Knowledge *int64         `json:"knowledge"`
	Next      *FinishNext    `json:"next"`
}

// FinishNext is the session's next task after a finish.
type FinishNext struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	Ready bool     `json:"ready"`
	Unmet []string `json:"unmet"`
}

// DependResult is the depend workflow's answer.
type DependResult struct {
	Created string   `json:"created"`
	Blocked string   `json:"blocked"`
	Waiting *bool    `json:"waiting,omitempty"`
	Woke    *WaitHit `json:"woke,omitempty"`
	Hint    string   `json:"hint,omitempty"`
}

// HandoffResult is the handoff workflow's answer.
type HandoffResult struct {
	Passed int64    `json:"passed"`
	Task   *string  `json:"task"`
	Woke   *WaitHit `json:"woke,omitempty"`
}

// AskWaitResult is the ask_wait workflow's answer.
type AskWaitResult struct {
	Asked     int64  `json:"asked"`
	Answer    string `json:"answer,omitempty"`
	From      string `json:"from,omitempty"`
	Seq       int64  `json:"seq,omitempty"`
	Timeout   bool   `json:"timeout,omitempty"`
	Cancelled bool   `json:"cancelled,omitempty"`
	Hint      string `json:"hint,omitempty"`
}

// opCall is the op's channel and who it acts as.
type opCall struct {
	ch          *Channel
	me          string
	waitDefault int
}

// opDef is one operation: its description and input schema for the tool list, its required
// arguments, whether it is the owner's or the orchestrator's, how it runs and how it renders.
type opDef struct {
	desc     string
	props    *jsfmt.Value
	required []string
	blocks   bool
	owner    bool
	hook     bool
	run      func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error)
	text     func(r any, c opCall) string
}

func argOf(a *jsfmt.Value, k string) *jsfmt.Value {
	if a == nil {
		return nil
	}
	return objField(a, k)
}

func argStr(a *jsfmt.Value, k string) string { return strOf(argOf(a, k)) }

func argStrPtr(a *jsfmt.Value, k string) *string { return strPtrOf(argOf(a, k)) }

func argBoolPtr(a *jsfmt.Value, k string) *bool {
	f := argOf(a, k)
	if f == nil || f.Kind() != jsfmt.KindBool {
		return nil
	}
	b := f.EncodeJSON() == "true"
	return &b
}

func argIntPtr(a *jsfmt.Value, k string) *int64 {
	f := argOf(a, k)
	if f == nil {
		return nil
	}
	if f.Kind() == jsfmt.KindNumber {
		if n, err := strconv.ParseInt(f.EncodeJSON(), 10, 64); err == nil {
			return &n
		}
	}
	if f.Kind() == jsfmt.KindString {
		if n, err := strconv.ParseInt(f.Text(), 10, 64); err == nil {
			return &n
		}
	}
	return nil
}

func argStrs(a *jsfmt.Value, k string) []string { return strsOf(argOf(a, k)) }

// argID reads one id argument as a number, 0 for anything that is not one.
func argID(a *jsfmt.Value, k string) int64 {
	if v := argIntPtr(a, k); v != nil {
		return *v
	}
	return 0
}

// jsTruthy answers the argument's JavaScript truthiness: a present boolean, a non-empty string or
// a non-zero number.
func jsTruthy(v *jsfmt.Value) bool {
	if v == nil || v.Kind() == jsfmt.KindNull {
		return false
	}
	switch v.Kind() {
	case jsfmt.KindBool:
		return v.EncodeJSON() == "true"
	case jsfmt.KindString:
		return v.Text() != ""
	case jsfmt.KindNumber:
		return v.EncodeJSON() != "0"
	}
	return true
}

// argAny reads one argument as an untyped value (a string, or nothing).
func argAny(a *jsfmt.Value, k string) any {
	f := argOf(a, k)
	if f == nil || f.Kind() == jsfmt.KindNull {
		return nil
	}
	if f.Kind() == jsfmt.KindString {
		return f.Text()
	}
	return f.EncodeJSON()
}

// opTimeout is an op's wait budget: the caller's timeout argument clamped to one hour, or the
// transport's default.
func opTimeout(c opCall, a *jsfmt.Value) int {
	n := int64(c.waitDefault)
	if v := argIntPtr(a, "timeout"); v != nil {
		n = *v
	}
	return int(max(0, min(3600, n)))
}

// waitsOnHint lists what a task waits on, the way an agent reads it.
func waitsOnHint(t *TaskView) string {
	var parts []string
	for _, u := range t.Unmet {
		owner := "unowned"
		if u.Owner != nil {
			owner = *u.Owner
		}
		parts = append(parts, fmt.Sprintf("%s (%s, %s)", u.ID, owner, u.Status))
	}
	return fmt.Sprintf("waits on %s: wait with topics [\"task.ready\"]", strings.Join(parts, ", "))
}

// NextDone says a session's plan order holds no task it could take.
type NextDone struct {
	Done bool `json:"done"`
}

// opsOrder lists the operation table in the order the tool list shows.
var opsOrder = []string{
	"join", "status", "leave", "remove", "sessions", "state", "publish", "send", "reply", "inbox", "events",
	"wait", "ack", "gate", "pause", "resume", "turn", "pass", "take", "next", "task", "map",
	"tasks", "task_create", "task_update", "task_status", "wait_task", "note",
	"start", "finish", "depend", "handoff", "ask_wait",
	"remember", "recall", "kb", "verify", "share", "digest",
	"configure", "import_plan", "approve", "note_edit", "assign", "brief",
	"reserve", "release", "reservations",
	"touched",
}

var ops = map[string]opDef{
	"join": {
		desc: "Join the channel (or resume). Returns who else is here, the turn, your pause state, your inbox, your next task and the latest shared knowledge, plus either your unread events (context sync) or a brief instead of the backlog (context fresh). Call first.",
		props: jsfmt.Object([]string{"role", "label", "task", "context"}, []*jsfmt.Value{
			sProp("one line: what you do here"), sProp(""), sProp("what you are doing now"),
			enProp(Contexts, "sync: everything unread since you left; fresh: skip the backlog, get a brief (open asks are kept). Omit it: fresh on a first join, else what the orchestrator set for you, else sync"),
		}),
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			return c.ch.Join(ctx, c.me, JoinOpts{
				Label: argStrPtr(a, "label"), Role: argStrPtr(a, "role"), Task: argStrPtr(a, "task"), Context: argStrPtr(a, "context"),
			})
		},
		text: func(r any, c opCall) string { return snapText(r.(Snapshot)) },
	},
	"status": {
		desc: "The same picture as join, without changing presence.",
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			return c.ch.Snapshot(ctx, c.me, SnapshotJoin{})
		},
		text: func(r any, c opCall) string { return snapText(r.(Snapshot)) },
	},
	"state": {
		desc:     "Say what you are doing (shown live to the owner and the other sessions).",
		props:    jsfmt.Object([]string{"state", "task", "step"}, []*jsfmt.Value{enProp(States, ""), sProp(""), sProp("task id")}),
		required: []string{"state"},
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			step := argStrPtr(a, "step")
			return c.ch.Presence(ctx, c.me, PresenceInput{
				State: argStrPtr(a, "state"), Task: argStrPtr(a, "task"), SetStep: step != nil, Step: step,
			})
		},
	},
	"leave": {
		desc:  "Leave the channel when your work is finished (subagents: always, with a one-line summary).",
		props: jsfmt.Object([]string{"summary"}, []*jsfmt.Value{sProp("")}),
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			return c.ch.Leave(ctx, c.me, argStr(a, "summary"))
		},
	},
	"remove": {
		desc: "Owner only: remove a session (and its subagents) from the channel; it stays in the history, out of the live views.",
		props: jsfmt.Object([]string{"target", "why"}, []*jsfmt.Value{
			sProp(""), sProp(""),
		}),
		required: []string{"target"},
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			return c.ch.Remove(ctx, c.me, argStr(a, "target"), argStr(a, "why"))
		},
	},
	"sessions": {
		desc: "Every session and subagent: state, task, pause, unread, open asks, who holds the turn.",
		run:  func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) { return c.ch.Sessions(ctx) },
	},
	"publish": {
		desc: "Publish an event on the channel (to everyone, or to one session with `to`). Durable; wakes whoever waits on the topic. key makes a retry idempotent.",
		props: jsfmt.Object([]string{"topic", "msg", "to", "ref", "data", "ask", "key"}, []*jsfmt.Value{
			sProp("e.g. finding.ready, build.ready"), sProp(""), sProp("one session; omit for everyone"),
			sProp("file or id the event is about"), oProp(),
			bProp("needs a reply: lands in the recipient's inbox"), sProp("idempotency key"),
		}),
		required: []string{"topic", "msg"},
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			return c.ch.Publish(ctx, c.me, PublishArgs{
				Topic: argStr(a, "topic"), Msg: argStr(a, "msg"), To: argStrPtr(a, "to"),
				Ref: argStrPtr(a, "ref"), Data: argOf(a, "data"),
				NeedsReply: jsTruthy(argOf(a, "ask")), Key: argStrPtr(a, "key"),
			})
		},
	},
	"send": {
		desc: "Message one session (to) or everyone. ask=true needs a reply: it sits in their inbox and wakes their wait until they answer.",
		props: jsfmt.Object([]string{"to", "msg", "ask", "task", "key"}, []*jsfmt.Value{
			sProp("session name; omit for everyone"), sProp(""), bProp(""), sProp("task id it is about"), sProp(""),
		}),
		required: []string{"msg"},
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			return c.ch.Send(ctx, c.me, argStrPtr(a, "to"), argStr(a, "msg"), SendOpts{
				Ask: jsTruthy(argOf(a, "ask")), Task: argStrPtr(a, "task"), Key: argStrPtr(a, "key"),
			})
		},
	},
	"reply": {
		desc:     "Answer a message (by seq). Closes it in your inbox.",
		props:    jsfmt.Object([]string{"seq", "msg"}, []*jsfmt.Value{iProp(""), sProp("")}),
		required: []string{"seq", "msg"},
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			return c.ch.Reply(ctx, c.me, argID(a, "seq"), argStr(a, "msg"))
		},
	},
	"inbox": {
		desc: "Messages waiting for your reply (asks from sessions, the owner's directives).",
		run:  func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) { return c.ch.Inbox(ctx, c.me) },
	},
	"events": {
		desc: "Read the channel history (newest last).",
		props: jsfmt.Object([]string{"after", "limit", "topic", "from"}, []*jsfmt.Value{
			iProp(""), iProp(""), sProp("glob"), sProp(""),
		}),
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			return c.ch.Events(ctx, EventsFilter{
				After: argIntPtr(a, "after"), Limit: argIntPtr(a, "limit"),
				Topic: argStr(a, "topic"), From: argStrPtr(a, "from"), Reader: c.me,
			})
		},
	},
	"wait": {
		desc: "Block until (1) a message needs your reply → kind=message, (2) the first unread event for you (to you or to everyone) matching a topic glob → kind=event (handle it and its skipped list, which includes messages between other sessions, then ack its seq), or (3) timeout → call wait again. Use this to stop and listen while another session works.",
		props: jsfmt.Object([]string{"topics", "timeout"}, []*jsfmt.Value{
			arrProp("globs, e.g. task.ready build.* turn.pass; empty = any"),
			iProp("seconds; omit it: the default waits as long as the transport allows, and a timeout only means call wait again"),
		}),
		blocks: true,
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			topics := argStrs(a, "topics")
			if topics == nil {
				topics = []string{}
			}
			return c.ch.Wait(ctx, c.me, topics, opTimeout(c, a))
		},
	},
	"ack": {
		desc:     "Mark everything up to seq as handled (your cursor; never moves back).",
		props:    jsfmt.Object([]string{"seq"}, []*jsfmt.Value{iProp("")}),
		required: []string{"seq"},
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			return c.ch.Ack(ctx, c.me, argID(a, "seq"))
		},
	},
	"gate": {
		desc:   "Check the pause. block=true (default) waits until you are resumed. Call before any action that changes things; while paused, publish/task changes are refused.",
		props:  jsfmt.Object([]string{"block", "timeout"}, []*jsfmt.Value{bProp(""), iProp("")}),
		blocks: true,
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			if block := argBoolPtr(a, "block"); block != nil && !*block {
				control, err := c.ch.ControlOf(ctx, c.me)
				if err != nil {
					return nil, err
				}
				return struct {
					Control string `json:"control"`
				}{control}, nil
			}
			timeout := 0
			if v := argIntPtr(a, "timeout"); v != nil {
				timeout = int(*v)
			}
			return c.ch.Gate(ctx, c.me, timeout)
		},
	},
	"pause": {
		desc:     "Pause another session (it stops at its next gate; its writes are refused until resume).",
		props:    jsfmt.Object([]string{"target", "why"}, []*jsfmt.Value{sProp(""), sProp("")}),
		required: []string{"target"},
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			return c.ch.Control(ctx, c.me, argStr(a, "target"), "pause", argStr(a, "why"))
		},
	},
	"resume": {
		desc:     "Resume a paused session.",
		props:    jsfmt.Object([]string{"target", "why"}, []*jsfmt.Value{sProp(""), sProp("")}),
		required: []string{"target"},
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			return c.ch.Control(ctx, c.me, argStr(a, "target"), "resume", argStr(a, "why"))
		},
	},
	"turn": {
		desc: "Who holds the turn (in ping-pong work only the holder acts), and the event that gave it.",
		run:  func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) { return c.ch.Turn(ctx) },
	},
	"pass": {
		desc:     "Hand the turn to another session, with what it should do next.",
		props:    jsfmt.Object([]string{"to", "msg"}, []*jsfmt.Value{sProp(""), sProp("")}),
		required: []string{"to"},
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			return c.ch.PassTurn(ctx, c.me, argStr(a, "to"), argStr(a, "msg"))
		},
	},
	"take": {
		desc:  "Take the turn (only when the owner asked you to, or the channel works in parallel).",
		props: jsfmt.Object([]string{"msg"}, []*jsfmt.Value{sProp("")}),
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			return c.ch.TakeTurn(ctx, c.me, argStr(a, "msg"))
		},
	},
	"next": {
		desc:  "Your next task in plan order (yours or unowned): what, why, how, checks, the owner's notes (they override the text). ready=false lists what it waits on: wait for task.ready.",
		props: jsfmt.Object([]string{"json"}, []*jsfmt.Value{bProp("")}),
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			t, done, err := c.ch.Next(ctx, c.me)
			if err != nil {
				return nil, err
			}
			if done || t == nil {
				return NextDone{Done: true}, nil
			}
			return t, nil
		},
		text: func(r any, c opCall) string {
			if t, ok := r.(*TaskView); ok {
				return taskText(t, c.ch.Name)
			}
			return "every task you can take is done or skipped"
		},
	},
	"task": {
		desc:     "One task as the owner sees it.",
		props:    jsfmt.Object([]string{"id", "json"}, []*jsfmt.Value{sProp(""), bProp("")}),
		required: []string{"id"},
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			t, err := c.ch.Task(ctx, argStr(a, "id"))
			if err != nil {
				return nil, err
			}
			if t == nil {
				return nil, httpErr(404, "no task "+argStr(a, "id"))
			}
			return t, nil
		},
		text: func(r any, c opCall) string { return taskText(r.(*TaskView), c.ch.Name) },
	},
	"tasks": {
		desc: "The plan as a list (filter by owner, status, phase).",
		props: jsfmt.Object([]string{"owner", "status", "phase"}, []*jsfmt.Value{
			sProp(""), enProp(Statuses, ""), iProp(""),
		}),
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			return c.ch.Tasks(ctx, TasksFilter{
				Owner: argStrPtr(a, "owner"), Status: argStr(a, "status"), Phase: argIntPtr(a, "phase"),
			})
		},
		text: func(r any, c opCall) string {
			lines := []string{}
			for _, t := range r.([]TaskListItem) {
				owner := ""
				if t.Owner != nil {
					owner = " @" + *t.Owner
				}
				waits := ""
				if len(t.BlockedBy) > 0 {
					waits = "  ⟵ waits on " + strings.Join(t.BlockedBy, ", ")
				}
				lines = append(lines, fmt.Sprintf("%s [%s]%s %s%s", t.ID, t.Status, owner, t.Title, waits))
			}
			if len(lines) == 0 {
				return "(no tasks)"
			}
			return strings.Join(lines, "\n")
		},
	},
	"task_create": {
		desc: "Create a task, optionally for another session (owner) and after other tasks (after): that session gets task.created now and task.ready when the dependencies finish.",
		props: jsfmt.Object([]string{"title", "owner", "after", "id", "phase", "what", "how", "verify"}, []*jsfmt.Value{
			sProp(""), sProp(""), arrProp("task ids it waits on"), sProp(""), iProp(""),
			sProp(""), arrProp(""), arrProp(""),
		}),
		required: []string{"title"},
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			how := argStrs(a, "how")
			if how == nil {
				how = []string{}
			}
			verify := argStrs(a, "verify")
			checks := make([]*jsfmt.Value, 0, len(verify))
			for _, v := range verify {
				checks = append(checks, jsfmt.Object([]string{"cmd", "expect"}, []*jsfmt.Value{jstr(v), jstr("")}))
			}
			body := jsfmt.Object([]string{"what", "how", "verify"}, []*jsfmt.Value{
				jstr(argStr(a, "what")), jstrs(how), jsfmt.Array(checks),
			})
			return c.ch.CreateTask(ctx, c.me, CreateTaskInput{
				ID: argStr(a, "id"), Title: argStr(a, "title"), Owner: argStrPtr(a, "owner"),
				After: argStrs(a, "after"), Phase: argIntPtr(a, "phase"), Body: body,
			})
		},
	},
	"task_update": {
		desc: "Reassign (owner), change dependencies (after; cycles are refused) or edit a field (field + value; null restores the plan's text).",
		props: jsfmt.Object([]string{"id", "owner", "after", "field", "value"}, []*jsfmt.Value{
			sProp(""), sProp(""), arrProp(""), sProp(""), jsfmt.Object(nil, nil),
		}),
		required: []string{"id"},
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			return c.ch.UpdateTask(ctx, c.me, argStr(a, "id"), UpdateTaskInput{
				Owner: argStrPtr(a, "owner"), After: argStrs(a, "after"),
				Field: argStr(a, "field"), Value: argOf(a, "value"),
			})
		},
		text: func(r any, c opCall) string { return taskText(r.(*TaskView), c.ch.Name) },
	},
	"task_status": {
		desc: "Move a task: doing (refused while it waits on unfinished tasks), done (with evidence in note: releases the tasks waiting on it), blocked (why), skipped.",
		props: jsfmt.Object([]string{"id", "status", "note"}, []*jsfmt.Value{
			sProp(""), enProp(Statuses, ""), sProp("evidence or reason"),
		}),
		required: []string{"id", "status"},
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			return c.ch.SetStatus(ctx, c.me, argStr(a, "id"), argStr(a, "status"), argStr(a, "note"))
		},
	},
	"wait_task": {
		desc: "Block until a task (usually another session's) is done or skipped.",
		props: jsfmt.Object([]string{"id", "timeout"}, []*jsfmt.Value{
			sProp(""), iProp("seconds; omit it"),
		}),
		required: []string{"id"},
		blocks:   true,
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			return c.ch.WaitTask(ctx, c.me, argStr(a, "id"), opTimeout(c, a))
		},
	},
	"note": {
		desc: "Add a review note to a task.",
		props: jsfmt.Object([]string{"id", "kind", "body"}, []*jsfmt.Value{
			sProp(""), enProp(NoteKinds, ""), sProp(""),
		}),
		required: []string{"id", "kind", "body"},
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			return c.ch.Note(ctx, c.me, argStr(a, "id"), argStr(a, "kind"), argStr(a, "body"))
		},
		text: func(r any, c opCall) string { return taskText(r.(*TaskView), c.ch.Name) },
	},
	"map": {
		desc:  "The big picture, when you need it: each phase's progress, what every session does now and next, and the critical path (the longest chain of unfinished dependencies).",
		props: jsfmt.Object([]string{"json"}, []*jsfmt.Value{bProp("")}),
		run:   func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) { return c.ch.Map(ctx) },
		text:  func(r any, c opCall) string { return mapText(r.(MapView)) },
	},

	// ── workflows: the common moves in one call each (fewer turns, fewer tokens) ──
	"start": {
		desc:  "Start work: checks the pause, takes the task (id, or your next one), marks it doing and returns its full text. If it still waits on other tasks, says on which and does not start it: then call wait with topics [\"task.ready\"].",
		props: jsfmt.Object([]string{"id"}, []*jsfmt.Value{sProp("task id; omit for your next task")}),
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			paused, err := c.ch.ControlOf(ctx, c.me)
			if err != nil {
				return nil, err
			}
			if paused == "pause" {
				return StartResult{Started: false, Paused: true, Hint: "paused: call gate"}, nil
			}
			var t *TaskView
			if id := argStr(a, "id"); id != "" {
				t, err = c.ch.Task(ctx, id)
				if err != nil {
					return nil, err
				}
				if t == nil {
					return nil, httpErr(404, fmt.Sprintf("no task %s", id))
				}
			} else {
				n, done, err := c.ch.Next(ctx, c.me)
				if err != nil {
					return nil, err
				}
				if done {
					return StartResult{Started: false, Done: true}, nil
				}
				t = n
			}
			if !t.Ready {
				return StartResult{Started: false, Task: t, Hint: waitsOnHint(t)}, nil
			}
			if t.Status != "doing" {
				if _, err := c.ch.SetStatus(ctx, c.me, t.ID, "doing", ""); err != nil {
					return nil, err
				}
			}
			if _, err := c.ch.Presence(ctx, c.me, PresenceInput{
				State: sp("working"), Task: sp(fmt.Sprintf("%s %s", t.ID, t.Title)), SetStep: true, Step: sp(t.ID),
			}); err != nil {
				return nil, err
			}
			full, err := c.ch.Task(ctx, t.ID)
			if err != nil {
				return nil, err
			}
			return StartResult{Started: true, Task: full}, nil
		},
		text: func(r any, c opCall) string {
			s := r.(StartResult)
			if s.Paused {
				return "PAUSED: call gate before any work"
			}
			if s.Done {
				return "every task you can take is done or skipped"
			}
			head := "STARTED"
			if !s.Started {
				head = "NOT STARTED: " + s.Hint
			}
			return head + "\n\n" + taskText(s.Task, c.ch.Name)
		},
	},
	"finish": {
		desc: "Finish a task: marks it done with your evidence (note), optionally shares its output as a result others can recall, and returns what it released and your next task.",
		props: jsfmt.Object([]string{"id", "note", "result", "title"}, []*jsfmt.Value{
			sProp("omit for the task you are doing"), sProp("evidence: command + result"),
			sProp("output another task needs (stored as knowledge kind=result)"), sProp("title for that result"),
		}),
		required: []string{"note"},
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			id := argStr(a, "id")
			if id == "" {
				current, err := c.ch.Current(ctx, c.me)
				if err != nil {
					return nil, err
				}
				if current == nil {
					return nil, bad("no task in progress: pass id")
				}
				id = *current
			}
			before, err := c.ch.LastSeq(ctx)
			if err != nil {
				return nil, err
			}
			if _, err := c.ch.SetStatus(ctx, c.me, id, "done", argStr(a, "note")); err != nil {
				return nil, err
			}
			evs, err := c.ch.Events(ctx, EventsFilter{After: &before, Topic: "task.ready"})
			if err != nil {
				return nil, err
			}
			released := []ReleasedItem{}
			for _, e := range evs {
				released = append(released, ReleasedItem{Task: e.Ref, To: e.To})
			}
			t, err := c.ch.Task(ctx, id)
			if err != nil {
				return nil, err
			}
			var knowledge *int64
			if result := argStr(a, "result"); result != "" {
				title := argStr(a, "title")
				if title == "" {
					title = fmt.Sprintf("%s %s", id, t.Title)
				}
				kb, err := c.ch.Remember(ctx, c.me, RememberInput{Kind: "result", Title: title, Body: result, Task: sp(id)})
				if err != nil {
					return nil, err
				}
				knowledge = &kb.ID
			}
			n, done, err := c.ch.Next(ctx, c.me)
			if err != nil {
				return nil, err
			}
			var next *FinishNext
			if !done && n != nil {
				unmet := []string{}
				for _, u := range n.Unmet {
					unmet = append(unmet, u.ID)
				}
				next = &FinishNext{ID: n.ID, Title: n.Title, Ready: n.Ready, Unmet: unmet}
			}
			if _, err := c.ch.Presence(ctx, c.me, PresenceInput{
				State: sp("working"), Task: sp("finished " + id), SetStep: true,
			}); err != nil {
				return nil, err
			}
			return FinishResult{Done: id, Released: released, Knowledge: knowledge, Next: next}, nil
		},
	},
	"depend": {
		desc: "You need another session's work before yours can go on: creates a task for that session, makes your task wait on it, marks yours blocked, then blocks until it is released (task.ready) — one call instead of four.",
		props: jsfmt.Object([]string{"owner", "title", "what", "id", "wait", "timeout"}, []*jsfmt.Value{
			sProp("the session that must do it"), sProp("what they must do"), sProp("details"),
			sProp("your task; omit for the one you are doing"), bProp("default true"), iProp("seconds; omit it"),
		}),
		required: []string{"owner", "title"},
		blocks:   true,
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			mine := argStr(a, "id")
			if mine == "" {
				current, err := c.ch.Current(ctx, c.me)
				if err != nil {
					return nil, err
				}
				if current == nil {
					return nil, bad("no task in progress: pass id")
				}
				mine = *current
			}
			body := jsfmt.Object([]string{"what"}, []*jsfmt.Value{jstr(argStr(a, "what"))})
			created, err := c.ch.CreateTask(ctx, c.me, CreateTaskInput{Title: argStr(a, "title"), Owner: argStrPtr(a, "owner"), Body: body})
			if err != nil {
				return nil, err
			}
			cur, err := c.ch.Task(ctx, mine)
			if err != nil {
				return nil, err
			}
			var after []string
			for _, d := range cur.Depends {
				if !slices.Contains(after, d) {
					after = append(after, d)
				}
			}
			if !slices.Contains(after, created.Task.ID) {
				after = append(after, created.Task.ID)
			}
			if _, err := c.ch.UpdateTask(ctx, c.me, mine, UpdateTaskInput{After: after}); err != nil {
				return nil, err
			}
			note := fmt.Sprintf("waits on %s (%s): %s", created.Task.ID, argStr(a, "owner"), argStr(a, "title"))
			if _, err := c.ch.SetStatus(ctx, c.me, mine, "blocked", note); err != nil {
				return nil, err
			}
			if wait := argBoolPtr(a, "wait"); wait != nil && !*wait {
				no := false
				return DependResult{Created: created.Task.ID, Blocked: mine, Waiting: &no}, nil
			}
			h, err := c.ch.Wait(ctx, c.me, []string{"task.ready"}, opTimeout(c, a))
			if err != nil {
				return nil, err
			}
			hint := "call wait with topics [\"task.ready\"]"
			if h.Kind == "event" {
				hint = fmt.Sprintf("ack %d, then start %s", h.Seq, mine)
			}
			return DependResult{Created: created.Task.ID, Blocked: mine, Woke: &h, Hint: hint}, nil
		},
	},
	"handoff": {
		desc: "Hand the work to another session in one call: optionally creates a task for it (title, after), passes the turn with your message, then waits for your next wake-up (unless wait=false).",
		props: jsfmt.Object([]string{"to", "msg", "title", "after", "topics", "wait", "timeout"}, []*jsfmt.Value{
			sProp(""), sProp("what they should do next"), sProp("create a task for them with this title"),
			arrProp("their task waits on these"), arrProp("what to wait for afterwards; default turn.pass task.ready"),
			bProp("default true"), iProp("seconds; omit it"),
		}),
		required: []string{"to", "msg"},
		blocks:   true,
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			to := argStr(a, "to")
			var taskID *string
			if title := argStr(a, "title"); title != "" {
				created, err := c.ch.CreateTask(ctx, c.me, CreateTaskInput{Title: title, Owner: sp(to), After: argStrs(a, "after")})
				if err != nil {
					return nil, err
				}
				taskID = sp(created.Task.ID)
			}
			ev, err := c.ch.PassTurn(ctx, c.me, to, argStr(a, "msg"))
			if err != nil {
				return nil, err
			}
			if wait := argBoolPtr(a, "wait"); wait != nil && !*wait {
				return HandoffResult{Passed: ev.Seq, Task: taskID}, nil
			}
			topics := argStrs(a, "topics")
			if topics == nil {
				topics = []string{"turn.pass", "task.ready"}
			}
			h, err := c.ch.Wait(ctx, c.me, topics, opTimeout(c, a))
			if err != nil {
				return nil, err
			}
			return HandoffResult{Passed: ev.Seq, Task: taskID, Woke: &h}, nil
		},
	},
	"ask_wait": {
		desc:     "Ask one session a question and block until it replies (the answer comes back as the result).",
		props:    jsfmt.Object([]string{"to", "msg", "timeout"}, []*jsfmt.Value{sProp(""), sProp(""), iProp("seconds; omit it")}),
		required: []string{"to", "msg"},
		blocks:   true,
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			q, err := c.ch.Send(ctx, c.me, sp(argStr(a, "to")), argStr(a, "msg"), SendOpts{Ask: true})
			if err != nil {
				return nil, err
			}
			h, err := c.ch.WaitReply(ctx, c.me, q.Seq, opTimeout(c, a))
			if err != nil {
				return nil, err
			}
			if h.Kind == "event" {
				return AskWaitResult{Asked: q.Seq, Answer: deref(h.Msg), From: h.From, Seq: h.Seq}, nil
			}
			return AskWaitResult{
				Asked: q.Seq, Timeout: h.Kind == "timeout", Cancelled: h.Kind == "cancelled",
				Hint: "no reply yet: the ask stays open; wait again later",
			}, nil
		},
	},

	// ── knowledge: what one session learned, so the others do not pay for it again ──
	"remember": {
		desc: "Share what you learned so no other session or subagent pays for it again: fact, lesson, decision (with why), context (summary of what you read), result (output of finished work), howto (commands that work). supersedes retires an older entry. If an entry already says the same, nothing is added and you get its id: supersede it, verify it, or pass force. scope=server: for every channel on this server (tool quirks, machine facts).",
		props: jsfmt.Object([]string{"kind", "title", "body", "tags", "refs", "task", "supersedes", "scope", "force"}, []*jsfmt.Value{
			enProp(KBKinds, ""), sProp("one line, searchable"), sProp(""), arrProp(""),
			arrProp("files, urls, event seqs (a file that changes later marks the entry may be stale)"),
			sProp(""), iProp(""), enProp([]string{"channel", "server"}, "channel (default) or server: every channel on this server"),
			bProp("add it even if a similar entry exists"),
		}),
		required: []string{"kind", "title", "body"},
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			return Remember(ctx, c.ch, c.me, RememberArgs{
				Kind: argStr(a, "kind"), Title: argStr(a, "title"), Body: argStr(a, "body"),
				Tags: argStrs(a, "tags"), Refs: argStrs(a, "refs"), Task: argStrPtr(a, "task"),
				Supersedes: argIntPtr(a, "supersedes"), Scope: argStr(a, "scope"),
				Force: jsTruthy(argOf(a, "force")),
			})
		},
		text: func(r any, c opCall) string {
			if d, ok := r.(DuplicateResult); ok {
				return "not added: " + d.Hint
			}
			switch v := r.(type) {
			case KBEntry:
				return fmt.Sprintf("#%d remembered: %s", v.ID, v.Title)
			case RememberShared:
				return fmt.Sprintf("#%d remembered: %s", v.ID, v.Title)
			}
			return ""
		},
	},
	"recall": {
		desc: `Search the shared knowledge (this channel's and the entries for every channel) before reading files or re-deriving anything. Verified entries come first; each shows its age, and "may be stale" when it is old or its files changed. Returns titles and snippets; read one with kb.`,
		props: jsfmt.Object([]string{"q", "kind", "tag", "by", "limit"}, []*jsfmt.Value{
			sProp("words; empty = latest"), enProp(KBKinds, ""), sProp(""), sProp(""), iProp(""),
		}),
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			lim := 0
			if v := argIntPtr(a, "limit"); v != nil {
				lim = int(*v)
			}
			return Recall(ctx, c.ch, argStr(a, "q"), RecallOpts{
				Kind: argStr(a, "kind"), Tag: argStr(a, "tag"), By: argStr(a, "by"), Limit: lim,
			})
		},
		text: func(r any, c opCall) string {
			var lines []string
			for _, k := range r.([]KBShape) {
				tags := ""
				if len(k.Tags) > 0 {
					tags = fmt.Sprintf(" {%s}", strings.Join(k.Tags, ","))
				}
				lines = append(lines, fmt.Sprintf("#%d [%s] %s — %s%s%s\n   %s",
					k.ID, k.Kind, k.Title, k.By, kbFlags(k), tags,
					jsscalar.UTF16Slice(spaceRuns.ReplaceAllString(k.Hit, " "), 0, 240)))
			}
			if len(lines) == 0 {
				return "(nothing yet: remember what you learn)"
			}
			return strings.Join(lines, "\n")
		},
	},
	"kb": {
		desc:     "Read one knowledge entry in full.",
		props:    jsfmt.Object([]string{"id"}, []*jsfmt.Value{iProp("")}),
		required: []string{"id"},
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			return KB(ctx, c.ch, argID(a, "id"))
		},
		text: func(r any, c opCall) string { return kbText(r) },
	},
	"verify": {
		desc:     `Vouch for a knowledge entry you checked is still right (verified entries come first in recall; verifying also clears "may be stale"). undo=true takes it back.`,
		props:    jsfmt.Object([]string{"id", "undo"}, []*jsfmt.Value{iProp(""), bProp("")}),
		required: []string{"id"},
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			return Verify(ctx, c.ch, c.me, argID(a, "id"), jsTruthy(argOf(a, "undo")))
		},
		text: func(r any, c opCall) string {
			k := r.(KBShape)
			verb := "no longer verified"
			if k.VerifiedAt != nil {
				verb = fmt.Sprintf("verified by %s", deref(k.VerifiedBy))
			}
			return fmt.Sprintf("#%d %s: %s", k.ID, verb, k.Title)
		},
	},
	"share": {
		desc:     "Make one of this channel's knowledge entries one for every channel on this server (a tool quirk, a machine fact); it gets a new id.",
		props:    jsfmt.Object([]string{"id"}, []*jsfmt.Value{iProp("")}),
		required: []string{"id"},
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			return Share(ctx, c.ch, c.me, argID(a, "id"))
		},
		text: func(r any, c opCall) string {
			switch k := r.(type) {
			case ShareResult:
				return fmt.Sprintf("#%d is for every channel now (was #%d): %s", k.ID, k.From, k.Title)
			case KBShared:
				return fmt.Sprintf("#%d is for every channel now: %s", k.ID, k.Title)
			}
			return ""
		},
	},
	"digest": {
		desc: "What happened since a moment (default: the last 24 h), per session: tasks finished, notes, knowledge added, approval requests; what is blocked now and the questions still open; and the estimated cost per session when the Radar plugin runs. Built from the channel's history, no model calls.",
		props: jsfmt.Object([]string{"since", "json"}, []*jsfmt.Value{
			sProp("24h, 90m, 7d or an ISO date; default 24h"), bProp(""),
		}),
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			return Digest(ctx, c.ch, argAny(a, "since"), DigestOpts{})
		},
		text: func(r any, c opCall) string { return DigestText(r.(*DigestResult)) },
	},

	// ops that run the plan: the owner's, and the channel's orchestrator's (config
	// `orchestrator`). Only the owner changes who the orchestrator is and who may join.
	"configure": {
		desc: "Owner or orchestrator: configure the channel: title, description, start (who holds the turn first), handover ({sender: {to, topics}}), profile, repo; the owner only: members, orchestrator.",
		props: jsfmt.Object([]string{"title", "description", "start", "handover", "profile", "repo", "members", "orchestrator"}, []*jsfmt.Value{
			sProp(""), sProp(""), sProp(""), oProp(), sProp(""), sProp(""),
			arrProp("who may join; empty = anyone"), sProp("the session that runs the plan with the owner; empty = none"),
		}),
		owner: true,
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			if c.me != Owner && (argOf(a, "members") != nil || argOf(a, "orchestrator") != nil) {
				return nil, httpErr(403, "only the owner changes members and the orchestrator")
			}
			return c.ch.Configure(ctx, ConfigureArgs{
				Title: argStrPtr(a, "title"), Description: argStrPtr(a, "description"), Start: argStrPtr(a, "start"),
				Handover: argOf(a, "handover"), Profile: argStrPtr(a, "profile"), Repo: argStrPtr(a, "repo"),
				Members: argOf(a, "members"), Orchestrator: argStrPtr(a, "orchestrator"),
			})
		},
	},
	"import_plan": {
		desc:     "Owner or orchestrator: load a plan {phases:[{n,title,…,steps:[{id,title,owner,depends,…}]}]}; merged by task id, keeping status, edits and notes.",
		props:    jsfmt.Object([]string{"plan", "owner"}, []*jsfmt.Value{oProp(), sProp("owner of steps that name none")}),
		required: []string{"plan"},
		owner:    true,
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			plan, err := PlanFromValue(argOf(a, "plan"))
			if err != nil {
				return nil, err
			}
			return c.ch.ImportPlan(ctx, c.me, plan, ImportOpts{Owner: argStrPtr(a, "owner")})
		},
	},
	"approve": {
		desc:     "Owner or orchestrator: approve a gated task (owner / ask-first): a note on it and a message to its owner.",
		props:    jsfmt.Object([]string{"id", "msg"}, []*jsfmt.Value{sProp(""), sProp("")}),
		required: []string{"id"},
		owner:    true,
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			return c.ch.Approve(ctx, argStr(a, "id"), argStr(a, "msg"), c.me)
		},
	},
	"note_edit": {
		desc: "Owner or orchestrator: resolve, edit or remove a note.",
		props: jsfmt.Object([]string{"id", "resolved", "body", "remove"}, []*jsfmt.Value{
			iProp(""), bProp(""), sProp(""), bProp(""),
		}),
		required: []string{"id"},
		owner:    true,
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			return c.ch.EditNote(ctx, argID(a, "id"), EditNoteInput{
				Resolved: argBoolPtr(a, "resolved"), Body: argStrPtr(a, "body"),
				Remove: jsTruthy(argOf(a, "remove")),
			})
		},
	},
	"assign": {
		desc: "Owner or orchestrator: how a session joins when it returns (context sync or fresh; null = sync), and optionally a task it now owns.",
		props: jsfmt.Object([]string{"session", "context", "task"}, []*jsfmt.Value{
			sProp(""), nullEnumProp(Contexts), sProp("task id to give the session"),
		}),
		required: []string{"session"},
		owner:    true,
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			return c.ch.Assign(ctx, c.me, argStr(a, "session"), argStrPtr(a, "context"), argStr(a, "task"))
		},
	},
	"brief": {
		desc:     "Owner or orchestrator: what a session gets on its next fresh join instead of the history (kept until then; also sent now as a message).",
		props:    jsfmt.Object([]string{"session", "msg"}, []*jsfmt.Value{sProp(""), sProp("")}),
		required: []string{"session", "msg"},
		owner:    true,
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			return c.ch.Brief(ctx, c.me, argStr(a, "session"), argStr(a, "msg"))
		},
	},

	// ── reservations: who is about to change what, so two sessions never collide unawares ──
	"reserve": {
		desc: "Reserve files you are about to change so other sessions are warned before they edit them. Each path answers held, or taken by <session> until <time> — never blocked; re-reserving your own path extends it. ttl_minutes 1..480 (default 60); note says why. repo (the repository's identity, its normalised origin) with root (the checkout root the paths are under) lets another checkout or machine of the same repository match the same file.",
		props: jsfmt.Object([]string{"paths", "ttl_minutes", "note", "repo", "root"}, []*jsfmt.Value{
			arrProp("absolute paths you are about to change"),
			iProp("1..480; omit it: 60"),
			sProp("why, shown to whoever reaches a reserved path"),
			sProp("the repository's identity: its normalised origin, e.g. github.com/org/repo"),
			sProp("the checkout root the paths are under"),
		}),
		required: []string{"paths"},
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			return Reserve(ctx, c.ch, c.me, ReserveArgs{
				Paths: argStrs(a, "paths"), TTLMinutes: argIntPtr(a, "ttl_minutes"),
				Note: argStrPtr(a, "note"), Repo: argStrPtr(a, "repo"), Root: argStrPtr(a, "root"),
			})
		},
		text: reserveText,
	},
	"release": {
		desc: "Release your reservations: the paths named, or all of yours when none are. A leaving session's reservations are released with its leave.",
		props: jsfmt.Object([]string{"paths"}, []*jsfmt.Value{
			arrProp("absolute paths to free; omit for all of yours"),
		}),
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			return Release(ctx, c.ch, c.me, argStrs(a, "paths"))
		},
		text: releaseText,
	},
	"reservations": {
		desc: "The channel's live reservations, newest first: who holds which path, until when, and why.",
		props: jsfmt.Object([]string{"mine"}, []*jsfmt.Value{
			bProp("keep yours"),
		}),
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			holder := ""
			if jsTruthy(argOf(a, "mine")) {
				holder = holderOf(c.me)
			}
			return Reservations(ctx, c.ch, holder)
		},
		text: reservationsText,
	},

	// ops the hooks call, not tools an agent sees
	"reserved": {
		desc:     "The PreToolUse hook: is this file (or a directory above it) reserved by another live session?",
		props:    jsfmt.Object([]string{"path", "repo", "rel"}, []*jsfmt.Value{sProp("the absolute path about to be edited"), sProp("the repository's identity, when the file sits in one"), sProp("the path inside that repository")}),
		required: []string{"path"},
		hook:     true,
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			return Reserved(ctx, c.ch, c.me, argStr(a, "path"), argStr(a, "repo"), argStr(a, "rel"))
		},
		text: reservedText,
	},
	"touched": {
		desc:     "The PostToolUse hook: this session edited a file (path relative to its repo).",
		props:    jsfmt.Object([]string{"path", "repo"}, []*jsfmt.Value{sProp(""), sProp("")}),
		required: []string{"path", "repo"},
		hook:     true,
		run: func(ctx context.Context, c opCall, a *jsfmt.Value) (any, error) {
			return Touched(ctx, c.ch, c.me, TouchArgs{Path: argAny(a, "path"), Repo: argAny(a, "repo")})
		},
		text: func(r any, c opCall) string { return deref(r.(TouchResult).Warn) },
	},
}

// Blocks says whether the op waits for the channel by design, so the transports give its tool
// their long timeout. An unknown name is not blocking.
func Blocks(name string) bool {
	return ops[name].blocks
}

// Identity answers who acts: a connection is one session; it may act for its own subagents
// ("<me>.<role>") by passing `as`. The owner may act as anyone (the UI speaks for the owner).
func Identity(conn string, as *string) (string, error) {
	if as == nil || *as == "" || *as == conn {
		return conn, nil
	}
	a := *as
	if !NameRe.MatchString(a) && a != Owner {
		return "", httpErr(400, fmt.Sprintf("bad identity %s", a))
	}
	if conn == Owner || strings.HasPrefix(a, conn+".") {
		return a, nil
	}
	return "", httpErr(403, fmt.Sprintf("%s may act only as itself or its subagents (%s.<role>), not %s", conn, conn, a))
}

// RunOp runs one operation as conn (acting as the `as` in args, when allowed).
func RunOp(ctx context.Context, name string, ch *Channel, conn string, args *jsfmt.Value) (OpResult, error) {
	def, ok := ops[name]
	if !ok {
		return OpResult{}, httpErr(404, fmt.Sprintf("no operation %s", name))
	}
	me, err := Identity(conn, strPtrOf(argOf(args, "as")))
	if err != nil {
		return OpResult{}, err
	}
	if def.owner && me != Owner && deref(ch.Config().Orchestrator) != me {
		return OpResult{}, httpErr(403, fmt.Sprintf("%s is the owner's or the orchestrator's", name))
	}
	for _, r := range def.required {
		v := argOf(args, r)
		if v == nil || v.Kind() == jsfmt.KindNull || (v.Kind() == jsfmt.KindString && v.Text() == "") {
			return OpResult{}, httpErr(400, fmt.Sprintf("%s: missing %s", name, r))
		}
	}
	if name != "join" {
		if err := ch.touch(ctx, me); err != nil {
			return OpResult{}, err
		}
	}
	result, err := def.run(ctx, opCall{ch: ch, me: me, waitDefault: 240}, args)
	if err != nil {
		return OpResult{}, err
	}
	var text *string
	if def.text != nil && !truthy(argOf(args, "json")) {
		rendered := def.text(result, opCall{ch: ch, me: me, waitDefault: 240})
		text = &rendered
	}
	return OpResult{Result: result, Text: text}, nil
}

func truthy(v *jsfmt.Value) bool {
	if v == nil {
		return false
	}
	return v.EncodeJSON() == "true"
}

// ToolDef is one tool of the MCP list.
type ToolDef struct {
	Name        string
	Description string
	Schema      *jsfmt.Value
}

// ToolDefs lists the ops as tools an agent sees (the hook ops are not among them), plus `as` for
// acting as one of your subagents.
func ToolDefs() []ToolDef {
	as := sProp("act as one of your subagents: <you>.<role> (join it first)")
	out := make([]ToolDef, 0, len(opsOrder))
	for _, name := range opsOrder {
		def := ops[name]
		if def.hook {
			continue
		}
		props := def.props
		keys := []string{}
		fields := []*jsfmt.Value{}
		if props != nil && props.Kind() == jsfmt.KindObject {
			keys = append(keys, props.Keys()...)
			fields = append(fields, props.Fields()...)
		}
		keys = append(keys, "as")
		fields = append(fields, as)
		schemaKeys := []string{"type", "properties"}
		schemaFields := []*jsfmt.Value{jstr("object"), jsfmt.Object(keys, fields)}
		if len(def.required) > 0 {
			schemaKeys = append(schemaKeys, "required")
			req := make([]*jsfmt.Value, len(def.required))
			for i, r := range def.required {
				req[i] = jstr(r)
			}
			schemaFields = append(schemaFields, jsfmt.Array(req))
		}
		out = append(out, ToolDef{
			Name: name, Description: def.desc, Schema: jsfmt.Object(schemaKeys, schemaFields),
		})
	}
	return out
}

// ── the input schema's property builders ─────────────────────────────────────

func propWith(typ string, desc string) *jsfmt.Value {
	keys := []string{"type"}
	fields := []*jsfmt.Value{jstr(typ)}
	if desc != "" {
		keys = append(keys, "description")
		fields = append(fields, jstr(desc))
	}
	return jsfmt.Object(keys, fields)
}

func sProp(desc string) *jsfmt.Value { return propWith("string", desc) }
func bProp(desc string) *jsfmt.Value { return propWith("boolean", desc) }
func iProp(desc string) *jsfmt.Value { return propWith("integer", desc) }
func oProp() *jsfmt.Value            { return propWith("object", "") }

func arrProp(desc string) *jsfmt.Value {
	keys := []string{"type", "items"}
	fields := []*jsfmt.Value{jstr("array"), jsfmt.Object([]string{"type"}, []*jsfmt.Value{jstr("string")})}
	if desc != "" {
		keys = append(keys, "description")
		fields = append(fields, jstr(desc))
	}
	return jsfmt.Object(keys, fields)
}

func enProp(vals []string, desc string) *jsfmt.Value {
	items := make([]*jsfmt.Value, len(vals))
	for i, v := range vals {
		items[i] = jstr(v)
	}
	keys := []string{"type", "enum"}
	fields := []*jsfmt.Value{jstr("string"), jsfmt.Array(items)}
	if desc != "" {
		keys = append(keys, "description")
		fields = append(fields, jstr(desc))
	}
	return jsfmt.Object(keys, fields)
}

// nullEnumProp is a "string | null" enum (a context or null).
func nullEnumProp(vals []string) *jsfmt.Value {
	items := make([]*jsfmt.Value, len(vals)+1)
	for i, v := range vals {
		items[i] = jstr(v)
	}
	items[len(vals)] = jnull()
	return jsfmt.Object([]string{"type", "enum"}, []*jsfmt.Value{
		jsfmt.Array([]*jsfmt.Value{jstr("string"), jstr("null")}), jsfmt.Array(items),
	})
}

// ── text renderings for an agent's context ──────────────────────────────────

// kbFlags is a knowledge entry's marks, for an agent's context.
func kbFlags(k KBShape) string {
	age := " · today"
	if k.AgeDays != 0 {
		age = fmt.Sprintf(" · %dd old", k.AgeDays)
	}
	verified := ""
	if k.VerifiedAt != nil {
		verified = fmt.Sprintf(" · verified (%s)", deref(k.VerifiedBy))
	}
	scope := ""
	if k.Scope == "server" {
		scope = " · every channel"
	}
	stale := ""
	if k.Stale != nil {
		stale = fmt.Sprintf(" · MAY BE STALE: %s", *k.Stale)
	}
	return age + verified + scope + stale
}

// kbText renders one knowledge entry in full, whichever store it lives in.
func kbText(r any) string {
	var id int64
	var kind, title, by, createdAt, body string
	var task *string
	var tags, refs []string
	var supersededBy, movedTo *int64
	var flags string
	switch k := r.(type) {
	case KBFull:
		id, kind, title, by, createdAt, body = k.ID, k.Kind, k.Title, k.By, k.CreatedAt, k.Body
		task, tags, refs = k.Task, k.Tags, k.Refs
		supersededBy, movedTo = k.SupersededBy, k.MovedTo
		flags = kbFlags(KBShape{
			AgeDays: k.AgeDays, VerifiedBy: k.VerifiedBy, VerifiedAt: k.VerifiedAt,
			Scope: k.Scope, Stale: k.Stale,
		})
	case KBShared:
		id, kind, title, by, createdAt, body = k.ID, k.Kind, k.Title, k.By, k.CreatedAt, k.Body
		task, tags, refs = k.Task, k.Tags, k.Refs
		supersededBy, movedTo = k.SupersededBy, k.MovedTo
		flags = kbFlags(k.KBShape)
	default:
		return ""
	}
	head := fmt.Sprintf("#%d [%s] %s\nby %s · %s%s", id, kind, title, by, createdAt, flags)
	if task != nil && *task != "" {
		head += fmt.Sprintf(" · task %s", *task)
	}
	if supersededBy != nil {
		head += fmt.Sprintf(" · SUPERSEDED by #%d", *supersededBy)
	}
	if movedTo != nil {
		head += fmt.Sprintf(" · now for every channel as #%d", *movedTo)
	}
	if len(tags) > 0 {
		head += fmt.Sprintf("\ntags: %s", strings.Join(tags, ", "))
	}
	if len(refs) > 0 {
		head += fmt.Sprintf("\nrefs: %s", strings.Join(refs, ", "))
	}
	return head + "\n\n" + body
}

func snapText(r Snapshot) string {
	var L []string
	running := "running"
	if r.Control != "run" {
		running = "PAUSED: call gate before any work"
	}
	L = append(L, fmt.Sprintf("channel %s · you are %s · %s", r.Channel, r.Me, running))
	holder := "free (parallel)"
	if r.Turn.Holder != nil {
		holder = *r.Turn.Holder
	}
	since := ""
	if r.Turn.Since != nil {
		since = fmt.Sprintf(" (since #%d %s %s)", r.Turn.Since.Seq, r.Turn.Since.From, r.Turn.Since.Topic)
	}
	L = append(L, fmt.Sprintf("turn: %s%s", holder, since))
	if r.Orchestrator != nil {
		you := ""
		if *r.Orchestrator == r.Me {
			you = " (you: you run the plan with the owner)"
		}
		L = append(L, "orchestrator: "+*r.Orchestrator+you)
	}
	if r.Context != nil {
		if *r.Context == "fresh" {
			L = append(L, fmt.Sprintf("context: fresh (%d earlier events skipped; asks kept)", r.Skipped))
		} else {
			L = append(L, "context: sync (everything unread since you left)")
		}
	}
	if r.Brief != nil {
		b := r.Brief
		goal := ""
		if b.Goal != "" {
			goal = " · " + b.Goal
		}
		L = append(L, "brief: "+b.Title+goal)
		if b.FromOrchestrator != nil {
			L = append(L, fmt.Sprintf("from %s (%s): %s", b.FromOrchestrator.By, b.FromOrchestrator.At, b.FromOrchestrator.Msg))
		}
		tasks := ""
		if len(b.Tasks) > 0 {
			for _, t := range b.Tasks {
				waits := ""
				if !t.Ready {
					waits = fmt.Sprintf(" (waits on %s)", strings.Join(t.WaitsOn, ", "))
				}
				tasks += fmt.Sprintf("\n  %s [%s] %s%s", t.ID, t.Status, t.Title, waits)
			}
		} else {
			tasks = "none"
		}
		L = append(L, "your tasks: "+tasks)
		if len(b.Knowledge) > 0 {
			kb := ""
			for _, k := range b.Knowledge {
				kb += fmt.Sprintf("\n  #%d [%s] %s (%s)", k.ID, k.Kind, k.Title, k.By)
			}
			L = append(L, "relevant knowledge (kb <id> to read):"+kb)
		}
	}
	if len(r.Sessions) > 0 {
		var parts []string
		for _, x := range r.Sessions {
			paused := ""
			if x.Control == "pause" {
				paused = "(paused)"
			}
			task := ""
			if x.Task != nil {
				task = ": " + *x.Task
			}
			stale := ""
			if x.Stale {
				stale = " [stale]"
			}
			parts = append(parts, fmt.Sprintf("%s%s %s%s%s", x.Name, paused, x.State, task, stale))
		}
		L = append(L, "others: "+strings.Join(parts, " · "))
	} else {
		L = append(L, "others: none yet")
	}
	inbox := ""
	for _, m := range r.Inbox {
		inbox += fmt.Sprintf("\n  #%d from %s: %s", m.Seq, m.From, m.Msg)
	}
	L = append(L, fmt.Sprintf("inbox: %d%s", len(r.Inbox), inbox))
	unread := ""
	if r.UnreadTotal > 0 {
		var parts []string
		lo := max(0, len(r.Unread)-12)
		for _, e := range r.Unread[lo:] {
			to := ""
			if e.To != nil {
				to = "→" + *e.To
			}
			parts = append(parts, fmt.Sprintf("#%d %s%s %s", e.Seq, e.From, to, e.Topic))
		}
		unread = " → " + strings.Join(parts, ", ")
	}
	L = append(L, fmt.Sprintf("unread: %d%s", r.UnreadTotal, unread))
	if r.Next != nil {
		waits := ""
		if !r.Next.Ready {
			var parts []string
			for _, u := range r.Next.Unmet {
				owner := "?"
				if u.Owner != nil {
					owner = *u.Owner
				}
				parts = append(parts, fmt.Sprintf("%s@%s:%s", u.ID, owner, u.Status))
			}
			waits = fmt.Sprintf(" (waits on %s)", strings.Join(parts, ", "))
		}
		L = append(L, fmt.Sprintf("next task: %s %s%s", r.Next.ID, r.Next.Title, waits))
	}
	latest := ""
	if len(r.Knowledge.Latest) > 0 {
		latest = "\n  " + strings.Join(r.Knowledge.Latest, "\n  ")
	}
	L = append(L, fmt.Sprintf("knowledge: %d entries%s — recall before you read or re-derive", r.Knowledge.Total, latest))
	return strings.Join(L, "\n")
}

// mapText renders the map in about 40 lines at most.
func mapText(r MapView) string {
	var L []string
	orch := ""
	if r.Orchestrator != nil {
		orch = " · orchestrator " + *r.Orchestrator
	}
	L = append(L, fmt.Sprintf("map: %s · %d/%d tasks done%s", r.Title, r.Done, r.Total, orch), "phases:")
	ph := r.Phases
	first := len(ph)
	for i, p := range ph {
		if p.Done < p.Total {
			first = i
			break
		}
	}
	if first == len(ph) {
		first = 0
	}
	shown := ph
	after := 0
	if len(ph) > 14 {
		end := min(first+12, len(ph))
		shown = ph[first:end]
		after = len(ph) - end
		if first > 0 {
			L = append(L, fmt.Sprintf("  … %d finished phase(s)", first))
		}
	}
	for _, p := range shown {
		n := "-"
		if p.N != nil {
			n = strconv.FormatInt(*p.N, 10)
		}
		mark := " "
		if p.Done == p.Total {
			mark = "✓"
		}
		L = append(L, fmt.Sprintf("  %3s %s %d/%d  %s", n, mark, p.Done, p.Total, p.Title))
	}
	if after > 0 {
		L = append(L, fmt.Sprintf("  … %d more phase(s)", after))
	}
	L = append(L, "sessions:")
	count := min(10, len(r.Sessions))
	for _, s := range r.Sessions[:count] {
		current := "nothing in progress"
		if s.Current != nil {
			current = fmt.Sprintf("doing %s %s", s.Current.ID, s.Current.Title)
		}
		next := ""
		if s.Next != nil {
			waits := ""
			if !s.Next.Ready {
				waits = " (waits)"
			}
			next = fmt.Sprintf(" · next %s%s", s.Next.ID, waits)
		}
		L = append(L, fmt.Sprintf("  %s (%s): %s%s", s.Name, s.State, current, next))
	}
	cp := r.CriticalPath
	plural := "s"
	if len(cp) == 1 {
		plural = ""
	}
	none := ""
	if len(cp) == 0 {
		none = "none"
	}
	L = append(L, fmt.Sprintf("critical path (%d open task%s in a chain): %s", len(cp), plural, none))
	shownTasks := min(10, len(cp))
	for _, t := range cp[:shownTasks] {
		owner := ""
		if t.Owner != nil {
			owner = " @" + *t.Owner
		}
		L = append(L, fmt.Sprintf("  %s [%s]%s %s", t.ID, t.Status, owner, t.Title))
	}
	if len(cp) > 10 {
		L = append(L, fmt.Sprintf("  … %d more", len(cp)-10))
	}
	return strings.Join(L, "\n")
}

// taskText renders one task for an agent's context.
func taskText(t *TaskView, channel string) string {
	if t == nil {
		return ""
	}
	var L []string
	gate := ""
	if g := t.Text("gate"); g != "" && g != "none" {
		gate = " · gate " + g
	}
	owner := "unowned"
	if t.Owner != nil {
		owner = "owner " + *t.Owner
	}
	owner = " · " + owner
	lastBy := ""
	if by := t.TextPtr("status_by"); by != nil {
		lastBy = fmt.Sprintf(" (last by %s)", *by)
	}
	kind := t.Text("kind")
	if kind == "" {
		kind = "task"
	}
	risk := ""
	if r := t.Text("risk"); r != "" {
		risk = fmt.Sprintf(" · %s risk", r)
	}
	L = append(L, fmt.Sprintf("%s  %s", t.ID, t.Title), fmt.Sprintf("%s%s%s · %s%s%s", kind, risk, gate, t.Status, owner, lastBy))
	if len(t.Unmet) > 0 {
		L = append(L, fmt.Sprintf("WAITS ON: %s → wait with topics [\"task.ready\"] or wait_task", waitsOnList(t)))
	}
	if len(t.OpenNotes) > 0 {
		L = append(L, "", "OWNER NOTES (read first; they override the text below):")
		for _, c := range t.OpenNotes {
			by := ""
			if c.By != nil && *c.By != "owner" {
				by = " — " + *c.By
			}
			L = append(L, fmt.Sprintf("  [%s] %s%s", c.Kind, c.Body, by))
		}
	}
	if len(t.Edited) > 0 {
		var fields []string
		for _, e := range t.Edited {
			fields = append(fields, e.Field)
		}
		L = append(L, fmt.Sprintf("(edited: %s)", strings.Join(fields, ", ")))
	}
	if what := t.Text("what"); what != "" {
		L = append(L, "", "WHAT", what)
	}
	if why := t.Text("why"); why != "" {
		L = append(L, "", "WHY", why)
	}
	if alts := t.JSON("alternatives"); alts != nil && alts.Kind() == jsfmt.KindArray {
		var lines []string
		for _, a := range alts.Items() {
			lines = append(lines, fmt.Sprintf("  - %s: %s", strOf(objField(a, "option")), strOf(objField(a, "why_not"))))
		}
		if len(lines) > 0 {
			L = append(L, append([]string{"", "NOT THAT WAY"}, lines...)...)
		}
	}
	if how := t.JSON("how"); how != nil && how.Kind() == jsfmt.KindArray {
		var lines []string
		for i, h := range how.Items() {
			lines = append(lines, fmt.Sprintf("  %d. %s", i+1, strOf(h)))
		}
		if len(lines) > 0 {
			L = append(L, append([]string{"", "HOW"}, lines...)...)
		}
	}
	if snips := t.JSON("snippets"); snips != nil && snips.Kind() == jsfmt.KindArray {
		for _, sn := range snips.Items() {
			title := strOf(objField(sn, "title"))
			path := strOf(objField(sn, "path"))
			if path != "" {
				path = fmt.Sprintf(" (%s)", path)
			}
			proposed := ""
			if p := objField(sn, "proposed"); p != nil && p.EncodeJSON() == "true" {
				proposed = " [proposed]"
			}
			L = append(L, "", fmt.Sprintf("CODE: %s%s%s", title, path, proposed))
			for _, line := range strings.Split(strOf(objField(sn, "code")), "\n") {
				L = append(L, "    "+line)
			}
		}
	}
	if verify := t.JSON("verify"); verify != nil && verify.Kind() == jsfmt.KindArray {
		var lines []string
		for _, v := range verify.Items() {
			expect := strOf(objField(v, "expect"))
			if expect != "" {
				lines = append(lines, fmt.Sprintf("  $ %s\n    → %s", strOf(objField(v, "cmd")), expect))
			} else {
				lines = append(lines, "  $ "+strOf(objField(v, "cmd")))
			}
		}
		if len(lines) > 0 {
			L = append(L, append([]string{"", "CHECK"}, lines...)...)
		}
	}
	if value := t.Text("value"); value != "" {
		L = append(L, "", "DONE MEANS", value)
	}
	if use := t.Text("use"); use != "" {
		L = append(L, "", "USE", use)
	}
	if rollback := t.Text("rollback"); rollback != "" {
		L = append(L, "", "ROLLBACK", rollback)
	}
	if len(t.Depends) > 0 {
		L = append(L, "", fmt.Sprintf("depends on %s", strings.Join(t.Depends, ", ")))
	}
	if len(t.NeededBy) > 0 {
		L = append(L, fmt.Sprintf("needed by %s", strings.Join(t.NeededBy, ", ")))
	}
	L = append(L, "", fmt.Sprintf("board: /#/c/%s/t/%s", channel, t.ID))
	return strings.Join(L, "\n")
}

func waitsOnList(t *TaskView) string {
	var parts []string
	for _, u := range t.Unmet {
		owner := "unowned"
		if u.Owner != nil {
			owner = *u.Owner
		}
		parts = append(parts, fmt.Sprintf("%s (%s, %s)", u.ID, owner, u.Status))
	}
	return strings.Join(parts, ", ")
}
