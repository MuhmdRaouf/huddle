// Golden parity of the stdio bridge: the recording under ../server/testdata/golden (stdio-bridge)
// pins the exact lines of one bridge session against the channel the fixture script builds; this
// test builds the same channel in a Go server, runs this bridge against it over a pipe and wants
// the same lines on stdout, byte for byte. The fields that carry the wall clock or a minted
// secret are marked "<volatile>" on both sides.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/server"
)

// goldenBridgeLine is one line of the recorded bridge session: ">" went to the bridge's stdin,
// "<" came off its stdout.
type goldenBridgeLine struct {
	Dir  string `json:"dir"`
	Line string `json:"line"`
}

// goldenBridgeEntry is the recorded stdio session.
type goldenBridgeEntry struct {
	Seq     int                `json:"seq"`
	Kind    string             `json:"kind"`
	Name    string             `json:"name"`
	Channel string             `json:"channel"`
	As      string             `json:"as"`
	Script  []goldenBridgeLine `json:"script"`
}

// The volatile marks the recording carries.
var (
	goldenISO    = regexp.MustCompile(`20\d{2}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z`)
	goldenCred   = regexp.MustCompile(`hcred_[A-Za-z0-9_-]+`)
	goldenInvite = regexp.MustCompile(`\b[a-z0-9]{6}\.[a-z0-9]{16}\b`)
)

func goldenMark(line string) string {
	out := goldenISO.ReplaceAllLiteralString(line, "<volatile>")
	out = goldenCred.ReplaceAllLiteralString(out, "<volatile>")
	return goldenInvite.ReplaceAllLiteralString(out, "<volatile>")
}

// startGoldenHuddle runs one Go huddle server in-process on a free port until the test ends; the
// bridge reaches it over HTTP, as it does any huddle. The version is the plugin's own, the one
// the bridge names in initialize.
func startGoldenHuddle(t *testing.T) string {
	t.Helper()
	port := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	home := filepath.Join(t.TempDir(), "home")
	data := filepath.Join(t.TempDir(), "data")
	log := &mcpLog{}
	served := make(chan struct{})
	go func() {
		_ = server.Serve(ctx, server.Options{
			Version: mcpPluginVersion(t), Host: "127.0.0.1", Port: port,
			Home:  home,
			Data:  data,
			Token: mcpRootToken, AutoCreate: true, Stdout: log, Stderr: io.Discard,
		})
		close(served)
	}()
	// the channel databases must be closed before the temp homes are removed.
	t.Cleanup(func() {
		cancel()
		select {
		case <-served:
		case <-time.After(5 * time.Second):
		}
	})
	u := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(u + "/health")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return u
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("the test's huddle never answered /health: %s", log.text())
	return ""
}

// buildGoldenChannel creates the fixture channel and applies every op of the committed script: the
// state the recorded bridge session ran against (the HTTP recordings before it change nothing it
// can see, and the writes came after it).
func buildGoldenChannel(t *testing.T, u string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "fixture", "script.json"))
	if err != nil {
		t.Fatalf("read the fixture script: %v", err)
	}
	var script struct {
		Channel string `json:"channel"`
		Struct  struct {
			Title        string `json:"title"`
			Description  string `json:"description"`
			Start        string `json:"start"`
			Orchestrator string `json:"orchestrator"`
		} `json:"configure"`
		Ops []struct {
			Op   string          `json:"op"`
			As   string          `json:"as"`
			Args json.RawMessage `json:"args"`
		} `json:"ops"`
	}
	if err := json.Unmarshal(raw, &script); err != nil {
		t.Fatalf("decode the fixture script: %v", err)
	}
	post := func(path string, body string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, u+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("content-type", "application/json")
		req.Header.Set("x-huddle-token", mcpRootToken)
		req.Header.Set(server.ActionHeader, strings.SplitN(path, "?", 2)[0])
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s = %d: %s", path, resp.StatusCode, string(raw))
		}
	}
	configure, err := json.Marshal(struct {
		Name         string `json:"name"`
		Title        string `json:"title,omitempty"`
		Description  string `json:"description,omitempty"`
		Start        string `json:"start,omitempty"`
		Orchestrator string `json:"orchestrator,omitempty"`
	}{script.Channel, script.Struct.Title, script.Struct.Description, script.Struct.Start, script.Struct.Orchestrator})
	if err != nil {
		t.Fatal(err)
	}
	post("/api/channels", string(configure))
	for _, step := range script.Ops {
		// sleep spaces two ops past a second boundary, where a timestamp tie would leave their
		// order to the storage engine: the recording and the replay must agree on the order
		if step.Op == "sleep" {
			var wait struct {
				Ms int64 `json:"ms"`
			}
			_ = json.Unmarshal(step.Args, &wait)
			time.Sleep(time.Duration(wait.Ms) * time.Millisecond)
			continue
		}
		post(fmt.Sprintf("/api/c/%s/op/%s?as=%s", script.Channel, step.Op, step.As), string(step.Args))
	}
}

// TestGoldenStdioBridge replays the recorded bridge session over a pipe: every stdin line the
// recording shows goes in, every stdout line it shows must come out in order, and nothing else.
func TestGoldenStdioBridge(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "golden", "stdio-bridge.json"))
	if err != nil {
		t.Fatalf("read the stdio golden: %v", err)
	}
	var entry goldenBridgeEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("decode the stdio golden: %v", err)
	}
	u := startGoldenHuddle(t)
	buildGoldenChannel(t, u)

	state := t.TempDir()
	vars := map[string]string{
		"HOME":                   filepath.Join(state, "home"),
		"CLAUDE_PROJECT_DIR":     filepath.Join(state, "project"),
		"XDG_STATE_HOME":         state,
		"HUDDLE_NO_PROJECT_CRED": "1",
		"HUDDLE_URL":             u,
		"HUDDLE_CHANNEL":         entry.Channel,
		"HUDDLE_AS":              entry.As,
		"HUDDLE_TOKEN":           mcpRootToken,
		"HUDDLE_PUSH":            "off",
	}
	entries := make([]string, 0, len(vars))
	for key, value := range vars {
		entries = append(entries, key+"="+value)
	}
	w := &World{
		Getenv:  func(key string) string { return vars[key] },
		Environ: func() []string { return append([]string{}, entries...) },
		Dir:     vars["CLAUDE_PROJECT_DIR"],
		Now:     func() time.Time { return time.Now() },
	}
	reader, writer := io.Pipe()
	out := &mcpLog{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- McpStdio(ctx, w, mcpPluginVersion(t), reader, out, io.Discard) }()
	t.Cleanup(func() {
		cancel()
		_ = writer.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	})

	waitForLine := func(want string) {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			for _, line := range out.linesNow() {
				if goldenMark(line) == want {
					return
				}
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatalf("the bridge never printed:\n%s\nit printed:\n%s", want, out.text())
	}
	heard := 0
	for _, line := range entry.Script {
		if line.Dir == ">" {
			if _, err := writer.Write([]byte(line.Line + "\n")); err != nil {
				t.Fatalf("send %s: %v", line.Line, err)
			}
			continue
		}
		waitForLine(line.Line)
		heard++
	}
	_ = writer.Close()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("the bridge exited %d, want 0", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the bridge did not end within 2 s of stdin closing")
	}
	if printed := len(out.linesNow()); printed != heard {
		t.Errorf("the bridge printed %d lines, the golden holds %d:\n%s", printed, heard, out.text())
	}
}

// linesNow answers a copy of everything the log holds.
func (l *mcpLog) linesNow() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string{}, l.lines...)
}
