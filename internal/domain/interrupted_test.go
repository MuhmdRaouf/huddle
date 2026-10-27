package domain

import (
	"errors"
	"testing"
)

func TestInterruptedLifecycle(t *testing.T) {
	assertNext := func(t *testing.T, from JobState, event JobEvent, want JobState) {
		t.Helper()
		got, err := NextState(from, event)
		if err != nil {
			t.Fatalf("%s --%s-->: unexpected error %v", from, event.Type, err)
		}
		if got != want {
			t.Errorf("%s --%s--> = %s, want %s", from, event.Type, got, want)
		}
	}
	assertIllegal := func(t *testing.T, from JobState, event JobEvent) {
		t.Helper()
		_, err := NextState(from, event)
		if err == nil {
			t.Fatalf("%s --%s--> succeeded, want illegal_transition", from, event.Type)
		}
		var lifecycleErr LifecycleError
		if !errors.As(err, &lifecycleErr) {
			t.Fatalf("%s --%s-->: error %v is not a LifecycleError", from, event.Type, err)
		}
		want := LifecycleError{Kind: IllegalTransition, From: from, Event: event.Type}
		if lifecycleErr != want {
			t.Errorf("%s --%s-->: error %+v, want %+v", from, event.Type, lifecycleErr, want)
		}
	}

	t.Run("queued|running|verifying --interrupted--> interrupted", func(t *testing.T) {
		for _, from := range []JobState{JobStateQueued, JobStateRunning, JobStateVerifying} {
			assertNext(t, from, JobEvent{Type: EventInterrupted}, JobStateInterrupted)
		}
	})

	t.Run("interrupted --returned--> queued: the only way it runs again", func(t *testing.T) {
		assertNext(t, JobStateInterrupted, JobEvent{Type: EventReturned, Feedback: "Run it again."}, JobStateQueued)
		assertIllegal(t, JobStateInterrupted, JobEvent{Type: EventSlotAcquired})
		assertIllegal(t, JobStateInterrupted, JobEvent{Type: EventWorkerExited})
	})

	t.Run("interrupted decides like awaiting_review: accepted and discarded", func(t *testing.T) {
		assertNext(t, JobStateInterrupted, JobEvent{Type: EventAccepted}, JobStateAccepted)
		assertNext(t, JobStateInterrupted, JobEvent{Type: EventDiscarded}, JobStateDiscarded)
	})

	t.Run("interrupted is not running and never was", func(t *testing.T) {
		for _, event := range []JobEvent{
			{Type: EventStopped},
			{Type: EventSlotAcquired},
			{Type: EventWorkerExited},
			{Type: EventVerified},
			{Type: EventRetry, RetryKind: AttemptKindAutoFix},
			{Type: EventRetry, RetryKind: AttemptKindInfraRetry},
			{Type: EventInterrupted},
		} {
			assertIllegal(t, JobStateInterrupted, event)
		}
	})

	t.Run("interrupted is not terminal: return decides it", func(t *testing.T) {
		if IsTerminal(JobStateInterrupted) {
			t.Errorf("IsTerminal(%s) = true, want false", JobStateInterrupted)
		}
	})
}
