package channel

import (
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
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/huddle/outside"
)

func digestSession(list []DigestSess, name string) *DigestSess {
	for i := range list {
		if list[i].Name == name {
			return &list[i]
		}
	}
	return nil
}

// What happened in a channel since a moment, per session: tasks finished, notes, knowledge,
// approvals; what is blocked now and the questions still open; the estimated cost per session when
// Radar runs, folded onto Huddle names.
func TestDigest(t *testing.T) {
	t.Run("the digest: per session what got done, notes, knowledge, approvals; blocked now; open questions", func(t *testing.T) {
		ch := openChannel(t, "dg")
		for _, j := range []struct{ name, role string }{{"api", "api"}, {"web", "web"}} {
			if _, err := ch.Join(bg, j.name, JoinOpts{Role: sp(j.role)}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := ch.Join(bg, "api.explore", JoinOpts{}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.CreateTask(bg, "api", CreateTaskInput{ID: "t1", Title: "Ship the API"}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.CreateTask(bg, "api", CreateTaskInput{ID: "t2", Title: "Client", Owner: sp("web")}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.SetStatus(bg, "api", "t1", "done", "40 tests green"); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.SetStatus(bg, "web", "t2", "blocked", "needs the schema"); err != nil {
			t.Fatal(err)
		}
		kb, err := ch.Remember(bg, "api.explore", RememberInput{Kind: "fact", Title: "orders schema", Body: "orders(id, total)"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Note(bg, "web", "t1", "question", "is it paginated?"); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Send(bg, "web", sp(Owner), "may I drop the old table?", SendOpts{Ask: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := ch.Append(bg, "api", evIn{
			topic: "approval.request", to: sp(Owner), msg: sp("api asks permission: Force push"),
			data: vv(t, `{"rules": ["force_push"], "labels": ["Force push"]}`),
		}); err != nil {
			t.Fatal(err)
		}
		d, err := Digest(bg, ch, "24h", DigestOpts{SkipCost: true})
		if err != nil {
			t.Fatal(err)
		}
		if d == nil {
			t.Fatal("digest answered nothing")
		}
		if got := d.Totals; got.Done != 1 || got.Knowledge != 1 || got.Notes != 1 || got.Blocked != 1 ||
			got.Questions != 1 || got.Approvals != 1 {
			t.Errorf("totals = %+v, want one of each", got)
		}
		api := digestSession(d.Sessions, "api")
		web := digestSession(d.Sessions, "web")
		if api == nil || web == nil {
			t.Fatalf("sessions = %v, want api and web", d.Sessions)
		}
		if len(api.Done) != 1 || api.Done[0].ID != "t1" {
			t.Errorf("api done = %+v, want t1", api.Done)
		}
		if len(api.Knowledge) != 1 || deref(api.Knowledge[0].Kind) != "fact" || deref(api.Knowledge[0].Title) != "orders schema" {
			t.Errorf("api knowledge = %+v, want the fact a subagent added", api.Knowledge)
		}
		if len(api.Approvals) != 1 || !slices.Equal(api.Approvals[0].Labels, []string{"Force push"}) {
			t.Errorf("api approvals = %+v", api.Approvals)
		}
		if len(web.Notes) != 1 || web.Notes[0].Task != "t1" || web.Notes[0].Kind != "question" {
			t.Errorf("web notes = %+v", web.Notes)
		}
		if web.Asked != 1 {
			t.Errorf("web asked = %d, want 1", web.Asked)
		}
		if len(d.Blocked) != 1 || d.Blocked[0].ID != "t2" || deref(d.Blocked[0].Owner) != "web" || d.Blocked[0].Note != "needs the schema" {
			t.Errorf("blocked = %+v", d.Blocked)
		}
		if len(d.Questions) != 1 || d.Questions[0].From != "web" || deref(d.Questions[0].To) != Owner {
			t.Errorf("questions = %+v", d.Questions)
		}
		if d.Cost.Available {
			t.Error("cost available without radar")
		}
		txt := DigestText(d)
		for _, want := range []string{
			"done t1 Ship the API",
			fmt.Sprintf("kb #%d [fact] orders schema", kb.ID),
			"blocked now:",
			"t2 Client @web",
			"open questions:",
			"asked permission 1×: Force push",
		} {
			if !strings.Contains(txt, want) {
				t.Errorf("digest text misses %q:\n%s", want, txt)
			}
		}
		// a window that starts after everything: nothing finished, the present still shown
		laterAt := time.Now().Add(60 * time.Second).UTC().Format("2006-01-02T15:04:05.000Z")
		later, err := Digest(bg, ch, laterAt, DigestOpts{SkipCost: true})
		if err != nil {
			t.Fatal(err)
		}
		if later == nil {
			t.Fatal("later digest answered nothing")
		}
		if later.Totals.Done != 0 || len(later.Blocked) != 1 {
			t.Errorf("later digest = %+v, want nothing done and the blocked task", later.Totals)
		}
		if got, err := ParseSince("2h", 10_000_000); err != nil || got != 10_000_000-7_200_000 {
			t.Errorf("parseSince(2h) = %d (%v)", got, err)
		}
		if _, err := ParseSince("yesterday-ish", 0); err == nil {
			t.Error("parseSince(yesterday-ish) = no error")
		}
	})

	t.Run("Radar absent: the digest's cost is simply missing, no error", func(t *testing.T) {
		ch := openChannel(t, "dg")
		if _, err := ch.Join(bg, "api", JoinOpts{}); err != nil {
			t.Fatal(err)
		}
		ch.Radar = &outside.Radar{Env: func(name string) string {
			if name == "RADAR_HOME" {
				return t.TempDir()
			}
			return ""
		}}
		d, err := Digest(bg, ch, "24h", DigestOpts{})
		if err != nil {
			t.Fatal(err)
		}
		if d == nil {
			t.Fatal("digest answered nothing")
		}
		if d.Cost.Available || d.Cost.Total != nil {
			t.Errorf("cost = %+v, want radar absent", d.Cost)
		}
	})

	t.Run("Radar running: the cost per session, folded onto Huddle names, in the digest too", func(t *testing.T) {
		ch := openChannel(t, "shop")
		for _, n := range []string{"api", "web"} {
			if _, err := ch.Join(bg, n, JoinOpts{}); err != nil {
				t.Fatal(err)
			}
		}
		Link(bg, ch, "api", "claude-api")
		Link(bg, ch, "web", "claude-web")
		obs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/attribution" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("content-type", "application/json")
			fmt.Fprint(w, `{"rows": [
{"key": "claude-api", "label": "api", "requests": 10, "tokens": {"input": 1, "output": 1}, "costUsd": 1.25},
{"key": "claude-web", "label": "web", "requests": 3, "tokens": {"input": 1, "output": 1}, "costUsd": 0.5},
{"key": "claude-elsewhere", "label": "x", "requests": 3, "tokens": {}, "costUsd": 9}]}`)
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
		d, err := Digest(bg, ch, "24h", DigestOpts{})
		if err != nil {
			t.Fatal(err)
		}
		if d == nil {
			t.Fatal("digest answered nothing")
		}
		if !d.Cost.Available || d.Cost.Total == nil || *d.Cost.Total != 1.75 || d.Cost.Range != "day" {
			t.Errorf("cost = %+v, want radar's 1.75 over a day", d.Cost)
		}
		if api := digestSession(d.Sessions, "api"); api == nil || api.Cost == nil || *api.Cost != 1.25 {
			t.Errorf("api session = %+v, want cost 1.25", api)
		}
	})

	t.Run("the session order breaks name ties the way localeCompare does, not by bytes", func(t *testing.T) {
		ch := openChannel(t, "dg")
		at := isoNow(time.Now())
		for _, name := range []string{"GLM", "glm"} {
			if err := ch.Store.Run(bg, "INSERT INTO sessions (name, joined_at, last_seen, state) VALUES (?,?,?,'working')", name, at, at); err != nil {
				t.Fatal(err)
			}
			if err := ch.Store.Run(bg, "INSERT INTO events (ts, from_name, topic, needs_reply) VALUES (?,?,'msg',0)", at, name); err != nil {
				t.Fatal(err)
			}
		}
		d, err := Digest(bg, ch, "24h", DigestOpts{SkipCost: true})
		if err != nil {
			t.Fatal(err)
		}
		if d == nil {
			t.Fatal("digest answered nothing")
		}
		if len(d.Sessions) != 2 {
			t.Fatalf("sessions = %v, want two", d.Sessions)
		}
		// lowercase sorts before uppercase in the locale; a byte order would answer GLM first
		if d.Sessions[0].Name != "glm" || d.Sessions[1].Name != "GLM" {
			t.Errorf("sessions = %v then %v, want glm before GLM", d.Sessions[0].Name, d.Sessions[1].Name)
		}
	})
}

// parseSinceVectors and squashVectors are the verdict tables: parseSince's answer for every
// since shape, and the fold replace(/\s+/g, " ") gives every squash vector.
func TestParseSinceVectors(t *testing.T) {
	for _, v := range []struct {
		id    string
		since string
		now   int64
		want  int64
		err   string
	}{
		{"since001", "", 1000000000000, 999913600000, ""},
		{"since002", "24h", 1000000000000, 999913600000, ""},
		{"since003", "90M", 1000000000000, 999994600000, ""},
		{"since004", "7d", 1000000000000, 999395200000, ""},
		{"since005", "2w", 1000000000000, 998790400000, ""},
		{"since006", "45", 1000000000000, 999999955000, ""},
		{"since007", "1.5h", 1000000000000, 999994600000, ""},
		{"since008", "24H", 1000000000000, 999913600000, ""},
		{"since009", "1 w", 1000000000000, 999395200000, ""},
		{"since010", "2 h", 1000000000000, 999992800000, ""},
		{"since011", "  24h  ", 1000000000000, 999913600000, ""},
		{"since012", "2026-10-14T08:00:00Z", 1792233600000, 1791964800000, ""},
		{"since013", "2026-10-14", 1792233600000, 1791936000000, ""},
		{"since014", "yesterday-ish", 0, 0, "since: 24h, 90m, 7d or an ISO date, not yesterday-ish"},
		{"since015", "24hh", 0, 0, "since: 24h, 90m, 7d or an ISO date, not 24hh"},
		{"since016", "-3h", 0, 0, "since: 24h, 90m, 7d or an ISO date, not -3h"},
		{"since017", "999999w", 1000000000000, 992224000000, ""},
		{"since018", "0", 5000, 5000, ""},
		{"since019", "1e3", 0, 0, "since: 24h, 90m, 7d or an ISO date, not 1e3"},
		{"since020", "24 h", 1000000000000, 999913600000, ""},
		{"since021", ".5d", 0, 0, "since: 24h, 90m, 7d or an ISO date, not .5d"},
	} {
		got, err := ParseSince(v.since, v.now)
		if v.err != "" {
			if err == nil || err.Error() != v.err {
				t.Errorf("%s parseSince(%q) error = %v, want %q", v.id, v.since, err, v.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s parseSince(%q) = %v", v.id, v.since, err)
			continue
		}
		if got != v.want {
			t.Errorf("%s parseSince(%q) = %d, want %d", v.id, v.since, got, v.want)
		}
	}
}

func TestSquashVectors(t *testing.T) {
	for _, v := range []struct {
		id   string
		in   string
		want string
	}{
		{"sq001", "a\n\nb", "a b"},
		{"sq002", "a b", "a b"},
		{"sq003", "x\vy", "x y"},
		{"sq004", "a b", "a b"},
		{"sq005", "  lead", " lead"},
		{"sq006", "trail  ", "trail "},
		{"sq007", "a\t\tb", "a b"},
		{"sq008", "\u3000x\u3000", " x "},
		{"sq009", "one space", "one space"},
		{"sq010", "", ""},
		{"sq011", "\ufeffz", " z"},
	} {
		if got := spaceRuns.ReplaceAllString(v.in, " "); got != v.want {
			t.Errorf("%s squash(%q) = %q, want %q", v.id, v.in, got, v.want)
		}
	}
}

// The window names the range radar sums over, and the text carries the costs it answered.
func TestDigestCostText(t *testing.T) {
	ch := openChannel(t, "dg")
	for _, n := range []string{"api", "web", "idle"} {
		if _, err := ch.Join(bg, n, JoinOpts{}); err != nil {
			t.Fatal(err)
		}
	}
	Link(bg, ch, "api", "claude-api")
	Link(bg, ch, "web", "claude-web")
	Link(bg, ch, "idle", "claude-idle")
	Link(bg, ch, "gone", "claude-gone")
	obs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		fmt.Fprint(w, `{"rows": [{"key": "claude-api", "label": "api", "requests": 10, "tokens": {}, "costUsd": 1.25}, {"key": "claude-web", "label": "web", "requests": 3, "tokens": {}, "costUsd": 0.5}, {"key": "claude-idle", "label": "idle", "requests": 1, "tokens": {}, "costUsd": 0}, {"key": "claude-gone", "label": "gone", "requests": 1, "tokens": {}, "costUsd": 5}]}`)
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
	for _, v := range []struct {
		since  any
		window string
	}{
		{"24h", "day"},
		{"7d", "week"},
		{"90d", "month"},
		{3600, "day"},
	} {
		d, err := Digest(bg, ch, v.since, DigestOpts{})
		if err != nil {
			t.Fatal(err)
		}
		if d.Cost.Range != v.window {
			t.Errorf("range(%v) = %q, want %q", v.since, d.Cost.Range, v.window)
		}
		if !d.Cost.Available || d.Cost.Total == nil || *d.Cost.Total != 6.75 {
			t.Errorf("cost(%v) = %+v", v.since, d.Cost)
		}
	}
	// a task a status event names only in its data, a task nobody knows, an ask about a task in
	// an object, and a note whose writer and task are gone
	if _, err := ch.Append(bg, "api", evIn{topic: "task.status", data: vv(t, `{"status": "done", "task": "t1", "note": "by data"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := ch.Append(bg, "web", evIn{topic: "task.status", data: vv(t, `{"status": "skipped", "task": "unknown", "note": "gone"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := ch.Send(bg, "web", sp("api"), "which task?", SendOpts{Ask: true, Task: sp("t1")}); err != nil {
		t.Fatal(err)
	}
	at := isoNow(time.Now())
	if err := ch.Store.Run(bg, "INSERT INTO tasks (id, title, status, created_at) VALUES ('gone-task','Gone','todo',?)", at); err != nil {
		t.Fatal(err)
	}
	if err := ch.Store.Run(bg, "INSERT INTO notes (task_id, kind, body, by, resolved, created_at) VALUES ('gone-task','note','orphan body',NULL,0,?)", at); err != nil {
		t.Fatal(err)
	}
	for _, since := range []any{"24h", nil, 45.5} {
		d, err := Digest(bg, ch, since, DigestOpts{})
		if err != nil {
			t.Fatal(err)
		}
		if since == "24h" {
			txt := DigestText(d)
			for _, want := range []string{"est. cost $6.75", "est. $1.25", "est. $0", "gone (left)"} {
				if !strings.Contains(txt, want) {
					t.Errorf("digest text misses %q:\n%s", want, txt)
				}
			}
			var orphan bool
			for _, s := range d.Sessions {
				if s.Name == Owner {
					for _, n := range s.Notes {
						if n.Task == "gone-task" && n.Kind == "note" {
							orphan = true
						}
					}
				}
			}
			if !orphan {
				t.Errorf("owner notes = %+v, want the orphan note", d.Sessions)
			}
		}
	}
}

// Radar's costs fold onto Huddle names in the order its rows name them, so the sums add in that
// order: a sorted fold would answer 0.6000000000000001 for these rows, the rows' order 0.6.
func TestDigestCostRowOrder(t *testing.T) {
	ch := openChannel(t, "dg")
	for _, n := range []string{"alpha", "mid", "zeta"} {
		if _, err := ch.Join(bg, n, JoinOpts{}); err != nil {
			t.Fatal(err)
		}
		Link(bg, ch, n, "claude-"+n)
	}
	obs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		fmt.Fprint(w, `{"rows": [
{"key": "claude-zeta", "label": "z", "requests": 1, "tokens": {}, "costUsd": 0.3},
{"key": "claude-alpha", "label": "a", "requests": 1, "tokens": {}, "costUsd": 0.2},
{"key": "claude-elsewhere", "label": "x", "requests": 1, "tokens": {}, "costUsd": 9},
{"key": "claude-mid", "label": "m", "requests": 1, "tokens": {}, "costUsd": 0.1}]}`)
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
	d, err := Digest(bg, ch, "24h", DigestOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if d == nil {
		t.Fatal("digest answered nothing")
	}
	raw, err := domain.MarshalCompactJSON(d.Cost)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"available":true,"total":0.6,"range":"day"}` {
		t.Errorf("cost = %s, want the rows' 0.6 total over a day", raw)
	}
	for name, usd := range map[string]float64{"zeta": 0.3, "alpha": 0.2, "mid": 0.1} {
		s := digestSession(d.Sessions, name)
		if s == nil || s.Cost == nil || *s.Cost != usd {
			t.Errorf("%s's cost = %+v, want %v", name, s, usd)
		}
	}
}
