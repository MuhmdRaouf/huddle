package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/harness"
)

// statuslineSample is the stdin JSON Claude Code hands its statusline command, with both
// windows filled.
const statuslineSample = `{"session_id":"s1","cwd":"/shop","model":{"display_name":"Opus"},
	"cost":{"total_cost_usd":1.25},
	"rate_limits":{"five_hour":{"used_percentage":42,"resets_at":2000000000},
	"seven_day":{"used_percentage":18,"resets_at":2000500000}}}`

// statuslineWorld is one session in a huddle whose server is nowhere: env identity, a temp
// home, a temp huddle home, no project credential.
func statuslineWorld(t *testing.T, stdin string, env map[string]string) (*World, string) {
	t.Helper()
	hud := t.TempDir()
	home := t.TempDir()
	vars := map[string]string{
		"HOME":        home,
		"HUDDLE_HOME": hud,
	}
	for key, value := range env {
		vars[key] = value
	}
	w := world(t, t.TempDir(), vars)
	if stdin != "" {
		copied := stdin
		w.Stdin = strings.NewReader(copied)
	}
	return w, hud
}

func TestFormatStatusline(t *testing.T) {
	whole := 42.0
	online := 3
	spend := 1.5
	cases := []struct {
		name  string
		parts statuslineParts
		want  string
	}{
		{"every part, in order", statuslineParts{Channel: "shop", Online: &online, Spend: &spend, FiveHour: &whole, Week: &whole},
			"huddle shop · 3 online · $1.50 today · 5h 42% · week 42%"},
		{"only the channel", statuslineParts{Channel: "shop"}, "huddle shop"},
		{"only the limits", statuslineParts{FiveHour: &whole, Week: &whole}, "5h 42% · week 42%"},
		{"one limit", statuslineParts{FiveHour: &whole}, "5h 42%"},
		{"nothing to say", statuslineParts{}, ""},
		{"fractions round to whole percents", statuslineParts{FiveHour: floatPtr(42.6), Week: floatPtr(17.4)}, "5h 43% · week 17%"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatStatusline(tc.parts); got != tc.want {
				t.Errorf("formatStatusline = %q, want %q", got, tc.want)
			}
		})
	}
}

func floatPtr(v float64) *float64 { return &v }

// TestStatuslineWordWithTheServerDown pins the shape and the speed: the server's parts are
// left out, the limits stay, and the whole word answers inside its half-second budget.
func TestStatuslineWordWithTheServerDown(t *testing.T) {
	w, _ := statuslineWorld(t, statuslineSample, map[string]string{
		"HUDDLE_URL":     "http://127.0.0.1:1",
		"HUDDLE_CHANNEL": "shop",
		"HUDDLE_AS":      "api",
	})
	var stdout, stderr bytes.Buffer
	started := time.Now()
	if code := Huddle(context.Background(), w, []string{"statusline"}, &stdout, &stderr); code != 0 {
		t.Fatalf("statusline = %d, stderr %q", code, stderr.String())
	}
	if got := stdout.String(); got != "huddle shop · 5h 42% · week 18%\n" {
		t.Fatalf("statusline printed %q, want the channel and the two windows", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("statusline said %q on stderr, want silence", stderr.String())
	}
	if took := time.Since(started); took > 500*time.Millisecond {
		t.Fatalf("statusline took %s, want under 500 ms", took)
	}
}

// TestStatuslineWordWithAPreviousStatusline pins the chaining: the recorded previous command
// runs with the same stdin, its first line leads, huddle's line follows.
func TestStatuslineWordWithAPreviousStatusline(t *testing.T) {
	w, hud := statuslineWorld(t, statuslineSample, map[string]string{
		"HUDDLE_URL":     "http://127.0.0.1:1",
		"HUDDLE_CHANNEL": "shop",
		"HUDDLE_AS":      "api",
	})
	// a settings file with a statusline of the user's own, then the install that replaced it
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, ".claude", "settings.json"),
		`{"statusLine":{"type":"command","command":"printf 'PREV LINE\nSECOND\n'"}}`+"\n")
	if _, err := harness.InstallStatusLine(hud, harness.Env{
		Lookup: func(key string) string {
			if key == "HOME" {
				return dir
			}
			return ""
		},
	}, huddleBin); err != nil {
		t.Fatalf("seed the ledger: %v", err)
	}
	var stdout, stderr bytes.Buffer
	if code := Huddle(context.Background(), w, []string{"statusline"}, &stdout, &stderr); code != 0 {
		t.Fatalf("statusline = %d, stderr %q", code, stderr.String())
	}
	got := strings.TrimSuffix(stdout.String(), "\n")
	if !strings.HasPrefix(got, "PREV LINE · ") {
		t.Fatalf("statusline = %q, want the previous line first", got)
	}
	if !strings.HasSuffix(got, "5h 42% · week 18%") {
		t.Fatalf("statusline = %q, want huddle's line last", got)
	}
}

// TestStatuslineWordPrintsNothingWhenThereIsNothingToSay pins the silence: no server, no
// limits, no previous line — the word prints nothing and still exits 0.
func TestStatuslineWordPrintsNothingWhenThereIsNothingToSay(t *testing.T) {
	w, _ := statuslineWorld(t, "{}", nil)
	var stdout, stderr bytes.Buffer
	if code := Huddle(context.Background(), w, []string{"statusline"}, &stdout, &stderr); code != 0 {
		t.Fatalf("statusline = %d, want 0", code)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("statusline said %q / %q, want silence", stdout.String(), stderr.String())
	}
}

// TestStatuslineWordColorsTheLimits pins the flag: no colour without --color, the thresholds'
// codes with it.
func TestStatuslineWordColorsTheLimits(t *testing.T) {
	if got := statuslinePercent(42, false, "5h"); got != "5h 42%" {
		t.Fatalf("plain = %q, want no colour codes", got)
	}
	if got := statuslinePercent(42, true, "5h"); !strings.Contains(got, "\x1b[32m") {
		t.Fatalf("42 with --color = %q, want the ok colour", got)
	}
	if got := statuslinePercent(75, true, "5h"); !strings.Contains(got, "\x1b[33m") {
		t.Fatalf("75 with --color = %q, want the warn colour", got)
	}
	if got := statuslinePercent(95, true, "week"); !strings.Contains(got, "\x1b[31m") {
		t.Fatalf("95 with --color = %q, want the danger colour", got)
	}
}

// TestStatuslineWordInstallsAndRemoves walks the settings through the word: install writes the
// entry naming this binary, a second install says so, remove gives the file back exactly.
func TestStatuslineWordInstallsAndRemoves(t *testing.T) {
	w, hud := statuslineWorld(t, "", map[string]string{"HUDDLE_NO_PROJECT_CRED": "1"})
	home := w.Getenv("HOME")
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, settings, `{"model":"opus"}`+"\n")

	var stdout, stderr bytes.Buffer
	if code := Huddle(context.Background(), w, []string{"statusline", "--install"}, &stdout, &stderr); code != 0 {
		t.Fatalf("statusline --install = %d, stderr %q", code, stderr.String())
	}
	raw := readFile(t, settings)
	bin, err := filepath.EvalSymlinks(huddleBin)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "statusline") || !strings.Contains(raw, bin) {
		t.Fatalf("install wrote %s, want the entry naming %s", raw, bin)
	}
	if !strings.Contains(raw, `"model":"opus"`) && !strings.Contains(raw, "\"model\": \"opus\"") {
		t.Fatalf("install disturbed the other keys: %s", raw)
	}

	stdout.Reset()
	if code := Huddle(context.Background(), w, []string{"statusline", "--install"}, &stdout, &stderr); code != 0 {
		t.Fatalf("the second install = %d", code)
	}
	if !strings.Contains(stdout.String(), "already") {
		t.Fatalf("the second install said %q, want already", stdout.String())
	}

	stdout.Reset()
	if code := Huddle(context.Background(), w, []string{"statusline", "--remove"}, &stdout, &stderr); code != 0 {
		t.Fatalf("statusline --remove = %d, stderr %q", code, stderr.String())
	}
	if got := readFile(t, settings); got != `{"model":"opus"}`+"\n" {
		t.Fatalf("remove left %q, want the original bytes", got)
	}
	ledger := readFile(t, filepath.Join(hud, "harness-ledger.json"))
	if strings.Contains(ledger, "statusline") {
		t.Fatalf("the ledger still records the statusline: %s", ledger)
	}
}

// TestStatuslineWordRemoveWithNothingThere pins the empty hand: remove without an install is
// exit 4 and the file untouched.
func TestStatuslineWordRemoveWithNothingThere(t *testing.T) {
	w, _ := statuslineWorld(t, "", map[string]string{"HUDDLE_NO_PROJECT_CRED": "1"})
	home := w.Getenv("HOME")
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(home, ".claude", "settings.json")
	writeFile(t, settings, `{"model":"opus"}`+"\n")
	var stdout, stderr bytes.Buffer
	if code := Huddle(context.Background(), w, []string{"statusline", "--remove"}, &stdout, &stderr); code != 4 {
		t.Fatalf("statusline --remove = %d, want 4 (stderr %q)", code, stderr.String())
	}
	if got := readFile(t, settings); got != `{"model":"opus"}`+"\n" {
		t.Fatalf("remove touched the file: %q", got)
	}
}
