package server

// The insights report's routes. The preview is the owner's look at what a Run would send: the
// payload built from the stores' structure, sized and priced, and no model asked. A Run is a
// separate `huddle insights run` process the hub starts and then only reads about: the hub asks
// no model, keeps no timer and does nothing between requests.

import (
	"context"
	"errors"
	"net/http"
	"os/exec"
	"syscall"

	"github.com/MuhmdRaouf/huddle/internal/adapters/process"
	"github.com/MuhmdRaouf/huddle/internal/adapters/safego"
	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/insights/lane"
	"github.com/MuhmdRaouf/huddle/internal/insights/report"
	"github.com/MuhmdRaouf/huddle/internal/insights/sources"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
)

// insightsPreviewRoute answers GET /api/insights/preview for the owner alone. The provider and
// model queries pick among the enabled providers and default to GLM; with none enabled the
// answer is the one-line note and nothing is read.
func (s *server) insightsPreviewRoute(w http.ResponseWriter, r *http.Request, me auth.Who) error {
	if !(me.Root || me.Browser != "") {
		return &channel.HuddleError{Status: 403, Msg: "only the owner previews Insights"}
	}
	choices := s.reportChoices()
	if len(choices) == 0 {
		s.writeJSON(w, 200, report.NoProviderPreview(), nil)
		return nil
	}
	now := s.now()
	stores, err := s.openReportStores(r.Context())
	if err != nil {
		return err
	}
	defer func() { _ = stores.Close() }()
	structure, labels, err := report.Build(r.Context(), stores.Sources(), report.LastDays(now, report.WindowDays))
	if err != nil {
		return err
	}
	query := r.URL.Query()
	preview, err := report.NewPreview(structure, labels, choices, query.Get("provider"), query.Get("model"), now)
	var unknown *report.UnknownChoiceError
	if errors.As(err, &unknown) {
		return &channel.HuddleError{Status: 400, Msg: unknown.Error()}
	}
	if err != nil {
		return err
	}
	s.writeJSON(w, 200, preview, nil)
	return nil
}

// openReportStores opens the stores a report reads, read-only, under the radar home the server
// was started with. The caller closes them.
func (s *server) openReportStores(ctx context.Context) (*sources.Stores, error) {
	env := bodiesEnv(s.o.Environ)
	var asks sources.AskHistory
	if s.registry != nil {
		asks = s.registry
	}
	return sources.Open(paths.LedgerPath(ctx, env), paths.HistoryPath(ctx, env), asks)
}

// reportChoices lists the providers huddle-router has enabled, each with its main and flash
// model. A server that names no environment cannot be told where the router's files live and
// reads none, so it never reads the owner's.
func (s *server) reportChoices() []report.ProviderChoice {
	if len(s.o.Environ) == 0 {
		return nil
	}
	return lane.Choices(bodiesEnv(s.o.Environ))
}

// runsListed is how many runs the runs route lists.
const runsListed = 20

// insightsRunRoute answers POST /api/insights/run for the owner alone: it names the run, writes
// its first record and starts the process that does the work, then answers 202 with the id.
// The provider and model queries pick as the preview does. A model that is not enabled, a Claude
// model or no enabled provider is refused before anything starts.
func (s *server) insightsRunRoute(w http.ResponseWriter, r *http.Request, me auth.Who) error {
	if !(me.Root || me.Browser != "") {
		return &channel.HuddleError{Status: 403, Msg: "only the owner runs Insights"}
	}
	query := r.URL.Query()
	picked, model, err := report.Pick(s.reportChoices(), query.Get("provider"), query.Get("model"))
	var none *report.NoProviderError
	var unknown *report.UnknownChoiceError
	var claude *report.ClaudeLaneError
	switch {
	case errors.As(err, &none):
		return &channel.HuddleError{Status: 409, Msg: none.Error()}
	case errors.As(err, &unknown), errors.As(err, &claude):
		return &channel.HuddleError{Status: 400, Msg: err.Error()}
	case err != nil:
		return err
	}
	run := report.Run{
		ID: report.NewRunID(s.now()), Status: report.RunStarting, Provider: picked.Provider, Model: model, StartedAt: s.now(),
	}
	runs := s.insightsRuns()
	if err := runs.Save(run); err != nil {
		return err
	}
	start := s.startRun
	if start == nil {
		start = s.spawnInsightsRun
	}
	if err := start(run.ID, run.Provider, run.Model); err != nil {
		run.Status, run.Error, run.FinishedAt = report.RunFailed, err.Error(), s.now()
		_ = runs.Save(run)
		return err
	}
	s.writeJSON(w, 202, struct {
		ID string `json:"id"`
	}{run.ID}, nil)
	return nil
}

// insightsRunsRoute answers GET /api/insights/runs for the owner alone: the newest runs with
// their status, step and answer, read from the files the run processes keep.
func (s *server) insightsRunsRoute(w http.ResponseWriter, r *http.Request, me auth.Who) error {
	if !(me.Root || me.Browser != "") {
		return &channel.HuddleError{Status: 403, Msg: "only the owner reads Insights runs"}
	}
	runs, err := s.insightsRuns().List(runsListed)
	if err != nil {
		return err
	}
	if runs == nil {
		runs = []report.Run{}
	}
	s.writeJSON(w, 200, struct {
		Runs []report.Run `json:"runs"`
	}{runs}, nil)
	return nil
}

func (s *server) insightsRuns() *report.RunStore {
	return report.NewRunStore(home.Home{Root: s.o.Home}.InsightsRuns())
}

// spawnInsightsRun starts `huddle insights run` for one run, detached in its own session, with
// the hub's environment and home. A test binary is never started as huddle. The hub reaps the
// process and settles its record.
func (s *server) spawnInsightsRun(id, provider, model string) error {
	if s.exe == "" || process.IsTestBinary(s.exe) {
		return errors.New("this process is not the huddle binary, so it cannot start a run")
	}
	cmd := exec.Command(s.exe, "insights", "run", "--run-id", id, "--provider", provider, "--model", model)
	cmd.Env = append(append([]string{}, s.o.Environ...), "HUDDLE_HOME="+s.o.Home)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	safego.Go(s.warn, func() { s.settleRun(id, cmd.Wait()) })
	return nil
}

// settleRun closes the record of a run whose process has ended: a run still starting or running
// at that point never finished, so it is saved as failed with the exit status. A run that wrote
// its own end is left as it wrote it.
func (s *server) settleRun(id string, waited error) {
	runs := s.insightsRuns()
	run, err := runs.Load(id)
	if err != nil || (run.Status != report.RunStarting && run.Status != report.RunRunning) {
		return
	}
	run.Status, run.Step, run.FinishedAt = report.RunFailed, "", s.now()
	run.Error = "the process ended before the run finished"
	if waited != nil {
		run.Error = waited.Error()
	}
	_ = runs.Save(run)
}
