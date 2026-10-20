package main

// The kanban boards' end to end: two sessions in two repositories run their
// session-start hook against one hub, each line links the board's page in the
// hub, the hub's /api/kanban/boards lists both boards, and no kanban serve was
// started — the hub is the only place boards are served.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
	kanban "github.com/MuhmdRaouf/huddle/internal/kanban/server"
)

// kanbanRepo answers one git repository holding a board, its git reads
// hermetic. No origin remote: the board's key is the path's, which no loaded
// machine can make flake — the origin key's equality is the scenarios' proof.
func kanbanRepo(t *testing.T, root, name string) string {
	t.Helper()
	repo := filepath.Join(root, name)
	cmd := exec.Command("git", "init", "-q", repo)
	cmd.Env = []string{
		"HOME=" + filepath.Join(root, "user"),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"PATH=" + strippedPath,
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v: %s", repo, err, out)
	}
	if _, err := board.Init(context.Background(), filepath.Join(repo, ".agents", "kanban"), "", time.Now()); err != nil {
		t.Fatalf("init the board in %s: %v", repo, err)
	}
	return repo
}

// postReport lands one session report the way the hooks deliver them.
func postReport(t *testing.T, hubURL, token, body string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, hubURL+"/api/registry/report", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-huddle-token", token)
	req.Header.Set("x-huddle-action", "/api/registry/report")
	req.Header.Set("content-type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("report the session: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("report the session = %d %s, want 200", resp.StatusCode, raw)
	}
}

// hubBoardsListed answers the keys of the boards the hub lists.
func hubBoardsListed(t *testing.T, hubURL, token string) []string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, hubURL+"/api/kanban/boards", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-huddle-token", token)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("read the hub's boards: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("the hub's boards = %d %s, want 200", resp.StatusCode, raw)
	}
	var listed struct {
		Boards []struct {
			Key string `json:"key"`
		} `json:"boards"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	keys := make([]string, 0, len(listed.Boards))
	for _, b := range listed.Boards {
		keys = append(keys, b.Key)
	}
	return keys
}

func TestE2EKanbanBoardsLiveInTheHub(t *testing.T) {
	root := t.TempDir()
	user := filepath.Join(root, "user")
	hubHome := filepath.Join(root, "hub-home")
	hubKanbanHomes := filepath.Join(root, "kanban-homes")
	const token = "e2e-kanban-root-token"

	hubPort := pinFreePort(t, hubHome)
	startServe(t, t, huddleBin, serveEnv{
		home: hubHome, user: user, port: hubPort, token: token,
		data: filepath.Join(user, "data"),
	})
	hubURL := fmt.Sprintf("http://127.0.0.1:%d", hubPort)
	until(t, 30*time.Second, func() bool { return healthy(hubURL) }, "the hub to answer /health")
	// the hook finds the hub the way the flow client does: the home's port file
	if err := os.WriteFile(home.Home{Root: hubHome}.PortFile(), []byte(strconv.Itoa(hubPort)), 0o600); err != nil {
		t.Fatalf("write the port file: %v", err)
	}

	repos := map[string]string{
		"s-alpha": kanbanRepo(t, root, "alpha"),
		"s-beta":  kanbanRepo(t, root, "beta"),
	}

	// both sessions are registered the way the hooks report: one report each
	for session, repo := range repos {
		postReport(t, hubURL, token, fmt.Sprintf(
			`{"session_id":%q,"cwd":%q,"last_event":"UserPromptSubmit","last_event_at":"2026-10-20T09:00:00.000Z"}`,
			session, repo))
	}

	// each session's start runs the merged hook with its own project dir
	keys := map[string]string{}
	for session, repo := range repos {
		keys[session] = kanban.KeyOf(filepath.Join(repo, ".agents", "kanban"))
		stdin := `{"hook_event_name":"SessionStart","session_id":"` + session + `","source":"startup","cwd":` + strconv.Quote(repo) + `}`
		code, stdout, stderr := runSh(t, 30*time.Second, map[string]string{
			"PATH":                strippedPath,
			"HOME":                user,
			"CLAUDE_CONFIG_DIR":   filepath.Join(user, ".claude"),
			"HUDDLE_HOME":         hubHome,
			"HUDDLE_TOKEN":        token,
			"KANBAN_HOME":         filepath.Join(hubKanbanHomes, session),
			"CLAUDE_PROJECT_DIR":  repo,
			"GIT_CONFIG_GLOBAL":   "/dev/null",
			"GIT_CONFIG_NOSYSTEM": "1",
		}, repo, strconv.Quote(huddleBin)+" hook session-start", stdin)
		if code != 0 {
			t.Fatalf("%s's hook = %d, stdout %q, stderr %q", session, code, stdout, stderr)
		}
		link := hubURL + "/#/kanban/" + keys[session]
		if !strings.Contains(stdout, link) {
			t.Fatalf("%s's hook printed %q, want it to link %s", session, stdout, link)
		}
		if strings.Contains(stdout, token) {
			t.Fatalf("%s's hook printed %q, want no credential in the link", session, stdout)
		}
	}

	// the one hub lists both boards
	listed := hubBoardsListed(t, hubURL, token)
	for session, key := range keys {
		if !strings.Contains(strings.Join(listed, ","), key) {
			t.Fatalf("the hub lists %v, want %s's board %s among them", listed, session, key)
		}
	}

	// no session start served its own board: the kanban state dirs hold no
	// server record, because no server was ever started
	left, err := filepath.Glob(filepath.Join(hubKanbanHomes, "*", "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("the sessions' kanban homes hold %v, want them empty: no kanban serve was started", left)
	}
}
