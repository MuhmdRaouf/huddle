package server

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// TestALockedWriterTakesLinesFromManyGoroutines writes from eight goroutines at once into one
// plain buffer through the writer Serve hands its parts; under -race an unlocked buffer fails.
func TestALockedWriterTakesLinesFromManyGoroutines(t *testing.T) {
	var buf bytes.Buffer
	w := &lockedWriter{w: &buf}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				fmt.Fprintf(w, "line %d %d\n", g, i)
			}
		}()
	}
	wg.Wait()
	if got := strings.Count(buf.String(), "\n"); got != 800 {
		t.Fatalf("the buffer holds %d lines, want 800", got)
	}
}
