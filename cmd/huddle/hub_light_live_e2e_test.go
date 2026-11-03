package main

// The hub's light guard at the live world's size: the built binary serves a temp home seeded
// with the request and tool-call counts this machine holds inside the store's 24-hour window,
// spread over more sessions than the store keeps, so the first catch-up drops a stale session
// for nearly every session it meets and sheds the aged records it reads. The run settles, then
// the fixed 30 s polled workload of TestE2EHubStaysLight runs against it. The sessions the
// summary counts after the catch-up are held to the store's cap; the CPU and resident sizes are
// logged beside TestE2EHubStaysLight's.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// This machine's live counts, from structure-only counts of its transcripts.
const (
	// liveWindowRequests and liveWindowTools are the requests and tool calls inside the
	// 24-hour window, liveActiveSessions the sessions that wrote them and liveTranscripts
	// every transcript on disk: the catch-up reads all of them, far past the store's 200
	// sessions.
	liveWindowRequests = 44_561
	liveWindowTools    = 36_159
	liveActiveSessions = 158
	liveTranscripts    = 1_702

	// liveSessionLines is one active session's lines: its share of the window's requests.
	liveSessionLines = liveWindowRequests / liveActiveSessions
	// liveSessionRouteLines is how many of them carry a route spool line, four in five as
	// on the live machine.
	liveSessionRouteLines = liveSessionLines * 4 / 5
	// liveSeedRequests is what the seed's active sessions write and the settle waits for.
	liveSeedRequests = liveActiveSessions * liveSessionLines
	// liveBareEvery is the spacing of the sessions whose transcript has no user line, a
	// twentieth of them: the store sees their records without ever seeing an upsert.
	liveBareEvery = 20
	// liveStaleSessions are the transcripts older than the window: one question and one aged answer each.
	liveStaleSessions = liveTranscripts - liveActiveSessions
)

func TestE2EHubStaysLightAtLiveSize(t *testing.T) {
	switch runtime.GOOS {
	case "darwin", "linux":
	default:
		t.Skipf("no RSS sampler for %s", runtime.GOOS)
	}
	client := &http.Client{}
	template := t.TempDir()
	seedHubLightLiveTemplate(t, template)

	logC, hubURL, transcripts, catchUpPeak := startHubLightRun(t, client, template, liveSeedRequests)
	pid := logC.cmd.Process.Pid
	workloadStart := processCPU(t, pid)
	workload := startHubLightWorkload(t, client, hubURL, transcripts)
	settledRSS := sampleRSS(t, pid, 30*time.Second, 2*time.Second)
	workload.stop()
	workload.reportErrors(t)
	polledCPU := processCPU(t, pid) - workloadStart
	sessions := hubLightSummarySessions(t, client, hubURL)
	wholeLife := stopAndMeasure(t, logC)

	catchUpMB := catchUpPeak / (1024 * 1024)
	settledMB := settledRSS / (1024 * 1024)
	t.Logf("hub light at live size: %d sessions, polled %v cpu, whole life %v, catch-up peak rss %d MB, settled peak rss %d MB",
		sessions, polledCPU, wholeLife, catchUpMB, settledMB)
	if sessions > shared.MaxSessions {
		t.Errorf("the summary counts %d sessions after the catch-up, want at most the %d the store keeps", sessions, shared.MaxSessions)
	}
}

// seedHubLightLiveTemplate writes the home of the live-size run under its user's
// .claude/projects/-tmp-hub-light: liveActiveSessions sessions whose answers fill the last 6
// hours, with their route lines and the window's tool calls in the spool, and liveStaleSessions
// sessions of one question and one answer each from 2 to 11 days ago. The whole seed must land in under
// hubLightSeedBudget.
func seedHubLightLiveTemplate(t *testing.T, root string) {
	t.Helper()
	started := time.Now()
	transcripts := filepath.Join(root, "user", ".claude", "projects", "-tmp-hub-light")
	for _, dir := range []string{transcripts, filepath.Join(root, "user", "data"), filepath.Join(root, "home")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("make %s: %v", dir, err)
		}
	}
	var spool hubLightSpool
	now := time.Now()
	lineStep := time.Duration(int64(6*time.Hour/time.Millisecond)/int64(liveSessionLines)) * time.Millisecond
	for s := 0; s < liveActiveSessions; s++ {
		session := fmt.Sprintf("light-busy-%02d", s)
		first := now.Add(-6 * time.Hour).Add(time.Duration(s) * time.Second)
		writeHubLightTranscript(t, filepath.Join(transcripts, session+".jsonl"), func(w *bufio.Writer) error {
			if s%liveBareEvery != 0 {
				if err := writeHubLightUserLine(w, session, first.Add(-time.Second)); err != nil {
					return err
				}
			}
			for i := 0; i < liveSessionLines; i++ {
				at := first.Add(time.Duration(i) * lineStep)
				model := hubLightModels[(s+i)%len(hubLightModels)]
				line, err := hubLightAssistantLine(session, i, at, model)
				if err != nil {
					return err
				}
				if _, err := w.WriteString(line); err != nil {
					return err
				}
				if i < liveSessionRouteLines {
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
	toolStep := time.Duration(int64(6*time.Hour/time.Millisecond)/int64(liveWindowTools)) * time.Millisecond
	for n := 0; n < liveWindowTools; n++ {
		at := now.Add(-6 * time.Hour).Add(time.Duration(n) * toolStep)
		session := fmt.Sprintf("light-busy-%02d", n%liveActiveSessions)
		id := fmt.Sprintf("toolu-live-%06d", n)
		pre, err := hubLightToolHookLine("PreToolUse", session, id, at)
		if err != nil {
			t.Fatal(err)
		}
		post, err := hubLightToolHookLine("PostToolUse", session, id, at.Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		spool.add(at, pre)
		spool.add(at.Add(time.Second), post)
	}
	spool.write(t, paths.SpoolDir(t.Context(), domain.EnvLookup{
		"RADAR_HOME": filepath.Join(root, "hub-radar"),
	}))
	for s := 0; s < liveStaleSessions; s++ {
		session := fmt.Sprintf("light-stale-%04d", s)
		at := now.Add(-time.Duration(48+s%216) * time.Hour)
		writeHubLightTranscript(t, filepath.Join(transcripts, session+".jsonl"), func(w *bufio.Writer) error {
			if s%liveBareEvery == 0 {
				return writeHubLightAnswer(w, session, at, hubLightModels[s%len(hubLightModels)])
			}
			return writeHubLightTurn(w, session, at, hubLightModels[s%len(hubLightModels)])
		})
	}
	if spent := time.Since(started); spent >= hubLightSeedBudget {
		t.Fatalf("the seed took %s, want under %s", spent.Round(time.Millisecond), hubLightSeedBudget)
	}
	t.Logf("hub light: the live-size template seeded in %s", time.Since(started).Round(time.Millisecond))
}

// writeHubLightUserLine opens a session's transcript with the user line that carries its
// facts, the line that makes the watcher upsert the session.
func writeHubLightUserLine(w *bufio.Writer, session string, at time.Time) error {
	line, err := hubLightUserLine(session, at)
	if err != nil {
		return err
	}
	_, err = w.WriteString(line)
	return err
}

// writeHubLightAnswer writes one assistant line alone.
func writeHubLightAnswer(w *bufio.Writer, session string, at time.Time, model string) error {
	line, err := hubLightAssistantLine(session, 0, at, model)
	if err != nil {
		return err
	}
	_, err = w.WriteString(line)
	return err
}

// hubLightToolHookLine renders one half of a tool call's hook pair: the Pre or Post line the
// store joins by tool use id into one timed call.
func hubLightToolHookLine(event, session, toolUseID string, at time.Time) (string, error) {
	raw, err := json.Marshal(hubLightHook{
		Ts: hubLightStamp(at), Event: event, SessionID: session, ToolName: "Bash", ToolUseID: toolUseID,
	})
	if err != nil {
		return "", fmt.Errorf("marshal the %s line for %s: %w", event, toolUseID, err)
	}
	return string(raw) + "\n", nil
}

// hubLightSummarySessions reads radar's summary through the hub and answers the sessions it counts.
func hubLightSummarySessions(t *testing.T, client *http.Client, hubURL string) int64 {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, hubURL+"/api/radar/api/summary", nil)
	if err != nil {
		t.Fatalf("build the summary read: %v", err)
	}
	req.Header.Set("x-huddle-token", hubLightToken)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("read the summary: %v", err)
	}
	defer resp.Body.Close()
	var answer struct {
		Summary struct {
			Sessions int64 `json:"sessions"`
		} `json:"summary"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&answer); err != nil {
		t.Fatalf("the summary's body: %v", err)
	}
	return answer.Summary.Sessions
}
