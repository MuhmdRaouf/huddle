package router

// The request body's JSON on its own: what a parse keeps, what a marshal writes back byte for
// byte, and the capture hash's canonical form. The behaviours the providers and the spool read.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"testing"
)

func TestParseBodyNodeKeepsEveryShape(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want string // the EncodeLiteral round trip
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
			if got := node.EncodeLiteral(); got != tt.want {
				t.Errorf("EncodeLiteral = %s, want %s", got, tt.want)
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

func TestValueEdits(t *testing.T) {
	t.Parallel()
	node := parseBodyNode([]byte(`{"model":"glm-5.3","messages":[{"role":"user"}],"n":7}`))

	t.Run("Has sees a key a null value holds", func(t *testing.T) {
		null := parseBodyNode([]byte(`{"gone":null}`))
		if !null.Has("gone") {
			t.Error("Has missed a key whose value is null")
		}
		if node.Has("absent") {
			t.Error("Has invented a key")
		}
		var nilValue *jsfmt.Value
		if nilValue.Has("anything") {
			t.Error("a nil value has anything")
		}
	})

	t.Run("Set overwrites in place and appends a new key", func(t *testing.T) {
		node.Set("model", jsfmt.String("glm-5.3-flash"))
		node.Set("fresh", jsfmt.Number(1))
		got := node.EncodeLiteral()
		if !strings.Contains(got, `"model":"glm-5.3-flash"`) || !strings.Contains(got, `"fresh":1`) {
			t.Errorf("EncodeLiteral = %s, want both edits", got)
		}

	})

	t.Run("Del removes the key and its place", func(t *testing.T) {
		node.Del("n")
		if node.Has("n") {
			t.Error("Del left the key")
		}
		if got := node.EncodeLiteral(); strings.Contains(got, `"n":7`) {
			t.Errorf("EncodeLiteral = %s, want n gone", got)
		}
		node.Del("n") // a second Del is nothing
		node.Del("absent")
		var nilValue *jsfmt.Value
		nilValue.Del("n")
	})

	t.Run("numbers round trip as written", func(t *testing.T) {
		if got := jsfmt.Number(131072).EncodeLiteral(); got != "131072" {
			t.Errorf("Number(131072) = %s, want 131072", got)
		}
	})

	t.Run("a stripped field leaves every other copy", func(t *testing.T) {
		body := parseBodyNode([]byte(`{"cache_control":"a","messages":[{"cache_control":"b","x":1}],"tools":[{"cache_control":"c"}]}`))
		if !stripFromNode(body, map[string]bool{"cache_control": true}) {
			t.Fatal("stripFromNode stripped nothing")
		}
		if got := body.EncodeLiteral(); strings.Contains(got, "cache_control") {
			t.Errorf("EncodeLiteral = %s, want every cache_control gone", got)
		}
		if plain := parseBodyNode([]byte(`{"x":1}`)); stripFromNode(plain, map[string]bool{"cache_control": true}) {
			t.Error("stripFromNode stripped from a body without the field")
		}
		if stripFromNode(parseBodyNode([]byte(`[1,2]`)), map[string]bool{"cache_control": true}) {
			t.Error("stripFromNode stripped from an array without objects")
		}
		var nilValue *jsfmt.Value
		if stripFromNode(nilValue, map[string]bool{"cache_control": true}) {
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
	body := func(raw string) *jsfmt.Value { return parseBodyNode([]byte(raw)) }

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

// The splitter's capture must answer exactly what the whole-body parse answers: the members it
// hands over parse to the same trees, and a body it refuses keeps the whole-body parse.
func TestCapturedOfMatchesTheWholeBodyParse(t *testing.T) {
	t.Parallel()
	cases := []string{
		`{"system":"s","tools":[{"t":1}],"messages":[{"role":"user","content":"hi"}]}`,
		`{"messages":[{"role":"user","content":"hi"}],"system":[` +
			`{"type":"text","text":"` + billingPrefix + `cc_prompt_id=p;","cache_control":{"type":"ephemeral"}},` +
			`{"type":"text","text":"real"}]}`,
		`{"system":null}`,
		`{"tools":[]}`,
		`{"messages":[]}`,
		`[1,2]`,
		`not json`,
		// The splitter refuses a repeated key; the parse reads the last one.
		`{"system":"s","system":"t"}`,
	}
	for _, raw := range cases {
		body := []byte(raw)
		// Both handovers answer the parse: the capture's own scan, and the spans the provider
		// route's forward read first (nil again when the splitter refuses).
		for _, spans := range [][]keySpan{nil, ownSpans(body)} {
			fromSplit, splitOK := capturedOf(body, spans)
			fromParse, parseOK := promptOf(parseBodyNode(body))
			hashSplit, hashParse := "", ""
			billingSplit, billingParse := billingLine{}, billingLine{}
			if splitOK {
				hashSplit, billingSplit = fromSplit.hash(), fromSplit.billing
			}
			if parseOK {
				hashParse, billingParse = fromParse.hash(), fromParse.billing
			}
			sameID := (billingSplit.promptID == nil) == (billingParse.promptID == nil)
			if sameID && billingSplit.promptID != nil {
				sameID = *billingSplit.promptID == *billingParse.promptID
			}
			if splitOK != parseOK || hashSplit != hashParse || !sameID {
				t.Errorf("%s: capturedOf = %q, %t, %v, want %q, %t, %v", raw, hashSplit, splitOK, billingSplit.promptID, hashParse, parseOK, billingParse.promptID)
			}
		}
	}
	// A giant body carrying neither member parses nothing: the answer is still no capture, and
	// the scan alone reads it.
	huge := `{"messages":[{"role":"user","content":"` + strings.Repeat("x", 2<<20) + `"}]}`
	if _, ok := capturedOf([]byte(huge), nil); ok {
		t.Error("a giant body without system and tools counts as captured")
	}
}

// ownSpans is the body's spans when the splitter accepts it, and nil when it refuses — the same
// handover the provider route's forward makes.
func ownSpans(body []byte) []keySpan {
	spans, _ := splitTopLevel(body)
	return spans
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
		// The splitter refuses the body, so forward has no spans to hand over.
		out, model := providerBody(worker, original, nil, "glm-5.3", "", nil)
		if string(out) != string(original) || model != "glm-5.3" {
			t.Errorf("providerBody = %s, %q, want the original", out, model)
		}
	})

	t.Run("max_tokens is clamped to the model's cap", func(t *testing.T) {
		t.Parallel()
		original := []byte(`{"model":"glm-5.3","max_tokens":999999}`)
		out, _ := providerBody(worker, original, spansOf(t, original), "glm-5.3", "", nil)
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
		out, model := providerBody(worker, original, spansOf(t, original), "glm-5.3-flash", "", request)
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
		out, model := providerBody(worker, original, spansOf(t, original), "glm-5.3-flash", "", nil)
		if string(out) != string(original) || model != "glm-5.3-flash" {
			t.Errorf("providerBody = %s, %q, want the original", out, model)
		}
	})

	t.Run("a strip field anywhere in the body takes the tree so the field is stripped", func(t *testing.T) {
		t.Parallel()
		stripper := routerProvider
		stripper.Strip = []string{"cache_control"}
		worker := newRouterWorker(RouterOptions{Provider: stripper}, nil)
		original := []byte(`{"model":"glm-5.3","cache_control":{"type":"ephemeral"},"max_tokens":999999}`)
		out, model := providerBody(worker, original, spansOf(t, original), "glm-5.3", "", nil)
		if model != "glm-5.3" {
			t.Errorf("providerBody model = %q, want the model as it came", model)
		}
		var parsed struct {
			CacheControl json.RawMessage `json:"cache_control"`
			MaxTokens    int64           `json:"max_tokens"`
		}
		if json.Unmarshal(out, &parsed) != nil || parsed.CacheControl != nil || parsed.MaxTokens != 131072 {
			t.Errorf("providerBody = %s, want cache_control stripped and max_tokens 131072", out)
		}
	})

	t.Run("a strip field named only inside a string value keeps the body as it came", func(t *testing.T) {
		t.Parallel()
		stripper := routerProvider
		stripper.Strip = []string{"cache_control"}
		worker := newRouterWorker(RouterOptions{Provider: stripper}, nil)
		original := []byte(`{"model":"glm-5.3","note":"cache_control"}`)
		out, model := providerBody(worker, original, spansOf(t, original), "glm-5.3", "", nil)
		if string(out) != string(original) || model != "glm-5.3" {
			t.Errorf("providerBody = %s, %q, want the original", out, model)
		}
	})
}

// TestProviderBodyPathsAgree is the one proof the fast path may serve the provider: on every body,
// what the real entry answers — the spliced bytes when its stripsAny gate allows them, the tree's
// re-encoding otherwise — decodes to the same JSON as the tree's own answer and names the same
// model. A body whose bytes hide a name the parse would see (`cache_control`) goes through
// both here: the gate must send it to the tree, never serve it raw.
func TestProviderBodyPathsAgree(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	stripper := routerProvider
	stripper.Strip = []string{"cache_control"}
	cases := []struct {
		name     string
		body     string
		model    string
		fallback string
		provider domain.Provider
	}{
		{"nothing to change", `{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}`, "glm-5.3", "", routerProvider},
		{"a max_tokens above the cap", `{"model":"glm-5.3","max_tokens":999999}`, "glm-5.3", "", routerProvider},
		{"a web_search tool", `{"model":"glm-5.3-flash","tools":[{"type":"web_search_20250305","name":"x"}]}`, "glm-5.3-flash", "", routerProvider},
		{"the fallback's rename", `{"model":"glm-5.3-flash","messages":[{"role":"user","content":"hi"}]}`, "glm-5.3-flash", "glm-5.3", routerProvider},
		{"the fallback's rename and a clamp", `{"model":"glm-5.3-flash","max_tokens":999999}`, "glm-5.3-flash", "glm-5.3", routerProvider},
		{"a rename the web_search model already serves", `{"model":"glm-5.3-flash","tools":[{"type":"web_search_20250305","name":"x"}]}`, "glm-5.3-flash", "glm-5.3", routerProvider},
		{"whitespace wherever JSON allows it", `  {  "model" : "glm-5.3-flash" ,  "max_tokens" : 999999 }  `, "glm-5.3-flash", "", routerProvider},
		{"a strip provider's rejected field", `{"model":"glm-5.3","cache_control":{"type":"ephemeral"},"max_tokens":999999}`, "glm-5.3", "", stripper},
		{"an escaped strip key", `{"model":"glm-5.3","cache\u005fcontrol":{"type":"ephemeral"},"max_tokens":999999}`, "glm-5.3", "", stripper},
		{"an escaped web_search tool", `{"model":"glm-5.3-flash","tools":[{"type":"web\u005fsearch_20250305","name":"x"}]}`, "glm-5.3-flash", "", routerProvider},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			original := []byte(testCase.body)
			entryOut, entryNamed := providerBody(newRouterWorker(RouterOptions{Provider: testCase.provider}, nil),
				original, spansOf(t, original), testCase.model, testCase.fallback, request)
			treeOut, treeNamed := providerBodyTree(newRouterWorker(RouterOptions{Provider: testCase.provider}, nil),
				original, testCase.model, testCase.fallback, request)
			var entryValue, treeValue any
			if err := json.Unmarshal(entryOut, &entryValue); err != nil {
				t.Fatalf("the entry's bytes do not decode: %s: %v", entryOut, err)
			}
			if err := json.Unmarshal(treeOut, &treeValue); err != nil {
				t.Fatalf("the tree's bytes do not decode: %s: %v", treeOut, err)
			}
			if !reflect.DeepEqual(entryValue, treeValue) {
				t.Errorf("the paths disagree: entry %s, tree %s", entryOut, treeOut)
			}
			if entryNamed != treeNamed {
				t.Errorf("the paths name %q and %q", entryNamed, treeNamed)
			}
		})
	}
}
