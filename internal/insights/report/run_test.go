package report

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeLane records what it is asked and, on the way, what the run store says about progress.
type fakeLane struct {
	t      *testing.T
	store  *RunStore
	id     string
	reply  Reply
	err    error
	calls  int
	model  string
	system string
	user   string
	during Run
}

func (l *fakeLane) Ask(_ context.Context, model, system, user string) (Reply, error) {
	l.calls++
	l.model, l.system, l.user = model, system, user
	if l.store != nil {
		run, err := l.store.Load(l.id)
		if err != nil {
			l.t.Fatalf("load the run while it asks: %v", err)
		}
		l.during = run
	}
	return l.reply, l.err
}

var zaiChoices = []ProviderChoice{{Provider: "zai", Name: "GLM", Models: []string{"glm-5.3", "glm-5.3-flash"}}}

func executeOptions(t *testing.T, lane *fakeLane) ExecuteOptions {
	t.Helper()
	store := NewRunStore(t.TempDir())
	lane.t, lane.store, lane.id = t, store, "20261101T120000Z-test"
	return ExecuteOptions{
		ID: lane.id, Sources: fixture().sources(), Choices: zaiChoices, Window: LastDays(t0, WindowDays),
		Lane: lane, Store: store, Now: func() time.Time { return t0 },
	}
}

func TestExecuteAsksTheLaneOnceWithThePayloadThePreviewSizes(t *testing.T) {
	lane := &fakeLane{reply: Reply{Text: "1. Where the tokens went", InputTokens: 321, OutputTokens: 45}}
	o := executeOptions(t, lane)

	run, err := Execute(context.Background(), o)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if lane.calls != 1 {
		t.Fatalf("the lane was asked %d times, want once", lane.calls)
	}
	structure, _ := build(t, fixture(), o.Window)
	payload, err := json.Marshal(structure)
	if err != nil {
		t.Fatal(err)
	}
	if lane.user != string(payload) {
		t.Errorf("the lane got a different payload than Build marshals:\n got  %.200s\n want %.200s", lane.user, payload)
	}
	if lane.model != "glm-5.3" || strings.Contains(lane.user+lane.system, sentinel) {
		t.Errorf("model %q, sentinel in the request: %v", lane.model, strings.Contains(lane.user+lane.system, sentinel))
	}
	if got := EstimateTokens([]byte(lane.system + lane.user)); got > MaxInputTokens {
		t.Errorf("the request is about %d tokens, want at most %d", got, MaxInputTokens)
	}
	if lane.during.Status != RunRunning || lane.during.Step != StepAsking {
		t.Errorf("while asking the run says %s/%s, want %s/%s", lane.during.Status, lane.during.Step, RunRunning, StepAsking)
	}
	want := Run{
		ID: o.ID, Status: RunDone, Provider: "zai", Model: "glm-5.3", Window: structure.Window, StartedAt: t0, FinishedAt: t0,
		InputTokens: 321, OutputTokens: 45, Answer: "1. Where the tokens went",
	}
	stored, err := o.Store.Load(o.ID)
	if err != nil || run != want || stored != want {
		t.Errorf("run = %+v, stored = %+v (%v), want %+v", run, stored, err, want)
	}
}

func TestExecuteRefusesBeforeAnyCall(t *testing.T) {
	cases := []struct {
		name    string
		choices []ProviderChoice
		model   string
		as      any
	}{
		{"no provider is enabled", nil, "", new(*NoProviderError)},
		{"a claude model is chosen", []ProviderChoice{{Provider: "zai", Models: []string{"claude-opus-5.5"}}}, "", new(*ClaudeLaneError)},
		{"a model that is not enabled is chosen", zaiChoices, "gpt-5", new(*UnknownChoiceError)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lane := &fakeLane{}
			o := executeOptions(t, lane)
			o.Choices, o.Model = c.choices, c.model
			run, err := Execute(context.Background(), o)
			if err == nil || !errors.As(err, c.as) {
				t.Fatalf("Execute = %v, want %T", err, c.as)
			}
			if lane.calls != 0 {
				t.Errorf("the lane was asked %d times, want none", lane.calls)
			}
			stored, loadErr := o.Store.Load(o.ID)
			if loadErr != nil || stored.Status != RunFailed || stored.Error != err.Error() || run.Status != RunFailed {
				t.Errorf("stored = %+v (%v), want a failed run saying %q", stored, loadErr, err)
			}
		})
	}
}

func TestExecuteSavesAFailedRunWhenTheLaneFails(t *testing.T) {
	lane := &fakeLane{err: errors.New("huddle-router answered 502")}
	o := executeOptions(t, lane)
	_, err := Execute(context.Background(), o)
	if !errors.Is(err, lane.err) {
		t.Fatalf("Execute = %v, want the lane's error", err)
	}
	stored, loadErr := o.Store.Load(o.ID)
	if loadErr != nil || stored.Status != RunFailed || !strings.Contains(stored.Error, "502") || stored.Answer != "" {
		t.Errorf("stored = %+v (%v), want a failed run with the lane's error and no answer", stored, loadErr)
	}
}

func TestInstructionsFitTheirReserve(t *testing.T) {
	if got := EstimateTokens([]byte(Instructions)); got > instructionReserve {
		t.Errorf("the instructions are about %d tokens, want at most the %d reserved", got, instructionReserve)
	}
}
