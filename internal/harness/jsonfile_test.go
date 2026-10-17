package harness

import (
	"errors"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// The JSON adapters' edges, through the bytes they edit: an entry that drifted is replaced
// in place, a table that is not an object is refused, a remove without an entry of ours
// refuses with the bytes untouched, and an entry the adapter cannot render stops the edit
// before anything is written.

func TestJSONInstallReplacesADeviatingEntry(t *testing.T) {
	entry := NewEntry("gemini", "/users/home/bin/huddle")
	before := []byte("{\"mcpServers\":{\"huddle\":{\"command\":\"old\"},\"other\":{\"command\":\"o\"}}}")
	out, err := gemini.Install(before, entry)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	value, err := jsfmt.DecodeValue(out)
	if err != nil {
		t.Fatalf("the config does not parse: %v\n%s", err, out)
	}
	servers, ok := field(value, "mcpServers")
	if !ok || servers.Kind() != jsfmt.KindObject || len(servers.Keys()) != 2 {
		t.Fatalf("mcpServers = %s, want huddle and other", value.EncodeJSON())
	}
	huddle, ok := field(servers, "huddle")
	if !ok || huddle.Kind() != jsfmt.KindObject {
		t.Fatalf("huddle = %v, want the entry's object", servers)
	}
	if got := textField(huddle, "command"); got != entry.Command {
		t.Errorf("huddle's command = %q, want %q", got, entry.Command)
	}
	other, ok := field(servers, "other")
	if !ok || other.Kind() != jsfmt.KindObject {
		t.Fatalf("other = %v, want the other server's object", servers)
	}
	if got := textField(other, "command"); got != "o" {
		t.Errorf("other's command = %q, want it untouched", got)
	}
}

// A table that is not an object is refused on the way in and on the way out.
func TestJSONRefusesATableThatIsNotAnObject(t *testing.T) {
	entry := NewEntry("gemini", "/bin/huddle")
	cfg := []byte(`{"mcpServers":"nope"}`)
	if _, err := gemini.Install(cfg, entry); err == nil || !strings.Contains(err.Error(), "mcpServers") {
		t.Errorf("install = %v, want the mcpServers member refused", err)
	}
	if _, err := gemini.Remove(cfg, entry); err == nil || !strings.Contains(err.Error(), "mcpServers") {
		t.Errorf("remove = %v, want the mcpServers member refused", err)
	}
}

// A remove of a config that holds no entry of ours refuses, bytes as they were.
func TestJSONRemoveWithoutAnEntryRefuses(t *testing.T) {
	entry := NewEntry("gemini", "/bin/huddle")
	cases := []struct{ name, body string }{
		{"no table at all", `{"theme":"dark"}`},
		{"a table without huddle", `{"mcpServers":{"other":{}}}`},
	}
	for _, tc := range cases {
		out, err := gemini.Remove([]byte(tc.body), entry)
		if !errors.Is(err, ErrEntryChanged) {
			t.Errorf("%s: remove = %v, want ErrEntryChanged", tc.name, err)
		}
		if string(out) != tc.body {
			t.Errorf("%s: remove touched the bytes: %q", tc.name, out)
		}
	}
}

// An entry the adapter cannot render stops the edit before anything is written, on the
// way in and on the way out.
func TestJSONInstallAndRemoveRefuseAnEntryThatCannotRender(t *testing.T) {
	buildErr := errors.New("cannot build")
	failing := jsonAdapter{name: "gemini", parent: "mcpServers", build: func(Entry) ([]byte, error) {
		return nil, buildErr
	}}
	entry := NewEntry("gemini", "/bin/huddle")
	if _, err := failing.Install(nil, entry); !errors.Is(err, buildErr) {
		t.Errorf("install = %v, want the build error", err)
	}
	nilBuild := jsonAdapter{name: "gemini", parent: "mcpServers", build: func(Entry) ([]byte, error) {
		return nil, nil
	}}
	if _, err := nilBuild.Install(nil, entry); err == nil || !strings.Contains(err.Error(), "cannot render") {
		t.Errorf("install = %v, want the unrenderable snippet refused", err)
	}
	withEntry := []byte(`{"mcpServers":{"huddle":{}}}`)
	if _, err := nilBuild.Remove(withEntry, entry); err == nil || !strings.Contains(err.Error(), "cannot render") {
		t.Errorf("remove = %v, want the unrenderable snippet refused", err)
	}
}

// contains and emptied judge honest bytes: garbage and a table that is not an object are
// not ours, and only a file of empty objects is empty.
func TestContainsAndEmptiedJudgeHonestBytes(t *testing.T) {
	cases := []struct {
		name     string
		cfg      string
		contains bool
		emptied  bool
	}{
		{"garbage", "not json", false, false},
		{"a table that is not an object", `{"mcpServers":5}`, false, false},
		{"a member that is not an object", `{"a":5}`, false, false},
		{"an object with content", `{"a":{"b":1}}`, false, false},
		{"huddle's entry", `{"mcpServers":{"huddle":{}}}`, true, false},
		{"empty objects only", `{"a":{}}`, false, true},
	}
	for _, tc := range cases {
		cfg := []byte(tc.cfg)
		if got := gemini.contains(cfg); got != tc.contains {
			t.Errorf("%s: contains = %v, want %v", tc.name, got, tc.contains)
		}
		if got := gemini.emptied(cfg); got != tc.emptied {
			t.Errorf("%s: emptied = %v, want %v", tc.name, got, tc.emptied)
		}
	}
}

// A snippet a MarshalIndent cannot shape still lays a file down: the flat fallback.
func TestFreshFallsBackWhenTheSnippetCannotIndent(t *testing.T) {
	out := gemini.fresh([]byte("{broken"))
	if !strings.Contains(string(out), "mcpServers") || !strings.Contains(string(out), "{broken") {
		t.Errorf("fresh = %s, want the fallback document naming the table and the snippet", out)
	}
}

// The spans the byte surgery reads: members in order and where the object closes, an
// escaped quote inside a key never ends it early, whitespace before a colon is walked
// over, and half-written bytes are refused.
func TestJSONMembersWalksTheSpans(t *testing.T) {
	cfg := []byte(`{"a":1,"b":{"c":2}}`)
	members, closeOff, err := jsonMembers(cfg, 0)
	if err != nil {
		t.Fatalf("jsonMembers: %v", err)
	}
	if len(members) != 2 || members[0].key != "a" || members[1].key != "b" {
		t.Fatalf("members = %+v, want a then b", members)
	}
	if cfg[closeOff] != '}' {
		t.Errorf("closeOff = %d, want the closing brace", closeOff)
	}

	escaped, _, err := jsonMembers([]byte(`{"a\"b":1}`), 0)
	if err != nil {
		t.Fatalf("jsonMembers with an escaped quote in a key: %v", err)
	}
	if escaped[0].key != `a"b` {
		t.Errorf("key = %q, want the escaped quote inside it", escaped[0].key)
	}

	spaced, _, err := jsonMembers([]byte("{\"a\" : 1}"), 0)
	if err != nil {
		t.Fatalf("jsonMembers with space before the colon: %v", err)
	}
	if spaced[0].key != "a" {
		t.Errorf("key = %q, want a", spaced[0].key)
	}

	if _, _, err := jsonMembers([]byte(`[1,2]`), 0); err == nil {
		t.Error("jsonMembers took an array for an object")
	}
	if _, _, err := jsonMembers([]byte(`{oops`), 0); err == nil {
		t.Error("jsonMembers took a half-written object")
	}
}

// The span helpers answer where a value begins and where a key ends, and say so when the
// bytes do not hold together.
func TestJSONSpanHelpersRefuseBytesThatDoNotHold(t *testing.T) {
	if _, err := jsonValueStart([]byte(`"a" x`), 3); err == nil {
		t.Error("jsonValueStart took x for a colon")
	}
	if start := jsonKeyStart([]byte(`abc`), 3); start != 0 {
		t.Errorf("jsonKeyStart = %d, want 0 for a key with no opening quote", start)
	}
	if _, err := jsonFind([]byte("not json"), 0, "x"); err == nil {
		t.Error("jsonFind read a config that does not parse")
	}
}
