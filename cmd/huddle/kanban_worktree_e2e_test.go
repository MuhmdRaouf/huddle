package main

// A session in a git worktree works its repository's board: kanban init and
// one card in the main checkout, kanban list from a worktree prints the card,
// and the session-start hooks of both checkouts link one board in the hub.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	kanban "github.com/MuhmdRaouf/huddle/internal/kanban/server"
)

func TestE2EKanbanListFromAWorktree(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	user := filepath.Join(root, "user")
	hubHome := filepath.Join(root, "hub-home")
	const token = "e2e-kanban-worktree-token"
	gitEnv := map[string]string{
		"PATH":                strippedPath,
		"HOME":                user,
		"GIT_CONFIG_GLOBAL":   "/dev/null",
		"GIT_CONFIG_NOSYSTEM": "1",
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Env = argvEntries(gitEnv)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	main, wt := filepath.Join(root, "proxbeam"), filepath.Join(root, "proxbeam", ".agents", "worktrees", "homelab")
	git("init", "-q", main)
	git("-C", main, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init")
	git("-C", main, "worktree", "add", "-q", wt)

	kanbanEnv := map[string]string{}
	for k, v := range gitEnv {
		kanbanEnv[k] = v
	}
	kanbanEnv["KANBAN_HOME"] = filepath.Join(root, "kanban-home")
	if code, out, errOut := runBin(t, huddleBin, kanbanEnv, main, "kanban", "init"); code != 0 {
		t.Fatalf("kanban init = %d, %q %q", code, out, errOut)
	}
	if code, out, errOut := runBin(t, huddleBin, kanbanEnv, main, "kanban", "new", "HOMELAB-DNS",
		"--actor", "lead", "--cluster", "homelab", "--type", "docs",
		"--title", "Document the homelab DNS records",
		"--summary", "The homelab DNS records live in one zone file nobody wrote down. Document each record and who owns it, so a worktree session can find it."); code != 0 {
		t.Fatalf("kanban new = %d, %q %q", code, out, errOut)
	}

	code, out, errOut := runBin(t, huddleBin, kanbanEnv, wt, "kanban", "list")
	if code != 0 || !strings.Contains(out, "HOMELAB-DNS") {
		t.Fatalf("kanban list from the worktree = %d, stdout %q, stderr %q, want the main checkout's card", code, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(wt, ".agents", "kanban")); !os.IsNotExist(err) {
		t.Fatalf("the worktree grew a board of its own: %v", err)
	}

	hubPort := pinFreePort(t, hubHome)
	startServe(t, t, huddleBin, serveEnv{
		home: hubHome, user: user, port: hubPort, token: token,
		data: filepath.Join(user, "data"),
	})
	hubURL := fmt.Sprintf("http://127.0.0.1:%d", hubPort)
	until(t, 30*time.Second, func() bool { return healthy(hubURL) }, "the hub to answer /health")
	if err := os.WriteFile(home.Home{Root: hubHome}.PortFile(), []byte(strconv.Itoa(hubPort)), 0o600); err != nil {
		t.Fatalf("write the port file: %v", err)
	}

	sessions := map[string]string{"s-main": main, "s-worktree": wt}
	for session, dir := range sessions {
		postReport(t, hubURL, token, fmt.Sprintf(
			`{"session_id":%q,"cwd":%q,"last_event":"UserPromptSubmit","last_event_at":"2026-10-20T09:00:00.000Z"}`,
			session, dir))
	}
	key := kanban.KeyOf(filepath.Join(main, ".agents", "kanban"))
	link := hubURL + "/#/kanban/" + key
	for session, dir := range sessions {
		hookEnv := map[string]string{
			"CLAUDE_CONFIG_DIR":  filepath.Join(user, ".claude"),
			"HUDDLE_HOME":        hubHome,
			"HUDDLE_TOKEN":       token,
			"KANBAN_HOME":        filepath.Join(root, "kanban-homes", session),
			"CLAUDE_PROJECT_DIR": dir,
		}
		for k, v := range gitEnv {
			hookEnv[k] = v
		}
		stdin := `{"hook_event_name":"SessionStart","session_id":"` + session + `","source":"startup","cwd":` + strconv.Quote(dir) + `}`
		code, stdout, stderr := runSh(t, 30*time.Second, hookEnv, dir, strconv.Quote(huddleBin)+" hook session-start", stdin)
		if code != 0 || !strings.Contains(stdout, link) {
			t.Fatalf("%s's hook = %d, stdout %q, stderr %q, want it to link the main checkout's board %s", session, code, stdout, stderr, link)
		}
	}

	listed := hubBoardsListed(t, hubURL, token)
	if len(listed) != 1 || listed[0] != key {
		t.Fatalf("the hub lists %v, want the one board %s for both checkouts", listed, key)
	}
}
