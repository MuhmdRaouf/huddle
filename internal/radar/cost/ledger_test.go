package cost

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// ledgerEnv is a whole temp world: HOME and RADAR_HOME point into one throwaway directory,
// so no test can read or write anything real. Built from scratch (never copied from the
// process environment) so a stray variable set in the surrounding shell cannot leak a real
// path in.
type ledgerEnv struct {
	env   domain.EnvLookup
	home  string
	state string
}

func makeLedgerEnv(t *testing.T) ledgerEnv {
	t.Helper()
	home := t.TempDir()
	return ledgerEnv{
		env: domain.EnvLookup{
			"HOME":       home,
			"RADAR_HOME": filepath.Join(home, "state"),
		},
		home:  home,
		state: filepath.Join(home, "state"),
	}
}

// writeText mkdir -p's the parent, then writes with 0600 — the mode every state file has.
func writeText(t *testing.T, path, text string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// makeRequest is the suite's base record, which each case reshapes through shape.
func makeRequest(shape func(*shared.RequestRecord)) shared.RequestRecord {
	latency := int64(100)
	r := shared.RequestRecord{
		ID:        "req-1",
		SessionID: "s1",
		AgentID:   "main",
		Model:     "claude-sonnet-5-5",
		Upstream:  "https://api.anthropic.com",
		Ts:        1_000,
		LatencyMs: &latency,
		Tokens:    shared.Tokens{Input: 10, Output: 5},
		Provider:  "Anthropic",
	}
	shape(&r)
	return r
}

// makeTool is the suite's base tool call, which each case reshapes through shape.
func makeTool(shape func(*shared.ToolCallRecord)) shared.ToolCallRecord {
	duration := int64(50)
	r := shared.ToolCallRecord{
		ID:         "tool-1",
		SessionID:  "s1",
		AgentID:    str("main"),
		Name:       "Bash",
		StartedAt:  1_000,
		DurationMs: &duration,
		Ok:         true,
	}
	shape(&r)
	return r
}

// i64 is the pointer an optional count carries.
func i64(v int64) *int64 { return &v }

// atMs is the fixed clock a ledger test runs on.
func atMs(now int64) func() time.Time { return func() time.Time { return time.UnixMilli(now) } }

// fieldOf reads one field out of a decoded JSON object.
func fieldOf(t *testing.T, v *jsfmt.Value, key string) *jsfmt.Value {
	t.Helper()
	for i, k := range v.Keys() {
		if k == key {
			return v.Fields()[i]
		}
	}
	t.Fatalf("no %q field in %s", key, v.EncodeJSON())
	return nil
}

// ledgerJSONNames lists a day file's JSON file names ending in .json, in directory order.
func ledgerJSONNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	return names
}

func TestLedger(t *testing.T) {
	t.Run("records requests by id, replacing repeats and skipping requests with no model", func(t *testing.T) {
		e := makeLedgerEnv(t)
		now := time.Now().UnixMilli()
		ledger := NewLedger(t.Context(), e.env, atMs(now))
		ledger.Record(makeRequest(func(r *shared.RequestRecord) {
			r.ID, r.Ts, r.Model, r.Tokens = "a", now-1000, "glm-5.3", millionInput
		}))
		ledger.Record(makeRequest(func(r *shared.RequestRecord) {
			r.ID, r.Ts, r.Model = "a", now-1000, "glm-5.3"
			r.Tokens = shared.Tokens{Input: 1_000_000, Output: 5}
		}))
		ledger.Record(makeRequest(func(r *shared.RequestRecord) {
			r.ID, r.Ts, r.Model = "a", now-1000, "glm-5.3"
			r.Tokens = shared.Tokens{Input: 1_000_000, Output: 5}
		}))
		ledger.Record(makeRequest(func(r *shared.RequestRecord) { r.ID, r.Model, r.Ts = "e", "", now }))
		ledger.Record(makeRequest(func(r *shared.RequestRecord) {
			r.ID = "old"
			r.Ts = now - (LedgerDays+2)*dayMs
		}))
		rows := ledger.Rows(0)
		if len(rows) != 1 {
			t.Fatalf("rows: got %d want 1", len(rows))
		}
		if rows[0].Tokens.Output != 5 {
			t.Errorf("tokens.output: got %d want 5", rows[0].Tokens.Output)
		}
		if got := ledger.Rows(now); len(got) != 0 {
			t.Errorf("rows(now): got %d want 0", len(got))
		}
	})

	t.Run("moves a request that changed day, and flushes 0600 day files a new ledger reads back", func(t *testing.T) {
		e := makeLedgerEnv(t)
		now := time.Now().UnixMilli()
		ledger := NewLedger(t.Context(), e.env, atMs(now))
		ledger.Record(makeRequest(func(r *shared.RequestRecord) { r.ID, r.Ts = "a", now-2*dayMs }))
		ledger.Record(makeRequest(func(r *shared.RequestRecord) { r.ID, r.Ts = "a", now }))
		session := SessionNames{Project: str("app"), Title: str("fix it"), Repo: str("/w/repo")}
		ledger.NameSession("s1", session)
		ledger.NameSession("s1", session)
		agent := AgentNames{Name: "explorer", Type: str("Explore"), Description: str("look around")}
		ledger.NameAgent("s1", "w1", agent)
		ledger.NameAgent("s1", "w1", agent)
		before := ledger.Version()
		ledger.Flush(t.Context())
		ledger.Flush(t.Context())
		if ledger.Version() != before {
			t.Errorf("version: got %d want %d (a nothing-new flush keeps it)", ledger.Version(), before)
		}
		dir := LedgerDir(t.Context(), e.env)
		if _, err := os.Stat(filepath.Join(dir, DayKey(now)+".json")); err != nil {
			t.Fatalf("today's day file: %v", err)
		}
		info, err := os.Stat(filepath.Join(dir, DayKey(now)+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("day file mode: got %o want 600", info.Mode().Perm())
		}
		again := NewLedger(t.Context(), e.env, atMs(now))
		var ids []string
		for _, row := range again.Rows(0) {
			ids = append(ids, row.ID)
		}
		if !slices.Equal(ids, []string{"a"}) {
			t.Errorf("ids: got %v want [a]", ids)
		}
		want := LedgerNames{
			Sessions: map[string]SessionNames{"s1": session},
			Agents:   map[string]AgentNames{"s1:w1": agent},
		}
		if got := again.Names(); !reflect.DeepEqual(got, want) {
			t.Errorf("names: got %+v want %+v", got, want)
		}
	})

	t.Run("prunes days past the window and survives broken files and a missing dir", func(t *testing.T) {
		e := makeLedgerEnv(t)
		now := time.Now().UnixMilli()
		dir := LedgerDir(t.Context(), e.env)
		writeText(t, filepath.Join(dir, DayKey(now-(LedgerDays+3)*dayMs)+".json"), "{}")
		writeText(t, filepath.Join(dir, DayKey(now-dayMs)+".json"), "{broken")
		writeText(t, filepath.Join(dir, "notes.txt"), "ignored")
		ledger := NewLedger(t.Context(), e.env, atMs(now))
		if got := ledger.Rows(0); len(got) != 0 {
			t.Fatalf("rows: got %d want 0", len(got))
		}
		// a later clock: yesterday's file falls out of the window too
		later := NewLedger(t.Context(), e.env, atMs(now+LedgerDays*dayMs))
		later.Record(makeRequest(func(r *shared.RequestRecord) {
			r.ID = "x"
			r.Ts = now + LedgerDays*dayMs
		}))
		later.Flush(t.Context())
		want := []string{DayKey(now+LedgerDays*dayMs) + ".json"}
		if got := ledgerJSONNames(t, dir); !slices.Equal(got, want) {
			t.Errorf("day files: got %v want %v", got, want)
		}
		missing := NewLedger(t.Context(), domain.EnvLookup{"HOME": "/nonexistent-radar"}, nil)
		if got := missing.Rows(0); len(got) != 0 {
			t.Errorf("rows under a missing home: got %d want 0", len(got))
		}
	})

	t.Run("parses only well-formed entries, reading an older ledger's bare agent names", func(t *testing.T) {
		day := parseDay(`{
			"rows": {"ok": [1, "m", "s", "a", 1, 2, 3, 4], "bad": [1, "m"], "worse": "x"},
			"sessions": {
				"s": {"project": "p", "title": 3, "repo": "/w/r", "parentSessionId": "par", "name": "n"},
				"t": null,
				"u": {"project": "p", "repo": 7}
			},
			"agents": {"s:a": "legacy", "s:b": {"name": "renamed", "type": "Explore", "description": "D"}, "s:c": 4}
		}`)
		if got := day.rows.keys(); !slices.Equal(got, []string{"ok"}) {
			t.Errorf("rows: got %v want [ok]", got)
		}
		wantSessions := map[string]SessionNames{
			"s": {Project: str("p"), Repo: str("/w/r"), ParentSessionID: str("par"), Name: str("n")},
			"t": {},
			"u": {Project: str("p")},
		}
		if got := day.sessions.keys(); !slices.Equal(got, []string{"s", "t", "u"}) {
			t.Fatalf("sessions: got %v want [s t u]", got)
		}
		for id, want := range wantSessions {
			got, ok := day.sessions.get(id)
			if !ok || !namesEqual(got, want) {
				t.Errorf("session %s: got %+v want %+v", id, got, want)
			}
		}
		wantAgents := map[string]AgentNames{
			"s:a": {Name: "legacy"},
			"s:b": {Name: "renamed", Type: str("Explore"), Description: str("D")},
		}
		if got := day.agents.keys(); !slices.Equal(got, []string{"s:a", "s:b"}) {
			t.Fatalf("agents: got %v want [s:a s:b]", got)
		}
		for id, want := range wantAgents {
			got, ok := day.agents.get(id)
			if !ok || !agentsEqual(got, want) {
				t.Errorf("agent %s: got %+v want %+v", id, got, want)
			}
		}
		if got := parseDay("[]"); got.rows.len() != 0 {
			t.Errorf("parseDay([]): got %d rows want 0", got.rows.len())
		}
	})

	t.Run("never throws when the directory cannot be written", func(t *testing.T) {
		e := makeLedgerEnv(t)
		writeText(t, e.state, "a file where the state dir should be")
		ledger := NewLedger(t.Context(), e.env, nil)
		ledger.Record(makeRequest(func(r *shared.RequestRecord) { r.Ts = time.Now().UnixMilli() }))
		ledger.Flush(t.Context()) // must not panic: the ledger is a convenience
	})
}

func TestLedgerFiles(t *testing.T) {
	t.Run("contain numbers and ids only", func(t *testing.T) {
		e := makeLedgerEnv(t)
		ledger := NewLedger(t.Context(), e.env, nil)
		ledger.Record(makeRequest(func(r *shared.RequestRecord) { r.Ts = time.Now().UnixMilli() }))
		ledger.Flush(t.Context())
		text, err := os.ReadFile(filepath.Join(LedgerDir(t.Context(), e.env), DayKey(time.Now().UnixMilli())+".json"))
		if err != nil {
			t.Fatal(err)
		}
		value, err := jsfmt.DecodeValue(text)
		if err != nil {
			t.Fatalf("day file is not JSON: %v (%s)", err, text)
		}
		if version := fieldOf(t, value, "version"); version.Kind() != jsfmt.KindNumber ||
			version.EncodeJSON() != "1" {
			t.Errorf("version: got %s want 1", version.EncodeJSON())
		}
		rows := fieldOf(t, value, "rows")
		if rows.Kind() != jsfmt.KindObject {
			t.Fatalf("rows: got %d want an object", rows.Kind())
		}
		if row := fieldOf(t, rows, "req-1"); row.Kind() != jsfmt.KindArray {
			t.Errorf("rows.req-1: got %d want an array", row.Kind())
		}
	})

	t.Run("round-trip a request's pricing fields beside its tokens", func(t *testing.T) {
		e := makeLedgerEnv(t)
		ledger := NewLedger(t.Context(), e.env, nil)
		now := time.Now().UnixMilli()
		ledger.Record(makeRequest(func(r *shared.RequestRecord) {
			r.Ts = now
			r.Tokens = shared.Tokens{Input: 10, CacheWrite: 6}
			r.CacheWrite1h = i64(2)
			r.Speed = str("fast")
			r.Geo = str("us")
			r.ServiceTier = str("priority")
		}))
		ledger.Record(makeRequest(func(r *shared.RequestRecord) { r.ID, r.Ts = "req-2", now }))
		ledger.Flush(t.Context())
		rows := ledger.Rows(0)
		rowByID := func(id string) LedgerRow {
			t.Helper()
			for _, row := range rows {
				if row.ID == id {
					return row
				}
			}
			t.Fatalf("no %s row", id)
			return LedgerRow{}
		}
		priced := rowByID("req-1")
		if priced.Tokens.Input != 10 || priced.Tokens.CacheWrite != 6 {
			t.Errorf("tokens: got %+v want input 10 cacheWrite 6", priced.Tokens)
		}
		if priced.CacheWrite1h == nil || *priced.CacheWrite1h != 2 {
			t.Errorf("cacheWrite1h: got %v want 2", priced.CacheWrite1h)
		}
		if priced.Speed == nil || *priced.Speed != "fast" {
			t.Errorf("speed: got %v want fast", priced.Speed)
		}
		if priced.Geo == nil || *priced.Geo != "us" {
			t.Errorf("geo: got %v want us", priced.Geo)
		}
		if priced.ServiceTier == nil || *priced.ServiceTier != "priority" {
			t.Errorf("serviceTier: got %v want priority", priced.ServiceTier)
		}
		// a request that said nothing reads back with the fields absent, not null
		plain := rowByID("req-2")
		if plain.CacheWrite1h != nil {
			t.Errorf("plain cacheWrite1h: got %v want absent", *plain.CacheWrite1h)
		}
		if plain.Speed != nil {
			t.Errorf("plain speed: got %v want absent", *plain.Speed)
		}
	})

	t.Run("still read an old eight-slot row, pricing it from the model alone", func(t *testing.T) {
		e := makeLedgerEnv(t)
		now := time.Now().UnixMilli()
		writeText(t, filepath.Join(LedgerDir(t.Context(), e.env), DayKey(now)+".json"), fmt.Sprintf(
			`{"version":1,"rows":{"old-1":[%d,"claude-opus-5-5","s1","main",1000000,0,0,0]},"sessions":{},"agents":{}}`,
			now,
		))
		ledger := NewLedger(t.Context(), e.env, nil)
		rows := ledger.Rows(0)
		if len(rows) != 1 {
			t.Fatalf("rows: got %d want 1", len(rows))
		}
		row := rows[0]
		if row.ID != "old-1" || row.Model != "claude-opus-5-5" || row.Tokens.Input != 1_000_000 {
			t.Errorf("row: got %+v want old-1 on claude-opus-5-5 with 1M input", row)
		}
		if row.CacheWrite1h != nil {
			t.Errorf("cacheWrite1h: got %v want absent", *row.CacheWrite1h)
		}
		if row.Speed != nil {
			t.Errorf("speed: got %v want absent", *row.Speed)
		}
		// priced through the model-id fallback: opus-5-5 is 4/M input, and no condition
		// applies without a record
		cost := RequestCost(pricedOfRow(row))
		if cost == nil {
			t.Fatal("the old row: no price")
		}
		closeTo(t, "the old row", float64(cost.USD), 4)
	})
}

// TestLedgerDaysAreUTC files each request under its UTC date: half an hour either side of a
// UTC midnight lands on two different files whatever the machine's own zone is.
func TestLedgerDaysAreUTC(t *testing.T) {
	e := makeLedgerEnv(t)
	midnight := time.Date(2026, time.October, 16, 0, 0, 0, 0, time.UTC).UnixMilli()
	ledger := NewLedger(t.Context(), e.env, atMs(midnight+dayMs/2))
	ledger.Record(makeRequest(func(r *shared.RequestRecord) { r.ID, r.Ts = "before", midnight-30*60_000 }))
	ledger.Record(makeRequest(func(r *shared.RequestRecord) { r.ID, r.Ts = "after", midnight+30*60_000 }))
	ledger.Flush(t.Context())
	want := []string{"2026-10-15.json", "2026-10-16.json"}
	if got := ledgerJSONNames(t, LedgerDir(t.Context(), e.env)); !slices.Equal(got, want) {
		t.Errorf("day files: got %v want %v", got, want)
	}
}
