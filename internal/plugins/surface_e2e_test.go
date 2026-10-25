package plugins

// The commands a set-up provider plugin ships beyond setup, end to end on the committed launcher
// for all five providers: /<p>:usage reads what the router spooled from the provider's own usage
// fields, /<p>:board lists the subagent the router served and the job the fake worker ran, and
// /<p>:review shows that job's packet. Each provider's upstream is fakeProvider(p), built from
// the provider's catalog entry: its base path, its auth header and the Anthropic usage fields its
// answers carry. The rig is the fresh install's own; setup finds the key already stored, so it
// opens no page.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/core/providers"
	"github.com/MuhmdRaouf/huddle/internal/core/testkit"
	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/testkit/bdd"
)

// surfaceSession and surfaceAgent are the ids Claude Code stamps a subagent's requests with.
const (
	surfaceSession = "b1c0ffee-0000-4000-8000-00000000c0de"
	surfaceAgent   = "a9e7b1d2c3f4e5d6"
)

// surfaceReport is the change report the fake worker answers with.
var surfaceReport = map[string]any{
	"summary":     "Set the value to right",
	"files":       []map[string]string{{"path": "src/value.txt", "why": "the check expects right"}},
	"tests_added": []any{},
	"open_items":  []any{},
}

// surfaceRig is one provider's set-up machine: the fresh install's rig with the provider's fake
// upstream, and the repository a job runs in.
type surfaceRig struct {
	*freshInstall
	repo testkit.TestRepo
}

// newSurfaceRig sets one provider up against its fake upstream: the key stored before setup runs,
// so setup checks it against the fake and goes on without the page.
func newSurfaceRig(t *testing.T, name string) *surfaceRig {
	t.Helper()
	rig := newFreshInstall(t, name)
	if err := os.MkdirAll(filepath.Dir(rig.keyFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rig.keyFile, []byte(rig.provider.KeyEnv[0]+"="+rig.key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := json.Marshal(surfaceReport)
	if err != nil {
		t.Fatal(err)
	}
	// The key check and the lane both reach the provider's own fake; the fresh install's
	// recording fake stays idle (exec keeps the last of two entries with one name).
	upstream := newFakeProvider(t, rig.provider, rig.key)
	rig.env = append(rig.env,
		rig.provider.EnvPrefix+"_BASE_URL="+upstream.url,
		rig.provider.EnvPrefix+"_ROUTER_URL="+upstream.url,
	)
	for gitName, value := range testkit.ISOLATED_GIT_ENV {
		rig.env = append(rig.env, gitName+"="+value)
	}
	rig.env = append(rig.env,
		"FAKE_CLAUDE_SCENARIO=edit-file",
		"FAKE_CLAUDE_REPORT="+string(report),
		"FAKE_CLAUDE_EDIT=src/value.txt:right\n",
		"FAKE_CLAUDE_EDIT_ON_RESUME=src/value.txt:right\n",
	)
	repo := testkit.MakeRepo(t)
	repo.Write("src/value.txt", "wrong\n")
	repo.CommitAll("surface fixture")
	surface := &surfaceRig{freshInstall: rig, repo: repo}
	t.Cleanup(surface.killDrivers)
	return surface
}

// launch runs one plugin command through the launcher, the way its slash command does, in the
// job's repository.
func (rig *surfaceRig) launch(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	return runOne(t, "sh", append([]string{filepath.Join(rig.copyRoot, "dist", "run"), rig.name + ".js"}, args...),
		rig.env, rig.repo.Dir)
}

// subagentRequest sends one subagent request for the provider's main model through the router,
// stamped with the ids Claude Code sends, and reads the answer whole.
func (rig *surfaceRig) subagentRequest(t *testing.T) string {
	t.Helper()
	model := rig.provider.Catalog[domain.ModelTierMain].ID
	request, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/v1/messages", rig.port),
		strings.NewReader(fmt.Sprintf(`{"model":%q,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, model)))
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer anthropic-oauth")
	request.Header.Set("X-Claude-Code-Session-Id", surfaceSession)
	request.Header.Set("X-Claude-Code-Agent-Id", surfaceAgent)
	response, err := freshHTTP.Do(request)
	if err != nil {
		t.Fatalf("post %s: %v", model, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read the answer: %v", err)
	}
	if response.StatusCode != 200 {
		t.Fatalf("%s = %d %q", model, response.StatusCode, body)
	}
	return string(body)
}

// brief writes the job's brief: the fake worker's edit and the gate that passes on it.
func (rig *surfaceRig) brief(t *testing.T) string {
	t.Helper()
	path := filepath.Join(rig.home, "brief.md")
	body := strings.Join([]string{
		"---",
		"title: Fix the value",
		`scope: ["src/**"]`,
		"gates: [grep -q right src/value.txt]",
		"env: [FAKE_CLAUDE_SCENARIO, FAKE_CLAUDE_EDIT, FAKE_CLAUDE_EDIT_ON_RESUME, FAKE_CLAUDE_REPORT]",
		"---",
		"Make the check pass.",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write the brief: %v", err)
	}
	return path
}

// killDrivers ends the detached drivers the job left behind, each by the pid its own lock names.
func (rig *surfaceRig) killDrivers() {
	killJobDrivers(filepath.Join(rig.home, "state"))
}

// killJobDrivers ends each live driver a job lock under the state root names, with its process
// group: only locks inside the scenario's own temp home are read.
func killJobDrivers(stateRoot string) {
	entries, err := os.ReadDir(filepath.Join(stateRoot, "jobs"))
	if err != nil {
		return
	}
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(stateRoot, "jobs", entry.Name(), "driver.lock"))
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil || pid <= 1 || !testkit.IsPidAlive(pid) {
			continue
		}
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// surfaceLine is a line of the output that matches the pattern, or the test fails naming both.
func surfaceLine(t *testing.T, output, pattern string) {
	t.Helper()
	if !regexp.MustCompile(`(?m)` + pattern).MatchString(output) {
		t.Errorf("no line matches %s in:\n%s", pattern, output)
	}
}

func TestSurfaceOnFakes(t *testing.T) {
	bdd.Feature(t, "usage, board and review work on every provider against its fake upstream", func(f *bdd.F) {
		for _, name := range providers.Names {
			f.Scenario(name+" usage, board and review", func(s *bdd.S) {
				s.T.Parallel()
				var rig *surfaceRig
				var jobID string
				model := ""
				s.Given("a machine set up for the provider against its fake upstream", func(t *testing.T) {
					rig = newSurfaceRig(s.T, name)
					model = rig.provider.Catalog[domain.ModelTierMain].ID
					rig.startSetup(t)
					rig.awaitSetup(t)
				})
				s.When("a subagent's request for the main model goes through the router", func(t *testing.T) {
					rig.subagentRequest(t)
					if seen := rig.upstream.count(); seen != 0 {
						t.Errorf("the fresh install's recording fake saw %d requests, want the provider's fake to serve them", seen)
					}
				})
				s.And("a job runs with the fake worker and passes its gate", func(t *testing.T) {
					stdout, stderr, code := rig.launch(t, "run", rig.brief(t), "--wait", "--json")
					if code != 0 {
						t.Fatalf("run exited %d:\n%s\n%s", code, stdout, stderr)
					}
					var landed struct {
						ID    string `json:"id"`
						State string `json:"state"`
					}
					if err := json.Unmarshal([]byte(stdout), &landed); err != nil {
						t.Fatalf("run --json printed %q: %v", stdout, err)
					}
					if landed.State != "awaiting_review" {
						t.Fatalf("the job landed %s, want awaiting_review", landed.State)
					}
					jobID = landed.ID
				})
				s.Then("usage reports the tokens the provider's answer carried", func(t *testing.T) {
					stdout, stderr, code := rig.launch(t, "usage")
					if code != 0 {
						t.Fatalf("usage exited %d:\n%s\n%s", code, stdout, stderr)
					}
					surfaceLine(t, stdout, `^`+regexp.QuoteMeta(rig.provider.Display)+` usage$`)
					surfaceLine(t, stdout, `^Today, agents and /model$`)
					surfaceLine(t, stdout, `^`+regexp.QuoteMeta(model)+` +1 +1\.2k +42 +300 `)
				})
				s.And("board lists the subagent and the job", func(t *testing.T) {
					stdout, stderr, code := rig.launch(t, "board")
					if code != 0 {
						t.Fatalf("board exited %d:\n%s\n%s", code, stdout, stderr)
					}
					surfaceLine(t, stdout, `^KIND +ID +MODEL +STATE +REQ +IN +OUT +LAST +TITLE$`)
					// The board shows an agent id's first eight characters.
					surfaceLine(t, stdout, `^subagent +`+surfaceAgent[:8]+` +`+regexp.QuoteMeta(model)+` +active +1 +1\.2k +42 `)
					surfaceLine(t, stdout, `^job +`+regexp.QuoteMeta(jobID)+` +`+regexp.QuoteMeta(model)+` +review: pass .*Fix the value$`)
				})
				s.And("review shows the job's packet and its next step", func(t *testing.T) {
					stdout, stderr, code := rig.launch(t, "review", jobID)
					if code != 0 {
						t.Fatalf("review exited %d:\n%s\n%s", code, stdout, stderr)
					}
					surfaceLine(t, stdout, `^`+regexp.QuoteMeta(name+" job "+jobID+": Fix the value")+`$`)
					surfaceLine(t, stdout, `^verdict pass · `+regexp.QuoteMeta(model)+` · edit · `)
					surfaceLine(t, stdout, `^ +pass +grep -q right src/value\.txt +exit 0 `)
					surfaceLine(t, stdout, `^Next$`)
					surfaceLine(t, stdout, `^ +`+regexp.QuoteMeta(rig.provider.Slash+"review "+jobID)+` `)
				})
			})
		}
	})
}
