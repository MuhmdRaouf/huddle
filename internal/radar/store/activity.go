// Package store keeps the live session store's pure parts: per-session activity
// sparklines, aggregation over records, and the merge of a transcript call with its
// route line.
package store

import "github.com/MuhmdRaouf/huddle/internal/radar/shared"

// ActivityRequest is one request as a sparkline reads it: when it happened and on what
// model.
type ActivityRequest struct {
	Ts    int64
	Model string
}

// ActivityWindow is the window one session's sparkline covers, decided by the caller (the
// store knows live from ended).
type ActivityWindow struct {
	Live bool
	// Start is the session's first activity; nil when nothing recorded it (the window falls
	// back to 15 minutes).
	Start *int64
	// Last is the session's last activity, ending the window.
	Last int64
	Now  int64
}

// ActivityRow is one counted bucket of a grouped read: the bucket index, the model (nil
// when unnamed), the requests.
type ActivityRow struct {
	Bucket int64
	Model  *string
	C      int64
}

// EmptyActivity is the empty sparkline at a bucket size: zero requests everywhere, no
// model anywhere.
func EmptyActivity(bucketMs int64) shared.Activity {
	return shared.Activity{
		BucketMs: bucketMs,
		Counts:   make([]int64, shared.ActivityBuckets),
		Models:   make([]string, shared.ActivityBuckets),
	}
}

// BucketAt is the bucket a timestamp lands in, clamped so a boundary timestamp never falls
// off either end. Truncating division lands every negative offset in bucket 0, which the
// clamp puts there anyway.
func BucketAt(ts, start, bucketMs int64) int64 {
	at := (ts - start) / bucketMs
	if at < 0 {
		return 0
	}
	if at > shared.ActivityBuckets-1 {
		return shared.ActivityBuckets - 1
	}
	return at
}

// TopModelOf is the most-seen model of one bucket, ties broken alphabetically; "" when the
// bucket holds no named model. A model seen zero times never names a bucket.
func TopModelOf(models map[string]int64) string {
	var best string
	var bestCount int64
	named := false
	for model, count := range models {
		if count > bestCount || (count == bestCount && named && model < best) {
			best = model
			bestCount = count
			named = true
		}
	}
	return best
}

// clampBucket clamps a bucket index a query produced into the sparkline (SQLite divides
// toward zero).
func clampBucket(bucket int64) int64 {
	return BucketAt(bucket, 0, 1)
}

// ActivityFromRows assembles grouped rows into a sparkline: whatever query produced them,
// the assembly is the same.
func ActivityFromRows(rows []ActivityRow, bucketMs int64) shared.Activity {
	activity := EmptyActivity(bucketMs)
	byBucket := make(map[int64]map[string]int64)
	for _, row := range rows {
		at := clampBucket(row.Bucket)
		activity.Counts[at] += row.C
		if row.Model == nil || *row.Model == "" {
			continue
		}
		models, ok := byBucket[at]
		if !ok {
			models = make(map[string]int64)
			byBucket[at] = models
		}
		models[*row.Model] += row.C
	}
	for at := range shared.ActivityBuckets {
		activity.Models[at] = TopModelOf(byBucket[int64(at)])
	}
	return activity
}

// ActivityOf is one session's sparkline: 48 buckets over the window its live state names.
// A live session's window is its last 15 minutes; an ended one stretches from its start
// to its last activity, so a whole run fits the same 48 buckets.
func ActivityOf(requests []ActivityRequest, window ActivityWindow) shared.Activity {
	var start, bucketMs int64
	if window.Live {
		start = window.Now - shared.ActivityLiveMs
		bucketMs = shared.ActivityLiveBucketMs
	} else {
		if window.Start != nil {
			start = *window.Start
		} else {
			start = window.Last - shared.ActivityLiveMs
		}
		bucketMs = ceilDiv(window.Last-start, shared.ActivityBuckets)
		if bucketMs < 1 {
			bucketMs = 1
		}
	}
	rows := make([]ActivityRow, 0, len(requests))
	for _, request := range requests {
		rows = append(rows, ActivityRow{Bucket: BucketAt(request.Ts, start, bucketMs), Model: &request.Model, C: 1})
	}
	return ActivityFromRows(rows, bucketMs)
}

// ceilDiv divides a non-negative span by d rounding up; a negative span rounds to a
// non-positive bucket size, which the caller's floor of 1 ms then replaces.
func ceilDiv(span, d int64) int64 {
	return (span + d - 1) / d
}
