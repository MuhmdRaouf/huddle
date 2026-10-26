// The prompt estimate's cases, inputs and expected outputs.
package router

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

func TestPromptKey(t *testing.T) {
	t.Parallel()
	t.Run("is 16 hex characters and stable for the same system and first message", func(t *testing.T) {
		first := json.RawMessage(`{"role":"user","content":"hi"}`)
		key := PromptKey("You delegate work.", first)
		if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(key) {
			t.Fatalf("promptKey = %q, want 16 hex characters", key)
		}
		if PromptKey("You delegate work.", first) != key {
			t.Fatal("promptKey is not stable for the same system and first message")
		}
	})

	t.Run("moves when the system or the first message moves", func(t *testing.T) {
		if PromptKey("a", "m") == PromptKey("b", "m") {
			t.Fatal("the key ignored a moved system")
		}
		if PromptKey("a", "m") == PromptKey("a", "n") {
			t.Fatal("the key ignored a moved first message")
		}
	})

	t.Run("reads only the first 4096 characters of each part", func(t *testing.T) {
		edge := strings.Repeat("x", 4096)
		// Differences past the cut are invisible: the head is the identity, not the whole transcript.
		if PromptKey(edge+"A", "m") != PromptKey(edge+"B", "m") {
			t.Fatal("the key read past the first 4096 characters")
		}
		if PromptKey("a", "m") == PromptKey("b", "m") {
			t.Fatal("the head alone stopped telling conversations apart")
		}
	})

	// The exact hash keys for the same parts: the cut at UTF-16 code units, the lone surrogate
	// it can leave, and the raw U+2028 included.
	t.Run("hashes the exact bytes for the edge-case parts", func(t *testing.T) {
		emoji := strings.Repeat("\U0001F600", 2048)
		cases := []struct {
			name         string
			system       any
			firstMessage any
			want         string
		}{
			{"a plain pair", "You delegate work.", json.RawMessage(`{"role":"user","content":"hi"}`), "0b7de24774564d9f"},
			{"a moved system", "a", "m", "d120c14b4ed3fed1"},
			{"a cut at a plain edge", strings.Repeat("x", 4096) + "A", "m", "03f5326a2ca7fd19"},
			{"a cut inside a surrogate pair", emoji + "A", "m", "12d145cdd3687830"},
			{"a line separator kept raw", "a b", "m", "87dc2175e89cddcd"},
			{"a missing system", nil, "m", "7eb1920948e18570"},
			{"a decoded field that was absent", json.RawMessage(nil), "m", "7eb1920948e18570"},
			{"a nested message", "s", json.RawMessage(`{"role":"user","content":[{"type":"text","text":"hi & <bye>"}]}`), "41a7306e45e362f5"},
		}
		for _, testCase := range cases {
			if got := PromptKey(testCase.system, testCase.firstMessage); got != testCase.want {
				t.Errorf("%s: promptKey = %s, want %s", testCase.name, got, testCase.want)
			}
		}
	})
}

// The raw fast path must answer exactly what the encoder's own path answers: the head window's
// bytes are the first ones of the whole value's compact text, so past any cut both read the same.
func TestCutPartRawMatchesTheEncodersPath(t *testing.T) {
	t.Parallel()
	// One giant string per body, in every rune width the window can cut in half, each long enough
	// to pass both the window and the budget.
	giants := map[string]json.RawMessage{
		"plain ascii":            messageWith(strings.Repeat("x", 100000)),
		"two-byte runes":         messageWith(strings.Repeat("é", 60000)),
		"three-byte runes":       messageWith(strings.Repeat("中", 50000)),
		"a four-byte emoji":      messageWith(strings.Repeat("\U0001F600", 40000)),
		"escapes as they came":   messageWith(strings.Repeat(`a\"b\\c`, 20000)),
		"line-separator escapes": messageWith(strings.Repeat(`a\u2028b`, 20000)),
		"spaces in and out":      json.RawMessage(`{ "role" : "user" , "content" : "` + strings.Repeat(" ", 100000) + `" }`),
		"the window's last byte": messageWith(strings.Repeat("x", keyPartHead)),
	}
	for name, raw := range giants {
		if got, want := cutPartRaw(raw), cutCompact(raw); got != want {
			t.Errorf("%s: cutPartRaw read %d bytes, want the encoder's own %d", name, len(got), len(want))
		}
	}
	// The window cuts at every phase: a one-to-three byte shift moves the cut inside two-, three-
	// and four-byte runes, where the head must end at a rune's own end.
	for _, shift := range []string{"", "a", "ab", "abc"} {
		for _, text := range []string{
			strings.Repeat("é", 60000),
			strings.Repeat("中", 50000),
			strings.Repeat("\U0001F600", 40000),
		} {
			if got, want := cutPartRaw(messageWith(shift+text)), cutCompact(messageWith(shift+text)); got != want {
				t.Errorf("shift %q: cutPartRaw read %d bytes, want the encoder's own %d", shift, len(got), len(want))
			}
		}
	}
	// The budget's cut at a surrogate pair: whole pairs, and one lone surrogate both sides leave.
	for _, emoji := range []string{strings.Repeat("\U0001F600", 2048), strings.Repeat("\U0001F600", 2047)} {
		if got, want := cutPartRaw(messageWith(emoji+"A")), cutCompact(messageWith(emoji+"A")); got != want {
			t.Error("the cut at a surrogate pair disagreed with the encoder's path")
		}
	}
	// A part the window holds whole, a window short of the budget, and no part at all keep the
	// encoder's own answers.
	short := messageWith(strings.Repeat("x", keyPartChars-1))
	if cutPartRaw(short) != cutCompact(short) {
		t.Error("a part short of the budget took the window")
	}
	if cutPartRaw(nil) != "" || cutPartRaw(json.RawMessage("")) != "" {
		t.Error("a missing part was not empty")
	}
}

// messageWith is a first message whose `content` is one string of text, as raw body bytes.
func messageWith(text string) json.RawMessage {
	return json.RawMessage(`{"role":"user","content":"` + text + `"}`)
}

func TestEstimatePrompt(t *testing.T) {
	t.Parallel()
	t.Run("takes a quarter token per byte for a conversation never seen", func(t *testing.T) {
		estimates := NewPromptEstimator()
		if got := estimates.EstimatePrompt("no-such-conversation", 8192); got != 2048 {
			t.Fatalf("estimatePrompt(8192) = %d, want 2048", got)
		}
		if got := estimates.EstimatePrompt("no-such-conversation", 7); got != 2 {
			t.Fatalf("estimatePrompt(7) = %d, want 2", got)
		}
	})

	t.Run("grows from what the last turn of the same conversation really cost", func(t *testing.T) {
		estimates := NewPromptEstimator()
		key := PromptKey("s-grown", "m")
		estimates.RecordPrompt(key, domain.RouteUsage{
			"input_tokens":                1000.0,
			"cache_read_input_tokens":     50.0,
			"cache_creation_input_tokens": 3.0,
		}, 8000)
		if got := estimates.EstimatePrompt(key, 12000); got != 2053 {
			t.Fatalf("estimatePrompt(12000) = %d, want 2053", got)
		}
	})

	t.Run("never goes down when the next body is shorter", func(t *testing.T) {
		estimates := NewPromptEstimator()
		key := PromptKey("s-shorter", "m")
		estimates.RecordPrompt(key, domain.RouteUsage{"input_tokens": 900.0}, 8000)
		if got := estimates.EstimatePrompt(key, 4000); got != 900 {
			t.Fatalf("estimatePrompt(4000) = %d, want 900", got)
		}
	})

	t.Run("ignores a final usage that still reports no prompt size", func(t *testing.T) {
		estimates := NewPromptEstimator()
		key := PromptKey("s-zero", "m")
		estimates.RecordPrompt(key, domain.RouteUsage{"input_tokens": 0.0, "output_tokens": 30.0}, 8000)
		if got := estimates.EstimatePrompt(key, 8000); got != 2000 {
			t.Fatalf("estimatePrompt(8000) = %d, want 2000", got)
		}
	})

	t.Run("forgets the oldest conversation past 500", func(t *testing.T) {
		estimates := NewPromptEstimator()
		evicted := PromptKey("s-oldest", "m")
		estimates.RecordPrompt(evicted, domain.RouteUsage{"input_tokens": 999999.0}, 0)
		for i := 0; i < 500; i++ {
			estimates.RecordPrompt(
				PromptKey(fmt.Sprintf("s-flood-%d", i), "m"),
				domain.RouteUsage{"input_tokens": 1000000.0 + float64(i)},
				0,
			)
		}
		// The flooded-out conversation estimates like a stranger again; the newest one is still known.
		if got := estimates.EstimatePrompt(evicted, 4000); got != 1000 {
			t.Fatalf("estimatePrompt(evicted) = %d, want 1000", got)
		}
		if got := estimates.EstimatePrompt(PromptKey("s-flood-499", "m"), 0); got != 1000499 {
			t.Fatalf("estimatePrompt(newest) = %d, want 1000499", got)
		}
	})
}

// The extra cases cover the branches recordPrompt guards: a stream with no final usage, and a
// usage whose input is not a number, remember nothing.
func TestRecordPrompt(t *testing.T) {
	t.Parallel()
	t.Run("records nothing without a final usage or with no number in it", func(t *testing.T) {
		estimates := NewPromptEstimator()
		key := PromptKey("s-none", "m")
		estimates.RecordPrompt(key, nil, 8000)
		estimates.RecordPrompt(key, domain.RouteUsage{"input_tokens": "1000"}, 8000)
		if got := estimates.EstimatePrompt(key, 8000); got != 2000 {
			t.Fatalf("estimatePrompt(8000) = %d, want the stranger's 2000", got)
		}
		if _, known := estimates.turns[key]; known {
			t.Fatal("a numberless usage was remembered")
		}
	})

	t.Run("reads the counters however the caller decoded them", func(t *testing.T) {
		estimates := NewPromptEstimator()
		asInt := PromptKey("s-int", "m")
		estimates.RecordPrompt(asInt, domain.RouteUsage{"input_tokens": 4000}, 0)
		if got := estimates.EstimatePrompt(asInt, 0); got != 4000 {
			t.Fatalf("estimatePrompt(int counters) = %d, want 4000", got)
		}
		asInt64 := PromptKey("s-int64", "m")
		estimates.RecordPrompt(asInt64, domain.RouteUsage{"input_tokens": int64(2000)}, 0)
		if got := estimates.EstimatePrompt(asInt64, 0); got != 2000 {
			t.Fatalf("estimatePrompt(int64 counters) = %d, want 2000", got)
		}
	})
}
