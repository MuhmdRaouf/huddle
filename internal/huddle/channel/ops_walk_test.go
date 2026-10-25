package channel

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// runOp is the table's front door for one call, the way the transports call it.
func runOp(t *testing.T, ch *Channel, name, me, args string) any {
	t.Helper()
	var v *jsfmt.Value
	if args != "" {
		v = vv(t, args)
	}
	result, err := RunOp(bg, name, ch, me, v)
	if err != nil {
		t.Fatalf("op %s as %s: %v", name, me, err)
	}
	return result.Result
}

// TestOpsWalk runs one conversation through every table operation, the way a transport does, so
// each entry's own wiring runs at least once: presence and turns, messages and their replies, the
// plan's tasks and notes, and the knowledge base.
func TestOpsWalk(t *testing.T) {
	ch := joinedChannel(t)
	runOp(t, ch, "status", "a", `{"state": "working", "task": "t1", "step": "one"}`)
	runOp(t, ch, "state", "b", `{"state": "waiting"}`)
	sessions := runOp(t, ch, "sessions", "a", "")
	if !strings.Contains(mustJSONT(t, sessions), `"name":"b"`) {
		t.Fatalf("sessions = %#v", sessions)
	}
	turn := runOp(t, ch, "turn", "a", "")
	if !strings.Contains(mustJSONT(t, turn), `"holder"`) {
		t.Fatalf("turn = %#v", turn)
	}
	runOp(t, ch, "pass", "a", `{"to": "b", "msg": "your turn"}`)
	runOp(t, ch, "take", "b", `{}`)
	runOp(t, ch, "pause", "a", `{"target": "b", "why": "waiting on the owner"}`)
	runOp(t, ch, "resume", "a", `{"target": "b"}`)
	ask := runOp(t, ch, "send", "a", `{"to": "b", "msg": "which port?", "ask": true}`)
	seq := jsonField(t, ask, "seq")
	runOp(t, ch, "publish", "a", `{"topic": "build.ready", "msg": "done", "ref": "run/7"}`)
	runOp(t, ch, "reply", "b", `{"seq": `+seq+`, "msg": "18787"}`)
	if inbox := runOp(t, ch, "inbox", "b", ""); strings.Contains(mustJSONT(t, inbox), "which port?") {
		t.Fatalf("the answered ask still waits: %#v", inbox)
	}
	events := runOp(t, ch, "events", "a", `{"after": 0, "limit": 10, "topic": "build.*"}`)
	if !strings.Contains(mustJSONT(t, events), "build.ready") {
		t.Fatalf("events = %#v", events)
	}
	hit := runOp(t, ch, "wait", "b", `{"topics": ["build.*"], "timeout": 5}`)
	if kind := jsonField(t, hit, "kind"); kind == "" {
		t.Fatalf("wait = %#v", hit)
	}
	runOp(t, ch, "ack", "b", `{"seq": `+jsonField(t, hit, "seq")+`}`)
	runOp(t, ch, "gate", "a", `{"gate": "ask", "why": "the schema changes"}`)
	runOp(t, ch, "next", "a", `{}`)

	// the plan: create, update, status, wait, finish, and the notes and edits around them
	runOp(t, ch, "task_create", "a", `{"id": "t1", "title": "Ship the board", "owner": "b", "after": [], "what": "the board"}`)
	runOp(t, ch, "task_create", "a", `{"id": "t0", "title": "Write the brief"}`)
	runOp(t, ch, "task_update", "a", `{"id": "t1", "owner": "a"}`)
	runOp(t, ch, "task_status", "a", `{"id": "t0", "status": "doing"}`)
	tasks := runOp(t, ch, "tasks", "a", "")
	if !strings.Contains(mustJSONT(t, tasks), `"id":"t1"`) {
		t.Fatalf("tasks = %#v", tasks)
	}
	task := runOp(t, ch, "task", "a", `{"id": "t1"}`)
	if !strings.Contains(mustJSONT(t, task), `"what":"the board"`) {
		t.Fatalf("the task hides its body: %#v", task)
	}
	runOp(t, ch, "note", "a", `{"id": "t1", "kind": "question", "body": "dark mode?"}`)
	view, err := ch.Task(bg, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Comments) == 0 {
		t.Fatalf("the note never landed: %#v", view)
	}
	runOp(t, ch, "note_edit", Owner, `{"id": `+strconv.FormatInt(view.Comments[0].ID, 10)+
		`, "body": "dark mode too?", "resolved": true}`)
	runOp(t, ch, "task_create", "a", `{"id": "t3", "title": "Announce"}`)
	// the blocking ops run with a short timeout: the wake-up itself has its own cases
	runOp(t, ch, "depend", "a", `{"owner": "b", "title": "Gather the ports", "id": "t3", "timeout": 1}`)
	runOp(t, ch, "start", "a", `{"id": "t1"}`)
	runOp(t, ch, "wait_task", "a", `{"id": "t1", "timeout": 1}`)
	runOp(t, ch, "finish", "a", `{"id": "t0", "note": "the brief is out", "result": "docs/brief.md", "title": "the brief"}`)
	runOp(t, ch, "handoff", "a", `{"to": "b", "msg": "take the announcement", "wait": false}`)

	// knowledge and the digests over everything above
	entry := runOp(t, ch, "remember", "a", `{"kind": "fact", "title": "the port", "body": "18787", "tags": ["ops"]}`)
	kbID := jsonField(t, entry, "id")
	if items := runOp(t, ch, "recall", "a", `{"q": "port"}`); !strings.Contains(mustJSONT(t, items), "the port") {
		t.Fatalf("recall = %#v", items)
	}
	runOp(t, ch, "kb", "a", `{"id": `+kbID+`}`)
	runOp(t, ch, "verify", "a", `{"id": `+kbID+`}`)
	// share needs the server-wide store a lone channel has not: its refusal still runs the op
	if _, err := RunOp(bg, "share", ch, "a", vv(t, `{"id": `+kbID+`}`)); err == nil ||
		!strings.Contains(err.Error(), "no server-wide knowledge") {
		t.Fatalf("share without the server-wide store = %v", err)
	}
	runOp(t, ch, "digest", "a", `{"since": "24h"}`)
	runOp(t, ch, "brief", Owner, `{"session": "b", "msg": "pick up the announcement"}`)
	runOp(t, ch, "assign", Owner, `{"session": "b", "task": "t1"}`)
	runOp(t, ch, "touched", "a", `{"repo": "/r/shop", "path": "a.ts"}`)
	runOp(t, ch, "map", "a", `{"repo": "/r/shop", "files": ["a.ts", "b.ts"]}`)
	runOp(t, ch, "ask_wait", "a", `{"to": "b", "msg": "still there?", "timeout": 1}`)
	runOp(t, ch, "leave", "b", `{"summary": "b is done for now"}`)

	// a task view marshals as the flattened body with the review's tail
	view, err = ch.Task(bg, "t1")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"id":"t1"`, `"title":"Ship the board"`, `"what":"the board"`, `"comments":[{"id":1`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("the task view = %s, want %s in it", encoded, want)
		}
	}
}

func jsonField(t *testing.T, v any, key string) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %#v: %v", v, err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	switch field := m[key].(type) {
	case string:
		return field
	case float64:
		return strconv.FormatInt(int64(field), 10)
	}
	t.Fatalf("%s has no %s", raw, key)
	return ""
}

func mustJSONT(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %#v: %v", v, err)
	}
	return string(raw)
}

// TestBlockingOpsAreTheTablesFact pins Blocks to the table: the ops that block wait by design
// and get the transports' long timeouts, every other op answers at once, and an unknown name is
// not blocking.
func TestBlockingOpsAreTheTablesFact(t *testing.T) {
	for _, name := range []string{"wait", "wait_task", "gate", "depend", "handoff", "ask_wait"} {
		if !Blocks(name) {
			t.Errorf("Blocks(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"publish", "status", "join"} {
		if Blocks(name) {
			t.Errorf("Blocks(%q) = true, want false", name)
		}
	}
	if Blocks("no_such_op") {
		t.Error(`Blocks("no_such_op") = true, want false`)
	}
}
