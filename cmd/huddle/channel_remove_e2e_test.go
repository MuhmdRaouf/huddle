package main

// The channel-removal e2e (HR-CHANNEL-REMOVE): the creator removes a quiet channel from the CLI
// and its file is kept under channels/.removed/, a channel with a session online is refused,
// and moving the kept file back while the hub is down brings the channel and its history back.
// Every process here is this test's child and every home a temp one.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

// channelRows is GET /api/channels' answer, the fields the e2e reads.
type channelRows []struct {
	Name  string `json:"name"`
	Stats struct {
		Events int64 `json:"events"`
	} `json:"stats"`
}

// readChannels answers the hub's channel list, as the hub's token names it.
func readChannels(t *testing.T, hubURL, token string) channelRows {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, hubURL+"/api/channels", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-huddle-token", token)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("GET /api/channels: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET /api/channels = %d", resp.StatusCode)
	}
	var rows channelRows
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		t.Fatalf("decode the channel list: %v", err)
	}
	return rows
}

func TestE2EChannelRemove(t *testing.T) {
	bdd.Feature(t, "the creator removes a channel", func(f *bdd.F) {
		f.Scenario("a quiet channel is removed with its history kept, a busy one is refused, the file put back restores it", func(s *bdd.S) {
			// a short root: the homes and sockets under it stay inside the unix path bounds
			root, err := os.MkdirTemp("", "hr-crm-")
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
			// the hub's token: the API reads need it, and `up` mints the root credential from it
			token := "channel-remove-e2e-root"
			// the creator's credential is the root one `up` minted for HUDDLE_SESSION=owner; a
			// layer's channel and session name come from HUDDLE_CHANNEL and HUDDLE_AS
			creator := argvLayer(env, map[string]string{
				"HUDDLE_SESSION": "owner", "HUDDLE_URL": hubURL, "HUDDLE_TOKEN": token,
			})
			maker := argvLayer(creator, map[string]string{"HUDDLE_CHANNEL": "old", "HUDDLE_AS": "maker"})
			// the channels live under HUDDLE_DATA (server.go builds the hub on it)
			channelsDir := filepath.Join(root, "huddle-data", "channels")
			stopHub := func() {
				_, _, _ = runBin(t, huddleBin, creator, proj, "down")
				if pollFor(5*time.Second, func() bool { return !argvHealthy(hubURL) }) {
					return
				}
				// `down` left the test's own server running: end it the direct way
				var serverPid int
				if raw, err := os.ReadFile(filepath.Join(root, "huddle-home", "run", "huddle.pid")); err == nil {
					serverPid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
				}
				if serverPid > 0 {
					_ = syscall.Kill(serverPid, syscall.SIGTERM)
					if !pollFor(3*time.Second, func() bool { return !argvHealthy(hubURL) }) {
						_ = syscall.Kill(serverPid, syscall.SIGKILL)
						pollFor(2*time.Second, func() bool { return !argvHealthy(hubURL) })
					}
				}
			}
			t.Cleanup(stopHub)

			s.Given("a hub a plain huddle up started, and a channel a session has left", func(t *testing.T) {
				code, out, errOut := runBin(t, huddleBin, creator, proj, "up")
				if code != 0 {
					t.Fatalf("huddle up = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
				}
				until(t, 20*time.Second, func() bool { return argvHealthy(hubURL) }, "the hub to answer its health path")
				code, out, errOut = runBin(t, huddleBin, maker, proj, "join")
				if code != 0 {
					t.Fatalf("join old = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
				}
				code, out, errOut = runBin(t, huddleBin, maker, proj, "leave", "done")
				if code != 0 {
					t.Fatalf("leave old = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
				}
			})

			var kept string
			s.When("the creator removes the quiet channel", func(t *testing.T) {
				code, out, errOut := runBin(t, huddleBin, creator, proj, "channel", "rm", "old")
				if code != 0 {
					t.Fatalf("channel rm old = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
				}
				if !strings.Contains(out, "old removed; its history is kept at ") {
					t.Errorf("channel rm said %q", out)
				}
				matches, err := filepath.Glob(filepath.Join(channelsDir, ".removed", "old-*.db"))
				if err != nil || len(matches) != 1 {
					t.Fatalf(".removed holds %v (%v), want one kept file", matches, err)
				}
				kept = matches[0]
			})
			s.Then("the channel is gone from the list and its file sits under .removed", func(t *testing.T) {
				for _, row := range readChannels(t, hubURL, token) {
					if row.Name == "old" {
						t.Errorf("the channel list still has old")
					}
				}
				if _, err := os.Stat(filepath.Join(channelsDir, "old.db")); !os.IsNotExist(err) {
					t.Errorf("channels/old.db stayed: %v", err)
				}
				if _, err := os.Stat(kept); err != nil {
					t.Errorf("the kept file: %v", err)
				}
			})
			s.Then("removing it again answers not found", func(t *testing.T) {
				code, out, errOut := runBin(t, huddleBin, creator, proj, "channel", "rm", "old")
				if code != 4 {
					t.Errorf("channel rm old again = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
				}
				if !strings.Contains(out+errOut, "no channel old") {
					t.Errorf("the refusal = %q %q, want no channel old", out, errOut)
				}
			})

			s.When("a session is online in another channel", func(t *testing.T) {
				keeper := argvLayer(creator, map[string]string{"HUDDLE_CHANNEL": "busy", "HUDDLE_AS": "keeper"})
				// the keeper's wait keeps its session working while the remove is tried
				cmd := exec.Command(huddleBin, "join")
				cmd.Env = argvEntries(keeper)
				cmd.Dir = proj
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("join busy: %v\n%s", err, out)
				}
				wait := exec.Command(huddleBin, "wait", "--timeout", "30")
				wait.Env = argvEntries(keeper)
				wait.Dir = proj
				if err := wait.Start(); err != nil {
					t.Fatalf("start the keeper's wait: %v", err)
				}
				t.Cleanup(func() {
					_ = wait.Process.Kill()
					_, _ = wait.Process.Wait()
				})
				until(t, 10*time.Second, func() bool {
					for _, row := range readChannels(t, hubURL, token) {
						if row.Name == "busy" {
							return true
						}
					}
					return false
				}, "busy to list")
			})
			s.Then("removing the busy channel is refused with the online count", func(t *testing.T) {
				code, out, errOut := runBin(t, huddleBin, creator, proj, "channel", "rm", "busy")
				if code == 0 {
					t.Fatalf("channel rm busy = 0, want a refusal\nstdout:\n%s", out)
				}
				if !strings.Contains(out+errOut, "online") {
					t.Errorf("the refusal = %q %q, want the online count", out, errOut)
				}
			})

			s.When("the hub stops and the kept file moves back", func(t *testing.T) {
				code, out, errOut := runBin(t, huddleBin, creator, proj, "down")
				if code != 0 {
					t.Fatalf("down = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
				}
				until(t, 10*time.Second, func() bool { return !argvHealthy(hubURL) }, "the hub to stop")
				if err := os.Rename(kept, filepath.Join(channelsDir, "old.db")); err != nil {
					t.Fatalf("move the kept file back: %v", err)
				}
				code, out, errOut = runBin(t, huddleBin, creator, proj, "up")
				if code != 0 {
					t.Fatalf("huddle up again = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
				}
				until(t, 20*time.Second, func() bool { return argvHealthy(hubURL) }, "the hub to answer again")
			})
			s.Then("old lists again with the history it had", func(t *testing.T) {
				found := false
				for _, row := range readChannels(t, hubURL, token) {
					if row.Name == "old" {
						found = true
						if row.Stats.Events < 1 {
							t.Errorf("old's stats = %+v, want the events it had", row.Stats)
						}
					}
				}
				if !found {
					t.Error("the channel list misses old after the file came back")
				}
			})
		})
	})
}
