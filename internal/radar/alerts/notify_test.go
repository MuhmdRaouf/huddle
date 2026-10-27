package alerts

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/notifyicon"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// testEnv is a whole throwaway world: HOME and RADAR_HOME under one temp dir, built from
// scratch so no stray variable from the surrounding shell can leak a real path in.
type testEnv struct {
	env   domain.EnvLookup
	home  string
	state string
}

func makeEnv(t *testing.T) testEnv {
	t.Helper()
	home := t.TempDir()
	return testEnv{
		env: domain.EnvLookup{
			"HOME":              home,
			"RADAR_HOME":        filepath.Join(home, "state"),
			"CLAUDE_CONFIG_DIR": filepath.Join(home, "claude"),
			"ZAI_STATE_DIR":     filepath.Join(home, "zai"),
		},
		home:  home,
		state: filepath.Join(home, "state"),
	}
}

// writeText mkdir -p's the parent, then writes with 0600.
func writeText(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// notifyAlert is one stuck alert as the engine emits it.
func notifyAlert() shared.Alert {
	return shared.Alert{
		ID:        "stuck:s1:1",
		Kind:      shared.AlertStuck,
		SessionID: "s1",
		Project:   "app",
		Since:     1,
		Detail:    "No activity",
		Severity:  shared.SeverityErr,
	}
}

func TestDesktopNotifications(t *testing.T) {
	t.Run("builds an osascript line on macOS and escapes the text", func(t *testing.T) {
		if got := AppleScriptText("say \"hi\"\\\n"); got != "say \\\"hi\\\"\\\\ " {
			t.Errorf("AppleScriptText = %q, want %q", got, "say \\\"hi\\\"\\\\ ")
		}
		line := NotifyCommand("darwin", domain.EnvLookup{}, "T", "b \"q\"", "")
		if line == nil || line.Command != "osascript" || len(line.Args) != 2 || line.Args[0] != "-e" ||
			line.Args[1] != "display notification \"b \\\"q\\\"\" with title \"T\"" {
			t.Errorf("line = %+v", line)
		}
		if NotifyCommand("linux", domain.EnvLookup{"PATH": "/nonexistent"}, "T", "b", "") != nil {
			t.Error("linux without notify-send on PATH has no notifier")
		}
	})

	t.Run("uses notify-send on Linux only when it is on PATH", func(t *testing.T) {
		env := makeEnv(t)
		bin := filepath.Join(env.home, "bin", "notify-send")
		writeText(t, bin, "#!/bin/sh\n")
		if err := os.Chmod(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		line := NotifyCommand("linux", domain.EnvLookup{"PATH": ":" + filepath.Join(env.home, "bin")}, "T", "b", "")
		if line == nil || line.Command != "notify-send" || len(line.Args) != 3 ||
			line.Args[0] != "--app-name=Radar" || line.Args[1] != "T" || line.Args[2] != "b" {
			t.Errorf("line = %+v", line)
		}
	})

	t.Run("the linux line carries the icon in its file:// form, darwin's line is the applet stub", func(t *testing.T) {
		env := makeEnv(t)
		bin := filepath.Join(env.home, "bin", "notify-send")
		writeText(t, bin, "#!/bin/sh\n")
		if err := os.Chmod(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		linuxEnv := domain.EnvLookup{"PATH": ":" + filepath.Join(env.home, "bin")}
		line := NotifyCommand("linux", linuxEnv, "T", "b", "/x/huddle.png")
		if line == nil || !slices.Equal(line.Args, []string{"--app-name=Radar", "-i", "file:///x/huddle.png", "T", "b"}) {
			t.Errorf("line = %+v, want --app-name=Radar -i file:///x/huddle.png T b", line)
		}
		line = NotifyCommand("linux", linuxEnv, "T", "b", "")
		if line == nil || !slices.Equal(line.Args, []string{"--app-name=Radar", "T", "b"}) {
			t.Errorf("line = %+v, want --app-name=Radar T b", line)
		}
		stub := NotifyCommand("darwin", domain.EnvLookup{}, "T", "b", "/x/applet")
		if stub == nil || stub.Command != "/x/applet" || !slices.Equal(stub.Args, []string{"T", "b"}) {
			t.Errorf("darwin line = %+v, want the applet stub with the title and the body", stub)
		}
		script := NotifyCommand("darwin", domain.EnvLookup{}, "T", "b", "")
		if script == nil || script.Command != "osascript" || slices.Contains(script.Args, "file:///x/huddle.png") {
			t.Errorf("darwin line = %+v, want the osascript line with no icon", script)
		}
	})

	t.Run("on darwin a ready applet's stub takes the alert, and nothing builds", func(t *testing.T) {
		env := makeEnv(t)
		ctx := context.Background()
		stub := notifyicon.AppletStub(env.home)
		if err := os.MkdirAll(filepath.Dir(stub), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(stub, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(notifyicon.AppletDigestPath(env.home), []byte(notifyicon.AppletDigest()), 0o600); err != nil {
			t.Fatal(err)
		}
		var calls [][]string
		notifier := NewNotifier(ctx, NotifierOptions{
			Env:      env.env,
			Enabled:  func() bool { return true },
			Runner:   func(command string, args []string) { calls = append(calls, append([]string{command}, args...)) },
			Platform: "darwin",
			Now:      func() time.Time { return time.UnixMilli(1_000) },
		})
		if got := notifier.Consider(ctx, []shared.Alert{notifyAlert()}); got != 1 {
			t.Fatalf("consider = %d, want 1", got)
		}
		want := []string{stub, "Radar: session looks stuck", "app: No activity"}
		if len(calls) != 1 || !slices.Equal(calls[0], want) {
			t.Errorf("calls = %v, want %v", calls, want)
		}
	})

	t.Run("on linux the notifier writes the huddle icon and points the command at it", func(t *testing.T) {
		env := makeEnv(t)
		ctx := context.Background()
		bin := filepath.Join(env.home, "bin", "notify-send")
		writeText(t, bin, "#!/bin/sh\n")
		if err := os.Chmod(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		env.env["PATH"] = ":" + filepath.Dir(bin)
		var calls [][]string
		notifier := NewNotifier(ctx, NotifierOptions{
			Env:      env.env,
			Enabled:  func() bool { return true },
			Runner:   func(command string, args []string) { calls = append(calls, append([]string{command}, args...)) },
			Platform: "linux",
			Now:      func() time.Time { return time.UnixMilli(1_000) },
		})
		if got := notifier.Consider(ctx, []shared.Alert{notifyAlert()}); got != 1 {
			t.Fatalf("consider = %d, want 1", got)
		}
		icon := filepath.Join(env.home, ".agents", "huddle", "huddle.png")
		want := []string{"notify-send", "--app-name=Radar", "-i", "file://" + icon, "Radar: session looks stuck", "app: No activity"}
		if len(calls) != 1 || !slices.Equal(calls[0], want) {
			t.Errorf("calls = %v, want %v", calls, want)
		}
		if _, err := os.Stat(icon); err != nil {
			t.Errorf("the icon PNG: %v", err)
		}
	})

	t.Run("notifies once per alert, at most once per subject in 10 minutes, and remembers across restarts", func(t *testing.T) {
		env := makeEnv(t)
		ctx := context.Background()
		var calls [][]string
		at := int64(1_000)
		clock := func() time.Time { return time.UnixMilli(at) }
		make := func() *Notifier {
			return NewNotifier(ctx, NotifierOptions{
				Env:      env.env,
				Enabled:  func() bool { return true },
				Runner:   func(command string, args []string) { calls = append(calls, append([]string{command}, args...)) },
				Platform: "darwin",
				Now:      clock,
			})
		}
		retry := notifyAlert()
		retry.ID = "retry"
		retry.Kind = shared.AlertRetryStorm
		notifier := make()
		if got := notifier.Consider(ctx, []shared.Alert{notifyAlert(), retry}); got != 1 {
			t.Fatalf("consider = %d, want 1", got)
		}
		if len(calls) != 1 || !strings.Contains(calls[0][2], "app: No activity") {
			t.Errorf("calls = %v", calls)
		}
		if got := notifier.Consider(ctx, []shared.Alert{notifyAlert()}); got != 0 {
			t.Fatalf("consider again = %d, want 0", got)
		}
		second := notifyAlert()
		second.ID = "stuck:s1:2"
		if got := notifier.Consider(ctx, []shared.Alert{second}); got != 0 {
			t.Fatalf("same subject within 10 min = %d, want 0", got)
		}
		at += 11 * 60_000
		third := notifyAlert()
		third.ID = "stuck:s1:3"
		if got := notifier.Consider(ctx, []shared.Alert{third}); got != 1 {
			t.Fatalf("after the window = %d, want 1", got)
		}
		budgetAlert := notifyAlert()
		budgetAlert.ID = "budget:b1:5:80"
		budgetAlert.Kind = shared.AlertBudget
		budgetAlert.SessionID = ""
		budgetAlert.Project = ""
		if got := notifier.Consider(ctx, []shared.Alert{budgetAlert}); got != 1 {
			t.Fatalf("budget = %d, want 1", got)
		}
		if !strings.HasPrefix(calls[2][2], `display notification "No activity"`) {
			t.Errorf("script = %q", calls[2][2])
		}
		data, err := os.ReadFile(NotifiedPath(ctx, env.env))
		if err != nil {
			t.Fatal(err)
		}
		var memory struct {
			IDs map[string]float64 `json:"ids"`
		}
		if err := json.Unmarshal(data, &memory); err != nil {
			t.Fatalf("notified.json is not JSON: %v", err)
		}
		if _, ok := memory.IDs["stuck:s1:1"]; !ok {
			t.Errorf("notified.json ids = %v, want stuck:s1:1 among them", memory.IDs)
		}
		if got := make().Consider(ctx, []shared.Alert{notifyAlert()}); got != 0 {
			t.Fatalf("a fresh notifier = %d, want 0", got)
		}
	})

	t.Run("notifies Claude's 5-hour window once, under its own title", func(t *testing.T) {
		env := makeEnv(t)
		ctx := context.Background()
		var calls [][]string
		notifier := NewNotifier(ctx, NotifierOptions{
			Env:      env.env,
			Enabled:  func() bool { return true },
			Runner:   func(command string, args []string) { calls = append(calls, append([]string{command}, args...)) },
			Platform: "darwin",
			Now:      func() time.Time { return time.UnixMilli(1_000) },
		})
		window := shared.Alert{
			ID:       "quota_nearly_used:claude-5h:50400000",
			Kind:     shared.AlertQuotaNearlyUsed,
			Since:    32_400_000,
			Detail:   "Claude 5-hour window 80% used, resets 14:00 UTC",
			Severity: shared.SeverityWarn,
		}
		if got := notifier.Consider(ctx, []shared.Alert{window, window}); got != 1 {
			t.Fatalf("consider = %d, want 1", got)
		}
		want := `display notification "Claude 5-hour window 80% used, resets 14:00 UTC" with title "Radar: Claude 5-hour window"`
		if len(calls) != 1 || calls[0][2] != want {
			t.Errorf("calls = %v, want one %q", calls, want)
		}
	})

	t.Run("does nothing when turned off, by the toggle or the environment", func(t *testing.T) {
		env := makeEnv(t)
		ctx := context.Background()
		var calls [][]string
		runner := func(command string, args []string) { calls = append(calls, append([]string{command}, args...)) }
		on := func() bool { return true }
		if got := NewNotifier(ctx, NotifierOptions{
			Env: env.env, Enabled: func() bool { return false }, Runner: runner, Platform: "darwin",
		}).Consider(ctx, []shared.Alert{notifyAlert()}); got != 0 {
			t.Fatalf("toggle off = %d, want 0", got)
		}
		off := domain.EnvLookup{}
		for key, value := range env.env {
			off[key] = value
		}
		off["RADAR_NOTIFY"] = "0"
		if got := NewNotifier(ctx, NotifierOptions{
			Env: off, Enabled: on, Runner: runner, Platform: "darwin",
		}).Consider(ctx, []shared.Alert{notifyAlert()}); got != 0 {
			t.Fatalf("RADAR_NOTIFY=0 = %d, want 0", got)
		}
		if len(calls) != 0 {
			t.Errorf("calls = %v, want none", calls)
		}
	})

	t.Run("spawns detached and never throws, even for a missing binary", func(t *testing.T) {
		SpawnDetached("/nonexistent/radar-notifier", nil)
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		SpawnDetached(exe, []string{"-test.run=^$", "-test.count=1"})
		time.Sleep(200 * time.Millisecond)
	})

	t.Run("reads a broken memory file as empty", func(t *testing.T) {
		env := makeEnv(t)
		ctx := context.Background()
		var calls [][]string
		writeText(t, NotifiedPath(ctx, env.env), "{oops")
		notifier := NewNotifier(ctx, NotifierOptions{
			Env:      env.env,
			Enabled:  func() bool { return true },
			Runner:   func(command string, args []string) { calls = append(calls, append([]string{command}, args...)) },
			Now:      func() time.Time { return time.UnixMilli(1_000) },
			Platform: "darwin",
		})
		if got := notifier.Consider(ctx, []shared.Alert{notifyAlert()}); got != 1 {
			t.Fatalf("consider = %d, want 1", got)
		}
		writeText(t, NotifiedPath(ctx, env.env), `{"ids":{"x":"no"}}`)
		empty := NewNotifier(ctx, NotifierOptions{
			Env:      env.env,
			Enabled:  func() bool { return true },
			Platform: "darwin",
			Runner:   func(string, []string) {},
		})
		if got := empty.Consider(ctx, nil); got != 0 {
			t.Fatalf("consider = %d, want 0", got)
		}
	})
}
