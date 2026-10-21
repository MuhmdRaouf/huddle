package jsfmt

import (
	"encoding/json"
	"slices"
	"strings"
)

// Null builds the JSON null.
func Null() *Value { return &Value{kind: KindNull} }

// NumberLiteral builds a number value from its literal; EncodeLiteral and EncodeIndented print it
// byte for byte, since the router's body rewrite and the settings writer must return a number
// exactly as it was read.
func NumberLiteral(n json.Number) *Value { return &Value{kind: KindNumber, number: n} }

// IsObject reports whether the value is an object (false for nil).
func (v *Value) IsObject() bool { return v != nil && v.kind == KindObject }

// IsArray reports whether the value is an array (false for nil).
func (v *Value) IsArray() bool { return v != nil && v.kind == KindArray }

// TextOf is the value as a string, !ok for anything else (a missing key reads as
// not-a-string too).
func (v *Value) TextOf() (string, bool) {
	if v == nil || v.kind != KindString {
		return "", false
	}
	return v.text, true
}

// Literal is a number value's decoded literal; "" for any other shape.
func (v *Value) Literal() json.Number {
	if v == nil || v.kind != KindNumber {
		return ""
	}
	return v.number
}

// Boolean is a boolean value's answer; false for any other shape.
func (v *Value) Boolean() bool { return v != nil && v.kind == KindBool && v.boolean }

// Get is the key's value; nil when the object does not carry it.
func (v *Value) Get(key string) *Value {
	if !v.IsObject() {
		return nil
	}
	for i, k := range v.keys {
		if k == key {
			return v.fields[i]
		}
	}
	return nil
}

// Has reports whether the object carries the key (a JSON null counts).
func (v *Value) Has(key string) bool {
	return v.IsObject() && slices.Contains(v.keys, key)
}

// Len is the object's key count.
func (v *Value) Len() int {
	if !v.IsObject() {
		return 0
	}
	return len(v.keys)
}

// Set assigns the key, appending it when the object does not carry it: a JS assignment, where
// an existing key keeps its position. Nothing to do on a value that is not an object.
func (v *Value) Set(key string, value *Value) {
	if !v.IsObject() {
		return
	}
	for i, k := range v.keys {
		if k == key {
			v.fields[i] = value
			return
		}
	}
	v.keys = append(v.keys, key)
	v.fields = append(v.fields, value)
}

// Del removes the key and its value; a key the object does not carry is nothing.
func (v *Value) Del(key string) {
	if !v.IsObject() {
		return
	}
	for i, k := range v.keys {
		if k != key {
			continue
		}
		v.keys = append(v.keys[:i], v.keys[i+1:]...)
		v.fields = append(v.fields[:i], v.fields[i+1:]...)
		return
	}
}

// Append adds items at the end of an array.
func (v *Value) Append(items ...*Value) {
	if !v.IsArray() {
		return
	}
	v.items = append(v.items, items...)
}

// SetItems replaces an array's items.
func (v *Value) SetItems(items []*Value) {
	if !v.IsArray() {
		return
	}
	v.items = items
}

// EncodeLiteral writes v as compact JSON text with every number byte for byte the literal it
// carries: object keys in arrival order, strings through WriteQuoted. A nil field is an absent
// value and drops; a nil array item writes null.
func (v *Value) EncodeLiteral() string {
	var out strings.Builder
	v.encode(&out, true, "", "")
	return out.String()
}

// EncodeIndented writes v as JSON text indented by unit per level, numbers as literals: the
// shape JSON.stringify(v, null, unit) writes, without a trailing newline. Empty objects and
// arrays print as {} and [].
func (v *Value) EncodeIndented(unit string) string {
	var out strings.Builder
	v.encode(&out, true, unit, "")
	return out.String()
}

// Equal reports whether a and b are the same tree: shape, text and order, an object's keys in
// the same positions and its numbers by literal — the way comparing their JSON.stringify texts
// would. Two nils are equal; a nil beside anything else is not.
func Equal(a, b *Value) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.kind != b.kind {
		return false
	}
	switch a.kind {
	case KindObject:
		if len(a.keys) != len(b.keys) {
			return false
		}
		for i, key := range a.keys {
			if key != b.keys[i] || !Equal(a.fields[i], b.fields[i]) {
				return false
			}
		}
		return true
	case KindArray:
		if len(a.items) != len(b.items) {
			return false
		}
		for i, item := range a.items {
			if !Equal(item, b.items[i]) {
				return false
			}
		}
		return true
	case KindString:
		return a.text == b.text
	case KindNumber:
		return a.number == b.number
	case KindBool:
		return a.boolean == b.boolean
	}
	return true // KindNull
}
