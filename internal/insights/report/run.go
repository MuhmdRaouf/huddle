package report

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// WindowDays is the window a run reports on.
const WindowDays = 7

// Instructions is the system text of a run's request. It fits instructionReserve.
const Instructions = `You read the numbers of one developer's AI coding sessions: tokens, cost, request timing, tool calls, waits for the owner and waste. Sessions and repos appear as labels.

Write a short report in plain words with exactly five sections, in this order:
1. Where the tokens went
2. What blocked you
3. Waste
4. What went well
5. Change since the last run

Give at most three tips in all. Tie each tip to a number from the data and quote the number. If the data holds a "days" list, the window was folded to fit: the totals, models and tools cover the whole window, and each day lists its busiest sessions. If the data holds no earlier run, say so in section 5.`

// Reply is a lane's answer: the model's text and the usage the lane reported.
type Reply struct {
	Text                      string
	InputTokens, OutputTokens int64
}

// Lane asks a model through huddle-router. A run calls it once.
type Lane interface {
	Ask(ctx context.Context, model, system, user string) (Reply, error)
}

// NoProviderError says a run has no enabled provider to ask.
type NoProviderError struct{}

func (*NoProviderError) Error() string { return NoProviderNote }

// ClaudeLaneError says a run was pointed at a Claude model. A Claude lane waits for an API key in
// Settings > Providers; a run never asks one through the login.
type ClaudeLaneError struct{ Model string }

func (e *ClaudeLaneError) Error() string {
	return fmt.Sprintf("model %q is a Claude model; Insights run on an enabled provider", e.Model)
}

// Pick resolves the provider and model a run asks, among the enabled choices (the preview's
// rules), and refuses what a run never asks: nothing enabled, or a Claude model.
func Pick(choices []ProviderChoice, provider, model string) (ProviderChoice, string, error) {
	if len(choices) == 0 {
		return ProviderChoice{}, "", &NoProviderError{}
	}
	picked, model, err := choose(choices, provider, model)
	if err != nil {
		return ProviderChoice{}, "", err
	}
	if strings.HasPrefix(strings.ToLower(model), "claude-") {
		return ProviderChoice{}, "", &ClaudeLaneError{Model: model}
	}
	return picked, model, nil
}

// ExecuteOptions is everything a run needs: what to read, whom to ask, and where to keep the run.
type ExecuteOptions struct {
	ID              string
	Sources         Sources
	Choices         []ProviderChoice
	Provider, Model string // "" takes the preview's default
	Window          Window
	Lane            Lane
	Store           *RunStore
	Now             func() time.Time
}

// Execute builds the payload the preview shows, asks the lane once and keeps the run in the
// store: running while it reads and asks, then done with the answer or failed with the reason.
// It refuses before any call when no provider is enabled or the model is a Claude model.
func Execute(ctx context.Context, o ExecuteOptions) (Run, error) {
	run := Run{ID: o.ID, Status: RunRunning, Step: StepReading, StartedAt: o.Now()}
	if err := o.Store.Save(run); err != nil {
		return run, err
	}
	if err := work(ctx, o, &run); err != nil {
		run.Status, run.Step, run.Error, run.FinishedAt = RunFailed, "", err.Error(), o.Now()
		if saveErr := o.Store.Save(run); saveErr != nil {
			err = errors.Join(err, saveErr)
		}
		return run, err
	}
	run.Status, run.Step, run.FinishedAt = RunDone, "", o.Now()
	return run, o.Store.Save(run)
}

// work is Execute's work: it fills the run in as it goes, and the run store holds its progress.
func work(ctx context.Context, o ExecuteOptions, run *Run) error {
	picked, model, err := Pick(o.Choices, o.Provider, o.Model)
	if err != nil {
		return err
	}
	run.Provider, run.Model = picked.Provider, model
	structure, _, err := Build(ctx, o.Sources, o.Window)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(structure)
	if err != nil {
		return fmt.Errorf("encode the payload: %w", err)
	}
	if tokens := EstimateTokens([]byte(Instructions)) + EstimateTokens(payload); tokens > MaxInputTokens {
		return fmt.Errorf("the request is about %d tokens, over the %d cap", tokens, MaxInputTokens)
	}
	run.Window, run.Step = structure.Window, StepAsking
	if err := o.Store.Save(*run); err != nil {
		return err
	}
	reply, err := o.Lane.Ask(ctx, model, Instructions, string(payload))
	if err != nil {
		return err
	}
	run.Answer, run.InputTokens, run.OutputTokens = reply.Text, reply.InputTokens, reply.OutputTokens
	return nil
}
