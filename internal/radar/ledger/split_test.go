package ledger

import (
	"bytes"
	"testing"
)

// The body splitter's cases: a chat body cuts at its top-level members and at the
// elements of array-valued members, everything else stays one span.

func TestSplitBody(t *testing.T) {
	t.Parallel()

	t.Run("cuts a chat body at its members and message elements", func(t *testing.T) {
		body := `{"model":"m","messages":[{"role":"user","content":"a"}, ` +
			`{"role":"assistant","content":"b"}],"stream":true}`
		want := []string{
			`{`,
			`"model":"m"`,
			`,"messages":[`,
			`{"role":"user","content":"a"}`,
			`, {"role":"assistant","content":"b"}]`,
			`,"stream":true}`,
		}
		spans := splitBody([]byte(body))
		if len(spans) != len(want) {
			t.Fatalf("split into %d spans, want %d: %q", len(spans), len(want), spans)
		}
		for at, span := range spans {
			if string(span) != want[at] {
				t.Errorf("span %d = %q, want %q", at, span, want[at])
			}
		}
		if joined := bytes.Join(spans, nil); string(joined) != body {
			t.Errorf("the spans joined = %q, want the body unchanged", joined)
		}
	})

	t.Run("an object-valued member is cut as one span with its value whole", func(t *testing.T) {
		body := `{"model":"m","metadata":{"user_id":"u"},"messages":[{"role":"user","content":"a"}],` +
			`"thinking":{"type":"enabled","budget_tokens":8000},"stream":true}`
		want := []string{
			`{`,
			`"model":"m"`,
			`,"metadata":{"user_id":"u"}`,
			`,"messages":[`,
			`{"role":"user","content":"a"}]`,
			`,"thinking":{"type":"enabled","budget_tokens":8000}`,
			`,"stream":true}`,
		}
		spans := splitBody([]byte(body))
		if len(spans) != len(want) {
			t.Fatalf("split into %d spans, want %d: %q", len(spans), len(want), spans)
		}
		for at, span := range spans {
			if string(span) != want[at] {
				t.Errorf("span %d = %q, want %q", at, span, want[at])
			}
		}
		if joined := bytes.Join(spans, nil); string(joined) != body {
			t.Errorf("the spans joined = %q, want the body unchanged", joined)
		}
	})

	t.Run("an object as the last member still splits", func(t *testing.T) {
		body := `{"model":"m","metadata":{"a":{"b":[1,2]}}}`
		want := []string{`{`, `"model":"m"`, `,"metadata":{"a":{"b":[1,2]}}}`}
		spans := splitBody([]byte(body))
		if len(spans) != len(want) {
			t.Fatalf("split into %d spans, want %d: %q", len(spans), len(want), spans)
		}
		for at, span := range spans {
			if string(span) != want[at] {
				t.Errorf("span %d = %q, want %q", at, span, want[at])
			}
		}
	})

	t.Run("empty and nested values at the top level join back exactly", func(t *testing.T) {
		for _, member := range []string{`{}`, `[]`, `null`, `{"a":{}}`, `{"a":[{"b":[]}]}`, `{"a":"}{"}`} {
			body := `{"model":"m","extra":` + member + `,"stream":true}`
			spans := splitBody([]byte(body))
			if joined := bytes.Join(spans, nil); string(joined) != body {
				t.Errorf("member %s: the spans joined = %q, want the body unchanged", member, joined)
			}
			if len(spans) < 4 {
				t.Errorf("member %s: split into %d spans, want at least 4: %q", member, len(spans), spans)
			}
		}
	})

	t.Run("a pretty-printed body keeps its newlines and indentation", func(t *testing.T) {
		body := "{\n  \"model\": \"m\",\n  \"messages\": [\n    {\"role\": \"user\", \"content\": \"a\"},\n" +
			"    {\"role\": \"assistant\", \"content\": \"b\"}\n  ],\n  \"stream\": true\n}"
		spans := splitBody([]byte(body))
		if joined := bytes.Join(spans, nil); string(joined) != body {
			t.Fatalf("the spans joined = %q, want the body unchanged", joined)
		}
		var messageSpans int
		for _, span := range spans {
			if bytes.Contains(span, []byte(`{"role"`)) {
				messageSpans++
			}
		}
		if messageSpans != 2 {
			t.Errorf("%d spans hold a message element, want each message in its own (2)", messageSpans)
		}
	})

	t.Run("a body that is not JSON stays one span", func(t *testing.T) {
		body := []byte("this is not json at all")
		spans := splitBody(body)
		if len(spans) != 1 || !bytes.Equal(spans[0], body) {
			t.Fatalf("split = %q, want the one span %q", spans, body)
		}
	})

	t.Run("a JSON array stays one span", func(t *testing.T) {
		body := []byte(`[{"role":"user","content":"a"},{"role":"assistant","content":"b"}]`)
		spans := splitBody(body)
		if len(spans) != 1 || !bytes.Equal(spans[0], body) {
			t.Fatalf("split = %q, want the one span %q", spans, body)
		}
	})

	t.Run("a malformed object stays one span", func(t *testing.T) {
		body := []byte(`{"model":"m","messages":`)
		spans := splitBody(body)
		if len(spans) != 1 || !bytes.Equal(spans[0], body) {
			t.Fatalf("split = %q, want the one span %q", spans, body)
		}
	})

	t.Run("an object followed by trailing bytes stays one span", func(t *testing.T) {
		body := []byte(`{"model":"m"} trailing junk`)
		spans := splitBody(body)
		if len(spans) != 1 || !bytes.Equal(spans[0], body) {
			t.Fatalf("split = %q, want the one span %q", spans, body)
		}
	})

	t.Run("an empty body splits into no spans", func(t *testing.T) {
		if spans := splitBody(nil); len(spans) != 0 {
			t.Fatalf("split(nil) = %q, want no spans", spans)
		}
		if spans := splitBody([]byte{}); len(spans) != 0 {
			t.Fatalf("split(empty) = %q, want no spans", spans)
		}
	})
}
