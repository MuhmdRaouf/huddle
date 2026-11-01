package ingest_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/ingest"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// The watcher reloads the store before its first pass: the warm option runs in the
// watcher's own goroutine, and the dashboard reads "catching up" until both are done.

// warmWatcher starts a watcher that polls only by hand and runs warm before its first pass.
func warmWatcher(t *testing.T, env domain.EnvLookup, st *store.Store, warm func(context.Context) error,
	log func(string)) *ingest.Watcher {
	t.Helper()
	w := ingest.StartWatcher(t.Context(), ingest.WatcherOptions{
		Env:        env,
		Store:      st,
		SinceMs:    watchDay,
		IntervalMs: 3_600_000,
		RescanMs:   5_000,
		Now:        func() int64 { return watchT0 },
		Alive:      ownPidAlive,
		NewTicker:  func(time.Duration) (<-chan time.Time, func()) { return make(chan time.Time), func() {} },
		Warm:       warm,
		Log:        log,
	})
	t.Cleanup(w.Stop)
	return w
}

// onePromptTranscript writes a transcript whose one request the first pass ingests.
func onePromptTranscript(t *testing.T, e testEnv, session, requestID string) {
	t.Helper()
	writeText(t, filepath.Join(e.config, "projects", "-w-app", session+".jsonl"),
		jsonl(t, assistantLine(watchT1, requestID)))
}

func TestTheWatcherWarmsBeforeTheFirstPass(t *testing.T) {
	e := makeEnv(t)
	onePromptTranscript(t, e, "sess-f", "req-file")
	st := newStore()

	var mu sync.Mutex
	var order []string
	warmedWhenFileLanded := false
	st.OnUpdate(func(change store.Change) {
		mu.Lock()
		defer mu.Unlock()
		for _, request := range change.Requests {
			order = append(order, request.ID)
			if request.ID == "req-file" {
				for _, held := range st.RequestsFor(store.RequestFilter{}) {
					warmedWhenFileLanded = warmedWhenFileLanded || held.ID == "req-warmed"
				}
			}
		}
	})
	entered := make(chan struct{})
	release := make(chan struct{})
	warm := func(context.Context) error {
		close(entered)
		<-release
		st.AddRequest(makeWarmRequest("req-warmed"), nil)
		return nil
	}

	w := warmWatcher(t, e.env, st, warm, nil) // StartWatcher returns while warm still blocks
	<-entered
	if !w.CatchingUp() {
		t.Error("CatchingUp while the warm load runs: got false, want true")
	}
	if got := requestIDs(t, st, store.RequestFilter{}); len(got) != 0 {
		t.Errorf("the requests before the warm load ends: got %v, want none (no pass has run)", got)
	}

	close(release)
	waitFor(t, "the first pass after the warm load", 5*time.Second, func() bool { return !w.CatchingUp() })
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "req-warmed" || order[1] != "req-file" {
		t.Errorf("the requests in arrival order: got %v, want [req-warmed req-file]", order)
	}
	if !warmedWhenFileLanded {
		t.Error("the first pass ran against a store without the warmed request")
	}
}

func TestAWarmErrorFallsBackToAColdPass(t *testing.T) {
	e := makeEnv(t)
	onePromptTranscript(t, e, "sess-f", "req-file")
	st := newStore()
	var mu sync.Mutex
	var logs []string
	log := func(line string) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, line)
	}

	w := warmWatcher(t, e.env, st, func(context.Context) error { return errors.New("history is gone") }, log)
	waitFor(t, "the cold first pass", 5*time.Second, func() bool { return !w.CatchingUp() })

	if got := requestIDs(t, st, store.RequestFilter{}); len(got) != 1 || got[0] != "req-file" {
		t.Errorf("the requests after a failed warm load: got %v, want the file's [req-file]", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logs) != 1 || !strings.Contains(logs[0], "history is gone") {
		t.Errorf("the log after a failed warm load: got %q, want one line naming the error", logs)
	}
}

func TestTheWatcherStopsAWarmLoadWhenItStops(t *testing.T) {
	e := makeEnv(t)
	st := newStore()
	entered := make(chan struct{})
	warm := func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}
	w := warmWatcher(t, e.env, st, warm, func(string) {})
	<-entered
	w.Stop()
	select {
	case <-w.Done():
	default:
		t.Error("Stop returned with the watcher still running")
	}
}

// warmSource is a WarmSource holding a few reloaded events and requests.
type warmSource struct {
	events   []shared.EventRecord
	requests []shared.RequestRecord
}

func (s warmSource) WarmRequests(limit int, fn func(shared.RequestRecord, int64) error) error {
	for _, request := range s.requests {
		if err := fn(request, request.Ts); err != nil {
			return err
		}
	}
	return nil
}

func (warmSource) WarmTools(func(string, shared.ToolCallRecord) error) error { return nil }

func (s warmSource) WarmEvents(limit int, fn func(shared.EventRecord) error) error {
	for _, event := range s.events {
		if err := fn(event); err != nil {
			return err
		}
	}
	return nil
}

func (warmSource) WarmNodes(func(store.WarmNode) error) error { return nil }

func makeWarmRequest(id string) shared.RequestRecord {
	return shared.RequestRecord{
		ID: id, SessionID: "sess-w", AgentID: "main", Model: "claude-sonnet-5-5", Ts: watchT0,
		Tokens: shared.Tokens{Input: 1, Output: 1},
	}
}

func TestTheWatcherLearnsSessionUpstreamsFromTheWarmedEvents(t *testing.T) {
	e := makeEnv(t)
	onePromptTranscript(t, e, "sess-w", "req-file")
	st := newStore()
	hook := shared.SpoolLine{
		Ts: iso(watchT0 - 60_000), Event: "UserPromptSubmit", SessionID: ptr("sess-w"),
		BaseURL: ptr("https://proxy.example.test"),
	}
	source := warmSource{events: []shared.EventRecord{
		{Seq: 1, Ts: watchT0 - 60_000, Kind: hook.Event, SessionID: hook.SessionID, Payload: hook},
	}}

	w := warmWatcher(t, e.env, st, func(ctx context.Context) error {
		_, err := st.Warm(ctx, source)
		return err
	}, nil)
	waitFor(t, "the first pass", 5*time.Second, func() bool { return !w.CatchingUp() })

	if got := findRequest(t, st, "req-file").Upstream; got != "https://proxy.example.test" {
		t.Errorf("the file request's upstream: got %q, want the base URL the warmed hook line named", got)
	}
}

func TestTheWatcherLearnsRouterUpstreamsFromTheWarmedRoutes(t *testing.T) {
	e := makeEnv(t)
	e.env["KIMI_STATE_DIR"] = filepath.Join(e.home, "kimi")
	writeJobDir(t, e.env, e.home, "kimi", "k1",
		map[string]any{"attempts": []any{map[string]any{"n": 1}}, "updatedAt": "2030-01-01T00:00:00Z"})
	writeText(t, filepath.Join(e.home, "kimi", "jobs", "k1", "attempt-1.jsonl"),
		jsonl(t, assistantLine(watchT1, "kreq-1", "kimi-k2.5")))
	st := newStore()
	route := makeWarmRequest("route-kimi")
	route.Model, route.Upstream, route.Route, route.Via = "kimi-k2.5", "https://api.moonshot.ai/anthropic",
		ptr("provider"), ptr("kimi")
	source := warmSource{requests: []shared.RequestRecord{route}}

	w := warmWatcher(t, e.env, st, func(ctx context.Context) error {
		_, err := st.Warm(ctx, source)
		return err
	}, nil)
	waitFor(t, "the first pass", 5*time.Second, func() bool { return !w.CatchingUp() })

	jobRequests := st.Requests(sessionFilter("kimi:k1"))
	if len(jobRequests) != 1 || jobRequests[0].Upstream != "https://api.moonshot.ai/anthropic" {
		t.Errorf("the kimi job's request: got %+v, want the upstream its warmed route named", jobRequests)
	}
}
