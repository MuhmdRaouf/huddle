package server

// The owner's "Add to channel": POST /api/registry/sessions/<id>/channel makes a live session
// join an existing channel and saves the channel to the session's project. The route is the
// owner's alone, answers the step-up the other session acts answer, never creates a channel, and
// leaves a session on another machine alone.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeProjectFiles stands in for the CLI's project settings in the child server: the name is the
// one asked for or "fake-project", and a save leaves one file in the project's directory.
type fakeProjectFiles struct{}

func (fakeProjectFiles) MemberName(_ context.Context, _, asked string) string {
	if asked != "" {
		return asked
	}
	return "fake-project"
}

func (fakeProjectFiles) SaveChannel(_ context.Context, cwd, channel, name string) (string, error) {
	file := filepath.Join(cwd, "saved-channel.txt")
	return file, os.WriteFile(file, []byte(channel+" "+name), 0o600)
}

// addSessionRig is a server with one channel and one session that reported from a real directory.
type addSessionRig struct {
	srv *childServer
	cwd string
}

// reportAt lands a SessionStart report for id working in cwd and joined to no channel.
func reportAt(t *testing.T, srv *childServer, id, cwd, machine string) {
	t.Helper()
	body := fmt.Sprintf(`{"session_id":%q,"cwd":%q,"machine":%q,"last_event":"SessionStart","last_event_at":%q}`,
		id, cwd, machine, isoAgo(2*time.Second))
	if code, out := srv.post(t, "/api/registry/report", body, nil); code != 200 {
		t.Fatalf("report %s: %d %s", id, code, out)
	}
}

func newAddSessionRig(t *testing.T) addSessionRig {
	t.Helper()
	srv := startServer(t, nil)
	if code, out := srv.op(t, "homelab", "join", "lead", `{}`); code != 200 {
		t.Fatalf("make channel homelab: %d %v", code, out)
	}
	cwd := t.TempDir()
	reportAt(t, srv, "sess-one", cwd, "")
	return addSessionRig{srv: srv, cwd: cwd}
}

// addTo posts the route for session id with body, as the owner.
func (r addSessionRig) addTo(t *testing.T, id, body string) (int, string) {
	t.Helper()
	return r.srv.post(t, "/api/registry/sessions/"+id+"/channel", body, nil)
}

func errorOf(t *testing.T, out string) string {
	t.Helper()
	msg, _ := bodyMap(t, out)["error"].(string)
	return msg
}

// members lists the names the channel shows.
func members(t *testing.T, srv *childServer, channel string) []string {
	t.Helper()
	code, _, out := srv.get(t, "/api/c/"+channel+"/sessions", nil)
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

func TestAddToChannelJoinsTheSessionAndSavesTheProject(t *testing.T) {
	rig := newAddSessionRig(t)
	code, out := rig.addTo(t, "sess-one", `{"channel":"homelab"}`)
	if code != 200 {
		t.Fatalf("add to channel = %d %s", code, out)
	}
	answer := bodyMap(t, out)
	if answer["channel"] != "homelab" || answer["member"] != "fake-project" {
		t.Errorf("the answer = %s, want channel homelab and member fake-project", out)
	}
	if got := members(t, rig.srv, "homelab"); !strings.Contains(strings.Join(got, ","), "fake-project") {
		t.Errorf("the channel lists %v, want fake-project among them", got)
	}
	saved, err := os.ReadFile(filepath.Join(rig.cwd, "saved-channel.txt"))
	if err != nil || string(saved) != "homelab fake-project" {
		t.Errorf("the project's saved file = %q (%v), want %q", saved, err, "homelab fake-project")
	}
}

func TestAddToChannelJoinsUnderTheNameAsked(t *testing.T) {
	rig := newAddSessionRig(t)
	if code, out := rig.addTo(t, "sess-one", `{"channel":"homelab","as":"backup"}`); code != 200 {
		t.Fatalf("add to channel = %d %s", code, out)
	}
	if got := members(t, rig.srv, "homelab"); !strings.Contains(strings.Join(got, ","), "backup") {
		t.Errorf("the channel lists %v, want backup among them", got)
	}
}

func TestAddToChannelIsTheOwnersAlone(t *testing.T) {
	rig := newAddSessionRig(t)
	_, out := rig.srv.post(t, "/api/tokens", `{}`, nil)
	var invite struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(out), &invite); err != nil || invite.Token == "" {
		t.Fatalf("make an invite: %s (%v)", out, err)
	}
	_, out = rig.srv.post(t, "/api/join", `{"token":"`+invite.Token+`","name":"worker"}`, map[string]string{"x-huddle-token": ""})
	var joined struct {
		Credential string `json:"credential"`
	}
	if err := json.Unmarshal([]byte(out), &joined); err != nil || joined.Credential == "" {
		t.Fatalf("join as a member: %s (%v)", out, err)
	}
	code, out := rig.srv.post(t, "/api/registry/sessions/sess-one/channel", `{"channel":"homelab"}`,
		map[string]string{"x-huddle-token": joined.Credential})
	if code != http.StatusForbidden || !strings.Contains(errorOf(t, out), "only the owner") {
		t.Errorf("a member's add = %d %s, want 403 only the owner", code, out)
	}
	if _, err := os.Stat(filepath.Join(rig.cwd, "saved-channel.txt")); err == nil {
		t.Error("a refused add still saved the project")
	}
}

func TestAddToChannelAnswers404ForAnUnknownSession(t *testing.T) {
	rig := newAddSessionRig(t)
	code, out := rig.addTo(t, "no-such-session", `{"channel":"homelab"}`)
	if code != http.StatusNotFound {
		t.Errorf("an unknown session = %d %s, want 404", code, out)
	}
}

func TestAddToChannelNeverCreatesTheChannel(t *testing.T) {
	rig := newAddSessionRig(t)
	code, out := rig.addTo(t, "sess-one", `{"channel":"nowhere"}`)
	if code != http.StatusConflict || !strings.Contains(errorOf(t, out), "no channel nowhere") {
		t.Fatalf("an unknown channel = %d %s, want 409 naming the channel", code, out)
	}
	if !strings.Contains(errorOf(t, out), "New channel") {
		t.Errorf("the refusal %q carries no hint to create the channel first", errorOf(t, out))
	}
	if code, _, _ := rig.srv.get(t, "/api/c/nowhere/sessions", nil); code == 200 {
		t.Error("the refused add created the channel")
	}
	if _, err := os.Stat(filepath.Join(rig.cwd, "saved-channel.txt")); err == nil {
		t.Error("a refused add still saved the project")
	}
}

func TestAddToChannelNeedsAChannelName(t *testing.T) {
	rig := newAddSessionRig(t)
	if code, out := rig.addTo(t, "sess-one", `{}`); code != http.StatusBadRequest {
		t.Errorf("no channel named = %d %s, want 400", code, out)
	}
}

func TestAddToChannelRefusesAnAllowlistedChannelTheSessionIsNotOn(t *testing.T) {
	rig := newAddSessionRig(t)
	if code, out := rig.srv.op(t, "homelab", "configure", "owner", `{"members":["lead"]}`); code != 200 {
		t.Fatalf("allowlist homelab: %d %v", code, out)
	}
	code, out := rig.addTo(t, "sess-one", `{"channel":"homelab"}`)
	if code != http.StatusForbidden || !strings.Contains(errorOf(t, out), "admits lead") {
		t.Fatalf("an allowlisted channel = %d %s, want 403 from the join", code, out)
	}
	if _, err := os.Stat(filepath.Join(rig.cwd, "saved-channel.txt")); err == nil {
		t.Error("a refused join still saved the project")
	}
}

func TestAddToChannelLeavesASessionOnAnotherMachineAlone(t *testing.T) {
	home := tempDir("huddle-named-home-")
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("machine = \"workbench\"\n"), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
	srv := startServer(t, map[string]string{"HUDDLE_HOME": home})
	if code, out := srv.op(t, "homelab", "join", "lead", `{}`); code != 200 {
		t.Fatalf("make channel homelab: %d %v", code, out)
	}
	cwd := t.TempDir()
	reportAt(t, srv, "sess-away", cwd, "elsewhere")
	code, out := srv.post(t, "/api/registry/sessions/sess-away/channel", `{"channel":"homelab"}`, nil)
	if code != http.StatusNotImplemented || !strings.Contains(errorOf(t, out), "elsewhere") {
		t.Errorf("a remote session = %d %s, want 501 naming its machine", code, out)
	}
}

func TestAddToChannelLeavesASessionWithoutALocalDirectoryAlone(t *testing.T) {
	rig := newAddSessionRig(t)
	reportAt(t, rig.srv, "sess-gone", filepath.Join(rig.cwd, "not-here"), "")
	code, out := rig.addTo(t, "sess-gone", `{"channel":"homelab"}`)
	if code != http.StatusNotImplemented {
		t.Errorf("a session whose directory is not on this machine = %d %s, want 501", code, out)
	}
}
