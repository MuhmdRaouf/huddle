package cli

// The identity walk's quiet edges: a settings file that cannot be read stops the walk, a world
// without a home names none, and the JSON field readers answer their type's zero value.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// TestFindConfigStopsAtAnUnreadableSettingsFile pins the torn file: a directory named
// huddle.json stats like the settings and reads like nothing, and the walk answers no config
// rather than stepping over it.
func TestFindConfigStopsAtAnUnreadableSettingsFile(t *testing.T) {
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, FileRel), 0o700); err != nil {
		t.Fatal(err)
	}
	w := world(t, proj, map[string]string{"CLAUDE_PROJECT_DIR": proj})
	if found := FindConfig(context.Background(), w, proj); found != nil {
		t.Errorf("an unreadable settings file = %+v", found)
	}
	if _, ok := readConfig(filepath.Join(proj, FileRel)); ok {
		t.Error("readConfig read a directory")
	}
}

// TestHomeOfAndTheHomeNameNoneWithoutAHome pins the two resolvers over a world that names no
// home at all: no path, and no home for the entries that lay one out.
func TestHomeOfAndTheHomeNameNoneWithoutAHome(t *testing.T) {
	w := world(t, t.TempDir(), map[string]string{"HUDDLE_HOME": "", "HOME": ""})
	if got := HomeOf(context.Background(), w); got != "" {
		t.Errorf("HomeOf = %q", got)
	}
	if h, ok := TheHome(context.Background(), w); ok || h.Root != "" {
		t.Errorf("TheHome = (%q, %v)", h.Root, ok)
	}
}

// TestSavePortSaysNothingWhenTheConfigCannotBeWritten pins the refused save: a config path that
// is a directory answers no file name.
func TestSavePortSaysNothingWhenTheConfigCannotBeWritten(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "config.toml"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := SavePort(context.Background(), world(t, t.TempDir(), nil), 18860, true, root); got != "" {
		t.Errorf("SavePort over a blocked config = %q", got)
	}
	if _, ok := SavedPort(root); ok {
		t.Error("SavedPort read a blocked config")
	}
}

// TestListeningPortReadsNothingWithoutASocket pins the port reader over a process that holds no
// listening socket: zero, and no guess.
func TestListeningPortReadsNothingWithoutASocket(t *testing.T) {
	w := &World{Proc: Proc{ListenOf: func(int) (int, bool) { return 0, false }}}
	if got := ListeningPort(context.Background(), w, 1); got != 0 {
		t.Errorf("ListeningPort = %d", got)
	}
}

// TestHomeURLSaysNoneWhenNothingRunsHere pins the home's url: no saved port and no running
// server means no url; a running one is saved as a choice the owner made.
func TestHomeURLSaysNoneWhenNothingRunsHere(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	w := &World{Dir: root, Getenv: func(string) string { return "" }}
	if url, source, ok := homeURL(ctx, w, root); ok || url != "" || source != "" {
		t.Errorf("homeURL with nothing running = (%q, %q, %v)", url, source, ok)
	}
	running := cliWorld(t, root, t.TempDir(), map[string]string{"HUDDLE_TOKEN": ""})
	if err := os.MkdirAll(filepath.Join(root, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "run", "huddle.pid"), "4242")
	running.Proc = Proc{
		Command:  func(pid int) (string, bool) { return "huddle serve", true },
		ListenOf: func(pid int) (int, bool) { return 4321, true },
	}
	url, source, ok := homeURL(ctx, running, root)
	if !ok || url != "http://127.0.0.1:4321" || source != "running" {
		t.Errorf("homeURL over a running server = (%q, %q, %v)", url, source, ok)
	}
	choice, has := SavedPort(root)
	if !has || choice.Port != 4321 || choice.Auto {
		t.Errorf("the running port was not saved: %+v (%v)", choice, has)
	}
}

// TestListFieldJoinsArraysScalarsAndEverythingElse pins the list reader: an array joins its
// items (whatever each holds), a string stays as it is, anything else prints as its JSON.
func TestListFieldJoinsArraysScalarsAndEverythingElse(t *testing.T) {
	value, err := jsfmt.DecodeValue([]byte(`{"a": ["x", 5, false], "b": "y,z", "c": {"d": 1}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := listField(value, "a"); got != "x,5,false" {
		t.Errorf("a mixed array = %q", got)
	}
	if got := listField(value, "b"); got != "y,z" {
		t.Errorf("a string = %q", got)
	}
	if got := listField(value, "c"); got != `{"d":1}` {
		t.Errorf("an object = %q", got)
	}
	if got := listField(value, "missing"); got != "" {
		t.Errorf("a missing key = %q", got)
	}
}

// TestNumberValueOfAnythingUnparsableIsZero pins the number reader over a value that is not a
// number: zero, never a panic.
func TestNumberValueOfAnythingUnparsableIsZero(t *testing.T) {
	if got := numberValue(jsfmt.String("tomorrow")); got != 0 {
		t.Errorf("numberValue(string) = %v", got)
	}
}

// TestIdentifyKeepsTheFileWhenTheCredentialNamesNoHome pins Identify over a settings file whose
// keys the credential does not name: the file's own settings come through.
func TestIdentifyKeepsTheFileWhenTheCredentialNamesNoHome(t *testing.T) {
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, ".agents", "huddle"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(proj, FileRel), `{"channel": "file-c", "as": "file-a", "url": "http://127.0.0.1:4321"}`)
	w := world(t, proj, map[string]string{"CLAUDE_PROJECT_DIR": proj, "HUDDLE_SESSIONS_DIR": filepath.Join(t.TempDir(), "sessions")})
	id := Identify(context.Background(), w, "")
	if id.Channel != "file-c" || id.As != "file-a" || id.URL != "http://127.0.0.1:4321" || id.Source != "config" {
		t.Errorf("Identify over a settings file = %+v", id)
	}
}
