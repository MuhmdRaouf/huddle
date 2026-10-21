package jsfmt

// The tree behaviours the router's body rewriting and the settings writer rely on: numbers that
// keep their literal, keys that keep their place, duplicate keys that follow JSON.parse, and the
// absent-value rules JSON.stringify writes.

import "testing"

func TestEncodeLiteralKeepsNumbersByteForByte(t *testing.T) {
	t.Run("a decoded value round trips byte for byte", func(t *testing.T) {
		const doc = `{"id":12345678901234567890,"x":1.0,"e":1e3}`
		v, err := DecodeValue([]byte(doc))
		if err != nil {
			t.Fatal(err)
		}
		if got := v.EncodeLiteral(); got != doc {
			t.Errorf("EncodeLiteral() = %s, want %s", got, doc)
		}
	})

	t.Run("EncodeJSON still prints the double", func(t *testing.T) {
		v, err := DecodeValue([]byte(`{"id":12345678901234567890,"x":1.0,"e":1e3}`))
		if err != nil {
			t.Fatal(err)
		}
		const want = `{"id":12345678901234567000,"x":1,"e":1000}`
		if got := v.EncodeJSON(); got != want {
			t.Errorf("EncodeJSON() = %s, want %s", got, want)
		}
	})

	t.Run("the constructors print the shapes they build", func(t *testing.T) {
		v := Object([]string{"id", "gone"}, []*Value{NumberLiteral("131072"), Null()})
		if got := v.EncodeLiteral(); got != `{"id":131072,"gone":null}` {
			t.Errorf("EncodeLiteral() = %s, want the literal and the null", got)
		}
	})
}

func TestDecodeValueDuplicateKeys(t *testing.T) {
	v, err := DecodeValue([]byte(`{"a":1,"b":2,"a":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if keys := v.Keys(); len(keys) != 2 || keys[0] != "a" || keys[1] != "b" {
		t.Errorf("Keys() = %v, want [a b]", keys)
	}
	if a := v.Get("a"); a == nil || a.Kind() != KindNumber || a.Literal() != "3" {
		t.Errorf("a = %v, want the number 3", a)
	}
	if got := v.EncodeJSON(); got != `{"a":3,"b":2}` {
		t.Errorf("EncodeJSON() = %s, want the last value in the first place", got)
	}
}

func TestValueMutators(t *testing.T) {
	t.Run("Set replaces in place and appends a new key at the end", func(t *testing.T) {
		v := Object([]string{"a", "b"}, []*Value{String("1"), String("2")})
		v.Set("a", NumberLiteral("3"))
		v.Set("c", Bool(true))
		if keys := v.Keys(); len(keys) != 3 || keys[0] != "a" || keys[1] != "b" || keys[2] != "c" {
			t.Errorf("Keys() = %v, want a's place kept and c at the end", keys)
		}
		if got := v.EncodeLiteral(); got != `{"a":3,"b":"2","c":true}` {
			t.Errorf("EncodeLiteral() = %s, want both edits", got)
		}
	})

	t.Run("Del removes the key and its value", func(t *testing.T) {
		v := Object([]string{"a", "b"}, []*Value{String("1"), String("2")})
		v.Del("a")
		if v.Has("a") || v.Get("a") != nil {
			t.Error("Del left the key behind")
		}
		if got := v.EncodeLiteral(); got != `{"b":"2"}` {
			t.Errorf("EncodeLiteral() = %s, want b alone", got)
		}
		v.Del("a") // a second Del is nothing
	})

	t.Run("Append and SetItems edit an array in place", func(t *testing.T) {
		v := Array([]*Value{String("1")})
		v.Append(NumberLiteral("2"), Null())
		if got := v.EncodeLiteral(); got != `["1",2,null]` {
			t.Errorf("EncodeLiteral() = %s, want the appended items", got)
		}
		v.SetItems([]*Value{NumberLiteral("9")})
		if got := v.EncodeLiteral(); got != `[9]` {
			t.Errorf("EncodeLiteral() = %s, want the new items alone", got)
		}
	})

	t.Run("Get and Has answer safely on anything that is not an object", func(t *testing.T) {
		object := Object([]string{"a"}, []*Value{String("v")})
		if got := object.Get("absent"); got != nil || object.Has("absent") {
			t.Error("an absent key answered")
		}
		text := String("s")
		if got := text.Get("a"); got != nil || text.Has("a") {
			t.Error("a string answered a key")
		}
		var nilValue *Value
		if got := nilValue.Get("a"); got != nil || nilValue.Has("a") {
			t.Error("a nil value answered a key")
		}
		nilValue.Del("a") // nothing to remove, no panic
	})

	t.Run("Len counts keys", func(t *testing.T) {
		v := Object([]string{"a", "b"}, []*Value{String("1"), String("2")})
		if v.Len() != 2 {
			t.Errorf("Len() = %d, want 2", v.Len())
		}
		if String("s").Len() != 0 {
			t.Error("a string has keys")
		}
		var nilValue *Value
		if nilValue.Len() != 0 {
			t.Error("a nil value has keys")
		}
	})

	t.Run("the scalar accessors answer their shape alone", func(t *testing.T) {
		object := Object([]string{"a"}, []*Value{String("v")})
		if text, ok := object.Get("a").TextOf(); !ok || text != "v" {
			t.Errorf(`TextOf() = %q, %t, want "v"`, text, ok)
		}
		if _, ok := String("s").TextOf(); !ok {
			t.Error("a string's own text is not one")
		}
		if _, ok := object.TextOf(); ok {
			t.Error("an object answered TextOf")
		}
		if !object.IsObject() || object.IsArray() || String("s").IsObject() || Array(nil).IsArray() == false {
			t.Error("IsObject and IsArray answered the wrong shapes")
		}
		if !Bool(true).Boolean() || Bool(false).Boolean() || String("true").Boolean() {
			t.Error("Boolean answered for a value that is not a true boolean")
		}
		if got := NumberLiteral("1.5").Literal(); got != "1.5" {
			t.Errorf("Literal() = %s, want 1.5", got)
		}
		var nilValue *Value
		if nilValue.IsObject() || nilValue.IsArray() {
			t.Error("a nil value is a container")
		}
	})
}

func TestAbsentValuesEncodeLikeJSONStringify(t *testing.T) {
	object := Object([]string{"a", "b"}, []*Value{nil, String("2")})
	const want = `{"b":"2"}`
	for name, got := range map[string]string{
		"EncodeJSON":     object.EncodeJSON(),
		"EncodeLiteral":  object.EncodeLiteral(),
		"EncodeIndented": object.EncodeIndented("  "),
	} {
		if name == "EncodeIndented" {
			if got != "{\n  \"b\": \"2\"\n}" {
				t.Errorf("EncodeIndented() = %q, want the field dropped", got)
			}
			continue
		}
		if got != want {
			t.Errorf("%s() = %s, want the nil field dropped", name, got)
		}
	}
	array := Array([]*Value{String("1"), nil})
	for name, got := range map[string]string{
		"EncodeJSON":     array.EncodeJSON(),
		"EncodeLiteral":  array.EncodeLiteral(),
		"EncodeIndented": array.EncodeIndented("  "),
	} {
		if name == "EncodeIndented" {
			if got != "[\n  \"1\",\n  null\n]" {
				t.Errorf("EncodeIndented() = %q, want the nil item as null", got)
			}
			continue
		}
		if got != `["1",null]` {
			t.Errorf("%s() = %s, want the nil item as null", name, got)
		}
	}
}

func TestEncodeIndented(t *testing.T) {
	v, err := DecodeValue([]byte(`{"a":[1,{"b":{}}],"c":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	const want = "{\n  \"a\": [\n    1,\n    {\n      \"b\": {}\n    }\n  ],\n  \"c\": []\n}"
	if got := v.EncodeIndented("  "); got != want {
		t.Errorf("EncodeIndented(\"  \") = %q, want %q", got, want)
	}
}

func TestEqual(t *testing.T) {
	parse := func(doc string) *Value {
		v, err := DecodeValue([]byte(doc))
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if Equal(parse(`{"a":1,"b":2}`), parse(`{"b":2,"a":1}`)) {
		t.Error("key order counted as equal")
	}
	if Equal(parse(`{"a":1}`), parse(`{"a":1.0}`)) {
		t.Error("1 counted equal to 1.0")
	}
	if !Equal(parse(`{"a":[1,{"b":"x"}],"c":null}`), parse(`{"a":[1,{"b":"x"}],"c":null}`)) {
		t.Error("deep-equal trees compared unequal")
	}
	if !Equal(nil, nil) || Equal(nil, String("s")) {
		t.Error("nil compared wrongly")
	}
}
