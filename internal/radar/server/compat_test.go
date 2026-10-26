// The compat suite: the recorded radar home in testdata/legacy_home — a state dir an earlier
// release wrote over the committed inputs in testdata/legacy_home_inputs — opened by this
// server. Every route answers with the rows the fixture holds, the only
// files that change are the ones a running server owns, and a stop followed by a fresh start answers the
// same, with nothing moved twice.
package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/store"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

// The fixture's own facts: two sessions under one project, a subagent, a captured prompt.
const (
	compatMainSession = "demo-sess-1"
	compatOldSession  = "demo-sess-2"
	compatRootID      = compatMainSession + "/main"
	compatScoutNode   = compatMainSession + "/scout-01"
	compatOldRootID   = compatOldSession + "/main"
	compatPromptHash  = "a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4a1b2c3d4"
	compatSinceMs     = 10 * 365 * dayMs // any run date's transcript mtimes sit inside the window
)

// compatOwnedFiles is every file a running server writes into a home it opened, with the reason; the
// day files under ledger/ join them (their names are the fixture's own dates).
var compatOwnedFiles = map[string]string{
	"port":               "the port this start bound",
	"history.db":         "re-opened: the schema version re-recorded, the re-ingested rows rewritten",
	"history.db-wal":     "the write-ahead log the database open leaves behind",
	"history.db-shm":     "the shared-memory index the database open leaves behind",
	"budget-status.json": "the insights tick rewrites it while budgets exist",
}

// compatSpoolLine is one committed spool line as the ids need it.
type compatSpoolLine struct {
	Ts        string       `json:"ts"`
	Event     string       `json:"event"`
	SessionID *string      `json:"session_id"`
	AgentID   *string      `json:"agent_id"`
	Model     *string      `json:"model"`
	Status    *json.Number `json:"status"`
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
		owned := strings.TrimSuffix(path, " (removed)")
		if _, ok := compatOwnedFiles[owned]; ok {
			continue
		}
		if strings.HasPrefix(owned, "ledger/") {
			continue // the usage ledger's day files: one per day the re-ingested requests touch
		}
		unexpected = append(unexpected, path)
	}
	if len(unexpected) > 0 {
		t.Fatalf("%s changed files the server does not own: %v (all changes: %v)", label, unexpected, changed)
	}
}

// compatSpoolLines reads every spool line of the home's dated files, oldest file first.
func compatSpoolLines(t *testing.T, home string) []compatSpoolLine {
	t.Helper()
	dir := filepath.Join(home, "spool")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the spool: %v", err)
	}
	names := []string{}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".jsonl") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	lines := []compatSpoolLine{}
	for _, name := range names {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read the spool file %s: %v", name, err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			if line == "" {
				continue
			}
			var parsed compatSpoolLine
			if err := json.Unmarshal([]byte(line), &parsed); err != nil {
				t.Fatalf("parse the spool line %q: %v", line, err)
			}
			lines = append(lines, parsed)
		}
	}
	return lines
}

// compatRouteID answers the deterministic request id the fixture's route line for one call built:
// route:<session>:<agent>:<model>:<ts>:<status>.
func compatRouteID(t *testing.T, home, session, agent, model string) string {
	t.Helper()
	for _, line := range compatSpoolLines(t, home) {
		if line.Event != "route" || line.SessionID == nil || *line.SessionID != session {
			continue
		}
		if line.AgentID == nil || *line.AgentID != agent || line.Model == nil || *line.Model != model {
			continue
		}
		ts, err := time.Parse(time.RFC3339, line.Ts)
		if err != nil {
			t.Fatalf("parse the route line ts %q: %v", line.Ts, err)
		}
		status := ""
		if line.Status != nil {
			status = line.Status.String()
		}
		return fmt.Sprintf("route:%s:%s:%s:%d:%s", session, agent, model, ts.UnixMilli(), status)
	}
	t.Fatalf("the fixture holds no route line for %s/%s/%s", session, agent, model)
	return ""
}

// compatFixtureNow pins the clock at the newest spool day's noon UTC: inside the spool window, past
// every fixture timestamp, whatever day the fixture was recorded on.
func compatFixtureNow(t *testing.T, home string) time.Time {
	t.Helper()
	newest := ""
	for _, line := range compatSpoolLines(t, home) {
		if day := line.Ts[:10]; day > newest {
			newest = day
		}
	}
	if newest == "" {
		t.Fatalf("the fixture spool holds no lines")
	}
	year, err := strconv.Atoi(newest[0:4])
	if err != nil {
		t.Fatalf("parse the spool day %q: %v", newest, err)
	}
	month, err := strconv.Atoi(newest[5:7])
	if err != nil {
		t.Fatalf("parse the spool day %q: %v", newest, err)
	}
	day, err := strconv.Atoi(newest[8:10])
	if err != nil {
		t.Fatalf("parse the spool day %q: %v", newest, err)
	}
	return time.Date(year, time.Month(month), day, 12, 0, 0, 0, time.UTC)
}

// compatFetch reads one answer whole: status and body text.
func compatFetch(t *testing.T, port int, path string) (int, string) {
	t.Helper()
	response, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, path))
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

// compatObject decodes one answer body.
func compatObject(t *testing.T, path, body string) map[string]any {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("the %s answer is not a JSON object: %v (%s)", path, err, body)
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

// compatFieldStrings lists one string field of every row, sorted.
func compatFieldStrings(rows []any, key string) []string {
	out := []string{}
	for _, row := range rows {
		if object, ok := row.(map[string]any); ok {
			if value, ok := object[key].(string); ok {
				out = append(out, value)
			}
		}
	}
	sort.Strings(out)
	return out
}

// TestCompatLegacyHome opens the recorded home and proves the three compat rules: every route answers
// with the rows the fixture holds, only the server's own files change, and a second start answers
// the same with nothing moved twice.
func TestCompatLegacyHome(t *testing.T) {
	testdata := filepath.Join("..", "testdata")
	home := filepath.Join(t.TempDir(), "state")
	root := t.TempDir()
	config := filepath.Join(root, "claude")
	compatCopyTree(t, filepath.Join(testdata, "legacy_home"), home)
	compatCopyTree(t, filepath.Join(testdata, "legacy_home_inputs", "projects"), filepath.Join(config, "projects"))
	env := domain.EnvLookup{
		"HOME":              root,
		"RADAR_HOME":        home,
		"CLAUDE_CONFIG_DIR": config,
	}
	now := compatFixtureNow(t, home)
	port := ports.Free(t)
	mainRoute := compatRouteID(t, home, compatMainSession, "main", "glm-5.3")
	scoutRoute := compatRouteID(t, home, compatMainSession, "scout-01", "glm-5.3-flash")

	compatRoutes := []string{
		"/api/summary",
		"/api/sessions",
		"/api/sessions/" + compatMainSession,
		"/api/requests",
		"/api/events",
		"/api/tools",
		"/api/models",
		"/api/health",
		"/api/alerts",
		"/api/attribution?by=model&range=month",
		"/api/budgets",
		"/api/budget-status",
		"/api/spend",
		"/api/settings",
		"/api/router",
		"/api/advisor",
		"/api/history/roots?scope=history",
		"/api/history/roots?scope=live",
		"/api/history/tree/" + compatRootID,
		"/api/history/requests?root=" + compatRootID,
		"/api/history/requests?node=" + compatScoutNode,
		"/api/history/content/req_1001",
		"/api/history/content/" + mainRoute,
		"/api/history/context/" + mainRoute,
		"/api/history/request/" + mainRoute + "/capture",
		"/api/history/agent/" + compatScoutNode + "/transcript",
		"/api/history/repos",
		fmt.Sprintf("/api/history/flow?from=%d&to=%d", now.Add(-6*time.Hour).UnixMilli(), now.UnixMilli()),
		"/api/history/stats",
	}

	// startOnce runs one app over the home: the answers it served, and the files it changed.
	startOnce := func() (map[string]string, map[string]string) {
		before := compatHashTree(t, home)
		app := mustStart(t, env, AppOptions{
			Version:           "compat",
			SinceMs:           compatSinceMs,
			Port:              port,
			IntervalMs:        quietMs,
			InsightsTickMs:    quietMs,
			RemovalIntervalMs: msPtr(3_600_000),
			Now:               func() time.Time { return now },
			NotifyRunner:      func(string, []string) {},
			Log:               func(string) {},
		})
		waitFor(t, "the fixture to ingest", 10*time.Second, func() bool {
			return len(app.Store.Requests(store.RequestFilter{})) == 4 && app.Store.SessionCount() == 2
		})
		// while it runs, the one file only a live server owns is there (stop takes it back)
		if !exists(filepath.Join(home, "server.json")) {
			t.Fatalf("a running server publishes server.json")
		}
		answers := map[string]string{}
		for _, route := range compatRoutes {
			status, body := compatFetch(t, app.Port, route)
			if status != 200 {
				t.Fatalf("%s answered %d, want 200: %s", route, status, body)
			}
			answers[route] = body
			t.Logf("%s -> %s", route, body)
		}
		app.Stop()
		return answers, before
	}

	assertRows := func(answers map[string]string) {
		health := compatObject(t, "/api/health", answers["/api/health"])
		// uptimeMs rides the real clock (it is the live process's age), everything else the pinned one
		if health["ok"] != true || compatString(t, "/api/health", health, "version") != "compat" ||
			compatNumber(t, "/api/health", health, "port") != float64(port) ||
			compatNumber(t, "/api/health", health, "sessions") != 2 ||
			compatNumber(t, "/api/health", health, "uptimeMs") < 0 ||
			compatNumber(t, "/api/health", health, "startedAt") != float64(now.UnixMilli()) {
			t.Fatalf("health names this run: %v", health)
		}

		summary := compatObjectAt(t, "/api/summary", compatObject(t, "/api/summary", answers["/api/summary"]), "summary")
		tokens := compatObjectAt(t, "/api/summary", summary, "tokens")
		if compatNumber(t, "/api/summary", summary, "sessions") != 2 ||
			compatNumber(t, "/api/summary", summary, "agents") != 3 ||
			compatNumber(t, "/api/summary", summary, "requests") != 4 ||
			compatNumber(t, "/api/summary", summary, "toolCalls") != 1 ||
			compatNumber(t, "/api/summary", summary, "errors") != 0 ||
			compatNumber(t, "/api/summary", tokens, "input") != 1110 ||
			compatNumber(t, "/api/summary", tokens, "output") != 275 ||
			compatNumber(t, "/api/summary", tokens, "cacheRead") != 2300 ||
			compatNumber(t, "/api/summary", tokens, "cacheWrite") != 64 {
			t.Fatalf("the summary holds the fixture's rows: %v", summary)
		}

		sessions := compatFieldStrings(compatList(t, "/api/sessions",
			compatObject(t, "/api/sessions", answers["/api/sessions"]), "sessions"), "id")
		if strings.Join(sessions, ",") != compatMainSession+","+compatOldSession {
			t.Fatalf("the session list holds both sessions: %v", sessions)
		}

		detail := compatObjectAt(t, "/api/sessions/"+compatMainSession,
			compatObject(t, "/api/sessions/"+compatMainSession, answers["/api/sessions/"+compatMainSession]), "session")
		agents := compatFieldStrings(compatList(t, "/api/sessions/"+compatMainSession, detail, "agents"), "id")
		if strings.Join(agents, ",") != "main,scout-01" {
			t.Fatalf("the session carries its main agent and the scout: %v", agents)
		}

		requests := compatFieldStrings(compatList(t, "/api/requests",
			compatObject(t, "/api/requests", answers["/api/requests"]), "requests"), "id")
		want := []string{mainRoute, scoutRoute, "req_1001", "req_3001"}
		sort.Strings(want)
		if strings.Join(requests, ",") != strings.Join(want, ",") {
			t.Fatalf("the request list holds the fixture's four calls: %v", requests)
		}

		events := compatFieldStrings(compatList(t, "/api/events",
			compatObject(t, "/api/events", answers["/api/events"]), "events"), "kind")
		if strings.Join(events, ",") != "SessionStart,SessionStart,UserPromptSubmit,UserPromptSubmit" {
			t.Fatalf("the event list holds the four hook lines: %v", events)
		}

		tools := compatList(t, "/api/tools", compatObject(t, "/api/tools", answers["/api/tools"]), "tools")
		if len(tools) != 1 {
			t.Fatalf("one tool call in the fixture, got %v", tools)
		}
		tool, _ := tools[0].(map[string]any)
		if tool["id"] != "tu_1" || tool["name"] != "Bash" || tool["ok"] != true {
			t.Fatalf("the Bash call the transcript recorded: %v", tool)
		}

		models := compatFieldStrings(compatList(t, "/api/models",
			compatObject(t, "/api/models", answers["/api/models"]), "models"), "model")
		if strings.Join(models, ",") != "claude-sonnet-5-5,glm-5.3,glm-5.3-flash,kimi-k2" {
			t.Fatalf("the models table holds the fixture's four models: %v", models)
		}

		alerts := compatList(t, "/api/alerts", compatObject(t, "/api/alerts", answers["/api/alerts"]), "alerts")
		if len(alerts) != 0 {
			t.Fatalf("the fixture raises no alerts, got %v", alerts)
		}

		budgets := compatObject(t, "/api/budgets", answers["/api/budgets"])
		saved := compatList(t, "/api/budgets", budgets, "budgets")
		if len(saved) != 1 {
			t.Fatalf("the saved budget answers, got %v", saved)
		}
		budget, _ := saved[0].(map[string]any)
		if budget["id"] != "zai-month" || budget["scope"] != "provider:zai" || budget["period"] != "month" ||
			budget["limitUsd"] != float64(25) || budget["action"] != "warn" {
			t.Fatalf("the budget the dashboard saved: %v", budget)
		}
		providers := compatList(t, "/api/budgets", budgets, "providers")
		if len(providers) == 0 || providers[0] != "zai" {
			t.Fatalf("the provider list answers: %v", providers)
		}

		attribution := compatObject(t, "/api/attribution?by=model&range=month",
			answers["/api/attribution?by=model&range=month"])
		if compatString(t, "/api/attribution", attribution, "by") != "model" ||
			compatString(t, "/api/attribution", attribution, "range") != "month" ||
			len(compatList(t, "/api/attribution", attribution, "rows")) == 0 {
			t.Fatalf("the attribution grouping answers: %v", attribution)
		}

		status := compatObject(t, "/api/budget-status", answers["/api/budget-status"])
		spend := compatList(t, "/api/budget-status", status, "spend")
		if len(compatList(t, "/api/budget-status", status, "stopped")) != 0 {
			t.Fatalf("nothing is stopped: %v", status)
		}
		if len(spend) != 1 {
			t.Fatalf("the one budget's spend answers, got %v", spend)
		}
		if compatNumber(t, "/api/budget-status", status, "updatedAt") != float64(now.UnixMilli()) {
			t.Fatalf("the status this pinned clock wrote: %v", status)
		}

		settings := compatObjectAt(t, "/api/settings",
			compatObject(t, "/api/settings", answers["/api/settings"]), "settings")
		if settings["notifications"] != true || compatNumber(t, "/api/settings", settings, "historyRetentionDays") != 90 {
			t.Fatalf("the setting the dashboard saved: %v", settings)
		}

		router := compatObject(t, "/api/router", answers["/api/router"])
		routerProviders := compatList(t, "/api/router", router, "providers")
		counts := map[string]map[string]any{}
		for _, entry := range routerProviders {
			provider, _ := entry.(map[string]any)
			name, _ := provider["plugin"].(string)
			counts[name], _ = provider["counts"].(map[string]any)
		}
		if counts["zai"] == nil || counts["zai"]["restart"] != float64(1) ||
			counts["kimi"] == nil || counts["kimi"]["rate_limited"] != float64(1) {
			t.Fatalf("the router events the spool recorded: %v", router)
		}

		advisor := compatObject(t, "/api/advisor", answers["/api/advisor"])
		if _, ok := advisor["runsChecked"]; !ok {
			t.Fatalf("the advisor report answers: %v", advisor)
		}

		rootsAnswer := compatObject(t, "/api/history/roots?scope=history", answers["/api/history/roots?scope=history"])
		roots := compatList(t, "/api/history/roots?scope=history", rootsAnswer, "roots")
		rootIDs := compatFieldStrings(roots, "id")
		if strings.Join(rootIDs, ",") != compatRootID+","+compatOldRootID {
			t.Fatalf("the history holds both trees: %v", rootIDs)
		}
		first, _ := roots[0].(map[string]any)
		if first["id"] != compatRootID || compatNumber(t, "/api/history/roots?scope=history", first, "requests") != 3 ||
			compatNumber(t, "/api/history/roots?scope=history", first, "nodes") != 2 {
			t.Fatalf("the main tree's own counts: %v", first)
		}
		if rootsAnswer["next"] != nil {
			t.Fatalf("one page holds every root: %v", rootsAnswer["next"])
		}

		liveRoots := compatList(t, "/api/history/roots?scope=live",
			compatObject(t, "/api/history/roots?scope=live", answers["/api/history/roots?scope=live"]), "roots")
		if len(liveRoots) != 0 {
			t.Fatalf("nothing moved inside the live window, got %v", liveRoots)
		}

		nodes := compatFieldStrings(compatList(t, "/api/history/tree/"+compatRootID,
			compatObject(t, "/api/history/tree/"+compatRootID, answers["/api/history/tree/"+compatRootID]), "nodes"), "id")
		if strings.Join(nodes, ",") != compatRootID+","+compatScoutNode {
			t.Fatalf("the tree holds the session and its scout: %v", nodes)
		}

		treeRequests := compatFieldStrings(compatList(t, "/api/history/requests?root="+compatRootID,
			compatObject(t, "/api/history/requests?root="+compatRootID, answers["/api/history/requests?root="+compatRootID]),
			"requests"), "id")
		wantTree := []string{mainRoute, scoutRoute, "req_1001"}
		sort.Strings(wantTree)
		if strings.Join(treeRequests, ",") != strings.Join(wantTree, ",") {
			t.Fatalf("the tree's three requests: %v", treeRequests)
		}

		scoutRequests := compatFieldStrings(compatList(t, "/api/history/requests?node="+compatScoutNode,
			compatObject(t, "/api/history/requests?node="+compatScoutNode, answers["/api/history/requests?node="+compatScoutNode]),
			"requests"), "id")
		if strings.Join(scoutRequests, ",") != scoutRoute {
			t.Fatalf("the scout's own request: %v", scoutRequests)
		}

		content := compatObject(t, "/api/history/content/req_1001", answers["/api/history/content/req_1001"])
		if content["input"] == nil || content["output"] == nil ||
			compatNumber(t, "/api/history/content/req_1001", content, "bytes") != 115 {
			t.Fatalf("the stored sides of the first call: %v", content)
		}
		routed := compatObject(t, "/api/history/content/"+mainRoute, answers["/api/history/content/"+mainRoute])
		if compatNumber(t, "/api/history/content/"+mainRoute, routed, "bytes") != 129 {
			t.Fatalf("the stored sides of the routed call: %v", routed)
		}

		context := compatObject(t, "/api/history/context/"+mainRoute, answers["/api/history/context/"+mainRoute])
		if compatString(t, "/api/history/context/"+mainRoute, context, "note") != ContextNote ||
			len(compatList(t, "/api/history/context/"+mainRoute, context, "messages")) == 0 {
			t.Fatalf("the rebuilt conversation answers: %v", context)
		}

		capture := compatObject(t, "/api/history/request/"+mainRoute+"/capture",
			answers["/api/history/request/"+mainRoute+"/capture"])
		if compatString(t, "/api/history/request/"+mainRoute+"/capture", capture, "hash") != compatPromptHash ||
			compatNumber(t, "/api/history/request/"+mainRoute+"/capture", capture, "bytes") != 78 {
			t.Fatalf("the prompt capture the router sent: %v", capture)
		}

		transcript := compatObject(t, "/api/history/agent/"+compatScoutNode+"/transcript",
			answers["/api/history/agent/"+compatScoutNode+"/transcript"])
		if len(compatList(t, "/api/history/agent/"+compatScoutNode+"/transcript", transcript, "messages")) == 0 {
			t.Fatalf("the scout's transcript answers: %v", transcript)
		}

		flowRoute := fmt.Sprintf("/api/history/flow?from=%d&to=%d", now.Add(-6*time.Hour).UnixMilli(), now.UnixMilli())
		flow := compatObject(t, flowRoute, answers[flowRoute])
		if _, ok := flow["requests"]; !ok {
			t.Fatalf("the flow chart answers: %v", flow)
		}

		stats := compatObject(t, "/api/history/stats", answers["/api/history/stats"])
		// bytes is the database file the fixture holds, pages and all (86 016 = 21 pages of 4 096)
		if compatNumber(t, "/api/history/stats", stats, "bytes") != 86016 ||
			compatNumber(t, "/api/history/stats", stats, "nodes") != 3 ||
			compatNumber(t, "/api/history/stats", stats, "requests") != 4 ||
			compatNumber(t, "/api/history/stats", stats, "roots") != 2 ||
			compatNumber(t, "/api/history/stats", stats, "retentionDays") != 90 {
			t.Fatalf("the whole history the fixture holds: %v", stats)
		}

		if _, ok := compatObject(t, "/api/spend", answers["/api/spend"])["todayUsd"]; !ok {
			t.Fatalf("the spend view answers")
		}
		if _, ok := compatObject(t, "/api/history/repos", answers["/api/history/repos"])["repos"]; !ok {
			t.Fatalf("the repos view answers")
		}
	}

	firstAnswers, beforeFirst := startOnce()
	assertRows(firstAnswers)
	compatRequireChanged(t, "the first start", compatChangedFiles(beforeFirst, compatHashTree(t, home)))
	// the stop took the live server's own file with it; the saved port stays for the next start
	if exists(filepath.Join(home, "server.json")) {
		t.Fatalf("stop removes server.json")
	}

	t.Run("a stop and a fresh start answer the same, with nothing moved twice", func(t *testing.T) {
		secondAnswers, beforeSecond := startOnce()
		assertRows(secondAnswers)
		compatRequireChanged(t, "the second start", compatChangedFiles(beforeSecond, compatHashTree(t, home)))
		for _, route := range compatRoutes {
			// health rides the real clock (uptimeMs); assertRows checks it per run instead
			if route == "/api/health" {
				continue
			}
			if firstAnswers[route] != secondAnswers[route] {
				t.Fatalf("%s answered differently on the second start:\n%s\n%s", route, firstAnswers[route], secondAnswers[route])
			}
		}
	})
}
