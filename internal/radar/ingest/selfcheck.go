// The self-check's side of the transcripts: one UTC day's model calls re-summed straight from
// the jsonl files, independent of the store, so Radar can compare its own totals against them.
package ingest

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// DaySums is one day's model calls and their four token counters.
type DaySums struct {
	Requests   int64 `json:"requests"`
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
}

// TranscriptRoots are the projects dirs Radar reads Claude Code transcripts from: the user's
// own and each provider plugin's job claude-home.
func TranscriptRoots(ctx context.Context, env domain.EnvLookup) []string {
	roots := []string{filepath.Join(paths.ClaudeConfigDir(env), "projects")}
	for _, plugin := range JobPlugins {
		roots = append(roots, filepath.Join(JobClaudeHome(ctx, env, plugin), "projects"))
	}
	return roots
}

// SumTranscripts sums the model calls every transcript under the roots wrote on day's UTC
// date. Claude Code writes one assistant line per content block, each repeating the call's
// usage, and a resume or fork copies lines into other files, so each message id counts once.
// The first lines are written while the answer still streams and carry a partial output
// count, so each call counts its last copy: the latest timestamp, then the larger output,
// whatever order the lines appear in. A missing root sums to nothing.
func SumTranscripts(roots []string, day time.Time) (DaySums, error) {
	utc := day.UTC()
	from := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC)
	window := dayWindow{from: from.UnixMilli(), to: from.AddDate(0, 0, 1).UnixMilli()}
	calls := map[string]callCopy{}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
				return nil
			}
			// a file last written before the day began holds none of its lines
			if info, err := entry.Info(); err != nil || info.ModTime().UnixMilli() < window.from {
				return nil
			}
			return readCalls(path, window, calls)
		})
		if err != nil {
			return DaySums{}, err
		}
	}
	var sums DaySums
	for _, call := range calls {
		sums.Requests++
		sums.Input += call.tokens.Input
		sums.Output += call.tokens.Output
		sums.CacheRead += call.tokens.CacheRead
		sums.CacheWrite += call.tokens.CacheWrite
	}
	return sums, nil
}

// dayWindow is one UTC day as epoch ms, from inclusive, to exclusive.
type dayWindow struct{ from, to int64 }

// callCopy is one transcript line's copy of a call: when it was written and the usage it carries.
type callCopy struct {
	ts     int64
	tokens shared.Tokens
}

// laterThan says whether this copy was written after other: the later timestamp, else the larger
// output, since a streaming answer's output count only grows.
func (c callCopy) laterThan(other callCopy) bool {
	if c.ts != other.ts {
		return c.ts > other.ts
	}
	return c.tokens.Output > other.tokens.Output
}

// readCalls keeps, per call id, the last copy of each of the window's calls one transcript holds.
func readCalls(path string, window dayWindow, calls map[string]callCopy) error {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 && bytes.Contains(line, []byte(`"assistant"`)) {
			keepCall(string(line), window, calls)
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// keepCall records one line when it is a model call of the window written after the copy of its
// call already kept.
func keepCall(raw string, window dayWindow, calls map[string]callCopy) {
	entry := parseEntry(raw)
	if entry == nil {
		return
	}
	if kind, _ := stringField(entry, "type"); kind != "assistant" {
		return
	}
	message, ok := objField(entry, "message")
	if !ok {
		return
	}
	model, _ := stringField(message, "model")
	isAPIError, _ := boolField(entry, "isApiErrorMessage")
	if model == "" || model == SyntheticModel || isAPIError {
		return
	}
	ts, ok := tsOf(entry)
	if !ok || ts < window.from || ts >= window.to {
		return
	}
	id := callIDOf(entry, message)
	if id == "" {
		return
	}
	line := callCopy{ts: ts, tokens: UsageOf(message)}
	if kept, ok := calls[id]; ok && !line.laterThan(kept) {
		return
	}
	calls[id] = line
}

// callIDOf is the id one API call is known by across its block lines and copies: the
// message id, else the request id, else the line's uuid; empty when the line names none.
func callIDOf(entry, message rawObject) string {
	if id, ok := stringField(message, "id"); ok && id != "" {
		return id
	}
	if id, ok := stringField(entry, "requestId"); ok && id != "" {
		return id
	}
	id, _ := stringField(entry, "uuid")
	return id
}
