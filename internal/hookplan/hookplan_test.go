package hookplan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// hooksFile is the shape of plugin/hooks/hooks.json.
type hooksFile struct {
	Hooks map[string][]struct {
		Matcher string `json:"matcher"`
		Hooks   []struct {
			Command string `json:"command"`
			Timeout int    `json:"timeout"`
		} `json:"hooks"`
	} `json:"hooks"`
}

// readHooksJSON reads the plugin's hooks.json, the file Claude Code loads.
func readHooksJSON(t *testing.T) hooksFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "plugin", "hooks", "hooks.json"))
	if err != nil {
		t.Fatalf("read hooks.json: %v", err)
	}
	var file hooksFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("parse hooks.json: %v", err)
	}
	return file
}

// eventWord is the word after the last `hook ` in a launcher line; ok is false for the two
// decision lines (approve, permit), which keep their own hooks and are no events.
func eventWord(command string) (word string, ok bool) {
	const fallback = `; exec sh "${CLAUDE_PLUGIN_ROOT}/dist/run" hook `
	at := strings.LastIndex(command, fallback)
	if at < 0 {
		return "", false
	}
	word = command[at+len(fallback):]
	return word, word != "approve" && word != "permit"
}

// wantParts is the coverage the old per-part hooks.json lines had, part for part and event for
// event: the merged process must run exactly these, in this order.
var wantParts = map[string][]string{
	"session-start":      {"session-start", "tips", "report", "radar", "kanban", "agents", "providers"},
	"user-prompt-submit": {"listen", "tips", "report", "radar"},
	"pre-tool-use":       {"reserve", "report"},
	"post-tool-use":      {"listen", "report"},
	"notification":       {"report", "radar"},
	"permission-request": {"report"},
	"stop":               {"stop", "report", "radar"},
	"subagent-start":     {"radar"},
	"subagent-stop":      {"report", "radar", "usage"},
	"session-end":        {"report", "radar"},
	"pre-compact":        {"radar"},
	"task-created":       {"task", "kanban-task"},
	"teammate-idle":      {"report"},
}

// TestThePlanCoversTheHooksJsonLines holds the plan against the plugin's hooks.json: the events
// are the same, the parts are the ones the old per-part lines ran, in the old order, and the
// deadlines sum inside the timeout Claude Code grants the event, with a tenth of it to spare.
func TestThePlanCoversTheHooksJsonLines(t *testing.T) {
	file := readHooksJSON(t)
	seen := map[string]int{}
	for _, groups := range file.Hooks {
		for _, group := range groups {
			for _, entry := range group.Hooks {
				word, ok := eventWord(entry.Command)
				if !ok {
					continue
				}
				if _, again := seen[word]; again {
					t.Fatalf("hooks.json runs the %s event line twice", word)
				}
				seen[word] = entry.Timeout
				parts := Parts(word)
				planned := parts != nil
				if !planned {
					t.Fatalf("hooks.json runs hook %s, the plan has no such event", word)
				}
				if got := namesOf(parts); strings.Join(got, ",") != strings.Join(wantParts[word], ",") {
					t.Errorf("hook %s runs %v, want %v (the old lines' coverage)", word, got, wantParts[word])
				}
				var sum time.Duration
				for _, part := range parts {
					if part.Budget <= 0 {
						t.Errorf("hook %s: part %s owns a %s deadline, want a positive one", word, part.Name, part.Budget)
					}
					sum += part.Budget
				}
				budget := time.Duration(entry.Timeout) * time.Second
				if sum*10 > budget*9 {
					t.Errorf("hook %s: its parts' deadlines sum to %s, over 90%% of the event's %s", word, sum, budget)
				}
			}
		}
	}
	for word := range Events() {
		if _, ran := seen[word]; !ran {
			t.Errorf("the plan plans %s, hooks.json runs no such line", word)
		}
	}
	for word := range wantParts {
		if Parts(word) == nil {
			t.Errorf("the old lines' coverage names %s, the plan has no such event", word)
		}
	}
}

// TestEveryPartKeepsItsHandlersWorstCase holds each huddle handler's own worst-case deadlines
// against the largest budget the plan grants that part: a handler whose internals overshoot every
// slice it gets would answer nothing but cuts. radar's and kanban's parts are bounded by the
// deadlines their own packages pin.
func TestEveryPartKeepsItsHandlersWorstCase(t *testing.T) {
	worst := map[string]time.Duration{
		"session-start": 3000 * time.Millisecond,
		"listen":        3000 * time.Millisecond,
		"tips":          400 * time.Millisecond,
		"report":        1900 * time.Millisecond,
		"stop":          5500 * time.Millisecond,
	}
	for part, ms := range worst {
		largest := time.Duration(0)
		for _, parts := range Events() {
			for _, listed := range parts {
				if listed.Name == part && listed.Budget > largest {
					largest = listed.Budget
				}
			}
		}
		if largest == 0 {
			t.Fatalf("the plan grants %s no budget", part)
		}
		if ms > largest {
			t.Errorf("%s can take %s inside itself, the plan never grants it more than %s", part, ms, largest)
		}
	}
}

func namesOf(parts []Part) []string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		out = append(out, part.Name)
	}
	return out
}

func TestBudget(t *testing.T) {
	cases := []struct {
		event, part string
		want        time.Duration
		ok          bool
	}{
		{"stop", "stop", 6000 * time.Millisecond, true},
		{"session-start", "kanban", 2500 * time.Millisecond, true},
		{"session-start", "agents", 100 * time.Millisecond, true},
		{"user-prompt-submit", "tips", 400 * time.Millisecond, true},
		{"session-start", "tips", 500 * time.Millisecond, true},
		{"stop", "tips", 0, false},
		{"no-such-event", "report", 0, false},
		{"pre-compact", "report", 0, false},
	}
	for _, c := range cases {
		got, ok := Budget(c.event, c.part)
		if got != c.want || ok != c.ok {
			t.Errorf("Budget(%q, %q) = %v, %v; want %v, %v", c.event, c.part, got, ok, c.want, c.ok)
		}
	}
}

func TestEventsIsAFreshTableEachCall(t *testing.T) {
	first := Events()
	first["stop"][0].Budget = 0
	delete(first, "pre-compact")
	if got, _ := Budget("stop", "stop"); got != 6000*time.Millisecond {
		t.Fatalf("a caller's change reached the plan: stop budget %v", got)
	}
	if Parts("pre-compact") == nil {
		t.Fatal("a caller's delete reached the plan")
	}
}
