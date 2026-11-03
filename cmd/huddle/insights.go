package main

// `huddle insights run`: one insights run as its own short process. It reads the ledger,
// history.db and the hub's needs-you history read-only, builds the payload the preview shows,
// asks the lane huddle-router serves once, keeps the run in the huddle home and exits. The hub
// starts it and only reads the run's file for progress.

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
	"github.com/MuhmdRaouf/huddle/internal/insights/lane"
	"github.com/MuhmdRaouf/huddle/internal/insights/report"
	"github.com/MuhmdRaouf/huddle/internal/insights/sources"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
)

const insightsUsage = "usage: huddle insights run [--run-id ID] [--provider NAME] [--model ID]"

// insightsEntry runs the insights word: only `run` exists.
func insightsEntry(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "run" {
		fmt.Fprintln(stderr, insightsUsage)
		return 2
	}
	flags, ok := insightsFlags(args[1:])
	if !ok {
		fmt.Fprintln(stderr, insightsUsage)
		return 2
	}
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	run, err := insightsRun(ctx, flags)
	if err != nil {
		fmt.Fprintf(stderr, "huddle: insights run: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "insights run %s done\n", run.ID)
	return 0
}

type insightsRunFlags struct{ id, provider, model string }

// insightsFlags reads the three value flags; anything else is a usage error.
func insightsFlags(args []string) (insightsRunFlags, bool) {
	var flags insightsRunFlags
	for i := 0; i+1 < len(args); i += 2 {
		switch args[i] {
		case "--run-id":
			flags.id = args[i+1]
		case "--provider":
			flags.provider = args[i+1]
		case "--model":
			flags.model = args[i+1]
		default:
			return flags, false
		}
	}
	return flags, len(args)%2 == 0
}

func insightsRun(ctx context.Context, flags insightsRunFlags) (report.Run, error) {
	huddleHome, err := home.Resolve(os.Getenv)
	if err != nil {
		return report.Run{}, err
	}
	env := envOf(os.Environ())
	laneURL, err := lane.URL(env)
	if err != nil {
		return report.Run{}, err
	}
	asks, closeAsks := openAskHistory(ctx, huddleHome)
	defer closeAsks()
	stores, err := sources.Open(paths.LedgerPath(ctx, env), paths.HistoryPath(ctx, env), asks)
	if err != nil {
		return report.Run{}, err
	}
	defer func() { _ = stores.Close() }()
	if flags.id == "" {
		flags.id = report.NewRunID(time.Now())
	}
	return report.Execute(ctx, report.ExecuteOptions{
		ID:       flags.id,
		Sources:  stores.Sources(),
		Choices:  lane.Choices(env),
		Provider: flags.provider, Model: flags.model,
		Window: report.LastDays(time.Now(), report.WindowDays),
		Lane:   lane.New(laneURL),
		Store:  report.NewRunStore(huddleHome.InsightsRuns()),
		Now:    time.Now,
	})
}

// openAskHistory opens the hub's registry read-only. A home with no hub.db, or one that will not
// open, has no asks to report: the run goes on without them.
func openAskHistory(ctx context.Context, h home.Home) (sources.AskHistory, func()) {
	if _, err := os.Stat(h.HubDB()); err != nil {
		return nil, func() {}
	}
	registry, err := hub.OpenReader(ctx, h.HubDB())
	if err != nil {
		return nil, func() {}
	}
	return registry, func() { _ = registry.Close() }
}
