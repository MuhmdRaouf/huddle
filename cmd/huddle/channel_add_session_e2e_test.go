package main

// The owner's "Add to channel" end to end: a live session that started in no channel is added to
// homelab through the hub's route. The channel lists it within a second, its project's
// huddle.json names the channel, and the next session started in that project joins with no
// flag. Every process here is this test's child and every home a temp one.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

// addSessionCall sends one request to the hub as the owner and answers the status and body.
func addSessionCall(t *testing.T, method, url, token, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-huddle-token", token)
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-huddle-action", req.URL.Path)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// addSessionMembers lists the names the channel shows.
func addSessionMembers(t *testing.T, hubURL, token, channel string) []string {
	t.Helper()
	code, out := addSessionCall(t, http.MethodGet, hubURL+"/api/c/"+channel+"/sessions", token, "")
	if code != 200 {
		t.Fatalf("sessions of %s = %d %s", channel, code, out)
	}
	var list struct {
		Sessions []struct {
			Name string `json:"name"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("decode the sessions of %s: %v", channel, err)
	}
	names := []string{}
	for _, one := range list.Sessions {
		names = append(names, one.Name)
	}
	return names
}

func TestAddToChannelJoinsALiveSessionAndSavesTheProject(t *testing.T) {
	// a short root: the homes and sockets under it stay inside the unix path bounds
	root, err := os.MkdirTemp("", "hr-adds-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	hubPort := ports.Free(t)
	hubURL := fmt.Sprintf("http://127.0.0.1:%d", hubPort)
	token := "add-session-e2e-root"
	env := isoenv.Lookup(isoenv.Environ(t, filepath.Join(root, "user"),
		"RADAR_HOME="+filepath.Join(root, "radar"),
		"RADAR_AUTOSTART=0",
		"HUDDLE_HOME="+filepath.Join(root, "huddle-home"),
		"HUDDLE_DATA="+filepath.Join(root, "huddle-data"),
		"HUDDLE_ROUTER_HOME="+filepath.Join(root, "router"),
		"HUDDLE_ROUTER_WATCH=0",
		"HUDDLE_PORT="+strconv.Itoa(hubPort),
	))
	owner := argvLayer(env, map[string]string{"HUDDLE_SESSION": "owner", "HUDDLE_URL": hubURL, "HUDDLE_TOKEN": token})
	maker := filepath.Join(root, "maker")
	proxbeam := filepath.Join(root, "proxbeam")
	for _, dir := range []string{maker, proxbeam} {
		if err := os.MkdirAll(filepath.Join(dir, ".git", "info"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		pid := 0
		if raw, err := os.ReadFile(filepath.Join(root, "huddle-home", "run", "huddle.pid")); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
		}
		_, _, _ = runBin(t, huddleBin, owner, maker, "down")
		if !pollFor(5*time.Second, func() bool { return !argvHealthy(hubURL) }) && pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGTERM)
			pollFor(3*time.Second, func() bool { return !argvHealthy(hubURL) })
		}
	})

	// the hub runs, and homelab exists because its maker joined it
	if code, out, errOut := runBin(t, huddleBin, owner, maker, "up"); code != 0 {
		t.Fatalf("huddle up = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	until(t, 20*time.Second, func() bool { return argvHealthy(hubURL) }, "the hub to answer its health path")
	makerEnv := argvLayer(owner, map[string]string{"HUDDLE_CHANNEL": "homelab", "HUDDLE_AS": "maker"})
	if code, out, errOut := runBin(t, huddleBin, makerEnv, maker, "join"); code != 0 {
		t.Fatalf("join homelab = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}

	// a live session in the proxbeam repo reports itself and names no channel
	report := fmt.Sprintf(`{"session_id":"sess-live","cwd":%q,"last_event":"SessionStart","last_event_at":%q}`,
		proxbeam, time.Now().UTC().Format("2006-01-02T15:04:05.000Z"))
	if code, out := addSessionCall(t, http.MethodPost, hubURL+"/api/registry/report", token, report); code != 200 {
		t.Fatalf("report the live session: %d %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(proxbeam, ".agents", "huddle", "huddle.json")); err == nil {
		t.Fatal("the project already holds a huddle.json")
	}

	// the owner adds it to homelab
	code, out := addSessionCall(t, http.MethodPost, hubURL+"/api/registry/sessions/sess-live/channel", token, `{"channel":"homelab"}`)
	if code != 200 {
		t.Fatalf("add to channel = %d %s", code, out)
	}
	if !pollFor(time.Second, func() bool {
		return strings.Contains(strings.Join(addSessionMembers(t, hubURL, token, "homelab"), ","), "proxbeam")
	}) {
		t.Errorf("homelab lists %v within a second, want proxbeam among them", addSessionMembers(t, hubURL, token, "homelab"))
	}
	settings := twoReposSettings(t, proxbeam)
	if settings.Channel != "homelab" || settings.As != "proxbeam" {
		t.Errorf("the project's huddle.json = %+v, want channel homelab, as proxbeam", settings)
	}

	// the next session started in that project joins with no flag
	next := argvLayer(env, map[string]string{
		"HUDDLE_URL": hubURL, "HUDDLE_TOKEN": token,
		"CLAUDE_PROJECT_DIR": proxbeam, "CLAUDE_CODE_SESSION_ID": "sess-next",
	})
	stdin := `{"hook_event_name":"SessionStart","session_id":"sess-next","source":"startup","cwd":` + strconv.Quote(proxbeam) + `}`
	code, out, errOut := hooksRun(t, next, proxbeam, stdin, "hook", "session-start")
	if code != 0 {
		t.Fatalf("the next session-start hook = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	var answer struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &answer); err != nil {
		t.Fatalf("the next session-start hook printed %q, want its context as JSON: %v", out, err)
	}
	want := `You are in Huddle channel "homelab" as "proxbeam"`
	if said := answer.HookSpecificOutput.AdditionalContext; !strings.Contains(said, want) {
		t.Errorf("the next session-start hook said %q, want it to hold %q", said, want)
	}
}
