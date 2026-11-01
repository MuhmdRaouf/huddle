package main

// A second project joins a channel on the machine's one hub (V2-CH-SLASH-WORDS): `huddle join
// <channel>` refuses a channel nobody created and names the create word, `huddle channel create
// <name>` makes the channel and puts its project in, and `huddle join <channel>` then brings the
// second project in, saving the channel in its huddle.json. Every process here is this test's
// child and every home a temp one.

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

func TestE2EJoinSecondProjectToAChannel(t *testing.T) {
	bdd.Feature(t, "a second project joins a channel on the running hub", func(f *bdd.F) {
		f.Scenario("join refuses an unknown channel, create makes it, join brings the second project in", func(s *bdd.S) {
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
			// each side is its own session in its own project, as Claude Code names it
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

			projectFile := func(dir string) string {
				raw, _ := os.ReadFile(filepath.Join(dir, ".agents", "huddle", "huddle.json"))
				return string(raw)
			}

			s.When("the second project joins a channel nobody created", func(t *testing.T) {
				code, out, errOut := runBin(t, huddleBin, env2, p2, "join", "shared")
				if code != 2 {
					t.Fatalf("join shared = %d, want 2\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
				}
				if want := "/huddle:channel create shared"; !strings.Contains(errOut, want) {
					t.Errorf("join said %q, want a hint naming %q", errOut, want)
				}
			})
			s.Then("nothing was created or saved", func(t *testing.T) {
				if raw := projectFile(p2); raw != "" {
					t.Errorf("the second project saved %q for a channel that does not exist", raw)
				}
				if _, err := os.Stat(filepath.Join(root, "huddle-data", "channels", "shared.db")); err == nil {
					t.Errorf("a join created the channel's file")
				}
			})

			s.When("the first project creates the channel", func(t *testing.T) {
				code, out, errOut := runBin(t, huddleBin, env1, p1, "channel", "create", "shared")
				if code != 0 {
					t.Fatalf("channel create shared = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
				}
			})
			s.Then("the first project is in it, saved in its huddle.json", func(t *testing.T) {
				if raw := projectFile(p1); !strings.Contains(raw, `"channel": "shared"`) {
					t.Errorf("first project's huddle.json = %q, want channel shared", raw)
				}
				code, out, errOut := runBin(t, huddleBin, env1, p1, "status")
				if code != 0 || !strings.Contains(out+errOut, "shared") {
					t.Errorf("status = %d %q %q, want channel shared", code, out, errOut)
				}
			})
			s.Then("creating it again is refused with a hint to join", func(t *testing.T) {
				code, _, errOut := runBin(t, huddleBin, env1, p1, "channel", "create", "shared")
				if code == 0 || !strings.Contains(errOut, "/huddle:join shared") {
					t.Errorf("second create = %d %q, want a refusal naming /huddle:join shared", code, errOut)
				}
			})

			s.When("the second project joins that channel", func(t *testing.T) {
				code, out, errOut := runBin(t, huddleBin, env2, p2, "join", "shared")
				if code != 0 {
					t.Fatalf("join shared = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
				}
			})
			s.Then("its huddle.json names the channel and the project's session", func(t *testing.T) {
				raw := projectFile(p2)
				for _, want := range []string{`"channel": "shared"`, `"as": "second-proj"`} {
					if !strings.Contains(raw, want) {
						t.Errorf("second project's huddle.json = %q, want %s", raw, want)
					}
				}
			})
			s.Then("status in the second project says it is in", func(t *testing.T) {
				code, out, errOut := runBin(t, huddleBin, env2, p2, "status")
				if code != 0 {
					t.Fatalf("status = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
				}
				if !strings.Contains(out+errOut, "shared") || !strings.Contains(out+errOut, "second-proj") {
					t.Errorf("status said %q %q, want channel shared and second-proj", out, errOut)
				}
			})
			s.Then("a rerun adds no second member", func(t *testing.T) {
				code, out, errOut := runBin(t, huddleBin, env2, p2, "join", "shared")
				if code != 0 {
					t.Fatalf("join again = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
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
				if count > 1 {
					t.Errorf("the hub lists %d members for the second project (%q), want at most one", count, members)
				}
			})
		})
	})
}
