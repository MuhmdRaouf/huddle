package main

// Claude Code's agent-team hooks end to end on the built binary: `huddle hook task-created` puts
// `task: <subject>` on the session's channel and a CC-<short id> card in cluster "cc tasks" on
// the project's board, and `huddle hook teammate-idle` marks the teammate idle under the session
// in the registry the Now list reads. The hub is this test's child, every home is a temp one, and
// the children's environments come from isoenv.

import (
	"context"
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

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
	kanbancli "github.com/MuhmdRaouf/huddle/internal/kanban/cli"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

// tasksHubCall sends one request to the hub at path (query included) with its root credential,
// naming its action the way the hooks' own client does, and answers the body.
func tasksHubCall(t *testing.T, method, hubURL, path, token, body string) []byte {
	t.Helper()
	url := hubURL + path
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-huddle-token", token)
	req.Header.Set("x-huddle-action", strings.SplitN(path, "?", 2)[0])
	req.Header.Set("content-type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("%s %s = %d %s", method, url, resp.StatusCode, raw)
	}
	return raw
}

func TestE2EHooksTasks(t *testing.T) {
	bdd.Feature(t, "Claude Code's agent teams show on the channel, the board and Now", func(f *bdd.F) {
		f.Scenario("a created task posts and lands as a card, an idle teammate shows under its lead", func(s *bdd.S) {
			// a short root: the homes and sockets under it stay inside the unix path bounds
			root, err := os.MkdirTemp("", "hr-tasks-")
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
			token := "hooks-tasks-e2e-root"
			owner := argvLayer(env, map[string]string{
				"HUDDLE_SESSION": "owner", "HUDDLE_URL": hubURL, "HUDDLE_TOKEN": token,
				"CLAUDE_PROJECT_DIR": proj,
			})
			lead := argvLayer(owner, map[string]string{"HUDDLE_CHANNEL": "teams", "HUDDLE_AS": "lead"})
			watcher := argvLayer(owner, map[string]string{"HUDDLE_CHANNEL": "teams", "HUDDLE_AS": "watcher"})
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

			s.Given("a hub, a channel the lead and a watcher joined, and a project with a board", func(t *testing.T) {
				if code, out, errOut := runBin(t, huddleBin, owner, proj, "up"); code != 0 {
					t.Fatalf("huddle up = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
				}
				until(t, 20*time.Second, func() bool { return argvHealthy(hubURL) }, "the hub to answer its health path")
				for name, who := range map[string]map[string]string{"lead": lead, "watcher": watcher} {
					if code, out, errOut := runBin(t, huddleBin, who, proj, "join"); code != 0 {
						t.Fatalf("join %s = %d\nstdout:\n%s\nstderr:\n%s", name, code, out, errOut)
					}
				}
				if _, err := board.InitDB(context.Background(), filepath.Join(proj, ".agents", "kanban"), time.Now()); err != nil {
					t.Fatalf("init the board: %v", err)
				}
			})

			s.When("Claude Code runs the TaskCreated hook in the lead's session", func(t *testing.T) {
				payload := `{"session_id":"owner","cwd":` + strconv.Quote(proj) + `,"hook_event_name":"TaskCreated",` +
					`"task_id":"task-001","task_subject":"Implement user authentication",` +
					`"task_description":"Add login and signup endpoints","teammate_name":"implementer",` +
					`"team_name":"session-a1b2c3d4"}`
				code, stdout, stderr := hooksRun(t, lead, proj, payload, "hook", "task-created")
				if code != 0 || stdout != "" || stderr != "" {
					t.Fatalf("hook task-created = %d, stdout %q, stderr %q; want 0 in silence", code, stdout, stderr)
				}
			})

			s.Then("the channel carries the task from the lead", func(t *testing.T) {
				raw := tasksHubCall(t, http.MethodPost, hubURL, "/api/c/teams/op/events?as=watcher", token, "{}")
				var answer struct {
					Result []struct {
						From string `json:"from"`
						Msg  string `json:"msg"`
					} `json:"result"`
				}
				if err := json.Unmarshal(raw, &answer); err != nil {
					t.Fatalf("decode %s: %v", raw, err)
				}
				for _, ev := range answer.Result {
					if ev.From == "lead" && ev.Msg == "task: Implement user authentication" {
						return
					}
				}
				t.Errorf("the channel holds %s, want lead's `task: Implement user authentication`", raw)
			})

			s.Then("the board holds the task as a card in cluster cc tasks", func(t *testing.T) {
				b, closeBoard, err := board.Open(context.Background(), board.Board{Root: filepath.Join(proj, ".agents", "kanban")})
				if err != nil {
					t.Fatalf("open the board: %v", err)
				}
				defer closeBoard()
				cards, err := b.List(context.Background(), "")
				if err != nil {
					t.Fatalf("list the board: %v", err)
				}
				want := kanbancli.TaskCardID("owner", "task-001")
				if len(cards) != 1 || cards[0].ID != want || cards[0].Cluster != "cc tasks" ||
					cards[0].Title != "Implement user authentication" {
					t.Errorf("the board holds %+v, want one card %s in cluster cc tasks", cards, want)
				}
			})

			s.When("Claude Code runs the TeammateIdle hook in the lead's session", func(t *testing.T) {
				payload := `{"session_id":"owner","cwd":` + strconv.Quote(proj) + `,"permission_mode":"default",` +
					`"hook_event_name":"TeammateIdle","teammate_name":"researcher","team_name":"session-a1b2c3d4"}`
				code, stdout, stderr := hooksRun(t, lead, proj, payload, "hook", "teammate-idle")
				if code != 0 || stdout != "" || stderr != "" {
					t.Fatalf("hook teammate-idle = %d, stdout %q, stderr %q; want 0 in silence", code, stdout, stderr)
				}
			})

			s.Then("the registry lists the teammate idle under the lead's session", func(t *testing.T) {
				var listed struct {
					Sessions []struct {
						SessionID string `json:"session_id"`
						Teammates []struct {
							Name  string `json:"name"`
							State string `json:"state"`
						} `json:"teammates"`
					} `json:"sessions"`
				}
				raw := tasksHubCall(t, http.MethodGet, hubURL, "/api/registry/sessions", token, "")
				if err := json.Unmarshal(raw, &listed); err != nil {
					t.Fatalf("decode %s: %v", raw, err)
				}
				for _, row := range listed.Sessions {
					if row.SessionID == "owner" && len(row.Teammates) == 1 &&
						row.Teammates[0].Name == "researcher" && row.Teammates[0].State == "idle" {
						return
					}
				}
				t.Errorf("the registry lists %s, want owner with researcher idle", raw)
			})
		})
	})
}
