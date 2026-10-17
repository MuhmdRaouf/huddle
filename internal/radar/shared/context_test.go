package shared

import "testing"

// bigInput builds a request with input tokens and nothing else set.
func bigInput(input int64, over func(*RequestRecord)) *RequestRecord {
	r := &RequestRecord{Model: "glm-5.3", Tokens: Tokens{Input: input}}
	if over != nil {
		over(r)
	}
	return r
}

func TestContextWindow(t *testing.T) {
	t.Run("knows a session's window", func(t *testing.T) {
		cases := []struct {
			name     string
			requests []*RequestRecord
			want     int64
		}{
			{"a small request", []*RequestRecord{bigInput(10, nil)}, ContextSmall},
			{"a 1m model id", []*RequestRecord{bigInput(10, func(r *RequestRecord) {
				r.Model = "claude-opus-5[1m]"
			})}, ContextLarge},
			{"a request over the small window", []*RequestRecord{bigInput(300_000, nil)}, ContextLarge},
		}
		for _, tt := range cases {
			if got := ContextWindow(tt.requests); got != tt.want {
				t.Errorf("%s: ContextWindow() = %d, want %d", tt.name, got, tt.want)
			}
		}
	})

	t.Run("a run total does not make the window large", func(t *testing.T) {
		runTotal := &RequestRecord{
			Model: "glm-5.3-flash", Totals: true,
			Tokens: Tokens{Input: 2_000_000, Output: 10_000, CacheRead: 574_000},
		}
		if got := ContextWindow([]*RequestRecord{runTotal}); got != ContextSmall {
			t.Errorf("ContextWindow([run total]) = %d, want %d", got, ContextSmall)
		}
		// the same tokens without the totals flag do say 1M
		perCall := *runTotal
		perCall.Totals = false
		if got := ContextWindow([]*RequestRecord{&perCall}); got != ContextLarge {
			t.Errorf("ContextWindow([per call]) = %d, want %d", got, ContextLarge)
		}
	})

	t.Run("an empty session works in the small window", func(t *testing.T) {
		if got := ContextWindow(nil); got != ContextSmall {
			t.Errorf("ContextWindow(nil) = %d, want %d", got, ContextSmall)
		}
	})
}

func TestContextOf(t *testing.T) {
	t.Run("is the call's input plus cache tokens", func(t *testing.T) {
		r := &RequestRecord{Tokens: Tokens{Input: 100, Output: 5_000, CacheRead: 50, CacheWrite: 60}}
		if got := ContextOf(r); got != 210 {
			t.Errorf("ContextOf() = %d, want 210", got)
		}
	})
}

func TestIsRunTotal(t *testing.T) {
	t.Run("is true only when the totals flag is set", func(t *testing.T) {
		if !IsRunTotal(&RequestRecord{Totals: true}) {
			t.Error("IsRunTotal(totals set) = false, want true")
		}
		if IsRunTotal(&RequestRecord{}) {
			t.Error("IsRunTotal(no totals) = true, want false")
		}
	})
}
