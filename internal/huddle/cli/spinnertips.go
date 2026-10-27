package cli

import (
	"context"

	"github.com/MuhmdRaouf/huddle/internal/harness"
	"github.com/MuhmdRaouf/huddle/internal/insights"
)

// EnsureSpinnerTips adds the one spinnerTipsOverride entry to Claude Code's settings.json that
// points the spinner at the owner's spinner-tips file. The entry is added once: a start that
// finds it already there writes nothing.
func EnsureSpinnerTips(ctx context.Context, w *World) error {
	hud := Home(ctx, w)
	userHome := w.getenv("HOME")
	if hud == "" || userHome == "" {
		return nil
	}
	_, err := harness.InstallSpinnerTips(hud, harnessEnv(w), insights.SpinnerTipsFile(userHome))
	return err
}
