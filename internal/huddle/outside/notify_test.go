package outside

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

// fakeClock is a clock the tests move by hand.
type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

// logLines is the notification log, one JSON line per notification.
func logLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		t.Fatalf("read %s: %v", path, err)
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func TestNotifier(t *testing.T) {
	t.Run("the notifier: once per kind and subject in the window, off when switched off, no secrets in the text", func(t *testing.T) {
		dir := t.TempDir()
		logFile := filepath.Join(dir, "log")
		t.Setenv("HUDDLE_NOTIFY_LOG", logFile)
		t.Setenv("HUDDLE_NOTIFY_WINDOW_MS", "")
		n := NewNotifier(dir)
		if !n.Enabled() {
			t.Fatal("Enabled() = false, want true by default with no settings file")
		}
		if !n.Send("ask", "c/a", "Huddle · c", "a asks you a question") {
			t.Error("first ask c/a: want true")
		}
		if n.Send("ask", "c/a", "Huddle · c", "a asks you a question") {
			t.Error("same kind+subject: rate-limited, want false")
		}
		if !n.Send("ask", "c/b", "Huddle · c", "b asks you a question") {
			t.Error("another subject: want true")
		}
		if !n.Send("pause", "c/a", "Huddle · c", "a was paused") {
			t.Error("another kind: want true")
		}
		n.Set(false)
		body, err := os.ReadFile(filepath.Join(dir, "settings.json"))
		if err != nil {
			t.Fatalf("read settings.json: %v", err)
		}
		var saved struct {
			Notify bool `json:"notify"`
		}
		if err := json.Unmarshal(body, &saved); err != nil {
			t.Fatalf("settings.json is not JSON: %v", err)
		}
		if saved.Notify {
			t.Error("settings.json notify = true, want false")
		}
		if want := "{\n  \"notify\": false\n}\n"; string(body) != want {
			t.Errorf("settings.json = %q, want %q", body, want)
		}
		if n.Send("blocked", "c/t1", "Huddle · c", "t1 blocked") {
			t.Error("switched off: want false")
		}
		if NewNotifier(dir).Enabled() {
			t.Error("the switch is the file: another process reads it")
		}
		n.Set(true)
		if !n.Send("blocked", "c/t1", "Huddle · c", "t1 blocked") {
			t.Error("switched on again: want true")
		}
		if lines := logLines(t, logFile); len(lines) != 4 {
			t.Errorf("notification log holds %d lines, want 4: %v", len(lines), lines)
		}
		secret := "ghp_" + strings.Repeat("A", 36)
		long := strings.Repeat("x", 50)
		c := Clean("token=abc123 "+secret+" key: hunter2 "+long+" ok\u0007bell", 140)
		for _, leak := range []string{"abc123", secret, "hunter2", long, "\u0007"} {
			if strings.Contains(c, leak) {
				t.Errorf("Clean leaks %q: %q", leak, c)
			}
		}
		if want := "token=… … key: … … ok bell"; c != want {
			t.Errorf("Clean = %q, want %q", c, want)
		}
		if got := jsscalar.UTF16Len(Clean(strings.Repeat("y", 500), 140)); got > 140 {
			t.Errorf("Clean of 500 chars is %d long, want at most 140", got)
		}
	})

	t.Run("each notification is one JSON line", func(t *testing.T) {
		dir := t.TempDir()
		logFile := filepath.Join(dir, "log")
		t.Setenv("HUDDLE_NOTIFY_LOG", logFile)
		clock := &fakeClock{now: time.Date(2026, 10, 15, 3, 13, 50, 500000000, time.UTC)}
		n := NewNotifier(dir)
		n.Now = clock.Now
		if !n.Send("ask", "shop/api", "Huddle · shop", "api asks you a question") {
			t.Fatal("Send = false, want true")
		}
		want := `{"at":"2026-10-15T03:13:50.500Z","kind":"ask","subject":"shop/api","title":"Huddle · shop","body":"api asks you a question"}` + "\n"
		got, err := os.ReadFile(logFile)
		if err != nil {
			t.Fatalf("read the log: %v", err)
		}
		if string(got) != want {
			t.Errorf("log line = %q, want %q", got, want)
		}
	})

	t.Run("the settings file keeps what is already there, in place", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("HUDDLE_NOTIFY_LOG", "")
		settings := filepath.Join(dir, "settings.json")
		if err := os.WriteFile(settings, []byte(`{"other": true,"notify": true}`), 0o600); err != nil {
			t.Fatal(err)
		}
		n := NewNotifier(dir)
		n.Set(false)
		want := "{\n  \"other\": true,\n  \"notify\": false\n}\n"
		if got, _ := os.ReadFile(settings); string(got) != want {
			t.Errorf("settings.json = %q, want %q", got, want)
		}
		n.Set(true)
		want = "{\n  \"other\": true,\n  \"notify\": true\n}\n"
		if got, _ := os.ReadFile(settings); string(got) != want {
			t.Errorf("settings.json = %q, want %q", got, want)
		}
		// a nested value is laid out the way the dashboard writes it back
		if err := os.WriteFile(settings, []byte(`{"a": [1,  2]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		n.Set(false)
		want = "{\n  \"a\": [\n    1,\n    2\n  ],\n  \"notify\": false\n}\n"
		if got, _ := os.ReadFile(settings); string(got) != want {
			t.Errorf("settings.json = %q, want %q", got, want)
		}
	})

	t.Run("the enabled switch is read at most every 2 s", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("HUDDLE_NOTIFY_LOG", "")
		t.Setenv("HUDDLE_NOTIFY", "")
		clock := &fakeClock{now: time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)}
		n := NewNotifier(dir)
		n.Now = clock.Now
		if !n.Enabled() {
			t.Fatal("Enabled() = false, want true with no settings file")
		}
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"notify": false}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if !n.Enabled() {
			t.Error("Enabled() = false from a cache that is under 2 s old, want the cached true")
		}
		clock.advance(2 * time.Second)
		if n.Enabled() {
			t.Error("Enabled() = true after the cache expired, want false")
		}
		t.Setenv("HUDDLE_NOTIFY", "0")
		if n.Enabled() {
			t.Error("HUDDLE_NOTIFY=0 forces notifications off whatever the file says")
		}
	})

	t.Run("state names the log, the program here, or nothing", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("HUDDLE_NOTIFY", "")
		t.Setenv("HUDDLE_NOTIFY_LOG", "/a/log")
		st := NewNotifier(dir).State()
		if st.Notifier == nil || *st.Notifier != "log" {
			t.Errorf("state.Notifier = %v, want log", st.Notifier)
		}
		if st.ForcedOff {
			t.Error("state.ForcedOff = true, want false")
		}
		t.Setenv("HUDDLE_NOTIFY_LOG", "")
		t.Setenv("HUDDLE_NOTIFY", "0")
		st = NewNotifier(dir).State()
		if !st.ForcedOff || st.Notify {
			t.Errorf("state = %+v, want forced off", st)
		}
		if want := NotifierProgram(); (st.Notifier == nil) != (want == "") || (st.Notifier != nil && *st.Notifier != want) {
			t.Errorf("state.Notifier = %v, want %q", st.Notifier, want)
		}
		t.Setenv("HUDDLE_NOTIFY", "")
		if st := NewNotifier(dir).State(); st.ForcedOff || !st.Notify {
			t.Errorf("state = %+v, want on and not forced", st)
		}
	})

	t.Run("the rate window comes from HUDDLE_NOTIFY_WINDOW_MS", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("HUDDLE_NOTIFY_LOG", filepath.Join(dir, "log"))
		t.Setenv("HUDDLE_NOTIFY_WINDOW_MS", "1000")
		clock := &fakeClock{now: time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)}
		n := NewNotifier(dir)
		n.Now = clock.Now
		if !n.Send("ask", "c/a", "t", "b") {
			t.Fatal("first send: want true")
		}
		clock.advance(500 * time.Millisecond)
		if n.Send("ask", "c/a", "t", "b") {
			t.Error("500 ms into a 1 s window: rate-limited, want false")
		}
		clock.advance(600 * time.Millisecond)
		if !n.Send("ask", "c/a", "t", "b") {
			t.Error("1.1 s into a 1 s window: want true")
		}
	})

	t.Run("the seen table is pruned past 2000 subjects", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("HUDDLE_NOTIFY_LOG", filepath.Join(dir, "log"))
		t.Setenv("HUDDLE_NOTIFY_WINDOW_MS", "1")
		clock := &fakeClock{now: time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)}
		n := NewNotifier(dir)
		n.Now = clock.Now
		for i := 0; i < 2100; i++ {
			n.Send("ask", "c/s"+strconv.Itoa(i), "t", "b")
			clock.advance(2 * time.Millisecond)
		}
		n.mu.Lock()
		size := len(n.last)
		n.mu.Unlock()
		if size >= 2000 {
			t.Errorf("the seen table holds %d subjects after 2100 sends, want it pruned", size)
		}
	})

	t.Run("the text goes to the program as arguments, never into a script", func(t *testing.T) {
		how := NotifierProgram()
		if how == "" {
			t.Skip("no notifier program on this platform")
		}
		dir := t.TempDir()
		t.Setenv("HOME", t.TempDir()) // the notify-send path writes the icon under HOME
		t.Setenv("HUDDLE_NOTIFY_LOG", "")
		t.Setenv("HUDDLE_NOTIFY", "")
		clock := &fakeClock{now: time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)}
		var program string
		var args []string
		n := NewNotifier(dir)
		n.Now = clock.Now
		n.Spawn = func(p string, a []string) error {
			program, args = p, a
			return nil
		}
		if !n.Send("ask", "s", strings.Repeat("word ", 60), strings.Repeat("phrase ", 60)) {
			t.Fatal("Send = false, want true")
		}
		if program == "" || len(args) < 3 {
			t.Fatalf("spawned %q %v", program, args)
		}
		title, body := args[len(args)-2], args[len(args)-1]
		if got := jsscalar.UTF16Len(title); got != 80 {
			t.Errorf("title is %d units, want 80: %q", got, title)
		}
		if !strings.HasSuffix(title, "…") {
			t.Errorf("title = %q, want it capped with …", title)
		}
		if got := jsscalar.UTF16Len(body); got != 160 {
			t.Errorf("body is %d units, want 160", got)
		}
		if how == "osascript" {
			if program != "osascript" {
				t.Errorf("program = %q, want osascript", program)
			}
			want := []string{"-e", "on run argv", "-e", "display notification (item 2 of argv) with title (item 1 of argv)", "-e", "end run"}
			if len(args) != len(want)+2 {
				t.Fatalf("args = %v", args)
			}
			for i, w := range want {
				if args[i] != w {
					t.Errorf("args[%d] = %q, want %q", i, args[i], w)
				}
			}
		} else {
			if program != "notify-send" {
				t.Errorf("program = %q, want notify-send", program)
			}
			if args[0] != "-a" || args[1] != "Huddle" {
				t.Errorf("args = %v, want [-a Huddle title body]", args)
			}
		}
	})

	t.Run("with no notifier program here, nothing is sent", func(t *testing.T) {
		if NotifierProgram() != "" {
			t.Skip("a notifier program exists here")
		}
		t.Setenv("HUDDLE_NOTIFY_LOG", "")
		n := NewNotifier(t.TempDir())
		if n.Send("ask", "s", "t", "b") {
			t.Error("Send = true with no program to show it, want false")
		}
	})

	t.Run("the notify-send send carries the huddle icon in its file:// form", func(t *testing.T) {
		if runtime.GOOS != "linux" {
			t.Skip("NotifierProgram is this platform's, so notify-send only steers on linux")
		}
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("HUDDLE_NOTIFY_LOG", "")
		t.Setenv("HUDDLE_NOTIFY", "")
		bin := t.TempDir()
		if err := os.WriteFile(filepath.Join(bin, "notify-send"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin)
		var program string
		var args []string
		n := NewNotifier(t.TempDir())
		n.Spawn = func(p string, a []string) error {
			program, args = p, a
			return nil
		}
		if !n.Send("ask", "s", "T", "b") {
			t.Fatal("Send = false, want true")
		}
		icon := filepath.Join(home, ".agents", "huddle", "huddle.png")
		want := []string{"-a", "Huddle", "-i", "file://" + icon, "T", "b"}
		if program != "notify-send" || !slices.Equal(args, want) {
			t.Errorf("spawned %q %v, want %q %v", program, args, "notify-send", want)
		}
		if _, err := os.Stat(icon); err != nil {
			t.Errorf("the icon PNG: %v", err)
		}
		t.Setenv("HOME", "")
		if !n.Send("ask", "s2", "T", "b") {
			t.Fatal("second send = false, want true")
		}
		if want := []string{"-a", "Huddle", "T", "b"}; !slices.Equal(args, want) {
			t.Errorf("args with no HOME = %v, want %v", args, want)
		}
	})

	t.Run("the argv choice: the icon in its file:// form, or today's argv without one", func(t *testing.T) {
		if got := notifySendArgs("/i/huddle.png", "T", "b"); !slices.Equal(got, []string{"-a", "Huddle", "-i", "file:///i/huddle.png", "T", "b"}) {
			t.Errorf("notifySendArgs with an icon = %v, want [-a Huddle -i file:///i/huddle.png T b]", got)
		}
		if got := notifySendArgs("", "T", "b"); !slices.Equal(got, []string{"-a", "Huddle", "T", "b"}) {
			t.Errorf("notifySendArgs with no icon = %v, want [-a Huddle T b]", got)
		}
	})

	t.Run("the notifier program starts detached and is forgotten", func(t *testing.T) {
		if err := spawnDetached("/bin/echo", []string{"huddle"}); err != nil {
			t.Errorf("spawnDetached: %v", err)
		}
	})
}

// TestCleanFoldsAndMasksJavaScriptWhitespace: the mask's and the fold's \s are JavaScript's,
// which U+00A0, U+202F and U+3000 belong to and U+0085 does not.
func TestCleanFoldsAndMasksJavaScriptWhitespace(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"token\u00a0=\u00a0hunter2", "token = …"},
		{"api-key\u3000:\u3000sekrit", "api-key : …"},
		{"auth\u202f=\u202fpurple31", "auth = …"},
		{"line1\u3000\u00a0line2\u2028line3\u2029", "line1 line2 line3"},
		{"password:\u0085nel-before-value", "password:…"},
		{"a\u00a0b\u00a0c", "a b c"},
	}
	for _, c := range cases {
		if got := Clean(c.in, 140); got != c.want {
			t.Errorf("Clean(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestOnPath(t *testing.T) {
	t.Run("a program on PATH is found; a directory or a missing one is not", func(t *testing.T) {
		root := t.TempDir()
		bin := filepath.Join(root, "bin")
		other := filepath.Join(root, "other")
		for _, d := range []string{bin, other} {
			if err := os.MkdirAll(d, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(bin, "notify-send"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(bin, "a-directory"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(other, "a-tool"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", strings.Join([]string{bin, "", other}, string(os.PathListSeparator)))
		if got := onPath("notify-send"); got != filepath.Join(bin, "notify-send") {
			t.Errorf("onPath(notify-send) = %q, want the one in the first dir", got)
		}
		if got := onPath("a-tool"); got != filepath.Join(other, "a-tool") {
			t.Errorf("onPath(a-tool) = %q, want the one in the second dir", got)
		}
		if got := onPath("a-directory"); got != "" {
			t.Errorf("onPath(a-directory) = %q, want empty: a directory is not a program", got)
		}
		if got := onPath("missing"); got != "" {
			t.Errorf("onPath(missing) = %q, want empty", got)
		}
	})

	t.Run("notifier() names the program this desktop answers to", func(t *testing.T) {
		switch runtime.GOOS {
		case "darwin":
			if _, err := os.Stat("/usr/bin/osascript"); err != nil {
				t.Skip("no /usr/bin/osascript here")
			}
			if got := NotifierProgram(); got != "osascript" {
				t.Errorf("NotifierProgram() = %q, want osascript", got)
			}
		case "linux":
			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, "notify-send"), []byte("#!/bin/sh\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			if got := NotifierProgram(); got != "notify-send" {
				t.Errorf("NotifierProgram() = %q, want notify-send", got)
			}
			t.Setenv("PATH", "")
			if got := NotifierProgram(); got != "" {
				t.Errorf("NotifierProgram() = %q with nothing on PATH, want empty", got)
			}
		default:
			if got := NotifierProgram(); got != "" {
				t.Errorf("NotifierProgram() = %q on %s, want empty", got, runtime.GOOS)
			}
		}
	})
}

func TestSettingsLayout(t *testing.T) {
	t.Run("a settings file that is not an object reads as empty", func(t *testing.T) {
		dir := t.TempDir()
		settings := filepath.Join(dir, "settings.json")
		t.Setenv("HUDDLE_NOTIFY_LOG", "")
		for _, body := range []string{"[1, 2]", "bogus{", `"`} {
			if err := os.WriteFile(settings, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			n := NewNotifier(dir)
			if !n.Enabled() {
				t.Errorf("Enabled() = false over settings %q, want true", body)
			}
			n.Set(true)
			want := "{\n  \"notify\": true\n}\n"
			if got, _ := os.ReadFile(settings); string(got) != want {
				t.Errorf("settings.json = %q over %q, want %q", got, body, want)
			}
		}
	})

	t.Run("values are laid out member by member, strings untouched", func(t *testing.T) {
		got := prettyJSON(`"a <b> é"`, 1)
		if want := `"a <b> é"`; got != want {
			t.Errorf("prettyJSON of a string = %s, want %s", got, want)
		}
		got = prettyJSON(`[1, "two", null]`, 1)
		want := "[\n    1,\n    \"two\",\n    null\n  ]"
		if got != want {
			t.Errorf("prettyJSON of an array = %s, want %s", got, want)
		}
		got = prettyJSON(`{"x": {"y": [1]}, "z": {}}`, 0)
		want = "{\n  \"x\": {\n    \"y\": [\n      1\n    ]\n  },\n  \"z\": {}\n}"
		if got != want {
			t.Errorf("prettyJSON of an object = %s, want %s", got, want)
		}
	})

	t.Run("a malformed body never reaches the file", func(t *testing.T) {
		for _, body := range []string{"{", `{"a"`, `{"a":`, `{"a" 1}`, `{"a": [1`, `{"a": "b`} {
			if members := scanMembers(body); members != nil {
				t.Errorf("scanMembers(%q) = %v, want nil", body, members)
			}
		}
		if got := prettyJSON(`[ "a ]`, 0); got != `[ "a ]` {
			t.Errorf("prettyJSON of a torn array = %s, want it back untouched", got)
		}
	})

	t.Run("a notification log that cannot be written is quiet", func(t *testing.T) {
		t.Setenv("HUDDLE_NOTIFY_LOG", filepath.Join(t.TempDir(), "no-such-dir", "log"))
		n := NewNotifier(t.TempDir())
		if n.Send("ask", "s", "t", "b") {
			t.Error("Send = true with nowhere to log, want false")
		}
	})

	t.Run("a program that is not there reports its start failure", func(t *testing.T) {
		if err := spawnDetached(filepath.Join(t.TempDir(), "no-such-program"), nil); err == nil {
			t.Error("spawnDetached of a missing program: want an error")
		}
	})
}

// The note line is JSON.stringify's bytes — `<`, `>`, `&` and the two line separators raw in
// the subject, which clean never touches — while the title and body fold the separators,
// which JavaScript's \s counts.
func TestNoteLineKeepsJSONStringifyBytes(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "log")
	t.Setenv("HUDDLE_NOTIFY_LOG", logFile)
	t.Setenv("HUDDLE_NOTIFY_WINDOW_MS", "")
	clock := &fakeClock{now: time.Date(2026, 10, 15, 4, 0, 0, 0, time.UTC)}
	n := NewNotifier(dir)
	n.Now = clock.Now
	if !n.Send("ask", "shop/api\u2028x\u2029y", "Huddle · <a>&b\u2028c", "is 1 < 2 & 3 > 2?\u2029yes") {
		t.Fatal("Send = false, want true")
	}
	want := "{\"at\":\"2026-10-15T04:00:00.000Z\",\"kind\":\"ask\",\"subject\":\"shop/api\u2028x\u2029y\"," +
		"\"title\":\"Huddle · <a>&b c\",\"body\":\"is 1 < 2 & 3 > 2? yes\"}\n"
	got, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("log line = %q, want %q", got, want)
	}
}
