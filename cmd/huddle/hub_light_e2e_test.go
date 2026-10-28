package main

// The hub's light guard: the built binary serves temp homes seeded like a busy machine, and
// three runs from identical copies of two seeded homes hold it to its budgets. Run A settles
// and stops and is the polled phase's baseline; run B adds 20 s of idle, its CPU read live at
// both ends; run C adds the fixed 30 s polled workload and stops, its whole-life CPU minus the
// baseline's. The resident size is sampled while the idle and the polled phases run.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	// hubIdleCPUBudget is the most CPU the hub may spend over 20 s of quiet: no clients, no
	// new records, nothing for it to do.
	hubIdleCPUBudget = 200 * time.Millisecond
	// hubPolledCPUBudget is the most CPU the hub may spend over the fixed 30 s polled
	// workload: two web clients that hold the Radar stream and send three poll ticks each,
	// while a writer appends 30 request lines and opens 10 new sessions.
	hubPolledCPUBudget = 12500 * time.Millisecond
	// hubRSSCeilingMB is the most a busy hub may hold resident: a 200k-record Radar store with its
	// views and the web pages fits well under it.
	hubRSSCeilingMB = 300

	hubLightToken = "hub-light-root-token"
	// hubLightSeedBudget is the most the template's transcripts may take to write.
	hubLightSeedBudget = 5 * time.Second

	busyLines     = 400
	smallSessions = 160
	// idleSeedBusy is the busy sessions of the idle runs' seed: 2,000 requests.
	idleSeedBusy = 5
	// polledSeedBusy is the busy sessions of the polled run's seed: 20,000 requests, which
	// with the small ones sits past the store's 200-session cap, so session eviction runs
	// the way it does on a busy machine.
	polledSeedBusy = 50

	// idleSeedRequests and polledSeedRequests are the request counts the two seeds carry and
	// their settles wait for.
	idleSeedRequests   = idleSeedBusy * busyLines
	polledSeedRequests = polledSeedBusy * busyLines
)

// hubLightModels is the models the seeded requests rotate through.
var hubLightModels = []string{"glm-5.3", "glm-5.3-flash", "claude-opus-5-5", "kimi-k3"}

// hubLightTickPaths is the exact request list one poll tick sends, read off the web app's
// controller: the POLL_EVERY_MS interval runs pollInsights, which fetches five Radar reads
// under /api/radar and the hub's own settings for the clock's zone.
var hubLightTickPaths = []string{
	"/api/radar/api/alerts",
	"/api/radar/api/budget-status",
	"/api/radar/api/spend",
	"/api/radar/api/health",
	"/api/radar/api/quota",
	"/api/settings",
}

func TestE2EHubStaysLight(t *testing.T) {
	switch runtime.GOOS {
	case "darwin", "linux":
	default:
		t.Skipf("no RSS sampler for %s", runtime.GOOS)
	}
	client := &http.Client{}
	// One seed per phase, both laid out once and copied fresh per run: the idle runs settle
	// the small one, the polled run settles the busy one.
	idleTemplate := t.TempDir()
	seedHubLightTemplate(t, idleTemplate, idleSeedBusy)
	polledTemplate := t.TempDir()
	seedHubLightTemplate(t, polledTemplate, polledSeedBusy)

	// Run A, the polled phase's baseline: the settle alone, then the hub's whole-life CPU out
	// of its rusage.
	logA, _, _ := startHubLightRun(t, client, idleTemplate, idleSeedRequests)
	cpuA := stopAndMeasure(t, logA)

	// Run B, idle: the same settle, then 20 s with no clients and no writes, the hub's CPU
	// read live at both ends.
	logB, _, _ := startHubLightRun(t, client, idleTemplate, idleSeedRequests)
	pidB := logB.cmd.Process.Pid
	idleStart := processCPU(t, pidB)
	idleRSS := sampleRSS(t, pidB, 20*time.Second, 5*time.Second)
	idleCPU := processCPU(t, pidB) - idleStart

	// Run C, polled: the busy seed, then the fixed 30 s workload of two clients and a writer.
	logC, hubURL, transcripts := startHubLightRun(t, client, polledTemplate, polledSeedRequests)
	workload := startHubLightWorkload(t, client, hubURL, transcripts)
	polledRSS := sampleRSS(t, logC.cmd.Process.Pid, 30*time.Second, 5*time.Second)
	workload.stop()
	workload.reportErrors(t)
	cpuC := stopAndMeasure(t, logC)

	polledCPU := cpuC - cpuA
	peakMB := max(idleRSS, polledRSS) / (1024 * 1024)

	t.Logf("hub light: baseline %v, idle %v cpu, polled %v cpu, peak rss %d MB",
		cpuA, idleCPU, polledCPU, peakMB)
	if idleCPU >= hubIdleCPUBudget {
		t.Errorf("idle CPU %v over 20 s is at or over the %v budget", idleCPU, hubIdleCPUBudget)
	}
	if polledCPU >= hubPolledCPUBudget {
		t.Errorf("polled CPU %v over the 30 s workload is at or over the %v budget", polledCPU, hubPolledCPUBudget)
	}
	if peakMB >= hubRSSCeilingMB {
		t.Errorf("peak rss %d MB is at or over the %d MB ceiling", peakMB, hubRSSCeilingMB)
	}
}

// seedHubLightTemplate writes the one home a phase's runs start from, under its user's
// .claude/projects/-tmp-hub-light: busy sessions of 400 assistant lines each over the last 6
// hours, and 160 small sessions from 2 to 3 days ago, one request each. The whole seed must
// land in under hubLightSeedBudget.
func seedHubLightTemplate(t *testing.T, root string, busy int) {
	t.Helper()
	user := filepath.Join(root, "user")
	transcripts := filepath.Join(user, ".claude", "projects", "-tmp-hub-light")
	for _, dir := range []string{
		transcripts,
		filepath.Join(user, "data"),
		filepath.Join(root, "home"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("make %s: %v", dir, err)
		}
	}
	started := time.Now()
	now := time.Now()
	// the busy lines walk the whole 6-hour window, one session's answers a step apart
	lineStep := time.Duration(int64((6*time.Hour)/busyLines/time.Millisecond)) * time.Millisecond
	for s := 0; s < busy; s++ {
		session := fmt.Sprintf("light-busy-%02d", s)
		first := now.Add(-6 * time.Hour).Add(time.Duration(s) * time.Second)
		writeHubLightTranscript(t, filepath.Join(transcripts, session+".jsonl"), func(w *bufio.Writer) error {
			for i := 0; i < busyLines; i++ {
				at := first.Add(time.Duration(i) * lineStep)
				line, err := hubLightAssistantLine(session, i, at, hubLightModels[(s+i)%len(hubLightModels)])
				if err != nil {
					return err
				}
				if _, err := w.WriteString(line); err != nil {
					return err
				}
			}
			return nil
		})
	}
	for s := 0; s < smallSessions; s++ {
		session := fmt.Sprintf("light-small-%03d", s)
		at := now.Add(-time.Duration(48+s%24) * time.Hour)
		writeHubLightTranscript(t, filepath.Join(transcripts, session+".jsonl"), func(w *bufio.Writer) error {
			return writeHubLightTurn(w, session, at, hubLightModels[s%len(hubLightModels)])
		})
	}
	if spent := time.Since(started); spent >= hubLightSeedBudget {
		t.Fatalf("the seed took %s, want under %s", spent.Round(time.Millisecond), hubLightSeedBudget)
	}
	t.Logf("hub light: the template seeded in %s", time.Since(started).Round(time.Millisecond))
}

// writeHubLightTranscript creates one transcript file and fills it through a buffered writer.
func writeHubLightTranscript(t *testing.T, path string, fill func(w *bufio.Writer) error) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	w := bufio.NewWriter(file)
	if err := fill(w); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("flush %s: %v", path, err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}

// writeHubLightTurn writes the small session's whole transcript: a user line and its answer.
func writeHubLightTurn(w *bufio.Writer, session string, at time.Time, model string) error {
	question, err := hubLightUserLine(session, at)
	if err != nil {
		return err
	}
	if _, err := w.WriteString(question); err != nil {
		return err
	}
	answer, err := hubLightAssistantLine(session, 0, at.Add(30*time.Second), model)
	if err != nil {
		return err
	}
	_, err = w.WriteString(answer)
	return err
}

// startHubLightRun copies the template and starts one hub on the copy, settled the same way
// every run settles: radar's summary at the seeded request count, the watcher's catching-up
// word false on the stream's snapshot, then 5 s more. The run's seed directory comes back for
// a phase that writes.
func startHubLightRun(t *testing.T, client *http.Client, template string, requests int64) (*serveLog, string, string) {
	t.Helper()
	run := t.TempDir()
	if err := os.CopyFS(run, os.DirFS(template)); err != nil {
		t.Fatalf("copy the template home: %v", err)
	}
	home := filepath.Join(run, "home")
	user := filepath.Join(run, "user")
	transcripts := filepath.Join(user, ".claude", "projects", "-tmp-hub-light")
	port := pinFreePort(t, home)
	log := startServe(t, t, huddleBin, serveEnv{
		home: home, user: user, port: port, token: hubLightToken,
		data: filepath.Join(user, "data"),
		// the hub records itself in its own RADAR_HOME, kept beside the run's other homes
		extra: map[string]string{"RADAR_HOME": filepath.Join(run, "hub-radar")},
	})
	hubURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	until(t, 30*time.Second, func() bool { return healthy(hubURL) }, "the hub to answer /health")
	last := int64(-1)
	readAt := time.Now().Add(-time.Minute)
	until(t, 120*time.Second, func() bool {
		// the summary read is the hub's costliest answer while the store changes: one every
		// half second is enough to watch the count climb
		if time.Since(readAt) < 500*time.Millisecond {
			return last >= requests
		}
		readAt = time.Now()
		last = hubLightSummaryRequests(t, client, hubURL)
		return last >= requests
	}, fmt.Sprintf("radar's summary to count %d requests", requests))
	until(t, 120*time.Second, func() bool { return hubLightStreamSettled(t, client, hubURL) },
		"the watcher to finish its first catch-up")
	time.Sleep(5 * time.Second)
	return log, hubURL, transcripts
}

// hubLightSummaryRequests reads radar's summary through the hub, under the hub's credential
// the way the tab's own reads carry it.
func hubLightSummaryRequests(t *testing.T, client *http.Client, hubURL string) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, hubURL+"/api/radar/api/summary", nil)
	if err != nil {
		t.Fatalf("build the summary read: %v", err)
	}
	req.Header.Set("x-huddle-token", hubLightToken)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("read the summary: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read the summary's body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the summary answered %d: %s", resp.StatusCode, raw)
	}
	var answer struct {
		Summary struct {
			Requests int64 `json:"requests"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		t.Fatalf("the summary's body %s: %v", raw, err)
	}
	return answer.Summary.Requests
}

// hubLightStreamSettled opens the stream the web app holds, reads its first snapshot frame and
// answers whether the watcher's catching-up word in it is false. Anything that goes wrong
// answers false, so until retries the whole read.
func hubLightStreamSettled(t *testing.T, client *http.Client, hubURL string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, hubURL+"/api/radar/api/stream", nil)
	if err != nil {
		return false
	}
	req.Header.Set("x-huddle-token", hubLightToken)
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data: ")
		if !ok {
			continue
		}
		var frame struct {
			Type       string `json:"type"`
			CatchingUp bool   `json:"catchingUp"`
		}
		if json.Unmarshal([]byte(data), &frame) != nil || frame.Type != "snapshot" {
			continue
		}
		return !frame.CatchingUp
	}
	return false
}

// stopAndMeasure stops the hub a run started and answers the CPU time its whole life cost,
// exactly what the kernel holds as the child's rusage once it is reaped: user plus system.
// The interrupt gives the hub its graceful stop; one still standing 15 s in is killed. The
// cleanup startServe registered waits on the child a second time, which only errors, and it
// ignores that error.
func stopAndMeasure(t *testing.T, log *serveLog) time.Duration {
	t.Helper()
	if log.cmd == nil || log.cmd.Process == nil {
		t.Fatal("the hub was never started, so there is nothing to stop and measure")
	}
	if log.cmd.ProcessState != nil {
		t.Fatal("the hub was already waited on, so its rusage is spent")
	}
	if err := log.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("interrupt the hub: %v", err)
	}
	done := make(chan struct{})
	go func() {
		_ = log.cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		_ = log.cmd.Process.Kill()
		<-done
	}
	state := log.cmd.ProcessState
	if state == nil {
		t.Fatal("the hub exited without a process state, so its CPU time is unreadable")
	}
	return state.UserTime() + state.SystemTime()
}

// sampleRSS walks one phase, sampling the hub's resident size every interval, and answers the
// largest sample.
func sampleRSS(t *testing.T, pid int, total, interval time.Duration) int64 {
	t.Helper()
	var peak int64
	for at := interval; at <= total; at += interval {
		time.Sleep(interval)
		if rss := processRSS(t, pid); rss > peak {
			peak = rss
		}
	}
	return peak
}

// clockTicksPerSecond is the kernel's CLK_TCK, 100 on every Linux runner: /proc's tick counts
// are hundredths of a second.
const clockTicksPerSecond = 100

// processCPU answers the CPU time the kernel holds for a live process, user plus system: the
// tick counts of /proc's stat on Linux, ps's TIME column on darwin. Any other OS has no
// sampler and skips the run.
func processCPU(t *testing.T, pid int) time.Duration {
	t.Helper()
	switch runtime.GOOS {
	case "linux":
		raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			t.Fatalf("read /proc/%d/stat: %v", pid, err)
		}
		user, system, err := parseProcStat(string(raw))
		if err != nil {
			t.Fatalf("parse /proc/%d/stat: %v", pid, err)
		}
		return time.Duration((user + system) * (int64(time.Second) / clockTicksPerSecond))
	case "darwin":
		out, err := exec.Command("ps", "-o", "cputime=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			t.Fatalf("ps -o cputime= -p %d: %v", pid, err)
		}
		spent, err := parsePsCputime(string(out))
		if err != nil {
			t.Fatalf("parse ps -o cputime= for %d: %v", pid, err)
		}
		return spent
	default:
		t.Skipf("no CPU sampler for %s", runtime.GOOS)
		return 0
	}
}

// parseProcStat reads one /proc/<pid>/stat body's utime and stime: the two tick counts that
// stand at fields 14 and 15, counted after the `)` that closes the command name, which may
// hold spaces of its own.
func parseProcStat(stat string) (user, system int64, err error) {
	comm := strings.LastIndex(stat, ")")
	if comm < 0 {
		return 0, 0, fmt.Errorf("no command name in the stat")
	}
	fields := strings.Fields(stat[comm+1:])
	if len(fields) < 13 {
		return 0, 0, fmt.Errorf("the stat holds %d fields after the command, want at least 13", len(fields))
	}
	if user, err = strconv.ParseInt(fields[11], 10, 64); err != nil {
		return 0, 0, fmt.Errorf("the utime ticks %q: %w", fields[11], err)
	}
	if system, err = strconv.ParseInt(fields[12], 10, 64); err != nil {
		return 0, 0, fmt.Errorf("the stime ticks %q: %w", fields[12], err)
	}
	return user, system, nil
}

// parsePsCputime reads one ps TIME column as a duration: `H:MM:SS.cc` or `M:SS.cc`.
func parsePsCputime(out string) (time.Duration, error) {
	fields := strings.Fields(out)
	if len(fields) != 1 {
		return 0, fmt.Errorf("ps cputime answered %q, want one time", out)
	}
	parts := strings.Split(fields[0], ":")
	if len(parts) != 2 && len(parts) != 3 {
		return 0, fmt.Errorf("the ps time %q does not read `H:MM:SS.cc` or `M:SS.cc`", fields[0])
	}
	seconds := 0.0
	for _, part := range parts {
		value, err := strconv.ParseFloat(part, 64)
		if err != nil {
			return 0, fmt.Errorf("the ps time %q: %w", fields[0], err)
		}
		seconds = seconds*60 + value
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

// processRSS answers a live process's resident size in bytes: the VmRSS line of /proc's status
// on Linux, ps's rss column on darwin. Any other OS has no sampler and skips the run.
func processRSS(t *testing.T, pid int) int64 {
	t.Helper()
	switch runtime.GOOS {
	case "linux":
		raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
		if err != nil {
			t.Fatalf("read /proc/%d/status: %v", pid, err)
		}
		size, err := parseStatusRSS(string(raw))
		if err != nil {
			t.Fatalf("parse /proc/%d/status: %v", pid, err)
		}
		return size
	case "darwin":
		out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			t.Fatalf("ps -o rss= -p %d: %v", pid, err)
		}
		size, err := parsePsRSS(string(out))
		if err != nil {
			t.Fatalf("parse ps -o rss= for %d: %v", pid, err)
		}
		return size
	default:
		t.Skipf("no RSS sampler for %s", runtime.GOOS)
		return 0
	}
}

// parseStatusRSS reads one /proc/<pid>/status body's VmRSS line as bytes.
func parseStatusRSS(status string) (int64, error) {
	for _, line := range strings.Split(status, "\n") {
		text, ok := strings.CutPrefix(line, "VmRSS:")
		if !ok {
			continue
		}
		fields := strings.Fields(text)
		if len(fields) != 2 || fields[1] != "kB" {
			return 0, fmt.Errorf("the VmRSS line %q does not read `<size> kB`", line)
		}
		kb, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("the VmRSS size %q: %w", fields[0], err)
		}
		return kb * 1024, nil
	}
	return 0, fmt.Errorf("no VmRSS line in the status")
}

// parsePsRSS reads one `ps -o rss=` answer as bytes: a single KB count.
func parsePsRSS(out string) (int64, error) {
	fields := strings.Fields(out)
	if len(fields) != 1 {
		return 0, fmt.Errorf("ps -o rss= answered %q, want one KB count", out)
	}
	kb, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("the ps rss count %q: %w", fields[0], err)
	}
	return kb * 1024, nil
}

// TestParseCPU pins both CPU parsers on fixed strings: /proc's stat tail and ps's TIME column.
func TestParseCPU(t *testing.T) {
	t.Run("the tail of a /proc stat", func(t *testing.T) {
		stat := "4423 (huddle serve) S 1 4423 4423 0 -1 4194560 123 0 4 0 250 75 0 0 20 0 12 0 12345 234567890 1234 18446744073709551615 1 1 0 0 0 0 0 16777216 0 0 0 17 0 0 0 0 0 0 0 0 0 0 0 0 0 0\n"
		user, system, err := parseProcStat(stat)
		if err != nil {
			t.Fatalf("parseProcStat: %v", err)
		}
		if user != 250 || system != 75 {
			t.Errorf("parseProcStat = %d, %d; want 250, 75", user, system)
		}
		// a command name holding a space and a paren must not move the counts
		paren := "4423 (huddle serve (dev)) S 1 4423 4423 0 -1 4194560 123 0 4 0 250 75 0 0 20 0 12 0 12345 234567890 1234 18446744073709551615 1 1 0 0 0 0 0 16777216 0 0 0 17 0 0 0 0 0 0 0 0 0 0 0 0 0 0\n"
		user, system, err = parseProcStat(paren)
		if err != nil {
			t.Fatalf("parseProcStat: %v", err)
		}
		if user != 250 || system != 75 {
			t.Errorf("parseProcStat = %d, %d; want 250, 75", user, system)
		}
		if _, _, err := parseProcStat("4423 S 1 2\n"); err == nil {
			t.Errorf("parseProcStat without a command name, want an error")
		}
		if _, _, err := parseProcStat("4423 (huddle) S 1 2\n"); err == nil {
			t.Errorf("parseProcStat short after the command, want an error")
		}
	})
	t.Run("ps's TIME column", func(t *testing.T) {
		got, err := parsePsCputime("4:32.10\n")
		if err != nil {
			t.Fatalf("parsePsCputime: %v", err)
		}
		if want := 4*time.Minute + 32*time.Second + 100*time.Millisecond; got != want {
			t.Errorf("parsePsCputime = %v, want %v", got, want)
		}
		got, err = parsePsCputime("1:02:03.45\n")
		if err != nil {
			t.Fatalf("parsePsCputime: %v", err)
		}
		if want := time.Hour + 2*time.Minute + 3*time.Second + 450*time.Millisecond; got != want {
			t.Errorf("parsePsCputime = %v, want %v", got, want)
		}
		got, err = parsePsCputime("1:02:03\n")
		if err != nil {
			t.Fatalf("parsePsCputime: %v", err)
		}
		if want := time.Hour + 2*time.Minute + 3*time.Second; got != want {
			t.Errorf("parsePsCputime = %v, want %v", got, want)
		}
		if _, err := parsePsCputime(""); err == nil {
			t.Errorf("parsePsCputime of an empty answer, want an error")
		}
		if _, err := parsePsCputime("soon"); err == nil {
			t.Errorf("parsePsCputime of a word where a time belongs, want an error")
		}
	})
}

// TestParseRSS pins both RSS parsers on fixed strings: /proc's VmRSS line and ps's rss column.
func TestParseRSS(t *testing.T) {
	t.Run("the VmRSS line of a /proc status", func(t *testing.T) {
		status := "Name:\thuddle\n" +
			"Umask:\t0022\n" +
			"VmPeak:\t 1112134 kB\n" +
			"VmRSS:\t  234567 kB\n" +
			"Threads:\t12\n"
		got, err := parseStatusRSS(status)
		if err != nil {
			t.Fatalf("parseStatusRSS: %v", err)
		}
		if want := int64(234567) * 1024; got != want {
			t.Errorf("parseStatusRSS = %d, want %d", got, want)
		}
		if got, err := parseStatusRSS("Name:\thuddle\nPid:\t42\n"); err == nil {
			t.Errorf("parseStatusRSS without a VmRSS line = %d, want an error", got)
		}
		if got, err := parseStatusRSS("VmRSS:\tzero kB\n"); err == nil {
			t.Errorf("parseStatusRSS of a word where a size belongs = %d, want an error", got)
		}
	})
	t.Run("ps's rss column", func(t *testing.T) {
		got, err := parsePsRSS("  3216\n")
		if err != nil {
			t.Fatalf("parsePsRSS: %v", err)
		}
		if want := int64(3216) * 1024; got != want {
			t.Errorf("parsePsRSS = %d, want %d", got, want)
		}
		if _, err := parsePsRSS(""); err == nil {
			t.Errorf("parsePsRSS of an empty answer, want an error")
		}
		if _, err := parsePsRSS("12 34"); err == nil {
			t.Errorf("parsePsRSS of two counts, want an error")
		}
	})
}

// hubLightWorkload is the polled phase's traffic: two web clients and one writer, each a
// goroutine that ends when the workload stops.
type hubLightWorkload struct {
	client *http.Client
	done   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
	errs   chan error
}

// startHubLightWorkload runs the fixed 30 s polled workload against the hub: two clients, each
// holding the Radar stream open and sending three poll ticks at 0 s, 10 s and 20 s, and one
// writer appending one assistant line a second to the busy sessions in turn while it starts a
// new session every 3 s. The whole workload stops on the test's cleanup too.
func startHubLightWorkload(t *testing.T, client *http.Client, hubURL, transcripts string) *hubLightWorkload {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	w := &hubLightWorkload{client: client, done: make(chan struct{}), ctx: ctx, cancel: cancel, errs: make(chan error, 64)}
	for n := 1; n <= 2; n++ {
		w.wg.Add(1)
		go func() {
			defer w.wg.Done()
			w.runClient(hubURL, n)
		}()
	}
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		w.runWriter(transcripts)
	}()
	t.Cleanup(w.stop)
	return w
}

// stop ends the workload now and waits for every goroutine in it.
func (w *hubLightWorkload) stop() {
	w.once.Do(func() {
		close(w.done)
		w.cancel()
	})
	w.wg.Wait()
}

// reportErrors fails the test with everything the workload's goroutines hit.
func (w *hubLightWorkload) reportErrors(t *testing.T) {
	t.Helper()
	close(w.errs)
	for err := range w.errs {
		t.Errorf("the polled workload: %v", err)
	}
}

// runClient is one web client: the Radar stream held open and drained, and exactly three poll
// ticks at 0 s, 10 s and 20 s, each request read to the end and checked for 200.
func (w *hubLightWorkload) runClient(hubURL string, n int) {
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		w.drainStream(hubURL)
	}()
	start := time.Now()
	for tick, at := range []time.Duration{0, 10 * time.Second, 20 * time.Second} {
		if !w.sleepUntil(start.Add(at)) {
			return
		}
		ctx, cancel := context.WithTimeout(w.ctx, 25*time.Second)
		for _, path := range hubLightTickPaths {
			if err := hubLightGet(ctx, w.client, hubURL, path); err != nil {
				cancel()
				w.errs <- fmt.Errorf("client %d's tick %d: %w", n, tick+1, err)
				return
			}
		}
		cancel()
	}
}

// drainStream holds one Radar stream open and discards its frames, the way a tab's EventSource
// does, until the workload stops or the stream breaks.
func (w *hubLightWorkload) drainStream(hubURL string) {
	req, err := http.NewRequestWithContext(w.ctx, http.MethodGet, hubURL+"/api/radar/api/stream", nil)
	if err != nil {
		w.errs <- err
		return
	}
	req.Header.Set("x-huddle-token", hubLightToken)
	resp, err := w.client.Do(req)
	if err != nil {
		select {
		case <-w.done:
		default:
			w.errs <- err
		}
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		w.errs <- fmt.Errorf("the stream answered %d", resp.StatusCode)
		return
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4<<20), 4<<20)
	for scanner.Scan() {
	}
}

// runWriter adds the polled phase's records for 30 s: one assistant line each second, walking
// the busy sessions in turn, and a new session's transcript every 3 s — 30 lines, 10 sessions.
func (w *hubLightWorkload) runWriter(transcripts string) {
	start := time.Now()
	for second := 1; second <= 30; second++ {
		if !w.sleepUntil(start.Add(time.Duration(second) * time.Second)) {
			return
		}
		session := fmt.Sprintf("light-busy-%02d", (second-1)%polledSeedBusy)
		line, err := hubLightAssistantLine(session, busyLines+second, time.Now(),
			hubLightModels[second%len(hubLightModels)])
		if err != nil {
			w.errs <- err
			return
		}
		if err := appendHubLightLine(filepath.Join(transcripts, session+".jsonl"), line); err != nil {
			w.errs <- err
			return
		}
		if second%3 == 0 {
			if err := w.writeNewSession(transcripts, second/3, time.Now()); err != nil {
				w.errs <- err
				return
			}
		}
	}
}

// writeNewSession lays one new session's transcript down: a user line and its answer, both
// stamped now.
func (w *hubLightWorkload) writeNewSession(transcripts string, n int, at time.Time) error {
	session := fmt.Sprintf("light-new-%02d", n)
	question, err := hubLightUserLine(session, at)
	if err != nil {
		return err
	}
	answer, err := hubLightAssistantLine(session, 0, at.Add(time.Second),
		hubLightModels[n%len(hubLightModels)])
	if err != nil {
		return err
	}
	path := filepath.Join(transcripts, session+".jsonl")
	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if _, err := file.WriteString(question + answer); err != nil {
		file.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	return file.Close()
}

// sleepUntil waits until at, answering false once the workload is stopped.
func (w *hubLightWorkload) sleepUntil(at time.Time) bool {
	after := time.NewTimer(time.Until(at))
	defer after.Stop()
	select {
	case <-after.C:
		return true
	case <-w.done:
		return false
	}
}

// hubLightGet sends one GET the way the web app does, reads the answer to the end, and names
// any status but 200.
func hubLightGet(ctx context.Context, client *http.Client, hubURL, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, hubURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-huddle-token", hubLightToken)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %d", path, resp.StatusCode)
	}
	return nil
}

// appendHubLightLine adds one line to an existing transcript, the way Claude Code appends.
func appendHubLightLine(path, line string) error {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("append to %s: %w", path, err)
	}
	if _, err := io.WriteString(file, line); err != nil {
		file.Close()
		return fmt.Errorf("append to %s: %w", path, err)
	}
	return file.Close()
}

// The transcript line shapes the seed writes, the ones web/e2e/fixtures/transcript.jsonl
// shows: a user line, then assistant lines with their own request and message ids, the model
// and the usage.

type hubLightUsage struct {
	Input      int64 `json:"input_tokens"`
	Output     int64 `json:"output_tokens"`
	CacheRead  int64 `json:"cache_read_input_tokens"`
	CacheWrite int64 `json:"cache_creation_input_tokens"`
}

type hubLightBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type hubLightMessage struct {
	ID      string          `json:"id"`
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	Content []hubLightBlock `json:"content"`
	Stop    string          `json:"stop_reason"`
	Usage   hubLightUsage   `json:"usage"`
}

type hubLightAssistant struct {
	Timestamp string          `json:"timestamp"`
	RequestID string          `json:"requestId"`
	Type      string          `json:"type"`
	SessionID string          `json:"sessionId"`
	Message   hubLightMessage `json:"message"`
}

type hubLightUserMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type hubLightUser struct {
	Timestamp string              `json:"timestamp"`
	Type      string              `json:"type"`
	SessionID string              `json:"sessionId"`
	Cwd       string              `json:"cwd"`
	Version   string              `json:"version"`
	Branch    string              `json:"gitBranch"`
	Slug      string              `json:"slug"`
	Message   hubLightUserMessage `json:"message"`
}

// hubLightStamp renders one transcript timestamp the way Claude Code writes one.
func hubLightStamp(at time.Time) string {
	return at.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// hubLightAssistantLine renders one assistant transcript line: its own request and message
// ids, the model, and usage the pricing reads.
func hubLightAssistantLine(session string, n int, at time.Time, model string) (string, error) {
	raw, err := json.Marshal(hubLightAssistant{
		Timestamp: hubLightStamp(at),
		RequestID: fmt.Sprintf("req-%s-%04d", session, n),
		Type:      "assistant",
		SessionID: session,
		Message: hubLightMessage{
			ID:      fmt.Sprintf("msg-%s-%04d", session, n),
			Role:    "assistant",
			Model:   model,
			Content: []hubLightBlock{{Type: "text", Text: fmt.Sprintf("seeded answer %d", n)}},
			Stop:    "end_turn",
			Usage: hubLightUsage{
				Input:      int64(100 + n%97),
				Output:     int64(10 + n%37),
				CacheRead:  int64((n % 5) * 512),
				CacheWrite: int64((n % 7) * 256),
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("marshal the assistant line for %s: %w", session, err)
	}
	return string(raw) + "\n", nil
}

// hubLightUserLine renders the user line that opens a session's transcript.
func hubLightUserLine(session string, at time.Time) (string, error) {
	raw, err := json.Marshal(hubLightUser{
		Timestamp: hubLightStamp(at),
		Type:      "user",
		SessionID: session,
		Cwd:       "/tmp/hub-light",
		Version:   "2.1.0",
		Branch:    "main",
		Slug:      session,
		Message:   hubLightUserMessage{Role: "user", Content: "seeded question"},
	})
	if err != nil {
		return "", fmt.Errorf("marshal the user line for %s: %w", session, err)
	}
	return string(raw) + "\n", nil
}
