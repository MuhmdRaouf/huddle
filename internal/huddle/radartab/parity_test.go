// Golden parity of every radar route through huddle's mount: the committed goldens under
// radar's testdata pin the exact bytes of every answer over the fixture state; this test builds
// the same state, mounts the tab the way huddle's server does and replays every recording under
// /api/radar, byte for byte. The volatile fields carry the same "<volatile>" marks on both sides.
package radartab

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// quietMs keeps the watcher on its first pass only: no poll lands inside a test.
const quietMs int64 = 3_600_000

// goldenManifest is the fixture plan radar's testdata/fixture carries.
type goldenManifest struct {
	SinceMs int64 `json:"sinceMs"`
	Flow    struct {
		From int64 `json:"from"`
		To   int64 `json:"to"`
	} `json:"flow"`
	Files []struct {
		From    string `json:"from"`
		To      string `json:"to"`
		MtimeMs *int64 `json:"mtimeMs"`
	} `json:"files"`
}

// goldenVariant is one recorded answer.
type goldenVariant struct {
	Query       string `json:"query"`
	Sent        string `json:"sent"`
	Status      int    `json:"status"`
	ContentType string `json:"contentType"`
	Body        string `json:"body"`
}

// goldenFile is one route's recordings.
type goldenFile struct {
	Seq      int             `json:"seq"`
	Route    string          `json:"route"`
	Method   string          `json:"method"`
	Variants []goldenVariant `json:"variants"`
}

// goldenMark is one route's volatile fields and the marker they take.
type goldenMark struct {
	pattern *regexp.Regexp
	mark    string
}

// The volatile fields, named per route, marked exactly as the goldens carry them.
var goldenVolatile = map[string]goldenMark{
	"/api/health":        {regexp.MustCompile(`"(startedAt|port|uptimeMs)":\d+`), `"$1":"<volatile>"`},
	"/api/budget-status": {regexp.MustCompile(`"(updatedAt|periodStart)":\d+`), `"$1":"<volatile>"`},
	"/api/summary":       {regexp.MustCompile(`"now":\d+`), `"now":"<volatile>"`},
	"/api/stream":        {regexp.MustCompile(`"now":\d+`), `"now":"<volatile>"`},
}

// goldenBody marks one answer's volatile fields.
func goldenBody(route, body string) string {
	marker, ok := goldenVolatile[route]
	if !ok {
		return body
	}
	return marker.pattern.ReplaceAllString(body, marker.mark)
}

// buildGoldenState builds the fixture state into temp homes, the same placement radar's own
// golden test does.
func buildGoldenState(t *testing.T) (domain.EnvLookup, int64) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "radar", "testdata", "fixture", "manifest.json"))
	if err != nil {
		t.Fatalf("read the fixture manifest: %v", err)
	}
	var manifest goldenManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("decode the fixture manifest: %v", err)
	}
	root := t.TempDir()
	env := domain.EnvLookup{
		"RADAR_HOME":         filepath.Join(root, "radar"),
		"HOME":               filepath.Join(root, "home"),
		"CLAUDE_CONFIG_DIR":  filepath.Join(root, "claude"),
		"ZAI_STATE_DIR":      filepath.Join(root, "zai"),
		"KIMI_STATE_DIR":     filepath.Join(root, "kimi"),
		"DEEPSEEK_STATE_DIR": filepath.Join(root, "deepseek"),
		"MINIMAX_STATE_DIR":  filepath.Join(root, "minimax"),
		"QWEN_STATE_DIR":     filepath.Join(root, "qwen"),
	}
	today := time.Now().UTC().Format("2006-01-02")
	place := func(target string) string {
		return strings.NewReplacer(
			"__CONFIG__", env["CLAUDE_CONFIG_DIR"],
			"__ZAI__", env["ZAI_STATE_DIR"],
			"__RADAR__", env["RADAR_HOME"],
			"__DATE__", today,
		).Replace(target)
	}
	spool := filepath.Join(env["RADAR_HOME"], "spool", today+".jsonl")
	if err := os.MkdirAll(filepath.Dir(spool), 0o700); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join("..", "..", "radar", "testdata", "fixture", "spool.jsonl"))
	if err != nil {
		t.Fatalf("read the fixture spool: %v", err)
	}
	if err := os.WriteFile(spool, body, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, entry := range manifest.Files {
		target := place(entry.To)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", target, err)
		}
		data, err := os.ReadFile(filepath.Join("..", "..", "radar", "testdata", "fixture", entry.From))
		if err != nil {
			t.Fatalf("read the fixture input %s: %v", entry.From, err)
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", target, err)
		}
		if entry.MtimeMs != nil {
			at := time.UnixMilli(*entry.MtimeMs)
			if err := os.Chtimes(target, at, at); err != nil {
				t.Fatalf("pin the time of %s: %v", target, err)
			}
		}
	}
	return env, manifest.SinceMs
}

// loadGoldenFiles reads every recording, ordered by its seq.
func loadGoldenFiles(t *testing.T) []goldenFile {
	t.Helper()
	names, err := filepath.Glob(filepath.Join("..", "..", "radar", "testdata", "golden", "*.json"))
	if err != nil || len(names) == 0 {
		t.Fatalf("read the golden dir: %v", err)
	}
	files := make([]goldenFile, 0, len(names))
	for _, name := range names {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		var file goldenFile
		if err := json.Unmarshal(raw, &file); err != nil {
			t.Fatalf("decode %s: %v", name, err)
		}
		files = append(files, file)
	}
	slices.SortFunc(files, func(a, b goldenFile) int { return a.Seq - b.Seq })
	return files
}

// askGolden replays one recording against the mounted tab; a stream answers with its retry line
// and snapshot frame, then the client walks away.
func askGolden(t *testing.T, client *http.Client, base string, file goldenFile, variant goldenVariant) (int, string, string) {
	t.Helper()
	method := file.Method
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if variant.Sent != "" {
		body = strings.NewReader(variant.Sent)
	}
	request, err := http.NewRequest(method, base+Prefix+file.Route+variant.Query, body)
	if err != nil {
		t.Fatalf("build the %s request: %v", file.Route, err)
	}
	if variant.Sent != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("the %s%s request failed: %v", file.Route, variant.Query, err)
	}
	defer func() { _ = response.Body.Close() }()
	contentType := response.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "text/event-stream") {
		reader := bufio.NewReader(response.Body)
		var out strings.Builder
		sawData := false
		for {
			line, err := reader.ReadString('\n')
			out.WriteString(line)
			if strings.HasPrefix(line, "data: ") {
				sawData = true
			}
			if err != nil || (sawData && line == "\n") {
				break
			}
		}
		return response.StatusCode, contentType, out.String()
	}
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read the %s%s answer: %v", file.Route, variant.Query, err)
	}
	return response.StatusCode, contentType, string(raw)
}

// TestGoldenParity holds the mounted tab to radar's committed recordings: same fixture state,
// same requests under /api/radar, same bytes.
func TestGoldenParity(t *testing.T) {
	env, sinceMs := buildGoldenState(t)
	files := loadGoldenFiles(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	port := listener.Addr().(*net.TCPAddr).Port

	lines := &logRecorder{}
	mux := http.NewServeMux()
	tab := Mount(context.Background(), mux, env["RADAR_HOME"], MountOptions{
		Env:            env,
		Version:        "golden",
		Port:           port,
		SinceMs:        sinceMs,
		IntervalMs:     quietMs,
		InsightsTickMs: quietMs,
		NotifyRunner:   func(string, []string) {},
		Log:            lines.record,
	})
	t.Cleanup(tab.Stop)
	if !tab.Owns {
		t.Fatalf("the tab did not take the lock: %s", lines.joined())
	}
	httpSrv := &http.Server{Handler: mux}
	serveDone := make(chan struct{})
	go func() { defer close(serveDone); _ = httpSrv.Serve(listener) }()
	t.Cleanup(func() { _ = httpSrv.Close(); <-serveDone })

	waitFor(t, "the fixture in the store", 15*time.Second, func() bool {
		ids := []string{}
		for _, session := range tab.Store.SessionList() {
			ids = append(ids, session.ID)
		}
		return slices.Contains(ids, "sess-a") && slices.Contains(ids, "zai:goldjob") && !tab.Watcher.CatchingUp()
	})
	if tab.Writer == nil {
		t.Fatal("the history writer did not attach; the history routes have nothing to read")
	}
	tab.Writer.Flush()

	client := &http.Client{Timeout: 30 * time.Second}
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	for _, file := range files {
		for _, variant := range file.Variants {
			status, contentType, body := askGolden(t, client, base, file, variant)
			label := Prefix + file.Route + variant.Query
			if status != variant.Status {
				t.Errorf("%s: status %d, want %d", label, status, variant.Status)
			}
			if contentType != variant.ContentType {
				t.Errorf("%s: content type %q, want %q", label, contentType, variant.ContentType)
			}
			if have := goldenBody(file.Route, body); have != variant.Body {
				t.Errorf("%s: the answer diverges from the golden:\nwant %s\ngot  %s",
					label, variant.Body, have)
			}
		}
	}
}

// logRecorder collects the lines a mount logs.
type logRecorder struct {
	lines []string
}

func (r *logRecorder) record(line string) { r.lines = append(r.lines, line) }

func (r *logRecorder) joined() string { return strings.Join(r.lines, "\n") }

// waitFor polls probe until it holds or the budget passes.
func waitFor(t *testing.T, label string, budget time.Duration, probe func() bool) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		if probe() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", label)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
