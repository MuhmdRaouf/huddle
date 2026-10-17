// The JSON harness configs: Gemini's and Qwen's settings.json, opencode's and Crush's
// <tool>.json. An edit splices the huddle entry into the bytes and leaves every other byte
// where it was, so install then remove gives the file back byte for byte whatever formatting
// the harness (or the user's hand) wrote it with.
package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// jsonAdapter is one harness whose MCP servers sit in a JSON object under one parent key.
type jsonAdapter struct {
	name   string
	parent string // the object the entry lives under: "mcpServers" or "mcp"
	path   func(Env) string
	detect func(Env) bool
	build  func(Entry) ([]byte, error)
}

func (a jsonAdapter) Name() string              { return a.name }
func (a jsonAdapter) ConfigPath(env Env) string { return a.path(env) }
func (a jsonAdapter) Detect(env Env) bool       { return a.detect(env) }

// Install answers the config with the entry in it. A config that is there but holds a changed
// huddle entry gets ours back: install makes it so.
func (a jsonAdapter) Install(cfg []byte, entry Entry) ([]byte, error) {
	snippet, err := a.snippet(entry)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return a.fresh(snippet), nil
	}
	if err := jsonCheck(cfg); err != nil {
		return nil, err
	}
	parent, err := jsonFind(cfg, 0, a.parent)
	if err != nil {
		return nil, err
	}
	if parent == nil {
		// no table yet: the whole "parent":{"huddle":{...}} member lands in the top object
		full := fmt.Sprintf("%q:{%q:%s}", a.parent, entry.Name, snippet)
		return jsonInsertMember(cfg, 0, full)
	}
	members, _, err := jsonMembers(cfg, parent.value.start)
	if err != nil {
		return nil, fmt.Errorf("the %s member is not a JSON object", a.parent)
	}
	mine := jsonMemberNamed(members, entry.Name)
	if mine == nil {
		return jsonInsertMember(cfg, parent.value.start, fmt.Sprintf("%q:%s", entry.Name, snippet))
	}
	if jsonEqual(cfg[mine.value.start:mine.value.end], snippet) {
		return cfg, nil
	}
	// a huddle entry that is no longer ours: take it out, then write ours in
	next := jsonCutMember(cfg, *mine)
	return jsonInsertMember(next, parent.value.start, fmt.Sprintf("%q:%s", entry.Name, snippet))
}

// Remove answers the config with the entry gone. An entry that no longer matches ours is the
// user's: ErrEntryChanged, and the bytes come back untouched.
func (a jsonAdapter) Remove(cfg []byte, entry Entry) ([]byte, error) {
	if err := jsonCheck(cfg); err != nil {
		return nil, err
	}
	parent, err := jsonFind(cfg, 0, a.parent)
	if err != nil {
		return nil, err
	}
	if parent == nil {
		return cfg, ErrEntryChanged
	}
	members, _, err := jsonMembers(cfg, parent.value.start)
	if err != nil {
		return nil, fmt.Errorf("the %s member is not a JSON object", a.parent)
	}
	mine := jsonMemberNamed(members, entry.Name)
	if mine == nil {
		return cfg, ErrEntryChanged
	}
	snippet, err := a.snippet(entry)
	if err != nil {
		return nil, err
	}
	if !jsonEqual(cfg[mine.value.start:mine.value.end], snippet) {
		return cfg, ErrEntryChanged
	}
	next := jsonCutMember(cfg, *mine)
	// the entry was the table's only member: the table goes with it, so a file install added
	// the table to comes back as it was. The table's own span moved with the cut: find it
	// again in the edited bytes.
	if inner, _, err := jsonMembers(next, parent.value.start); err == nil && len(inner) == 0 {
		if table, err := jsonFind(next, 0, a.parent); err == nil && table != nil {
			next = jsonCutMember(next, *table)
		}
	}
	return next, nil
}

// contains answers whether the config holds a huddle entry under the parent.
func (a jsonAdapter) contains(cfg []byte) bool {
	parent, err := jsonFind(cfg, 0, a.parent)
	if err != nil || parent == nil {
		return false
	}
	members, _, err := jsonMembers(cfg, parent.value.start)
	if err != nil {
		return false
	}
	return jsonMemberNamed(members, "huddle") != nil
}

// emptied answers whether the config is empty of user content: an object whose every member is
// an empty object, which is what a created file looks like once the entry is cut out.
func (a jsonAdapter) emptied(cfg []byte) bool {
	members, _, err := jsonMembers(cfg, 0)
	if err != nil {
		return false
	}
	for _, member := range members {
		inner, _, err := jsonMembers(cfg, member.value.start)
		if err != nil || len(inner) > 0 {
			return false
		}
	}
	return true
}

// snippet answers the entry as the JSON object install writes: compact, keys in the order the
// harness's own examples show.
func (a jsonAdapter) snippet(entry Entry) ([]byte, error) {
	raw, err := a.build(entry)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, errors.New("cannot render the entry")
	}
	return raw, nil
}

// fresh answers the document install creates when the file is not there yet: the table with
// the one entry in it, indented the way a human would read it.
func (a jsonAdapter) fresh(snippet []byte) []byte {
	doc := map[string]json.RawMessage{
		a.parent: json.RawMessage(fmt.Sprintf("{%q:%s}", "huddle", snippet)),
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Appendf(nil, "{%q:{%q:%s}}\n", a.parent, "huddle", snippet)
	}
	return append(out, '\n')
}

// stdioEntry is the {command, args, env} record Gemini CLI and Qwen Code read.
type stdioEntry struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// stdioBuild marshals the shared Gemini/Qwen shape.
func stdioBuild(e Entry) ([]byte, error) {
	env := e.Env
	if env == nil {
		env = map[string]string{}
	}
	return json.Marshal(stdioEntry{Command: e.Command, Args: e.Args, Env: env})
}

// — the byte surgery. The standard decoder walks the document and answers byte spans; the
// edits splice on those spans and touch nothing else.

// span is one byte range of the document.
type span struct{ start, end int }

// jsonMember is one member of a JSON object: its key, the whole member (the key's opening
// quote through the value's last byte) and the value alone.
type jsonMember struct {
	key   string
	span  span
	value span
}

// isJSONSpace answers the four characters JSON calls whitespace.
func isJSONSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// jsonMembers walks the object whose `{` sits at start and answers its members in order and
// the offset of its closing `}`.
func jsonMembers(data []byte, start int) ([]jsonMember, int, error) {
	dec := json.NewDecoder(bytes.NewReader(data[start:]))
	if tok, err := dec.Token(); err != nil {
		return nil, 0, err
	} else if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, 0, errors.New("not a JSON object")
	}
	var members []jsonMember
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, 0, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, 0, errors.New("not a JSON object")
		}
		keyEnd := start + int(dec.InputOffset())
		valueStart, err := jsonValueStart(data, keyEnd)
		if err != nil {
			return nil, 0, err
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, 0, err
		}
		valueEnd := start + int(dec.InputOffset())
		for valueEnd > valueStart && isJSONSpace(data[valueEnd-1]) {
			valueEnd--
		}
		members = append(members, jsonMember{
			key:   key,
			span:  span{jsonKeyStart(data, keyEnd), valueEnd},
			value: span{valueStart, valueEnd},
		})
	}
	if _, err := dec.Token(); err != nil {
		return nil, 0, err
	}
	return members, start + int(dec.InputOffset()) - 1, nil
}

// jsonValueStart answers where the value of the key ending at keyEnd begins: past the colon
// and its whitespace.
func jsonValueStart(data []byte, keyEnd int) (int, error) {
	i := keyEnd
	for i < len(data) && isJSONSpace(data[i]) {
		i++
	}
	if i >= len(data) || data[i] != ':' {
		return 0, errors.New("malformed JSON: no colon after a key")
	}
	i++
	for i < len(data) && isJSONSpace(data[i]) {
		i++
	}
	return i, nil
}

// jsonKeyStart answers where the key ending at keyEnd (just past its closing quote) begins:
// back over the string, past escapes.
func jsonKeyStart(data []byte, keyEnd int) int {
	i := keyEnd - 2 // the closing quote's left neighbour
	for i > 0 {
		if data[i] == '"' {
			slashes := 0
			for i-1-slashes >= 0 && data[i-1-slashes] == '\\' {
				slashes++
			}
			if slashes%2 == 0 {
				return i
			}
		}
		i--
	}
	return 0
}

// jsonFind answers the member key holds in the object that opens at start, or nil.
func jsonFind(data []byte, start int, key string) (*jsonMember, error) {
	members, _, err := jsonMembers(data, start)
	if err != nil {
		return nil, err
	}
	if mine := jsonMemberNamed(members, key); mine != nil {
		return mine, nil
	}
	return nil, nil
}

// jsonMemberNamed answers the member of one key, or nil.
func jsonMemberNamed(members []jsonMember, key string) *jsonMember {
	for i := range members {
		if members[i].key == key {
			return &members[i]
		}
	}
	return nil
}

// jsonInsertMember answers the document with one more member in the object that opens at
// start, comma separated from the members already there. The member lands before the object's
// trailing whitespace, so a later cut of it gives the byte back exactly.
func jsonInsertMember(data []byte, objStart int, snippet string) ([]byte, error) {
	members, closeOff, err := jsonMembers(data, objStart)
	if err != nil {
		return nil, err
	}
	if len(members) > 0 {
		snippet = "," + snippet
	}
	for closeOff > objStart+1 && isJSONSpace(data[closeOff-1]) {
		closeOff--
	}
	out := make([]byte, 0, len(data)+len(snippet))
	out = append(out, data[:closeOff]...)
	out = append(out, snippet...)
	out = append(out, data[closeOff:]...)
	return out, nil
}

// jsonCutMember answers the document with one member gone, its adjoining comma and the
// whitespace around either with it, so the splice is the exact inverse of an insert.
func jsonCutMember(data []byte, member jsonMember) []byte {
	start := member.span.start
	for start > 0 && isJSONSpace(data[start-1]) {
		start--
	}
	end := member.span.end
	if start > 0 && data[start-1] == ',' {
		start--
		for start > 0 && isJSONSpace(data[start-1]) {
			start--
		}
	} else {
		after := end
		for after < len(data) && isJSONSpace(data[after]) {
			after++
		}
		if after < len(data) && data[after] == ',' {
			end = after + 1
		}
	}
	out := make([]byte, 0, len(data)-(end-start))
	out = append(out, data[:start]...)
	out = append(out, data[end:]...)
	return out
}

// jsonCheck parses a config huddle edits: it must be one JSON object.
func jsonCheck(cfg []byte) error {
	if !json.Valid(cfg) {
		return errors.New("not valid JSON")
	}
	dec := json.NewDecoder(bytes.NewReader(cfg))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return errors.New("not a JSON object")
	}
	return nil
}

// jsonEqual answers whether two JSON texts hold the same value.
func jsonEqual(a, b []byte) bool {
	left, err := jsfmt.DecodeValue(a)
	if err != nil {
		return false
	}
	right, err := jsfmt.DecodeValue(b)
	if err != nil {
		return false
	}
	return left.EncodeJSON() == right.EncodeJSON()
}
