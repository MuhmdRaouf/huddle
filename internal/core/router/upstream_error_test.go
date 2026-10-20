package router

// The one-line reason an upstream error answer gives: the shapes providers actually send, the
// key and every credential-shaped run redacted, and the length a JS slice would keep.

import (
	"strings"
	"testing"
)

func TestUpstreamErrorReasonReadsTheShapesProvidersSend(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want string
	}{
		{"anthropic's type and message", `{"error":{"type":"overloaded_error","message":"Overloaded"}}`, "overloaded_error: Overloaded"},
		{"zai's code and message", `{"error":{"code":"1210","message":"the balance is out"}}`, "1210: the balance is out"},
		{"zai's flat code and msg", `{"code":"1210","msg":"the balance is out"}`, "1210: the balance is out"},
		{"a code without a message", `{"error":{"code":"1210"}}`, "1210"},
		{"a message without a code", `{"error":{"message":"nope"}}`, "nope"},
		{"a flat message", `{"message":"nope"}`, "nope"},
		{"a body that is no json is the text itself", "  Gateway  timeout  ", "Gateway timeout"},
		{"a json array is the text itself", `[1,2]`, `[1,2]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := UpstreamErrorReason([]byte(tt.body), "")
			if !ok || got != tt.want {
				t.Errorf("UpstreamErrorReason = %q, %t, want %q", got, ok, tt.want)
			}
		})
	}

	t.Run("no reason is none", func(t *testing.T) {
		t.Parallel()
		for _, body := range []string{"", "   ", "\n"} {
			if got, ok := UpstreamErrorReason([]byte(body), ""); ok || got != "" {
				t.Errorf("UpstreamErrorReason(%q) = %q, %t, want none", body, got, ok)
			}
		}
	})
}

func TestUpstreamErrorReasonRedactsTheKeyAndEverySecret(t *testing.T) {
	t.Parallel()
	const key = "sk-ant-0123456789abcdef0123456789abcdef"

	t.Run("the route's own key is gone", func(t *testing.T) {
		t.Parallel()
		got, ok := UpstreamErrorReason([]byte("bad key "+key+" for that"), key)
		if !ok || strings.Contains(got, key) {
			t.Errorf("UpstreamErrorReason = %q, want the key redacted", got)
		}
	})

	t.Run("a credential-shaped run is gone too", func(t *testing.T) {
		t.Parallel()
		got, ok := UpstreamErrorReason([]byte("token AbCdEf0123456789AbCdEf01 refused"), "")
		if !ok || strings.Contains(got, "AbCdEf0123456789AbCdEf01") {
			t.Errorf("UpstreamErrorReason = %q, want the run redacted", got)
		}
	})

	t.Run("a short word survives", func(t *testing.T) {
		t.Parallel()
		got, ok := UpstreamErrorReason([]byte("the model glm-5.3 is busy"), "")
		if !ok || got != "the model glm-5.3 is busy" {
			t.Errorf("UpstreamErrorReason = %q, want the line whole", got)
		}
	})
}

func TestUpstreamErrorReasonKeepsTheFirstLineShort(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("word ", 200)
	got, ok := UpstreamErrorReason([]byte(long), "")
	if !ok || len(got) > len("word ")*reasonChars {
		t.Errorf("UpstreamErrorReason kept %d characters, want at most %d units", len(got), reasonChars)
	}

	t.Run("a cut falls between characters, not inside one", func(t *testing.T) {
		t.Parallel()
		text := strings.Repeat("Δ", reasonChars+40)
		got, ok := UpstreamErrorReason([]byte(text), "")
		if !ok || strings.TrimSpace(got) == "" {
			t.Errorf("UpstreamErrorReason = %q, %t, want a cut the runes survive", got, ok)
		}
	})
}

func TestUtf16SliceCutsLikeJavaScriptDoes(t *testing.T) {
	t.Parallel()
	if got := utf16Slice("hello", 100); got != "hello" {
		t.Errorf("utf16Slice = %q, want the whole", got)
	}
	if got := utf16Slice("hello", 3); got != "hel" {
		t.Errorf("utf16Slice = %q, want hel", got)
	}
	// Δ is one UTF-16 unit, 𝔊 is two: the cut never splits a surrogate pair
	if got := utf16Slice("Δ𝔊x", 2); got != "Δ" {
		t.Errorf("utf16Slice = %q, want Δ", got)
	}
	if got := utf16Slice("Δ𝔊x", 3); got != "Δ𝔊" {
		t.Errorf("utf16Slice = %q, want Δ𝔊", got)
	}
}

func TestErrorMessageAnswersFalseForWhatItCannotRead(t *testing.T) {
	t.Parallel()
	if _, ok := errorMessage("not json"); ok {
		t.Error("errorMessage read prose")
	}
	if _, ok := errorMessage("5"); ok {
		t.Error("errorMessage read a number as an error object")
	}
	if what, ok := errorMessage(`{"error":{"code":"x","message":""}}`); !ok || what != "x" {
		t.Errorf("errorMessage = %q, %t, want the code alone", what, ok)
	}
	if what, ok := errorMessage(`{"error":{"type":"","message":"only words"}}`); !ok || what != "only words" {
		t.Errorf("errorMessage = %q, %t, want the message alone", what, ok)
	}
}
