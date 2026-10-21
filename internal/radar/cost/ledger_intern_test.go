package cost

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// The ledger holds one row per request for 35 days, and a few models, a few thousand
// sessions and a handful of optional pricing words repeat across hundreds of thousands of
// rows. These tests pin that the rows share one copy of each repeated string instead of
// carrying the decoder's own copy per row.

// internDayText is one day file whose two rows repeat one model, one session, one agent and
// one speed, so a parse can only share them by intern.
func internDayText(t *testing.T, ts int64, ids ...string) string {
	t.Helper()
	rows := make([]string, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, fmt.Sprintf(
			`"%s":[%d,"claude-sonnet-5-5","550e8400-e29b-41d4-a716-000000000009","main",10,5,0,0,null,"fast","us","priority"]`,
			id, ts,
		))
	}
	return `{"version":1,"rows":{` + joinCommas(rows) + `},"sessions":{},"agents":{}}`
}

func joinCommas(parts []string) string {
	out := ""
	for i, part := range parts {
		if i > 0 {
			out += ","
		}
		out += part
	}
	return out
}

// internRow fetches one row by id or fails the test.
func internRow(t *testing.T, day *dayFile, id string) packed {
	t.Helper()
	row, ok := day.rows.get(id)
	if !ok {
		t.Fatalf("no %s row in the parsed day", id)
	}
	return row
}

func TestLedgerInternsRepeatedStrings(t *testing.T) {
	ts := time.Now().UnixMilli()

	t.Run("rows of two parsed days share one model, session and agent copy", func(t *testing.T) {
		day1 := parseDay(internDayText(t, ts, "req-1", "req-2"))
		day2 := parseDay(internDayText(t, ts, "req-1", "req-3"))
		a := internRow(t, day1, "req-1")
		b := internRow(t, day2, "req-1")
		if unsafe.StringData(a.Model) != unsafe.StringData(b.Model) {
			t.Error("the two days' rows carry separate copies of the model")
		}
		if unsafe.StringData(a.SessionID) != unsafe.StringData(b.SessionID) {
			t.Error("the two days' rows carry separate copies of the session id")
		}
		if unsafe.StringData(a.AgentID) != unsafe.StringData(b.AgentID) {
			t.Error("the two days' rows carry separate copies of the agent id")
		}
		// request ids are unique per row: they only have to survive intact
		if internRow(t, day2, "req-3").slots != 12 {
			t.Error("the parsed row lost its slots")
		}
	})

	t.Run("two rows of one day with the same speed hold the same pointer", func(t *testing.T) {
		day := parseDay(internDayText(t, ts, "req-1", "req-2"))
		a := internRow(t, day, "req-1")
		b := internRow(t, day, "req-2")
		if a.Speed != b.Speed {
			t.Error("two rows with speed fast hold separate speed pointers")
		}
		if a.Geo != b.Geo {
			t.Error("two rows with geo us hold separate geo pointers")
		}
		if a.ServiceTier != b.ServiceTier {
			t.Error("two rows with tier priority hold separate tier pointers")
		}
		if *a.Speed != "fast" || *a.Geo != "us" || *a.ServiceTier != "priority" {
			t.Errorf("speed/geo/tier read back %q/%q/%q", *a.Speed, *a.Geo, *a.ServiceTier)
		}
	})

	t.Run("a row recorded through Record shares storage with a parsed row", func(t *testing.T) {
		e := makeLedgerEnv(t)
		now := time.Now().UnixMilli()
		ledger := NewLedger(t.Context(), e.env, atMs(now))
		ledger.Record(makeRequest(func(r *shared.RequestRecord) {
			r.ID = "rec-1"
			r.Ts = now
			r.Model = "claude-sonnet-5-5"
			r.SessionID = "550e8400-e29b-41d4-a716-000000000009"
			r.AgentID = "main"
			r.Speed = str("fast")
		}))
		rows := ledger.Rows(0)
		if len(rows) != 1 {
			t.Fatalf("rows: got %d want 1", len(rows))
		}
		parsed := internRow(t, parseDay(internDayText(t, ts, "req-1")), "req-1")
		if unsafe.StringData(rows[0].SessionID) != unsafe.StringData(parsed.SessionID) {
			t.Error("the recorded row carries its own copy of the session id")
		}
		if unsafe.StringData(rows[0].Model) != unsafe.StringData(parsed.Model) {
			t.Error("the recorded row carries its own copy of the model")
		}
	})
}

// TestLedgerDayRewritesByteForByte pins the file format across a load: a day read back,
// re-encoded and written must be byte-for-byte the file it was read from, interning or not.
func TestLedgerDayRewritesByteForByte(t *testing.T) {
	e := makeLedgerEnv(t)
	now := time.Now().UnixMilli()
	path := filepath.Join(LedgerDir(t.Context(), e.env), DayKey(now)+".json")
	want := `{"version":1,"rows":{` +
		`"req-1":[` + fmt.Sprint(now) + `,"claude-sonnet-5-5","550e8400-e29b-41d4-a716-000000000009","main",10,5,0,0,null,"fast","us","priority"],` +
		`"req-2":[` + fmt.Sprint(now-1000) + `,"kimi/kimi-k3","550e8400-e29b-41d4-a716-000000000002","build",10,5,0,0],` +
		`"req-3":[` + fmt.Sprint(now-2000) + `,"zai/glm-5.3","550e8400-e29b-41d4-a716-000000000003","main",7,3,0,4,2,null,null,null]` +
		`},"sessions":{"550e8400-e29b-41d4-a716-000000000009":{"project":"proj-a","title":"fix it","repo":"/w/repo","parentSessionId":null,"name":null}},"agents":{}}`
	writeText(t, path, want)
	ledger := NewLedger(t.Context(), e.env, atMs(now))
	entry, ok := ledger.days.get(DayKey(now))
	if !ok {
		t.Fatal("the loaded day is missing")
	}
	if got := entry.rows.len(); got != 3 {
		t.Fatalf("rows: got %d want 3", got)
	}
	writeText(t, path, string(encodeDay(entry)))
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("the day file rewrote as\n%s\nwant\n%s", got, want)
	}
}
