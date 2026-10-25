package router

// modelOf reads only what picking a lane needs: the body's top-level `model`, read straight off
// the bytes.

import (
	"bytes"
	"encoding/json"
)

// modelOf is the body's top-level `model` as a string, "" for anything else — a body that is no
// object, a model that is no string, bytes that stop before the field. The bytes after the model
// are never read: one request's parse stops at the key that picks its lane.
func modelOf(body []byte) string {
	dec := json.NewDecoder(bytes.NewReader(body))
	if token, err := dec.Token(); err != nil || token != json.Delim('{') {
		return ""
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return ""
		}
		name, ok := key.(string)
		if !ok {
			return ""
		}
		if name != "model" {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return ""
			}
			continue
		}
		value, err := dec.Token()
		if err != nil {
			return ""
		}
		if text, ok := value.(string); ok {
			return text
		}
		return ""
	}
	return ""
}
