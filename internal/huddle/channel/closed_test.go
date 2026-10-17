package channel

import (
	"errors"
	"testing"
)

// A closed channel answers every read and write with an error, never a panic: the server closes
// channels on shutdown while callers may still hold them.
func TestClosedChannel(t *testing.T) {
	ch := openChannel(t, "t")
	if _, err := ch.Join(bg, "a", JoinOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: "c1", Title: "c"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ch.Remember(bg, "a", RememberInput{Kind: "fact", Title: "t", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	// prime the seen-write throttle so later ops walk straight into their run
	for _, who := range []string{"a", Owner} {
		if _, err := RunOp(bg, "status", ch, who, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := ch.Close(bg); err != nil {
		t.Fatal(err)
	}
	calls := map[string]func() error{
		"join":       func() error { _, err := ch.Join(bg, "a", JoinOpts{}); return err },
		"leave":      func() error { _, err := ch.Leave(bg, "a", ""); return err },
		"presence":   func() error { _, err := ch.Presence(bg, "a", PresenceInput{State: sp("idle")}); return err },
		"assign":     func() error { _, err := ch.Assign(bg, Owner, "a", nil, ""); return err },
		"brief":      func() error { _, err := ch.Brief(bg, Owner, "a", "m"); return err },
		"sessions":   func() error { _, err := ch.Sessions(bg); return err },
		"control":    func() error { _, err := ch.Control(bg, "a", "a", "pause", ""); return err },
		"gate":       func() error { _, err := ch.Gate(bg, "a", 0); return err },
		"publish":    func() error { _, err := ch.Publish(bg, "a", PublishArgs{Topic: "x.y", Msg: "m"}); return err },
		"send":       func() error { _, err := ch.Send(bg, "a", nil, "m", SendOpts{}); return err },
		"reply":      func() error { _, err := ch.Reply(bg, "a", 1, "m"); return err },
		"events":     func() error { _, err := ch.Events(bg, EventsFilter{}); return err },
		"unread":     func() error { _, err := ch.Unread(bg, "a"); return err },
		"inbox":      func() error { _, err := ch.Inbox(bg, "a"); return err },
		"ack":        func() error { _, err := ch.Ack(bg, "a", 1); return err },
		"waitTask":   func() error { _, err := ch.WaitTask(bg, "a", "c1", 0); return err },
		"waitReply":  func() error { _, err := ch.WaitReply(bg, "a", 1, 0); return err },
		"passTurn":   func() error { _, err := ch.PassTurn(bg, "a", "a", ""); return err },
		"takeTurn":   func() error { _, err := ch.TakeTurn(bg, "a", ""); return err },
		"replayTurn": func() error { return ch.ReplayTurn(bg) },
		"task":       func() error { _, err := ch.Task(bg, "c1"); return err },
		"tasks":      func() error { _, err := ch.Tasks(bg, TasksFilter{}); return err },
		"next":       func() error { _, _, err := ch.Next(bg, "a"); return err },
		"createTask": func() error { _, err := ch.CreateTask(bg, "a", CreateTaskInput{Title: "t"}); return err },
		"updateTask": func() error {
			_, err := ch.UpdateTask(bg, "a", "c1", UpdateTaskInput{Field: "what", Value: vv(t, `"x"`)})
			return err
		},
		"setStatus":  func() error { _, err := ch.SetStatus(bg, "a", "c1", "done", ""); return err },
		"current":    func() error { _, err := ch.Current(bg, "a"); return err },
		"note":       func() error { _, err := ch.Note(bg, "a", "c1", "note", "b"); return err },
		"phases":     func() error { _, err := ch.Phases(bg); return err },
		"importPlan": func() error { _, err := ch.ImportPlan(bg, Owner, planValue(t, vv(t, `{}`)), ImportOpts{}); return err },
		"reindex":    func() error { return ch.Reindex(bg, nil) },
		"search":     func() error { _, err := ch.Search(bg, "x"); return err },
		"review":     func() error { _, err := ch.Review(bg); return err },
		"remember": func() error {
			_, err := ch.Remember(bg, "a", RememberInput{Kind: "fact", Title: "t", Body: "b"})
			return err
		},
		"recall":    func() error { _, err := ch.Recall(bg, "t", RecallOpts{}); return err },
		"kb":        func() error { _, err := ch.KB(bg, 1); return err },
		"snapshot":  func() error { _, err := ch.Snapshot(bg, "a", SnapshotJoin{}); return err },
		"attention": func() error { _, err := ch.Attention(bg); return err },
		"approve":   func() error { _, err := ch.Approve(bg, "c1", "", Owner); return err },
		"map":       func() error { _, err := ch.Map(bg); return err },
		"stats":     func() error { _, err := ch.Stats(bg); return err },
		"retired":   func() error { _, err := ch.Retired(bg); return err },
		"touched":   func() error { _, err := Touched(bg, ch, "a", TouchArgs{Repo: "/r", Path: "p"}); return err },
		"conflicts": func() error { _, err := Conflicts(bg, ch); return err },
		"lastEdit":  func() error { _, err := LastEdit(bg, ch, "p", "2000-01-01T00:00:00Z"); return err },
		"links":     func() error { _, err := Links(bg, ch); return err },
		"editNote":  func() error { _, err := ch.EditNote(bg, 1, EditNoteInput{Remove: true}); return err },
		"configure": func() error { _, err := ch.Configure(bg, ConfigureArgs{Title: sp("x")}); return err },
		"joinOp":    func() error { _, err := RunOp(bg, "status", ch, "a", nil); return err },
		"startOp":   func() error { _, err := RunOp(bg, "start", ch, "a", nil); return err },
		"finishOp":  func() error { _, err := RunOp(bg, "finish", ch, "a", vv(t, `{"id": "c1", "note": "n"}`)); return err },
		"mapOp":     func() error { _, err := RunOp(bg, "map", ch, "a", nil); return err },
		"briefOp": func() error {
			_, err := RunOp(bg, "brief", ch, Owner, vv(t, `{"session": "a", "msg": "m"}`))
			return err
		},
		"approveOp":   func() error { _, err := RunOp(bg, "approve", ch, Owner, vv(t, `{"id": "c1"}`)); return err },
		"importOp":    func() error { _, err := RunOp(bg, "import_plan", ch, Owner, vv(t, `{"plan": {}}`)); return err },
		"configureOp": func() error { _, err := RunOp(bg, "configure", ch, Owner, vv(t, `{"title": "x"}`)); return err },
		"stateOp":     func() error { _, err := RunOp(bg, "state", ch, "a", vv(t, `{"state": "working"}`)); return err },
		"assignOp": func() error {
			_, err := RunOp(bg, "assign", ch, Owner, vv(t, `{"session": "a", "context": "fresh"}`))
			return err
		},
		"dependOp": func() error {
			_, err := RunOp(bg, "depend", ch, "a", vv(t, `{"owner": "a", "title": "t"}`))
			return err
		},
		"handoffOp": func() error { _, err := RunOp(bg, "handoff", ch, "a", vv(t, `{"to": "a", "msg": "m"}`)); return err },
		"askWaitOp": func() error { _, err := RunOp(bg, "ask_wait", ch, "a", vv(t, `{"to": "a", "msg": "m"}`)); return err },
		"touchedOp": func() error {
			_, err := RunOp(bg, "touched", ch, "a", vv(t, `{"repo": "/r", "path": "p"}`))
			return err
		},
	}
	for name, call := range calls {
		if err := call(); err == nil {
			t.Errorf("%s on a closed channel answered nil", name)
		} else if errors.Is(err, err) == false {
			t.Errorf("%s returned a non-error", name)
		}
	}
}
