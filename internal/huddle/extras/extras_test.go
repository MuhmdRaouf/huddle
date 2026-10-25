package extras

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/outside"
	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

var bg = context.Background()

func sp(s string) *string { return &s }

// openChannel opens one channel over a fresh SQLite file, closed with the test.
func openChannel(t *testing.T, name string) *channel.Channel {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(bg, filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	ch, err := channel.Open(bg, name, st)
	if err != nil {
		t.Fatalf("open channel: %v", err)
	}
	t.Cleanup(func() { _ = ch.Close(bg) })
	return ch
}

// joinedChannel is one channel with api and web joined, the shape every extras case starts from.
func joinedChannel(t *testing.T, name string) *channel.Channel {
	t.Helper()
	ch := openChannel(t, name)
	for _, n := range []string{"api", "web"} {
		if _, err := ch.Join(bg, n, channel.JoinOpts{Role: sp(n)}); err != nil {
			t.Fatalf("join %s: %v", n, err)
		}
	}
	return ch
}

func vv(t *testing.T, json string) *jsfmt.Value {
	t.Helper()
	v, err := jsfmt.DecodeValue([]byte(json))
	if err != nil {
		t.Fatalf("decode %s: %v", json, err)
	}
	return v
}

func errStatus(err error) int {
	var he *channel.HuddleError
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

// route runs one extras route and fails the test unless it answered.
func route(t *testing.T, ch *channel.Channel, rest, method, since, as string, body *jsfmt.Value) any {
	t.Helper()
	r, ok, err := Route(bg, ch, rest, method, since, as, body)
	if err != nil {
		t.Fatalf("route %s %s: %v", method, rest, err)
	}
	if !ok {
		t.Fatalf("route %s %s: not an extras path", method, rest)
	}
	return r
}

// noteLine is one line of the notification log.
type noteLine struct {
	At      string `json:"at"`
	Kind    string `json:"kind"`
	Subject string `json:"subject"`
	Title   string `json:"title"`
	Body    string `json:"body"`
}

func readNotes(t *testing.T, path string) []noteLine {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []noteLine
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if l == "" {
			continue
		}
		var n noteLine
		if err := json.Unmarshal([]byte(l), &n); err != nil {
			t.Fatalf("notification line %q: %v", l, err)
		}
		out = append(out, n)
	}
	return out
}

// notifyLog points the notifier at a log file of its own and answers the notifier over it.
func notifyLog(t *testing.T) (*outside.Notifier, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "notify.log")
	t.Setenv("HUDDLE_NOTIFY_LOG", path)
	return outside.NewNotifier(t.TempDir()), path
}

func ruleByID(list []RuleView, id string) *RuleView {
	for i := range list {
		if list[i].ID == id {
			return &list[i]
		}
	}
	return nil
}

func approvalByID(list []ApprovalItem, from string) *ApprovalItem {
	for i := range list {
		if list[i].From == from {
			return &list[i]
		}
	}
	return nil
}

// What an event means for the owner's desktop: names and ids only, never a message body.
func TestNeedsYou(t *testing.T) {
	t.Run("what notifies: a question to the owner, a pause by someone else, an approval request, a blocked task; never the owner's own moves", func(t *testing.T) {
		evOf := func(set func(*channel.Ev)) channel.Ev {
			e := channel.Ev{Seq: 1, Ts: "", From: "api", To: nil, Topic: "msg", Ref: nil,
				Msg: sp("SECRET body"), Data: nil, ReplyTo: nil, NeedsReply: false}
			if set != nil {
				set(&e)
			}
			return e
		}
		ask := evOf(func(e *channel.Ev) { e.Topic = "ask"; e.NeedsReply = true; e.To = sp("owner") })
		if got := NeedsYou(ask, "shop"); got == nil || got.Kind != "ask" || got.Subject != "shop/api" ||
			got.Body != "api asks you a question" || got.Title != "Huddle · shop" {
			t.Errorf("ask = %+v, want the owner's question on shop/api", got)
		}
		if got := NeedsYou(evOf(func(e *channel.Ev) { e.Topic = "ask"; e.NeedsReply = true }), "shop"); got == nil || got.Kind != "ask" {
			t.Errorf("ask to everyone = %+v, want the owner included", got)
		}
		aboutTask := NeedsYou(evOf(func(e *channel.Ev) {
			e.Topic = "ask"
			e.NeedsReply = true
			e.Data = vv(t, `{"task": "t9"}`)
		}), "shop")
		if aboutTask == nil || aboutTask.Body != "api asks you a question about task t9" {
			t.Errorf("ask about a task = %+v, want the task named", aboutTask)
		}
		if got := NeedsYou(evOf(func(e *channel.Ev) {
			e.Topic = "ask"
			e.NeedsReply = true
			e.Data = vv(t, `{"task": 0}`)
		}), "shop"); got == nil || got.Body != "api asks you a question" {
			t.Errorf("ask about task 0 = %+v, a zero task is no task", got)
		}
		if got := NeedsYou(evOf(func(e *channel.Ev) { e.Topic = "ask"; e.NeedsReply = true; e.To = sp("web") }), "shop"); got != nil {
			t.Errorf("ask to another session = %+v, want nothing", got)
		}
		pause := NeedsYou(evOf(func(e *channel.Ev) { e.Topic = "control.pause"; e.To = sp("web") }), "shop")
		if pause == nil || pause.Kind != "pause" || pause.Body != "web was paused by api" || pause.Subject != "shop/web" {
			t.Errorf("pause = %+v, want web paused by api", pause)
		}
		if got := NeedsYou(evOf(func(e *channel.Ev) { e.Topic = "control.pause"; e.To = sp("web"); e.From = "owner" }), "shop"); got != nil {
			t.Errorf("the owner's own pause = %+v, want nothing", got)
		}
		approval := NeedsYou(evOf(func(e *channel.Ev) {
			e.Topic = "approval.request"
			e.To = sp("owner")
			e.Data = vv(t, `{"rules": ["force_push"], "labels": ["Force push"]}`)
		}), "shop")
		if approval == nil || approval.Kind != "approval" || !strings.Contains(approval.Body, "Force push") ||
			approval.Subject != "shop/api/force_push" {
			t.Errorf("approval = %+v, want the force-push request of api", approval)
		}
		blocked := NeedsYou(evOf(func(e *channel.Ev) {
			e.Topic = "task.status"
			e.Ref = sp("t1")
			e.Data = vv(t, `{"status": "blocked", "note": "SECRET"}`)
		}), "shop")
		if blocked == nil || blocked.Kind != "blocked" || blocked.Body != "Task t1 is blocked (api)" {
			t.Errorf("blocked = %+v, want t1 blocked by api", blocked)
		}
		if got := NeedsYou(evOf(func(e *channel.Ev) { e.Topic = "task.status"; e.Ref = sp("t1"); e.Data = vv(t, `{"status": "done"}`) }), "shop"); got != nil {
			t.Errorf("a done task = %+v, want nothing", got)
		}
		if got := NeedsYou(evOf(func(e *channel.Ev) { e.To = sp("owner") }), "shop"); got != nil {
			t.Errorf("a plain message = %+v, want nothing", got)
		}
		for _, topic := range []string{"ask", "control.pause", "approval.request", "task.status"} {
			e := evOf(func(e *channel.Ev) {
				e.Topic = topic
				e.NeedsReply = topic == "ask"
				e.To = sp("x")
				e.Data = vv(t, `{"status": "blocked"}`)
			})
			var rendered string
			if got := NeedsYou(e, "c"); got != nil {
				raw, err := domain.MarshalCompactJSON(got)
				if err != nil {
					t.Fatalf("render notification: %v", err)
				}
				rendered = string(raw)
			}
			if strings.Contains(rendered, "SECRET") {
				t.Errorf("%s notifies %s: the message body leaked", topic, rendered)
			}
		}
	})
}

// The owner's switches per channel: what they are, who may change them, what a change may look
// like. The route answers the same shapes the dashboard reads.
func TestExtrasRules(t *testing.T) {
	t.Run("rules per channel: defaults, the owner switches them, a session may not", func(t *testing.T) {
		ch := openChannel(t, "shop")
		answer, ok := route(t, ch, "rules", "GET", "", "", nil).(RulesAnswer)
		if !ok {
			t.Fatalf("rules answer has the wrong shape")
		}
		on := map[string]bool{}
		for _, r := range answer.Rules {
			on[r.ID] = r.On
		}
		want := map[string]bool{"force_push": true, "delete": true, "git_push": false, "git_tag": false, "publish": false}
		if len(on) != len(want) {
			t.Fatalf("rules = %v, want the five defaults", on)
		}
		for id, v := range want {
			if on[id] != v {
				t.Errorf("rule %s = %v, want %v", id, on[id], v)
			}
		}
		if got := ruleByID(answer.Rules, "force_push"); got == nil || got.Label != "Force push" || !got.Default {
			t.Errorf("force_push = %+v, want label Force push and on by default", got)
		}
		if _, _, err := Route(bg, ch, "rules", "POST", "", "api", vv(t, `{"rules": {"git_push": true}}`)); errStatus(err) != 403 {
			t.Errorf("a session switching rules = status %d, want 403", errStatus(err))
		}
		if _, _, err := Route(bg, ch, "rules", "POST", "", "owner", vv(t, `{"rules": {"nope": true}}`)); errStatus(err) != 400 {
			t.Errorf("an unknown rule = status %d, want 400", errStatus(err))
		}
		answer, _ = route(t, ch, "rules", "POST", "", "owner", vv(t, `{"rules": {"git_push": true}}`)).(RulesAnswer)
		if got := ruleByID(answer.Rules, "git_push"); got == nil || !got.On {
			t.Errorf("git_push after the switch = %+v, want on", got)
		}
		if got := ch.Meta("approval_rules"); got != `{"force_push":true,"delete":true,"git_push":true,"git_tag":false,"publish":false}` {
			t.Errorf("stored switches = %s, want every rule in order", got)
		}
		route(t, ch, "rules", "POST", "", "owner", vv(t, `{"rules": {"git_push": false}}`))
		if _, ok, err := Route(bg, ch, "nope", "GET", "", "", nil); err != nil || ok {
			t.Errorf("an unknown path answered ok=%v err=%v, want ok=false", ok, err)
		}
	})

	t.Run("a patch that is not an object, a rule that is not ours, a value that is not a switch", func(t *testing.T) {
		ch := openChannel(t, "shop")
		if _, err := SetRules(bg, ch, vv(t, `5`)); errStatus(err) != 400 {
			t.Errorf("a number patch = status %d, want 400", errStatus(err))
		}
		if _, err := SetRules(bg, ch, vv(t, `{"bogus": true}`)); err == nil || !strings.Contains(err.Error(), "no approval rule bogus (rules: force_push, delete, git_push, git_tag, publish)") {
			t.Errorf("an unknown rule = %v, want the rule list", err)
		}
		if _, err := SetRules(bg, ch, vv(t, `{"delete": "yes"}`)); err == nil || err.Error() != "rule delete: true or false" {
			t.Errorf("a non-boolean switch = %v, want rule delete: true or false", err)
		}
		if _, err := SetRules(bg, ch, vv(t, `[]`)); err != nil {
			t.Errorf("an empty patch = %v, want no change and no error", err)
		}
		if got := ruleByID(RulesList(ch), "delete"); got == nil || !got.On {
			t.Errorf("delete after an empty patch = %+v, want the default", got)
		}
		if rules := route(t, ch, "rules", "POST", "", "owner", vv(t, `{}`)).(RulesAnswer); len(rules.Rules) != len(channel.Rules) {
			t.Errorf("rules without a patch = %d, want every rule", len(rules.Rules))
		}
	})
}

// A session is about to run a command a rule matches: the request is recorded, the owner is
// notified, and the command itself is kept nowhere.
func TestExtrasApproval(t *testing.T) {
	t.Run("an approval request: ask with a short reason, an event for the owner, a notification; the command is kept nowhere", func(t *testing.T) {
		ch := joinedChannel(t, "shop")
		notifier, log := notifyLog(t)
		NewWatcher(notifier).Watch(ch)
		before := len(readNotes(t, log))

		r := route(t, ch, "approval", "POST", "", "api", vv(t, `{"command": "git push --force origin main # token=s3cr3t-value", "claude_session": "claude-api"}`)).(ApprovalAnswer)
		if !r.Ask || !slices.Equal(r.Rules, []string{"force_push"}) || !slices.Equal(r.Labels, []string{"Force push"}) {
			t.Errorf("the force push = %+v, want ask with force_push", r)
		}
		if !strings.Contains(r.Reason, "Huddle") || strings.Contains(r.Reason, "s3cr3t") {
			t.Errorf("reason = %q, want Huddle's and no secret", r.Reason)
		}
		if got := route(t, ch, "approval", "POST", "", "api", vv(t, `{"command": "git push origin main"}`)).(ApprovalAnswer); got.Ask {
			t.Errorf("a plain push = %+v, want no ask (git push is off by default)", got)
		}
		if _, _, err := Route(bg, ch, "approval", "POST", "", "ghost", vv(t, `{"command": "rm -rf /"}`)); errStatus(err) != 404 {
			t.Errorf("a session that never joined = status %d, want 404", errStatus(err))
		}
		events, err := ch.Events(bg, channel.EventsFilter{Topic: "approval.request"})
		if err != nil {
			t.Fatal(err)
		}
		if len(events) == 0 {
			t.Fatal("no approval event on the timeline")
		}
		last := events[len(events)-1]
		if last.From != "api" || deref(last.To) != channel.Owner || last.Topic != "approval.request" {
			t.Errorf("the approval event = %+v, want api asking the owner", last)
		}
		timeline, err := ch.Events(bg, channel.EventsFilter{})
		if err != nil {
			t.Fatal(err)
		}
		// the timeline as the dashboard reads it: the data column's JSON text in place of the value
		type evLine struct {
			Seq   int64   `json:"seq"`
			Ts    string  `json:"ts"`
			From  string  `json:"from"`
			To    *string `json:"to"`
			Topic string  `json:"topic"`
			Ref   *string `json:"ref"`
			Msg   *string `json:"msg"`
			Data  string  `json:"data"`
		}
		lines := make([]evLine, len(timeline))
		for i, e := range timeline {
			data := ""
			if e.Data != nil {
				data = e.Data.EncodeJSON()
			}
			lines[i] = evLine{Seq: e.Seq, Ts: e.Ts, From: e.From, To: e.To, Topic: e.Topic,
				Ref: e.Ref, Msg: e.Msg, Data: data}
		}
		raw, err := domain.MarshalCompactJSON(lines)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "s3cr3t") || strings.Contains(string(raw), "--force origin") {
			t.Errorf("the timeline carries the command: %s", raw)
		}
		var note *noteLine
		fresh := readNotes(t, log)[before:]
		for i := range fresh {
			if fresh[i].Kind == "approval" {
				note = &fresh[i]
			}
		}
		if note == nil {
			t.Fatalf("notes = %v, want the approval notification", readNotes(t, log)[before:])
		}
		if note.Title != "Huddle · shop" || note.Body != "api asks permission: Force push. Answer in its Claude Code window." {
			t.Errorf("the notification = %+v", note)
		}
		if data, _ := os.ReadFile(log); strings.Contains(string(data), "s3cr3t") {
			t.Error("the notification log carries the command")
		}

		// the Inbox lists it until the owner dismisses it
		a := route(t, ch, "approvals", "GET", "", "", nil).(ApprovalsAnswer)
		if len(a.Approvals) == 0 {
			t.Fatalf("approvals = %v, want the request", a.Approvals)
		}
		if got := a.Approvals[0]; got.From != "api" || !slices.Equal(got.Labels, []string{"Force push"}) {
			t.Errorf("the listed request = %+v, want api's Force push", got)
		}
		if _, _, err := Route(bg, ch, "approvals/dismiss", "POST", "", "api", vv(t, fmt.Sprintf(`{"seq": %d}`, a.Approvals[0].Seq))); errStatus(err) != 403 {
			t.Errorf("a session dismissing = status %d, want 403", errStatus(err))
		}
		d := route(t, ch, "approvals/dismiss", "POST", "", "owner", vv(t, fmt.Sprintf(`{"seq": %d}`, a.Approvals[0].Seq))).(Dismissal)
		if got := approvalByID(d.Approvals, "api"); got != nil {
			t.Errorf("after the dismiss = %v, want it gone", d.Approvals)
		}
		route(t, ch, "approval", "POST", "", "web", vv(t, `{"command": "rm x"}`))
		if got := len(route(t, ch, "approvals", "GET", "", "", nil).(ApprovalsAnswer).Approvals); got != 1 {
			t.Errorf("approvals after web's request = %d, want 1", got)
		}
		d = route(t, ch, "approvals/dismiss", "POST", "", "owner", vv(t, `{"all": true}`)).(Dismissal)
		if len(d.Approvals) != 0 {
			t.Errorf("after dismiss all = %v, want none", d.Approvals)
		}
		for _, body := range []string{`{}`, `{"seq": 0}`, `{"seq": 1.5}`, `{"seq": "later"}`} {
			if _, err := Dismiss(bg, ch, vv(t, body)); errStatus(err) != 400 {
				t.Errorf("dismiss %s = status %d, want 400", body, errStatus(err))
			}
		}
		// the remembered dismissals are capped: the last 300 stay
		var long []string
		for i := 1; i <= 305; i++ {
			long = append(long, strconv.Itoa(i))
		}
		if err := ch.SetMeta(bg, "approvals_dismissed", sp("["+strings.Join(long, ",")+"]")); err != nil {
			t.Fatal(err)
		}
		if _, err := Dismiss(bg, ch, vv(t, `{"seq": "306"}`)); err != nil {
			t.Fatal(err)
		}
		kept := metaJSON(ch, "approvals_dismissed")
		if kept == nil || kept.Kind() != jsfmt.KindArray || len(kept.Items()) != 300 {
			t.Fatalf("dismissed = %v, want the last 300", kept)
		}
		if first, _ := jsInt(kept.Items()[0]); first != 7 {
			t.Errorf("the first kept dismissal = %d, want 7", first)
		}
		if last, _ := jsInt(kept.Items()[299]); last != 306 {
			t.Errorf("the last kept dismissal = %d, want 306", last)
		}
		if _, err := ch.LastSeq(bg); err != nil {
			t.Fatal(err)
		}
		if got := ch.Meta("approvals_cleared"); got == "" || got == "0" {
			t.Errorf("approvals_cleared = %q, want the last seq", got)
		}

		// a session links its Claude session id itself (the SessionStart join's other half)
		if got := route(t, ch, "link", "POST", "", "web", vv(t, `{"claude_session": "claude-web"}`)).(OKAnswer); !got.OK {
			t.Errorf("link = %+v, want ok", got)
		}
		links, err := channel.Links(bg, ch)
		if err != nil {
			t.Fatal(err)
		}
		if links["claude-web"] != "web" || links["claude-api"] != "api" {
			t.Errorf("links = %v, want both sessions", links)
		}
	})
}

// The channel's live events reach the owner's desktop as they happen, once per subject per
// window, and the owner's own moves never do.
func TestExtrasNotifications(t *testing.T) {
	t.Run("the server notifies what needs the owner as it happens, once per subject per window, never the owner's own moves", func(t *testing.T) {
		ch := joinedChannel(t, "shop")
		notifier, log := notifyLog(t)
		NewWatcher(notifier).Watch(ch)
		if _, err := ch.Send(bg, "web", sp(channel.Owner), "may I deploy? password=hunter2", channel.SendOpts{Ask: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Send(bg, "web", sp(channel.Owner), "and now?", channel.SendOpts{Ask: true}); err != nil {
			t.Fatal(err) // same session, same window: once
		}
		if _, err := ch.Control(bg, "web", "api", "pause", "wait"); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Control(bg, channel.Owner, "api", "resume", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Control(bg, channel.Owner, "web", "pause", ""); err != nil {
			t.Fatal(err) // the owner's own: nothing
		}
		if _, err := ch.Control(bg, channel.Owner, "web", "resume", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.CreateTask(bg, "api", channel.CreateTaskInput{ID: "b1", Title: "Migrate"}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.SetStatus(bg, "api", "b1", "blocked", "disk full"); err != nil {
			t.Fatal(err)
		}
		notes := readNotes(t, log)
		kinds := make([]string, len(notes))
		for i, n := range notes {
			kinds[i] = n.Kind
		}
		slices.Sort(kinds)
		if !slices.Equal(kinds, []string{"ask", "blocked", "pause"}) {
			t.Fatalf("notes = %v, want one ask, one pause and one blocked", notes)
		}
		bodies := map[string]string{}
		for _, n := range notes {
			bodies[n.Kind] = n.Body
		}
		if bodies["ask"] != "web asks you a question" {
			t.Errorf("ask = %q", bodies["ask"])
		}
		if bodies["pause"] != "api was paused by web" {
			t.Errorf("pause = %q", bodies["pause"])
		}
		if bodies["blocked"] != "Task b1 is blocked (api)" {
			t.Errorf("blocked = %q", bodies["blocked"])
		}
		raw, err := domain.MarshalCompactJSON(notes)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "hunter2") {
			t.Errorf("the notifications carry the message body: %s", raw)
		}
		// the switch: off in the dashboard (the owner only), nothing shows; on again
		state, err := Settings("POST", notifier, outside.NewZones(t.TempDir()), channel.Owner, vv(t, `{"notify": false}`))
		if err != nil {
			t.Fatal(err)
		}
		if state.Notify {
			t.Errorf("state after switching off = %+v", state)
		}
		if state, err := Settings("GET", notifier, outside.NewZones(t.TempDir()), "", nil); err != nil || state.Notify {
			t.Errorf("GET settings = %+v (%v), want off", state, err)
		}
		before := len(readNotes(t, log))
		if _, err := ch.CreateTask(bg, "api", channel.CreateTaskInput{ID: "b2", Title: "Other"}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.SetStatus(bg, "api", "b2", "blocked", ""); err != nil {
			t.Fatal(err)
		}
		if got := len(readNotes(t, log)); got != before {
			t.Errorf("notes while off = %d, want %d", got, before)
		}
		if state, err = Settings("POST", notifier, outside.NewZones(t.TempDir()), channel.Owner, vv(t, `{"notify": true}`)); err != nil || !state.Notify {
			t.Errorf("state after switching on = %+v (%v)", state, err)
		}
	})

	t.Run("the settings switch is the owner's to change", func(t *testing.T) {
		notifier, _ := notifyLog(t)
		if _, err := Settings("POST", notifier, outside.NewZones(t.TempDir()), "api", vv(t, `{"notify": false}`)); errStatus(err) != 403 {
			t.Errorf("a session changing settings = status %d, want 403", errStatus(err))
		}
		if _, err := Settings("POST", notifier, outside.NewZones(t.TempDir()), channel.Owner, vv(t, `{"notify": "no"}`)); errStatus(err) != 400 {
			t.Errorf("a non-boolean switch = status %d, want 400", errStatus(err))
		}
		if _, err := Settings("POST", notifier, outside.NewZones(t.TempDir()), channel.Owner, vv(t, `{}`)); errStatus(err) != 400 {
			t.Errorf("a body without notify = status %d, want 400", errStatus(err))
		}
	})

	t.Run("the time zone is the owner's to change, to a name that loads", func(t *testing.T) {
		notifier, _ := notifyLog(t)
		zones := outside.NewZones(t.TempDir())
		if state, err := Settings("GET", notifier, zones, channel.Owner, nil); err != nil || state.Zone != "" || state.ZoneOrigin != "" {
			t.Errorf("a fresh home's settings = %+v (%v), want no zone", state, err)
		}
		if _, err := Settings("PUT", notifier, zones, "api", vv(t, `{"zone": "UTC"}`)); errStatus(err) != 403 {
			t.Errorf("a session changing the zone = status %d, want 403", errStatus(err))
		}
		for _, body := range []string{`{"zone": "Mars/Olympus_Mons"}`, `{"zone": 3}`, `{}`} {
			if _, err := Settings("PUT", notifier, zones, channel.Owner, vv(t, body)); errStatus(err) != 400 {
				t.Errorf("PUT %s = status %d, want 400", body, errStatus(err))
			}
		}
		state, err := Settings("PUT", notifier, zones, channel.Owner, vv(t, `{"zone": "Asia/Tokyo"}`))
		if err != nil || state.Zone != "Asia/Tokyo" || state.ZoneOrigin != outside.ZoneFromOwner || !state.Notify {
			t.Errorf("PUT Asia/Tokyo = %+v (%v), want the owner's zone beside the switch", state, err)
		}
	})
}

// Radar's own view of the channel: absent, its parts are missing without an error; running, its
// alerts and costs land on the channel's sessions.
func TestExtrasRadar(t *testing.T) {
	t.Run("Radar absent: its parts are simply missing, no error", func(t *testing.T) {
		ch := joinedChannel(t, "shop")
		ch.Radar = &outside.Radar{Env: func(name string) string {
			if name == "RADAR_HOME" {
				return t.TempDir()
			}
			return ""
		}}
		r := route(t, ch, "radar", "GET", "", "", nil)
		raw, err := domain.MarshalCompactJSON(r)
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != `{"available":false}` {
			t.Errorf("radar = %s, want only available:false", raw)
		}
		d := route(t, ch, "digest", "GET", "", "", nil).(*channel.DigestResult)
		if d.Cost.Available {
			t.Errorf("digest cost = %+v, want radar absent", d.Cost)
		}
	})

	t.Run("Radar running: its alerts for this channel's sessions, the cost per session, in the digest too", func(t *testing.T) {
		ch := joinedChannel(t, "shop")
		channel.Link(bg, ch, "api", "claude-api")
		channel.Link(bg, ch, "web", "claude-web")
		var obs *httptest.Server
		obs = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Host != fmt.Sprintf("127.0.0.1:%d", obs.Listener.Addr().(*net.TCPAddr).Port) {
				http.Error(w, "forbidden host", http.StatusForbidden)
				return
			}
			w.Header().Set("content-type", "application/json")
			switch r.URL.Path {
			case "/api/alerts":
				fmt.Fprint(w, `{"alerts": [
{"id": "a1", "kind": "loop", "sessionId": "claude-api", "agentId": null, "project": "shop", "since": 1690000000000, "detail": "same tool call 12 times", "costUsd": 0.4},
{"id": "a2", "kind": "stuck", "sessionId": "claude-elsewhere", "agentId": null, "project": "other", "since": 1690000060000, "detail": "not ours", "costUsd": null}]}`)
			case "/api/attribution":
				fmt.Fprint(w, `{"rows": [
{"key": "claude-api", "label": "api", "requests": 10, "tokens": {"input": 1, "output": 1, "cacheRead": 0, "cacheWrite": 0}, "costUsd": 1.25},
{"key": "claude-web", "label": "web", "requests": 3, "tokens": {"input": 1, "output": 1, "cacheRead": 0, "cacheWrite": 0}, "costUsd": 0.5},
{"key": "claude-elsewhere", "label": "x", "requests": 3, "tokens": {}, "costUsd": 9}]}`)
			default:
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(obs.Close)
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, "port"), []byte(strconv.Itoa(obs.Listener.Addr().(*net.TCPAddr).Port)), 0o600); err != nil {
			t.Fatal(err)
		}
		radar := outside.NewRadar()
		radar.Env = func(name string) string {
			if name == "RADAR_HOME" {
				return home
			}
			return ""
		}
		radar.Client = obs.Client()
		ch.Radar = radar
		// the answer quotes radar's own bytes: only this channel's alert and sessions, the
		// costs in the order its rows name them
		r := route(t, ch, "radar", "GET", "", "", nil)
		raw, err := domain.MarshalCompactJSON(r)
		if err != nil {
			t.Fatal(err)
		}
		want := `{"available":true,"range":"day","alerts":[` +
			`{"id":"a1","kind":"loop","session":"api","agent":null,"since":1690000000000,` +
			`"detail":"same tool call 12 times","cost":0.4}],` +
			`"cost":{"api":1.25,"web":0.5},"total":1.75}`
		if string(raw) != want {
			t.Errorf("radar =\n%s\nwant\n%s", raw, want)
		}
		d := route(t, ch, "digest", "GET", "24h", "", nil).(*channel.DigestResult)
		if raw, err = domain.MarshalCompactJSON(d.Cost); err != nil {
			t.Fatal(err)
		}
		if string(raw) != `{"available":true,"total":1.75,"range":"day"}` {
			t.Errorf("digest cost = %s", raw)
		}
		var api *channel.DigestSess
		for i := range d.Sessions {
			if d.Sessions[i].Name == "api" {
				api = &d.Sessions[i]
			}
		}
		if api == nil || api.Cost == nil || *api.Cost != 1.25 {
			t.Errorf("api's digest part = %+v, want cost 1.25", api)
		}
	})

	t.Run("the cost object keeps the order radar's rows name, not the sorted one", func(t *testing.T) {
		ch := openChannel(t, "shop")
		for _, n := range []string{"zeta", "alpha", "mid"} {
			channel.Link(bg, ch, n, "claude-"+n)
		}
		obs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("content-type", "application/json")
			switch r.URL.Path {
			case "/api/alerts":
				fmt.Fprint(w, `{"alerts":[]}`)
			case "/api/attribution":
				fmt.Fprint(w, `{"rows": [
{"key": "claude-zeta", "label": "z", "requests": 1, "tokens": {}, "costUsd": 0.3},
{"key": "claude-alpha", "label": "a", "requests": 1, "tokens": {}, "costUsd": 0.2},
{"key": "claude-elsewhere", "label": "x", "requests": 1, "tokens": {}, "costUsd": 9},
{"key": "claude-mid", "label": "m", "requests": 1, "tokens": {}, "costUsd": 0.1}]}`)
			default:
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(obs.Close)
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, "port"), []byte(strconv.Itoa(obs.Listener.Addr().(*net.TCPAddr).Port)), 0o600); err != nil {
			t.Fatal(err)
		}
		radar := outside.NewRadar()
		radar.Env = func(name string) string {
			if name == "RADAR_HOME" {
				return home
			}
			return ""
		}
		radar.Client = obs.Client()
		ch.Radar = radar
		raw, err := domain.MarshalCompactJSON(route(t, ch, "radar", "GET", "", "", nil))
		if err != nil {
			t.Fatal(err)
		}
		// the names come in the rows' order (zeta first), and the total adds in that order too
		want := `{"available":true,"range":"day","alerts":[],"cost":{"zeta":0.3,"alpha":0.2,"mid":0.1},"total":0.6}`
		if string(raw) != want {
			t.Errorf("radar =\n%s\nwant\n%s", raw, want)
		}
	})

	t.Run("a digest window that is not one answers 400", func(t *testing.T) {
		ch := joinedChannel(t, "shop")
		if _, _, err := Route(bg, ch, "digest", "GET", "bogus", "", nil); errStatus(err) != 400 {
			t.Errorf("since=bogus = status %d, want 400", errStatus(err))
		}
	})
}

// Every channel the hub answers is watched, the first time it is answered.
func TestWatcher(t *testing.T) {
	t.Run("every channel the hub answers is watched", func(t *testing.T) {
		notifier, log := notifyLog(t)
		hub := channel.NewHub(t.TempDir(), true)
		t.Cleanup(func() { _ = hub.Close(bg) })
		NewWatcher(notifier).WatchHub(hub)
		ch, err := hub.Get(bg, "shop")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Join(bg, "web", channel.JoinOpts{}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Send(bg, "web", sp(channel.Owner), "may I deploy?", channel.SendOpts{Ask: true}); err != nil {
			t.Fatal(err)
		}
		notes := readNotes(t, log)
		if len(notes) != 1 || notes[0].Kind != "ask" || notes[0].Subject != "shop/web" {
			t.Fatalf("notes = %v, want web's ask on shop", notes)
		}
		// live messages that are not events cost nothing
		ch.Emit("channel", ch.Config())
		ch.Emit("event", "not an event")
		if got := len(readNotes(t, log)); got != 1 {
			t.Errorf("notes after the other live messages = %d, want still one", got)
		}
		if again, err := hub.Get(bg, "shop"); err != nil || again != ch {
			t.Errorf("a second get = %v (%v), want the same watched channel", again, err)
		}
		if got := len(readNotes(t, log)); got != 1 {
			t.Errorf("notes after a second get = %d, want the watch to stay one", got)
		}
	})
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// Bodies and meta values are read with JavaScript's eyes: a template renders any value, an if
// counts false, 0, "" and null as nothing, and Number() takes a number or its digits.
func TestValueReaders(t *testing.T) {
	t.Run("values render, count and parse the way the source's JavaScript does", func(t *testing.T) {
		for _, v := range []struct {
			json string
			text string
		}{
			{`"x"`, "x"},
			{`5`, "5"},
			{`5.5`, "5.5"},
			{`1000000000000000000000`, "1e+21"},
			{`true`, "true"},
			{`false`, "false"},
			{`null`, "null"},
			{`{}`, "[object Object]"},
			{`[]`, "[object Object]"},
		} {
			if got := strAnyOf(vv(t, v.json)); got != v.text {
				t.Errorf("render %s = %q, want %q", v.json, got, v.text)
			}
		}
		if got := strAnyOf(nil); got != "null" {
			t.Errorf("render nothing = %q, want null", got)
		}
		for _, v := range []struct {
			json string
			want bool
		}{
			{`true`, true}, {`false`, false},
			{`1`, true}, {`0`, false}, {`0.0`, false},
			{`"x"`, true}, {`""`, false},
			{`null`, false}, {`[]`, true}, {`{}`, true},
		} {
			if got := truthy(vv(t, v.json)); got != v.want {
				t.Errorf("truthy %s = %v", v.json, got)
			}
		}
		if truthy(nil) {
			t.Error("truthy nothing = true")
		}
		for _, v := range []struct {
			json string
			seq  int64
			ok   bool
		}{
			{`5`, 5, true},
			{`"5"`, 5, true},
			{`"306"`, 306, true},
			{`5.0`, 5, true},
			{`5.5`, 0, false},
			{`-1`, 0, false},
			{`0`, 0, false},
			{`"later"`, 0, false},
			{`null`, 0, false},
		} {
			got, ok := jsSeqOf(vv(t, v.json))
			if got != v.seq || ok != v.ok {
				t.Errorf("seq %s = %d (%v), want %d (%v)", v.json, got, ok, v.seq, v.ok)
			}
		}
		for _, v := range []struct {
			row channel.Row
			n   int64
		}{
			{channel.Row{"k": int64(7)}, 7},
			{channel.Row{"k": float64(7)}, 7},
			{channel.Row{"k": 7}, 7},
			{channel.Row{"k": "7"}, 0},
			{channel.Row{}, 0},
		} {
			if got := rowInt(v.row, "k"); got != v.n {
				t.Errorf("rowInt %v = %d, want %d", v.row, got, v.n)
			}
		}
		if got := rowJSON(channel.Row{"k": `{"a":1}`}, "k"); got == nil || got.EncodeJSON() != `{"a":1}` {
			t.Errorf("rowJSON of an object = %v", got)
		}
		if got := rowJSON(channel.Row{"k": 5}, "k"); got != nil {
			t.Errorf("rowJSON of a number = %v, want nil", got)
		}
		if got := rowJSON(channel.Row{"k": "not json"}, "k"); got != nil {
			t.Errorf("rowJSON of garbage = %v, want nil", got)
		}
	})
}
