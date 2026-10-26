package hooks

// The tips part: one line of the hub's advice about this session, once per tip, only while the
// owner's tips-to-sessions switch is on. Every failure is silence, and silence asks for nothing.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/hookplan"
	"github.com/MuhmdRaouf/huddle/internal/huddle/cli"
	"github.com/MuhmdRaouf/huddle/internal/insights"
)

// tipsHub is a fake hub that records the insights asks and answers the tips it is given.
type tipsHub struct {
	server *httptest.Server
	mu     sync.Mutex
	asks   []string
	tips   string
}

func startTipsHub(t *testing.T, tips string) *tipsHub {
	t.Helper()
	hub := &tipsHub{tips: tips}
	hub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.mu.Lock()
		hub.asks = append(hub.asks, r.URL.RawQuery)
		hub.mu.Unlock()
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(hub.tips))
	}))
	t.Cleanup(hub.server.Close)
	return hub
}

func (h *tipsHub) asked() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string{}, h.asks...)
}

// twoTips is a hub answer that holds two tips, so a run can walk through them one at a time.
const twoTips = `{"tips":[{"id":"t-1","sentence":"one narrower run is faster"},{"id":"t-2","sentence":"an allow rule would skip the prompt"}]}`

// runTips runs the tips part in a world of its own: the switch file in home, the hub's url, no
// huddle outside the temp directories. An empty url deletes the variable.
func runTips(t *testing.T, home, url, input string) (string, time.Duration) {
	t.Helper()
	vars := map[string]string{
		"HOME":                   t.TempDir(),
		"HUDDLE_HOME":            home,
		"HUDDLE_URL":             url,
		"HUDDLE_NO_PROJECT_CRED": "1",
		"HUDDLE_AUTOSTART":       "0",
		"XDG_STATE_HOME":         t.TempDir(),
		"RADAR_HOME":             t.TempDir(),
		"PATH":                   os.Getenv("PATH"),
	}
	if url == "" {
		delete(vars, "HUDDLE_URL")
	}
	entries := make([]string, 0, len(vars))
	for key, value := range vars {
		entries = append(entries, key+"="+value)
	}
	w := &cli.World{
		Getenv:  func(key string) string { return vars[key] },
		Environ: func() []string { return append([]string{}, entries...) },
		Dir:     home,
		Now:     time.Now,
		Bin:     huddleBin,
	}
	var out strings.Builder
	t0 := time.Now()
	Tips(context.Background(), w, strings.NewReader(input), &out)
	return out.String(), time.Since(t0)
}

// turnTheSwitchOn records tips-to-sessions on in the home's switches file.
func turnTheSwitchOn(t *testing.T, home string) {
	t.Helper()
	if err := insights.WriteSettings(insights.SettingsFile(home), insights.Settings{TipsForMe: true, TipsToSessions: true}); err != nil {
		t.Fatalf("turn the switch on: %v", err)
	}
}

// seenTips reads the session's seen ledger.
func seenTips(t *testing.T, home, session string) ([]string, os.FileMode) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(home, "seen", session+".tips.json"))
	if err != nil {
		t.Fatalf("read the seen ledger: %v", err)
	}
	info, err := os.Stat(filepath.Join(home, "seen", session+".tips.json"))
	if err != nil {
		t.Fatalf("stat the seen ledger: %v", err)
	}
	var seen []string
	if err := json.Unmarshal(raw, &seen); err != nil {
		t.Fatalf("the seen ledger is not a list of ids: %s", raw)
	}
	return seen, info.Mode().Perm()
}

func TestTheTipsPartSaysOneTipOnceWhileTheSwitchIsOn(t *testing.T) {
	home := t.TempDir()
	hub := startTipsHub(t, twoTips)
	input := `{"session_id":"s-tips","hook_event_name":"UserPromptSubmit"}`

	stdout, _ := runTips(t, home, hub.server.URL, input)
	if stdout != "" {
		t.Errorf("with the switch off the part printed %q, want silence", stdout)
	}
	if asks := hub.asked(); len(asks) != 0 {
		t.Errorf("with the switch off the part asked %v, want nothing", asks)
	}

	turnTheSwitchOn(t, home)
	stdout, _ = runTips(t, home, hub.server.URL, input)
	if got := hookContext(t, stdout); got != "Huddle tip: one narrower run is faster" {
		t.Errorf("context = %q, want the first tip", got)
	}
	if asks := hub.asked(); len(asks) != 1 || asks[0] != "session=s-tips" {
		t.Errorf("the hub saw %v, want one ask at session=s-tips", asks)
	}
	seen, mode := seenTips(t, home, "s-tips")
	if len(seen) != 1 || seen[0] != "t-1" {
		t.Errorf("the ledger holds %v, want [t-1]", seen)
	}
	if mode != 0o600 {
		t.Errorf("the ledger sits at %v, want 0600", mode)
	}

	stdout, _ = runTips(t, home, hub.server.URL, input)
	if got := hookContext(t, stdout); got != "Huddle tip: an allow rule would skip the prompt" {
		t.Errorf("the second run says %q, want the next tip", got)
	}
	if seen, _ := seenTips(t, home, "s-tips"); len(seen) != 2 || seen[0] != "t-1" || seen[1] != "t-2" {
		t.Errorf("the ledger holds %v, want [t-1 t-2]", seen)
	}

	if stdout, _ = runTips(t, home, hub.server.URL, input); stdout != "" {
		t.Errorf("the third run printed %q, want silence once every tip was seen", stdout)
	}
	if asks := hub.asked(); len(asks) != 3 {
		t.Errorf("the hub saw %d asks, want 3", len(asks))
	}
}

func TestTheTipsPartNamesTheEventItServes(t *testing.T) {
	for _, event := range []string{"UserPromptSubmit", "SessionStart"} {
		t.Run(event, func(t *testing.T) {
			home := t.TempDir()
			turnTheSwitchOn(t, home)
			hub := startTipsHub(t, twoTips)
			stdout, _ := runTips(t, home, hub.server.URL,
				`{"session_id":"s-tips","hook_event_name":"`+event+`"}`)
			var j struct {
				HookSpecificOutput struct {
					HookEventName     string `json:"hookEventName"`
					AdditionalContext string `json:"additionalContext"`
				} `json:"hookSpecificOutput"`
			}
			if err := json.Unmarshal([]byte(stdout), &j); err != nil {
				t.Fatalf("hook output is not context JSON: %q", stdout)
			}
			if j.HookSpecificOutput.HookEventName != event {
				t.Errorf("hookEventName = %q, want %q", j.HookSpecificOutput.HookEventName, event)
			}
			if !strings.Contains(j.HookSpecificOutput.AdditionalContext, "Huddle tip: ") {
				t.Errorf("context = %q, want a tip line", j.HookSpecificOutput.AdditionalContext)
			}
		})
	}
}

func TestTheTipsPartStaysSilentWhenNothingIsOwed(t *testing.T) {
	t.Run("subagent", func(t *testing.T) {
		home := t.TempDir()
		turnTheSwitchOn(t, home)
		hub := startTipsHub(t, twoTips)
		stdout, _ := runTips(t, home, hub.server.URL,
			`{"session_id":"s-tips","agent_id":"ag-1","hook_event_name":"UserPromptSubmit"}`)
		if stdout != "" {
			t.Errorf("a subagent's hook printed %q, want silence", stdout)
		}
		if asks := hub.asked(); len(asks) != 0 {
			t.Errorf("a subagent's hook asked %v, want nothing", asks)
		}
	})
	t.Run("bad session ids", func(t *testing.T) {
		home := t.TempDir()
		turnTheSwitchOn(t, home)
		hub := startTipsHub(t, twoTips)
		for _, session := range []string{"../oops", "", strings.Repeat("a", 65), "has space"} {
			stdout, _ := runTips(t, home, hub.server.URL,
				`{"session_id":`+quoteJSON(session)+`,"hook_event_name":"UserPromptSubmit"}`)
			if stdout != "" {
				t.Errorf("session %q printed %q, want silence", session, stdout)
			}
		}
		if asks := hub.asked(); len(asks) != 0 {
			t.Errorf("bad sessions asked %v, want nothing", asks)
		}
	})
	t.Run("no hub url", func(t *testing.T) {
		home := t.TempDir()
		turnTheSwitchOn(t, home)
		stdout, _ := runTips(t, home, "",
			`{"session_id":"s-tips","hook_event_name":"UserPromptSubmit"}`)
		if stdout != "" {
			t.Errorf("no url printed %q, want silence", stdout)
		}
	})
	t.Run("hub answers nothing", func(t *testing.T) {
		home := t.TempDir()
		turnTheSwitchOn(t, home)
		stdout, elapsed := runTips(t, home, muteURL(t),
			`{"session_id":"s-tips","hook_event_name":"UserPromptSubmit"}`)
		if stdout != "" {
			t.Errorf("a mute hub printed %q, want silence", stdout)
		}
		if elapsed > 2*time.Second {
			t.Errorf("a mute hub held the part %s, want inside its own deadlines", elapsed)
		}
	})
	for name, url := range map[string]func(*testing.T) string{
		"down":        downURL,
		"garbage":     garbageURL,
		"wrong shape": wrongShapeURL,
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			turnTheSwitchOn(t, home)
			stdout, _ := runTips(t, home, url(t),
				`{"session_id":"s-tips","hook_event_name":"UserPromptSubmit"}`)
			neverBlocks(t, stdout)
			if strings.TrimSpace(stdout) != "" {
				t.Errorf("a %s hub printed %q, want silence", name, stdout)
			}
		})
	}
	t.Run("no tips in the answer", func(t *testing.T) {
		home := t.TempDir()
		turnTheSwitchOn(t, home)
		hub := startTipsHub(t, `{"tips":[]}`)
		stdout, _ := runTips(t, home, hub.server.URL,
			`{"session_id":"s-tips","hook_event_name":"UserPromptSubmit"}`)
		if stdout != "" {
			t.Errorf("an empty answer printed %q, want silence", stdout)
		}
		if _, err := os.Stat(filepath.Join(home, "seen", "s-tips.tips.json")); !os.IsNotExist(err) {
			t.Errorf("an empty answer left a ledger: %v", err)
		}
	})
	t.Run("a ledger that says nothing starts over", func(t *testing.T) {
		home := t.TempDir()
		turnTheSwitchOn(t, home)
		if err := os.MkdirAll(filepath.Join(home, "seen"), 0o700); err != nil {
			t.Fatalf("make the seen folder: %v", err)
		}
		if err := os.WriteFile(filepath.Join(home, "seen", "s-tips.tips.json"), []byte("{not json"), 0o600); err != nil {
			t.Fatalf("break the ledger: %v", err)
		}
		hub := startTipsHub(t, twoTips)
		stdout, _ := runTips(t, home, hub.server.URL,
			`{"session_id":"s-tips","hook_event_name":"UserPromptSubmit"}`)
		if got := hookContext(t, stdout); got != "Huddle tip: one narrower run is faster" {
			t.Errorf("context = %q, want the first tip again", got)
		}
		if seen, _ := seenTips(t, home, "s-tips"); len(seen) != 1 || seen[0] != "t-1" {
			t.Errorf("the ledger holds %v, want [t-1]", seen)
		}
	})
}

// quoteJSON is one string as it sits in a hook's stdin.
func quoteJSON(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

func TestTheTipsPartKeepsItsOwnBudget(t *testing.T) {
	own := (tipsStdinMS + tipsFetchMS) * time.Millisecond
	largest, found := largestBudget("tips")
	if !found {
		t.Fatal("the plan grants tips no budget")
	}
	if own > largest {
		t.Errorf("tips can take %s inside itself, the plan never grants it more than %s", own, largest)
	}
	for _, event := range []string{"user-prompt-submit", "session-start"} {
		if _, ok := hookplan.Budget(event, "tips"); !ok {
			t.Errorf("the plan grants tips no budget on %s", event)
		}
	}
}
