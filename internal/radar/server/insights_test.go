package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/radar/alerts"
	"github.com/MuhmdRaouf/huddle/internal/radar/budget"
	"github.com/MuhmdRaouf/huddle/internal/radar/cost"
	"github.com/MuhmdRaouf/huddle/internal/radar/settings"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
)

// nowAt is the suite's clock: a fixed mid-October UTC noon, so day windows, ledger days and
// budget periods all hold one date.
var nowAt = time.Date(2026, time.October, 15, 12, 0, 0, 0, time.UTC)

// clock is the injected now every insights here runs on.
func clock() time.Time { return nowAt }

// testEnv is a whole throwaway world: HOME, RADAR_HOME, CLAUDE_CONFIG_DIR and
// ZAI_STATE_DIR all point into one temp directory, so no test reads or writes anything real.
type testEnv struct {
	env    domain.EnvLookup
	home   string
	state  string
	config string
	zai    string
}

func makeEnv(t *testing.T) testEnv {
	t.Helper()
	home := t.TempDir()
	return testEnv{
		env: domain.EnvLookup{
			"HOME":              home,
			"RADAR_HOME":        filepath.Join(home, "state"),
			"CLAUDE_CONFIG_DIR": filepath.Join(home, "claude"),
			"ZAI_STATE_DIR":     filepath.Join(home, "zai"),
		},
		home:   home,
		state:  filepath.Join(home, "state"),
		config: filepath.Join(home, "claude"),
		zai:    filepath.Join(home, "zai"),
	}
}

func strPtr(text string) *string { return &text }
func int64Ptr(v int64) *int64    { return &v }

// makeRequest is one stored request, edited in place by edit when there is one.
func makeRequest(edit func(*shared.RequestRecord)) shared.RequestRecord {
	latency := int64(100)
	record := shared.RequestRecord{
		ID:        "req-1",
		SessionID: "s1",
		AgentID:   "main",
		Model:     "claude-sonnet-5-5",
		Upstream:  "https://api.anthropic.com",
		Ts:        1_000,
		LatencyMs: &latency,
		Tokens:    shared.Tokens{Input: 10, Output: 5},
	}
	if edit != nil {
		edit(&record)
	}
	return record
}

// setLatency returns an edit that names one request's latency.
func setLatency(ms int64) func(*shared.RequestRecord) {
	return func(r *shared.RequestRecord) { r.LatencyMs = &ms }
}

// makeTool is one stored tool call, edited in place by edit when there is one.
func makeTool(edit func(*shared.ToolCallRecord)) shared.ToolCallRecord {
	duration := int64(50)
	record := shared.ToolCallRecord{
		ID:         "tool-1",
		SessionID:  "s1",
		AgentID:    strPtr("main"),
		Name:       "Bash",
		StartedAt:  1_000,
		DurationMs: &duration,
		Ok:         true,
	}
	if edit != nil {
		edit(&record)
	}
	return record
}

// mustValue decodes one test JSON document.
func mustValue(t *testing.T, text string) *jsfmt.Value {
	t.Helper()
	value, err := jsfmt.DecodeValue([]byte(text))
	if err != nil {
		t.Fatalf("bad test JSON %q: %v", text, err)
	}
	return value
}

// seedStore fills one store the way every insights test starts: a session over /w/app, a
// main-agent request and a subagent request on glm-5.3, the subagent named explorer.
func seedStore(st *store.Store) {
	cwd := "/w/app"
	st.UpsertSession(store.SessionUpsert{ID: "s1", Cwd: &cwd})
	st.AddRequest(makeRequest(func(r *shared.RequestRecord) {
		r.ID, r.Ts, r.Model, r.Tokens = "r1", nowAt.UnixMilli()-1000, "glm-5.3", outTokens
	}), nil)
	st.AddRequest(makeRequest(func(r *shared.RequestRecord) {
		r.ID, r.Ts, r.AgentID, r.Model, r.Tokens = "r2", nowAt.UnixMilli()-2000, "w1", "glm-5.3", outTokens
	}), nil)
	name := "explorer"
	st.UpsertAgent(store.AgentUpsert{SessionID: "s1", ID: "w1", Name: &name})
}

// seeded is one insights over the seeded store, with the notifier's command lines captured.
type seeded struct {
	env      testEnv
	store    *store.Store
	insights *Insights
	calls    *[][]string
}

func seededInsights(t *testing.T) seeded {
	t.Helper()
	world := makeEnv(t)
	st := store.CreateStore(clock)
	seedStore(st)
	calls := &[][]string{}
	created := CreateInsights(context.Background(), InsightsOptions{
		Env:    world.env,
		Store:  st,
		Now:    clock,
		TickMs: 0,
		Runner: func(command string, args []string) {
			*calls = append(*calls, append([]string{command}, args...))
		},
	})
	t.Cleanup(created.Stop)
	return seeded{env: world, store: st, insights: created, calls: calls}
}

// countingStore counts the whole-record reads: the store copy the caches exist to avoid.
type countingStore struct {
	*store.Store
	rawCalls int
}

func (c *countingStore) Raw() store.RawRecords {
	c.rawCalls++
	return c.Store.Raw()
}

// seededCounting seeds a store behind a counting read, then builds insights on it.
func seededCounting(t *testing.T) (*countingStore, *Insights) {
	t.Helper()
	world := makeEnv(t)
	counted := &countingStore{Store: store.CreateStore(clock)}
	seedStore(counted.Store)
	created := CreateInsights(context.Background(), InsightsOptions{
		Env:    world.env,
		Store:  counted,
		Now:    clock,
		TickMs: 0,
	})
	t.Cleanup(created.Stop)
	return counted, created
}

func TestInsights(t *testing.T) {
	t.Run("feeds the ledger from the store, before and after it starts", func(t *testing.T) {
		fresh := seededInsights(t)
		fresh.store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
			r.ID, r.Ts, r.Model = "r3", nowAt.UnixMilli(), "kimi-k3"
			r.Tokens = shared.Tokens{Input: 1000}
		}), nil)
		ids := make([]string, 0, 3)
		for _, row := range fresh.insights.Ledger().Rows(0) {
			ids = append(ids, row.ID)
		}
		slices.Sort(ids)
		if !slices.Equal(ids, []string{"r1", "r2", "r3"}) {
			t.Errorf("ledger rows = %v, want r1 r2 r3", ids)
		}
		spend := fresh.insights.SpendToday().USD
		if spend == nil {
			t.Fatal("spendToday = nil, want a figure")
		}
		if diff := float64(*spend) - 8.803; diff < -0.005 || diff > 0.005 {
			t.Errorf("spendToday = %v, want about 8.803", *spend)
		}
		providers := fresh.insights.Providers()
		for _, want := range []string{"zai", "kimi", "qwen"} {
			if !slices.Contains(providers, want) {
				t.Errorf("providers = %v, want %s among them", providers, want)
			}
		}
		rows := fresh.insights.Attribution(cost.ByAgent, cost.RangeDay)
		labels := make([]string, len(rows))
		for i, row := range rows {
			labels[i] = row.Label
		}
		want := []string{"main · app · s1", "explorer · app · s1"}
		if !slices.Equal(labels, want) {
			t.Errorf("agent attribution labels = %v, want %v", labels, want)
		}
	})

	t.Run("counts today's calls it could not price beside the spend", func(t *testing.T) {
		fresh := seededInsights(t)
		priced := fresh.insights.SpendToday()
		if priced.USD == nil || priced.Unpriced != 0 {
			t.Fatalf("seeded spend = %+v, want the two priced calls and nothing unpriced", priced)
		}
		// two router-only calls the router saw with no usage, and one on a model no table knows
		for _, id := range []string{"route-1", "route-2"} {
			fresh.store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
				r.ID, r.Ts, r.Model, r.Tokens = id, nowAt.UnixMilli()-500, "claude-opus-5-5", shared.Tokens{}
			}), nil)
		}
		fresh.store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
			r.ID, r.Ts, r.Model, r.Tokens = "mystery", nowAt.UnixMilli()-500, "mystery-model", outTokens
		}), nil)
		spend := fresh.insights.SpendToday()
		if spend.USD == nil || *spend.USD != *priced.USD {
			t.Errorf("spend = %v, want the two priced calls' %v", spend.USD, *priced.USD)
		}
		if spend.Unpriced != 3 {
			t.Errorf("unpriced = %d, want 3", spend.Unpriced)
		}
	})

	t.Run("saves budgets and settings, rejecting bad input, and writes the status file", func(t *testing.T) {
		ctx := context.Background()
		fresh := seededInsights(t)
		_, refusal, err := fresh.insights.SetBudgets(ctx, mustValue(t, `{"budgets":[{"id":"x"}]}`))
		if err != nil {
			t.Fatalf("SetBudgets(bad) errored: %v", err)
		}
		if !strings.Contains(refusal, "scope") {
			t.Errorf("SetBudgets(bad) refused with %q, want it to name the scope", refusal)
		}
		saved, _, err := fresh.insights.SetBudgets(ctx, mustValue(t,
			`{"budgets":[{"id":"z","scope":"provider:zai","period":"day","limitUsd":2,"action":"stop"}]}`))
		if err != nil {
			t.Fatalf("SetBudgets(good) errored: %v", err)
		}
		if len(saved) != 1 {
			t.Fatalf("SetBudgets(good) saved %d budgets, want 1", len(saved))
		}
		if onDisk := budget.ReadBudgets(ctx, fresh.env.env); len(onDisk) != 1 {
			t.Errorf("budgets.json holds %d budgets, want 1", len(onDisk))
		}
		data, err := os.ReadFile(budget.BudgetStatusPath(ctx, fresh.env.env))
		if err != nil {
			t.Fatalf("read budget-status.json: %v", err)
		}
		var status struct {
			Stopped []string `json:"stopped"`
			Spend   []struct {
				ID  string  `json:"id"`
				Pct float64 `json:"pct"`
			} `json:"spend"`
		}
		if err := json.Unmarshal(data, &status); err != nil {
			t.Fatalf("parse budget-status.json: %v", err)
		}
		if !slices.Equal(status.Stopped, []string{"provider:zai"}) {
			t.Errorf("status stopped = %v, want provider:zai", status.Stopped)
		}
		if len(status.Spend) != 1 || status.Spend[0].ID != "z" || status.Spend[0].Pct != 440 {
			t.Errorf("status spend = %+v, want one z at 440 percent", status.Spend)
		}
		if live := fresh.insights.BudgetStatus(); !slices.Equal(live.Stopped, []string{"provider:zai"}) {
			t.Errorf("budgetStatus stopped = %v, want provider:zai", live.Stopped)
		}
		kinds := make([]string, 0)
		for _, alert := range fresh.insights.Alerts() {
			kinds = append(kinds, string(alert.Kind))
		}
		if !slices.Equal(kinds, []string{"budget"}) {
			t.Errorf("alerts = %v, want one budget", kinds)
		}
		if _, refusal, _ := fresh.insights.SetSettings(ctx, mustValue(t, `{"notifications":"x"}`)); !strings.Contains(refusal, "true or false") {
			t.Errorf("SetSettings(bad) refused with %q, want it to say true or false", refusal)
		}
		next, _, err := fresh.insights.SetSettings(ctx, mustValue(t, `{"notifications":false}`))
		if err != nil {
			t.Fatalf("SetSettings(false) errored: %v", err)
		}
		if next != (settings.Settings{Notifications: false, HistoryRetentionDays: 30}) {
			t.Errorf("SetSettings(false) = %+v, want notifications off and 30 days", next)
		}
		if held := fresh.insights.Settings(); held != (settings.Settings{Notifications: false, HistoryRetentionDays: 30}) {
			t.Errorf("settings() = %+v, want notifications off and 30 days", held)
		}
	})

	t.Run("a failed status write reaches the caller, the budgets still saved", func(t *testing.T) {
		ctx := context.Background()
		fresh := seededInsights(t)
		// a directory where budget-status.json goes: the atomic rename onto it cannot land
		if err := os.Mkdir(budget.BudgetStatusPath(ctx, fresh.env.env), 0o700); err != nil {
			t.Fatal(err)
		}
		saved, _, err := fresh.insights.SetBudgets(ctx, mustValue(t,
			`{"budgets":[{"id":"z","scope":"provider:zai","period":"day","limitUsd":2,"action":"warn"}]}`))
		if err == nil {
			t.Fatal("SetBudgets swallowed the failed status write, want the error")
		}
		if saved != nil {
			t.Errorf("SetBudgets returned %d budgets with the failure, want none", len(saved))
		}
		if onDisk := budget.ReadBudgets(ctx, fresh.env.env); len(onDisk) != 1 {
			t.Errorf("budgets.json holds %d budgets, want the one saved before the status", len(onDisk))
		}
	})

	t.Run("dismisses alerts by id and notifies new ones on a tick", func(t *testing.T) {
		ctx := context.Background()
		fresh := seededInsights(t)
		if _, _, err := fresh.insights.SetBudgets(ctx, mustValue(t,
			`{"budgets":[{"id":"t","scope":"total","period":"month","limitUsd":1,"action":"warn"}]}`)); err != nil {
			t.Fatalf("SetBudgets errored: %v", err)
		}
		list := fresh.insights.Alerts()
		if len(list) != 1 || list[0].Kind != shared.AlertBudget {
			t.Fatalf("alerts = %+v, want one budget alert", list)
		}
		fresh.insights.Tick(ctx)
		// only darwin has a notifier command; elsewhere nothing may run at all
		if runtime.GOOS == "darwin" && len(*fresh.calls) != 1 {
			t.Errorf("the tick ran %d commands, want 1: %v", len(*fresh.calls), *fresh.calls)
		}
		if fresh.insights.Dismiss("") {
			t.Error("Dismiss(\"\") = true, want false")
		}
		if !fresh.insights.Dismiss(list[0].ID) {
			t.Fatal("Dismiss(alert id) = false, want true")
		}
		if remaining := fresh.insights.Alerts(); len(remaining) != 0 {
			t.Errorf("alerts after dismissal = %+v, want none", remaining)
		}
		if providers := fresh.insights.RouterHealth().Providers; len(providers) != 0 {
			t.Errorf("router health providers = %+v, want none", providers)
		}
		if checked := fresh.insights.Advisor().RunsChecked; checked != 1 {
			t.Errorf("advisor runsChecked = %d, want 1", checked)
		}
		if text := fresh.insights.Metrics("9"); !strings.Contains(text, `project="app"`) {
			t.Errorf("metrics text has no project=\"app\" line:\n%s", text)
		}
	})

	t.Run("serves advisor and metrics from a version-keyed cache, not a store copy per call", func(t *testing.T) {
		counted, cached := seededCounting(t)
		before := counted.rawCalls // the one call the start-up tick made
		cached.Advisor()
		cached.Advisor()
		if counted.rawCalls != before+1 {
			t.Errorf("two advisor reads cost %d copies, want 1", counted.rawCalls-before)
		}
		text := cached.Metrics("0.0.1")
		cached.Metrics("0.0.1")
		// the alerts the text folds in come from their own cache, so one build is one copy
		if counted.rawCalls != before+2 {
			t.Errorf("two metrics reads cost %d copies, want 2", counted.rawCalls-before)
		}
		if again := cached.Metrics("0.0.1"); again != text {
			t.Error("the metrics text is not served from the cache")
		}
		if counted.rawCalls != before+2 {
			t.Errorf("a cached metrics read cost a copy: %d", counted.rawCalls-before)
		}
		counted.Store.AddRequest(makeRequest(func(r *shared.RequestRecord) {
			r.ID, r.Ts, r.Model, r.Tokens = "r9", nowAt.UnixMilli(), "kimi-k3", outTokens
		}), nil)
		cached.Advisor()
		if counted.rawCalls != before+3 {
			// the version moved: the answer builds once, then holds again
			t.Errorf("a moved version cost %d copies, want 1", counted.rawCalls-before)
		}
	})

	t.Run("rebuilds the metrics text when the budgets change, not only when the store does", func(t *testing.T) {
		ctx := context.Background()
		counted, cached := seededCounting(t)
		before := counted.rawCalls
		first := cached.Metrics("0.0.1")
		if _, _, err := cached.SetBudgets(ctx, mustValue(t,
			`{"budgets":[{"id":"z","scope":"provider:zai","period":"day","limitUsd":2,"action":"warn"}]}`)); err != nil {
			t.Fatalf("SetBudgets errored: %v", err)
		}
		if next := cached.Metrics("0.0.1"); next == first {
			t.Error("the metrics text did not rebuild after the budgets changed")
		}
		if counted.rawCalls != before+3 {
			t.Errorf("the rebuild cost %d copies, want 3", counted.rawCalls-before)
		}
	})
}

// midPassStore raises the ingest backlog word at the alert snapshot's first read: a pass
// that starts between the tick's backlog read and its snapshot, whose half-fed store the
// snapshot then reads. The overspend itself is seeded ahead of time; the word is the part
// under test.
type midPassStore struct {
	*store.Store
	word  *backlogWatcher
	armed bool
}

func (m *midPassStore) Version() int64 {
	if m.armed {
		m.armed = false
		m.word.set(true)
	}
	return m.Store.Version()
}

func TestInsightsHoldsNotificationsAcrossAPass(t *testing.T) {
	t.Run("holds the notification when a pass starts between the backlog read and the snapshot", func(t *testing.T) {
		world := makeEnv(t)
		ctx := context.Background()
		// one glm-5.3 call that overspends a $1 monthly budget: an alert worth a
		// notification from the first snapshot that sees it
		writeText(t, filepath.Join(world.state, "budgets.json"),
			`{"version":1,"budgets":[{"id":"zai-month","scope":"provider:zai","period":"month","limitUsd":1,"action":"warn"}]}`)
		st := store.CreateStore(clock)
		st.AddRequest(makeRequest(func(r *shared.RequestRecord) {
			r.ID, r.Ts, r.Model = "r-over", nowAt.UnixMilli(), "glm-5.3"
			r.Tokens = shared.Tokens{Input: 1_000_000, Output: 1_000_000}
		}), nil)
		word := &backlogWatcher{}
		calls := &[][]string{}
		// darwin names a notifier command on every platform the suite runs on, so the
		// runner is reached wherever the test runs
		notifier := alerts.NewNotifier(ctx, alerts.NotifierOptions{
			Env:      world.env,
			Enabled:  func() bool { return true },
			Runner:   func(command string, args []string) { *calls = append(*calls, append([]string{command}, args...)) },
			Platform: "darwin",
			Now:      clock,
		})
		insights := CreateInsights(ctx, InsightsOptions{
			Env: world.env,
			Store: &midPassStore{
				Store: st, word: word, armed: true,
			},
			Now:      clock,
			TickMs:   0,
			Notifier: notifier,
			Watcher:  word,
		})
		t.Cleanup(insights.Stop)

		// the overspend answers in the alerts while the word is up: the hold is what stops
		// the notification, not a missing alert
		if list := insights.Alerts(); len(list) == 0 {
			t.Fatalf("the overspend = no alert, want one while the pass runs")
		}
		if !word.CatchingUp() {
			t.Fatal("the word is down, want the pass running")
		}
		if len(*calls) != 0 {
			t.Fatalf("a notification went out while the pass was mid-flight: %v", *calls)
		}
		// the pass drains: the held notification goes out on the next tick
		word.set(false)
		insights.Tick(ctx)
		if len(*calls) != 1 {
			t.Fatalf("the drained pass notified %d times, want 1: %v", len(*calls), *calls)
		}
		body, err := os.ReadFile(alerts.NotifiedPath(ctx, world.env))
		if err != nil || !strings.Contains(string(body), "budget:zai-month") {
			t.Fatalf("notified.json = %q (%v), want the budget alert remembered", body, err)
		}
	})
}

func TestInsightsRaisesQuotaNearlyUsedOffTheNewestSnapshot(t *testing.T) {
	world := makeEnv(t)
	st := store.CreateStore(clock)
	quotas := CreateQuotas(world.env)
	created := CreateInsights(context.Background(), InsightsOptions{
		Env: world.env, Store: st, Now: clock, TickMs: 0, Quotas: quotas,
	})
	t.Cleanup(created.Stop)
	quotaKinds := func() []shared.Alert {
		var out []shared.Alert
		for _, alert := range created.Alerts() {
			if alert.Kind == shared.AlertQuotaNearlyUsed {
				out = append(out, alert)
			}
		}
		return out
	}
	if got := quotaKinds(); len(got) != 0 {
		t.Fatalf("no snapshot: alerts = %+v, want none", got)
	}
	used, resets := 85.0, nowAt.Add(2*time.Hour).Unix()
	if _, err := quotas.Record(context.Background(), QuotaSnapshot{
		SessionID: "s1", FiveHour: &used, FiveHourResetsAt: &resets, At: nowAt.Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	got := quotaKinds()
	if len(got) != 1 || got[0].Detail != "Claude 5-hour window 85% used, resets 14:00 UTC" {
		t.Fatalf("alerts = %+v, want one for the 5-hour window", got)
	}
}
