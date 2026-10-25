package router

// The prompt rewriter: the first message_start of a provider stream gets a real prompt size, and
// every other byte goes out as it came. Whole lines only are looked at.

import (
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// readAll reads a rewriter's whole answer.
func readAll(t *testing.T, rewriter io.Reader) string {
	t.Helper()
	out, err := io.ReadAll(rewriter)
	if err != nil {
		t.Fatalf("the rewriter's read: %v", err)
	}
	return string(out)
}

const (
	// zeroStart is the lie the rewriter exists to fix: a message_start with input_tokens 0.
	zeroStart = "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":0,\"output_tokens\":1}}}\n\n"
	// realStart carries a real prompt size, left exactly as it came.
	realStart = "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1500}}}\n\n"
	// cachedStart already carries the cache fields Claude Code reads beside it.
	cachedStart = "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":0,\"cache_read_input_tokens\":5}}}\n\n"
)

func TestPromptRewriterRewritesOnlyTheZeroStart(t *testing.T) {
	t.Parallel()
	estimator := NewPromptEstimator()

	t.Run("a zero start gets the estimate and the cache fields", func(t *testing.T) {
		t.Parallel()
		hint := &PromptHint{Key: "k", BodyChars: 12345}
		estimator.RecordPrompt("k", domain.RouteUsage{"input_tokens": 900}, 111)
		out := readAll(t, &promptRewriter{
			source:    strings.NewReader(zeroStart + "data: {\"type\":\"ping\"}\n\n"),
			prompt:    *hint,
			estimator: estimator,
		})
		want := strconv.FormatInt(estimator.EstimatePrompt("k", 12345), 10)
		if !strings.Contains(out, `"input_tokens":`+want) {
			t.Errorf("the rewrite = %q, want the estimate %s", out, want)
		}
		if !strings.Contains(out, `"cache_creation_input_tokens":0`) || !strings.Contains(out, `"cache_read_input_tokens":0`) {
			t.Errorf("the rewrite = %q, want the cache fields added", out)
		}
		if !strings.HasSuffix(out, "data: {\"type\":\"ping\"}\n\n") {
			t.Errorf("the rewrite = %q, want the rest byte for byte", out)
		}
	})

	t.Run("a real prompt size and the cache fields go out as they came", func(t *testing.T) {
		t.Parallel()
		for name, start := range map[string]string{"a real size": realStart, "the cache fields": cachedStart} {
			out := readAll(t, &promptRewriter{
				source:    strings.NewReader(start),
				prompt:    PromptHint{Key: "k", BodyChars: 1},
				estimator: estimator,
			})
			if out != start {
				t.Errorf("%s: the rewrite = %q, want the line unchanged", name, out)
			}
		}
	})

	t.Run("a stream whose first start never comes passes through", func(t *testing.T) {
		t.Parallel()
		source := "data: {\"type\":\"ping\"}\n\nno newline at all"
		out := readAll(t, &promptRewriter{
			source:    strings.NewReader(source),
			prompt:    PromptHint{Key: "k"},
			estimator: estimator,
		})
		if out != source {
			t.Errorf("the rewrite = %q, want the stream as it came", out)
		}
	})

	t.Run("a line split across chunks is held until it is whole", func(t *testing.T) {
		t.Parallel()
		source := zeroStart
		at := strings.Index(source, ",\"output_tokens\"")
		hint := &PromptHint{Key: "k", BodyChars: 10}
		rewriter := &promptRewriter{source: strings.NewReader(source[at:]), prompt: *hint, estimator: estimator}
		rewriter.pending = []byte(source[:at])
		out := readAll(t, rewriter)
		if !strings.Contains(out, "message_start") {
			t.Errorf("the rewrite = %q, want the whole line rewritten", out)
		}
	})
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("the stream broke") }

func TestPromptRewriterPassesAnErrorThrough(t *testing.T) {
	t.Parallel()
	rewriter := &promptRewriter{source: errReader{}, prompt: PromptHint{Key: "k"}, estimator: NewPromptEstimator()}
	if _, err := rewriter.Read(make([]byte, 16)); err == nil {
		t.Error("the rewriter swallowed the stream's error")
	}
}

func TestRewrittenStartLineKeepsTheLinesEnding(t *testing.T) {
	t.Parallel()
	estimator := NewPromptEstimator()
	line := "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":0}}}\r\n"
	out, decided, changed := rewrittenStartLine(line, PromptHint{Key: "k"}, estimator)
	if !decided || !changed || !strings.HasSuffix(out, "\r\n") || !strings.HasPrefix(out, "data: ") {
		t.Errorf("rewrittenStartLine = %q, %t, %t, want a rewrite keeping the crlf", out, decided, changed)
	}
	if out2, decided, changed := rewrittenStartLine("data: {\"type\":\"ping\"}\n", PromptHint{Key: "k"}, estimator); decided || changed || out2 != "" {
		t.Errorf("rewrittenStartLine of a ping = %q, %t, %t, want nothing", out2, decided, changed)
	}
}

func TestZeroInputTokensAndTheEventNode(t *testing.T) {
	t.Parallel()
	usageOf := func(usage string) *jsfmt.Value {
		line := "data: {\"type\":\"message_start\",\"message\":{\"usage\":" + usage + "}}\n"
		event := sseEventNode(line)
		if event == nil {
			t.Fatalf("sseEventNode(%s) = nil", line)
		}
		return event.Get("message").Get("usage")
	}
	if !zeroInputTokens(usageOf(`{"input_tokens":0}`)) {
		t.Error("an input_tokens of 0 was not seen as one")
	}
	if zeroInputTokens(usageOf(`{"input_tokens":5}`)) {
		t.Error("an input_tokens of 5 was seen as zero")
	}
	if zeroInputTokens(usageOf(`{}`)) {
		t.Error("a usage without input_tokens was seen as zero")
	}
	if zeroInputTokens(usageOf(`{"input_tokens":"0"}`)) {
		t.Error("an input_tokens of a string was seen as zero")
	}

	if node := sseEventNode("event: ping\n"); node != nil {
		t.Error("a non-data line parsed as an event")
	}
	if node := sseEventNode("data: [1,2]\n"); node != nil {
		t.Error("an array parsed as an event object")
	}
}

func TestPromptHintOfCarriesTheKeyAndTheLength(t *testing.T) {
	t.Parallel()

	t.Run("the key and the length come from the tree", func(t *testing.T) {
		t.Parallel()
		system, first := promptPartsOf(parseBodyNode([]byte(
			`{"system":"sys","messages":[{"role":"user","content":"hi"},{"role":"assistant"}]}`)))
		hint := promptHintOf(system, first, 42)
		if hint == nil || hint.BodyChars != 42 {
			t.Fatalf("promptHintOf = %+v, want 42 chars", hint)
		}
		if hint.Key == "" {
			t.Error("promptHintOf built no key from the system and the first message")
		}
	})

	t.Run("a body that is no object still answers a hint of the body's length", func(t *testing.T) {
		t.Parallel()
		system, first := promptPartsOf(parseBodyNode([]byte("not json")))
		if bare := promptHintOf(system, first, 7); bare == nil || bare.BodyChars != 7 {
			t.Errorf("promptHintOf = %+v, want 7 chars", bare)
		}
	})

	t.Run("later messages do not move the key", func(t *testing.T) {
		t.Parallel()
		oneSystem, oneFirst := promptPartsOf(parseBodyNode([]byte(
			`{"system":"sys","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"first turn"}]}`)))
		twoSystem, twoFirst := promptPartsOf(parseBodyNode([]byte(
			`{"system":"sys","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"another second turn"}]}`)))
		one := promptHintOf(oneSystem, oneFirst, 1)
		two := promptHintOf(twoSystem, twoFirst, 2)
		if one.Key != two.Key {
			t.Errorf("keys = %q and %q, want one conversation", one.Key, two.Key)
		}
	})

	t.Run("a different system is another conversation", func(t *testing.T) {
		t.Parallel()
		oneSystem, oneFirst := promptPartsOf(parseBodyNode([]byte(
			`{"system":"sys","messages":[{"role":"user","content":"hi"}]}`)))
		twoSystem, twoFirst := promptPartsOf(parseBodyNode([]byte(
			`{"system":"another sys","messages":[{"role":"user","content":"hi"}]}`)))
		one := promptHintOf(oneSystem, oneFirst, 1)
		two := promptHintOf(twoSystem, twoFirst, 1)
		if one.Key == two.Key {
			t.Errorf("both keys = %q, want them apart", one.Key)
		}
	})
}
