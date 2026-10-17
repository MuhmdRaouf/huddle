package ingest_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/ingest"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// makeRegistryEnv builds a temp Claude config dir with an empty sessions dir inside.
func makeRegistryEnv(t *testing.T) (config string, dir string) {
	t.Helper()
	config = t.TempDir()
	dir = filepath.Join(config, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	return config, dir
}

// writeRegistryFile drops one file into the registry dir.
func writeRegistryFile(t *testing.T, dir, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// sessionText is one registry file's content: the base session with over's fields replaced.
func sessionText(over map[string]any) string {
	session := map[string]any{"pid": 81_812, "sessionId": "94d64de0", "status": "busy"}
	for key, value := range over {
		session[key] = value
	}
	text, err := json.Marshal(session)
	if err != nil {
		panic(err)
	}
	return string(text)
}

func TestRegistryDir(t *testing.T) {
	t.Run("sits beside projects/ in the same Claude config dir", func(t *testing.T) {
		env := domain.EnvLookup{"CLAUDE_CONFIG_DIR": "/cfg", "HOME": "/home/u"}
		if got := ingest.RegistryDir(env); got != "/cfg/sessions" {
			t.Errorf("RegistryDir(CLAUDE_CONFIG_DIR) = %q, want %q", got, "/cfg/sessions")
		}
		if got := ingest.RegistryDir(domain.EnvLookup{"HOME": "/home/u"}); got != "/home/u/.claude/sessions" {
			t.Errorf("RegistryDir(HOME) = %q, want %q", got, "/home/u/.claude/sessions")
		}
	})
}

func TestRegistrySessionOf(t *testing.T) {
	t.Run("reads the fields radar names sessions and states from", func(t *testing.T) {
		got, ok := ingest.RegistrySessionOf(sessionText(map[string]any{
			"name": "ProxBeam", "nameSource": "user", "cwd": "/w/app", "startedAt": 1_000,
		}))
		if !ok {
			t.Fatal("RegistrySessionOf(full) rejected the session")
		}
		want := shared.RegistrySession{
			PID: 81_812, SessionID: "94d64de0",
			Name: ptr("ProxBeam"), NameSource: ptr("user"), Status: ptr("busy"),
			Cwd: ptr("/w/app"), StartedAt: ptr(int64(1_000)),
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("RegistrySessionOf(full) = %+v, want %+v", got, want)
		}

		got, ok = ingest.RegistrySessionOf(sessionText(nil))
		if !ok {
			t.Fatal("RegistrySessionOf(base) rejected the session")
		}
		want = shared.RegistrySession{
			PID: 81_812, SessionID: "94d64de0", Status: ptr("busy"),
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("RegistrySessionOf(base) = %+v, want %+v", got, want)
		}

		got, _ = ingest.RegistrySessionOf(sessionText(map[string]any{"startedAt": "not-a-number"}))
		if got.StartedAt != nil {
			t.Errorf("startedAt = %v, want nil", *got.StartedAt)
		}
	})

	t.Run("refuses anything without a session id and a positive whole pid", func(t *testing.T) {
		for name, text := range map[string]string{
			"broken json":   "not json",
			"an array":      "[1,2]",
			"a scalar":      "42",
			"no pid":        `{"sessionId":"x"}`,
			"no session id": `{"pid":7}`,
			"pid zero":      `{"sessionId":"x","pid":0}`,
			"pid negative":  `{"sessionId":"x","pid":-3}`,
			"pid fraction":  `{"sessionId":"x","pid":1.5}`,
		} {
			if _, ok := ingest.RegistrySessionOf(text); ok {
				t.Errorf("RegistrySessionOf(%s) accepted the file", name)
			}
		}
	})
}

func TestReadRegistry(t *testing.T) {
	t.Run("lists the live sessions, dropping entries whose pid is gone", func(t *testing.T) {
		_, dir := makeRegistryEnv(t)
		writeRegistryFile(t, dir, "81812.json", sessionText(map[string]any{"sessionId": "live-one"}))
		writeRegistryFile(t, dir, "81813.json", sessionText(map[string]any{"pid": 81_813, "sessionId": "live-two", "status": "idle"}))
		writeRegistryFile(t, dir, "81814.json", sessionText(map[string]any{"pid": 81_814, "sessionId": "dead-one"}))
		entries := ingest.ReadRegistry(
			domain.EnvLookup{"CLAUDE_CONFIG_DIR": filepath.Dir(dir)},
			func(pid int64) bool { return pid != 81_814 },
		)
		var ids []string
		for _, entry := range entries {
			ids = append(ids, entry.SessionID)
		}
		if len(ids) != 2 || ids[0] != "live-one" || ids[1] != "live-two" {
			t.Fatalf("session ids = %v, want [live-one live-two]", ids)
		}
		if entries[1].Status == nil || *entries[1].Status != "idle" {
			t.Errorf("second entry status = %v, want idle", entries[1].Status)
		}
	})

	t.Run("never reads the .key files beside the session files, nor anything but .json", func(t *testing.T) {
		_, dir := makeRegistryEnv(t)
		writeRegistryFile(t, dir, "81812.key", sessionText(map[string]any{"pid": 42, "sessionId": "from-the-key"}))
		writeRegistryFile(t, dir, "81812.jsonl", sessionText(map[string]any{"pid": 43, "sessionId": "from-the-jsonl"}))
		writeRegistryFile(t, dir, "notes.txt", sessionText(map[string]any{"pid": 44, "sessionId": "from-the-notes"}))
		var asked []int64
		entries := ingest.ReadRegistry(
			domain.EnvLookup{"CLAUDE_CONFIG_DIR": filepath.Dir(dir)},
			func(pid int64) bool {
				asked = append(asked, pid)
				return true
			},
		)
		if len(entries) != 0 {
			t.Errorf("entries = %+v, want none", entries)
		}
		if len(asked) != 0 {
			t.Errorf("asked = %v, want none: no secret-bearing file was even opened for parsing", asked)
		}
	})

	t.Run("skips a malformed or empty file and carries on with the rest", func(t *testing.T) {
		_, dir := makeRegistryEnv(t)
		writeRegistryFile(t, dir, "a.json", "{oops")
		writeRegistryFile(t, dir, "b.json", "")
		writeRegistryFile(t, dir, "c.json", sessionText(map[string]any{"sessionId": "good"}))
		entries := ingest.ReadRegistry(domain.EnvLookup{"CLAUDE_CONFIG_DIR": filepath.Dir(dir)}, func(int64) bool { return true })
		if len(entries) != 1 || entries[0].SessionID != "good" {
			t.Fatalf("entries = %+v, want only [good]", entries)
		}
	})

	t.Run("answers nothing when the registry directory does not exist or holds nothing", func(t *testing.T) {
		config, dir := makeRegistryEnv(t)
		if err := os.RemoveAll(dir); err != nil {
			t.Fatalf("remove %s: %v", dir, err)
		}
		if entries := ingest.ReadRegistry(domain.EnvLookup{"CLAUDE_CONFIG_DIR": config}, func(int64) bool { return true }); len(entries) != 0 {
			t.Fatalf("entries without a dir = %+v, want none", entries)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if entries := ingest.ReadRegistry(domain.EnvLookup{"CLAUDE_CONFIG_DIR": config}, func(int64) bool { return true }); len(entries) != 0 {
			t.Fatalf("entries with an empty dir = %+v, want none", entries)
		}
	})
}

func TestPidAlive(t *testing.T) {
	t.Run("takes this process for alive and an impossible pid for gone", func(t *testing.T) {
		_, dir := makeRegistryEnv(t)
		thisProcess := int64(os.Getpid())
		writeRegistryFile(t, dir, "81812.json", sessionText(map[string]any{"pid": thisProcess, "sessionId": "this-process"}))
		writeRegistryFile(t, dir, "999999999.json", sessionText(map[string]any{"pid": 999_999_999, "sessionId": "impossible"}))
		entries := ingest.ReadRegistry(domain.EnvLookup{"CLAUDE_CONFIG_DIR": filepath.Dir(dir)}, nil)
		if len(entries) != 1 || entries[0].SessionID != "this-process" {
			t.Fatalf("entries = %+v, want only this process (the impossible pid is gone)", entries)
		}
	})
}
