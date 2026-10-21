package router

// The fail streak's own rows: the raise and recovery rules the fail-alert scenarios and the
// fail-alert e2e stand on, with injected millisecond times.

import "testing"

func TestFailStreak(t *testing.T) {
	t.Run("two failures raise nothing", func(t *testing.T) {
		streak := &FailStreak{}
		if streak.Fail(1_000, "ECERT") {
			t.Error("the first failure raised")
		}
		if streak.Fail(2_000, "ECERT") {
			t.Error("the second failure raised")
		}
	})

	t.Run("three failures within a minute raise one event", func(t *testing.T) {
		streak := &FailStreak{}
		streak.Fail(1_000, "ECERT")
		streak.Fail(2_000, "ECERT")
		if !streak.Fail(3_000, "ECERT") {
			t.Error("the third failure inside the window did not raise")
		}
	})

	t.Run("three failures spread over more than a minute raise nothing", func(t *testing.T) {
		streak := &FailStreak{}
		streak.Fail(1_000, "ECERT")
		streak.Fail(31_000, "ECERT")
		if streak.Fail(62_000, "ECERT") {
			t.Error("failures 61 s apart raised")
		}
	})

	t.Run("a second raise inside five minutes stays quiet", func(t *testing.T) {
		streak := &FailStreak{}
		streak.Fail(1_000, "ECERT")
		streak.Fail(2_000, "ECERT")
		if !streak.Fail(3_000, "ECERT") {
			t.Fatal("the first raise never happened")
		}
		streak.Fail(62_000, "ECERT")
		streak.Fail(63_000, "ECERT")
		if streak.Fail(64_000, "ECERT") {
			t.Error("a raise a minute after the last one raised again")
		}
	})

	t.Run("a raise after five quiet minutes goes out", func(t *testing.T) {
		streak := &FailStreak{}
		streak.Fail(1_000, "ECERT")
		streak.Fail(2_000, "ECERT")
		if !streak.Fail(3_000, "ECERT") {
			t.Fatal("the first raise never happened")
		}
		streak.Fail(302_000, "ECERT")
		streak.Fail(303_000, "ECERT")
		if !streak.Fail(304_000, "ECERT") {
			t.Error("a raise five minutes after the last one stayed quiet")
		}
	})

	t.Run("a success after a raise recovers once and then nothing", func(t *testing.T) {
		streak := &FailStreak{}
		streak.Fail(1_000, "ECERT")
		streak.Fail(2_000, "ECERT")
		streak.Fail(3_000, "ECERT")
		if !streak.Ok(4_000) {
			t.Error("the first success after a raise did not recover")
		}
		if streak.Ok(5_000) {
			t.Error("the second success recovered again")
		}
	})

	t.Run("a success with no raise recovers nothing", func(t *testing.T) {
		streak := &FailStreak{}
		streak.Fail(1_000, "ECERT")
		if streak.Ok(2_000) {
			t.Error("a success without a raise recovered")
		}
	})

	t.Run("a success resets the count", func(t *testing.T) {
		streak := &FailStreak{}
		streak.Fail(1_000, "ECERT")
		streak.Fail(2_000, "ECERT")
		streak.Ok(3_000)
		if streak.Fail(4_000, "ECERT") {
			t.Error("the success had not cleared the count: the next failure raised early")
		}
		streak.Fail(5_000, "ECERT")
		if !streak.Fail(6_000, "ECERT") {
			t.Error("the third failure after the success did not raise")
		}
	})
}
