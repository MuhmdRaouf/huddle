package main

// The provider CLI's end to end, on the built binary: brief → run --wait → gates → review →
// accept over a real git repository, the gate-failure auto-fix, a return with feedback, the
// closed stdout, and the signals a follower lives through. The fake claude binary is the worker,
// so no network and no key are ever touched; every home, state root and repository is the test's
// own, and every detached driver a run leaves is killed when the test finishes. Ports of
// zai-plugin-cc's e2e suite (cli, epipe, follow, orphan); the golden transcript's legs live in
// internal/core/render and internal/core/cli.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/core/testkit"
	"github.com/MuhmdRaouf/huddle/internal/core/testkit/fakeclaude"
)

// providerReport is the change report the fake worker answers with.
var providerReport = map[string]any{
	"summary":     "Set the value to right",
	"files":       []map[string]string{{"path": "src/value.txt", "why": "the check expects right"}},
	"tests_added": []any{},
	"open_items":  []any{},
}

// providerJob is one scripted job's world: a real git repository, a state root, and the
// environment every CLI run of it shares.
type providerJob struct {
	t         *testing.T
	repo      testkit.TestRepo
	stateRoot string
	env       []string
}

// newProviderJob lays the world out and scripts the fake worker's edits: `edit` is what the first
// run writes (as `path:content`), `editOnResume` what a resumed session writes instead.
func newProviderJob(t *testing.T, edit, editOnResume, scenario string) *providerJob {
	t.Helper()
	repo := testkit.MakeRepo(t)
	repo.Write("src/value.txt", "wrong\n")
	repo.CommitAll("e2e fixture")
	sandbox := testkit.TempDir(t, "zai-e2e-")
	stateRoot := filepath.Join(sandbox, "state")
	report, err := json.Marshal(providerReport)
	if err != nil {
		t.Fatal(err)
	}
	env := providerChildEnv(t, sandbox)
	env = append(env,
		"ZAI_STATE_DIR="+stateRoot,
		"ZAI_CLAUDE_BIN="+fakeclaude.Build(t),
		"ZAI_API_KEY=zai-e2e-fake-key",
		"FAKE_CLAUDE_SCENARIO="+scenario,
		"FAKE_CLAUDE_REPORT="+string(report),
		"FAKE_CLAUDE_EDIT="+edit,
		"FAKE_CLAUDE_EDIT_ON_RESUME="+editOnResume,
	)
	job := &providerJob{t: t, repo: repo, stateRoot: stateRoot, env: env}
	t.Cleanup(func() { job.killDrivers() })
	return job
}

// providerChildEnv is the environment a CLI child runs in: a temp home and Claude config dir, and
// nothing of the machine's own provider state inherited.
func providerChildEnv(t *testing.T, home string) []string {
	t.Helper()
	set := map[string]string{
		"HOME":              home,
		"TMPDIR":            home,
		"CLAUDE_CONFIG_DIR": filepath.Join(home, "claude-config"),
	}
	drop := func(name string) bool {
		switch name {
		case "HOME", "TMPDIR", "TEMP", "TMP", "CLAUDE_CONFIG_DIR", "AGENTS_HUDDLE_HOME":
			return true
		}
		return strings.HasPrefix(name, "ZAI_") || strings.HasPrefix(name, "PLUGIN_CORE_") ||
			strings.HasPrefix(name, "FAKE_CLAUDE_")
	}
	env := make([]string, 0, len(os.Environ())+len(set))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !drop(name) {
			env = append(env, entry)
		}
	}
	for name, value := range set {
		env = append(env, name+"="+value)
	}
	for name, value := range testkit.ISOLATED_GIT_ENV {
		env = append(env, name+"="+value)
	}
	return env
}

// brief writes the scripted brief: the worker's edits, the gate that decides, and the front matter
// lines a scenario adds.
func (j *providerJob) brief(extra ...string) string {
	j.t.Helper()
	front := []string{
		"title: Fix the value",
		`scope: ["src/**"]`,
		"gates: [grep -q right src/value.txt]",
		"env: [FAKE_CLAUDE_SCENARIO, FAKE_CLAUDE_EDIT, FAKE_CLAUDE_EDIT_ON_RESUME, FAKE_CLAUDE_REPORT]",
	}
	front = append(front, extra...)
	path := filepath.Join(filepath.Dir(j.stateRoot), "brief.md")
	body := "---\n" + strings.Join(front, "\n") + "\n---\nMake the check pass.\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		j.t.Fatalf("write the brief: %v", err)
	}
	return path
}

// run runs the provider CLI once in the repository.
func (j *providerJob) run(args ...string) (stdout, stderr string, code int) {
	j.t.Helper()
	cmd := exec.Command(huddleBin, append([]string{"provider", "zai", "cli"}, args...)...)
	cmd.Env = j.env
	cmd.Dir = j.repo.Dir
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if exit, ok := err.(*exec.ExitError); ok {
		return out.String(), errb.String(), exit.ExitCode()
	}
	if err != nil {
		j.t.Fatalf("run zai cli %s: %v", strings.Join(args, " "), err)
	}
	return out.String(), errb.String(), 0
}

// start runs the provider CLI without waiting for it, so a test can signal it mid-run.
func (j *providerJob) start(stdout *os.File, args ...string) *exec.Cmd {
	j.t.Helper()
	cmd := exec.Command(huddleBin, append([]string{"provider", "zai", "cli"}, args...)...)
	cmd.Env = j.env
	cmd.Dir = j.repo.Dir
	cmd.Stdout = stdout
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		j.t.Fatalf("start zai cli %s: %v", strings.Join(args, " "), err)
	}
	j.t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})
	return cmd
}

// startedID is the job id the run's first line names.
func startedID(t *testing.T, firstLine string) string {
	t.Helper()
	match := regexp.MustCompile(`^zai job (\S+) started: Fix the value \(glm-5\.3, edit\)$`).
		FindStringSubmatch(firstLine)
	if match == nil {
		t.Fatalf("no started line: %q", firstLine)
	}
	return match[1]
}

// waitForState polls the job's file until it reaches state.
func (j *providerJob) waitForState(id, state string) map[string]any {
	j.t.Helper()
	var job map[string]any
	if err := testkit.Until(j.t, func() bool {
		raw, err := os.ReadFile(filepath.Join(j.stateRoot, "jobs", id, "job.json"))
		if err != nil {
			return false
		}
		if err := json.Unmarshal(raw, &job); err != nil {
			return false
		}
		return job["state"] == state
	}, testkit.UntilOptions{TimeoutMs: 30_000, Message: fmt.Sprintf("job %s never reached %s", id, state)}); err != nil {
		j.t.Fatal(err)
	}
	return job
}

// driverPid is the live driver a job's lock names.
func (j *providerJob) driverPid(id string) int {
	j.t.Helper()
	raw, err := os.ReadFile(filepath.Join(j.stateRoot, "jobs", id, "driver.lock"))
	if err != nil {
		j.t.Fatalf("read the driver lock: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		j.t.Fatalf("driver lock = %q: %v", raw, err)
	}
	return pid
}

// killDrivers ends every detached driver the job's runs left: the group first, then the leader.
func (j *providerJob) killDrivers() {
	entries, err := os.ReadDir(filepath.Join(j.stateRoot, "jobs"))
	if err != nil {
		return
	}
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(j.stateRoot, "jobs", entry.Name(), "driver.lock"))
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

// TestProviderCliE2E walks the provider CLI's user paths on the built binary.
func TestProviderCliE2E(t *testing.T) {
	t.Run("run --wait passes the gates and accept lands the commit", func(t *testing.T) {
		job := newProviderJob(t, "src/value.txt:right\n", "src/value.txt:right\n", "edit-file")
		brief := job.brief()

		stdout, stderr, code := job.run("run", brief, "--wait")
		if code != 0 {
			t.Fatalf("run exited %d: %s%s", code, stdout, stderr)
		}
		id := startedID(t, strings.SplitN(stdout, "\n", 2)[0])
		if lines := strings.SplitN(stdout, "\n", 2); lines[0] != "zai job "+id+" started: Fix the value (glm-5.3, edit)" {
			t.Fatalf("first line = %q", lines[0])
		}
		if !strings.Contains(stdout, "zai job "+id+": verdict pass (awaiting review)") {
			t.Fatalf("stdout never says the verdict passed:\n%s", stdout)
		}
		if got := job.repo.Read("src/value.txt"); got != "wrong\n" {
			t.Fatalf("the worktree already holds %q, want the untouched wrong", got)
		}

		review, _, code := job.run("review", id[:9], "--diff")
		if code != 0 {
			t.Fatalf("review exited %d", code)
		}
		if !strings.Contains(review, "+right") {
			t.Errorf("the review diff never shows +right:\n%s", review)
		}
		if !strings.Contains(review, "pass  grep -q right src/value.txt") {
			t.Errorf("the review never says the gate passed:\n%s", review)
		}

		accepted, stderr, code := job.run("accept", id)
		if code != 0 {
			t.Fatalf("accept exited %d: %s%s", code, accepted, stderr)
		}
		if got := job.repo.Read("src/value.txt"); got != "right\n" {
			t.Fatalf("after accept the file holds %q, want right", got)
		}
		if message := strings.TrimSpace(job.repo.Git("log", "-1", "--format=%B")); message !=
			"Fix the value\n\nSet the value to right\n\nWorked-by: glm-5.3 via zai" {
			t.Fatalf("the commit message = %q", message)
		}
		if status := job.repo.Git("status", "--porcelain"); status != "" {
			t.Fatalf("the repo is dirty after the landing: %q", status)
		}
		if branches := job.repo.Git("branch", "--list", "zai/"+id); branches != "" {
			t.Fatalf("the job's branch survived: %q", branches)
		}
		info, err := os.Stat(job.stateRoot)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("the state root is %v, want 0700", info.Mode().Perm())
		}
	})

	t.Run("a gate failure comes back as an auto-fix that resumes the session and passes", func(t *testing.T) {
		job := newProviderJob(t, "src/value.txt:still wrong\n", "src/value.txt:right\n", "edit-file")
		brief := job.brief()

		stdout, stderr, code := job.run("run", brief, "--wait", "--json")
		if code != 0 {
			t.Fatalf("run exited %d: %s%s", code, stdout, stderr)
		}
		var landed struct {
			ID       string `json:"id"`
			Attempts []struct {
				Kind      string  `json:"kind"`
				Verdict   *string `json:"verdict"`
				SessionID string  `json:"sessionId"`
				Prompt    string  `json:"prompt"`
			} `json:"attempts"`
		}
		if err := json.Unmarshal([]byte(stdout), &landed); err != nil {
			t.Fatalf("run --json printed %q: %v", stdout, err)
		}
		if len(landed.Attempts) != 2 ||
			landed.Attempts[0].Kind != "initial" || verdict(landed.Attempts[0].Verdict) != "gate_fail" ||
			landed.Attempts[1].Kind != "auto_fix" || verdict(landed.Attempts[1].Verdict) != "pass" {
			t.Fatalf("attempts = %s", attemptsOf(t, stdout))
		}
		if landed.Attempts[1].SessionID != landed.Attempts[0].SessionID {
			t.Errorf("the auto-fix ran on session %s, want the first attempt's %s",
				landed.Attempts[1].SessionID, landed.Attempts[0].SessionID)
		}
		if !strings.Contains(landed.Attempts[1].Prompt, "Gate failed: `grep -q right src/value.txt` (exit 1)") {
			t.Errorf("the auto-fix prompt never names the failed gate:\n%s", landed.Attempts[1].Prompt)
		}
	})

	t.Run("a return with feedback runs a review-fix that accept lands", func(t *testing.T) {
		job := newProviderJob(t, "src/value.txt:right\n", "src/extra.txt:added on review\n", "edit-file")
		brief := job.brief()

		stdout, _, code := job.run("run", brief, "--wait", "--json")
		if code != 0 {
			t.Fatalf("run exited %d", code)
		}
		var landed struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(stdout), &landed); err != nil {
			t.Fatalf("run --json printed %q: %v", stdout, err)
		}
		id := landed.ID

		_, stderr, code := job.run("return", id, "Also add src/extra.txt.")
		if code != 0 {
			t.Fatalf("return exited %d: %s", code, stderr)
		}
		settled := job.waitForState(id, "awaiting_review")
		attempts, _ := settled["attempts"].([]any)
		if len(attempts) != 2 || attemptField(t, attempts[0], "kind") != "initial" ||
			attemptField(t, attempts[0], "verdict") != "pass" ||
			attemptField(t, attempts[1], "kind") != "review_fix" ||
			attemptField(t, attempts[1], "verdict") != "pass" {
			t.Fatalf("attempts = %v", attempts)
		}
		if feedback := attemptField(t, attempts[1], "feedback"); feedback != "Also add src/extra.txt." {
			t.Fatalf("feedback = %q", feedback)
		}

		if _, stderr, code := job.run("accept", id); code != 0 {
			t.Fatalf("accept exited %d: %s", code, stderr)
		}
		if got := job.repo.Read("src/value.txt"); got != "right\n" {
			t.Errorf("after accept src/value.txt holds %q", got)
		}
		if got := job.repo.Read("src/extra.txt"); got != "added on review\n" {
			t.Errorf("after accept src/extra.txt holds %q", got)
		}
	})

	t.Run("a closed stdout ends the run quietly once the pipe's reader goes away", func(t *testing.T) {
		job := newProviderJob(t, "src/value.txt:right\n", "src/value.txt:right\n", "edit-file")
		brief := job.brief()

		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		child := job.start(writer, "run", brief, "--wait")
		writer.Close()

		first := make(chan string, 1)
		go func() {
			line, _ := bufioReadLine(reader)
			first <- line
		}()
		var line string
		select {
		case line = <-first:
		case <-time.After(30 * time.Second):
			t.Fatal("the run never printed its first line")
		}
		startedID(t, line)
		// The reader closes after the first line; the summary (written once the job lands, one
		// 2 s poll later at the earliest) then hits a closed pipe.
		reader.Close()

		done := make(chan error, 1)
		go func() { done <- child.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("the run ended with %v, want exit 0", err)
			}
		case <-time.After(60 * time.Second):
			t.Fatal("the run outlived the closed pipe")
		}
		if stderr := childStderr(child); stderr != "" {
			t.Fatalf("the closed-pipe run wrote stderr: %q", stderr)
		}
	})

	t.Run("a killed follower leaves the job running; wait re-attaches and prints the summary", func(t *testing.T) {
		job := newProviderJob(t, "src/value.txt:right\n", "src/value.txt:right\n", "sleep")
		brief := job.brief("timeout: 2s", "retries: { fix: 0, infra: 0 }")

		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		child := job.start(writer, "run", brief, "--wait")
		writer.Close()
		line := readFirstLine(t, reader, 30*time.Second)
		id := startedID(t, line)
		job.waitForState(id, "running")

		if err := child.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- child.Wait() }()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatal("the killed follower never ended")
		}
		if pid := job.driverPid(id); !testkit.IsPidAlive(pid) {
			t.Fatalf("the driver %d died with its follower", pid)
		}

		waited, stderr, code := job.run("wait", id)
		if code != 3 {
			t.Fatalf("wait exited %d (%s), want 3", code, stderr)
		}
		if stderr != "" {
			t.Fatalf("wait wrote stderr: %q", stderr)
		}
		want := regexp.MustCompile(`(?s)^zai job ` + regexp.QuoteMeta(id) + `: verdict timeout \(awaiting review\)\n`)
		if !want.MatchString(waited) {
			t.Fatalf("wait printed:\n%s", waited)
		}
		if next := "Next: the worker never finished (timed out): nothing to accept. Fix the cause, then /zai:review " +
			id + " to return it to try again, or discard it."; !strings.Contains(waited, next) {
			t.Fatalf("wait never says the next step:\n%s", waited)
		}
		again, _, _ := job.run("wait", id)
		if again != waited {
			t.Fatalf("the second wait printed a different summary:\n%s\n---\n%s", again, waited)
		}
	})

	t.Run("a tree-killed follower leaves the driver; the job lands in review", func(t *testing.T) {
		job := newProviderJob(t, "src/value.txt:right\n", "src/value.txt:right\n", "sleep")
		brief := job.brief("timeout: 2s", "retries: { fix: 0, infra: 0 }")

		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		child := job.start(writer, "run", brief, "--wait")
		writer.Close()
		line := readFirstLine(t, reader, 30*time.Second)
		id := startedID(t, line)
		job.waitForState(id, "running")

		driver := job.driverPid(id)
		table, err := testkit.ParentTable()
		if err != nil {
			t.Fatal(err)
		}
		// What Claude Code does to a background Bash shell it cleans up: SIGTERM the whole tree,
		// SIGKILL it ~300 ms later.
		tree := append([]int{child.Process.Pid}, testkit.Descendants(child.Process.Pid, table)...)
		for _, pid := range tree {
			_ = syscall.Kill(pid, syscall.SIGTERM)
		}
		time.Sleep(300 * time.Millisecond)
		for _, pid := range tree {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		_, _ = child.Process.Wait()

		if slices.Contains(tree, driver) {
			t.Fatalf("the tree-kill reached the driver %d: %v", driver, tree)
		}
		waited, stderr, code := job.run("wait", id)
		if code != 3 {
			t.Fatalf("wait exited %d (%s), want 3", code, stderr)
		}
		if !regexp.MustCompile(`(?s)^zai job ` + regexp.QuoteMeta(id) + `: verdict timeout \(awaiting review\)\n`).
			MatchString(waited) {
			t.Fatalf("wait printed:\n%s", waited)
		}
	})
}

// verdict is an attempt's verdict or "(none)".
func verdict(v *string) string {
	if v == nil {
		return "(none)"
	}
	return *v
}

// attemptsOf is a job's attempt kinds and verdicts, for a failure message.
func attemptsOf(t *testing.T, stdout string) string {
	t.Helper()
	var parsed struct {
		Attempts []struct {
			Kind    string  `json:"kind"`
			Verdict *string `json:"verdict"`
		} `json:"attempts"`
	}
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		return stdout
	}
	var parts []string
	for _, attempt := range parsed.Attempts {
		parts = append(parts, attempt.Kind+"="+verdict(attempt.Verdict))
	}
	return strings.Join(parts, ", ")
}

// attemptField is one attempt object's string field.
func attemptField(t *testing.T, attempt any, field string) string {
	t.Helper()
	object, ok := attempt.(map[string]any)
	if !ok {
		t.Fatalf("attempt %v is not an object", attempt)
	}
	value, _ := object[field].(string)
	return value
}

// readFirstLine reads one line off r, failing after the timeout.
func readFirstLine(t *testing.T, r *os.File, timeout time.Duration) string {
	t.Helper()
	line := make(chan string, 1)
	go func() {
		text, _ := bufioReadLine(r)
		line <- text
	}()
	select {
	case got := <-line:
		return got
	case <-time.After(timeout):
		t.Fatal("the run never printed its first line")
		return ""
	}
}

// bufioReadLine reads one newline-terminated line from r.
func bufioReadLine(r *os.File) (string, error) {
	var line []byte
	one := make([]byte, 1)
	for {
		n, err := r.Read(one)
		if n > 0 {
			if one[0] == '\n' {
				return string(line), nil
			}
			line = append(line, one[0])
		}
		if err != nil {
			return string(line), err
		}
	}
}

// childStderr is what a started child wrote to the stderr buffer the job gave it.
func childStderr(child *exec.Cmd) string {
	buffer, ok := child.Stderr.(*strings.Builder)
	if !ok {
		return ""
	}
	return buffer.String()
}
