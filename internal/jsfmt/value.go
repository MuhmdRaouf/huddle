// Package jsfmt holds an order-keeping JSON value that decodes and re-encodes the way
// JSON.stringify writes it.
package jsfmt

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

// Kind is which JSON shape a Value holds.
type Kind uint8

const (
	KindNull Kind = iota
	KindBool
	KindNumber
	KindString
	KindObject
	KindArray
)

// Value is a JSON value decoded for rewriting: an object keeps its keys in arrival
// order and every scalar keeps its decoded value, so a rewritten value re-encodes to
// the bytes JavaScript's JSON.stringify writes for the same data (a number like 1.0
// comes back out as 1, the double it parses to).
type Value struct {
	kind    Kind
	boolean bool
	number  json.Number
	text    string
	keys    []string
	fields  []*Value
	items   []*Value
}

// DecodeValue reads exactly one JSON value; anything after it, or nothing at all, is an
// error, like JSON.parse.
func DecodeValue(data []byte) (*Value, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	value, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("jsfmt: trailing data after the JSON value")
	}
	return value, nil
}

// decodeValue reads the next value token: an opening delimiter recurses, a scalar rides
// along with its literal.
func decodeValue(dec *json.Decoder) (*Value, error) {
	token, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch token := token.(type) {
	case json.Delim:
		switch token {
		case '{':
			return decodeObject(dec)
		case '[':
			return decodeArray(dec)
		}
		return nil, fmt.Errorf("jsfmt: unexpected %q in value position", rune(token))
	case bool:
		return &Value{kind: KindBool, boolean: token}, nil
	case json.Number:
		return &Value{kind: KindNumber, number: token}, nil
	case string:
		return &Value{kind: KindString, text: token}, nil
	default:
		return &Value{kind: KindNull}, nil
	}
}

// decodeObject reads an object's fields in arrival order.
func decodeObject(dec *json.Decoder) (*Value, error) {
	object := &Value{kind: KindObject}
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("jsfmt: object key is not a string")
		}
		field, err := decodeValue(dec)
		if err != nil {
			return nil, err
		}
		// A duplicate key overwrites the earlier value in place, as JSON.parse does.
		object.Set(key, field)
	}
	if _, err := dec.Token(); err != nil { // the closing brace
		return nil, err
	}
	return object, nil
}

// decodeArray reads an array's items in order.
func decodeArray(dec *json.Decoder) (*Value, error) {
	array := &Value{kind: KindArray}
	for dec.More() {
		item, err := decodeValue(dec)
		if err != nil {
			return nil, err
		}
		array.items = append(array.items, item)
	}
	if _, err := dec.Token(); err != nil { // the closing bracket
		return nil, err
	}
	return array, nil
}

// EncodeJSON writes v as JSON text, byte for byte the way JSON.stringify does: no
// spaces, object keys in arrival order, numbers as String(n), strings with
// JSON.stringify's minimal escaping.
func (v *Value) EncodeJSON() string {
	var out strings.Builder
	v.encode(&out, false, "", "")
	return out.String()
}

// MarshalJSON answers the value's own JSON, so a value inside a struct keeps its keys in
// insertion order through the standard encoder too, never re-sorted the way a Go map's are.
// A nil value is null.
func (v *Value) MarshalJSON() ([]byte, error) {
	if v == nil {
		return []byte("null"), nil
	}
	return []byte(v.EncodeJSON()), nil
}

// encode writes one value's bytes: numbers re-print through float64 when literal is false and
// keep their decoded literal when true; unit is the indent per level and at the current line's
// prefix (both "" for compact output). A nil field is an absent value and drops wherever it
// can; a nil array item writes null, as JSON.stringify writes undefined.
func (v *Value) encode(out *strings.Builder, literal bool, unit, at string) {
	if v == nil {
		out.WriteString("null")
		return
	}
	switch v.kind {
	case KindNull:
		out.WriteString("null")
	case KindBool:
		out.WriteString(strconv.FormatBool(v.boolean))
	case KindNumber:
		if literal {
			out.WriteString(string(v.number))
		} else {
			number, _ := v.number.Float64()
			out.WriteString(jsscalar.JSNumber(number))
		}
	case KindString:
		jsscalar.WriteQuoted(out, v.text)
	case KindObject:
		v.encodeObject(out, literal, unit, at)
	case KindArray:
		v.encodeArray(out, literal, unit, at)
	}
}

// encodeObject writes one object: compact when unit is "", indented by unit per level
// otherwise.
func (v *Value) encodeObject(out *strings.Builder, literal bool, unit, at string) {
	live := 0
	for _, field := range v.fields {
		if field != nil {
			live++
		}
	}
	if unit == "" {
		out.WriteByte('{')
		wrote := 0
		for i, key := range v.keys {
			if v.fields[i] == nil {
				continue
			}
			if wrote > 0 {
				out.WriteByte(',')
			}
			wrote++
			jsscalar.WriteQuoted(out, key)
			out.WriteByte(':')
			v.fields[i].encode(out, literal, unit, at)
		}
		out.WriteByte('}')
		return
	}
	if live == 0 {
		out.WriteString("{}")
		return
	}
	inner := at + unit
	out.WriteString("{\n")
	wrote := 0
	for i, key := range v.keys {
		if v.fields[i] == nil {
			continue
		}
		out.WriteString(inner)
		jsscalar.WriteQuoted(out, key)
		out.WriteString(": ")
		v.fields[i].encode(out, literal, unit, inner)
		wrote++
		if wrote < live {
			out.WriteByte(',')
		}
		out.WriteByte('\n')
	}
	out.WriteString(at)
	out.WriteByte('}')
}

// encodeArray writes one array: compact when unit is "", indented by unit per level otherwise.
func (v *Value) encodeArray(out *strings.Builder, literal bool, unit, at string) {
	if unit == "" {
		out.WriteByte('[')
		for i, item := range v.items {
			if i > 0 {
				out.WriteByte(',')
			}
			if item == nil {
				out.WriteString("null")
				continue
			}
			item.encode(out, literal, unit, at)
		}
		out.WriteByte(']')
		return
	}
	if len(v.items) == 0 {
		out.WriteString("[]")
		return
	}
	inner := at + unit
	out.WriteString("[\n")
	for i, item := range v.items {
		out.WriteString(inner)
		if item == nil {
			out.WriteString("null")
		} else {
			item.encode(out, literal, unit, inner)
		}
		if i < len(v.items)-1 {
			out.WriteByte(',')
		}
		out.WriteByte('\n')
	}
	out.WriteString(at)
	out.WriteByte(']')
}

// Kind reports which JSON shape the value holds.
func (v *Value) Kind() Kind { return v.kind }

// Text is a string value's text; empty for any other shape.
func (v *Value) Text() string { return v.text }

// Keys lists an object's keys in arrival order; nil for any other shape.
func (v *Value) Keys() []string { return v.keys }

// Fields lists an object's field values, one per key in Keys; nil for any other shape.
func (v *Value) Fields() []*Value { return v.fields }

// Items lists an array's items in order; nil for any other shape.
func (v *Value) Items() []*Value { return v.items }

// String builds a string value.
func String(text string) *Value { return &Value{kind: KindString, text: text} }

// Bool builds a boolean value.
func Bool(b bool) *Value { return &Value{kind: KindBool, boolean: b} }

// Number builds a number value that prints the way JavaScript prints f.
func Number(f float64) *Value {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return &Value{kind: KindNull}
	}
	return &Value{kind: KindNumber, number: json.Number(jsscalar.JSNumber(f))}
}

// Object builds an object value whose keys pair with fields positionally.
func Object(keys []string, fields []*Value) *Value {
	return &Value{kind: KindObject, keys: keys, fields: fields}
}

// Array builds an array value.
func Array(items []*Value) *Value { return &Value{kind: KindArray, items: items} }
