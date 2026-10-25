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

// jsonAdapter is one harness whose MCP servers sit in a JSON object under one parent key, or
// under one child key inside it.
type jsonAdapter struct {
	name   string
	parent string // the object the entry lives under: "mcpServers" or "mcp"
	child  string // the object inside parent the entry lives under ("servers"), "" for none
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
	table, missing, err := jsonFindTable(cfg, a.tablePath())
	if err != nil {
		return nil, err
	}
	if len(missing) > 0 {
		// no table yet: the missing levels land around the entry, "parent":{"huddle":{...}}
		return jsonInsertMember(cfg, table, jsonNested(missing, fmt.Sprintf("%q:%s", entry.Name, snippet)))
	}
	members, _, err := jsonMembers(cfg, table)
	if err != nil {
		return nil, err
	}
	mine := jsonMemberNamed(members, entry.Name)
	if mine == nil {
		return jsonInsertMember(cfg, table, fmt.Sprintf("%q:%s", entry.Name, snippet))
	}
	if jsonEqual(cfg[mine.value.start:mine.value.end], snippet) {
		return cfg, nil
	}
	// a huddle entry that is no longer ours: take it out, then write ours in
	next := jsonCutMember(cfg, *mine)
	return jsonInsertMember(next, table, fmt.Sprintf("%q:%s", entry.Name, snippet))
}

// Remove answers the config with the entry gone. An entry that no longer matches ours is the
// user's: ErrEntryChanged, and the bytes come back untouched.
func (a jsonAdapter) Remove(cfg []byte, entry Entry) ([]byte, error) {
	if err := jsonCheck(cfg); err != nil {
		return nil, err
	}
	table, missing, err := jsonFindTable(cfg, a.tablePath())
	if err != nil {
		return nil, err
	}
	if len(missing) > 0 {
		return cfg, ErrEntryChanged
	}
	members, _, err := jsonMembers(cfg, table)
	if err != nil {
		return nil, err
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
	// the entry was the table's only member: the table goes with it, and each level above it
	// the cut leaves empty, so a file install added the table to comes back as it was
	return jsonCutEmptied(jsonCutMember(cfg, *mine), a.tablePath()), nil
}

// tablePath answers the keys from the top object down to the table the entries sit in.
func (a jsonAdapter) tablePath() []string {
	if a.child == "" {
		return []string{a.parent}
	}
	return []string{a.parent, a.child}
}

// contains answers whether the config holds a huddle entry in the table.
func (a jsonAdapter) contains(cfg []byte) bool {
	table, missing, err := jsonFindTable(cfg, a.tablePath())
	if err != nil || len(missing) > 0 {
		return false
	}
	members, _, err := jsonMembers(cfg, table)
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
	compact := "{" + jsonNested(a.tablePath(), fmt.Sprintf("%q:%s", "huddle", snippet)) + "}"
	var out bytes.Buffer
	if err := json.Indent(&out, []byte(compact), "", "  "); err != nil {
		return []byte(compact + "\n")
	}
	return append(out.Bytes(), '\n')
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

// jsonFindTable walks the keys down from the top object: the start of the deepest object it
// reaches and the keys still missing below it. A key that names no object is an error.
func jsonFindTable(data []byte, path []string) (int, []string, error) {
	start := 0
	for i, key := range path {
		member, err := jsonFind(data, start, key)
		if err != nil {
			return 0, nil, err
		}
		if member == nil {
			return start, path[i:], nil
		}
		if _, _, err := jsonMembers(data, member.value.start); err != nil {
			return 0, nil, fmt.Errorf("the %s member is not a JSON object", key)
		}
		start = member.value.start
	}
	return start, nil, nil
}

// jsonNested answers one member wrapped in an object per key, outermost first:
// ["mcp","servers"] around "huddle":{...} is "mcp":{"servers":{"huddle":{...}}}.
func jsonNested(keys []string, member string) string {
	for i := len(keys) - 1; i >= 0; i-- {
		member = fmt.Sprintf("%q:{%s}", keys[i], member)
	}
	return member
}

// jsonCutEmptied answers the document with every object along the path that holds nothing
// cut out, deepest first; an object that still holds a member stops the cut.
func jsonCutEmptied(data []byte, path []string) []byte {
	for depth := len(path); depth > 0; depth-- {
		table, missing, err := jsonFindTable(data, path[:depth])
		if err != nil || len(missing) > 0 {
			return data
		}
		if inner, _, err := jsonMembers(data, table); err != nil || len(inner) > 0 {
			return data
		}
		holder, _, err := jsonFindTable(data, path[:depth-1])
		if err != nil {
			return data
		}
		member, err := jsonFind(data, holder, path[depth-1])
		if err != nil || member == nil {
			return data
		}
		data = jsonCutMember(data, *member)
	}
	return data
}

// jsonInsertAt answers the document with one more member or item spliced in before the
// container's trailing whitespace, comma separated when count is past zero, so a later cut of
// it gives the byte back exactly. The caller's scanner answers start (the container's opening
// byte), closeOff (its closing byte) and count (what the container already holds).
func jsonInsertAt(data []byte, start, closeOff, count int, snippet string) []byte {
	if count > 0 {
		snippet = "," + snippet
	}
	for closeOff > start+1 && isJSONSpace(data[closeOff-1]) {
		closeOff--
	}
	out := make([]byte, 0, len(data)+len(snippet))
	out = append(out, data[:closeOff]...)
	out = append(out, snippet...)
	out = append(out, data[closeOff:]...)
	return out
}

// jsonInsertMember answers the document with one more member in the object that opens at
// start, comma separated from the members already there. The member lands before the object's
// trailing whitespace, so a later cut of it gives the byte back exactly.
func jsonInsertMember(data []byte, objStart int, snippet string) ([]byte, error) {
	members, closeOff, err := jsonMembers(data, objStart)
	if err != nil {
		return nil, err
	}
	return jsonInsertAt(data, objStart, closeOff, len(members), snippet), nil
}

// jsonReplaceValue answers the document with one member's value swapped for another: the
// member's key and the whitespace on either side stay byte for byte, so a swap back with the
// original value is the exact inverse.
func jsonReplaceValue(data []byte, member jsonMember, value string) []byte {
	out := make([]byte, 0, len(data)+len(value)-(member.value.end-member.value.start))
	out = append(out, data[:member.value.start]...)
	out = append(out, value...)
	out = append(out, data[member.value.end:]...)
	return out
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

// — the provider edit: opencode's and pi's route to the router's gateway.

// providerEdit splices huddle's anthropic provider record into one parent object of a JSON
// config (opencode's provider, pi's providers) and back out, every other byte where it was.
// The record names the gateway as the provider's base URL and the harness in a header.
type providerEdit struct {
	ledger string // the ledger record's name
	parent string // the object the record lives under
	path   func(Env) string
	build  func(Entry) ([]byte, error)
	ours   func(raw []byte, entry Entry) bool // the record is one huddle wrote, whatever gateway it names
}

// ConfigPath answers the file the record goes into.
func (p providerEdit) ConfigPath(env Env) string { return p.path(env) }

func (p providerEdit) ledgerName() string { return p.ledger }

// Install answers the config with huddle's anthropic provider in it: added when there is none,
// rewritten when it is ours on another gateway. An anthropic provider of the user's own is
// ErrProviderTaken, and nothing is written. A file that is not there yet gets the record alone,
// indented the way a human would read it.
func (p providerEdit) Install(cfg []byte, entry Entry) ([]byte, error) {
	want, err := p.build(entry)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return p.fresh(want)
	}
	if err := jsonCheck(cfg); err != nil {
		return nil, err
	}
	parent, err := jsonFind(cfg, 0, p.parent)
	if err != nil {
		return nil, err
	}
	if parent == nil {
		return jsonInsertMember(cfg, 0, fmt.Sprintf(`%q:{"anthropic":%s}`, p.parent, want))
	}
	members, _, err := jsonMembers(cfg, parent.value.start)
	if err != nil {
		return nil, fmt.Errorf("the %s member is not a JSON object", p.parent)
	}
	mine := jsonMemberNamed(members, "anthropic")
	switch {
	case mine == nil:
		return jsonInsertMember(cfg, parent.value.start, `"anthropic":`+string(want))
	case jsonEqual(cfg[mine.value.start:mine.value.end], want):
		return cfg, nil
	case p.ours(cfg[mine.value.start:mine.value.end], entry):
		return jsonReplaceValue(cfg, *mine, string(want)), nil
	}
	return nil, ErrProviderTaken
}

// Remove answers the config with huddle's anthropic provider gone, and the parent object with
// it when nothing else is left in it. A provider that is not ours is ErrEntryChanged, and the
// bytes come back untouched.
func (p providerEdit) Remove(cfg []byte, entry Entry) ([]byte, error) {
	if err := jsonCheck(cfg); err != nil {
		return nil, err
	}
	parent, err := jsonFind(cfg, 0, p.parent)
	if err != nil {
		return nil, err
	}
	if parent == nil {
		return cfg, ErrEntryChanged
	}
	members, _, err := jsonMembers(cfg, parent.value.start)
	if err != nil {
		return nil, fmt.Errorf("the %s member is not a JSON object", p.parent)
	}
	want, err := p.build(entry)
	if err != nil {
		return nil, err
	}
	mine := jsonMemberNamed(members, "anthropic")
	if mine == nil || !jsonEqual(cfg[mine.value.start:mine.value.end], want) {
		return cfg, ErrEntryChanged
	}
	next := jsonCutMember(cfg, *mine)
	if inner, _, err := jsonMembers(next, parent.value.start); err == nil && len(inner) == 0 {
		if table, err := jsonFind(next, 0, p.parent); err == nil && table != nil {
			next = jsonCutMember(next, *table)
		}
	}
	return next, nil
}

// fresh answers the document install creates: the parent with the one record in it.
func (p providerEdit) fresh(record []byte) ([]byte, error) {
	doc := map[string]map[string]json.RawMessage{p.parent: {"anthropic": record}}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// jsonEmptyObject answers whether a document is an object with no members: what a file the
// provider edit created looks like once the record is cut out.
func jsonEmptyObject(cfg []byte) bool {
	members, _, err := jsonMembers(cfg, 0)
	return err == nil && len(members) == 0
}
