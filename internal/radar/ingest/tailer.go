// Byte-offset tailing for jsonl files: read only what appeared since the last poll, buffer a
// partial trailing line until it completes, restart when the file shrinks (rotation,
// truncation), and take even a first sighting in bounded chunks — a multi-gigabyte transcript
// is streamed from its first byte, never loaded whole and never skipped past.
package ingest

import (
	"os"
	"strings"
)

// TailChunkBytes is how many bytes one read may take: a large first catch-up walks a file a
// chunk per poll, yielding between the lines, so the caller keeps answering while the backlog
// drains.
const TailChunkBytes int64 = 4 * 1024 * 1024

// TailState is a jsonl file's read position: the offset of the next unread byte and the
// partial trailing line carried until it completes.
type TailState struct {
	Path    string
	Offset  int64
	Partial string
}

// TailResult is one tail read's take.
type TailResult struct {
	Lines     []string
	Restarted bool
}

// SplitLines is the pure buffer math: carry plus chunk to the complete lines and the new
// carry. A blank line is dropped.
func SplitLines(carry, chunk string) (lines []string, partial string) {
	parts := strings.Split(carry+chunk, "\n")
	partial = parts[len(parts)-1]
	for _, line := range parts[:len(parts)-1] {
		if !isJSBlank(line) {
			lines = append(lines, line)
		}
	}
	return lines, partial
}

// isJSBlank says the line holds nothing but the characters JavaScript's trim removes.
func isJSBlank(line string) bool {
	for _, r := range line {
		if !isJSSpace(r) {
			return false
		}
	}
	return true
}

// OpenTail opens a tail at the file's first byte: a big backlog is streamed chunk by chunk,
// never skipped.
func OpenTail(path string) TailState {
	return TailState{Path: path}
}

// TailFile reads everything new, up to chunk bytes of it; an empty result when the file is
// quiet, gone or unreadable. A file that shrank since the last read (rotation, truncation) is
// restarted from its first byte, which the result names.
func TailFile(state *TailState, chunk int64) TailResult {
	info, err := os.Stat(state.Path)
	if err != nil || info.Size() == state.Offset {
		return TailResult{}
	}
	restarted := false
	if info.Size() < state.Offset {
		// truncated or replaced: start over and take the whole new file
		state.Offset = 0
		state.Partial = ""
		restarted = true
	}
	file, err := os.Open(state.Path)
	if err != nil {
		return TailResult{Restarted: restarted}
	}
	defer file.Close()
	length := min(info.Size()-state.Offset, chunk)
	buffer := make([]byte, length)
	read, _ := file.ReadAt(buffer, state.Offset)
	if read == 0 {
		return TailResult{Restarted: true} // the read failed outright: treat the file as turned over
	}
	state.Offset += int64(read)
	lines, partial := SplitLines(state.Partial, string(buffer[:read]))
	// Each line and the partial are substrings of the whole chunk (up to TailChunkBytes): kept as they are, any one
	// of them held by a tail or a record pins the entire chunk in memory.
	for i := range lines {
		lines[i] = strings.Clone(lines[i])
	}
	state.Partial = strings.Clone(partial)
	return TailResult{Lines: lines, Restarted: restarted}
}
