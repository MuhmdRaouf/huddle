// The compat suite: the recorded huddle home in testdata/legacy_home — a home an earlier
// release's server built through its own API over a temp dir (three channels: a plan with
// dependencies, edits, notes, asks, a pause, a turn pass, knowledge verified, superseded and
// shared server-wide, touches, an approval request; members joined through single-use invites,
// two browsers signed in, the notify setting and the home's port file saved) — opened by this
// server. Every channel lists, searches its full-text indexes and serves its rows, the recorded
// credentials log in, the only files that change are the ones a running server owns, and a stop
// followed by a fresh start answers the same, with nothing moved twice. The one invite that
// stays live is pinned to a fixed secret at recording time, and fixture.json lists the
// recording's own facts (every plaintext in it a fake).
package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
)

// The fixture's own facts: three channels, the tasks the routes read in full, the words the
// full-text searches find, and how far past the recording the pinned clock sits.
const (
	compatAtelier = "atelier"
	compatRelay   = "relay"
	compatOrbiter = "orbiter"
	compatPinned  = 30 * time.Second
)

// compatChannel is one channel's share of the route list: its task read in full, the word its
// task search finds, the word its knowledge search finds.
type compatChannel struct {
	name   string
	task   string
	search string
	kb     string
}

var compatChannels = []compatChannel{
	{compatAtelier, "wire-harness", "zephyr", "zephyr"},
	{compatRelay, "relay-mount", "mast", "breaker"},
	{compatOrbiter, "polish-mirror", "mirror", "dawn"},
}

// compatOwned is every file a running server writes into a home it opened, with the reason. The
// channels' -wal/-shm pairs join by prefix (they come and go with each open).
var compatOwned = map[string]string{
	"data/auth.json":    "re-opened: the root digest re-recorded, the members' last-seen rewrites",
	"data/shared.db":    "the server-wide knowledge store's earlier spelling, moved onto knowledge.db at the first open",
	"data/knowledge.db": "the server-wide knowledge store, opened with the first channel",
	"home/hub.db":       "the registry every session's reports build, opened with the server",
	"home/hub.db-wal":   "the registry's write-ahead log while the server runs",
	"home/hub.db-shm":   "the registry's shared-memory index while the server runs",
}

// compatOwnedPath says whether one path (a changed file, maybe marked removed) belongs to the
// running server: an owned file by name, or a channel's database with its WAL pair.
func compatOwnedPath(path string) bool {
	owned := strings.TrimSuffix(path, " (removed)")
	if _, ok := compatOwned[owned]; ok {
		return true
	}
	if strings.HasPrefix(owned, "data/channels/") {
		return strings.HasSuffix(owned, ".db") || strings.HasSuffix(owned, ".db-wal") ||
			strings.HasSuffix(owned, ".db-shm")
	}
	return owned == "data/shared.db-wal" || owned == "data/shared.db-shm" ||
		owned == "data/knowledge.db-wal" || owned == "data/knowledge.db-shm"
}

// compatFixture is the manifest fixture.json beside the home: the recording instant and
// every fake plaintext a restart needs to log in again.
type compatFixture struct {
	T0      int64             `json:"t0"`
	Root    string            `json:"root"`
	Invite  string            `json:"invite"`
	Members map[string]string `json:"members"`
	Cookie  struct {
		Root   string `json:"root"`
		Member struct {
			Name string `json:"name"`
			Cred string `json:"cred"`
		} `json:"member"`
	} `json:"cookie"`
	Channels []string `json:"channels"`
}

// compatLoadFixture reads the manifest and checks it holds its facts.
func compatLoadFixture(t *testing.T) compatFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "legacy_home", "fixture.json"))
	if err != nil {
		t.Fatalf("read the fixture manifest: %v", err)
	}
	var f compatFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("parse the fixture manifest: %v", err)
	}
	if f.Root == "" || f.Invite == "" || len(f.Members) != 4 || f.Cookie.Root == "" || f.Cookie.Member.Cred == "" {
		t.Fatalf("the fixture manifest holds its facts: %+v", f)
	}
	return f
}

// compatWorld copies the recorded home into a fresh world and answers its root, home and data.
func compatWorld(t *testing.T) (root, home, data string) {
	t.Helper()
	testdata := filepath.Join("..", "testdata", "legacy_home")
	root = t.TempDir()
	compatCopyTree(t, filepath.Join(testdata, "home"), filepath.Join(root, "home"))
	compatCopyTree(t, filepath.Join(testdata, "data"), filepath.Join(root, "data"))
	return root, filepath.Join(root, "home"), filepath.Join(root, "data")
}

// compatCopyTree copies one directory tree into a fresh target, files and permission bits whole.
func compatCopyTree(t *testing.T, from, to string) {
	t.Helper()
	info, err := os.Stat(from)
	if err != nil {
		t.Fatalf("stat %s: %v", from, err)
	}
	if err := os.MkdirAll(to, info.Mode().Perm()); err != nil {
		t.Fatalf("mkdir %s: %v", to, err)
	}
	err = filepath.WalkDir(from, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		target := filepath.Join(to, rel)
		if entry.IsDir() {
			mode := entry.Type().Perm()
			if mode == 0 {
				mode = info.Mode().Perm()
			}
			return os.MkdirAll(target, mode)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		writeInfo, err := entry.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(target, body, writeInfo.Mode().Perm())
	})
	if err != nil {
		t.Fatalf("copy %s to %s: %v", from, to, err)
	}
}

// compatHashTree answers every file under root by content hash.
func compatHashTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		out[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatalf("hash %s: %v", root, err)
	}
	return out
}

// compatChangedFiles lists what moved between two hashings, marked when a file went missing.
func compatChangedFiles(before, after map[string]string) []string {
	changed := []string{}
	for path, sum := range after {
		if held, ok := before[path]; !ok || held != sum {
			changed = append(changed, path)
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			changed = append(changed, path+" (removed)")
		}
	}
	sort.Strings(changed)
	return changed
}

// compatRequireChanged fails unless every change sits among the files a running server owns.
func compatRequireChanged(t *testing.T, label string, changed []string) {
	t.Helper()
	unexpected := []string{}
	for _, path := range changed {
		if compatOwnedPath(path) {
			continue
		}
		unexpected = append(unexpected, path)
	}
	if len(unexpected) > 0 {
		t.Fatalf("%s changed files the server does not own: %v (all changes: %v)", label, unexpected, changed)
	}
}

// compatFreePort reserves and releases a loopback port away from the routers' 18787-18791.
func compatFreePort(t *testing.T) int {
	t.Helper()
	for {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("reserve a port: %v", err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		_ = listener.Close()
		if port < 18787 || port > 18791 {
			return port
		}
	}
}

// compatRun is one server over the fixture: its port and how to stop it.
type compatRun struct {
	port int
	stop func()
}

// compatServe runs one server over the recorded home until stop, its clock pinned at now: the
// credential bookkeeping and /health's start ride the pinned clock, while the channels' own
// staleness windows ride the wall clock, so the assertions never pin a freshness.
func compatServe(t *testing.T, token, home, data string, now time.Time) *compatRun {
	t.Helper()
	port := compatFreePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, Options{
			Version: "compat", Port: port, Home: home, Data: data, Token: token,
			AutoCreate: true, Now: func() time.Time { return now },
			Stdout: io.Discard, Stderr: io.Discard,
		})
	}()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(15 * time.Second)
	for {
		response, err := http.Get(base + "/health")
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode == 200 {
				return &compatRun{port: port, stop: func() {
					cancel()
					select {
					case <-done:
					case <-time.After(15 * time.Second):
						t.Fatalf("the server never stopped")
					}
				}}
			}
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("the server never answered /health: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// compatFetch reads one answer whole with the given credential: a token header, a cookie, or neither.
func compatFetch(t *testing.T, run *compatRun, path, token, cookie string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", run.port, path), nil)
	if err != nil {
		t.Fatalf("build the %s request: %v", path, err)
	}
	if token != "" {
		request.Header.Set("x-huddle-token", token)
	}
	if cookie != "" {
		request.Header.Set("Cookie", cookie)
	}
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("the %s request failed: %v", path, err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read the %s answer: %v", path, err)
	}
	return response.StatusCode, string(body)
}

// compatPost writes one JSON body with the given credential.
func compatPost(t *testing.T, run *compatRun, path, token, body string) (int, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d%s", run.port, path),
		strings.NewReader(body))
	if err != nil {
		t.Fatalf("build the %s request: %v", path, err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(ActionHeader, strings.SplitN(path, "?", 2)[0])
	if token != "" {
		request.Header.Set("x-huddle-token", token)
	}
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("the %s request failed: %v", path, err)
	}
	defer func() { _ = response.Body.Close() }()
	out, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read the %s answer: %v", path, err)
	}
	return response.StatusCode, string(out)
}

// compatObject decodes one answer body.
func compatObject(t *testing.T, path, body string) map[string]any {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("the %s answer is not a JSON object: %v (%s)", path, err, body)
	}
	return parsed
}

// compatArray decodes one answer body that is a list.
func compatArray(t *testing.T, path, body string) []any {
	t.Helper()
	var parsed []any
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("the %s answer is not a JSON list: %v (%s)", path, err, body)
	}
	return parsed
}

// compatList reads one array field of an answer.
func compatList(t *testing.T, path string, object map[string]any, key string) []any {
	t.Helper()
	value, ok := object[key]
	if !ok {
		t.Fatalf("the %s answer carries no %q: %v", path, key, object)
	}
	list, ok := value.([]any)
	if !ok {
		t.Fatalf("the %s answer's %q is not a list: %v", path, key, value)
	}
	return list
}

// compatObjectAt reads one object field of an answer.
func compatObjectAt(t *testing.T, path string, object map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := object[key]
	if !ok {
		t.Fatalf("the %s answer carries no %q: %v", path, key, object)
	}
	nested, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("the %s answer's %q is not an object: %v", path, key, value)
	}
	return nested
}

// compatNumber reads one number field of an answer.
func compatNumber(t *testing.T, path string, object map[string]any, key string) float64 {
	t.Helper()
	value, ok := object[key]
	if !ok {
		t.Fatalf("the %s answer carries no %q: %v", path, key, object)
	}
	number, ok := value.(float64)
	if !ok {
		t.Fatalf("the %s answer's %q is not a number: %v", path, key, value)
	}
	return number
}

// compatString reads one string field of an answer.
func compatString(t *testing.T, path string, object map[string]any, key string) string {
	t.Helper()
	value, ok := object[key]
	if !ok {
		t.Fatalf("the %s answer carries no %q: %v", path, key, object)
	}
	text, ok := value.(string)
	if !ok {
		t.Fatalf("the %s answer's %q is not a string: %v", path, key, value)
	}
	return text
}

// compatFieldStrings lists one string field of every row.
func compatFieldStrings(rows []any, key string) []string {
	out := []string{}
	for _, row := range rows {
		if object, ok := row.(map[string]any); ok {
			if value, ok := object[key].(string); ok {
				out = append(out, value)
			}
		}
	}
	return out
}

// compatSorted joins a list sorted, for one-line comparisons.
func compatSorted(values []string) string {
	sorted := append([]string{}, values...)
	sort.Strings(sorted)
	return strings.Join(sorted, ",")
}

// compatRoutes is every read the suite makes over one run, all as the root credential.
func compatRoutes() []string {
	routes := []string{
		"/health",
		"/api/channels",
		"/api/whoami",
		"/api/members",
		"/api/tokens",
		"/api/settings",
	}
	for _, ch := range compatChannels {
		base := "/api/c/" + ch.name
		routes = append(routes,
			base, base+"/board", base+"/sessions", base+"/timeline", base+"/search?q="+ch.search,
			base+"/plan.json", base+"/review", base+"/attention", base+"/conflicts",
			base+"/kb?q="+ch.kb, base+"/knowledge.md", base+"/x/rules", base+"/x/approvals",
			base+"/x/radar", base+"/task/"+ch.task)
	}
	return routes
}

// TestCompatLegacyHome opens the recorded home and proves the compat rules: every channel lists,
// searches its full-text indexes and serves its rows with the recorded credentials logging in,
// only the server's own files change, and a stop followed by a fresh start answers the same,
// with nothing moved twice.
func TestCompatLegacyHome(t *testing.T) {
	t.Setenv("RADAR_HOME", filepath.Join(t.TempDir())) // no radar of this machine answers the radar view
	fixture := compatLoadFixture(t)
	world, home, data := compatWorld(t)
	now := time.UnixMilli(fixture.T0).Add(compatPinned)

	// the recorded credentials log in: the members as themselves and their subagents, the two
	// browsers as the owner, and nobody else's name
	assertLogins := func(run *compatRun) {
		for name, cred := range fixture.Members {
			status, body := compatFetch(t, run, "/api/whoami", cred, "")
			if status != 200 {
				t.Fatalf("the %s credential answered %d: %s", name, status, body)
			}
			who := compatObject(t, "/api/whoami", body)
			if compatString(t, "/api/whoami", who, "name") != name || who["root"] != false {
				t.Fatalf("the %s credential logs in as %s: %v", name, name, who)
			}
		}
		atlas := fixture.Members["atlas"]
		if status, _ := compatFetch(t, run, "/api/c/atelier/board?as=atlas.scout", atlas, ""); status != 200 {
			t.Fatalf("a member's credential acts as its subagent: %d", status)
		}
		if status, _ := compatFetch(t, run, "/api/c/atelier/board?as=beacon", atlas, ""); status != 403 {
			t.Fatalf("a member's credential may not act as another session: %d", status)
		}
		if status, _ := compatFetch(t, run, "/api/channels", "not-a-credential", ""); status != 401 {
			t.Fatalf("an unknown credential is refused: %d", status)
		}
		cookie := func(value string) string {
			return fmt.Sprintf("huddle_session_%d=%s", run.port, value)
		}
		status, body := compatFetch(t, run, "/api/whoami", "", cookie(fixture.Cookie.Root))
		who := compatObject(t, "/api/whoami", body)
		if status != 200 || compatString(t, "/api/whoami", who, "name") != "owner" || who["root"] != true {
			t.Fatalf("the recorded root browser signs in: %d %v", status, who)
		}
		status, body = compatFetch(t, run, "/api/whoami", "", cookie(fixture.Cookie.Member.Cred))
		who = compatObject(t, "/api/whoami", body)
		if status != 200 || who["root"] != false || compatString(t, "/api/whoami", who, "browser") != fixture.Cookie.Member.Name {
			t.Fatalf("the recorded member browser signs in without admin rights: %d %v", status, who)
		}
		if status, _ := compatFetch(t, run, "/api/tokens", "", cookie(fixture.Cookie.Member.Cred)); status != 403 {
			t.Fatalf("the member's browser gets no admin rights: %d", status)
		}
	}

	// startOnce runs one server over the home: the answers it served and the tree it started from.
	// logins says the run first proves the recorded credentials (their last-seen moves to the
	// pinned clock, so the answers the two runs compare agree); detail says the run also reads one
	// knowledge entry in full — a read that counts its hits, so only the first run makes it.
	startOnce := func(logins, detail bool) (map[string]string, map[string]string) {
		before := compatHashTree(t, world)
		run := compatServe(t, fixture.Root, home, data, now)
		if logins {
			assertLogins(run)
		}
		answers := map[string]string{}
		for _, route := range compatRoutes() {
			status, body := compatFetch(t, run, route, fixture.Root, "")
			if status != 200 {
				t.Fatalf("%s answered %d, want 200: %s", route, status, body)
			}
			answers[route] = body
			t.Logf("%s -> %s", route, body)
		}
		if detail {
			recall := compatArray(t, "/api/c/atelier/kb", answers["/api/c/atelier/kb?q=zephyr"])
			first, _ := recall[0].(map[string]any)
			id := compatNumber(t, "/api/c/atelier/kb", first, "id")
			path := fmt.Sprintf("/api/c/atelier/kb/%d", int64(id))
			status, body := compatFetch(t, run, path, fixture.Root, "")
			if status != 200 {
				t.Fatalf("%s answered %d, want 200: %s", path, status, body)
			}
			answers["kb-detail"] = body
		}
		run.stop()
		return answers, before
	}

	// assertRows checks the answers against the fixture's own rows.
	assertRows := func(answers map[string]string) {
		health := compatObject(t, "/health", answers["/health"])
		if health["ok"] != true || compatString(t, "/health", health, "version") != "compat" ||
			compatNumber(t, "/health", health, "channels") != 3 ||
			compatString(t, "/health", health, "started") != now.UTC().Format("2006-01-02T15:04:05.000Z") {
			t.Fatalf("health names this run: %v", health)
		}

		channels := compatArray(t, "/api/channels", answers["/api/channels"])
		if got := compatSorted(compatFieldStrings(channels, "name")); got != "atelier,orbiter,relay" {
			t.Fatalf("the channel list holds the fixture's three channels: %v", got)
		}
		var atelier map[string]any
		for _, entry := range channels {
			if object, ok := entry.(map[string]any); ok && object["name"] == compatAtelier {
				atelier = object
			}
		}
		if atelier == nil || compatString(t, "/api/channels", atelier, "title") != "The Loom Atelier" ||
			compatString(t, "/api/channels", atelier, "turn") != "beacon" {
			t.Fatalf("the atelier entry carries its config and its turn: %v", atelier)
		}
		live := compatFieldStrings(compatList(t, "/api/channels", atelier, "sessions"), "name")
		for _, want := range []string{"atlas", "beacon", "cyrus", "atlas.scout"} {
			if !strings.Contains(","+compatSorted(live)+",", ","+want+",") {
				t.Fatalf("the atelier sessions miss %s: %v", want, live)
			}
		}
		if strings.Contains(compatSorted(live), "delta") {
			t.Fatalf("a session that left is not live: %v", live)
		}
		stats := compatObjectAt(t, "/api/channels", atelier, "stats")
		// done counts the finished and the skipped: the harness finished, the hull ships unpainted
		if compatNumber(t, "/api/channels", stats, "tasks") != 4 || compatNumber(t, "/api/channels", stats, "done") != 2 {
			t.Fatalf("the atelier stats hold its four tasks, two behind it: %v", stats)
		}

		who := compatObject(t, "/api/whoami", answers["/api/whoami"])
		if compatString(t, "/api/whoami", who, "name") != "owner" || who["root"] != true || who["invite"] != true {
			t.Fatalf("the root credential logs in as the owner: %v", who)
		}

		members := compatFieldStrings(compatArray(t, "/api/members", answers["/api/members"]), "name")
		if got := compatSorted(members); got != "atlas,beacon,cyrus,delta" {
			t.Fatalf("the member list holds the four who joined: %v", got)
		}

		invites := compatArray(t, "/api/tokens", answers["/api/tokens"])
		if len(invites) != 1 {
			t.Fatalf("one invite stays live, got %v", invites)
		}
		invite, _ := invites[0].(map[string]any)
		if compatString(t, "/api/tokens", invite, "description") != "the fixture's live invite" ||
			invite["expires"] != nil || invite["single_use"] != false || compatNumber(t, "/api/tokens", invite, "uses") != 0 {
			t.Fatalf("the live invite is the pinned one: %v", invite)
		}

		settings := compatObject(t, "/api/settings", answers["/api/settings"])
		if settings["notify"] != true {
			t.Fatalf("the recorded setting answers: %v", settings)
		}

		info := compatObject(t, "/api/c/atelier", answers["/api/c/atelier"])
		config := compatObjectAt(t, "/api/c/atelier", info, "config")
		if compatString(t, "/api/c/atelier", config, "orchestrator") != "atlas" ||
			compatString(t, "/api/c/atelier", config, "start") != "atlas" {
			t.Fatalf("the channel info carries its configuration: %v", info)
		}

		board := compatObject(t, "/api/c/atelier/board", answers["/api/c/atelier/board"])
		if got := compatSorted(compatFieldStrings(compatList(t, "/api/c/atelier/board", board, "phases"), "title")); got != "Finish the cloth,Frame the loom" {
			t.Fatalf("the board holds both phases: %v", got)
		}
		steps := compatList(t, "/api/c/atelier/board", board, "steps")
		if got := compatSorted(compatFieldStrings(steps, "id")); got != "loom-checks,paint-hull,stitch-panel,wire-harness" {
			t.Fatalf("the board holds the four steps: %v", got)
		}
		want := map[string]string{"wire-harness": "done", "loom-checks": "doing", "stitch-panel": "blocked", "paint-hull": "skipped"}
		for _, row := range steps {
			step, _ := row.(map[string]any)
			if status, ok := want[compatString(t, "/api/c/atelier/board", step, "id")]; ok && compatString(t, "/api/c/atelier/board", step, "status") != status {
				t.Fatalf("the step's status: %v", step)
			}
		}
		meta := compatObjectAt(t, "/api/c/atelier/board", board, "meta")
		if meta["fts"] != true || compatString(t, "/api/c/atelier/board", meta, "title") != "The Loom Atelier" {
			t.Fatalf("the board's meta says the indexes are live: %v", meta)
		}

		sessions := compatList(t, "/api/c/atelier/sessions",
			compatObject(t, "/api/c/atelier/sessions", answers["/api/c/atelier/sessions"]), "sessions")
		for _, row := range sessions {
			sess, _ := row.(map[string]any)
			switch compatString(t, "/api/c/atelier/sessions", sess, "name") {
			case "cyrus":
				if compatString(t, "/api/c/atelier/sessions", sess, "control") != "pause" {
					t.Fatalf("cyrus stays paused: %v", sess)
				}
			case "delta":
				if compatString(t, "/api/c/atelier/sessions", sess, "state") != "left" {
					t.Fatalf("delta stays left: %v", sess)
				}
			}
		}

		events := compatArray(t, "/api/c/atelier/timeline", answers["/api/c/atelier/timeline"])
		if len(events) == 0 {
			t.Fatalf("the timeline holds the channel's history")
		}
		topics := compatSorted(compatFieldStrings(events, "topic"))
		for _, want := range []string{"turn.pass", "control.pause", "kb.added", "task.status"} {
			if !strings.Contains(topics, want) {
				t.Fatalf("the timeline misses a %s event: %v", want, topics)
			}
		}

		hits := compatArray(t, "/api/c/atelier/search?q=zephyr", answers["/api/c/atelier/search?q=zephyr"])
		found := ""
		for _, row := range hits {
			hit, _ := row.(map[string]any)
			if compatString(t, "/api/c/atelier/search?q=zephyr", hit, "id") == "wire-harness" {
				found = compatString(t, "/api/c/atelier/search?q=zephyr", hit, "hit")
			}
		}
		if found == "" || !strings.Contains(found, "«") {
			t.Fatalf("the task search finds the harness with its snippet marks: %v", hits)
		}

		task := compatObject(t, "/api/c/atelier/task/wire-harness", answers["/api/c/atelier/task/wire-harness"])
		if compatString(t, "/api/c/atelier/task/wire-harness", task, "status") != "done" ||
			compatString(t, "/api/c/atelier/task/wire-harness", task, "owner") != "atlas" ||
			compatString(t, "/api/c/atelier/task/wire-harness", task, "what") != "Route the zephyr harness along the welded frame." {
			t.Fatalf("the task read in full carries the plan's text with the owner's edit: %v", task)
		}
		if task["ready"] != true {
			t.Fatalf("the finished task is ready: %v", task)
		}

		plan := compatObject(t, "/api/c/atelier/plan.json", answers["/api/c/atelier/plan.json"])
		if got := compatSorted(compatFieldStrings(compatList(t, "/api/c/atelier/plan.json", plan, "steps"), "id")); got != "loom-checks,paint-hull,stitch-panel,wire-harness" {
			t.Fatalf("the plan export holds every step: %v", got)
		}

		review := compatObject(t, "/api/c/atelier/review", answers["/api/c/atelier/review"])
		if len(compatList(t, "/api/c/atelier/review", review, "edits")) != 1 {
			t.Fatalf("the review shows the owner's edit: %v", review)
		}

		attention := compatObject(t, "/api/c/atelier/attention", answers["/api/c/atelier/attention"])
		asks := compatFieldStrings(compatList(t, "/api/c/atelier/attention", attention, "asks"), "msg")
		if !strings.Contains(strings.Join(asks, "\n"), "Which dye lot do I stitch with?") {
			t.Fatalf("the ask left open waits for the owner: %v", asks)
		}
		paused := compatFieldStrings(compatList(t, "/api/c/atelier/attention", attention, "paused"), "name")
		if !strings.Contains(","+compatSorted(paused)+",", ",cyrus,") {
			t.Fatalf("the paused session needs attention: %v", paused)
		}

		conflicts := compatObject(t, "/api/c/atelier/conflicts", answers["/api/c/atelier/conflicts"])
		if compatNumber(t, "/api/c/atelier/conflicts", conflicts, "window_min") != 30 {
			t.Fatalf("the conflicts panel names its window: %v", conflicts)
		}

		kb := compatArray(t, "/api/c/atelier/kb?q=zephyr", answers["/api/c/atelier/kb?q=zephyr"])
		if len(kb) == 0 {
			t.Fatalf("the knowledge search finds the zephyr entry")
		}
		entry, _ := kb[0].(map[string]any)
		if compatString(t, "/api/c/atelier/kb?q=zephyr", entry, "title") != "Zephyr thread heats past 80C" ||
			compatString(t, "/api/c/atelier/kb?q=zephyr", entry, "verified_by") != "owner" {
			t.Fatalf("the verified zephyr entry answers first: %v", entry)
		}

		export := answers["/api/c/atelier/knowledge.md"]
		for _, want := range []string{"## Team knowledge", "Zephyr thread heats past 80C", "The repeater breaker trips at 16A"} {
			if !strings.Contains(export, want) {
				t.Fatalf("the knowledge export misses %q: %s", want, export)
			}
		}

		rules := compatList(t, "/api/c/atelier/x/rules",
			compatObject(t, "/api/c/atelier/x/rules", answers["/api/c/atelier/x/rules"]), "rules")
		for _, row := range rules {
			rule, _ := row.(map[string]any)
			if compatString(t, "/api/c/atelier/x/rules", rule, "id") == "git_push" && rule["on"] != true {
				t.Fatalf("the switch the owner set: %v", rule)
			}
		}

		approvals := compatObject(t, "/api/c/atelier/x/approvals", answers["/api/c/atelier/x/approvals"])
		if _, ok := approvals["approvals"]; !ok {
			t.Fatalf("the approvals panel answers: %v", approvals)
		}

		radar := compatObject(t, "/api/c/atelier/x/radar", answers["/api/c/atelier/x/radar"])
		if radar["available"] != false {
			t.Fatalf("no radar runs here, the panel says so: %v", radar)
		}

		detail, ok := answers["kb-detail"]
		if ok {
			full := compatObject(t, "kb-detail", detail)
			if !strings.Contains(compatString(t, "kb-detail", full, "body"), "wind it cool") {
				t.Fatalf("the entry read in full carries its body: %v", full)
			}
		}

		// the relay and orbiter channels answer their own rows
		relayBoard := compatObject(t, "/api/c/relay/board", answers["/api/c/relay/board"])
		relaySteps := compatList(t, "/api/c/relay/board", relayBoard, "steps")
		if got := compatSorted(compatFieldStrings(relaySteps, "id")); got != "antenna-tune,relay-mount" {
			t.Fatalf("the relay board holds its steps: %v", got)
		}
		relayAttention := compatObject(t, "/api/c/relay/attention", answers["/api/c/relay/attention"])
		blocked := compatList(t, "/api/c/relay/attention", relayAttention, "blocked")
		if len(blocked) == 0 || !strings.Contains(fmt.Sprint(blocked[0]), "the mast bracket is cracked") {
			t.Fatalf("the relay's blocked task needs attention: %v", blocked)
		}
		orbiterTask := compatObject(t, "/api/c/orbiter/task/polish-mirror", answers["/api/c/orbiter/task/polish-mirror"])
		if compatString(t, "/api/c/orbiter/task/polish-mirror", orbiterTask, "status") != "done" {
			t.Fatalf("the orbiter's one task is done: %v", orbiterTask)
		}
	}

	first, before := startOnce(true, true)
	assertRows(first)
	compatRequireChanged(t, "the first start", compatChangedFiles(before, compatHashTree(t, world)))

	t.Run("a stop and a fresh start answer the same, with nothing moved twice", func(t *testing.T) {
		afterFirst := compatHashTree(t, world)
		second, beforeSecond := startOnce(false, false)
		assertRows(second)
		compatRequireChanged(t, "the second start", compatChangedFiles(beforeSecond, compatHashTree(t, world)))
		for _, route := range compatRoutes() {
			// the knowledge export stamps the moment of the export (the wall clock)
			if strings.HasSuffix(route, "/knowledge.md") {
				continue
			}
			if first[route] != second[route] {
				t.Fatalf("%s answered differently on the second start:\n%s\n%s", route, first[route], second[route])
			}
		}
		// nothing moved twice: every database and the auth state sit byte for byte where the first
		// stop left them
		for path, sum := range afterFirst {
			kept := strings.HasSuffix(path, ".db") || path == "data/auth.json"
			if !kept {
				continue
			}
			if again, ok := compatHashTree(t, world)[path]; !ok || again != sum {
				t.Fatalf("%s moved on the second start", path)
			}
		}
	})

	t.Run("the pinned invite still joins", func(t *testing.T) {
		_, home, data := compatWorld(t)
		run := compatServe(t, fixture.Root, home, data, now)
		defer run.stop()
		status, body := compatPost(t, run, "/api/join", "",
			fmt.Sprintf(`{"token": %q, "name": "late-joiner"}`, fixture.Invite))
		if status != 200 {
			t.Fatalf("the recorded invite joins: %d %s", status, body)
		}
		joined := compatObject(t, "/api/join", body)
		cred := compatString(t, "/api/join", joined, "credential")
		if cred == "" || compatString(t, "/api/join", joined, "name") != "late-joiner" {
			t.Fatalf("the join hands out a credential: %v", joined)
		}
		status, body = compatFetch(t, run, "/api/whoami", cred, "")
		who := compatObject(t, "/api/whoami", body)
		if status != 200 || compatString(t, "/api/whoami", who, "name") != "late-joiner" {
			t.Fatalf("the new member logs in: %d %v", status, who)
		}
	})

	t.Run("a channel file older than the schema gains the column it lacks, once", func(t *testing.T) {
		world, home, data := compatWorld(t)
		file := filepath.Join(data, "channels", compatRelay+".db")
		st, err := store.Open(context.Background(), file)
		if err != nil {
			t.Fatalf("open the relay database: %v", err)
		}
		if err := st.Run(context.Background(), "ALTER TABLE sessions DROP COLUMN brief"); err != nil {
			t.Fatalf("age the relay database: %v", err)
		}
		if _, err := st.All(context.Background(), "SELECT brief FROM sessions LIMIT 1"); err == nil {
			st.Close()
			t.Fatal("the brief column survived the drop")
		}
		if err := st.Close(); err != nil {
			t.Fatalf("close the aged database: %v", err)
		}

		before := compatHashTree(t, world)
		run := compatServe(t, fixture.Root, home, data, now)
		reads := []string{"/api/c/relay/sessions", "/api/c/relay/board"}
		firstAnswers := map[string]string{}
		for _, path := range reads {
			status, body := compatFetch(t, run, path, fixture.Root, "")
			if status != 200 {
				t.Fatalf("%s over the aged file answered %d: %s", path, status, body)
			}
			firstAnswers[path] = body
		}
		run.stop()
		compatRequireChanged(t, "the aged start", compatChangedFiles(before, compatHashTree(t, world)))

		st, err = store.Open(context.Background(), file)
		if err != nil {
			t.Fatalf("reopen the relay database: %v", err)
		}
		if _, err := st.All(context.Background(), "SELECT brief FROM sessions LIMIT 1"); err != nil {
			st.Close()
			t.Fatalf("the brief column did not come back: %v", err)
		}
		_ = st.Close()

		afterMove := compatHashTree(t, world)
		run = compatServe(t, fixture.Root, home, data, now)
		for _, path := range reads {
			status, body := compatFetch(t, run, path, fixture.Root, "")
			if status != 200 || body != firstAnswers[path] {
				t.Fatalf("%s moved after the one-time add: %d %s", path, status, body)
			}
		}
		run.stop()
		for path, sum := range afterMove {
			if !strings.HasSuffix(path, ".db") && path != "data/auth.json" {
				continue
			}
			if again, ok := compatHashTree(t, world)[path]; !ok || again != sum {
				t.Fatalf("%s moved again on the restart", path)
			}
		}
	})
}

// TestCompatFixtureHoldsItsRows checks the recorded home itself: every channel's database is
// there, the auth state keeps digests only and holds the members and the one live invite.
func TestCompatFixtureHoldsItsRows(t *testing.T) {
	dataDir := filepath.Join("..", "testdata", "legacy_home", "data")
	for _, ch := range compatChannels {
		if _, err := os.Stat(filepath.Join(dataDir, "channels", ch.name+".db")); err != nil {
			t.Fatalf("the fixture holds %s: %v", ch.name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dataDir, "shared.db")); err != nil {
		t.Fatalf("the fixture holds the shared store: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dataDir, "auth.json"))
	if err != nil {
		t.Fatalf("read the auth state: %v", err)
	}
	if bytes.Contains(raw, []byte("hcred_")) {
		t.Fatalf("the auth state stores digests only: %s", raw)
	}
	var auth struct {
		V       int `json:"v"`
		Invites []struct {
			Secret string `json:"secret"`
		} `json:"invites"`
		Members []struct {
			Name string `json:"name"`
		} `json:"members"`
	}
	if err := json.Unmarshal(raw, &auth); err != nil || auth.V != 1 || len(auth.Members) != 4 || len(auth.Invites) != 1 {
		t.Fatalf("the auth state holds its members and the one live invite: %v", err)
	}
}
