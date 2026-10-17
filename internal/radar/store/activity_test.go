package store

import (
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

const now int64 = 1_800_000_000_000

func TestActivityOf(t *testing.T) {
	t.Run("spans a live session's last 15 minutes in 18,750 ms buckets ending now", func(t *testing.T) {
		start := now - shared.ActivityLiveMs
		activity := ActivityOf(
			[]ActivityRequest{
				{Ts: start + 10, Model: "glm-5.3"},
				{Ts: now - 100, Model: "glm-5.3"},
				{Ts: now - 100, Model: "claude-opus-5-5"},
				{Ts: start - 5_000, Model: "glm-5.3"}, // older than the window clamps into the first bucket
			},
			ActivityWindow{Live: true, Start: i64(start - 5_000), Last: now - 100, Now: now},
		)
		if activity.BucketMs != shared.ActivityLiveBucketMs {
			t.Errorf("bucketMs: got %d, want %d", activity.BucketMs, shared.ActivityLiveBucketMs)
		}
		if activity.Counts[0] != 2 {
			t.Errorf("counts[0]: got %d, want 2", activity.Counts[0])
		}
		if activity.Counts[47] != 2 {
			t.Errorf("counts[47]: got %d, want 2", activity.Counts[47])
		}
		if activity.Models[0] != "glm-5.3" {
			t.Errorf("models[0]: got %q, want %q", activity.Models[0], "glm-5.3")
		}
		if activity.Models[47] != "claude-opus-5-5" { // a tie reads alphabetically
			t.Errorf("models[47]: got %q, want %q", activity.Models[47], "claude-opus-5-5")
		}
		var total int64
		for _, count := range activity.Counts {
			total += count
		}
		if total != 4 {
			t.Errorf("counts total: got %d, want 4", total)
		}
	})

	t.Run("stretches an ended session from its start to its last activity over the same 48 buckets", func(t *testing.T) {
		start := now - 4_800_000 // an 80-minute run: 100,000 ms per bucket
		activity := ActivityOf(
			[]ActivityRequest{
				{Ts: start + 50_000, Model: "glm-5.3"},
				{Ts: start + 150_000, Model: "claude-sonnet-5-5"},
				{Ts: start + 199_999, Model: "glm-5.3"},
				{Ts: now, Model: "claude-sonnet-5-5"}, // the last activity clamps into bucket 47
			},
			ActivityWindow{Live: false, Start: i64(start), Last: now, Now: now},
		)
		if activity.BucketMs != 100_000 {
			t.Errorf("bucketMs: got %d, want 100000", activity.BucketMs)
		}
		if activity.Counts[0] != 1 {
			t.Errorf("counts[0]: got %d, want 1", activity.Counts[0])
		}
		if activity.Counts[1] != 2 {
			t.Errorf("counts[1]: got %d, want 2", activity.Counts[1])
		}
		if activity.Models[1] != "claude-sonnet-5-5" { // the most-seen model, ties alphabetical
			t.Errorf("models[1]: got %q, want %q", activity.Models[1], "claude-sonnet-5-5")
		}
		if activity.Counts[47] != 1 {
			t.Errorf("counts[47]: got %d, want 1", activity.Counts[47])
		}
		if len(activity.Counts) != shared.ActivityBuckets {
			t.Errorf("counts length: got %d, want %d", len(activity.Counts), shared.ActivityBuckets)
		}
	})

	t.Run("falls back to the live window when an ended session never recorded its start", func(t *testing.T) {
		activity := ActivityOf(
			[]ActivityRequest{{Ts: now, Model: "glm-5.3"}},
			ActivityWindow{Live: false, Start: nil, Last: now, Now: now},
		)
		if activity.BucketMs != shared.ActivityLiveBucketMs {
			t.Errorf("bucketMs: got %d, want %d", activity.BucketMs, shared.ActivityLiveBucketMs)
		}
		if activity.Counts[47] != 1 {
			t.Errorf("counts[47]: got %d, want 1", activity.Counts[47])
		}
	})

	t.Run("keeps a span-less session in one bucket and leaves every bucket nameless when empty", func(t *testing.T) {
		single := ActivityOf(
			[]ActivityRequest{{Ts: now, Model: "glm-5.3"}},
			ActivityWindow{Live: false, Start: i64(now), Last: now, Now: now},
		)
		if single.BucketMs != 1 {
			t.Errorf("bucketMs: got %d, want 1", single.BucketMs)
		}
		if single.Counts[0] != 1 {
			t.Errorf("counts[0]: got %d, want 1", single.Counts[0])
		}
		if single.Models[0] != "glm-5.3" {
			t.Errorf("models[0]: got %q, want %q", single.Models[0], "glm-5.3")
		}
		none := ActivityOf(nil, ActivityWindow{Live: false, Start: i64(now), Last: now, Now: now})
		for at, count := range none.Counts {
			if count != 0 {
				t.Fatalf("counts[%d]: got %d, want 0", at, count)
			}
		}
		for at, model := range none.Models {
			if model != "" {
				t.Fatalf("models[%d]: got %q, want %q", at, model, "")
			}
		}
	})
}

func TestActivityFromRows(t *testing.T) {
	t.Run("assembles grouped rows, clamping stray buckets and skipping unnamed models", func(t *testing.T) {
		activity := ActivityFromRows(
			[]ActivityRow{
				{Bucket: -3, Model: str("glm-5.3"), C: 2},
				{Bucket: 99, Model: nil, C: 1},
				{Bucket: 5, Model: str("a"), C: 1},
				{Bucket: 5, Model: str("b"), C: 2},
				{Bucket: 5, Model: str(""), C: 1},
			},
			1_000,
		)
		if activity.BucketMs != 1_000 {
			t.Errorf("bucketMs: got %d, want 1000", activity.BucketMs)
		}
		if activity.Counts[0] != 2 {
			t.Errorf("counts[0]: got %d, want 2", activity.Counts[0])
		}
		if activity.Counts[5] != 4 {
			t.Errorf("counts[5]: got %d, want 4", activity.Counts[5])
		}
		if activity.Counts[47] != 1 {
			t.Errorf("counts[47]: got %d, want 1", activity.Counts[47])
		}
		if activity.Models[5] != "b" {
			t.Errorf("models[5]: got %q, want %q", activity.Models[5], "b")
		}
		if activity.Models[0] != "glm-5.3" {
			t.Errorf("models[0]: got %q, want %q", activity.Models[0], "glm-5.3")
		}
		if activity.Models[47] != "" { // a bucket only null models name stays nameless
			t.Errorf("models[47]: got %q, want %q", activity.Models[47], "")
		}
	})
}

func TestEmptyActivity(t *testing.T) {
	t.Run("is 48 silent buckets at the bucket size given", func(t *testing.T) {
		empty := EmptyActivity(18_750)
		if empty.BucketMs != 18_750 {
			t.Errorf("bucketMs: got %d, want 18750", empty.BucketMs)
		}
		if len(empty.Counts) != shared.ActivityBuckets {
			t.Fatalf("counts length: got %d, want %d", len(empty.Counts), shared.ActivityBuckets)
		}
		for at, count := range empty.Counts {
			if count != 0 {
				t.Fatalf("counts[%d]: got %d, want 0", at, count)
			}
		}
		if len(empty.Models) != shared.ActivityBuckets {
			t.Fatalf("models length: got %d, want %d", len(empty.Models), shared.ActivityBuckets)
		}
		for at, model := range empty.Models {
			if model != "" {
				t.Fatalf("models[%d]: got %q, want %q", at, model, "")
			}
		}
	})
}
