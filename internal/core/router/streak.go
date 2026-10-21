// The Anthropic path's failure streak: three `claude-*` upstream failures within a minute mean
// the path is down, and the first success after a raise means it is back. A few integers under
// the streak's own mutex, so counting never slows a request.
package router

import "sync"

// failWindowMs is how close together three failures mean a failing path.
const failWindowMs = 60_000

// raiseQuietMs is how long one raise keeps the next one quiet.
const raiseQuietMs = 5 * 60_000

// FailStreak counts one upstream path's failures and successes. The zero value counts from
// nothing.
type FailStreak struct {
	mu       sync.Mutex
	count    int
	firstAt  int64
	raised   bool
	raisedAt int64
}

// Fail counts one failure at now. It returns true when this failure is the third within a
// minute of its run and the last raise is five minutes old or more. code is the failure's
// own name; the caller puts it on the event, the streak does not read it.
func (s *FailStreak) Fail(now int64, code string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if now-s.firstAt > failWindowMs {
		s.count, s.firstAt = 1, now
	} else {
		s.count++
	}
	if s.count != 3 {
		return false
	}
	if s.raised && now-s.raisedAt < raiseQuietMs {
		return false
	}
	s.raised, s.raisedAt = true, now
	return true
}

// Ok counts one success at now and resets the count. It returns true only for the first
// success after a raise.
func (s *FailStreak) Ok(now int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.count = 0
	if !s.raised {
		return false
	}
	s.raised = false
	return true
}
