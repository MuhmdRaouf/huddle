package channel

import (
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// Reads the channel cases cover only in passing: the one-line views, the note editor, the events
// filters and the take/gate fast paths.
func TestChannelViews(t *testing.T) {
	t.Run("stats counts events, tasks, sessions, knowledge and open asks", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: "t1", Title: "one"}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.SetStatus(bg, "a", "t1", "done", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Remember(bg, "a", RememberInput{Kind: "fact", Title: "t", Body: "b"}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Send(bg, "a", nil, "all ok?", SendOpts{Ask: true}); err != nil {
			t.Fatal(err)
		}
		s, err := ch.Stats(bg)
		if err != nil {
			t.Fatal(err)
		}
		if s.Events == 0 || s.Tasks != 1 || s.Done != 1 || s.Sessions != 2 || s.Knowledge != 1 || s.OpenAsks != 1 || s.Last == 0 {
			t.Errorf("stats = %+v", s)
		}
	})

	t.Run("review lists open notes, edits and moved titles", func(t *testing.T) {
		ch := joinedChannel(t)
		plan := vv(t, `{"phases": [{"n": 0, "title": "p", "steps": [{"id": "m1", "title": "was"}]}]}`)
		if _, err := ch.ImportPlan(bg, Owner, planValue(t, plan), ImportOpts{}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Note(bg, Owner, "m1", "question", "sure?"); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.UpdateTask(bg, Owner, "m1", UpdateTaskInput{Field: "what", Value: vv(t, `"else"`)}); err != nil {
			t.Fatal(err)
		}
		renamed := vv(t, `{"phases": [{"n": 0, "title": "p", "steps": [{"id": "m1", "title": "now"}]}]}`)
		if _, err := ch.ImportPlan(bg, Owner, planValue(t, renamed), ImportOpts{}); err != nil {
			t.Fatal(err)
		}
		r, err := ch.Review(bg)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Open) != 1 || r.Open[0].Body != "sure?" {
			t.Errorf("open notes = %+v", r.Open)
		}
		if len(r.Edits) != 1 || r.Edits[0].Field != "what" {
			t.Errorf("edits = %+v", r.Edits)
		}
		if !strings.Contains(r.Moved.EncodeJSON(), `"id":"m1"`) {
			t.Errorf("moved = %s", r.Moved.EncodeJSON())
		}
	})

	t.Run("editNote resolves, rewrites and removes a note", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: "n1", Title: "n"}); err != nil {
			t.Fatal(err)
		}
		tk, err := ch.Note(bg, "a", "n1", "question", "first")
		if err != nil {
			t.Fatal(err)
		}
		if len(tk.Comments) != 1 || len(tk.OpenNotes) != 1 {
			t.Fatalf("notes = %+v", tk.Comments)
		}
		id := tk.Comments[0].ID
		resolved := true
		tk, err = ch.EditNote(bg, id, EditNoteInput{Resolved: &resolved})
		if err != nil || len(tk.OpenNotes) != 0 {
			t.Fatalf("open after resolve = %v (%v)", tk, err)
		}
		tk, err = ch.EditNote(bg, id, EditNoteInput{Body: sp("rewritten")})
		if err != nil || tk.Comments[0].Body != "rewritten" {
			t.Fatalf("note after edit = %+v (%v)", tk.Comments[0], err)
		}
		if _, err := ch.EditNote(bg, id, EditNoteInput{Remove: true}); err != nil {
			t.Fatal(err)
		}
		tk, err = ch.Task(bg, "n1")
		if err != nil || len(tk.Comments) != 0 {
			t.Fatalf("notes after remove = %+v (%v)", tk.Comments, err)
		}
		if _, err := ch.EditNote(bg, 9999, EditNoteInput{Remove: true}); errStatus(err) != 404 {
			t.Errorf("missing note status = %d (%v), want 404", errStatus(err), err)
		}
	})

	t.Run("events filters by after, before, from, limit and topic", func(t *testing.T) {
		ch := joinedChannel(t)
		for _, topic := range []string{"n.1", "n.2", "n.3"} {
			if _, err := ch.Publish(bg, "a", PublishArgs{Topic: topic, Msg: "m"}); err != nil {
				t.Fatal(err)
			}
		}
		last, err := ch.LastSeq(bg)
		if err != nil {
			t.Fatal(err)
		}
		all, err := ch.Events(bg, EventsFilter{})
		if err != nil || len(all) == 0 {
			t.Fatalf("events = %v (%v)", all, err)
		}
		recent, err := ch.Events(bg, EventsFilter{After: ip(last - 1)})
		if err != nil || len(recent) != 1 {
			t.Fatalf("events after = %v (%v)", recent, err)
		}
		old, err := ch.Events(bg, EventsFilter{Before: ip(last)})
		if err != nil || len(old) == 0 || old[len(old)-1].Seq >= last {
			t.Fatalf("events before = %v (%v)", old, err)
		}
		mine, err := ch.Events(bg, EventsFilter{From: sp("b")})
		if err != nil || len(mine) != 1 || mine[0].From != "b" {
			t.Fatalf("events from b = %v (%v)", mine, err)
		}
		limited, err := ch.Events(bg, EventsFilter{Limit: ip(2)})
		if err != nil || len(limited) != 2 || limited[0].Seq > limited[1].Seq {
			t.Fatalf("limited events = %v (%v)", limited, err)
		}
		globbed, err := ch.Events(bg, EventsFilter{Topic: "n.*"})
		if err != nil || len(globbed) != 3 {
			t.Fatalf("globbed events = %v (%v)", globbed, err)
		}
	})

	t.Run("takeTurn takes the turn; gate answers at once while running", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := ch.TakeTurn(bg, "a", ""); err != nil {
			t.Fatal(err)
		}
		turn, err := ch.Turn(bg)
		if err != nil || turn.Holder == nil || *turn.Holder != "a" {
			t.Fatalf("holder = %v (%v), want a", turn.Holder, err)
		}
		g, err := ch.Gate(bg, "a", 0)
		if err != nil || g.Control != "run" {
			t.Fatalf("gate = %+v (%v)", g, err)
		}
		if _, err := ch.Gate(bg, "nobody", 0); errStatus(err) != 404 {
			t.Errorf("gate of a stranger = %d (%v), want 404", errStatus(err), err)
		}
	})

	t.Run("recall filters by kind, by and tag", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := ch.Join(bg, "a.sub", JoinOpts{}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Remember(bg, "a", RememberInput{Kind: "fact", Title: "ports", Body: "6379", Tags: []string{"ops"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Remember(bg, "a.sub", RememberInput{Kind: "lesson", Title: "pipes", Body: "grep"}); err != nil {
			t.Fatal(err)
		}
		items, err := ch.Recall(bg, "", RecallOpts{Kind: "lesson"})
		if err != nil || len(items) != 1 || items[0].By != "a.sub" {
			t.Fatalf("recall(lesson) = %+v (%v)", items, err)
		}
		items, err = ch.Recall(bg, "", RecallOpts{By: "a"})
		if err != nil || len(items) != 2 {
			t.Fatalf("recall(by a) = %+v (%v), want the subagent's too", items, err)
		}
		items, err = ch.Recall(bg, "", RecallOpts{Tag: "ops"})
		if err != nil || len(items) != 1 || items[0].Title != "ports" {
			t.Fatalf("recall(ops) = %+v (%v)", items, err)
		}
	})

	t.Run("phases list their steps' body, flattened beside n and title", func(t *testing.T) {
		ch := joinedChannel(t)
		plan := vv(t, `{"phases": [{"n": 0, "title": "p", "goal": "ship", "steps": [{"id": "p1", "title": "one"}]}]}`)
		if _, err := ch.ImportPlan(bg, Owner, planValue(t, plan), ImportOpts{}); err != nil {
			t.Fatal(err)
		}
		ph, err := ch.Phases(bg)
		if err != nil || len(ph) != 1 {
			t.Fatalf("phases = %+v (%v)", ph, err)
		}
		if ph[0].Title != "p" || !strings.Contains(ph[0].Body.EncodeJSON(), `"goal":"ship"`) {
			t.Errorf("phase = %+v", ph[0])
		}
		encoded, err := domain.MarshalCompactJSON(ph)
		if err != nil {
			t.Fatal(err)
		}
		if want := `[{"n":0,"title":"p","goal":"ship"}]`; string(encoded) != want {
			t.Errorf("phases = %s, want %s", encoded, want)
		}
	})

	t.Run("a channel with no member list holds an empty one, and a note its 0", func(t *testing.T) {
		ch := joinedChannel(t)
		encoded, err := domain.MarshalCompactJSON(ch.Config())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(encoded), `"members":[]`) {
			t.Errorf("config = %s, want members as an empty array", encoded)
		}
		if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: "n1", Title: "one"}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Note(bg, Owner, "n1", "question", "sure?"); err != nil {
			t.Fatal(err)
		}
		task, err := ch.Task(bg, "n1")
		if err != nil || task == nil {
			t.Fatalf("task n1: %v", err)
		}
		notes, err := domain.MarshalCompactJSON(task.Comments)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(notes), `"resolved":0`) {
			t.Errorf("comments = %s, want the stored 0", notes)
		}
		resolved := true
		if _, err := ch.EditNote(bg, 1, EditNoteInput{Resolved: &resolved}); err != nil {
			t.Fatal(err)
		}
		task, _ = ch.Task(bg, "n1")
		notes, _ = domain.MarshalCompactJSON(task.Comments)
		if !strings.Contains(string(notes), `"resolved":1`) {
			t.Errorf("comments = %s, want the stored 1", notes)
		}
	})

	t.Run("a session that left holds no next task in the attention view", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: "l1", Title: "one", Owner: sp("a")}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Leave(bg, "b", "done here"); err != nil {
			t.Fatal(err)
		}
		at, err := ch.Attention(bg)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range at.Next {
			if n.Session == "b" {
				t.Errorf("attention.next holds the left session b: %+v", at.Next)
			}
		}
	})

	t.Run("a bad session name names the rule with its delimiters", func(t *testing.T) {
		ch := openChannel(t, "t")
		_, err := ch.Join(bg, "Not-a-name", JoinOpts{})
		if err == nil || !strings.Contains(err.Error(), "must match /^[a-z]") {
			t.Errorf("join(Not-a-name) = %v, want the rule with its slashes", err)
		}
	})

	t.Run("link maps a Claude session to its top-level Huddle session", func(t *testing.T) {
		ch := joinedChannel(t)
		Link(bg, ch, "b.sub", "claude-1")
		Link(bg, ch, "a", "claude-2")
		Link(bg, ch, "a", "not a session id") // refused: not a Claude session id
		Link(bg, ch, "owner", "claude-3")     // refused: the owner
		l, err := Links(bg, ch)
		if err != nil {
			t.Fatal(err)
		}
		if len(l) != 2 || l["claude-1"] != "b" || l["claude-2"] != "a" {
			t.Errorf("links = %v", l)
		}
	})

	t.Run("lastEdit answers the newest touch of a ref, and repoName the panel's label", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := Touched(bg, ch, "a", TouchArgs{Repo: "/r/shop", Path: "src/cart.ts"}); err != nil {
			t.Fatal(err)
		}
		at, err := LastEdit(bg, ch, "/r/shop/src/cart.ts", "2000-01-01T00:00:00Z")
		if err != nil || at == nil {
			t.Fatalf("lastEdit = %v (%v)", at, err)
		}
		if at, err = LastEdit(bg, ch, "/r/shop/other.ts", "2000-01-01T00:00:00Z"); err != nil || at != nil {
			t.Fatalf("lastEdit of an untouched ref = %v (%v)", at, err)
		}
		if RepoName("/r/shop/") != "shop" || RepoName("shop") != "shop" {
			t.Errorf("repoName labels are wrong")
		}
		if got := Ago(isoNow(time.Now())); got != "just now" && !strings.Contains(got, "min ago") {
			t.Errorf("ago = %q", got)
		}
	})

	t.Run("the task list filters by owner, status and phase", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: "t1", Title: "a's", Owner: sp("a"), Phase: ip(1)}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.CreateTask(bg, "b", CreateTaskInput{ID: "t2", Title: "b's", Owner: sp("b"), Phase: ip(2)}); err != nil {
			t.Fatal(err)
		}
		list, err := ch.Tasks(bg, TasksFilter{Owner: sp("a")})
		if err != nil || len(list) != 1 || list[0].ID != "t1" {
			t.Fatalf("owner filter: %v (%v)", list, err)
		}
		if list, err = ch.Tasks(bg, TasksFilter{Status: "todo", Phase: ip(2)}); err != nil || len(list) != 1 || list[0].ID != "t2" {
			t.Fatalf("status+phase filter: %v (%v)", list, err)
		}
		if list, err = ch.Tasks(bg, TasksFilter{Owner: sp("nobody")}); err != nil || len(list) != 0 {
			t.Fatalf("empty filter: %v (%v)", list, err)
		}
	})

	t.Run("a brief still shows knowledge when nothing matches its text", func(t *testing.T) {
		ch := openChannel(t, "t")
		if _, err := ch.Join(bg, "a", JoinOpts{Role: sp("builds")}); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"alpha", "beta", "gamma"} {
			if _, err := ch.Remember(bg, "a", RememberInput{Kind: "fact", Title: k, Body: k}); err != nil {
				t.Fatal(err)
			}
		}
		// no entry matches the text, so the picker backfills with the most-read entries
		kb, err := ch.relevant(bg, "something else entirely", 3)
		if err != nil {
			t.Fatal(err)
		}
		if len(kb) != 3 {
			t.Fatalf("the picker answered %d entries, want the 3 backfilled", len(kb))
		}
	})
}
