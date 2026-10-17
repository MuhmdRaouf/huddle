package shared

import (
	"reflect"
	"strings"
	"testing"
)

func TestIsSecretKey(t *testing.T) {
	t.Run("matches the obvious shapes at any casing", func(t *testing.T) {
		for _, key := range []string{
			"api_key", "API_KEY", "authToken", "Authorization", "cookie", "PASSWORD",
			"clientSecret",
		} {
			if !IsSecretKey(key) {
				t.Errorf("IsSecretKey(%q) = false, want true", key)
			}
		}
	})

	t.Run("does not match ordinary keys", func(t *testing.T) {
		for _, key := range []string{"model", "command", "file_path", "cwd", "prompt", "tool_use_id"} {
			if IsSecretKey(key) {
				t.Errorf("IsSecretKey(%q) = true, want false", key)
			}
		}
	})
}

func TestIsEnvFilePath(t *testing.T) {
	t.Run("accepts .env, dotted variants and named env files, with or without a directory", func(t *testing.T) {
		for _, value := range []string{".env", ".env.local", "/srv/app/.env", "prod.env", "../secrets/staging.env"} {
			if !IsEnvFilePath(value) {
				t.Errorf("IsEnvFilePath(%q) = false, want true", value)
			}
		}
	})

	t.Run("rejects ordinary paths and env-ish words", func(t *testing.T) {
		for _, value := range []string{"src/env.ts", ".environment", "environment", "notes.txt", ""} {
			if IsEnvFilePath(value) {
				t.Errorf("IsEnvFilePath(%q) = true, want false", value)
			}
		}
	})
}

func TestTruncate(t *testing.T) {
	t.Run("keeps short text as-is", func(t *testing.T) {
		if got := Truncate("hello", FieldLimit); got != "hello" {
			t.Errorf("Truncate(\"hello\") = %q, want \"hello\"", got)
		}
	})

	t.Run("cuts at the limit and reports what was dropped", func(t *testing.T) {
		cases := []struct {
			text  string
			limit int
			want  string
		}{
			{"abcdefghij", 8, "abcdefgh…[truncated 2 chars]"},
			{strings.Repeat("x", FieldLimit+3), FieldLimit, strings.Repeat("x", FieldLimit) + "…[truncated 3 chars]"},
		}
		for _, tt := range cases {
			if got := Truncate(tt.text, tt.limit); got != tt.want {
				t.Errorf("Truncate(len %d, %d) = %q, want %q", len(tt.text), tt.limit, got, tt.want)
			}
		}
	})

	t.Run("counts characters, not bytes", func(t *testing.T) {
		text := strings.Repeat("é", 10)
		want := "éééé…[truncated 6 chars]"
		if got := Truncate(text, 4); got != want {
			t.Errorf("Truncate(é×10, 4) = %q, want %q", got, want)
		}
	})
}

func TestScrubText(t *testing.T) {
	t.Run("returns undefined for anything that is not a string", func(t *testing.T) {
		for _, value := range []any{42, nil, map[string]any{"content": "hi"}} {
			if _, ok := ScrubText(value, FieldLimit); ok {
				t.Errorf("ScrubText(%v) = ok, want not ok", value)
			}
		}
	})

	t.Run("masks bearer headers and sk- keys inside free text", func(t *testing.T) {
		cases := []struct{ in, want string }{
			{"Authorization: Bearer abc123XYZ", "Authorization: Bearer [redacted]"},
			{"the key sk-abcdefgh123456 leaked", "the key [redacted] leaked"},
			{"plain prompt text", "plain prompt text"},
		}
		for _, tt := range cases {
			got, ok := ScrubText(tt.in, FieldLimit)
			if !ok || got != tt.want {
				t.Errorf("ScrubText(%q) = %q, %v; want %q, ok", tt.in, got, ok, tt.want)
			}
		}
	})

	t.Run("requires the sk- body to be long enough that ordinary words survive", func(t *testing.T) {
		if got, _ := ScrubText("task-sk-123 done", FieldLimit); got != "task-sk-123 done" {
			t.Errorf("ScrubText(\"task-sk-123 done\") = %q, want it unchanged", got)
		}
	})

	t.Run("truncates to a custom limit", func(t *testing.T) {
		if got, _ := ScrubText("0123456789", 4); got != "0123…[truncated 6 chars]" {
			t.Errorf("ScrubText(\"0123456789\", 4) = %q, want \"0123…[truncated 6 chars]\"", got)
		}
	})
}

func TestScrub(t *testing.T) {
	t.Run("replaces the value of any secret key, at any depth, in objects and arrays", func(t *testing.T) {
		input := map[string]any{
			"model":   "claude-sonnet-5-5",
			"api_key": "sk-abcdefgh123456",
			"nested":  map[string]any{"sessionToken": "top", "keep": "me"},
			"list":    []any{map[string]any{"password": "hunter2"}, "plain item"},
		}
		out, ok := Scrub(input, FieldLimit).(map[string]any)
		if !ok {
			t.Fatalf("Scrub did not return an object: %T", Scrub(input, FieldLimit))
		}
		if out["model"] != "claude-sonnet-5-5" {
			t.Errorf("out.model = %v, want claude-sonnet-5-5", out["model"])
		}
		if out["api_key"] != "[redacted]" {
			t.Errorf("out.api_key = %v, want [redacted]", out["api_key"])
		}
		nested := out["nested"].(map[string]any)
		if nested["sessionToken"] != "[redacted]" {
			t.Errorf("out.nested.sessionToken = %v, want [redacted]", nested["sessionToken"])
		}
		if nested["keep"] != "me" {
			t.Errorf("out.nested.keep = %v, want me", nested["keep"])
		}
		list := out["list"].([]any)
		if list[0].(map[string]any)["password"] != "[redacted]" {
			t.Errorf("out.list[0].password = %v, want [redacted]", list[0])
		}
		if list[1] != "plain item" {
			t.Errorf("out.list[1] = %v, want plain item", list[1])
		}
	})

	t.Run("redacts content-bearing keys when the same object mentions an env file", func(t *testing.T) {
		out := Scrub(map[string]any{
			"file_path": ".env",
			"command":   "cat /etc/passwd",
			"content":   "SECRET=1",
			"model":     "glm-5.3",
		}, FieldLimit).(map[string]any)
		if out["file_path"] != ".env" {
			t.Errorf("out.file_path = %v, want .env", out["file_path"])
		}
		if out["command"] != "[redacted]" {
			t.Errorf("out.command = %v, want [redacted]", out["command"])
		}
		if out["content"] != "[redacted]" {
			t.Errorf("out.content = %v, want [redacted]", out["content"])
		}
		if out["model"] != "glm-5.3" {
			t.Errorf("out.model = %v, want glm-5.3", out["model"])
		}
	})

	t.Run("keeps content keys when no env path is in sight", func(t *testing.T) {
		out := Scrub(map[string]any{"file_path": "src/app.ts", "command": "npm test"}, FieldLimit).(map[string]any)
		if out["command"] != "npm test" {
			t.Errorf("out.command = %v, want npm test", out["command"])
		}
	})

	t.Run("masks bearer/sk- material in plain string values and truncates everything", func(t *testing.T) {
		long := strings.Repeat("a", 100) + " sk-abcdefgh123456 " + strings.Repeat("b", 3000)
		out := Scrub(map[string]any{"note": long}, 64).(map[string]any)
		note := out["note"].(string)
		if !strings.HasPrefix(note, strings.Repeat("a", 64)) {
			t.Errorf("note starts with %q, want 64 a's", note[:min(70, len(note))])
		}
		if !strings.Contains(note, "…[truncated ") {
			t.Errorf("note = %q, want a truncation note", note)
		}
		if strings.Contains(note, "sk-abcdefgh123456") {
			t.Errorf("note = %q, want the sk- key masked", note)
		}
	})

	t.Run("passes scalars through untouched", func(t *testing.T) {
		if got := Scrub(7, FieldLimit); got != 7 {
			t.Errorf("Scrub(7) = %v, want 7", got)
		}
		if got := Scrub(true, FieldLimit); got != true {
			t.Errorf("Scrub(true) = %v, want true", got)
		}
		if got := Scrub(nil, FieldLimit); got != nil {
			t.Errorf("Scrub(nil) = %v, want nil", got)
		}
	})

	t.Run("leaves the input unmodified (a fresh value comes back)", func(t *testing.T) {
		input := map[string]any{"api_key": "sk-abcdefgh123456", "safe": "yes"}
		out := Scrub(input, FieldLimit).(map[string]any)
		if input["api_key"] != "sk-abcdefgh123456" {
			t.Errorf("input.api_key = %v, want it unchanged", input["api_key"])
		}
		if out["api_key"] != "[redacted]" {
			t.Errorf("out.api_key = %v, want [redacted]", out["api_key"])
		}
		if out["safe"] != "yes" {
			t.Errorf("out.safe = %v, want yes", out["safe"])
		}
	})
}

// TestScrubNumbers guards the pass-through of numbers decoded from JSON, which arrive
// as float64 and must not be mangled.
func TestScrubNumbers(t *testing.T) {
	t.Run("passes JSON numbers through untouched", func(t *testing.T) {
		if got := Scrub(any(1.5), FieldLimit); !reflect.DeepEqual(got, any(1.5)) {
			t.Errorf("Scrub(1.5) = %v, want 1.5", got)
		}
	})
}
