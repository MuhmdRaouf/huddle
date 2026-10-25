package main

// A second project's setup against a hub a plain `huddle up` started (HR-SETUP-JOIN-CHANNEL):
// the hub's registry entry names no channel, so setup lands the project in its own channel on
// that hub, named after the project, `huddle status` says it is in, and a rerun adds no second
// member. Every process here is this test's child and every home a temp one.

import (
	"fmt"
	"os"
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

func TestE2ESetupSecondProjectJoinsARunningHub(t *testing.T) {
	bdd.Feature(t, "a second project joins a running hub", func(f *bdd.F) {
		f.Scenario("setup in a second project lands in the project's own channel", func(s *bdd.S) {
			// a short root: the homes and sockets under it stay inside the unix path bounds
			root, err := os.MkdirTemp("", "hr-s2-")
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
			p1 := filepath.Join(root, "first-proj")
			p2 := filepath.Join(root, "second-proj")
			for _, dir := range []string{p1, p2} {
				if err := os.MkdirAll(filepath.Join(dir, ".git", "info"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			hubURL := fmt.Sprintf("http://127.0.0.1:%d", hubPort)
			// each side is its own session in its own project, as Claude Code names it: the
			// creator's root credential stays the hub-listing one, the joiner's member credential
			// never overwrites it
			env1 := argvLayer(env, map[string]string{"HUDDLE_SESSION": "owner", "CLAUDE_PROJECT_DIR": p1})
			env2 := argvLayer(env, map[string]string{"HUDDLE_SESSION": "joiner", "CLAUDE_PROJECT_DIR": p2})
			stopHub := func() {
				_, _, _ = runBin(t, huddleBin, env1, p1, "down")
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

			s.Given("a hub a plain huddle up started from one project", func(t *testing.T) {
				code, out, errOut := runBin(t, huddleBin, env1, p1, "up")
				if code != 0 {
					t.Fatalf("huddle up = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
				}
				until(t, 20*time.Second, func() bool { return argvHealthy(hubURL) }, "the hub to answer its health path")
			})

			var joined string
			s.When("setup runs in a second project", func(t *testing.T) {
				code, out, errOut := runBin(t, huddleBin, env2, p2, "setup", "--start")
				if code != 0 {
					t.Fatalf("setup --start = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
				}
				joined = out
			})
			s.Then("the setup names the project's own channel, not an empty one", func(t *testing.T) {
				if !strings.Contains(joined, "channel second-proj") {
					t.Errorf("setup said %q, want channel second-proj", joined)
				}
				if strings.Contains(joined, "channel ,") {
					t.Errorf("setup said %q, want no empty channel", joined)
				}
			})
			s.Then("status in the second project says it is in", func(t *testing.T) {
				code, out, errOut := runBin(t, huddleBin, env2, p2, "status")
				if code != 0 {
					t.Fatalf("status = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
				}
				if !strings.Contains(out+errOut, "second-proj") {
					t.Errorf("status said %q %q, want channel second-proj", out, errOut)
				}
			})
			s.Then("the second project may invite the next session into its channel", func(t *testing.T) {
				code, out, errOut := runBin(t, huddleBin, env2, p2, "token", "create", "--print-join-command")
				if code != 0 {
					t.Fatalf("token create = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
				}
				if !strings.Contains(out, "/huddle:join ") {
					t.Errorf("token create said %q, want a join line", out)
				}
			})
			s.Then("a rerun says already in and adds no second member", func(t *testing.T) {
				code, out, errOut := runBin(t, huddleBin, env2, p2, "setup", "--start")
				if code != 0 {
					t.Fatalf("setup again = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
				}
				if !strings.Contains(out, "already in the Huddle") {
					t.Errorf("the rerun said %q", out)
				}
				code, members, errOut := runBin(t, huddleBin, env1, p1, "members")
				if code != 0 {
					t.Fatalf("members = %d\nstdout:\n%s\nstderr:\n%s", code, members, errOut)
				}
				count := 0
				for line := range strings.SplitSeq(members, "\n") {
					if strings.HasPrefix(line, "second-proj") {
						count++
					}
				}
				if count != 1 {
					t.Errorf("the hub lists %d members for the second project (%q), want one", count, members)
				}
			})
		})
	})
}
