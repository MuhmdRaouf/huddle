package spool

import (
	"sync"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// The writer is one closure shared by every request goroutine of huddle-router, so its one-time
// setup holds up when they all call it at once.

func TestSpoolWriterIsSafeUnderConcurrentWrites(t *testing.T) {
	e := testEnv(t)
	noon := pinnedNoon()
	write := CreateSpoolWriter(e, nil, func() int64 { return noon })

	const writes = 64
	var group sync.WaitGroup
	group.Add(writes)
	for range writes {
		go func() {
			defer group.Done()
			write(routerStart("2026-10-07T10:00:00.000Z"))
		}()
	}
	group.Wait()

	lines := fileLines(t, SpoolFile(e, noon))
	if len(lines) != writes {
		t.Fatalf("wrote %d lines, want %d: %q", len(lines), writes, lines)
	}
	for _, line := range lines {
		var event domain.RouterEvent
		decode(t, line, &event)
		if event.Event != domain.EventRouter || event.State != domain.RouterStateStart {
			t.Fatalf("line %q is not the router start the goroutines wrote", line)
		}
	}
}
