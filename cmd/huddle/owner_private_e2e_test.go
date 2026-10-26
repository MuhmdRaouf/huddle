package main

// An owner's message to one session, end to end on the built binary: POST /api/send queues it,
// the fresh push ack and a live claude pid whose command line shows channel pushes put it on
// the now route, the push lands in the channel as an event from the owner addressed to that one
// member, and the member's listen hook carries it while another session's feed leaves it out.
// The fake claude is this suite's own test binary running as a helper process: the registry
// reads a pid's command line off ps, so the helper's argv names claude and --channels huddle
// the way a real session's does.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

// TestOwnerPrivateHelper is the e2e's fake claude: it runs the lead session's hook once in the
// environment the test handed over, so the report names a live pid, and stays alive until the
// test kills it. Its own environment is the test binary's sandbox, which drops every HUDDLE_*
// variable, so everything it needs rides in the E2E_* ones, which the sandbox keeps. argv
// carries claude and --channels huddle after the test flag: the registry reads both off this
// process's ps line.
func TestOwnerPrivateHelper(t *testing.T) {
	if os.Getenv("E2E_FAKE_CLAUDE") == "" {
		t.Skip("the e2e's helper process, not a test")
	}
	hook := exec.Command("/bin/sh", "-c", os.Getenv("E2E_HELPER_CMD"))
	hook.Env = strings.Split(os.Getenv("E2E_HELPER_ENV"), "\n")
	out, err := hook.CombinedOutput()
	if log := os.Getenv("E2E_HELPER_LOG"); log != "" {
		_ = os.WriteFile(log, out, 0o600)
	}
	if err != nil {
		t.Fatalf("the helper's hook run failed: %v", err)
	}
	for { // stay live: the registry reads this pid's command line; the test kills us when done
		time.Sleep(200 * time.Millisecond)
	}
}

func TestE2EAnOwnerMessageReachesOnlyItsSession(t *testing.T) {
	root, err := os.MkdirTemp("", "hr-ownmsg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	hubPort := ports.Free(t)
	env := isoenv.Lookup(isoenv.Environ(t, filepath.Join(root, "user"),
		"RADAR_HOME="+filepath.Join(root, "radar"),
		"HUDDLE_HOME="+filepath.Join(root, "huddle-home"),
		"HUDDLE_DATA="+filepath.Join(root, "huddle-data"),
		"HUDDLE_ROUTER_HOME="+filepath.Join(root, "router"),
		"HUDDLE_PORT="+strconv.Itoa(hubPort),
	))
	proj := filepath.Join(root, "proj")
	if err := os.MkdirAll(filepath.Join(proj, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	hubURL := fmt.Sprintf("http://127.0.0.1:%d", hubPort)
	token := "owner-private-e2e-root"
	owner := argvLayer(env, map[string]string{
		"HUDDLE_SESSION": "owner", "HUDDLE_URL": hubURL, "HUDDLE_TOKEN": token,
		"CLAUDE_PROJECT_DIR": proj,
	})
	lead := argvLayer(owner, map[string]string{
		"HUDDLE_CHANNEL": "teams", "HUDDLE_AS": "lead", "HUDDLE_SESSION": "s-lead",
	})
	dotfiles := argvLayer(owner, map[string]string{
		"HUDDLE_CHANNEL": "teams", "HUDDLE_AS": "dotfiles", "HUDDLE_SESSION": "s-dotfiles",
	})
	t.Cleanup(func() {
		_, _, _ = runBin(t, huddleBin, owner, proj, "down")
		if pollFor(5*time.Second, func() bool { return !argvHealthy(hubURL) }) {
			return
		}
		var pid int
		if raw, err := os.ReadFile(filepath.Join(root, "huddle-home", "run", "huddle.pid")); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
		}
		if pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGTERM)
			if !pollFor(3*time.Second, func() bool { return !argvHealthy(hubURL) }) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})

	hookPayload := func(session string) string {
		return `{"session_id":"` + session + `","cwd":` + strconv.Quote(proj) +
			`,"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"true"}}`
	}

	if code, out, errOut := runBin(t, huddleBin, owner, proj, "up"); code != 0 {
		t.Fatalf("huddle up = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	until(t, 20*time.Second, func() bool { return argvHealthy(hubURL) }, "the hub to answer its health path")
	for name, who := range map[string]map[string]string{"lead": lead, "dotfiles": dotfiles} {
		if code, out, errOut := runBin(t, huddleBin, who, proj, "join"); code != 0 {
			t.Fatalf("join %s = %d\nstdout:\n%s\nstderr:\n%s", name, code, out, errOut)
		}
	}
	// one feed pass each marks the sessions' seen cursors, so the next pass shows what arrives
	if code, out, errOut := hooksRun(t, dotfiles, proj, hookPayload("s-dotfiles"), "hook", "post-tool-use"); code != 0 {
		t.Fatalf("dotfiles warm-up hook = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}

	// the lead's warm-up runs inside the fake claude: the report names its live pid, whose
	// command line names claude and --channels huddle. The child's dir is this package's, where
	// its TestMain's build resolves the module; the hook's own cd puts it in the project.
	helperLog := filepath.Join(root, "helper.log")
	payload := hookPayload("s-lead")
	pkgDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	helper := exec.Command(os.Args[0], "-test.run=^TestOwnerPrivateHelper$", "claude", "--channels", "huddle")
	helper.Env = argvEntries(argvLayer(lead, map[string]string{
		"E2E_FAKE_CLAUDE": "1",
		"E2E_HELPER_CMD": "cd " + strconv.Quote(proj) + " && printf '%s' '" + payload +
			"' | " + huddleBin + " hook post-tool-use",
		"E2E_HELPER_LOG": helperLog,
		// the sandbox drops this process's HUDDLE_* variables, so the hook takes the lead
		// session's environment verbatim from here instead of os.Environ
		"E2E_HELPER_ENV": strings.Join(argvEntries(lead), "\n"),
	}))
	helper.Dir = pkgDir
	var helperOut bytes.Buffer
	helper.Stdout, helper.Stderr = &helperOut, &helperOut
	stdin, err := helper.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = helper.Process.Kill()
		_ = helper.Wait()
	})
	lastRegistry, lastStatus := "", 0
	reported := pollFor(30*time.Second, func() bool {
		req, err := http.NewRequest(http.MethodGet, hubURL+"/api/registry/sessions", nil)
		if err != nil {
			return false
		}
		req.Header.Set("x-huddle-token", token)
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			lastStatus = -1
			return false
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		lastStatus, lastRegistry = resp.StatusCode, string(raw)
		var list struct {
			Sessions []struct {
				SessionID string `json:"session_id"`
				Pid       int    `json:"pid"`
				Channel   string `json:"channel"`
			} `json:"sessions"`
		}
		if json.Unmarshal(raw, &list) != nil {
			return false
		}
		for _, row := range list.Sessions {
			if row.SessionID == "s-lead" && row.Pid > 0 && row.Channel == "teams" {
				return true
			}
		}
		return false
	})
	if !reported {
		t.Fatalf("s-lead never reported through the fake claude\nlastStatus=%d\nregistry:\n%s\nhelper ps:\n%s\nhelper output:\n%s\nhelper log:\n%s",
			lastStatus, lastRegistry, psOf(helper.Process.Pid), helperOut.String(), readLog(helperLog))
	}

	raw := tasksHubCall(t, http.MethodPost, hubURL, "/api/registry/push-ack", token, `{"channel":"teams","as":"lead"}`)
	if !strings.Contains(string(raw), `"pushed":`) {
		t.Fatalf("push-ack = %s", raw)
	}
	raw = tasksHubCall(t, http.MethodPost, hubURL, "/api/send", token,
		`{"sessions":["s-lead"],"text":"Pick the second option"}`)
	var sent struct {
		Sent []struct {
			Session string `json:"session"`
			Route   string `json:"route"`
		} `json:"sent"`
	}
	if err := json.Unmarshal(raw, &sent); err != nil || len(sent.Sent) != 1 {
		t.Fatalf("send = %s (err %v); want one queued message", raw, err)
	}
	if sent.Sent[0].Route != "now" {
		t.Fatalf("send took route %q; want now (the push route)", sent.Sent[0].Route)
	}
	// the push is a channel event from the owner addressed to lead alone
	until(t, 10*time.Second, func() bool {
		timeline := tasksHubCall(t, http.MethodGet, hubURL, "/api/c/teams/timeline?limit=20", token, "")
		var events []struct {
			From string `json:"from"`
			To   string `json:"to"`
			Msg  string `json:"msg"`
		}
		if json.Unmarshal(timeline, &events) != nil {
			return false
		}
		for _, ev := range events {
			if ev.From == "owner" && ev.To == "lead" && strings.Contains(ev.Msg, "Pick the second option") {
				return true
			}
		}
		return false
	}, "the channel to hold the owner's event addressed to lead")

	if code, stdout, errOut := hooksRun(t, dotfiles, proj, hookPayload("s-dotfiles"), "hook", "post-tool-use"); code != 0 {
		t.Fatalf("dotfiles hook = %d\nstdout:\n%s\nstderr:\n%s", code, stdout, errOut)
	} else if strings.Contains(stdout, "Pick the second option") {
		t.Fatalf("dotfiles's feed carries the owner's message to lead:\n%s", stdout)
	}
	if code, stdout, errOut := hooksRun(t, lead, proj, hookPayload("s-lead"), "hook", "post-tool-use"); code != 0 {
		t.Fatalf("lead hook = %d\nstdout:\n%s\nstderr:\n%s", code, stdout, errOut)
	} else if !strings.Contains(stdout, "Pick the second option") {
		t.Fatalf("lead's feed lost the owner's message:\nstdout:\n%s\nstderr:\n%s", stdout, errOut)
	}
}

// readLog answers a helper log's text, "" when none was written.
func readLog(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(raw)
}

// psOf answers a pid's command line, "" when it is gone.
func psOf(pid int) string {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
