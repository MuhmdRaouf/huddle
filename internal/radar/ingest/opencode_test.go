package ingest_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/history"
	"github.com/MuhmdRaouf/huddle/internal/radar/ingest"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// The opencode collector: a temp database holding the real opencode schema reads as request
// records, one per assistant message that carried tokens, exact to the token and to the
// harness-reported cost; the watermark keeps the next poll from reading any of it again, and
// a new message brings only itself. The tables follow opencode v1.17.7's
// packages/core/src/session/sql.ts — session(id, …, directory, …, time_created, …) and
// message(id, session_id, time_created, time_updated, data) — and the assistant message's
// data shape its packages/core/src/v1/session.ts SessionV1.Info: tokens
// {input, output, reasoning, cache:{read, write}} and cost.

// opencodeSessionID and opencodeBaseAt anchor the fixture: session ses_a1 in /w/app, its
// first message at 2026-10-16T10:00:00Z.
var (
	opencodeSessionID = "ses_a1"
	opencodeBaseAt    = time.Date(2026, time.October, 16, 10, 0, 0, 0, time.UTC).UnixMilli()
)

// opencodeAssistant is one assistant message's data JSON, the way opencode stores it.
func opencodeAssistant(modelID, providerID string, cost float64, tokens string, at int64) string {
	return fmt.Sprintf(`{"id":"msg_x","role":"assistant","parentID":"msg_000","modelID":%q,"providerID":%q,`+
		`"mode":"build","agent":"build","path":{"cwd":"/w/app","root":"/w/app"},"cost":%v,`+
		`"tokens":%s,"time":{"created":%d,"completed":%d}}`,
		modelID, providerID, cost, tokens, at, at+1_000)
}

// opencodeFixture builds a temp database with the real opencode tables and answers the env
// naming it.
func opencodeFixture(t *testing.T) domain.EnvLookup {
	t.Helper()
	file := filepath.Join(t.TempDir(), "opencode", "opencode.db")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(file), err)
	}
	db, err := history.OpenDB(file)
	if err != nil {
		t.Fatalf("open the fixture database: %v", err)
	}
	const sessionDDL = "CREATE TABLE IF NOT EXISTS `session` (" +
		"`id` text PRIMARY KEY NOT NULL," +
		"`project_id` text NOT NULL," +
		"`workspace_id` text," +
		"`parent_id` text," +
		"`slug` text NOT NULL," +
		"`directory` text NOT NULL," +
		"`path` text," +
		"`title` text NOT NULL," +
		"`version` text NOT NULL," +
		"`share_url` text," +
		"`summary_additions` integer," +
		"`summary_deletions` integer," +
		"`summary_files` integer," +
		"`summary_diffs` text," +
		"`metadata` text," +
		"`cost` real DEFAULT 0 NOT NULL," +
		"`tokens_input` integer DEFAULT 0 NOT NULL," +
		"`tokens_output` integer DEFAULT 0 NOT NULL," +
		"`tokens_reasoning` integer DEFAULT 0 NOT NULL," +
		"`tokens_cache_read` integer DEFAULT 0 NOT NULL," +
		"`tokens_cache_write` integer DEFAULT 0 NOT NULL," +
		"`revert` text," +
		"`permission` text," +
		"`agent` text," +
		"`model` text," +
		"`time_created` integer NOT NULL," +
		"`time_updated` integer NOT NULL," +
		"`time_compacting` integer," +
		"`time_archived` integer)"
	const messageDDL = "CREATE TABLE IF NOT EXISTS `message` (" +
		"`id` text PRIMARY KEY NOT NULL," +
		"`session_id` text NOT NULL REFERENCES `session` (`id`) ON DELETE CASCADE," +
		"`time_created` integer NOT NULL," +
		"`time_updated` integer NOT NULL," +
		"`data` text NOT NULL)"
	if err := db.Exec(sessionDDL + ";" + messageDDL); err != nil {
		t.Fatalf("create the opencode tables: %v", err)
	}
	if _, err := db.Run(
		"INSERT INTO session (id, project_id, slug, directory, title, version, time_created, time_updated) "+
			"VALUES (?, 'prj_1', 'fix-the-login-flow', '/w/app', 'Fix the login flow', '1.17.7', ?, ?)",
		opencodeSessionID, opencodeBaseAt-60_000, opencodeBaseAt-60_000); err != nil {
		t.Fatalf("insert the session: %v", err)
	}
	message := func(id string, at int64, data string) {
		t.Helper()
		if _, err := db.Run(
			"INSERT INTO message (id, session_id, time_created, time_updated, data) VALUES (?, ?, ?, ?, ?)",
			id, opencodeSessionID, at, at+1_000, data); err != nil {
			t.Fatalf("insert the message %s: %v", id, err)
		}
	}
	message("msg_aaa", opencodeBaseAt, opencodeAssistant("claude-sonnet-5-5", "anthropic", 0.0123,
		`{"input":900,"output":400,"reasoning":50,"cache":{"read":300,"write":40}}`, opencodeBaseAt))
	message("msg_bbb", opencodeBaseAt+120_000, opencodeAssistant("glm-5.3", "zai", 0.0042,
		`{"input":500,"output":200,"reasoning":0,"cache":{"read":100,"write":0}}`, opencodeBaseAt+120_000))
	// a message the model never answered: no usage to report, so no record
	message("msg_zero", opencodeBaseAt+240_000, opencodeAssistant("glm-5.3", "zai", 0,
		`{"input":0,"output":0,"reasoning":0,"cache":{"read":0,"write":0}}`, opencodeBaseAt+240_000))
	if err := db.Close(); err != nil {
		t.Fatalf("close the fixture database: %v", err)
	}
	return domain.EnvLookup{"HOME": t.TempDir(), "OPENCODE_DATA": file}
}

// opencodeRecord is one expected request record, edited in place by edit.
func opencodeRecord(id string, edit func(*shared.RequestRecord)) shared.RequestRecord {
	record := shared.RequestRecord{
		ID:        "opencode:" + id,
		SessionID: opencodeSessionID,
		AgentID:   "main",
		Harness:   shared.HarnessOpencode,
		Cwd:       ptr("/w/app"),
	}
	if edit != nil {
		edit(&record)
	}
	return record
}

func TestOpencodeCollector(t *testing.T) {
	t.Run("reads the fixture database as two records, exact to the token and the cent", func(t *testing.T) {
		env := opencodeFixture(t)
		records, err := ingest.NewOpencodeCollector(env).Collect(context.Background(), 0)
		if err != nil {
			t.Fatalf("collect: %v", err)
		}
		want := []shared.RequestRecord{
			opencodeRecord("msg_aaa", func(r *shared.RequestRecord) {
				r.Model = "claude-sonnet-5-5"
				r.Provider = "Anthropic"
				r.Ts = opencodeBaseAt
				r.Tokens = shared.Tokens{Input: 600, Output: 400, CacheRead: 300, CacheWrite: 40, Thinking: ptr(int64(50))}
				r.CostUSD = ptr(0.0123)
			}),
			opencodeRecord("msg_bbb", func(r *shared.RequestRecord) {
				r.Model = "glm-5.3"
				r.Provider = "Z.ai"
				r.Ts = opencodeBaseAt + 120_000
				r.Tokens = shared.Tokens{Input: 400, Output: 200, CacheRead: 100}
				r.CostUSD = ptr(0.0042)
			}),
		}
		if !reflect.DeepEqual(records, want) {
			t.Errorf("records = %+v, want %+v", records, want)
		}
	})

	t.Run("reads nothing new on the next poll and only the new message after it", func(t *testing.T) {
		env := opencodeFixture(t)
		collector := ingest.NewOpencodeCollector(env)
		if _, err := collector.Collect(context.Background(), 0); err != nil {
			t.Fatalf("first collect: %v", err)
		}
		again, err := collector.Collect(context.Background(), 0)
		if err != nil {
			t.Fatalf("second collect: %v", err)
		}
		if len(again) != 0 {
			t.Fatalf("second collect read %d records again, want none", len(again))
		}
		db, err := history.OpenDB(env["OPENCODE_DATA"])
		if err != nil {
			t.Fatalf("reopen the fixture: %v", err)
		}
		at := opencodeBaseAt + 360_000
		if _, err := db.Run(
			"INSERT INTO message (id, session_id, time_created, time_updated, data) VALUES ('msg_ccc', ?, ?, ?, ?)",
			opencodeSessionID, at, at+1_000, opencodeAssistant("glm-5.3", "zai", 0.0099,
				`{"input":300,"output":100,"reasoning":10,"cache":{"read":50,"write":0}}`, at)); err != nil {
			t.Fatalf("insert the new message: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close the fixture: %v", err)
		}
		after, err := collector.Collect(context.Background(), 0)
		if err != nil {
			t.Fatalf("third collect: %v", err)
		}
		want := []shared.RequestRecord{
			opencodeRecord("msg_ccc", func(r *shared.RequestRecord) {
				r.Model = "glm-5.3"
				r.Provider = "Z.ai"
				r.Ts = at
				r.Tokens = shared.Tokens{Input: 250, Output: 100, CacheRead: 50, Thinking: ptr(int64(10))}
				r.CostUSD = ptr(0.0099)
			}),
		}
		if !reflect.DeepEqual(after, want) {
			t.Errorf("records = %+v, want %+v", after, want)
		}
	})

	t.Run("counts a reply read mid-stream once it completes", func(t *testing.T) {
		env := opencodeFixture(t)
		collector := ingest.NewOpencodeCollector(env)
		if _, err := collector.Collect(context.Background(), 0); err != nil {
			t.Fatalf("first collect: %v", err)
		}
		db, err := history.OpenDB(env["OPENCODE_DATA"])
		if err != nil {
			t.Fatalf("reopen the fixture: %v", err)
		}
		at := opencodeBaseAt + 480_000
		streaming := fmt.Sprintf(`{"id":"msg_x","role":"assistant","modelID":"glm-5.3","providerID":"zai",`+
			`"cost":0,"tokens":{"input":0,"output":0,"reasoning":0,"cache":{"read":0,"write":0}},`+
			`"time":{"created":%d}}`, at)
		if _, err := db.Run(
			"INSERT INTO message (id, session_id, time_created, time_updated, data) VALUES ('msg_ddd', ?, ?, ?, ?)",
			opencodeSessionID, at, at, streaming); err != nil {
			t.Fatalf("insert the streaming message: %v", err)
		}
		mid, err := collector.Collect(context.Background(), 0)
		if err != nil {
			t.Fatalf("mid-stream collect: %v", err)
		}
		if len(mid) != 0 {
			t.Fatalf("mid-stream collect read %d records, want none", len(mid))
		}
		if _, err := db.Run("UPDATE message SET time_updated = ?, data = ? WHERE id = 'msg_ddd'",
			at+90_000, opencodeAssistant("glm-5.3", "zai", 0.002,
				`{"input":400,"output":80,"reasoning":0,"cache":{"read":0,"write":0}}`, at)); err != nil {
			t.Fatalf("complete the message: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close the fixture: %v", err)
		}
		done, err := collector.Collect(context.Background(), 0)
		if err != nil {
			t.Fatalf("completed collect: %v", err)
		}
		if len(done) != 1 || done[0].ID != "opencode:msg_ddd" || done[0].Tokens.Input != 400 {
			t.Fatalf("records = %+v, want msg_ddd with 400 input tokens", done)
		}
	})

	t.Run("answers no records and no error without a database", func(t *testing.T) {
		records, err := ingest.NewOpencodeCollector(domain.EnvLookup{
			"HOME": t.TempDir(), "OPENCODE_DATA": filepath.Join(t.TempDir(), "nowhere.db"),
		}).Collect(context.Background(), 0)
		if err != nil {
			t.Fatalf("collect: %v", err)
		}
		if len(records) != 0 {
			t.Errorf("records = %d, want none", len(records))
		}
	})
}
