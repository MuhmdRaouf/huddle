// Golden parity of the HTTP API and the MCP endpoints: the committed goldens under testdata/golden
// pin the exact bytes of every answer over the channel testdata/fixture/script.json builds; this
// test builds the same state in a child server, replays every api and mcp recording in seq order
// and wants the same bytes. The fields that carry the wall clock, the process or a minted secret
// are marked "<volatile>" on both sides; the requests that name a minted invite, invite id or
// login code carry the <invite>, <inviteId> and <code> placeholders, filled with the ones this
// run mints itself. A mismatch prints a diff.
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
	"strings"
	"testing"
	"time"
)

// goldenEntry is one recorded request with its answer.
type goldenEntry struct {
	Seq     int    `json:"seq"`
	Kind    string `json:"kind"` // api | mcp | stdio
	Name    string `json:"name"`
	Method  string `json:"method"`
	Path    string `json:"path"`
	Auth    string `json:"auth"` // root | none | member | bad
	Sent    string `json:"sent"`
	Host    string `json:"host"`
	Origin  string `json:"origin"`
	SendCT  string `json:"sendContentType"`
	Channel string `json:"channel"`
	As      string `json:"as"`

	Status        int    `json:"status"`
	ContentType   string `json:"contentType"`
	Body          string `json:"body"`
	Location      string `json:"location"`
	SetCookie     string `json:"setCookie"`
	SessionHeader bool   `json:"sessionHeader"`
}

// goldenScript is the committed fixture plan: one channel, the ops that fill it, the tool calls
// the recordings make.
type goldenScript struct {
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

// The volatile marks, exactly as the recordings carry them: a wall-clock or process field, the
// home the server runs from (a path of the machine that runs it), a credential, an invite, a
// cookie; the ids the auth routes mint themselves, and the machine /health names (the host that
// runs the test).
var (
	goldenISO    = regexp.MustCompile(`20\d{2}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z`)
	goldenCred   = regexp.MustCompile(`hcred_[A-Za-z0-9_-]+`)
	goldenInvite = regexp.MustCompile(`\b[a-z0-9]{6}\.[a-z0-9]{16}\b`)
	goldenCookie = regexp.MustCompile(`huddle_session_\d+=[^;\s]+`)
	goldenHome   = regexp.MustCompile(`"home":"[^"]*"`)
)

var goldenPerRoute = []struct {
	prefix string
	re     *regexp.Regexp
}{
	{"/api/tokens", regexp.MustCompile(`"(id|token|deleted)":"[^"]*"`)},
	{"/api/members", regexp.MustCompile(`"(invite)":"[^"]*"`)},
	{"/api/login", regexp.MustCompile(`"(code)":"[^"]*"`)},
	{"/health", regexp.MustCompile(`"(machine|executable)":"[^"]*"`)},
	{"/health", regexp.MustCompile(`"(pid)":\d+`)},
	{"/health", regexp.MustCompile(`"(lastOkAt|lastErrAt)":\d+`)},
}

// goldenMark replaces the volatile fields of one answer, the same rules on both sides.
func goldenMark(path, body string) string {
	clean := strings.SplitN(path, "?", 2)[0]
	route := clean
	if ok, _ := regexp.MatchString(`^/api/(tokens|members)/[^/]+$`, clean); ok {
		route = clean[:strings.LastIndex(clean, "/")]
	}
	out := goldenISO.ReplaceAllLiteralString(body, "<volatile>")
	out = goldenCred.ReplaceAllLiteralString(out, "<volatile>")
	out = goldenInvite.ReplaceAllLiteralString(out, "<volatile>")
	out = goldenCookie.ReplaceAllLiteralString(out, "<volatile>")
	out = goldenHome.ReplaceAllLiteralString(out, `"home":"<volatile>"`)
	for _, rule := range goldenPerRoute {
		if route == rule.prefix || strings.HasPrefix(route, rule.prefix+"/") {
			out = rule.re.ReplaceAllString(out, `"$1":"<volatile>"`)
		}
	}
	return out
}

// goldenVersion answers the version the recordings name. It is fixed, not read from plugin.json, so a
// plugin version bump never changes what the server is compared against.
func goldenVersion(t *testing.T) string {
	t.Helper()
	return "0.0.2"
}

// buildGoldenChannel creates the fixture channel and applies every op, the state all recordings
// start from.
func buildGoldenChannel(t *testing.T, srv *childServer) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "fixture", "script.json"))
	if err != nil {
		t.Fatalf("read the fixture script: %v", err)
	}
	var script goldenScript
	if err := json.Unmarshal(raw, &script); err != nil {
		t.Fatalf("decode the fixture script: %v", err)
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
	status, _, body, err := srv.request(bg, http.MethodPost, "/api/channels", string(configure),
		map[string]string{"content-type": "application/json", "x-huddle-token": srv.token})
	if err != nil || status != 200 {
		t.Fatalf("create the golden channel: %d %v %s", status, err, body)
	}
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
		path := fmt.Sprintf("/api/c/%s/op/%s?as=%s", script.Channel, step.Op, step.As)
		status, _, body, err := srv.request(bg, http.MethodPost, path, string(step.Args),
			map[string]string{"content-type": "application/json", "x-huddle-token": srv.token})
		if err != nil || status != 200 {
			t.Fatalf("op %s as %s: %d %v %s", step.Op, step.As, status, err, body)
		}
	}
}

// goldenMints holds the secrets this run minted for the requests that need them.
type goldenMints struct {
	invite     string
	memberCred string
	code       string
}

// fill substitutes a golden's placeholders with this run's secrets.
func (m *goldenMints) fill(text string) string {
	return strings.NewReplacer("<invite>", m.invite, "<inviteId>", strings.SplitN(m.invite, ".", 2)[0], "<code>", m.code).Replace(text)
}

// keep reads the secret a minting answer carries, the way the recordings' placeholders stand
// for it.
func (m *goldenMints) keep(name, body string) {
	var fields map[string]any
	if err := json.Unmarshal([]byte(body), &fields); err != nil {
		return
	}
	switch name {
	case "tokens-create", "tokens-create-plain":
		m.invite, _ = fields["token"].(string)
	case "join":
		m.memberCred, _ = fields["credential"].(string)
	case "login":
		m.code, _ = fields["code"].(string)
	}
}

// goldenStreamBody reads a stream's first frame (the retry line and hello), then walks away.
func goldenStreamBody(t *testing.T, body io.ReadCloser) string {
	t.Helper()
	defer func() { _ = body.Close() }()
	reader := bufio.NewReader(body)
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
	return out.String()
}

// replayGolden performs one recorded request against the running server and answers what came back.
func replayGolden(t *testing.T, srv *childServer, entry goldenEntry, mints *goldenMints) (int, http.Header, string) {
	t.Helper()
	path := mints.fill(entry.Path)
	var payload io.Reader
	if entry.Sent != "" {
		payload = strings.NewReader(mints.fill(entry.Sent))
	}
	request, err := http.NewRequest(entry.Method, srv.base+path, payload)
	if err != nil {
		t.Fatalf("build the %s request: %v", entry.Name, err)
	}
	if entry.Host != "" {
		request.Host = entry.Host
	}
	if entry.Origin != "" {
		request.Header.Set("Origin", entry.Origin)
	}
	switch entry.Auth {
	case "", "root":
		request.Header.Set("x-huddle-token", srv.token)
	case "member":
		request.Header.Set("x-huddle-token", mints.memberCred)
	case "bad":
		request.Header.Set("x-huddle-token", "not-a-credential")
	}
	if entry.Sent != "" && entry.SendCT == "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if entry.SendCT != "" {
		request.Header.Set("Content-Type", entry.SendCT)
	}
	// the recordings predate the action header; a replay of a write names its route the way the
	// clients do, so the recordings pin the answers, not the transport boilerplate
	if entry.Method != http.MethodGet && entry.Method != http.MethodHead && entry.Method != http.MethodOptions {
		request.Header.Set(ActionHeader, strings.SplitN(path, "?", 2)[0])
	}
	client := &http.Client{
		Transport:     &http.Transport{DisableKeepAlives: true},
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("the %s request failed: %v", entry.Name, err)
	}
	header := response.Header
	if strings.HasPrefix(header.Get("Content-Type"), "text/event-stream") {
		return response.StatusCode, header, goldenStreamBody(t, response.Body)
	}
	raw, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatalf("read the %s answer: %v", entry.Name, err)
	}
	return response.StatusCode, header, string(raw)
}

// TestGoldenHTTPReplay holds the server to the committed recordings: same fixture state, same
// requests, same bytes. The stdio recording is the bridge suite's (internal/huddle/cli).
func TestGoldenHTTPReplay(t *testing.T) {
	srv := startServer(t, map[string]string{"HUDDLE_TEST_VERSION": goldenVersion(t)})
	buildGoldenChannel(t, srv)

	names, err := filepath.Glob(filepath.Join("..", "testdata", "golden", "*.json"))
	if err != nil || len(names) == 0 {
		t.Fatalf("read the golden dir: %v", err)
	}
	entries := []goldenEntry{}
	for _, name := range names {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		var entry goldenEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			t.Fatalf("decode %s: %v", name, err)
		}
		entries = append(entries, entry)
	}
	slices.SortFunc(entries, func(a, b goldenEntry) int { return a.Seq - b.Seq })

	mints := &goldenMints{}
	for _, entry := range entries {
		if entry.Kind == "stdio" {
			continue
		}
		status, header, body := replayGolden(t, srv, entry, mints)
		label := fmt.Sprintf("%s (%s%s)", entry.Name, entry.Method, entry.Path)
		if status != entry.Status {
			t.Errorf("%s: status %d, want %d", label, status, entry.Status)
		}
		if got := header.Get("Content-Type"); got != entry.ContentType {
			t.Errorf("%s: content type %q, want %q", label, got, entry.ContentType)
		}
		if got := header.Get("Location"); got != entry.Location {
			t.Errorf("%s: location %q, want %q", label, got, entry.Location)
		}
		if marked := goldenMark(entry.Path, header.Get("Set-Cookie")); marked != entry.SetCookie {
			t.Errorf("%s: set-cookie %q, want %q", label, marked, entry.SetCookie)
		}
		if entry.SessionHeader && header.Get("mcp-session-id") == "" {
			t.Errorf("%s: no mcp-session-id header, want one", label)
		}
		if marked := goldenMark(entry.Path, body); marked != entry.Body {
			t.Errorf("%s: the answer diverges from the golden:\n%s", label, goldenDiff(entry.Body, marked))
		}
		mints.keep(entry.Name, body)
	}
}

// goldenDiff prints a line diff the way diff -u would: one shared prefix, the changed middle.
func goldenDiff(want, have string) string {
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
