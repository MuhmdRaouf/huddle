// Golden parity of every API route: the committed goldens under testdata/golden pin the exact
// bytes of every answer over the fixture state testdata/fixture builds; this test builds the
// same state, starts this server and replays every recording, byte for byte. The fields
// that carry the wall clock or the process are marked "<volatile>" on both sides: health's
// startedAt, port and uptimeMs, the budget clock's updatedAt and periodStart, the summary's
// now. A mismatch prints a unified diff of the two bodies.
package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
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

// goldenManifest is the fixture plan testdata/fixture carries: where every committed input
// lands, and the pinned file times.
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

// goldenFile is one route's recordings, replayed in seq order across files: the reads first,
// the writes after, the history clear last.
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

// buildGoldenState builds the fixture state into temp homes: every file the manifest names at
// its place, the spool under the day's date, the pinned file times.
func buildGoldenState(t *testing.T) (domain.EnvLookup, int64) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "fixture", "manifest.json"))
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
	body, err := os.ReadFile(filepath.Join("..", "testdata", "fixture", "spool.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spool, body, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, entry := range manifest.Files {
		target := place(entry.To)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", target, err)
		}
		data, err := os.ReadFile(filepath.Join("..", "testdata", "fixture", entry.From))
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
	names, err := filepath.Glob(filepath.Join("..", "testdata", "golden", "*.json"))
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

// askGolden replays one recording against the running server. A stream answers with the retry
// line and the snapshot frame, then the client walks away.
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
	request, err := http.NewRequest(method, base+file.Route+variant.Query, body)
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

// TestGoldenRoutes holds the server to the committed recordings: same fixture state, same
// requests, same bytes.
func TestGoldenRoutes(t *testing.T) {
	env, sinceMs := buildGoldenState(t)
	files := loadGoldenFiles(t)
	removal := 3_600_000
	app := mustStart(t, env, AppOptions{
		Version:           "golden",
		SinceMs:           sinceMs,
		IntervalMs:        quietMs,
		InsightsTickMs:    quietMs,
		RemovalIntervalMs: &removal,
		Exit:              func(int) {},
		NotifyRunner:      func(string, []string) {},
	})
	defer app.Stop()
	waitFor(t, "the fixture in the store", 15*time.Second, func() bool {
		ids := sessionIDs(t, app)
		return slices.Contains(ids, "sess-a") && slices.Contains(ids, "zai:goldjob") && !app.Watcher.CatchingUp()
	})
	if app.Writer == nil {
		t.Fatal("the history writer did not attach; the history routes have nothing to read")
	}
	app.Writer.Flush()

	client := &http.Client{Timeout: 30 * time.Second}
	base := "http://127.0.0.1:" + strconv.Itoa(app.Port)
	for _, file := range files {
		for _, variant := range file.Variants {
			status, contentType, body := askGolden(t, client, base, file, variant)
			label := file.Route + variant.Query
			if status != variant.Status {
				t.Errorf("%s: status %d, want %d", label, status, variant.Status)
			}
			if contentType != variant.ContentType {
				t.Errorf("%s: content type %q, want %q", label, contentType, variant.ContentType)
			}
			have := goldenBody(file.Route, body)
			if have != variant.Body {
				at := firstDifference(variant.Body, have)
				t.Errorf("%s: the answer diverges from the golden at byte %d of %d:\n%s",
					label, at, len(variant.Body), unifiedDiff(variant.Body, have))
			}
		}
	}
}

// firstDifference names the byte offset where two texts diverge.
func firstDifference(want, have string) int {
	at := 0
	for at < len(want) && at < len(have) && want[at] == have[at] {
		at++
	}
	return at
}

// unifiedDiff prints a line diff the way diff -u would: removed lines, added lines, in order.
func unifiedDiff(want, have string) string {
	wantLines := strings.SplitAfter(want, "\n")
	haveLines := strings.SplitAfter(have, "\n")
	var out strings.Builder
	write := func(prefix string, lines []string) {
		for _, line := range lines {
			if line == "" {
				continue
			}
			out.WriteString(prefix + strings.TrimRight(line, "\n") + "\n")
		}
	}
	// one shared prefix, then the tail split by the classic LCS table
	prefix := 0
	for prefix < len(wantLines) && prefix < len(haveLines) && wantLines[prefix] == haveLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(wantLines)-prefix && suffix < len(haveLines)-prefix &&
		wantLines[len(wantLines)-1-suffix] == haveLines[len(haveLines)-1-suffix] {
		suffix++
	}
	removed := wantLines[prefix : len(wantLines)-suffix]
	added := haveLines[prefix : len(haveLines)-suffix]
	fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", prefix+1, len(removed), prefix+1, len(added))
	write("-", removed)
	write("+", added)
	return out.String()
}
