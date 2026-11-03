package server

// The insights Run starts a separate process and the hub only shows its progress: the route
// names a run, writes its first record and hands the work over, and the runs list reads the
// files the process keeps in the home.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/insights/report"
)

// started is one process start the route asked for.
type started struct{ id, provider, model string }

// recordStarts makes the fixture's server record each start instead of spawning a process.
func (f *previewFixture) recordStarts(failWith error) *[]started {
	var starts []started
	f.srv.startRun = func(id, provider, model string) error {
		starts = append(starts, started{id, provider, model})
		return failWith
	}
	return &starts
}

func (f *previewFixture) runs() *report.RunStore {
	return report.NewRunStore(home.Home{Root: f.srv.o.Home}.InsightsRuns())
}

func (f *previewFixture) postRun(me auth.Who, query string) (int, map[string]string) {
	f.t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/insights/run?"+query, nil)
	err := f.srv.insightsRunRoute(rec, req, me)
	var herr *channel.HuddleError
	if errors.As(err, &herr) {
		return herr.Status, map[string]string{"error": herr.Msg}
	}
	if err != nil {
		return 500, map[string]string{"error": err.Error()}
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		f.t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return rec.Code, out
}

func TestInsightsRunRouteStartsOneProcessAndWritesTheFirstRecord(t *testing.T) {
	fix := newPreviewFixture(t, "zai", "kimi")
	starts := fix.recordStarts(nil)

	status, answer := fix.postRun(previewOwner, "provider=kimi&model=kimi-k2.6")
	if status != 202 || answer["id"] == "" {
		t.Fatalf("status %d, answer %v; want 202 and a run id", status, answer)
	}
	if len(*starts) != 1 || (*starts)[0] != (started{answer["id"], "kimi", "kimi-k2.6"}) {
		t.Fatalf("starts = %+v, want one start of kimi/kimi-k2.6 for run %s", *starts, answer["id"])
	}
	runs := fix.runs()
	run, err := runs.Load(answer["id"])
	if err != nil || run.Status != report.RunStarting || run.Provider != "kimi" || run.Model != "kimi-k2.6" {
		t.Fatalf("first record = %+v (%v), want a starting run on kimi/kimi-k2.6", run, err)
	}
	info, err := os.Stat(filepath.Join(home.Home{Root: fix.srv.o.Home}.InsightsRuns(), answer["id"]+".json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the record is %v (%v), want mode 0600", info, err)
	}
	if n := fix.lane.Load(); n != 0 {
		t.Errorf("the hub called the lane %d times, want 0: the process asks it", n)
	}
}

func TestInsightsRunRouteDefaultsToGLM(t *testing.T) {
	fix := newPreviewFixture(t, "kimi", "zai")
	starts := fix.recordStarts(nil)
	if status, _ := fix.postRun(previewOwner, ""); status != 202 || len(*starts) != 1 || (*starts)[0].provider != "zai" {
		t.Fatalf("status %d, starts %+v; want 202 and a start on zai", status, *starts)
	}
}

func TestInsightsRunRouteRefusesWithoutStarting(t *testing.T) {
	cases := []struct {
		name    string
		enabled []string
		me      auth.Who
		query   string
		want    int
	}{
		{"a member's token", []string{"zai"}, auth.Who{Name: "worker-1", Kind: auth.KindMember}, "", 403},
		{"no enabled provider", nil, previewOwner, "", 409},
		{"a provider that is not enabled", []string{"zai"}, previewOwner, "provider=deepseek", 400},
		{"a model that is not enabled", []string{"zai"}, previewOwner, "provider=zai&model=claude-opus-5.5", 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fix := newPreviewFixture(t, c.enabled...)
			starts := fix.recordStarts(nil)
			if status, _ := fix.postRun(c.me, c.query); status != c.want {
				t.Errorf("status = %d, want %d", status, c.want)
			}
			runs := fix.runs()
			if listed, _ := runs.List(10); len(*starts) != 0 || len(listed) != 0 {
				t.Errorf("starts %+v, runs %+v; want none of either", *starts, listed)
			}
		})
	}
}

func TestInsightsRunRouteMarksTheRunFailedWhenTheProcessWillNotStart(t *testing.T) {
	fix := newPreviewFixture(t, "zai")
	fix.recordStarts(errors.New("exec: no such file"))

	status, answer := fix.postRun(previewOwner, "")
	if status != 500 {
		t.Fatalf("status = %d (%v), want 500", status, answer)
	}
	runs := fix.runs()
	listed, err := runs.List(10)
	if err != nil || len(listed) != 1 || listed[0].Status != report.RunFailed || listed[0].Error == "" {
		t.Fatalf("runs = %+v (%v), want one failed run with its reason", listed, err)
	}
}

func TestInsightsRunsRouteListsTheRunsNewestFirstForTheOwner(t *testing.T) {
	fix := newPreviewFixture(t, "zai")
	runs := fix.runs()
	t0 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for _, r := range []report.Run{
		{ID: "20261101T120000Z-aaaa", Status: report.RunDone, Answer: "done text", StartedAt: t0},
		{ID: "20261102T120000Z-bbbb", Status: report.RunRunning, Step: report.StepAsking, StartedAt: t0.Add(24 * time.Hour)},
	} {
		if err := runs.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	if err := fix.srv.insightsRunsRoute(rec, httptest.NewRequest(http.MethodGet, "/api/insights/runs", nil), previewOwner); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Runs []report.Run `json:"runs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if len(out.Runs) != 2 || out.Runs[0].ID != "20261102T120000Z-bbbb" || out.Runs[0].Step != report.StepAsking || out.Runs[1].Answer != "done text" {
		t.Errorf("runs = %+v, want the running run first with its step, then the done run with its answer", out.Runs)
	}
	err := fix.srv.insightsRunsRoute(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/insights/runs", nil), auth.Who{Name: "worker-1", Kind: auth.KindMember})
	var herr *channel.HuddleError
	if !errors.As(err, &herr) || herr.Status != 403 {
		t.Errorf("a member's list = %v, want a 403", err)
	}
}

func TestSpawnInsightsRunStartsTheBinaryWithTheRunsWords(t *testing.T) {
	dir := t.TempDir()
	// the process is detached, so the test reads what it wrote through a pipe: the read returns
	// when the process has written and closed it
	record := filepath.Join(dir, "argv")
	if err := syscall.Mkfifo(record, 0o600); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "huddle")
	script := "#!/bin/sh\n{ printf '%s|' \"$@\"; printf '%s' \"$HUDDLE_HOME\"; } > " + record + "\n"
	if err := os.WriteFile(exe, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	srv := &server{exe: exe, o: Options{Home: "/the/home", Environ: []string{"PATH=" + os.Getenv("PATH")}}}

	if err := srv.spawnInsightsRun("run-1", "zai", "glm-5.3"); err != nil {
		t.Fatalf("spawnInsightsRun: %v", err)
	}
	got, err := os.ReadFile(record)
	if want := "insights|run|--run-id|run-1|--provider|zai|--model|glm-5.3|/the/home"; err != nil || string(got) != want {
		t.Fatalf("the process recorded %q (%v), want %q", got, err, want)
	}
}

func TestSpawnInsightsRunNeverStartsATestBinary(t *testing.T) {
	for _, exe := range []string{"", "/tmp/server.test"} {
		srv := &server{exe: exe}
		if err := srv.spawnInsightsRun("run-1", "zai", "glm-5.3"); err == nil {
			t.Errorf("exe %q started, want a refusal", exe)
		}
	}
}

// exitedWith runs a real child that ends with the given shell exit code and answers its wait error.
func exitedWith(t *testing.T, code string) error {
	t.Helper()
	return exec.Command("sh", "-c", "exit "+code).Run()
}

func TestSettleRunFailsARunWhoseProcessEndedWithoutFinishing(t *testing.T) {
	for _, status := range []report.RunStatus{report.RunStarting, report.RunRunning} {
		t.Run(string(status), func(t *testing.T) {
			fix := newPreviewFixture(t, "zai")
			runs := fix.runs()
			if err := runs.Save(report.Run{ID: "20261101T120000Z-aaaa", Status: status, Step: report.StepAsking}); err != nil {
				t.Fatal(err)
			}
			fix.srv.settleRun("20261101T120000Z-aaaa", exitedWith(t, "3"))
			got, err := runs.Load("20261101T120000Z-aaaa")
			if err != nil || got.Status != report.RunFailed || got.Step != "" || got.Error != "exit status 3" || got.FinishedAt.IsZero() {
				t.Errorf("run = %+v (%v), want failed with the exit status, no step and a finish time", got, err)
			}
		})
	}
}

func TestSettleRunLeavesAFinishedRunAlone(t *testing.T) {
	fix := newPreviewFixture(t, "zai")
	runs := fix.runs()
	done := report.Run{ID: "20261101T120000Z-bbbb", Status: report.RunDone, Answer: "text"}
	if err := runs.Save(done); err != nil {
		t.Fatal(err)
	}
	fix.srv.settleRun(done.ID, exitedWith(t, "3"))
	if got, _ := runs.Load(done.ID); got != done {
		t.Errorf("run = %+v, want the done run untouched", got)
	}
}

func TestSettleRunNamesACleanExitThatNeverFinished(t *testing.T) {
	fix := newPreviewFixture(t, "zai")
	runs := fix.runs()
	if err := runs.Save(report.Run{ID: "20261101T120000Z-cccc", Status: report.RunRunning}); err != nil {
		t.Fatal(err)
	}
	fix.srv.settleRun("20261101T120000Z-cccc", nil)
	if got, _ := runs.Load("20261101T120000Z-cccc"); got.Status != report.RunFailed || got.Error != "the process ended before the run finished" {
		t.Errorf("run = %+v, want failed with the one-line reason", got)
	}
}
