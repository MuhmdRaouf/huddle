package ingest_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/ingest"
)

// stateRoot is a provider's state dir under the test home.
func stateRoot(home string, plugin ingest.JobPlugin) string {
	return filepath.Join(home, string(plugin))
}

// writeJob makes a job dir: a job.json (empty text means none) plus any number of attempt
// files.
func writeJob(t *testing.T, stateDir, id, jobJSON string, attempts ...string) string {
	t.Helper()
	dir := filepath.Join(stateDir, "jobs", id)
	if jobJSON == "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	} else {
		writeText(t, filepath.Join(dir, "job.json"), jobJSON)
	}
	for _, name := range attempts {
		writeText(t, filepath.Join(dir, name), "")
	}
	return dir
}

// makeJobEnv is an env whose every provider state dir points into one throwaway home.
func makeJobEnv(t *testing.T) (domain.EnvLookup, string) {
	t.Helper()
	e := makeEnv(t)
	for _, plugin := range ingest.JobPlugins {
		e.env[strings.ToUpper(string(plugin))+"_STATE_DIR"] = filepath.Join(e.home, string(plugin))
	}
	return e.env, e.home
}

func TestPluginStateDirs(t *testing.T) {
	t.Run("reads each provider's jobs from its own state dir", func(t *testing.T) {
		env, home := makeJobEnv(t)
		writeJob(t, stateRoot(home, ingest.Zai), "z1", `{}`)
		writeJob(t, stateRoot(home, ingest.Kimi), "k1", `{}`)
		writeJob(t, stateRoot(home, ingest.DeepSeek), "d1", `{}`)
		writeJob(t, stateRoot(home, ingest.MiniMax), "m1", `{}`)
		writeJob(t, stateRoot(home, ingest.Qwen), "q1", `{}`)
		var sessions []string
		for _, job := range ingest.ReadJobs(t.Context(), env, nil) {
			sessions = append(sessions, job.SessionID)
		}
		want := []string{"zai:z1", "kimi:k1", "deepseek:d1", "minimax:m1", "qwen:q1"}
		if !reflect.DeepEqual(sessions, want) {
			t.Errorf("session ids = %v, want %v", sessions, want)
		}
	})

	t.Run("honours a per-provider <NAME>_STATE_DIR override", func(t *testing.T) {
		env, home := makeJobEnv(t)
		writeJob(t, stateRoot(home, ingest.Kimi), "elsewhere", `{}`)
		env["KIMI_STATE_DIR"] = filepath.Join(home, "kimi-override")
		writeJob(t, filepath.Join(home, "kimi-override"), "overridden", `{}`)
		jobs := ingest.ReadJobs(t.Context(), env, nil)
		if len(jobs) != 1 || jobs[0].ID != "overridden" {
			t.Fatalf("jobs = %+v, want only [overridden]", jobs)
		}
	})

	t.Run("falls back to ~/.agents/<name> without an override", func(t *testing.T) {
		env, home := makeJobEnv(t)
		for _, plugin := range ingest.JobPlugins {
			delete(env, strings.ToUpper(string(plugin))+"_STATE_DIR")
		}
		writeJob(t, filepath.Join(home, ".agents", "deepseek"), "d1", `{}`)
		jobs := ingest.ReadJobs(t.Context(), env, nil)
		if len(jobs) != 1 || jobs[0].ID != "d1" {
			t.Fatalf("jobs = %+v, want only [d1]", jobs)
		}
		if got := ingest.JobClaudeHome(t.Context(), env, ingest.Qwen); got != filepath.Join(home, ".agents", "qwen", "claude-home") {
			t.Errorf("JobClaudeHome = %q", got)
		}
	})

	t.Run("silently skips providers with no jobs dir", func(t *testing.T) {
		env, home := makeJobEnv(t)
		writeJob(t, stateRoot(home, ingest.MiniMax), "m1", `{}`)
		jobs := ingest.ReadJobs(t.Context(), env, nil)
		if len(jobs) != 1 || jobs[0].SessionID != "minimax:m1" {
			t.Fatalf("jobs = %+v, want only [minimax:m1]", jobs)
		}
	})
}

func TestReadJobs(t *testing.T) {
	t.Run("returns nothing when no provider has a jobs dir", func(t *testing.T) {
		env, _ := makeJobEnv(t)
		if jobs := ingest.ReadJobs(t.Context(), env, nil); len(jobs) != 0 {
			t.Errorf("ReadJobs = %+v, want none", jobs)
		}
	})

	t.Run("skips dirs without a readable job.json and keeps the rest", func(t *testing.T) {
		env, home := makeJobEnv(t)
		writeJob(t, stateRoot(home, ingest.Zai), "empty-dir", "")
		writeJob(t, stateRoot(home, ingest.Zai), "junk", "{oops")
		writeJob(t, stateRoot(home, ingest.Zai), "array", "[1]")
		writeJob(t, stateRoot(home, ingest.Zai), "good", `{"id":"good"}`)
		jobs := ingest.ReadJobs(t.Context(), env, nil)
		if len(jobs) != 1 || jobs[0].ID != "good" {
			t.Fatalf("jobs = %+v, want only [good]", jobs)
		}
	})

	t.Run("maps a full job: plugin-qualified session and model, numeric attempt order", func(t *testing.T) {
		env, home := makeJobEnv(t)
		dir := writeJob(t, stateRoot(home, ingest.Kimi), "j1",
			`{"id":"j1","brief":{"title":"Fix the flaky test","model":"kimi-k2.5"},`+
				`"state":"running","updatedAt":"2030-01-01T00:00:00Z"}`,
			"attempt-2.jsonl", "attempt-10.jsonl", "attempt-1.jsonl", "notes.txt")
		jobs := ingest.ReadJobs(t.Context(), env, nil)
		want := []ingest.PluginJob{{
			ID:        "j1",
			Plugin:    ingest.Kimi,
			SessionID: "kimi:j1",
			Title:     ptr("Fix the flaky test"),
			State:     ptr("running"),
			Live:      true,
			Model:     ptr("kimi:kimi-k2.5"),
			UpdatedAt: ptr(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()), // newer than any attempt mtime, so it stands
			Dir:       dir,
			Attempts: []ingest.JobAttempt{
				{File: filepath.Join(dir, "attempt-1.jsonl"), N: 1},
				{File: filepath.Join(dir, "attempt-2.jsonl"), N: 2},
				{File: filepath.Join(dir, "attempt-10.jsonl"), N: 10},
			},
			Upstream: "",
		}}
		if !reflect.DeepEqual(jobs, want) {
			t.Errorf("jobs = %+v,\nwant %+v", jobs, want)
		}
	})

	t.Run("reads workspace, origin and attempt session ids, and survives a missing origin", func(t *testing.T) {
		env, home := makeJobEnv(t)
		writeJob(t, stateRoot(home, ingest.Zai), "full",
			`{"workspace":{"repoRoot":"/w/main","branch":"zai/full"},`+
				`"origin":{"sessionId":"018c3a2b-7c8d-7cde-9f01-2a3b4c5d6e7f"},`+
				`"attempts":[{"n":1,"sessionId":"att-1"},{"n":2,"sessionId":"att-2"},{"n":3}]}`,
			"attempt-1.jsonl", "attempt-2.jsonl", "attempt-3.jsonl")
		writeJob(t, stateRoot(home, ingest.Zai), "bare", `{"brief":"not-an-object","state":42}`)
		jobs := ingest.ReadJobs(t.Context(), env, nil)
		if len(jobs) != 2 {
			t.Fatalf("jobs = %d, want 2", len(jobs))
		}
		full, bare := jobs[0], jobs[1]
		if full.SessionID != "zai:full" || *full.RepoRoot != "/w/main" || *full.Branch != "zai/full" ||
			*full.ParentSessionID != "018c3a2b-7c8d-7cde-9f01-2a3b4c5d6e7f" {
			t.Errorf("full job = %+v", full)
		}
		wantSessions := []*string{ptr("att-1"), ptr("att-2"), nil}
		for i, attempt := range full.Attempts {
			if !reflect.DeepEqual(attempt.SessionID, wantSessions[i]) {
				t.Errorf("attempt %d session id = %v, want %v", attempt.N, attempt.SessionID, wantSessions[i])
			}
		}
		if bare.RepoRoot != nil || bare.Branch != nil || bare.ParentSessionID != nil || bare.State != nil {
			t.Errorf("bare job = %+v, want no workspace, origin or state", bare)
		}
		if bare.Live {
			t.Error("bare job is live, want finished: no state field, no running job")
		}
	})

	t.Run("keeps zai's own upstream and takes the others from what radar knows", func(t *testing.T) {
		env, home := makeJobEnv(t)
		writeJob(t, stateRoot(home, ingest.Zai), "z1", `{}`)
		writeJob(t, stateRoot(home, ingest.Kimi), "k1", `{}`)
		writeJob(t, stateRoot(home, ingest.Qwen), "q1", `{}`)
		jobs := ingest.ReadJobs(t.Context(), env, func(plugin ingest.JobPlugin) string {
			if plugin == ingest.Kimi {
				return "https://api.moonshot.ai/anthropic"
			}
			return ""
		})
		upstreamOf := map[string]string{}
		for _, job := range jobs {
			upstreamOf[job.ID] = job.Upstream
		}
		if upstreamOf["z1"] != ingest.ZaiJobUpstream {
			t.Errorf("z1 upstream = %q, want %q", upstreamOf["z1"], ingest.ZaiJobUpstream)
		}
		if upstreamOf["k1"] != "https://api.moonshot.ai/anthropic" {
			t.Errorf("k1 upstream = %q", upstreamOf["k1"])
		}
		if upstreamOf["q1"] != "" {
			t.Errorf("q1 upstream = %q, want empty", upstreamOf["q1"])
		}
	})

	t.Run("keeps a job live only while the engine is still working it", func(t *testing.T) {
		env, home := makeJobEnv(t)
		cases := []struct {
			dir   string
			state *string
			live  bool
		}{
			{"t-queued", ptr("queued"), true},
			{"t-running", ptr("running"), true},
			{"t-verifying", ptr("verifying"), true},
			{"t-awaiting-review", ptr("awaiting_review"), false},
			{"t-accepted", ptr("accepted"), false},
			{"t-discarded", ptr("discarded"), false},
			{"t-failed", ptr("failed"), false},
			{"t-unknown", ptr("some-new-state"), false},
			{"t-none", nil, false},
		}
		for _, tc := range cases {
			job := map[string]any{}
			if tc.state != nil {
				job["state"] = *tc.state
			}
			text, err := json.Marshal(job)
			if err != nil {
				t.Fatal(err)
			}
			writeJob(t, stateRoot(home, ingest.Zai), tc.dir, string(text))
		}
		jobs := ingest.ReadJobs(t.Context(), env, nil)
		liveOf := map[string]bool{}
		for _, job := range jobs {
			liveOf[job.ID] = job.Live
		}
		for _, tc := range cases {
			if liveOf[tc.dir] != tc.live {
				t.Errorf("%s live = %v, want %v", tc.dir, liveOf[tc.dir], tc.live)
			}
		}
	})

	t.Run("takes the freshest attempt mtime over a stale updatedAt, ignoring broken symlinks", func(t *testing.T) {
		env, home := makeJobEnv(t)
		dir := writeJob(t, stateRoot(home, ingest.Zai), "live",
			`{"updatedAt":"2020-01-01T00:00:00Z"}`, "attempt-1.jsonl")
		if err := os.Symlink(filepath.Join(dir, "attempt-none.jsonl"), filepath.Join(dir, "attempt-9.jsonl")); err != nil {
			t.Fatalf("symlink a dangling attempt: %v", err) // dangling on purpose
		}
		jobs := ingest.ReadJobs(t.Context(), env, nil)
		if len(jobs) != 1 {
			t.Fatalf("jobs = %d, want 1", len(jobs))
		}
		job := jobs[0]
		wantFiles := []string{filepath.Join(dir, "attempt-1.jsonl"), filepath.Join(dir, "attempt-9.jsonl")}
		var gotFiles []string
		for _, attempt := range job.Attempts {
			gotFiles = append(gotFiles, attempt.File)
		}
		if !reflect.DeepEqual(gotFiles, wantFiles) {
			t.Errorf("attempt files = %v, want %v", gotFiles, wantFiles)
		}
		if job.UpdatedAt == nil || *job.UpdatedAt == time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli() {
			t.Fatalf("updatedAt = %v, want the attempt's mtime over the stale recorded one", job.UpdatedAt)
		}
		if *job.UpdatedAt < time.Now().UnixMilli()-60_000 { // the attempt we just touched
			t.Errorf("updatedAt = %d, want within the last minute", *job.UpdatedAt)
		}
	})

	t.Run("orders jobs newest-first, updatedAt-less jobs last", func(t *testing.T) {
		env, home := makeJobEnv(t)
		writeJob(t, stateRoot(home, ingest.Zai), "b", `{"updatedAt":"2030-01-02T00:00:00Z"}`)
		writeJob(t, stateRoot(home, ingest.Zai), "c", `{}`)
		writeJob(t, stateRoot(home, ingest.Zai), "a", `{"updatedAt":"2030-01-03T00:00:00Z"}`)
		var ids []string
		for _, job := range ingest.ReadJobs(t.Context(), env, nil) {
			ids = append(ids, job.ID)
		}
		if !reflect.DeepEqual(ids, []string{"a", "b", "c"}) {
			t.Errorf("job ids = %v, want [a b c]", ids)
		}
	})
}

func TestAttemptTranscript(t *testing.T) {
	t.Run("finds an attempt's transcript under any project dir and lists its subagents", func(t *testing.T) {
		env, _ := makeJobEnv(t)
		claudeHome := ingest.JobClaudeHome(t.Context(), env, ingest.Kimi)
		transcript := writeText(t, filepath.Join(claudeHome, "projects", "-w-main", "att-1.jsonl"),
			"{\"type\":\"user\",\"timestamp\":\"2026-01-01T00:00:00.000Z\"}\n")
		subagent := writeText(t, filepath.Join(claudeHome, "projects", "-w-main", "att-1", "subagents", "agent-x.jsonl"), "")
		got := ingest.FindAttemptTranscript(claudeHome, "att-1")
		if got == nil {
			t.Fatal("AttemptTranscript = nil, want the transcript and its subagents")
		}
		if got.File != transcript {
			t.Errorf("file = %q, want %q", got.File, transcript)
		}
		if len(got.Subagents) != 1 || got.Subagents[0] != subagent {
			t.Errorf("subagents = %v, want [%s]", got.Subagents, subagent)
		}
	})

	t.Run("returns null when no project dir holds the session, or the session has none", func(t *testing.T) {
		env, home := makeJobEnv(t)
		claudeHome := ingest.JobClaudeHome(t.Context(), env, ingest.Kimi)
		writeText(t, filepath.Join(claudeHome, "projects", "-w-main", "other.jsonl"), "")
		if got := ingest.FindAttemptTranscript(claudeHome, "att-9"); got != nil {
			t.Errorf("AttemptTranscript(other session) = %+v, want nil", got)
		}
		if got := ingest.FindAttemptTranscript(filepath.Join(home, "nope"), "att-9"); got != nil {
			t.Errorf("AttemptTranscript(missing home) = %+v, want nil", got)
		}
	})
}
