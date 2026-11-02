package main

// The hub's light guard: the built binary serves temp homes seeded like a busy machine, and
// three runs from identical copies of two seeded homes hold it to its budgets. The polled
// run's home carries a quarter of the live world's shape — 18,500 requests and the spool of
// route and hook lines a busy machine writes; the idle runs' home carries a small seed. Run A
// settles and stops and is the polled phase's baseline; run B adds 20 s of idle, its CPU read
// live at both ends; run C adds the fixed 30 s polled workload, its CPU read live across the
// workload, then two writer windows on the same hub, one with a single stream client and one
// with three, whose CPU the stream's one-encode fan-out keeps within twice a single client's.
// The resident size is sampled through run C's catch-up and its workload.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
)

const (
	// hubIdleCPUBudget is the most CPU the hub may spend over 20 s of quiet: no clients, no
	// new records, nothing for it to do.
	hubIdleCPUBudget = 200 * time.Millisecond
	// hubPolledCPUBudget is the most CPU the hub may spend over the fixed 30 s polled
	// workload: two web clients that hold the Radar stream and send three poll ticks each,
	// while a writer appends 30 request lines and opens 10 new sessions.
	hubPolledCPUBudget = 12500 * time.Millisecond
	// hubCatchUpRSSCeilingMB is the peak RSS the hub may reach while the watcher's first
	// catch-up drains the seeded spool and transcripts.
	hubCatchUpRSSCeilingMB = 165
	// hubSettledRSSCeilingMB is the peak RSS the hub may hold over the 30 s polled workload
	// once catch-up has drained.
	hubSettledRSSCeilingMB = 215

	// hubStreamClientsWide is the stream clients of the fan-out window that is held against
	// the single-client window, and hubStreamFanoutRatio is how many times the single
	// client's CPU the wide window may spend: the sessions message is built and encoded once
	// per window however many clients read it.
	hubStreamClientsWide = 3
	hubStreamFanoutRatio = 2
	// hubStreamWindow is how long each fan-out window's CPU is read, after hubStreamSettle
	// lets the window's clients take their snapshots.
	hubStreamWindow = 15 * time.Second
	hubStreamSettle = 3 * time.Second

	hubLightToken = "hub-light-root-token"
	// hubLightSeedBudget is the most the template's transcripts and spool may take to write.
	hubLightSeedBudget = 5 * time.Second

	// The live world's shape the seed scales down: 74,000 requests over 200 busy sessions
	// of 370 transcript lines each, one spool route line per call for about four requests
	// in five, 6,600 hook event lines, and 160 small sessions from 2 to 3 days ago.
	liveBusySessions  = 200
	liveSmallSessions = 160
	liveBusyLines     = 370
	liveRouteLines    = 58_000
	liveHookLines     = 6_600
	// liveShapeScale is what the live shape above is divided by: a quarter of the live
	// shape keeps the cold catch-up inside CI's 10-minute job.
	liveShapeScale = 4

	busyLines     = 400
	smallSessions = liveSmallSessions / liveShapeScale
	// idleSeedBusy is the busy sessions of the idle runs' seed: 2,000 requests.
	idleSeedBusy = 5

	polledSeedBusy  = liveBusySessions / liveShapeScale
	polledBusyLines = liveBusyLines
	// polledRouteLines is the route lines of one busy session, one per call for about four
	// requests in five.
	polledRouteLines = liveRouteLines / liveBusySessions
	polledHookLines  = liveHookLines / liveShapeScale

	// idleSeedRequests and polledSeedRequests are the request counts the two seeds carry
	// and their settles wait for; the small sessions sit under the store's 200-session cap
	// at this scale, and inside its 24-hour window, so the polled seed's summary counts them
	// beside the busy requests.
	idleSeedRequests   = idleSeedBusy * busyLines
	polledSeedRequests = polledSeedBusy*polledBusyLines + smallSessions

	// agedFeedPairs is the transcript lines the aged feed appends, each with its route spool
	// line, and agedFeedHooks the hook lines beside them: 2,000 pairs and 4,000 spool lines,
	// all dated agedFeedAge back.
	agedFeedPairs = 2_000
	agedFeedHooks = 2_000
	agedFeedAge   = 48 * time.Hour
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
	seedHubLightTemplate(t, idleTemplate, idleSeedBusy, busyLines, false)
	polledTemplate := t.TempDir()
	seedHubLightTemplate(t, polledTemplate, polledSeedBusy, polledBusyLines, true)

	// Run A, the polled phase's baseline: the settle alone, then the hub's whole-life CPU out
	// of its rusage.
	logA, _, _, _ := startHubLightRun(t, client, idleTemplate, idleSeedRequests)
	cpuA := stopAndMeasure(t, logA)

	// Run B, idle: the same settle, then 20 s with no clients and no writes, the hub's CPU
	// read live at both ends.
	logB, _, _, _ := startHubLightRun(t, client, idleTemplate, idleSeedRequests)
	pidB := logB.cmd.Process.Pid
	idleStart := processCPU(t, pidB)
	time.Sleep(20 * time.Second)
	idleCPU := processCPU(t, pidB) - idleStart

	// Run C, polled: the busy seed, whose settle carries the catch-up peak, then the fixed
	// 30 s workload of two clients and a writer, its CPU read live at both ends so the
	// budget weighs the workload alone and not the catch-up that ran before it.
	logC, hubURL, transcripts, catchUpPeak := startHubLightRun(t, client, polledTemplate, polledSeedRequests)
	pidC := logC.cmd.Process.Pid
	workloadStart := processCPU(t, pidC)
	workload := startHubLightWorkload(t, client, hubURL, transcripts)
	settledRSS := sampleRSS(t, pidC, 30*time.Second, 2*time.Second)
	workload.stop()
	workload.reportErrors(t)
	polledCPU := processCPU(t, pidC) - workloadStart
	// the same hub then runs a writer window with one stream client and one with three, the
	// wide window second so the store's growth works against the ratio, not for it
	oneClientCPU := hubLightStreamWindowCPU(t, client, hubURL, transcripts, pidC, 1, 1)
	wideCPU := hubLightStreamWindowCPU(t, client, hubURL, transcripts, pidC, hubStreamClientsWide, 2)
	feedAgedRecords(t, client, hubURL, transcripts)
	agedMB := processRSS(t, pidC) / (1024 * 1024)
	cpuC := stopAndMeasure(t, logC)

	catchUpMB := catchUpPeak / (1024 * 1024)
	settledMB := settledRSS / (1024 * 1024)

	t.Logf("hub light: baseline %v, idle %v cpu, polled %v cpu, run C whole life %v, catch-up peak rss %d MB, settled peak rss %d MB",
		cpuA, idleCPU, polledCPU, cpuC-cpuA, catchUpMB, settledMB)
	t.Logf("hub light: %v of cpu over %v with 1 stream client, %v with %d",
		oneClientCPU, hubStreamWindow, wideCPU, hubStreamClientsWide)
	if idleCPU >= hubIdleCPUBudget {
		t.Errorf("idle CPU %v over 20 s is at or over the %v budget", idleCPU, hubIdleCPUBudget)
	}
	if polledCPU >= hubPolledCPUBudget {
		t.Errorf("polled CPU %v over the 30 s workload is at or over the %v budget", polledCPU, hubPolledCPUBudget)
	}
	if wideCPU >= hubStreamFanoutRatio*oneClientCPU {
		t.Errorf("%d stream clients cost %v over %v, at or over %d times the %v of one client",
			hubStreamClientsWide, wideCPU, hubStreamWindow, hubStreamFanoutRatio, oneClientCPU)
	}
	if catchUpMB >= hubCatchUpRSSCeilingMB {
		t.Errorf("catch-up peak rss %d MB is at or over the %d MB ceiling", catchUpMB, hubCatchUpRSSCeilingMB)
	}
	if settledMB >= hubSettledRSSCeilingMB {
		t.Errorf("settled peak rss %d MB is at or over the %d MB ceiling", settledMB, hubSettledRSSCeilingMB)
	}
	if agedMB >= hubSettledRSSCeilingMB {
		t.Errorf("rss %d MB after the aged feed is at or over the %d MB ceiling", agedMB, hubSettledRSSCeilingMB)
	}
}

// feedAgedRecords appends records dated agedFeedAge back to the settled hub, the way a late
// flush would: agedFeedPairs transcript lines with their route spool lines, and agedFeedHooks
// hook lines, then one line stamped now. The hub's summary counts the fresh line once it has
// read the file in order, and the aged records, older than its window, never reach the count.
func feedAgedRecords(t *testing.T, client *http.Client, hubURL, transcripts string) {
	t.Helper()
	before := stableSummaryRequests(t, client, hubURL)
	now := time.Now()
	at := now.Add(-agedFeedAge)
	session := "light-aged"
	var spool hubLightSpool
	var transcript strings.Builder
	for i := 0; i < agedFeedPairs; i++ {
		when := at.Add(time.Duration(i) * time.Second)
		model := hubLightModels[i%len(hubLightModels)]
		line, err := hubLightAssistantLine(session, i, when, model)
		if err != nil {
			t.Fatal(err)
		}
		transcript.WriteString(line)
		route, err := hubLightRouteLine(session, i, when, model)
		if err != nil {
			t.Fatal(err)
		}
		spool.add(when, route)
	}
	for i := 0; i < agedFeedHooks; i++ {
		// numbered past the seed's hook lines, so no tool id repeats one the seed holds
		line, err := hubLightHookLine(polledHookLines+i, at.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		spool.add(at.Add(time.Duration(i)*time.Second), line)
	}
	fresh, err := hubLightAssistantLine(session, agedFeedPairs, now, hubLightModels[0])
	if err != nil {
		t.Fatal(err)
	}
	transcript.WriteString(fresh)

	spoolDir := paths.SpoolDir(t.Context(), domain.EnvLookup{
		"RADAR_HOME": filepath.Join(transcripts, "..", "..", "..", "..", "hub-radar"),
	})
	appendHubLightSpool(t, spoolDir, &spool, now)
	if err := os.WriteFile(filepath.Join(transcripts, session+".jsonl"), []byte(transcript.String()), 0o644); err != nil {
		t.Fatalf("write the aged transcript: %v", err)
	}
	until(t, 60*time.Second, func() bool {
		return hubLightSummaryRequests(t, client, hubURL) >= before+1
	}, "the hub to count the fresh line that follows the aged feed")
	time.Sleep(5 * time.Second)
	if got := hubLightSummaryRequests(t, client, hubURL); got != before+1 {
		t.Errorf("the summary counts %d requests after the aged feed, want %d: the fresh line only", got, before+1)
	}
}

// stableSummaryRequests reads radar's request count until two reads 3 s apart agree.
func stableSummaryRequests(t *testing.T, client *http.Client, hubURL string) int64 {
	t.Helper()
	last := hubLightSummaryRequests(t, client, hubURL)
	for i := 0; i < 20; i++ {
		time.Sleep(3 * time.Second)
		next := hubLightSummaryRequests(t, client, hubURL)
		if next == last {
			return next
		}
		last = next
	}
	t.Fatalf("the summary's request count kept moving: %d", last)
	return last
}

// appendHubLightSpool appends the spool's lines to today's spool file, where the hooks append
// whatever their own timestamp says.
func appendHubLightSpool(t *testing.T, dir string, spool *hubLightSpool, today time.Time) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("make %s: %v", dir, err)
	}
	slices.SortStableFunc(spool.lines, func(a, b hubLightSpoolLine) int { return a.at.Compare(b.at) })
	var body strings.Builder
	for _, line := range spool.lines {
		body.WriteString(line.text)
	}
	path := filepath.Join(dir, today.UTC().Format("2006-01-02")+".jsonl")
	if err := appendHubLightLine(path, body.String()); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body.String()), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}

// seedHubLightTemplate writes the one home a phase's runs start from, under its user's
// .claude/projects/-tmp-hub-light: busy sessions whose answers fill the last 6 hours at the
// given lines per session, and smallSessions small sessions from 2 to 20 hours ago, one request
// each. With withSpool the busy sessions also feed the run's spool, one route line per seeded
// call for the first polledRouteLines lines of every session plus polledHookLines hook
// events, written into the RADAR_HOME the runs start with. The whole seed must land in under
// hubLightSeedBudget.
func seedHubLightTemplate(t *testing.T, root string, busy, lines int, withSpool bool) {
	t.Helper()
	user := filepath.Join(root, "user")
	transcripts := filepath.Join(user, ".claude", "projects", "-tmp-hub-light")
	var spool hubLightSpool
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
	// the busy lines walk the whole 6-hour window, one session's answers a step apart; each
	// line and its route spool line render from the same instant and model, so the two pair
	lineStep := time.Duration(int64(6*time.Hour/time.Millisecond)/int64(lines)) * time.Millisecond
	for s := 0; s < busy; s++ {
		session := fmt.Sprintf("light-busy-%02d", s)
		first := now.Add(-6 * time.Hour).Add(time.Duration(s) * time.Second)
		writeHubLightTranscript(t, filepath.Join(transcripts, session+".jsonl"), func(w *bufio.Writer) error {
			for i := 0; i < lines; i++ {
				at := first.Add(time.Duration(i) * lineStep)
				model := hubLightModels[(s+i)%len(hubLightModels)]
				line, err := hubLightAssistantLine(session, i, at, model)
				if err != nil {
					return err
				}
				if _, err := w.WriteString(line); err != nil {
					return err
				}
				if withSpool && i < polledRouteLines {
					route, err := hubLightRouteLine(session, i, at, model)
					if err != nil {
						return err
					}
					spool.add(at, route)
				}
			}
			return nil
		})
	}
	if withSpool {
		hookStep := time.Duration(int64(6*time.Hour/time.Millisecond)/polledHookLines) * time.Millisecond
		for i := 0; i < polledHookLines; i++ {
			at := now.Add(-6 * time.Hour).Add(time.Duration(i) * hookStep)
			line, err := hubLightHookLine(i, at)
			if err != nil {
				t.Fatalf("render hook line %d: %v", i, err)
			}
			spool.add(at, line)
		}
		spool.write(t, paths.SpoolDir(t.Context(), domain.EnvLookup{
			"RADAR_HOME": filepath.Join(root, "hub-radar"),
		}))
	}
	for s := 0; s < smallSessions; s++ {
		session := fmt.Sprintf("light-small-%03d", s)
		at := now.Add(-time.Duration(2+s%19) * time.Hour)
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
// word false on the stream's snapshot, then 5 s more. The hub's resident size is sampled all
// through the settle and the largest sample comes back as the catch-up peak. The run's seed
// directory comes back for a phase that writes.
func startHubLightRun(t *testing.T, client *http.Client, template string, requests int64) (*serveLog, string, string, int64) {
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
	pid := log.cmd.Process.Pid
	// catchUpPeak is the largest resident size sampled while the catch-up runs
	var catchUpPeak int64
	noteRSS := func() {
		if rss := processRSS(t, pid); rss > catchUpPeak {
			catchUpPeak = rss
		}
	}
	hubURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	until(t, 30*time.Second, func() bool { return healthy(hubURL) }, "the hub to answer /health")
	settling := time.Now()
	last := int64(-1)
	readAt := time.Now().Add(-time.Minute)
	until(t, 300*time.Second, func() bool {
		// the summary read is the hub's costliest answer while the store changes, and the
		// hub needs its CPU for the catch-up: one read every two seconds is enough to watch
		// the count climb
		if time.Since(readAt) < 2*time.Second {
			return last >= requests
		}
		readAt = time.Now()
		noteRSS()
		last = hubLightSummaryRequests(t, client, hubURL)
		return last >= requests
	}, fmt.Sprintf("radar's summary to count %d requests", requests))
	streamAt := time.Now().Add(-time.Minute)
	until(t, 300*time.Second, func() bool {
		// every stream attempt costs the hub a snapshot, so the attempts stay half a second
		// apart while the catch-up runs
		if time.Since(streamAt) < 500*time.Millisecond {
			return false
		}
		streamAt = time.Now()
		noteRSS()
		return hubLightStreamSettled(t, client, hubURL)
	}, "the watcher to finish its first catch-up")
	t.Logf("hub light: settled %d requests in %s, catch-up peak rss %d MB", last,
		time.Since(settling).Round(time.Second), catchUpPeak/(1024*1024))
	time.Sleep(5 * time.Second)
	return log, hubURL, transcripts, catchUpPeak
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
		w.runWriter(transcripts, 0)
	}()
	t.Cleanup(w.stop)
	return w
}

// hubLightStreamWindowCPU holds `streams` Radar streams open beside the writer, lets the
// clients take their snapshots, and reads the hub's CPU over hubStreamWindow. Each round
// writes lines and sessions no earlier round wrote, so every line is a change.
func hubLightStreamWindowCPU(t *testing.T, client *http.Client, hubURL, transcripts string, pid, streams, round int) time.Duration {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	w := &hubLightWorkload{client: client, done: make(chan struct{}), ctx: ctx, cancel: cancel, errs: make(chan error, 64)}
	defer w.stop()
	for n := 0; n < streams; n++ {
		w.wg.Add(1)
		go func() {
			defer w.wg.Done()
			w.drainStream(hubURL)
		}()
	}
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		w.runWriter(transcripts, round)
	}()
	time.Sleep(hubStreamSettle)
	start := processCPU(t, pid)
	time.Sleep(hubStreamWindow)
	cpu := processCPU(t, pid) - start
	w.stop()
	w.reportErrors(t)
	return cpu
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
// Round r shifts the line numbers and the new sessions' names by r hundred, so a later round
// adds records an earlier one did not.
func (w *hubLightWorkload) runWriter(transcripts string, round int) {
	start := time.Now()
	for second := 1; second <= 30; second++ {
		if !w.sleepUntil(start.Add(time.Duration(second) * time.Second)) {
			return
		}
		session := fmt.Sprintf("light-busy-%02d", (second-1)%polledSeedBusy)
		line, err := hubLightAssistantLine(session, polledBusyLines+round*100+second, time.Now(),
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
			if err := w.writeNewSession(transcripts, round*100+second/3, time.Now()); err != nil {
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

// The spool line shapes the polled seed writes, the ones a busy machine's router and hooks
// append: one route line per model call, and hook events carrying prompt, tool and
// notification payloads.

// hubLightRoute is one route spool line, the fields the router writes per call.
type hubLightRoute struct {
	Ts        string                `json:"ts"`
	Event     string                `json:"event"`
	SessionID string                `json:"session_id"`
	Model     string                `json:"model"`
	Plugin    string                `json:"plugin"`
	Route     string                `json:"route"`
	Upstream  string                `json:"upstream"`
	Status    int                   `json:"status"`
	LatencyMs int64                 `json:"latency_ms"`
	Headers   *hubLightRouteHeaders `json:"headers,omitempty"`
	Usage     hubLightUsage         `json:"usage"`
}

// hubLightRouteHeaders is the response header set a route line may carry: the names the
// router's allow-list keeps. The request id rides on every line, pairing the call with its
// transcript copy however the watcher orders the two sources; four lines in ten carry the
// rate-limit pairs besides, about 420 bytes of header pairs on the lines that hold them.
type hubLightRouteHeaders struct {
	RequestID         string `json:"request-id"`
	ContentType       string `json:"content-type"`
	RequestsLimit     string `json:"anthropic-ratelimit-requests-limit"`
	RequestsRemaining string `json:"anthropic-ratelimit-requests-remaining"`
	RequestsReset     string `json:"anthropic-ratelimit-requests-reset"`
	TokensLimit       string `json:"anthropic-ratelimit-tokens-limit"`
	TokensRemaining   string `json:"anthropic-ratelimit-tokens-remaining"`
	TokensReset       string `json:"anthropic-ratelimit-tokens-reset"`
	RateRemaining     string `json:"x-ratelimit-remaining"`
	RateReset         string `json:"x-ratelimit-reset"`
}

// hubLightRouteLine renders the route spool line of one seeded call: the same session,
// model, instant and transcript request id its transcript line carries, so the two join
// into the one record the way a live call's router line and transcript copy do. Every line
// carries the request id; four lines in ten carry the rest of the allow-listed headers.
func hubLightRouteLine(session string, n int, at time.Time, model string) (string, error) {
	line := hubLightRoute{
		Ts:        hubLightStamp(at),
		Event:     "route",
		SessionID: session,
		Model:     model,
		Plugin:    "zai",
		Route:     "direct",
		Upstream:  "https://api.z.ai/api/anthropic",
		Status:    200,
		LatencyMs: int64(400 + (n*7)%3000),
		Usage: hubLightUsage{
			Input:      int64(100 + n%97),
			Output:     int64(10 + n%37),
			CacheRead:  int64((n % 5) * 512),
			CacheWrite: int64((n % 7) * 256),
		},
		Headers: &hubLightRouteHeaders{
			RequestID: fmt.Sprintf("req-%s-%04d", session, n),
		},
	}
	if n%5 < 2 {
		line.Headers.ContentType = "application/json"
		line.Headers.RequestsLimit = "1000"
		line.Headers.RequestsRemaining = fmt.Sprintf("%03d", 999-n%700)
		line.Headers.RequestsReset = fmt.Sprintf("%d", at.Add(time.Hour).Unix())
		line.Headers.TokensLimit = "12000000"
		line.Headers.TokensRemaining = fmt.Sprintf("%07d", 11_000_000-n%1_000_000)
		line.Headers.TokensReset = fmt.Sprintf("%d", at.Add(time.Hour).Unix())
		line.Headers.RateRemaining = fmt.Sprintf("%03d", 99-n%100)
		line.Headers.RateReset = fmt.Sprintf("%d", at.Unix())
	}
	raw, err := json.Marshal(line)
	if err != nil {
		return "", fmt.Errorf("marshal the route line for %s: %w", session, err)
	}
	return string(raw) + "\n", nil
}

// hubLightToolInput is the tool call a PreToolUse line names.
type hubLightToolInput struct {
	Command     string `json:"command,omitempty"`
	Description string `json:"description,omitempty"`
}

// hubLightHook is one hook event spool line, the fields the hooks write per event kind.
type hubLightHook struct {
	Ts           string             `json:"ts"`
	Event        string             `json:"event"`
	SessionID    string             `json:"session_id,omitempty"`
	AgentID      string             `json:"agent_id,omitempty"`
	ToolName     string             `json:"tool_name,omitempty"`
	ToolUseID    string             `json:"tool_use_id,omitempty"`
	ToolInput    *hubLightToolInput `json:"tool_input,omitempty"`
	ToolResponse string             `json:"tool_response,omitempty"`
	Prompt       string             `json:"prompt,omitempty"`
	Message      string             `json:"message,omitempty"`
}

// hubLightHookLine renders one hook event spool line: the six event kinds a busy machine's
// hooks write, their payloads generated filler text of about 3.3 KB with every hundredth
// line near 30 KB, the sizes the live spool holds. The tool lines pair Pre with Post by
// tool use id, the way the store joins them into one timed call.
func hubLightHookLine(n int, at time.Time) (string, error) {
	session := fmt.Sprintf("light-busy-%02d", n%polledSeedBusy)
	filler := 2_950 + (n%53)*8
	if n%100 == 99 {
		filler = 29_000
	}
	line := hubLightHook{Ts: hubLightStamp(at), SessionID: session}
	switch n % 6 {
	case 0:
		line.Event = "UserPromptSubmit"
		line.Prompt = hubLightFiller(filler)
	case 1:
		line.Event = "PreToolUse"
		line.ToolName = "Bash"
		line.ToolUseID = fmt.Sprintf("toolu-%06d", n)
		line.ToolInput = &hubLightToolInput{
			Command:     "verify " + hubLightFiller(filler/4),
			Description: hubLightFiller(filler),
		}
	case 2:
		line.Event = "PostToolUse"
		line.ToolName = "Bash"
		line.ToolUseID = fmt.Sprintf("toolu-%06d", n-1)
		line.ToolResponse = hubLightFiller(filler)
	case 3:
		line.Event = "Stop"
		line.Message = hubLightFiller(filler)
	case 4:
		line.Event = "SubagentStop"
		line.AgentID = fmt.Sprintf("agent-%04d", n%7)
		line.Message = hubLightFiller(filler)
	case 5:
		line.Event = "Notification"
		line.Message = hubLightFiller(filler)
	}
	raw, err := json.Marshal(line)
	if err != nil {
		return "", fmt.Errorf("marshal hook line %d: %w", n, err)
	}
	return string(raw) + "\n", nil
}

// hubLightFiller builds exactly n bytes of deterministic filler text: the same lorem-style
// words repeated, never any real data.
func hubLightFiller(n int) string {
	const words = "lorem ipsum dolor sit amet consectetur adipiscing elit sed do eiusmod tempor incididunt ut labore et dolore "
	return strings.Repeat(words, n/len(words)+1)[:n]
}

// hubLightSpool collects the seeded spool lines in memory and writes them out dated, the
// way the hooks append them: one file per UTC day, oldest line first.
type hubLightSpool struct {
	lines []hubLightSpoolLine
}

// hubLightSpoolLine is one spool line and the moment it happened.
type hubLightSpoolLine struct {
	at   time.Time
	text string
}

// add collects one line.
func (s *hubLightSpool) add(at time.Time, text string) {
	s.lines = append(s.lines, hubLightSpoolLine{at: at, text: text})
}

// write lays the collected lines into dated spool files under dir.
func (s *hubLightSpool) write(t *testing.T, dir string) {
	t.Helper()
	slices.SortStableFunc(s.lines, func(a, b hubLightSpoolLine) int { return a.at.Compare(b.at) })
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("make %s: %v", dir, err)
	}
	day := ""
	var file *os.File
	var w *bufio.Writer
	closeFile := func() {
		if w == nil {
			return
		}
		if err := w.Flush(); err != nil {
			t.Fatalf("flush the spool file: %v", err)
		}
		if err := file.Close(); err != nil {
			t.Fatalf("close the spool file: %v", err)
		}
		w, file = nil, nil
	}
	defer closeFile()
	for _, line := range s.lines {
		lineDay := line.at.UTC().Format("2006-01-02")
		if lineDay != day {
			closeFile()
			path := filepath.Join(dir, lineDay+".jsonl")
			f, err := os.Create(path)
			if err != nil {
				t.Fatalf("create %s: %v", path, err)
			}
			file, w = f, bufio.NewWriter(f)
			day = lineDay
		}
		if _, err := w.WriteString(line.text); err != nil {
			t.Fatalf("write the spool: %v", err)
		}
	}
}
