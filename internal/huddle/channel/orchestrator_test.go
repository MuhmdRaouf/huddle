package channel

import (
	"slices"
	"strings"
	"testing"
)

// The orchestrator, the map and the two join contexts (fresh and sync).
func TestOrchestrator(t *testing.T) {
	newChannel := func(t *testing.T) *Channel {
		t.Helper()
		ch := openChannel(t, "t")
		for _, j := range []struct{ name, role string }{
			{"lead", "runs the plan"}, {"api", "builds the orders API"}, {"web", "builds the client"},
		} {
			if _, err := ch.Join(bg, j.name, JoinOpts{Role: sp(j.role)}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := ch.Configure(bg, ConfigureArgs{Title: sp("Shop"), Description: sp("ship orders end to end"), Orchestrator: sp("lead")}); err != nil {
			t.Fatal(err)
		}
		return ch
	}

	t.Run("a fresh join skips the backlog but keeps open asks; sync replays it", func(t *testing.T) {
		ch := newChannel(t)
		for i := range 5 {
			if _, err := ch.Publish(bg, "web", PublishArgs{Topic: "note.x", Msg: "n" + string(rune('0'+i))}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := ch.Send(bg, "web", sp("api"), "which port?", SendOpts{Ask: true}); err != nil {
			t.Fatal(err)
		}
		f, err := ch.Join(bg, "api", JoinOpts{Context: sp("fresh")})
		if err != nil {
			t.Fatal(err)
		}
		if f.Context == nil || *f.Context != "fresh" {
			t.Fatalf("context = %v, want fresh", f.Context)
		}
		if f.Skipped < 6 {
			t.Errorf("skipped = %d, want at least 6", f.Skipped)
		}
		if f.UnreadTotal != 0 {
			t.Errorf("unread_total = %d, want 0", f.UnreadTotal)
		}
		if msgs := inboxMsgs(f.Inbox); !slices.Equal(msgs, []string{"which port?"}) {
			t.Errorf("inbox = %v, want which port?", msgs)
		}
		if f.Brief == nil {
			t.Fatal("fresh join got no brief")
		}
		if msgs := inboxMsgs(f.Brief.Asks); !slices.Equal(msgs, []string{"which port?"}) {
			t.Fatalf("brief asks = %v, want which port?", msgs)
		}
		if _, err := ch.Publish(bg, "web", PublishArgs{Topic: "note.y", Msg: "later"}); err != nil {
			t.Fatal(err)
		}
		s, err := ch.Join(bg, "api", JoinOpts{Context: sp("sync")})
		if err != nil {
			t.Fatal(err)
		}
		if s.Context == nil || *s.Context != "sync" || s.Brief != nil {
			t.Fatalf("sync join = %+v", s)
		}
		var got []string
		for _, e := range s.Unread {
			if e.Msg != nil {
				got = append(got, *e.Msg)
			}
		}
		if !slices.Equal(got, []string{"later"}) {
			t.Errorf("sync unread = %v, want later", got)
		}
	})

	t.Run("defaults: a first join and a subagent are fresh; a return is sync unless assign says fresh", func(t *testing.T) {
		ch := newChannel(t)
		if _, err := ch.Publish(bg, "web", PublishArgs{Topic: "note.x", Msg: "before"}); err != nil {
			t.Fatal(err)
		}
		contextOf := func(s Snapshot) string {
			if s.Context == nil {
				return ""
			}
			return *s.Context
		}
		j, err := ch.Join(bg, "docs", JoinOpts{})
		if err != nil || contextOf(j) != "fresh" {
			t.Fatalf("docs join = %q %v, want fresh", contextOf(j), err)
		}
		j, err = ch.Join(bg, "api.explore", JoinOpts{})
		if err != nil || contextOf(j) != "fresh" {
			t.Fatalf("api.explore join = %q %v, want fresh", contextOf(j), err)
		}
		j, err = ch.Join(bg, "api", JoinOpts{})
		if err != nil || contextOf(j) != "sync" {
			t.Fatalf("api return = %q %v, want sync", contextOf(j), err)
		}
		if _, err := ch.Assign(bg, "lead", "api", sp("fresh"), ""); err != nil {
			t.Fatal(err)
		}
		j, err = ch.Join(bg, "api", JoinOpts{})
		if err != nil || contextOf(j) != "fresh" {
			t.Fatalf("assigned api join = %q %v, want fresh", contextOf(j), err)
		}
		if _, err := ch.Assign(bg, Owner, "api", nil, ""); err != nil {
			t.Fatal(err)
		}
		j, err = ch.Join(bg, "api", JoinOpts{})
		if err != nil || contextOf(j) != "sync" {
			t.Fatalf("unassigned api join = %q %v, want sync", contextOf(j), err)
		}
	})

	t.Run("a brief appears on the next fresh join exactly once, and is sent as a message now", func(t *testing.T) {
		ch := newChannel(t)
		plan := vv(t, `{"phases": [{"n": 1, "title": "build", "steps": [
			{"id": "orders", "title": "Ship the orders API", "owner": "api", "what": "cursor pagination"},
			{"id": "client", "title": "Client", "owner": "web", "depends": ["orders"]}]}]}`)
		if _, err := ch.ImportPlan(bg, "lead", planValue(t, plan), ImportOpts{}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Remember(bg, "lead", RememberInput{Kind: "decision", Title: "orders paginate by cursor", Body: "?after=<id>&limit<=100", Tags: []string{"orders"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Remember(bg, "lead", RememberInput{Kind: "fact", Title: "unrelated cache note", Body: "valkey on 6379"}); err != nil {
			t.Fatal(err)
		}
		r, err := RunOp(bg, "brief", ch, "lead", vv(t, `{"session": "api", "msg": "Start with the orders API; ignore the old thread."}`))
		if err != nil {
			t.Fatal(err)
		}
		br := r.Result.(BriefResult)
		ev, err := ch.Event(bg, br.Event)
		if err != nil || ev == nil || ev.To == nil || *ev.To != "api" {
			t.Fatalf("brief event = %v %v, want to api", ev, err)
		}
		p, err := ch.PresenceOf(bg, "api")
		if err != nil || !p.BriefWaiting {
			t.Fatalf("brief_waiting = %v %v, want true", p, err)
		}
		j1, err := ch.Join(bg, "api", JoinOpts{Context: sp("fresh")})
		if err != nil {
			t.Fatal(err)
		}
		if j1.Brief == nil || j1.Brief.FromOrchestrator == nil || !strings.Contains(j1.Brief.FromOrchestrator.Msg, "orders API") {
			t.Fatalf("brief = %+v, want the orchestrator's", j1.Brief)
		}
		var ids []string
		for _, tk := range j1.Brief.Tasks {
			ids = append(ids, tk.ID)
		}
		if !slices.Equal(ids, []string{"orders"}) {
			t.Errorf("brief tasks = %v, want orders", ids)
		}
		if len(j1.Brief.Knowledge) == 0 || j1.Brief.Knowledge[0].Title != "orders paginate by cursor" {
			t.Errorf("brief knowledge = %+v", j1.Brief.Knowledge)
		}
		if j1.Brief.Goal != "ship orders end to end" {
			t.Errorf("brief goal = %q", j1.Brief.Goal)
		}
		p, err = ch.PresenceOf(bg, "api")
		if err != nil || p.BriefWaiting {
			t.Errorf("brief_waiting after the join = %v, want false", p)
		}
		j2, err := ch.Join(bg, "api", JoinOpts{Context: sp("fresh")})
		if err != nil || j2.Brief == nil || j2.Brief.FromOrchestrator != nil {
			t.Fatalf("second brief = %+v %v, want used once", j2.Brief, err)
		}
		w, err := ch.Join(bg, "web", JoinOpts{Context: sp("fresh")})
		if err != nil {
			t.Fatal(err)
		}
		want := []BriefTask{{ID: "client", Title: "Client", Status: "todo", Ready: false, WaitsOn: []string{"orders"}}}
		if len(w.Brief.Tasks) != len(want) {
			t.Fatalf("web brief tasks = %+v, want %+v", w.Brief.Tasks, want)
		}
		for i, tk := range w.Brief.Tasks {
			if tk.ID != want[i].ID || tk.Title != want[i].Title || tk.Status != want[i].Status || tk.Ready != want[i].Ready ||
				!slices.Equal(tk.WaitsOn, want[i].WaitsOn) {
				t.Errorf("web brief task %d = %+v, want %+v", i, tk, want[i])
			}
		}
		sr, err := RunOp(bg, "status", ch, "api", nil)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(deref(sr.Text), "orchestrator: lead") {
			t.Errorf("status text = %q, want the orchestrator in it", deref(sr.Text))
		}
	})

	t.Run("only the owner or the orchestrator may import_plan, assign, brief, approve; only the owner names the orchestrator", func(t *testing.T) {
		ch := newChannel(t)
		importPlan := func(by string) error {
			_, err := RunOp(bg, "import_plan", ch, by, vv(t, `{"plan": {"phases": [{"n": 1, "title": "p", "steps": [{"id": "a1", "title": "a1"}]}]}}`))
			return err
		}
		if st := opStatus(importPlan("api")); st != 403 {
			t.Errorf("import_plan by api = %d, want 403", st)
		}
		if _, err := RunOp(bg, "assign", ch, "api", vv(t, `{"session": "web", "context": "fresh"}`)); opStatus(err) != 403 {
			t.Errorf("assign by api = %d, want 403", opStatus(err))
		}
		if _, err := RunOp(bg, "brief", ch, "web", vv(t, `{"session": "api", "msg": "x"}`)); opStatus(err) != 403 {
			t.Errorf("brief by web = %d, want 403", opStatus(err))
		}
		if st := opStatus(importPlan("lead")); st != 200 {
			t.Errorf("import_plan by lead = %d, want 200", st)
		}
		if _, err := RunOp(bg, "assign", ch, "lead", vv(t, `{"session": "web", "context": "fresh", "task": "a1"}`)); opStatus(err) != 200 {
			t.Errorf("assign by lead = %d, want 200", opStatus(err))
		}
		tk, err := ch.Task(bg, "a1")
		if err != nil || tk == nil || tk.Owner == nil || *tk.Owner != "web" {
			t.Fatalf("a1 = %v %v, want owner web", tk, err)
		}
		if _, err := RunOp(bg, "approve", ch, "lead", vv(t, `{"id": "a1"}`)); opStatus(err) != 200 {
			t.Errorf("approve by lead = %d, want 200", opStatus(err))
		}
		if _, err := RunOp(bg, "configure", ch, "lead", vv(t, `{"title": "Shop 2"}`)); opStatus(err) != 200 {
			t.Errorf("configure by lead = %d, want 200", opStatus(err))
		}
		if _, err := RunOp(bg, "configure", ch, "lead", vv(t, `{"orchestrator": "api"}`)); opStatus(err) != 403 {
			t.Errorf("configure orchestrator by lead = %d, want 403", opStatus(err))
		}
		if _, err := RunOp(bg, "configure", ch, "lead", vv(t, `{"members": ["lead"]}`)); opStatus(err) != 403 {
			t.Errorf("configure members by lead = %d, want 403", opStatus(err))
		}
		if _, err := RunOp(bg, "configure", ch, Owner, vv(t, `{"orchestrator": "api"}`)); opStatus(err) != 200 {
			t.Errorf("configure orchestrator by owner = %d, want 200", opStatus(err))
		}
		if st := opStatus(importPlan("lead")); st != 403 {
			t.Errorf("import_plan by the former lead = %d, want 403", st)
		}
	})

	t.Run("map: phases, sessions and the critical path of a diamond", func(t *testing.T) {
		ch := newChannel(t)
		plan := vv(t, `{"phases": [
			{"n": 1, "title": "one", "steps": [{"id": "a", "title": "A", "owner": "api"}, {"id": "b", "title": "B", "owner": "web", "depends": ["a"]}, {"id": "c", "title": "C", "owner": "api", "depends": ["a"]}]},
			{"n": 2, "title": "two", "steps": [{"id": "c2", "title": "C2", "depends": ["c"]}, {"id": "d", "title": "D", "owner": "web", "depends": ["b", "c2"]}, {"id": "x", "title": "X"}]}]}`)
		if _, err := ch.ImportPlan(bg, Owner, planValue(t, plan), ImportOpts{}); err != nil {
			t.Fatal(err)
		}
		pathIDs := func(m MapView) []string {
			out := make([]string, len(m.CriticalPath))
			for i, tk := range m.CriticalPath {
				out[i] = tk.ID
			}
			return out
		}
		// a → b → d and a → c → c2 → d: the longest open chain is a, c, c2, d
		m, err := ch.Map(bg)
		if err != nil {
			t.Fatal(err)
		}
		if got := pathIDs(m); !slices.Equal(got, []string{"a", "c", "c2", "d"}) {
			t.Fatalf("critical path = %v, want a c c2 d", got)
		}
		var ph [][3]int64
		for _, p := range m.Phases {
			var n int64
			if p.N != nil {
				n = *p.N
			}
			ph = append(ph, [3]int64{n, int64(p.Done), int64(p.Total)})
		}
		if !slices.Equal(ph, [][3]int64{{1, 0, 3}, {2, 0, 3}}) {
			t.Errorf("phases = %v", ph)
		}
		if _, err := ch.SetStatus(bg, "api", "a", "done", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.SetStatus(bg, "api", "c", "doing", ""); err != nil {
			t.Fatal(err)
		}
		m, err = ch.Map(bg)
		if err != nil {
			t.Fatal(err)
		}
		if got := pathIDs(m); !slices.Equal(got, []string{"c", "c2", "d"}) {
			t.Errorf("critical path after progress = %v, want c c2 d", got)
		}
		var cur string
		for _, s := range m.Sessions {
			if s.Name == "api" && s.Current != nil {
				cur = s.Current.ID
			}
		}
		if cur != "c" {
			t.Errorf("api current = %q, want c", cur)
		}
		if m.Phases[0].Done != 1 {
			t.Errorf("phase one done = %d, want 1", m.Phases[0].Done)
		}
		r, err := RunOp(bg, "map", ch, "web", nil)
		if err != nil {
			t.Fatal(err)
		}
		if lines := strings.Split(deref(r.Text), "\n"); len(lines) > 40 {
			t.Errorf("map text = %d lines, want at most 40", len(lines))
		}
		if !strings.Contains(deref(r.Text), "critical path (3 open tasks") {
			t.Errorf("map text = %q, want the critical path summary", deref(r.Text))
		}
	})

	t.Run("every start is on the timeline: joined or returned, with its context and what it skipped", func(t *testing.T) {
		ch := newChannel(t)
		if _, err := ch.Send(bg, "lead", sp("api"), "one", SendOpts{}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Send(bg, "web", nil, "two", SendOpts{}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Join(bg, "api", JoinOpts{Context: sp("fresh")}); err != nil {
			t.Fatal(err)
		}
		evs, err := ch.Events(bg, EventsFilter{Topic: "session.joined", From: sp("api")})
		if err != nil || len(evs) == 0 {
			t.Fatalf("api joins = %v (%v)", evs, err)
		}
		e := evs[len(evs)-1]
		if e.Msg == nil || *e.Msg != "api returned" {
			t.Errorf("join msg = %v, want api returned", e.Msg)
		}
		// web joining after it, and the two messages
		if dataStr(e.Data, "context") != "fresh" || dataNum(e.Data, "skipped") != 3 || !dataBool(e.Data, "returning") {
			t.Errorf("join data = %s, want context fresh, skipped 3, returning", e.Data.EncodeJSON())
		}
		evs, err = ch.Events(bg, EventsFilter{Topic: "session.joined", From: sp("docs")})
		if err != nil || len(evs) != 0 {
			t.Fatalf("docs joins before joining = %v (%v), want none", evs, err)
		}
		if _, err := ch.Join(bg, "docs", JoinOpts{Role: sp("writes docs")}); err != nil {
			t.Fatal(err)
		}
		evs, err = ch.Events(bg, EventsFilter{Topic: "session.joined", From: sp("docs")})
		if err != nil || len(evs) != 1 {
			t.Fatalf("docs joins = %v (%v)", evs, err)
		}
		d := evs[0]
		if d.Msg == nil || *d.Msg != "docs joined: writes docs" {
			t.Errorf("docs join msg = %v", d.Msg)
		}
		if dataBool(d.Data, "returning") {
			t.Errorf("docs join data = %s, want returning false", d.Data.EncodeJSON())
		}
	})
}

func inboxMsgs(items []InboxItem) []string {
	out := make([]string, len(items))
	for i, m := range items {
		out[i] = m.Msg
	}
	return out
}
