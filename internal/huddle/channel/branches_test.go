package channel

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Branches the channel's own cases walk past only once: the snapshot's every line, the ops'
// refusals, and the touches warning with several editors.
func TestChannelBranches(t *testing.T) {
	t.Run("a snapshot with everything in it reads everything", func(t *testing.T) {
		ch := openChannel(t, "t")
		if _, err := ch.Join(bg, "lead", JoinOpts{Role: sp("runs the plan")}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Join(bg, "api", JoinOpts{Role: sp("builds")}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Configure(bg, ConfigureArgs{Orchestrator: sp("lead")}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.CreateTask(bg, "lead", CreateTaskInput{ID: "t1", Title: "One"}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.CreateTask(bg, "lead", CreateTaskInput{ID: "t2", Title: "Two", Owner: sp("api"), After: []string{"t1"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.SetStatus(bg, "lead", "t1", "doing", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Remember(bg, "lead", RememberInput{Kind: "fact", Title: "the build needs gcc", Body: "gcc 13"}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Send(bg, "lead", sp("api"), "which gcc?", SendOpts{Ask: true, Task: sp("t2")}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Control(bg, "lead", "api", "pause", "wait"); err != nil {
			t.Fatal(err)
		}
		s, err := ch.Join(bg, "lead", JoinOpts{Context: sp("sync")})
		if err != nil {
			t.Fatal(err)
		}
		s.Turn = TurnView{}
		if _, err := ch.TakeTurn(bg, "lead", ""); err != nil {
			t.Fatal(err)
		}
		s, err = ch.Snapshot(bg, "lead", SnapshotJoin{Context: sp("sync")})
		if err != nil {
			t.Fatal(err)
		}
		text := snapText(s)
		for _, want := range []string{
			"turn: lead (since #", "context: sync",
			"api(paused) working", "inbox: 0", "unread: 1 → #", "ask",
			"next task: t1 One", "knowledge: 1 entries", "gcc",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("snapText misses %q:\n%s", want, text)
			}
		}
		// a session reading its status sees the ask, its waiting next task and its orchestrator line
		if _, err := ch.Reply(bg, "api", mustAskSeq(t, ch), "gcc 13"); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Control(bg, "lead", "api", "resume", ""); err != nil {
			t.Fatal(err)
		}
		apiText, err := RunOp(bg, "status", ch, "api", nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"orchestrator: lead", "next task: t1 One"} {
			if !strings.Contains(deref(apiText.Text), want) {
				t.Errorf("status text misses %q:\n%s", want, deref(apiText.Text))
			}
		}
		// the task text names who moved it last
		if _, err := ch.SetStatus(bg, "lead", "t1", "done", "built"); err != nil {
			t.Fatal(err)
		}
		r, err := RunOp(bg, "start", ch, "api", vv(t, `{"id": "t2"}`))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(deref(r.Text), "(last by api)") {
			t.Errorf("start text misses the last mover:\n%s", deref(r.Text))
		}
		// a task that waits on another session's open work says so in the snapshot
		if _, err := ch.CreateTask(bg, "lead", CreateTaskInput{ID: "t3", Title: "Three", Owner: sp("lead"), After: []string{"t2"}}); err != nil {
			t.Fatal(err)
		}
		leadText, err := RunOp(bg, "status", ch, "lead", nil)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(deref(leadText.Text), "next task: t3 Three (waits on t2@api:doing)") {
			t.Errorf("lead status text:\n%s", deref(leadText.Text))
		}
	})

	t.Run("the ops refuse missing ids and tasks", func(t *testing.T) {
		ch := joinedChannel(t)
		if _, err := RunOp(bg, "start", ch, "a", vv(t, `{"id": "missing"}`)); errStatus(err) != 404 {
			t.Errorf("start of a missing task = %d (%v), want 404", errStatus(err), err)
		}
		if _, err := RunOp(bg, "finish", ch, "a", vv(t, `{"note": "n"}`)); errStatus(err) != 400 {
			t.Errorf("finish with nothing in progress = %d (%v), want 400", errStatus(err), err)
		}
		if _, err := RunOp(bg, "finish", ch, "a", vv(t, `{"id": "missing", "note": "n"}`)); errStatus(err) != 404 {
			t.Errorf("finish of a missing task = %d (%v), want 404", errStatus(err), err)
		}
		if _, err := RunOp(bg, "depend", ch, "a", vv(t, `{"owner": "b", "title": "t"}`)); errStatus(err) != 400 {
			t.Errorf("depend with nothing in progress = %d (%v), want 400", errStatus(err), err)
		}
		if _, err := RunOp(bg, "state", ch, "a", vv(t, `{"state": "nope"}`)); errStatus(err) != 400 {
			t.Errorf("bad state = %d (%v), want 400", errStatus(err), err)
		}
		if _, err := RunOp(bg, "state", ch, "stranger", vv(t, `{"state": "working"}`)); errStatus(err) != 404 {
			t.Errorf("state of a stranger = %d (%v), want 404", errStatus(err), err)
		}
		if _, err := RunOp(bg, "assign", ch, Owner, vv(t, `{"session": "a", "context": "nope"}`)); errStatus(err) != 400 {
			t.Errorf("assign a bad context = %d (%v), want 400", errStatus(err), err)
		}
		if _, err := RunOp(bg, "assign", ch, Owner, vv(t, `{"session": "stranger"}`)); errStatus(err) != 404 {
			t.Errorf("assign a stranger = %d (%v), want 404", errStatus(err), err)
		}
		if _, err := RunOp(bg, "brief", ch, Owner, vv(t, `{"session": "stranger", "msg": "m"}`)); errStatus(err) != 404 {
			t.Errorf("brief a stranger = %d (%v), want 404", errStatus(err), err)
		}
		if _, err := RunOp(bg, "brief", ch, Owner, vv(t, `{"session": "a", "msg": " "}`)); errStatus(err) != 400 {
			t.Errorf("brief without a msg = %d (%v), want 400", errStatus(err), err)
		}
		if _, err := RunOp(bg, "approve", ch, Owner, vv(t, `{"id": "missing"}`)); errStatus(err) != 404 {
			t.Errorf("approve a missing task = %d (%v), want 404", errStatus(err), err)
		}
		// json:true answers no text, and start of the whole plan done says so
		r, err := RunOp(bg, "map", ch, "a", vv(t, `{"json": true}`))
		if err != nil || deref(r.Text) != "" {
			t.Errorf("map json = %q (%v), want no text", deref(r.Text), err)
		}
		if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: "only", Title: "Only"}); err != nil {
			t.Fatal(err)
		}
		if _, err := RunOp(bg, "start", ch, "a", nil); err != nil {
			t.Fatal(err)
		}
		if _, err := RunOp(bg, "finish", ch, "a", vv(t, `{"note": "done"}`)); err != nil {
			t.Fatal(err)
		}
		r, err = RunOp(bg, "start", ch, "a", nil)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(deref(r.Text), "every task you can take is done or skipped") {
			t.Errorf("start with everything done = %q", deref(r.Text))
		}
	})

	t.Run("the touches warning names several editors, and the sweep runs", func(t *testing.T) {
		ch := openChannel(t, "t")
		for _, n := range []string{"one", "two", "three"} {
			if _, err := ch.Join(bg, n, JoinOpts{}); err != nil {
				t.Fatal(err)
			}
		}
		for _, n := range []string{"one", "two"} {
			if _, err := Touched(bg, ch, n, TouchArgs{Repo: "/r/x", Path: "f.ts"}); err != nil {
				t.Fatal(err)
			}
		}
		r, err := Touched(bg, ch, "three", TouchArgs{Repo: "/r/x", Path: "f.ts"})
		if err != nil {
			t.Fatal(err)
		}
		w := deref(r.Warn)
		if !strings.HasPrefix(w, "Huddle: ") || !strings.Contains(w, "edited f.ts") ||
			!strings.Contains(w, "one") || !strings.Contains(w, "two") || !strings.Contains(w, "coordinate with them") {
			t.Errorf("warn = %q, want both editors in it", w)
		}
		if !strings.Contains(*r.Warn, "coordinate with them") {
			t.Errorf("warn = %v, want them", r.Warn)
		}
		if len(r.Others) != 2 || !slices.ContainsFunc(r.Others, func(o TouchOther) bool { return o.Session == "two" }) {
			t.Errorf("others = %+v", r.Others)
		}
		// the occasional cleanup runs when the roll says so
		ch.Rand = func() float64 { return 0 }
		if _, err := Touched(bg, ch, "three", TouchArgs{Repo: "/r/x", Path: "g.ts"}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("a member's row and presence carry no label", func(t *testing.T) {
		ch := openChannel(t, "t")
		if _, err := ch.Join(bg, "a", JoinOpts{Role: sp("builds"), Task: sp("wiring the router")}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Join(bg, "a", JoinOpts{}); err != nil {
			t.Fatal(err)
		}
		s, err := ch.Session(bg, "a")
		if err != nil || s == nil {
			t.Fatalf("session: %v %v", s, err)
		}
		if deref(s.Role) != "builds" || deref(s.Task) != "joined" {
			t.Errorf("session after rejoin = %+v", s)
		}
		sb, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		p, err := ch.PresenceOf(bg, "a")
		if err != nil {
			t.Fatal(err)
		}
		pb, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range [][]byte{sb, pb} {
			if strings.Contains(string(b), `"label"`) {
				t.Errorf("json still names label:\n%s", b)
			}
		}
	})

	t.Run("join refuses the names it must and keeps its role", func(t *testing.T) {
		ch := openChannel(t, "t")
		if _, err := ch.Join(bg, "owner", JoinOpts{}); errStatus(err) != 400 {
			t.Errorf("joining as the owner = %d (%v), want 400", errStatus(err), err)
		}
		if _, err := ch.Join(bg, "all", JoinOpts{}); errStatus(err) != 400 {
			t.Errorf("joining as all = %d (%v), want 400", errStatus(err), err)
		}
		if _, err := ch.Join(bg, "Bad", JoinOpts{}); errStatus(err) != 400 {
			t.Errorf("joining as Bad = %d (%v), want 400", errStatus(err), err)
		}
		if _, err := ch.Join(bg, "a", JoinOpts{Context: sp("nope")}); errStatus(err) != 400 {
			t.Errorf("bad context = %d (%v), want 400", errStatus(err), err)
		}
		if _, err := ch.Join(bg, "a", JoinOpts{}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Presence(bg, "a", PresenceInput{State: sp("nope")}); errStatus(err) != 400 {
			t.Errorf("bad presence state = %d (%v), want 400", errStatus(err), err)
		}
		if _, err := ch.Presence(bg, "stranger", PresenceInput{State: sp("idle")}); errStatus(err) != 404 {
			t.Errorf("presence of a stranger = %d (%v), want 404", errStatus(err), err)
		}
		if _, err := ch.Leave(bg, "stranger", ""); errStatus(err) != 404 {
			t.Errorf("leave of a stranger = %d (%v), want 404", errStatus(err), err)
		}
		snap, err := ch.Join(bg, "a", JoinOpts{Task: sp("starting"), Role: sp("r1")})
		if err != nil || snap.Me != "a" {
			t.Fatalf("join = %v %v", snap, err)
		}
		snap, err = ch.Join(bg, "a", JoinOpts{})
		if err != nil {
			t.Fatal(err)
		}
		p, _ := ch.PresenceOf(bg, "a")
		if deref(p.Role) != "r1" || deref(p.Task) != "joined" {
			t.Errorf("presence after rejoin = %+v", p)
		}
		if _, err := ch.Assign(bg, Owner, "a", sp("sync"), "notask"); errStatus(err) != 404 {
			t.Errorf("assign a missing task = %d (%v), want 404", errStatus(err), err)
		}
	})
}

// mustAskSeq answers the first ask waiting in a's inbox.
func mustAskSeq(t *testing.T, ch *Channel) int64 {
	t.Helper()
	ib, err := ch.Inbox(bg, "api")
	if err != nil || len(ib) == 0 {
		t.Fatalf("no ask in api's inbox: %v", err)
	}
	return ib[0].Seq
}

// The map with many phases and a loose task, the brief an agent reads while it waits, and the
// waits the workflows take.
func TestChannelBranches2(t *testing.T) {
	t.Run("map trims many phases and reports a loose task", func(t *testing.T) {
		ch := openChannel(t, "t")
		if _, err := ch.Join(bg, "a", JoinOpts{}); err != nil {
			t.Fatal(err)
		}
		var steps []PlanStep
		for i := range 15 {
			steps = append(steps, PlanStep{ID: "p" + itoa(i), Title: "S" + itoa(i)})
		}
		steps[0] = PlanStep{ID: "p0", Title: "S0", Status: "done"}
		phases := []PlanPhase{{N: ip(0), Title: "zero", Steps: steps[:1]}}
		for i := 1; i < 15; i++ {
			phases = append(phases, PlanPhase{N: ip(int64(i)), Title: "phase" + itoa(i), Steps: steps[i : i+1]})
		}
		if _, err := ch.ImportPlan(bg, Owner, Plan{Phases: phases}, ImportOpts{}); err != nil {
			t.Fatal(err)
		}
		// a task outside every phase, and a chain of eleven
		if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: "loose", Title: "Loose", Phase: ip(99)}); err != nil {
			t.Fatal(err)
		}
		prev := ""
		for i := range 12 {
			after := []string{}
			if prev != "" {
				after = append(after, prev)
			}
			if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: "c" + itoa(i), Title: "C" + itoa(i), After: after, Phase: ip(98)}); err != nil {
				t.Fatal(err)
			}
			prev = "c" + itoa(i)
		}
		m, err := ch.Map(bg)
		if err != nil {
			t.Fatal(err)
		}
		last := m.Phases[len(m.Phases)-1]
		if last.Title != "(no phase)" || last.N != nil || last.Total != 13 {
			t.Errorf("the loose phase = %+v", last)
		}
		r, err := RunOp(bg, "map", ch, "a", nil)
		if err != nil {
			t.Fatal(err)
		}
		text := deref(r.Text)
		for _, want := range []string{"… 1 finished phase(s)", "… 3 more phase(s)", "critical path (12 open tasks in a chain):", "… 2 more"} {
			if !strings.Contains(text, want) {
				t.Errorf("map text misses %q:\n%s", want, text)
			}
		}
	})

	t.Run("a brief reads with its waits, its knowledge and its doing focus", func(t *testing.T) {
		ch := openChannel(t, "t")
		if _, err := ch.Join(bg, "lead", JoinOpts{}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Join(bg, "api", JoinOpts{Role: sp("builds")}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Configure(bg, ConfigureArgs{Description: sp("ship it"), Orchestrator: sp("lead")}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.CreateTask(bg, "lead", CreateTaskInput{ID: "first", Title: "First"}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.CreateTask(bg, "lead", CreateTaskInput{ID: "mine1", Title: "Mine One", Owner: sp("api")}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.CreateTask(bg, "lead", CreateTaskInput{ID: "mine2", Title: "Mine Two", Owner: sp("api"), After: []string{"first"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Remember(bg, "lead", RememberInput{Kind: "fact", Title: "orders ship with gcc", Body: "gcc first"}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Remember(bg, "lead", RememberInput{Kind: "fact", Title: "gcc versions", Body: "gcc orders the build"}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.SetStatus(bg, "api", "mine1", "doing", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Brief(bg, "lead", "api", "go"); err != nil {
			t.Fatal(err)
		}
		r, err := RunOp(bg, "join", ch, "api", vv(t, `{"context": "fresh"}`))
		if err != nil {
			t.Fatal(err)
		}
		text := deref(r.Text)
		for _, want := range []string{
			"brief: t · ship it", "from lead (", "): go", "your tasks:",
			"mine2 [todo] Mine Two (waits on first)", "relevant knowledge (kb <id> to read):",
			"#2 [fact] gcc versions", "next task: first First",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("join text misses %q:\n%s", want, text)
			}
		}
	})

	t.Run("attention lists blocked tasks with their notes", func(t *testing.T) {
		ch := openChannel(t, "t")
		if _, err := ch.Join(bg, "a", JoinOpts{}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Join(bg, "b", JoinOpts{}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: "stuck", Title: "Stuck", Owner: sp("b")}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: "gated", Title: "Gated", Body: vv(t, `{"gate": "owner"}`)}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: "waiter", Title: "Waiter", After: []string{"stuck"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.SetStatus(bg, "b", "stuck", "blocked", "no disk"); err != nil {
			t.Fatal(err)
		}
		at, err := ch.Attention(bg)
		if err != nil {
			t.Fatal(err)
		}
		if len(at.Blocked) != 1 || at.Blocked[0].ID != "stuck" || at.Blocked[0].Note != "no disk" || !slices.Equal(at.Blocked[0].WaitsOn, []string{}) {
			t.Errorf("blocked = %+v", at.Blocked)
		}
		if len(at.Gates) != 1 || at.Gates[0].ID != "gated" {
			t.Errorf("gates = %+v", at.Gates)
		}
		if at.UnblockedBy != 1 || at.Total != 2 {
			t.Errorf("attention counts = %d unblocked, total %d", at.UnblockedBy, at.Total)
		}
	})

	t.Run("depend and handoff take their waiting paths", func(t *testing.T) {
		ch := openChannel(t, "t")
		if _, err := ch.Join(bg, "a", JoinOpts{}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Join(bg, "b", JoinOpts{}); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"mine", "open1", "open2"} {
			if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: id, Title: id}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := ch.SetStatus(bg, "a", "mine", "doing", ""); err != nil {
			t.Fatal(err)
		}
		// depend without waiting still blocks the task
		r, err := RunOp(bg, "depend", ch, "a", vv(t, `{"owner": "b", "title": "help", "wait": false}`))
		if err != nil {
			t.Fatal(err)
		}
		d := r.Result.(DependResult)
		if d.Waiting == nil || *d.Waiting {
			t.Errorf("depend wait:false = %+v", d)
		}
		tk, _ := ch.Task(bg, "mine")
		if tk.Status != "blocked" {
			t.Errorf("mine = %s, want blocked", tk.Status)
		}
		// the created task finishes, mine is released, and a finish whose next task still
		// waits on open work says what it waits on
		created := d.Created
		if _, err := ch.SetStatus(bg, "b", created, "done", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.SetStatus(bg, "a", "mine", "doing", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.CreateTask(bg, "a", CreateTaskInput{ID: "later", Title: "Later", After: []string{"open1", "open2"}, Phase: ip(-1)}); err != nil {
			t.Fatal(err)
		}
		r, err = RunOp(bg, "finish", ch, "a", vv(t, `{"note": "done", "result": "the output"}`))
		if err != nil {
			t.Fatal(err)
		}
		fin := r.Result.(FinishResult)
		if fin.Next == nil || fin.Next.ID != "later" || !slices.Equal(fin.Next.Unmet, []string{"open1", "open2"}) || fin.Knowledge == nil {
			t.Errorf("finish next = %+v (knowledge %v)", fin.Next, fin.Knowledge)
		}
		// a handoff that waits wakes on the returned turn
		type opOut struct {
			r   OpResult
			err error
		}
		last, err := ch.LastSeq(bg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Ack(bg, "a", last); err != nil {
			t.Fatal(err)
		}
		res := make(chan opOut, 1)
		go func() {
			r, err := RunOp(bg, "handoff", ch, "a", vv(t, `{"to": "b", "msg": "yours"}`))
			res <- opOut{r, err}
		}()
		until(t, "a waiting", waiting(t, ch, "a"))
		if _, err := ch.PassTurn(bg, "b", "a", "back"); err != nil {
			t.Fatal(err)
		}
		o := <-res
		if o.err != nil {
			t.Fatal(o.err)
		}
		h := o.r.Result.(HandoffResult)
		if h.Woke == nil || h.Woke.Topic != "turn.pass" {
			t.Errorf("handoff woke = %+v", h.Woke)
		}
	})
}

func itoa(i int) string { return strconv.Itoa(i) }

// Opening the same file twice, a repeated send key, the exported emit, and a question nobody
// answers.
func TestChannelEdges(t *testing.T) {
	t.Run("a channel file opens twice and keeps its creation", func(t *testing.T) {
		dir := t.TempDir()
		st, err := storeOpen(bg, dir+"/t.db")
		if err != nil {
			t.Fatal(err)
		}
		ch, err := Open(bg, "t", st)
		if err != nil {
			t.Fatal(err)
		}
		created := ch.meta("created_at")
		if created == "" {
			t.Fatal("no created_at")
		}
		if err := ch.Close(bg); err != nil {
			t.Fatal(err)
		}
		st2, err := storeOpen(bg, dir+"/t.db")
		if err != nil {
			t.Fatal(err)
		}
		ch2, err := Open(bg, "t", st2)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = ch2.Close(bg) }()
		if ch2.meta("created_at") != created {
			t.Errorf("created_at changed: %q then %q", created, ch2.meta("created_at"))
		}
	})

	t.Run("a repeated send key answers the first event", func(t *testing.T) {
		ch := joinedChannel(t)
		e1, err := ch.Send(bg, "a", sp("b"), "m", SendOpts{Key: sp("k")})
		if err != nil {
			t.Fatal(err)
		}
		e2, err := ch.Send(bg, "a", sp("b"), "m", SendOpts{Key: sp("k")})
		if err != nil {
			t.Fatal(err)
		}
		if e2.Seq != e1.Seq || !e2.Duplicate {
			t.Errorf("retry = %+v, want %d duplicate", e2, e1.Seq)
		}
		ch.Emit("probe", nil)
	})

	t.Run("a question nobody answers times out", func(t *testing.T) {
		ch := joinedChannel(t)
		r, err := RunOp(bg, "ask_wait", ch, "a", vv(t, `{"to": "b", "msg": "there?", "timeout": 1}`))
		if err != nil {
			t.Fatal(err)
		}
		a := r.Result.(AskWaitResult)
		if !a.Timeout || a.Hint == "" {
			t.Errorf("ask_wait = %+v, want a timeout with its hint", a)
		}
	})
}
