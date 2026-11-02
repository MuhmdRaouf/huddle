package ingest_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/MuhmdRaouf/huddle/internal/radar/ingest"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// The watcher reads a changed file when the operating system says it changed, and keeps a slow
// scan for what the feed cannot tell it: a folder made before the watch armed, an event the
// system dropped, the open-files limit.

// fakeFolderWatch is a change feed the test drives: it arms folders until its limit, counts
// every Add, and delivers only the events the test sends.
type fakeFolderWatch struct {
	mu       sync.Mutex
	limit    int // folders it arms before Add fails with EMFILE; negative arms any number
	cost     func(dir string) int
	held     []string
	attempts int
	events   chan fsnotify.Event
	errors   chan error
}

func newFakeFolderWatch(limit int) *fakeFolderWatch {
	return &fakeFolderWatch{limit: limit, events: make(chan fsnotify.Event, 64), errors: make(chan error, 4)}
}

func (f *fakeFolderWatch) open() (ingest.FolderWatch, error) { return f, nil }

func (f *fakeFolderWatch) Add(dir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	if f.limit >= 0 && len(f.held) >= f.limit {
		return fmt.Errorf("add %q: %w", dir, syscall.EMFILE)
	}
	f.held = append(f.held, dir)
	return nil
}

func (f *fakeFolderWatch) Remove(dir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.held = slices.DeleteFunc(f.held, func(held string) bool { return held == dir })
	return nil
}

func (f *fakeFolderWatch) Cost(dir string) int {
	if f.cost == nil {
		return 1
	}
	return f.cost(dir)
}

func (f *fakeFolderWatch) names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.held)
}

func (f *fakeFolderWatch) Events() <-chan fsnotify.Event { return f.events }
func (f *fakeFolderWatch) Errors() <-chan error          { return f.errors }
func (f *fakeFolderWatch) Close() error                  { return nil }

func (f *fakeFolderWatch) holds() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.held)
}

func (f *fakeFolderWatch) tried() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts
}

// startFolderWatcher starts a watcher whose poll clock never fires on its own and whose
// fallback scan is an hour away, so only the change feed or a hand-fired tick reads anything.
func startFolderWatcher(t *testing.T, e testEnv, st *store.Store, options ingest.WatcherOptions) *ingest.Watcher {
	t.Helper()
	options.Env = e.env
	options.Store = st
	options.SinceMs = watchDay
	options.IntervalMs = 3_600_000
	options.RescanMs = 5_000
	options.Alive = ownPidAlive
	if options.OpenFilesLimit == nil {
		options.OpenFilesLimit = func() (uint64, error) { return 1 << 20, nil }
	}
	if options.Now == nil {
		options.Now = func() int64 { return watchT0 }
	}
	w := ingest.StartWatcher(t.Context(), options)
	t.Cleanup(w.Stop)
	waitFor(t, "initial ingest", 5*time.Second, func() bool { return !w.CatchingUp() })
	return w
}

func hasRequest(t *testing.T, st *store.Store, id string) func() bool {
	return func() bool { return slices.Contains(requestIDs(t, st, store.RequestFilter{}), id) }
}

func TestTheWatcherReadsAChangedFileWithoutATick(t *testing.T) {
	t.Run("a line appended to a transcript", func(t *testing.T) {
		e := makeEnv(t)
		path := writeText(t, filepath.Join(e.config, "projects", "-w-app", "sess-a.jsonl"),
			jsonl(t, assistantLine(watchT1, "req-1")))
		st := newStore()
		startFolderWatcher(t, e, st, ingest.WatcherOptions{})
		waitFor(t, "the first line", 5*time.Second, hasRequest(t, st, "req-1"))

		appendText(t, path, oneLine(t, assistantLine(watchT1+1, "req-2")))
		waitFor(t, "the appended line, no tick", time.Second, hasRequest(t, st, "req-2"))
	})

	t.Run("a line appended to a spool file", func(t *testing.T) {
		e := makeEnv(t)
		path := writeText(t, spoolFilePath(t, e.env), jsonl(t, map[string]any{
			"ts": iso(watchT0), "event": "UserPromptSubmit", "session_id": "s1", "prompt": "a",
		}))
		st := newStore()
		startFolderWatcher(t, e, st, ingest.WatcherOptions{})
		limit := int64(10)
		count := func() int { return len(st.Events(store.EventsFilter{Limit: &limit})) }
		waitFor(t, "the first spool line", 5*time.Second, func() bool { return count() == 1 })

		appendText(t, path, oneLine(t, map[string]any{
			"ts": iso(watchT1), "event": "Stop", "session_id": "s1",
		}))
		waitFor(t, "the appended spool line, no tick", time.Second, func() bool { return count() == 2 })
	})

	t.Run("a transcript in a project folder made after the watch armed", func(t *testing.T) {
		e := makeEnv(t)
		writeText(t, filepath.Join(e.config, "projects", "-w-app", "sess-a.jsonl"),
			jsonl(t, assistantLine(watchT1, "req-1")))
		st := newStore()
		startFolderWatcher(t, e, st, ingest.WatcherOptions{})
		waitFor(t, "the first line", 5*time.Second, hasRequest(t, st, "req-1"))

		late := filepath.Join(e.config, "projects", "-w-late", "sess-late.jsonl")
		writeText(t, late, jsonl(t, assistantLine(watchT1, "req-late")))
		waitFor(t, "the new folder's line, no tick", time.Second, hasRequest(t, st, "req-late"))

		appendText(t, late, oneLine(t, assistantLine(watchT1+1, "req-later")))
		waitFor(t, "the new folder's appended line, no tick", time.Second, hasRequest(t, st, "req-later"))
	})

	t.Run("a subagent transcript once its meta file is written", func(t *testing.T) {
		e := makeEnv(t)
		session := filepath.Join(e.config, "projects", "-w-app", "sess-a")
		writeText(t, filepath.Join(e.config, "projects", "-w-app", "sess-a.jsonl"),
			jsonl(t, assistantLine(watchT1, "req-1")))
		st := newStore()
		startFolderWatcher(t, e, st, ingest.WatcherOptions{})
		waitFor(t, "the first line", 5*time.Second, hasRequest(t, st, "req-1"))

		subagents := filepath.Join(session, "subagents")
		writeText(t, filepath.Join(subagents, "agent-w1.jsonl"), jsonl(t, assistantLine(watchT1, "sreq-1")))
		writeText(t, filepath.Join(subagents, "agent-w1.meta.json"),
			`{"agentType":"Explore","description":"find the flaky test"}`)
		waitFor(t, "the subagent's line, no tick", time.Second, hasRequest(t, st, "sreq-1"))
		agent := findAgent(t, st.SessionDetail("sess-a"), "agent-w1")
		if agent.Name == nil || *agent.Name != "Explore — find the flaky test" {
			t.Errorf("agent-w1 name = %v, want Explore — find the flaky test", agent.Name)
		}
	})
}

func TestAFolderTheFeedNeverReportedIsReadByTheFallbackScan(t *testing.T) {
	e := makeEnv(t)
	writeText(t, filepath.Join(e.config, "projects", "-w-app", "sess-a.jsonl"),
		jsonl(t, assistantLine(watchT1, "req-1")))
	st := newStore()
	feed := newFakeFolderWatch(-1)
	tick := newFakeTicker()
	var clock atomic.Int64
	clock.Store(watchT0)
	startFolderWatcher(t, e, st, ingest.WatcherOptions{
		NewFolderWatch: feed.open, NewTicker: tick.new, Now: clock.Load,
	})
	waitFor(t, "the first line", 5*time.Second, hasRequest(t, st, "req-1"))

	writeText(t, filepath.Join(e.config, "projects", "-w-late", "sess-late.jsonl"),
		jsonl(t, assistantLine(watchT1, "req-late")))
	time.Sleep(100 * time.Millisecond)
	if hasRequest(t, st, "req-late")() {
		t.Fatal("the late folder was read before any event or tick")
	}

	clock.Add(30_000)
	tick.fire()
	waitFor(t, "the fallback scan reading the late folder", 5*time.Second, hasRequest(t, st, "req-late"))
}

func TestTheWatchLimitKeepsWhatItHoldsAndTheFallbackReadsTheRest(t *testing.T) {
	e := makeEnv(t)
	const folders = 6
	var paths []string
	for i := range folders {
		paths = append(paths, writeText(t,
			filepath.Join(e.config, "projects", fmt.Sprintf("-w-app%d", i), "sess.jsonl"),
			jsonl(t, assistantLine(watchT1, fmt.Sprintf("req-%d", i)))))
	}
	st := newStore()
	feed := newFakeFolderWatch(2)
	tick := newFakeTicker()
	var clock atomic.Int64
	clock.Store(watchT0)
	var mu sync.Mutex
	var lines []string
	startFolderWatcher(t, e, st, ingest.WatcherOptions{
		NewFolderWatch: feed.open, NewTicker: tick.new, Now: clock.Load,
		Log: func(line string) { mu.Lock(); defer mu.Unlock(); lines = append(lines, line) },
	})
	for i := range folders {
		waitFor(t, "every folder's first pass", 5*time.Second, hasRequest(t, st, fmt.Sprintf("req-%d", i)))
	}
	waitFor(t, "the limit met", 5*time.Second, func() bool { return feed.tried() == 3 })

	if got := feed.holds(); got != 2 {
		t.Errorf("watches held = %d, want the 2 armed before the limit", got)
	}
	mu.Lock()
	limitLines := slices.DeleteFunc(slices.Clone(lines), func(line string) bool { return !strings.Contains(line, "watch") })
	mu.Unlock()
	if len(limitLines) != 1 || !strings.Contains(limitLines[0], "holds 2") || !strings.Contains(limitLines[0], "limit") {
		t.Errorf("log lines about the watch = %q, want one naming the 2 it holds and the limit", limitLines)
	}

	appendText(t, paths[5], oneLine(t, assistantLine(watchT1+1, "req-after")))
	clock.Add(30_000)
	tick.fire()
	waitFor(t, "the fallback scan reading an unwatched folder", 5*time.Second, hasRequest(t, st, "req-after"))
	if got := feed.tried(); got != 3 {
		t.Errorf("Add attempts = %d after a fallback scan, want 3: no retry once the limit is met", got)
	}
}

func TestAnIdleWatcherMakesNoStatCallsBetweenFallbackScans(t *testing.T) {
	e := makeEnv(t)
	const folders = 200
	for i := range folders {
		writeText(t, filepath.Join(e.config, "projects", fmt.Sprintf("-w-app%d", i), "sess.jsonl"),
			jsonl(t, assistantLine(watchT1, fmt.Sprintf("req-%d", i))))
	}
	st := newStore()
	feed := newFakeFolderWatch(-1)
	tick := newFakeTicker()
	var clock atomic.Int64
	clock.Store(watchT0)
	var stats atomic.Int64
	rescan := newFakeTicker()
	startFolderWatcher(t, e, st, ingest.WatcherOptions{
		NewFolderWatch: feed.open, NewTicker: tick.new, NewRescanTicker: rescan.new, Now: clock.Load,
		Stat: func(path string) (os.FileInfo, error) { stats.Add(1); return os.Stat(path) },
	})
	waitFor(t, "every folder armed", 5*time.Second, func() bool { return feed.holds() >= folders })
	if stats.Load() == 0 {
		t.Fatal("the first pass made no stat call through the seam")
	}

	time.Sleep(200 * time.Millisecond)
	settled := stats.Load()
	time.Sleep(300 * time.Millisecond)
	if got := stats.Load(); got != settled {
		t.Errorf("stat calls grew from %d to %d with 200 watched folders and no writes", settled, got)
	}

	clock.Add(5_000)
	rescan.fire()
	time.Sleep(300 * time.Millisecond)
	if got := stats.Load(); got != settled {
		t.Errorf("stat calls grew from %d to %d across a registry rescan tick, want no transcript stat", settled, got)
	}

	clock.Add(30_000)
	tick.fire()
	waitFor(t, "the fallback scan's stat calls", 5*time.Second, func() bool { return stats.Load() > settled })
}

func TestTheFallbackScanDefaultsToThirtySeconds(t *testing.T) {
	e := makeEnv(t)
	asked := make(chan time.Duration, 1)
	w := ingest.StartWatcher(t.Context(), ingest.WatcherOptions{
		Env: e.env, Store: newStore(), SinceMs: watchDay, Alive: ownPidAlive,
		NewFolderWatch: newFakeFolderWatch(-1).open,
		NewTicker: func(d time.Duration) (<-chan time.Time, func()) {
			asked <- d
			return make(chan time.Time), func() {}
		},
	})
	t.Cleanup(w.Stop)
	if got := <-asked; got != 30*time.Second {
		t.Errorf("default poll interval = %v, want the 30 s fallback scan", got)
	}
}

// folderNames is the folders the feed holds, last path element each, in name order.
func folderNames(feed *fakeFolderWatch) []string {
	var names []string
	for _, dir := range feed.names() {
		names = append(names, filepath.Base(dir))
	}
	slices.Sort(names)
	return names
}

func TestTheBudgetStopsArmingAndTheFallbackScanReadsTheRest(t *testing.T) {
	e := makeEnv(t)
	var paths []string
	for i := range 6 {
		paths = append(paths, writeText(t,
			filepath.Join(e.config, "projects", fmt.Sprintf("-w-app%d", i), "sess.jsonl"),
			jsonl(t, assistantLine(watchT1, fmt.Sprintf("req-%d", i)))))
	}
	st := newStore()
	feed := newFakeFolderWatch(-1)
	feed.cost = func(string) int { return 3 }
	tick := newFakeTicker()
	var clock atomic.Int64
	clock.Store(watchT0)
	var mu sync.Mutex
	var lines []string
	// a quarter of 40 descriptors is a budget of 10: three watches at 3 each, the spool, the
	// projects root and one project folder
	startFolderWatcher(t, e, st, ingest.WatcherOptions{
		NewFolderWatch: feed.open, NewTicker: tick.new, Now: clock.Load,
		OpenFilesLimit: func() (uint64, error) { return 40, nil },
		Log:            func(line string) { mu.Lock(); defer mu.Unlock(); lines = append(lines, line) },
	})
	for i := range 6 {
		waitFor(t, "every folder's first pass", 5*time.Second, hasRequest(t, st, fmt.Sprintf("req-%d", i)))
	}
	waitFor(t, "arming settled", 5*time.Second, func() bool { return feed.holds() == 3 })

	mu.Lock()
	budgetLines := slices.DeleteFunc(slices.Clone(lines), func(line string) bool { return !strings.Contains(line, "watch") })
	mu.Unlock()
	if len(budgetLines) != 1 || !strings.Contains(budgetLines[0], "holds 3") || !strings.Contains(budgetLines[0], "budget of 10") {
		t.Errorf("log lines about the watch = %q, want one naming the 3 it holds and the budget of 10", budgetLines)
	}

	appendText(t, paths[5], oneLine(t, assistantLine(watchT1+1, "req-after")))
	clock.Add(30_000)
	tick.fire()
	waitFor(t, "the fallback scan reading a folder past the budget", 5*time.Second, hasRequest(t, st, "req-after"))
	if got := feed.tried(); got != 3 {
		t.Errorf("Add attempts = %d after a fallback scan, want 3: nothing past the budget is armed", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(lines) != len(budgetLines) {
		t.Errorf("log lines after the scan = %q, want the one line only", lines)
	}
}

func TestAFolderThatWentQuietIsUnwatchedAndItsCostIsReleased(t *testing.T) {
	e := makeEnv(t)
	quiet := writeText(t, filepath.Join(e.config, "projects", "-w-quiet", "sess.jsonl"),
		jsonl(t, assistantLine(watchT1, "req-quiet")))
	busy := writeText(t, filepath.Join(e.config, "projects", "-w-busy", "sess.jsonl"),
		jsonl(t, assistantLine(watchT1, "req-busy")))
	setMtime(t, quiet, watchT0)
	setMtime(t, busy, watchT0)
	setMtime(t, filepath.Dir(quiet), watchT0) // no entry added to the folder either
	st := newStore()
	feed := newFakeFolderWatch(-1)
	feed.cost = func(string) int { return 2 }
	tick := newFakeTicker()
	var clock atomic.Int64
	clock.Store(watchT0)
	// a budget of 8 descriptors: the spool, the projects root and two folders at 2 each
	startFolderWatcher(t, e, st, ingest.WatcherOptions{
		NewFolderWatch: feed.open, NewTicker: tick.new, Now: clock.Load,
		OpenFilesLimit: func() (uint64, error) { return 32, nil },
		Log:            func(string) {},
	})
	waitFor(t, "both folders armed", 5*time.Second, func() bool { return feed.holds() == 4 })

	// a third folder has no room until a watch is released
	fresh := filepath.Join(e.config, "projects", "-w-fresh", "sess.jsonl")
	writeText(t, fresh, jsonl(t, assistantLine(watchT1, "req-fresh")))
	feed.events <- fsnotify.Event{Name: fresh, Op: fsnotify.Create}
	waitFor(t, "the fresh folder's line", 5*time.Second, hasRequest(t, st, "req-fresh"))
	if slices.Contains(folderNames(feed), "-w-fresh") {
		t.Fatal("the fresh folder was armed past the budget")
	}

	// two days on, only the busy folder and the fresh one still hold a recent transcript
	clock.Add(2 * 86_400_000)
	setMtime(t, busy, clock.Load())
	setMtime(t, fresh, clock.Load())
	tick.fire()
	waitFor(t, "the quiet folder unwatched and the fresh one armed in its place", 5*time.Second, func() bool {
		names := folderNames(feed)
		return !slices.Contains(names, "-w-quiet") && slices.Contains(names, "-w-fresh") && slices.Contains(names, "-w-busy")
	})
}

func TestAFolderOutsideTheWatchWindowIsNeverArmed(t *testing.T) {
	e := makeEnv(t)
	recent := writeText(t, filepath.Join(e.config, "projects", "-w-recent", "sess.jsonl"),
		jsonl(t, assistantLine(watchT1, "req-recent")))
	old := writeText(t, filepath.Join(e.config, "projects", "-w-old", "sess.jsonl"),
		jsonl(t, assistantLine(watchT1, "req-old")))
	setMtime(t, recent, watchT0)
	setMtime(t, old, watchT0-2*86_400_000)
	// a spool line names the old transcript, which the scan then tails whatever its age
	writeText(t, spoolFilePath(t, e.env), jsonl(t, map[string]any{
		"ts": iso(watchT0), "event": "UserPromptSubmit", "session_id": "sess", "prompt": "a",
		"transcript_path": old,
	}))
	st := newStore()
	feed := newFakeFolderWatch(-1)
	startFolderWatcher(t, e, st, ingest.WatcherOptions{NewFolderWatch: feed.open, NewTicker: newFakeTicker().new})
	waitFor(t, "the recent folder armed", 5*time.Second, func() bool { return slices.Contains(folderNames(feed), "-w-recent") })
	time.Sleep(100 * time.Millisecond)
	if names := folderNames(feed); slices.Contains(names, "-w-old") {
		t.Errorf("folders armed = %v, want the folder holding only a 2 day old transcript left out", names)
	}
}

func TestARegistryEntryIsSeenWithinOneRescanTick(t *testing.T) {
	e, sessions := registryEnv(t)
	writeText(t, filepath.Join(e.config, "projects", "-w-app", "sess-a.jsonl"), jsonl(t, userLine(watchT0)))
	st := newStore()
	rescan := newFakeTicker()
	var clock atomic.Int64
	clock.Store(watchT0)
	startFolderWatcher(t, e, st, ingest.WatcherOptions{
		NewFolderWatch: newFakeFolderWatch(-1).open, NewTicker: newFakeTicker().new,
		NewRescanTicker: rescan.new, Now: clock.Load,
	})
	if detail := st.SessionDetail("sess-a"); detail != nil && detail.Live {
		t.Fatal("sess-a live before any registry entry")
	}

	writeText(t, filepath.Join(sessions, "81812.json"), fmt.Sprintf(
		`{"pid":%d,"sessionId":"sess-a","status":"busy"}`, os.Getpid()))
	clock.Add(5_000)
	rescan.fire()
	waitFor(t, "the registry entry seen by the rescan tick", 5*time.Second, func() bool {
		detail := st.SessionDetail("sess-a")
		return detail != nil && detail.Live
	})
}

func TestASessionFolderStaysArmedSoItsFirstSubagentIsReadAtOnce(t *testing.T) {
	e := makeEnv(t)
	project := filepath.Join(e.config, "projects", "-w-app")
	writeText(t, filepath.Join(project, "sess-a.jsonl"), jsonl(t, assistantLine(watchT1, "req-1")))
	st := newStore()
	tick := newFakeTicker()
	var clock atomic.Int64
	clock.Store(watchT0)
	startFolderWatcher(t, e, st, ingest.WatcherOptions{NewTicker: tick.new, Now: clock.Load})
	waitFor(t, "the first line", 5*time.Second, hasRequest(t, st, "req-1"))

	// the session folder holds no transcript yet; a scan runs before its first subagent
	session := filepath.Join(project, "sess-a")
	if err := os.MkdirAll(session, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", session, err)
	}
	time.Sleep(100 * time.Millisecond)
	clock.Add(30_000)
	tick.fire()
	time.Sleep(200 * time.Millisecond)

	subagents := filepath.Join(session, "subagents")
	writeText(t, filepath.Join(subagents, "agent-x.meta.json"), `{"agentType":"Explore","description":"look"}`)
	writeText(t, filepath.Join(subagents, "agent-x.jsonl"), jsonl(t, assistantLine(watchT1, "sreq-x")))
	waitFor(t, "the first subagent's line, no tick", time.Second, hasRequest(t, st, "sreq-x"))
}

func TestAFolderOlderThanTheWatchWindowIsUnwatchedWhateverItHolds(t *testing.T) {
	e := makeEnv(t)
	project := filepath.Join(e.config, "projects", "-w-app")
	writeText(t, filepath.Join(project, "sess-a.jsonl"), jsonl(t, assistantLine(watchT1, "req-1")))
	st := newStore()
	feed := newFakeFolderWatch(-1)
	tick := newFakeTicker()
	var clock atomic.Int64
	clock.Store(watchT0)
	startFolderWatcher(t, e, st, ingest.WatcherOptions{NewFolderWatch: feed.open, NewTicker: tick.new, Now: clock.Load})
	waitFor(t, "the first line", 5*time.Second, hasRequest(t, st, "req-1"))

	// two session folders made after the watch armed, neither holding a transcript
	recent := filepath.Join(project, "sess-recent")
	stale := filepath.Join(project, "sess-stale")
	for _, dir := range []string{recent, stale} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		feed.events <- fsnotify.Event{Name: dir, Op: fsnotify.Create}
	}
	waitFor(t, "both session folders armed", 5*time.Second, func() bool {
		names := folderNames(feed)
		return slices.Contains(names, "sess-recent") && slices.Contains(names, "sess-stale")
	})
	setMtime(t, recent, watchT0)
	setMtime(t, stale, watchT0-2*86_400_000)

	clock.Add(30_000)
	tick.fire()
	waitFor(t, "the stale folder unwatched", 5*time.Second, func() bool {
		return !slices.Contains(folderNames(feed), "sess-stale")
	})
	if !slices.Contains(folderNames(feed), "sess-recent") {
		t.Errorf("folders armed = %v, want the session folder made within the window kept", folderNames(feed))
	}
}

func TestATranscriptASpoolLineNamesIsReadOnThePassThatReadsTheLine(t *testing.T) {
	spoolLine := func(sessionID, transcript string) string {
		return oneLine(t, map[string]any{
			"ts": iso(watchT1), "event": "UserPromptSubmit", "session_id": sessionID, "transcript_path": transcript,
		})
	}

	t.Run("in a folder that held no transcript at the first read", func(t *testing.T) {
		e := makeEnv(t)
		quiet := filepath.Join(e.config, "projects", "-w-quiet")
		if err := os.MkdirAll(quiet, 0o700); err != nil {
			t.Fatal(err)
		}
		spool := writeText(t, spoolFilePath(t, e.env), oneLine(t, map[string]any{
			"ts": iso(watchT0), "event": "SessionStart", "session_id": "other",
		}))
		st := newStore()
		startFolderWatcher(t, e, st, ingest.WatcherOptions{})

		transcript := writeText(t, filepath.Join(quiet, "sess-new.jsonl"), jsonl(t, assistantLine(watchT1, "req-new")))
		appendText(t, spool, spoolLine("sess-new", transcript))
		waitFor(t, "the named transcript's line, no tick", time.Second, hasRequest(t, st, "req-new"))

		appendText(t, transcript, oneLine(t, assistantLine(watchT1+1, "req-newer")))
		waitFor(t, "the named transcript's appended line, no tick", time.Second, hasRequest(t, st, "req-newer"))
	})

	t.Run("with the subagents its session already has", func(t *testing.T) {
		e := makeEnv(t)
		quiet := filepath.Join(e.config, "projects", "-w-quiet")
		subagents := filepath.Join(quiet, "sess-new", "subagents")
		if err := os.MkdirAll(subagents, 0o700); err != nil {
			t.Fatal(err)
		}
		spool := writeText(t, spoolFilePath(t, e.env), oneLine(t, map[string]any{
			"ts": iso(watchT0), "event": "SessionStart", "session_id": "other",
		}))
		st := newStore()
		startFolderWatcher(t, e, st, ingest.WatcherOptions{})

		transcript := writeText(t, filepath.Join(quiet, "sess-new.jsonl"), jsonl(t, assistantLine(watchT1, "req-new")))
		writeText(t, filepath.Join(subagents, "agent-w1.jsonl"), jsonl(t, assistantLine(watchT1, "sreq-1")))
		writeText(t, filepath.Join(subagents, "agent-w1.meta.json"), `{"agentType":"Explore","description":"scan"}`)
		appendText(t, spool, spoolLine("sess-new", transcript))
		waitFor(t, "the subagent's line, no tick", time.Second, hasRequest(t, st, "sreq-1"))
	})
}
