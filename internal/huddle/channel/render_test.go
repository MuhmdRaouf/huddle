package channel

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// The renders, validations and fallbacks the channel's own cases walk past: the text an agent
// reads, the configuration's guards, the plan's error reports and the search without its
// full-text index.
func TestChannelRenders(t *testing.T) {
	t.Run("configure validates its fields, and every field round-trips", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := ch.Configure(bg, ConfigureArgs{Start: sp("Bad Name")}); errStatus(err) != 400 {
			t.Errorf("bad start = %d (%v), want 400", errStatus(err), err)
		}
		if _, err := ch.Configure(bg, ConfigureArgs{Handover: vv(t, `{"a": {"to": "b"}}`)}); errStatus(err) != 400 {
			t.Errorf("handover without topics = %d (%v), want 400", errStatus(err), err)
		}
		if _, err := ch.Configure(bg, ConfigureArgs{Orchestrator: sp("a.sub")}); errStatus(err) != 400 {
			t.Errorf("subagent orchestrator = %d (%v), want 400", errStatus(err), err)
		}
		if _, err := ch.Configure(bg, ConfigureArgs{Members: vv(t, `["a", "a.sub"]`)}); errStatus(err) != 400 {
			t.Errorf("subagent member = %d (%v), want 400", errStatus(err), err)
		}
		cfg, err := ch.Configure(bg, ConfigureArgs{
			Title: sp("T"), Description: sp("D"), Start: sp("a"), Profile: sp("p"), Repo: sp("r"),
			Handover: vv(t, `{"a": {"to": "b", "topics": ["t.x"]}}`), Members: vv(t, `["a", "b"]`),
			Orchestrator: sp("a"),
		})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Title != "T" || cfg.Description != "D" || deref(cfg.Start) != "a" || deref(cfg.Profile) != "p" ||
			deref(cfg.Repo) != "r" || !slices.Equal(cfg.Members, []string{"a", "b"}) || deref(cfg.Orchestrator) != "a" ||
			!strings.Contains(cfg.Handover.EncodeJSON(), `"a":{"to":"b"`) {
			t.Errorf("config = %+v", cfg)
		}
		cfg, err = ch.Configure(bg, ConfigureArgs{Start: sp(""), Profile: sp(""), Repo: sp(""), Orchestrator: sp("")})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Start != nil || cfg.Profile != nil || cfg.Repo != nil || cfg.Orchestrator != nil {
			t.Errorf("cleared config = %+v", cfg)
		}
	})

	t.Run("the live fan-out reaches subscribers and survives a listener that panics", func(t *testing.T) {
		ch := joinedChannel(t)
		var got []Live
		cancel := ch.Subscribe(func(m Live) { got = append(got, m) })
		ch.Subscribe(func(Live) { panic("a broken bridge") })
		if _, err := ch.Publish(bg, "a", PublishArgs{Topic: "n.x", Msg: "m"}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Ack(bg, "b", 1); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Presence(bg, "b", PresenceInput{State: sp("idle")}); err != nil {
			t.Fatal(err)
		}
		var kinds []string
		for _, m := range got {
			kinds = append(kinds, m.Type)
			if m.Ch != "t" || m.At == "" {
				t.Errorf("live message = %+v", m)
			}
		}
		for _, want := range []string{"event", "ack", "presence"} {
			if !slices.Contains(kinds, want) {
				t.Errorf("live kinds = %v, want %s among them", kinds, want)
			}
		}
		// the cancelled listener is gone: a later publish reaches no one
		cancel()
		got = nil
		if _, err := ch.Publish(bg, "a", PublishArgs{Topic: "n.y", Msg: "m"}); err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("a cancelled listener still heard %d messages", len(got))
		}
	})

	t.Run("taskText renders a full task, and snapText a rich snapshot", func(t *testing.T) {
		ch := joinedChannel(t)
		full := vv(t, `{
			"what": "open the port", "why": "the proxy needs it", "use": "runbooks", "value": "443 answers",
			"rollback": "close it again", "risk": "low", "gate": "owner", "kind": "chore",
			"alternatives": [{"option": "raw socket", "why_not": "no tls"}],
			"how": ["open the firewall", "reload"],
			"snippets": [{"title": "rule", "path": "fw.conf", "proposed": true, "code": "allow 443\nretry"}],
			"verify": [{"cmd": "curl -s localhost", "expect": "ok"}]
		}`)
		if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: "rich", Title: "Rich task", Body: full}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: "dep", Title: "Dep"}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.UpdateTask(bg, "a", "rich", UpdateTaskInput{After: []string{"dep"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.UpdateTask(bg, Owner, "rich", UpdateTaskInput{Field: "what", Value: vv(t, `"edited what"`)}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Note(bg, Owner, "rich", "direction", "do it twice"); err != nil {
			t.Fatal(err)
		}
		tk, err := ch.Task(bg, "rich")
		if err != nil || tk == nil {
			t.Fatal(err)
		}
		text := taskText(tk, "t")
		for _, want := range []string{
			"rich  Rich task", "chore · low risk · gate owner", "WAITS ON: dep (unowned, todo)",
			"OWNER NOTES", "[direction] do it twice", "(edited: what)", "WHAT", "edited what",
			"WHY", "NOT THAT WAY", "raw socket: no tls", "HOW", "1. open the firewall",
			"CODE: rule (fw.conf) [proposed]", "allow 443", "CHECK", "$ curl -s localhost", "→ ok",
			"DONE MEANS\n443 answers", "USE\nrunbooks", "ROLLBACK\nclose it again", "depends on dep",
			"board: /#/c/t/t/rich",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("taskText misses %q:\n%s", want, text)
			}
		}
		// restoring the plan's text drops the edit
		if _, err := ch.UpdateTask(bg, Owner, "rich", UpdateTaskInput{Field: "what", Value: jnull()}); err != nil {
			t.Fatal(err)
		}
		tk, _ = ch.Task(bg, "rich")
		if text = taskText(tk, "t"); !strings.Contains(text, "WHAT\nopen the port") || strings.Contains(text, "(edited:") {
			t.Errorf("taskText after restore:\n%s", text)
		}
		// a fresh join with a brief, an inbox and a next task reads fully
		if _, err := ch.SetStatus(bg, "a", "dep", "done", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Send(bg, Owner, sp("b"), "your orders?", SendOpts{Ask: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Brief(bg, Owner, "b", "start with rich"); err != nil {
			t.Fatal(err)
		}
		last, _ := ch.LastSeq(bg)
		if _, err := ch.Ack(bg, "b", last); err != nil {
			t.Fatal(err)
		}
		snap, err := ch.Join(bg, "b", JoinOpts{Context: sp("fresh")})
		if err != nil {
			t.Fatal(err)
		}
		stext := snapText(snap)
		for _, want := range []string{
			"channel t · you are b · running", "turn: free (parallel)",
			"context: fresh (", "brief: t", "from owner (", "start with rich",
			"your tasks: none", "inbox: 1", "#", "from owner: your orders?",
			"unread: 0", "next task: rich Rich task", "knowledge: ", "entries", "others: a working",
		} {
			if !strings.Contains(stext, want) {
				t.Errorf("snapText misses %q:\n%s", want, stext)
			}
		}
		// a sync join says so, and the owner has no next task
		snap, err = ch.Join(bg, "b", JoinOpts{Context: sp("sync")})
		if err != nil {
			t.Fatal(err)
		}
		if stext = snapText(snap); !strings.Contains(stext, "context: sync") {
			t.Errorf("snapText for a sync join:\n%s", stext)
		}
		ownerSnap, err := ch.Snapshot(bg, Owner, SnapshotJoin{})
		if err != nil {
			t.Fatal(err)
		}
		if stext = snapText(ownerSnap); !strings.Contains(stext, "you are owner") || strings.Contains(stext, "next task:") {
			t.Errorf("snapText for the owner:\n%s", stext)
		}
	})

	t.Run("search and recall fall back to LIKE without the full-text index", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: "s1", Title: "Configure the firewall", Body: vv(t, `{"what": "open port 443 for the proxy"}`)}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Remember(bg, "a", RememberInput{Kind: "fact", Title: "ports and proxies", Body: "443 fronts the proxy", Tags: []string{"net"}}); err != nil {
			t.Fatal(err)
		}
		ch.FTS = false
		ch.Store.FTS = false
		hits, err := ch.Search(bg, "firewall")
		if err != nil || len(hits) != 1 || hits[0].ID != "s1" {
			t.Fatalf("LIKE search = %+v (%v)", hits, err)
		}
		if _, err := ch.Search(bg, "  "); err != nil {
			t.Fatalf("empty search: %v", err)
		}
		items, err := ch.Recall(bg, "proxy", RecallOpts{})
		if err != nil || len(items) != 1 || items[0].Title != "ports and proxies" {
			t.Fatalf("LIKE recall = %+v (%v)", items, err)
		}
		// a brief without the index still answers, from the most-read entries
		b, err := ch.briefFor(bg, "a", sp("proxies"), nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(b.Knowledge) == 0 || b.Knowledge[0].Title != "ports and proxies" {
			t.Errorf("brief knowledge without the index = %+v", b.Knowledge)
		}
	})

	t.Run("the plan import reports kept owner state and unknown dependencies", func(t *testing.T) {
		ch := joinedChannel(t)
		first := vv(t, `{"phases": [{"n": 0, "title": "p", "steps": [{"id": "k1", "title": "one"}, {"id": "k2", "title": "two"}]}]}`)
		if _, err := ch.ImportPlan(bg, Owner, planValue(t, first), ImportOpts{}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.SetStatus(bg, Owner, "k2", "blocked", "waiting"); err != nil {
			t.Fatal(err)
		}
		second := vv(t, `{"phases": [{"n": 0, "title": "p", "steps": [{"id": "k1", "title": "one", "depends": ["ghost"]}]}]}`)
		r, err := ch.ImportPlan(bg, Owner, planValue(t, second), ImportOpts{})
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(r.Errors, " | ")
		if !strings.Contains(joined, "k2 left the plan but has owner state; kept") {
			t.Errorf("errors = %q, want k2 kept", joined)
		}
		if !strings.Contains(joined, "k1 depends on unknown ghost") {
			t.Errorf("errors = %q, want the unknown dependency", joined)
		}
		if tk, _ := ch.Task(bg, "k2"); tk == nil {
			t.Error("k2 with owner state was removed")
		}
		empty := vv(t, `{}`)
		r, err = ch.ImportPlan(bg, Owner, planValue(t, empty), ImportOpts{})
		if err != nil || r.Phases != 0 || r.Tasks != 0 {
			t.Errorf("empty plan = %+v (%v)", r, err)
		}
	})

	t.Run("task creation and edits refuse what they must", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: strings.Repeat("x", 33), Title: "t"}); errStatus(err) != 400 {
			t.Errorf("long id = %d (%v), want 400", errStatus(err), err)
		}
		if _, err := ch.CreateTask(bg, "a", CreateTaskInput{Title: ""}); errStatus(err) != 400 {
			t.Errorf("no title = %d (%v), want 400", errStatus(err), err)
		}
		if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: "ghost", Title: "t", Owner: sp("nobody")}); errStatus(err) != 404 {
			t.Errorf("unknown owner = %d (%v), want 404", errStatus(err), err)
		}
		if _, err := ch.CreateTask(bg, "a", CreateTaskInput{Title: "auto id"}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: "autoid", Title: "t"}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: "autoid", Title: "t"}); errStatus(err) != 409 {
			t.Errorf("existing id = %d (%v), want 409", errStatus(err), err)
		}
		if _, err := ch.UpdateTask(bg, "a", "missing", UpdateTaskInput{}); errStatus(err) != 404 {
			t.Errorf("missing task = %d (%v), want 404", errStatus(err), err)
		}
		if _, err := ch.UpdateTask(bg, "a", "autoid", UpdateTaskInput{Field: "seq", Value: vv(t, `1`)}); errStatus(err) != 400 {
			t.Errorf("uneditable field = %d (%v), want 400", errStatus(err), err)
		}
		if _, err := ch.SetStatus(bg, "a", "autoid", "nope", ""); errStatus(err) != 400 {
			t.Errorf("bad status = %d (%v), want 400", errStatus(err), err)
		}
		if _, err := ch.SetStatus(bg, "a", "missing", "done", ""); errStatus(err) != 404 {
			t.Errorf("missing status move = %d (%v), want 404", errStatus(err), err)
		}
		if _, err := ch.Note(bg, "a", "autoid", "diary", "x"); errStatus(err) != 400 {
			t.Errorf("bad note kind = %d (%v), want 400", errStatus(err), err)
		}
		if _, err := ch.Note(bg, "a", "autoid", "note", "  "); errStatus(err) != 400 {
			t.Errorf("empty note = %d (%v), want 400", errStatus(err), err)
		}
		if _, err := ch.Note(bg, "a", "missing", "note", "x"); errStatus(err) != 404 {
			t.Errorf("note on missing task = %d (%v), want 404", errStatus(err), err)
		}
	})

	t.Run("remember refuses what it must, and keeps refs", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := ch.Remember(bg, "a", RememberInput{Kind: "gossip", Title: "t", Body: "b"}); errStatus(err) != 400 {
			t.Errorf("bad kind = %d (%v), want 400", errStatus(err), err)
		}
		if _, err := ch.Remember(bg, "a", RememberInput{Kind: "fact", Title: " ", Body: "b"}); errStatus(err) != 400 {
			t.Errorf("empty title = %d (%v), want 400", errStatus(err), err)
		}
		if _, err := ch.Remember(bg, "a", RememberInput{Kind: "fact", Title: "t", Body: strings.Repeat("x", 50001)}); errStatus(err) != 400 {
			t.Errorf("long body = %d (%v), want 400", errStatus(err), err)
		}
		if _, err := ch.Remember(bg, "a", RememberInput{Kind: "fact", Title: "t", Body: "b", Task: sp("missing")}); errStatus(err) != 404 {
			t.Errorf("unknown task = %d (%v), want 404", errStatus(err), err)
		}
		if _, err := ch.Remember(bg, "stranger", RememberInput{Kind: "fact", Title: "t", Body: "b"}); errStatus(err) != 404 {
			t.Errorf("stranger = %d (%v), want 404", errStatus(err), err)
		}
		k, err := ch.Remember(bg, "a", RememberInput{Kind: "howto", Title: "  trim me  ", Body: " b ", Refs: []string{"a.go"}})
		if err != nil {
			t.Fatal(err)
		}
		if k.Title != "trim me" || k.Body != "b" || !slices.Equal(k.Refs, []string{"a.go"}) {
			t.Errorf("entry = %+v", k)
		}
		if _, err := ch.KB(bg, 9999); errStatus(err) != 404 {
			t.Errorf("missing entry = %d (%v), want 404", errStatus(err), err)
		}
	})

	t.Run("small guards: ack, event, presence and the owner's control", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := ch.Ack(bg, "b", -1); errStatus(err) != 400 {
			t.Errorf("negative ack = %d (%v), want 400", errStatus(err), err)
		}
		if e, err := ch.Event(bg, 9999); err != nil || e != nil {
			t.Errorf("missing event = %v %v", e, err)
		}
		if p, err := ch.PresenceOf(bg, "stranger"); err != nil || p != nil {
			t.Errorf("stranger presence = %v %v", p, err)
		}
		if st, err := ch.ControlOf(bg, Owner); err != nil || st != "run" {
			t.Errorf("owner control = %q %v", st, err)
		}
		if _, err := ch.Control(bg, "a", Owner, "pause", ""); errStatus(err) != 400 {
			t.Errorf("pausing the owner = %d (%v), want 400", errStatus(err), err)
		}
		if _, err := ch.Publish(bg, "stranger", PublishArgs{Topic: "x.y", Msg: "m"}); errStatus(err) != 404 {
			t.Errorf("stranger publish = %d (%v), want 404", errStatus(err), err)
		}
		if _, err := ch.Publish(bg, "a", PublishArgs{Topic: "task.done", Msg: "m"}); errStatus(err) != 400 {
			t.Errorf("reserved topic = %d (%v), want 400", errStatus(err), err)
		}
		if _, err := ch.Publish(bg, "a", PublishArgs{Topic: "x.y", Msg: ""}); errStatus(err) != 400 {
			t.Errorf("empty msg = %d (%v), want 400", errStatus(err), err)
		}
		if _, err := ch.Publish(bg, "a", PublishArgs{Topic: "x.y", Msg: "m", To: sp("stranger")}); errStatus(err) != 404 {
			t.Errorf("unknown recipient = %d (%v), want 404", errStatus(err), err)
		}
		if _, err := ch.Send(bg, "a", sp("stranger"), "m", SendOpts{}); errStatus(err) != 404 {
			t.Errorf("send to a stranger = %d (%v), want 404", errStatus(err), err)
		}
		if _, err := ch.Send(bg, "a", nil, "", SendOpts{}); errStatus(err) != 400 {
			t.Errorf("empty send = %d (%v), want 400", errStatus(err), err)
		}
		if _, err := ch.Reply(bg, "a", 9999, "x"); errStatus(err) != 404 {
			t.Errorf("reply to nothing = %d (%v), want 404", errStatus(err), err)
		}
		if _, err := ch.Wait(bg, "b", []string{"BAD"}, 0); errStatus(err) != 400 {
			t.Errorf("bad glob = %d (%v), want 400", errStatus(err), err)
		}
		if _, err := ch.WaitTask(bg, "b", "missing", 0); errStatus(err) != 404 {
			t.Errorf("wait for a missing task = %d (%v), want 404", errStatus(err), err)
		}
		// the owner appends straight to the timeline (the approval request's shape)
		if _, err := ch.Append(bg, Owner, evIn{topic: "approval.request", to: sp(Owner), msg: sp("asks"), data: vv(t, `{"rules": ["force_push"]}`)}); err != nil {
			t.Fatal(err)
		}
		evs, err := ch.Events(bg, EventsFilter{Topic: "approval.request"})
		if err != nil || len(evs) != 1 {
			t.Fatalf("approval events = %v (%v)", evs, err)
		}
	})

	t.Run("topic globs match segments and Ago reads its clocks", func(t *testing.T) {
		for _, c := range []struct {
			topic string
			glob  string
			want  bool
		}{
			{"a.b", "a.*", true}, {"a.b", "*.b", true}, {"a.b", "a.b", true},
			{"a.b", "a.b.c", false}, {"a.x.b", "a.*.b", true}, {"a.b", "*", true},
			{"ab", "a*", true}, {"a.b.c", "a.*", true}, {"a.b", "*.*", true}, {"a.b", "b.*", false},
		} {
			if got := GlobMatch(c.topic, []string{c.glob}); got != c.want {
				t.Errorf("GlobMatch(%q, %q) = %v", c.topic, c.glob, got)
			}
		}
		if Ago("not a date") != "just now" {
			t.Errorf("Ago(bad) = %q", Ago("not a date"))
		}
		if got := agoAt("2020-01-01T00:00:00Z", timeUnixMs(2020, 1, 2)); !strings.Contains(got, "h ago") && !strings.Contains(got, "min ago") {
			t.Errorf("agoAt of an old stamp = %q", got)
		}
	})

	t.Run("ops read a string timeout and answer missing operations", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: "w1", Title: "w"}); err != nil {
			t.Fatal(err)
		}
		r, err := RunOp(bg, "handoff", ch, "a", vv(t, `{"to": "b", "msg": "go", "wait": false, "timeout": "2"}`))
		if err != nil {
			t.Fatal(err)
		}
		if r.Result.(HandoffResult).Task != nil {
			t.Errorf("handoff without a title created a task: %+v", r.Result)
		}
		if _, err := RunOp(bg, "nope", ch, "a", nil); errStatus(err) != 404 {
			t.Errorf("unknown op = %d (%v), want 404", errStatus(err), err)
		}
		if _, err := RunOp(bg, "handoff", ch, "a", vv(t, `{"to": "b"}`)); errStatus(err) != 400 {
			t.Errorf("missing msg = %d (%v), want 400", errStatus(err), err)
		}
		if id, err := Identity("a", nil); err != nil || id != "a" {
			t.Errorf("identity(a, nil) = %q %v", id, err)
		}
		if _, err := Identity("a", sp(Owner)); errStatus(err) != 403 {
			t.Errorf("identity(a, owner) = %d (%v), want 403: only the owner acts as anyone", errStatus(err), err)
		}
		if id, err := Identity(Owner, sp("b")); err != nil || id != "b" {
			t.Errorf("identity(owner, b) = %q %v", id, err)
		}
		if _, err := Identity("a", sp("bad identity")); errStatus(err) != 400 {
			t.Errorf("bad identity = %d (%v), want 400", errStatus(err), err)
		}
		if _, err := RunOp(bg, "state", ch, "a", vv(t, `{"state": "working", "as": "owner"}`)); errStatus(err) != 403 {
			t.Errorf("acting as the owner = %d (%v), want 403", errStatus(err), err)
		}
	})
}

// timeUnixMs answers one timestamp, for Ago's older branches.
func timeUnixMs(year, month, day int) int64 {
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC).UnixMilli()
}
