package router

// The request body's JSON on its own: what a parse keeps, what a marshal writes back byte for
// byte, and the capture hash's canonical form. The behaviours the providers and the spool read.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"testing"
)

func TestParseBodyNodeKeepsEveryShape(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want string // the marshalNode round trip
	}{
		{"an object keeps its order", `{"b":1,"a":"x"}`, `{"b":1,"a":"x"}`},
		{"an array keeps its items", `[1,"two",true,null]`, `[1,"two",true,null]`},
		{"a duplicate key keeps the last in place", `{"a":1,"b":2,"a":3}`, `{"a":3,"b":2}`},
		{"a string is itself", `"hello"`, `"hello"`},
		{"a number is itself", `42`, `42`},
		{"true is itself", `true`, `true`},
		{"null is itself", `null`, `null`},
		{"escapes stay minimal", `{"s":"a\"b\\c\b\f\n\r\td"}`, `{"s":"a\"b\\c\b\f\n\r\td"}`},
		{"a control character is lowercase \\u00xx", "{\"s\":\"\\u0001\"}", "{\"s\":\"\\u0001\"}"},
		{"html characters stay raw", `{"s":"<a>&b"}`, `{"s":"<a>&b"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			node := parseBodyNode([]byte(tt.body))
			if node == nil {
				t.Fatalf("parseBodyNode(%s) = nil", tt.body)
			}
			if got := marshalNode(node); got != tt.want {
				t.Errorf("marshalNode = %s, want %s", got, tt.want)
			}
		})
	}

	t.Run("a body that is not one JSON value is nil", func(t *testing.T) {
		t.Parallel()
		for _, body := range []string{``, `not json`, `{"a":1} trailing`, `[1,2`} {
			if node := parseBodyNode([]byte(body)); node != nil {
				t.Errorf("parseBodyNode(%q) = %v, want nil", body, node)
			}
		}
	})
}

func TestWriteJSONStringEscapesLikeJSONStringify(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"the quotes and the backslash", "a\"b\\c", `"a\"b\\c"`},
		{"the named controls", "\b\f\n\r\t", `"\b\f\n\r\t"`},
		{"the other controls are lowercase unicode escapes", "\x00\x1f", `"\u0000\u001f"`},
		{"nothing else is escaped", "<a>&b", `"<a>&b"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var b strings.Builder
			jsfmt.WriteQuoted(&b, tt.in)
			if got := b.String(); got != tt.want {
				t.Errorf("jsfmt.WriteQuoted = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestJSONNodeEdits(t *testing.T) {
	t.Parallel()
	node := parseBodyNode([]byte(`{"model":"glm-5.3","messages":[{"role":"user"}],"n":7}`))

	t.Run("has sees a key a null value holds", func(t *testing.T) {
		null := parseBodyNode([]byte(`{"gone":null}`))
		if !null.has("gone") {
			t.Error("has missed a key whose value is null")
		}
		if node.has("absent") {
			t.Error("has invented a key")
		}
		var nilNode *jsonNode
		if nilNode.has("anything") {
			t.Error("a nil node has anything")
		}
	})

	t.Run("set overwrites in place and appends a new key", func(t *testing.T) {
		node.set("model", newTextNode("glm-5.3-flash"))
		node.set("fresh", newNumberNode(1))
		got := marshalNode(node)
		if !strings.Contains(got, `"model":"glm-5.3-flash"`) || !strings.Contains(got, `"fresh":1`) {
			t.Errorf("marshalNode = %s, want both edits", got)
		}

	})

	t.Run("del removes the key and its place", func(t *testing.T) {
		node.del("n")
		if node.has("n") {
			t.Error("del left the key")
		}
		if got := marshalNode(node); strings.Contains(got, `"n":7`) {
			t.Errorf("marshalNode = %s, want n gone", got)
		}
		node.del("n") // a second del is nothing
		node.del("absent")
		var nilNode *jsonNode
		nilNode.del("n")
	})

	t.Run("numbers round trip as written", func(t *testing.T) {
		if got := marshalNode(newNumberNode(131072)); got != "131072" {
			t.Errorf("newNumberNode = %s, want 131072", got)
		}
	})

	t.Run("a stripped field leaves every other copy", func(t *testing.T) {
		body := parseBodyNode([]byte(`{"cache_control":"a","messages":[{"cache_control":"b","x":1}],"tools":[{"cache_control":"c"}]}`))
		if !stripFromNode(body, map[string]bool{"cache_control": true}) {
			t.Fatal("stripFromNode stripped nothing")
		}
		if got := marshalNode(body); strings.Contains(got, "cache_control") {
			t.Errorf("marshalNode = %s, want every cache_control gone", got)
		}
		if plain := parseBodyNode([]byte(`{"x":1}`)); stripFromNode(plain, map[string]bool{"cache_control": true}) {
			t.Error("stripFromNode stripped from a body without the field")
		}
		if stripFromNode(parseBodyNode([]byte(`[1,2]`)), map[string]bool{"cache_control": true}) {
			t.Error("stripFromNode stripped from an array without objects")
		}
		var nilNode *jsonNode
		if stripFromNode(nilNode, map[string]bool{"cache_control": true}) {
			t.Error("stripFromNode stripped from nil")
		}
	})

	t.Run("modelIn reads the model", func(t *testing.T) {
		if modelIn(node) != "glm-5.3-flash" {
			t.Errorf("modelIn = %q", modelIn(node))
		}
		if modelIn(parseBodyNode([]byte(`{"model":7}`))) != "" {
			t.Error("modelIn read a number")
		}
	})
}

func TestPromptHashOfHashesSystemAndToolsAlone(t *testing.T) {
	t.Parallel()
	body := func(raw string) *jsonNode { return parseBodyNode([]byte(raw)) }

	t.Run("the same content hashes the same however the keys arrived", func(t *testing.T) {
		t.Parallel()
		first, ok1 := PromptHashOf(body(`{"system":"s","tools":[{"t":1}],"temperature":0}`))
		second, ok2 := PromptHashOf(body(`{"temperature":0,"tools":[{"t":1}],"system":"s"}`))
		if !ok1 || !ok2 || first != second {
			t.Errorf("hashes = %q/%q (%t/%t), want one hash", first, second, ok1, ok2)
		}
	})

	t.Run("a body with neither is not captured", func(t *testing.T) {
		t.Parallel()
		if _, ok := PromptHashOf(body(`{"messages":[]}`)); ok {
			t.Error("a body without system and tools counts as captured")
		}
		if _, ok := PromptHashOf(body(`[1,2]`)); ok {
			t.Error("an array counts as captured")
		}
	})

	t.Run("a null counts as carried", func(t *testing.T) {
		t.Parallel()
		hash, ok := PromptHashOf(body(`{"system":null}`))
		if !ok || hash == "" {
			t.Errorf("PromptHashOf = %q, %t, want a hash", hash, ok)
		}
	})

	t.Run("nested content is canonical too", func(t *testing.T) {
		t.Parallel()
		first, _ := PromptHashOf(body(`{"tools":[{"a":[1,{"z":2,"b":3}]}]}`))
		second, _ := PromptHashOf(body(`{"tools":[{"a":[1,{"b":3,"z":2}]}]}`))
		if first != second {
			t.Errorf("nested hashes differ: %q vs %q", first, second)
		}
	})
}

func TestCaptureMemoRemembersEveryHashOnce(t *testing.T) {
	t.Parallel()
	memo := newCaptureMemo()
	if !memo.first("a") || memo.first("a") {
		t.Error("first did not answer once for a")
	}
	if !memo.first("b") {
		t.Error("first answered false for a fresh hash")
	}
}

func TestCapturedHeadersKeepsTheAllowList(t *testing.T) {
	t.Parallel()
	answer := func(headers map[string]string) *map[string]string {
		h := make(map[string][]string, len(headers))
		for name, value := range headers {
			h[name] = []string{value}
		}
		return CapturedHeaders(h)
	}
	if got := answer(map[string]string{"Request-Id": "r-1", "anthropic-ratelimit-requests-remaining": "9", "secret": "no"}); got == nil ||
		(*got)["request-id"] != "r-1" || (*got)["anthropic-ratelimit-requests-remaining"] != "9" {
		t.Errorf("CapturedHeaders = %v, want the allow-listed names lowercased", got)
	}
	if got := answer(map[string]string{"secret": "no"}); got != nil {
		t.Errorf("CapturedHeaders = %v, want nil without an allow-listed name", got)
	}
}

func TestProviderBodyAdaptsWhatTheProviderNeeds(t *testing.T) {
	t.Parallel()
	worker := newRouterWorker(RouterOptions{Provider: routerProvider}, nil)

	t.Run("a body that is no object goes as it came", func(t *testing.T) {
		t.Parallel()
		original := []byte(`[1,2]`)
		out, model := providerBody(worker, original, parseBodyNode(original), "glm-5.3", nil)
		if string(out) != string(original) || model != "glm-5.3" {
			t.Errorf("providerBody = %s, %q, want the original", out, model)
		}
	})

	t.Run("max_tokens is clamped to the model's cap", func(t *testing.T) {
		t.Parallel()
		original := []byte(`{"model":"glm-5.3","max_tokens":999999}`)
		out, _ := providerBody(worker, original, parseBodyNode(original), "glm-5.3", nil)
		var parsed struct {
			MaxTokens int64 `json:"max_tokens"`
		}
		if json.Unmarshal(out, &parsed) != nil || parsed.MaxTokens != 131072 {
			t.Errorf("providerBody = %s, want max_tokens 131072", out)
		}
	})

	t.Run("a web_search tool rides the model that serves it", func(t *testing.T) {
		t.Parallel()
		original := []byte(`{"model":"glm-5.3-flash","tools":[{"type":"web_search_20250305","name":"x"}]}`)
		var lines []string
		worker := newRouterWorker(RouterOptions{Provider: routerProvider, Log: func(line string) { lines = append(lines, line) }}, nil)
		request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		out, model := providerBody(worker, original, parseBodyNode(original), "glm-5.3-flash", request)
		if model != "glm-5.3" {
			t.Errorf("providerBody model = %q, want the web search model", model)
		}
		if !strings.Contains(string(out), `"model":"glm-5.3"`) {
			t.Errorf("providerBody = %s, want the model rewritten", out)
		}
		if len(lines) != 1 {
			t.Errorf("the log = %v, want the rewrite line", lines)
		}
	})

	t.Run("a regular tool changes nothing", func(t *testing.T) {
		t.Parallel()
		original := []byte(`{"model":"glm-5.3-flash","tools":[{"name":"bash"}]}`)
		out, model := providerBody(worker, original, parseBodyNode(original), "glm-5.3-flash", nil)
		if string(out) != string(original) || model != "glm-5.3-flash" {
			t.Errorf("providerBody = %s, %q, want the original", out, model)
		}
	})
}
