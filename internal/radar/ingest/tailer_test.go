package ingest_test

import (
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/radar/ingest"
)

func TestSplitLines(t *testing.T) {
	t.Run("splits complete lines and keeps the tail as carry", func(t *testing.T) {
		if lines, partial := ingest.SplitLines("", "a\nb\n"); !slices.Equal(lines, []string{"a", "b"}) || partial != "" {
			t.Errorf("SplitLines(\"\", \"a\\nb\\n\") = %q, %q", lines, partial)
		}
		if lines, partial := ingest.SplitLines("", "a\nb"); !slices.Equal(lines, []string{"a"}) || partial != "b" {
			t.Errorf("SplitLines(\"\", \"a\\nb\") = %q, %q", lines, partial)
		}
		if lines, partial := ingest.SplitLines("a", "b\nc\n"); !slices.Equal(lines, []string{"ab", "c"}) || partial != "" {
			t.Errorf("SplitLines(\"a\", \"b\\nc\\n\") = %q, %q", lines, partial)
		}
		if lines, partial := ingest.SplitLines("", ""); len(lines) != 0 || partial != "" {
			t.Errorf("SplitLines(\"\", \"\") = %q, %q", lines, partial)
		}
	})

	t.Run("drops blank lines", func(t *testing.T) {
		if lines, partial := ingest.SplitLines("", "a\n\nb\n \n"); !slices.Equal(lines, []string{"a", "b"}) || partial != "" {
			t.Errorf("SplitLines(\"\", \"a\\n\\nb\\n \\n\") = %q, %q", lines, partial)
		}
	})
}

func TestOpenTail(t *testing.T) {
	t.Run("bounds one read, so even a first catch-up streams instead of loading a file whole", func(t *testing.T) {
		if ingest.TailChunkBytes != 4*1024*1024 {
			t.Errorf("TailChunkBytes = %d, want %d", ingest.TailChunkBytes, 4*1024*1024)
		}
	})

	t.Run("starts at the file's first byte, however big the file is", func(t *testing.T) {
		e := makeEnv(t)
		if got := ingest.OpenTail(filepath.Join(e.home, "nope.jsonl")); got != (ingest.TailState{
			Path: filepath.Join(e.home, "nope.jsonl"), Offset: 0, Partial: "",
		}) {
			t.Errorf("OpenTail(missing) = %+v", got)
		}
		small := writeText(t, filepath.Join(e.home, "small.jsonl"), "a\nb\n")
		if got := ingest.OpenTail(small); got != (ingest.TailState{Path: small}) {
			t.Errorf("OpenTail(small) = %+v", got)
		}
		big := writeText(t, filepath.Join(e.home, "big.jsonl"), strings.Repeat("x", 600)+"\n"+strings.Repeat("y", 300)+"\n")
		if got := ingest.OpenTail(big); got != (ingest.TailState{Path: big}) {
			t.Errorf("OpenTail(big) = %+v", got)
		}
	})
}

func TestTailFile(t *testing.T) {
	t.Run("returns new lines only, then goes quiet", func(t *testing.T) {
		e := makeEnv(t)
		path := writeText(t, filepath.Join(e.home, "t.jsonl"), "one\ntwo\n")
		state := ingest.OpenTail(path)
		if got := ingest.TailFile(&state, ingest.TailChunkBytes); !slices.Equal(got.Lines, []string{"one", "two"}) || got.Restarted {
			t.Errorf("first TailFile = %+v, want [one two] without restart", got)
		}
		if got := ingest.TailFile(&state, ingest.TailChunkBytes); len(got.Lines) != 0 || got.Restarted {
			t.Errorf("second TailFile = %+v, want quiet", got)
		}
	})

	t.Run("holds a partial trailing line until it completes", func(t *testing.T) {
		e := makeEnv(t)
		path := writeText(t, filepath.Join(e.home, "t.jsonl"), "")
		state := ingest.OpenTail(path)
		appendText(t, path, "abc")
		if got := ingest.TailFile(&state, ingest.TailChunkBytes); len(got.Lines) != 0 || got.Restarted {
			t.Errorf("TailFile(partial) = %+v, want no lines", got)
		}
		if state.Partial != "abc" {
			t.Errorf("state.Partial = %q, want abc", state.Partial)
		}
		appendText(t, path, "def\n")
		if got := ingest.TailFile(&state, ingest.TailChunkBytes); !slices.Equal(got.Lines, []string{"abcdef"}) {
			t.Errorf("TailFile(completed) = %+v, want [abcdef]", got)
		}
	})

	t.Run("restarts from the top when the file shrinks (rotation)", func(t *testing.T) {
		e := makeEnv(t)
		path := writeText(t, filepath.Join(e.home, "t.jsonl"), "aaaa\nbbbb\ncccc\n")
		state := ingest.OpenTail(path)
		ingest.TailFile(&state, ingest.TailChunkBytes)
		writeText(t, path, "x\ny\n")
		if got := ingest.TailFile(&state, ingest.TailChunkBytes); !slices.Equal(got.Lines, []string{"x", "y"}) || !got.Restarted {
			t.Errorf("TailFile(after shrink) = %+v, want [x y] restarted", got)
		}
		if state.Partial != "" {
			t.Errorf("state.Partial = %q, want empty", state.Partial)
		}
	})

	t.Run("reports nothing for a file that vanished", func(t *testing.T) {
		e := makeEnv(t)
		state := ingest.TailState{Path: filepath.Join(e.home, "gone.jsonl"), Offset: 40}
		if got := ingest.TailFile(&state, ingest.TailChunkBytes); len(got.Lines) != 0 || got.Restarted {
			t.Errorf("TailFile(vanished) = %+v, want nothing and no restart", got)
		}
	})

	t.Run("reads a file bigger than one chunk from the start, a bounded slice per read, in order", func(t *testing.T) {
		e := makeEnv(t)
		var all []string
		for i := range 50 {
			all = append(all, fmt.Sprintf("line-%02d-%s", i, strings.Repeat("x", 20)))
		}
		path := writeText(t, filepath.Join(e.home, "t.jsonl"), strings.Join(all, "\n")+"\n")
		state := ingest.OpenTail(path)
		var seen []string
		reads := 0
		for {
			result := ingest.TailFile(&state, 128) // far smaller than the file
			reads++
			seen = append(seen, result.Lines...)
			if len(result.Lines) == 0 {
				break
			}
		}
		if !slices.Equal(seen, all) {
			t.Fatal("the whole file was not read in order from its first line")
		}
		if reads <= 8 {
			t.Errorf("reads = %d, want more than 8 (bounded slices, not one whole-file read)", reads)
		}
		if state.Partial != "" {
			t.Errorf("state.Partial = %q, want empty", state.Partial)
		}
	})

	t.Run("carries a line that straddles a chunk boundary whole", func(t *testing.T) {
		e := makeEnv(t)
		path := writeText(t, filepath.Join(e.home, "t.jsonl"), "first\nsecond-continues-past-the-cut\nthird\n")
		state := ingest.OpenTail(path)
		if got := ingest.TailFile(&state, 12); !slices.Equal(got.Lines, []string{"first"}) {
			t.Errorf("first TailFile = %q, want [first]", got.Lines)
		}
		if state.Partial != "second" { // the rest of the line is held back as a partial
			t.Errorf("state.Partial = %q, want second", state.Partial)
		}
		if got := ingest.TailFile(&state, 64); !slices.Equal(got.Lines, []string{"second-continues-past-the-cut", "third"}) {
			t.Errorf("second TailFile = %q", got.Lines)
		}
	})
}

// Without the clone in TailFile this fails: each kept partial line is a substring of its whole
// read chunk, so 64 tails pin 64 chunks of about a MiB each.
func TestTailFileKeepsNoChunkAliveThroughItsPartialLine(t *testing.T) {
	e := makeEnv(t)
	states := make([]ingest.TailState, 0, 64)
	for i := range 64 {
		path := writeText(t, filepath.Join(e.home, fmt.Sprintf("t-%02d.jsonl", i)),
			strings.Repeat("a", 1<<20)+"\n"+`{"partial":`)
		state := ingest.TailState{Path: path}
		ingest.TailFile(&state, ingest.TailChunkBytes)
		states = append(states, state)
	}
	var m runtime.MemStats
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&m)
	if m.HeapInuse >= 32<<20 {
		t.Errorf("HeapInuse = %d bytes (%d MiB), want under 32 MiB: a kept partial line still pins its whole read chunk", m.HeapInuse, m.HeapInuse>>20)
	}
	for i := range states {
		if states[i].Partial != `{"partial":` {
			t.Errorf("states[%d].Partial = %q, want {\"partial\":", i, states[i].Partial)
		}
	}
	runtime.KeepAlive(states)
}
