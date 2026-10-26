package router

// The body's top-level byte splitter: what a whole JSON object's members look like as spans, what
// the splitter refuses, and how it agrees with encoding/json on everything it accepts.

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestSplitTopLevelFindsEveryMember(t *testing.T) {
	t.Parallel()
	// Nested objects and arrays, every metacharacter inside strings, every escape form, a `é` key,
	// numbers in every shape, and whitespace wherever JSON allows it.
	body := `  {  "model" : "glm-5.3" ,
    "nested" : {"deep":[ 1 , {"deeper":[true,null]} ]},
    "tricky" : "{},[],:,\"\\\/\b\f\n\r\t\u0041",
    "é" : "unicode key" ,
    "big" : 1e3 , "neg" : -0.5 , "exp" : -2.5e-7 , "zero" : 0 , "int" : 131072 ,
    "flags" : [ ] , "empty" : { } , "lit" : false , "nul" : null
}  `
	spans, ok := splitTopLevel([]byte(body))
	if !ok {
		t.Fatal("splitTopLevel refused a whole JSON object")
	}
	want := []struct{ key, value string }{
		{"model", `"glm-5.3"`},
		{"nested", `{"deep":[ 1 , {"deeper":[true,null]} ]}`},
		{"tricky", `"{},[],:,\"\\\/\b\f\n\r\t\u0041"`},
		{"é", `"unicode key"`},
		{"big", `1e3`},
		{"neg", `-0.5`},
		{"exp", `-2.5e-7`},
		{"zero", `0`},
		{"int", `131072`},
		{"flags", `[ ]`},
		{"empty", `{ }`},
		{"lit", `false`},
		{"nul", `null`},
	}
	if len(spans) != len(want) {
		t.Fatalf("splitTopLevel found %d members, want %d", len(spans), len(want))
	}
	for i, member := range want {
		if spans[i].key != member.key {
			t.Errorf("span %d key = %q, want %q", i, spans[i].key, member.key)
		}
		if got := body[spans[i].valueStart:spans[i].valueEnd]; got != member.value {
			t.Errorf("span %d (%s) value = %q, want %q", i, member.key, got, member.value)
		}
	}

	t.Run("a body that is not one whole object is refused", func(t *testing.T) {
		t.Parallel()
		// 10001 levels of nesting: one past the decoder's own ceiling, which refuses it.
		deep := `{"a":` + strings.Repeat(`{"b":`, 10000) + `1` + strings.Repeat(`}`, 10000) + `}`
		for _, body := range []string{
			`[1]`,
			`{"a":1} x`,
			`{"a":1,"a":2}`,
			`{"a":"unterminated`,
			`{"a":[1}`,
			`{"a":1]`,
			``,
			`   `,
			`{"a":}`,
			`{"a" 1}`,
			`{a:1}`,
			`{"a":01}`,
			`{"a":1.}`,
			`{"a":.5}`,
			`{"a":+1}`,
			`{"a":1e}`,
			`{"a":tru}`,
			`{"a":1,}`,
			`{"a":1 "b":2}`,
			`{"a":1}}`,
			`{"a":"\u00"}`,
			`{"a":"\q"}`,
			"{\"a\":\"\x01\"}",
			deep,
		} {
			if spans, ok := splitTopLevel([]byte(body)); ok {
				t.Errorf("splitTopLevel(%q) = %v, want false", body, spans)
			}
		}
	})
}

func TestFirstItemAndSpliced(t *testing.T) {
	t.Parallel()

	t.Run("firstItem answers the first element's text", func(t *testing.T) {
		t.Parallel()
		array := ` [ {"role":"user"} , {"role":"assistant"} ] `
		if item, ok := firstItem([]byte(array)); !ok || string(item) != `{"role":"user"}` {
			t.Errorf("firstItem = %q, %t, want the first message", item, ok)
		}
		if item, ok := firstItem([]byte(`[ [1,2] , {"x":3} ]`)); !ok || string(item) != `[1,2]` {
			t.Errorf("firstItem = %q, %t, want the nested array", item, ok)
		}
		if item, ok := firstItem([]byte(`[ {"a":"va[l]ue"} ]`)); !ok || string(item) != `{"a":"va[l]ue"}` {
			t.Errorf("firstItem = %q, %t, want the string kept whole", item, ok)
		}
		if item, ok := firstItem([]byte(`[]`)); ok {
			t.Errorf("firstItem([]) = %q, want false", item)
		}
		if item, ok := firstItem([]byte(`  `)); ok {
			t.Errorf("firstItem of whitespace = %q, want false", item)
		}
		if item, ok := firstItem([]byte(`{"a":1}`)); ok {
			t.Errorf("firstItem of an object = %q, want false", item)
		}
		if item, ok := firstItem([]byte(`[{"a":`)); ok {
			t.Errorf("firstItem of a truncated array = %q, want false", item)
		}
	})

	t.Run("spliced replaces one member's value and keeps the rest byte for byte", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"model":"glm-5.3","stream":true,"max_tokens":16}`)
		spans := spansOf(t, body)
		if len(spans) != 3 {
			t.Fatalf("splitTopLevel found %d members, want 3", len(spans))
		}
		out := spliced(body, spans[1], []byte(`false`))
		if want := `{"model":"glm-5.3","stream":false,"max_tokens":16}`; string(out) != want {
			t.Errorf("spliced = %s, want %s", out, want)
		}
		out = spliced(body, spans[0], []byte(`"glm-5.3-flash"`))
		if want := `{"model":"glm-5.3-flash","stream":true,"max_tokens":16}`; string(out) != want {
			t.Errorf("spliced = %s, want %s", out, want)
		}
		out = spliced(body, spans[2], []byte(`131072`))
		if want := `{"model":"glm-5.3","stream":true,"max_tokens":131072}`; string(out) != want {
			t.Errorf("spliced = %s, want %s", out, want)
		}
	})
}

func FuzzSplitTopLevelAgreesWithTheDecoder(f *testing.F) {
	for _, seed := range []string{
		`{"model":"glm-5.3","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
		`  {  "a" : { "b" : [ 1 , 2 ] } , "é" : "{},[],\"x" , "n" : -2.5e-7 }  `,
		`{}`,
		`{"system":"s","tools":[{"type":"web_search_20250305"}],"messages":[{"role":"user"}]}`,
		`{"a":1,"a":2}`,
		`[1]`,
		`{"a":1} x`,
		`{"a":"unterminated`,
		`{"a":[1}`,
		`{"a":01}`,
		`{"a":tru}`,
		``,
		// 10001 levels: the splitter must refuse what the decoder refuses.
		`{"a":` + strings.Repeat(`{"b":`, 10000) + `1` + strings.Repeat(`}`, 10000) + `}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		spans, ok := splitTopLevel(body)
		if !ok {
			if json.Valid(body) && wholeObjectWithUniqueKeys(body) {
				t.Fatalf("splitTopLevel refused the whole object %q", body)
			}
			return
		}
		// What the splitter accepts, the decoder accepts: the same members, and every value
		// compacting to the span's own text.
		var members map[string]json.RawMessage
		if err := json.Unmarshal(body, &members); err != nil {
			t.Fatalf("splitTopLevel accepted what the decoder refuses: %q: %v", body, err)
		}
		if len(members) != len(spans) {
			t.Fatalf("splitTopLevel found %d members of %q, the decoder %d", len(spans), body, len(members))
		}
		for _, span := range spans {
			raw, present := members[span.key]
			if !present {
				t.Fatalf("splitTopLevel found key %q the decoder does not carry: %q", span.key, body)
			}
			if compacted(raw) != compacted(body[span.valueStart:span.valueEnd]) {
				t.Fatalf("span %q of %q compacts to %s, the member to %s",
					span.key, body, compacted(body[span.valueStart:span.valueEnd]), compacted(raw))
			}
		}
	})
}

// wholeObjectWithUniqueKeys answers whether body is one whole JSON object whose top-level keys
// are unique, as the decoder walks it.
func wholeObjectWithUniqueKeys(body []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(body))
	if token, err := dec.Token(); err != nil || token != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return false
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return false
		}
		seen[key] = true
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return false
		}
	}
	if _, err := dec.Token(); err != nil {
		return false
	}
	if _, err := dec.Token(); err != io.EOF {
		return false
	}
	return true
}

// compacted is the JSON's bytes with the whitespace out, for comparing two values' texts.
func compacted(raw []byte) string {
	var out bytes.Buffer
	if err := json.Compact(&out, raw); err != nil {
		return "<invalid>"
	}
	return out.String()
}

// spansOf splits a body the tests know is a whole object.
func spansOf(t *testing.T, body []byte) []keySpan {
	t.Helper()
	spans, ok := splitTopLevel(body)
	if !ok {
		t.Fatalf("splitTopLevel refused %s", body)
	}
	return spans
}
