package jsfmt

import (
	"encoding/json"
	"testing"
)

func TestDecodeValueErrors(t *testing.T) {
	t.Run("rejects broken JSON", func(t *testing.T) {
		for _, broken := range []string{`{"ts":`, `{"a":}`, `{1:2}`, ``} {
			if _, err := DecodeValue([]byte(broken)); err == nil {
				t.Errorf("DecodeValue(%q) succeeded, want an error", broken)
			}
		}
	})

	t.Run("rejects trailing data after the value", func(t *testing.T) {
		if _, err := DecodeValue([]byte(`{"a":1} {"b":2}`)); err == nil {
			t.Error("DecodeValue(two objects) succeeded, want an error")
		}
	})
}

func TestValueEncodeJSON(t *testing.T) {
	t.Run("keeps object keys in arrival order", func(t *testing.T) {
		const doc = `{"z":1,"a":{"y":["x",2,true,null]},"m":{}}`
		v, err := DecodeValue([]byte(doc))
		if err != nil {
			t.Fatal(err)
		}
		if got := v.EncodeJSON(); got != doc {
			t.Errorf("EncodeJSON() = %s, want %s", got, doc)
		}
	})

	t.Run("prints numbers as String(n) prints them", func(t *testing.T) {
		v, err := DecodeValue([]byte(`[1.0,0.1,1e21,-0,9007199254740993,3.5e-7,42]`))
		if err != nil {
			t.Fatal(err)
		}
		const want = `[1,0.1,1e+21,0,9007199254740992,3.5e-7,42]`
		if got := v.EncodeJSON(); got != want {
			t.Errorf("EncodeJSON() = %s, want %s", got, want)
		}
	})

	t.Run("never escapes <, > or &, so <redacted> stays literal", func(t *testing.T) {
		const in = `"<redacted> & <b>"`
		v, err := DecodeValue([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		if got := v.EncodeJSON(); got != in {
			t.Errorf("EncodeJSON() = %s, want %s", got, in)
		}
		if got := String("<redacted>").EncodeJSON(); got != `"<redacted>"` {
			t.Errorf("String(<redacted>).EncodeJSON() = %s, want a literal <redacted>", got)
		}
	})

	t.Run("leaves U+2028 and U+2029 unescaped, as JSON.stringify does", func(t *testing.T) {
		sep := string(rune(0x2028)) + string(rune(0x2029))
		in := []byte("\"a" + sep + "b\"")
		v, err := DecodeValue(in)
		if err != nil {
			t.Fatal(err)
		}
		want := "\"a" + sep + "b\""
		if got := v.EncodeJSON(); got != want {
			t.Errorf("EncodeJSON() = %q, want %q", got, want)
		}
	})

	t.Run("escapes the controls below 0x20, with the five short forms; 0x7f stays raw", func(t *testing.T) {
		ctrl := string([]byte{1, 'x', 8, 12, 10, 13, 9, 0x1f})
		got := String(ctrl).EncodeJSON()
		want := "\"\\u0001x\\b\\f\\n\\r\\t\\u001f\""
		if got != want {
			t.Errorf("EncodeJSON() = %s, want %s", got, want)
		}
		del := string(rune(0x7f))
		if got := String(del).EncodeJSON(); got != "\""+del+"\"" {
			t.Errorf("EncodeJSON() = %q, want the raw DEL", got)
		}
	})

	t.Run("encodes a lone surrogate a UTF-16 cut left behind as \\udXXX", func(t *testing.T) {
		lone := "a" + string([]byte{0xed, 0xa0, 0xbd}) // U+D83D in its WTF-8 form
		got := String(lone).EncodeJSON()
		want := "\"a\\ud83d\""
		if got != want {
			t.Errorf("EncodeJSON() = %s, want %s", got, want)
		}
	})

	t.Run("scalars encode alone", func(t *testing.T) {
		for in, want := range map[string]string{
			`null`: `null`, `true`: `true`, `false`: `false`,
			`7`: `7`, `"s"`: `"s"`, `[]`: `[]`, `{}`: `{}`,
		} {
			v, err := DecodeValue([]byte(in))
			if err != nil {
				t.Fatal(err)
			}
			if got := v.EncodeJSON(); got != want {
				t.Errorf("DecodeValue(%s).EncodeJSON() = %s, want %s", in, got, want)
			}
		}
	})

	t.Run("the constructors build values that encode like decoded ones", func(t *testing.T) {
		v := Object([]string{"z", "a"}, []*Value{
			String("<redacted>"),
			Array([]*Value{String("q")}),
		})
		const want = `{"z":"<redacted>","a":["q"]}`
		if got := v.EncodeJSON(); got != want {
			t.Errorf("EncodeJSON() = %s, want %s", got, want)
		}
	})
}

func TestValueAccessors(t *testing.T) {
	t.Run("expose the decoded shape: kinds, text, keys, fields and items", func(t *testing.T) {
		v, err := DecodeValue([]byte(`{"s":"t","o":{"k":1},"a":[2]}`))
		if err != nil {
			t.Fatal(err)
		}
		if v.Kind() != KindObject {
			t.Errorf("Kind() = %v, want KindObject", v.Kind())
		}
		keys := v.Keys()
		if len(keys) != 3 || keys[0] != "s" || keys[1] != "o" || keys[2] != "a" {
			t.Errorf("Keys() = %v, want [s o a]", keys)
		}
		fields := v.Fields()
		if fields[0].Kind() != KindString || fields[0].Text() != "t" {
			t.Errorf("fields[0] = %v %q, want a string t", fields[0].Kind(), fields[0].Text())
		}
		if fields[1].Kind() != KindObject {
			t.Errorf("fields[1].Kind() = %v, want KindObject", fields[1].Kind())
		}
		items := fields[2].Items()
		if len(items) != 1 || items[0].Kind() != KindNumber {
			t.Errorf("fields[2].Items() = %v, want one number", items)
		}
		if got := items[0].EncodeJSON(); got != "2" {
			t.Errorf("items[0].EncodeJSON() = %s, want 2", got)
		}
	})
}

// A value inside a struct marshals through the standard encoder as its own JSON: keys in their
// own order, a nil value null — never the sorted keys of a Go map.
func TestValueMarshalJSON(t *testing.T) {
	ordered, err := DecodeValue([]byte(`{"zeta":0.3,"alpha":0.2}`))
	if err != nil {
		t.Fatal(err)
	}
	type holder struct {
		Cost  *Value  `json:"cost"`
		Total float64 `json:"total"`
	}
	raw, err := json.Marshal(holder{Cost: ordered, Total: 0.6})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"cost":{"zeta":0.3,"alpha":0.2},"total":0.6}` {
		t.Errorf("marshalled = %s, want the keys in their own order", raw)
	}
	if raw, err = json.Marshal(holder{Cost: nil, Total: 0}); err != nil || string(raw) != `{"cost":null,"total":0}` {
		t.Errorf("a nil value marshalled = %s (%v), want null", raw, err)
	}
}
