package ledger

import (
	"bytes"
	"encoding/json"
	"io"
)

// splitBody cuts a request or response body into contiguous spans whose concatenation is
// exactly the body. A JSON object is cut at its top-level members: one span from the start
// up to the first member, one span per member. A member whose value is an array is cut
// further into its key-and-opening-bracket span plus one span per element, so a resent
// conversation's history lands span for span on earlier requests' spans. Separators and
// whitespace ride along: each span starts where the previous one ends, and nothing is lost
// or reordered. A body that is not a JSON object, or one that fails to parse, stays one
// span. An empty body has no spans.
func splitBody(body []byte) [][]byte {
	if len(body) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	token, err := dec.Token()
	if err != nil {
		return [][]byte{body}
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return [][]byte{body}
	}
	cuts, ok := memberCuts(dec)
	if !ok {
		return [][]byte{body}
	}
	// The closing brace, then nothing: any other trailing byte means the body is not one
	// clean object and the whole body stays one span.
	if _, err := dec.Token(); err != nil {
		return [][]byte{body}
	}
	if _, err := dec.Token(); err != io.EOF {
		return [][]byte{body}
	}
	cuts = append(cuts, len(body))
	spans := make([][]byte, 0, len(cuts)-1)
	start := 0
	for _, cut := range cuts {
		spans = append(spans, body[start:cut])
		start = cut
	}
	return spans
}

// memberCuts walks the object's members, answering the byte offset each member starts at;
// the caller's first span runs from the body's start up to the first member. A member whose
// value is an array adds the offset of each element. False on any parse failure.
func memberCuts(dec *json.Decoder) ([]int, bool) {
	var cuts []int
	for dec.More() {
		cuts = append(cuts, int(dec.InputOffset()))
		if _, err := dec.Token(); err != nil {
			return nil, false
		}
		if !elementCuts(dec, &cuts) {
			return nil, false
		}
	}
	return cuts, true
}

// elementCuts consumes one member's value, adding the offset of each element of an array
// value; nested values are consumed without further cuts.
func elementCuts(dec *json.Decoder, cuts *[]int) bool {
	token, err := dec.Token()
	if err != nil {
		return false
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '[' {
		return true
	}
	for dec.More() {
		*cuts = append(*cuts, int(dec.InputOffset()))
		if !skipValue(dec) {
			return false
		}
	}
	if _, err := dec.Token(); err != nil {
		return false
	}
	return true
}

// skipValue consumes one complete value, arrays and objects included.
func skipValue(dec *json.Decoder) bool {
	token, err := dec.Token()
	if err != nil {
		return false
	}
	switch delim, ok := token.(json.Delim); {
	case ok && delim == '[':
		for dec.More() {
			if !skipValue(dec) {
				return false
			}
		}
		if _, err := dec.Token(); err != nil {
			return false
		}
	case ok && delim == '{':
		for dec.More() {
			if _, err := dec.Token(); err != nil {
				return false
			}
			if !skipValue(dec) {
				return false
			}
		}
		if _, err := dec.Token(); err != nil {
			return false
		}
	}
	return true
}
