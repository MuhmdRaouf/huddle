// The Codex collector: Codex CLI appends every turn of a session to a rollout file,
// $CODEX_HOME/sessions/YYYY/MM/DD/rollout-*.jsonl (default ~/.codex/sessions), one JSON
// object per line: {"timestamp":"…","type":"<tag>","payload":{…}} (openai/codex,
// codex-rs/history/src/rollout_payload.rs — tags snake_case, item flattened onto the line).
// A turn's counters sit in the event_msg payload of type token_count, under
// info.last_token_usage {input_tokens, cached_input_tokens, output_tokens,
// reasoning_output_tokens, …}; info.total_token_usage is cumulative and never read here.
// input_tokens includes the cached ones, so the uncached input is input - cached. The
// session's id and cwd come from session_meta, the model and cwd from the latest
// turn_context. Codex re-emits an unchanged count, so a delta identical to the previous
// one is skipped.
package ingest

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// CodexCollector reads Codex CLI's rollout files into request records tagged with the
// codex harness. Tails keep each file's read position, so a poll reads only what appeared
// since the last one; a record's id counts the file's kept deltas from its first byte, so
// a restart or a dropped tail re-reads into the same ids and never double-counts a turn.
type CodexCollector struct {
	env   domain.EnvLookup
	tails map[string]*codexTail
}

// NewCodexCollector builds the collector over the home the env names.
func NewCodexCollector(env domain.EnvLookup) *CodexCollector {
	return &CodexCollector{env: env, tails: make(map[string]*codexTail)}
}

// codexTail is one rollout file's read position and parse state, keyed by its real path so
// a symlinked sessions tree is read once.
type codexTail struct {
	tail  TailState
	parse codexParse
}

// Collect reads every rollout file the since window still names, answering the new turns'
// records. A missing sessions tree reads as a machine without Codex: no records, no error.
func (c *CodexCollector) Collect(ctx context.Context, sinceCutoff int64) ([]shared.RequestRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root := filepath.Join(paths.CodexHome(c.env), "sessions")
	if _, err := os.Stat(root); err != nil {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(c.tails))
	var records []shared.RequestRecord
	for _, path := range listRollouts(root, sinceCutoff) {
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			real = path
		}
		seen[real] = struct{}{}
		tail, ok := c.tails[real]
		if !ok {
			tail = &codexTail{tail: OpenTail(path)}
			tail.parse.fileKey = codexFileKey(path)
			c.tails[real] = tail
		}
		result := TailFile(&tail.tail, TailChunkBytes)
		if result.Restarted {
			// the file shrank or turned over: re-read from the first byte, so the ids the
			// re-read emits are the ones already held
			tail.parse = codexParse{fileKey: tail.parse.fileKey}
		}
		for _, line := range result.Lines {
			records = append(records, tail.parse.feed(line)...)
		}
	}
	// a rollout the window no longer names loses its tail; a comeback reads it again from
	// the first byte, and the stable ids make that read a no-op
	for real := range c.tails {
		if _, ok := seen[real]; !ok {
			delete(c.tails, real)
		}
	}
	return records, nil
}

// codexFileKey is the stable per-file part of a record id: the rollout file's own name.
func codexFileKey(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".jsonl")
}

// listRollouts walks the sessions tree's year/month/day levels and answers the rollout
// files changed since the cutoff, oldest first.
func listRollouts(root string, sinceCutoff int64) []string {
	var out []string
	years, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	for _, year := range years {
		months, err := os.ReadDir(filepath.Join(root, year.Name()))
		if err != nil {
			continue
		}
		for _, month := range months {
			days, err := os.ReadDir(filepath.Join(root, year.Name(), month.Name()))
			if err != nil {
				continue
			}
			for _, day := range days {
				dir := filepath.Join(root, year.Name(), month.Name(), day.Name())
				files, err := os.ReadDir(dir)
				if err != nil {
					continue
				}
				for _, file := range files {
					if !strings.HasPrefix(file.Name(), "rollout-") || !strings.HasSuffix(file.Name(), ".jsonl") {
						continue
					}
					path := filepath.Join(dir, file.Name())
					info, err := file.Info()
					if err != nil || info.ModTime().UnixMilli() < sinceCutoff {
						continue
					}
					out = append(out, path)
				}
			}
		}
	}
	slices.SortFunc(out, strings.Compare)
	return out
}

// codexCounters is one turn's last_token_usage, the fields Codex names there. Comparable,
// so a delta identical to the previous one is one == away.
type codexCounters struct {
	input     int64
	cached    int64
	output    int64
	reasoning int64
}

// codexParse is one rollout file's cross-line state: the session it names, the model and
// cwd its latest turn_context carries, and the last delta, so a repeated one is skipped.
// Pure and stateful-by-argument: the collector owns the state, the parser only reads lines.
type codexParse struct {
	fileKey   string
	sessionID string
	cwd       string
	model     string
	last      codexCounters
	seenLast  bool
	emitted   int
}

// feed parses one rollout line, answering the request records it produced (none for a
// context or a repeated count).
func (s *codexParse) feed(line string) []shared.RequestRecord {
	if line == "" || len(line) > maxHarnessLineBytes {
		return nil
	}
	entry := parseEntry(line)
	if entry == nil {
		return nil
	}
	lineType, _ := stringField(entry, "type")
	payload, ok := objField(entry, "payload")
	if !ok {
		return nil
	}
	switch lineType {
	case "session_meta":
		if id, ok := stringField(payload, "id"); ok {
			s.sessionID = id
		}
		if cwd, ok := stringField(payload, "cwd"); ok {
			s.cwd = cwd
		}
		return nil
	case "turn_context":
		if model, ok := stringField(payload, "model"); ok {
			s.model = model
		}
		if cwd, ok := stringField(payload, "cwd"); ok {
			s.cwd = cwd
		}
		return nil
	case "event_msg":
		return s.feedEvent(payload, entry)
	}
	return nil
}

// feedEvent reads one event_msg payload; only token_count produces records.
func (s *codexParse) feedEvent(payload rawObject, entry rawObject) []shared.RequestRecord {
	eventType, _ := stringField(payload, "type")
	if eventType != "token_count" {
		return nil
	}
	info, ok := objField(payload, "info")
	if !ok {
		return nil
	}
	last, ok := objField(info, "last_token_usage")
	if !ok {
		return nil
	}
	counters := codexCounters{
		input:     numberOrZero(last, "input_tokens"),
		cached:    numberOrZero(last, "cached_input_tokens"),
		output:    numberOrZero(last, "output_tokens"),
		reasoning: numberOrZero(last, "reasoning_output_tokens"),
	}
	if s.seenLast && counters == s.last {
		return nil
	}
	ts, ok := rolloutTsOf(entry)
	if !ok {
		return nil
	}
	s.last = counters
	s.seenLast = true
	record := shared.RequestRecord{
		ID:        "codex:" + s.identity() + ":" + strconv.Itoa(s.emitted),
		SessionID: s.sessionID,
		AgentID:   "main",
		Model:     s.model,
		Ts:        ts,
		Tokens:    codexTokensOf(counters),
		Provider:  shared.ProviderOf(s.model),
		Harness:   shared.HarnessCodex,
	}
	s.emitted++
	if s.cwd != "" {
		record.Cwd = &s.cwd
	}
	return []shared.RequestRecord{record}
}

// identity is the session the file names, else its own name: a line read before the
// session_meta still lands on a stable id.
func (s *codexParse) identity() string {
	if s.sessionID != "" {
		return s.sessionID
	}
	return s.fileKey
}

// codexTokensOf splits one turn's counters the way radar counts them: the cached share
// reads as cache input, the reasoning share of the output names itself only when present.
func codexTokensOf(counters codexCounters) shared.Tokens {
	tokens := shared.Tokens{
		Input:     max(counters.input-counters.cached, 0),
		Output:    counters.output,
		CacheRead: counters.cached,
	}
	if counters.reasoning > 0 {
		tokens.Thinking = &counters.reasoning
	}
	return tokens
}

// rolloutTsOf reads a rollout line's timestamp as epoch ms.
func rolloutTsOf(entry rawObject) (int64, bool) {
	value, ok := stringField(entry, "timestamp")
	if !ok {
		return 0, false
	}
	parsed, ok := jsscalar.ParseISO(value)
	if !ok {
		return 0, false
	}
	return parsed.UnixMilli(), true
}

// numberOrZero reads one counter field, 0 when the line says anything but a number.
func numberOrZero(obj rawObject, key string) int64 {
	value, ok := numberField(obj, key)
	if !ok {
		return 0
	}
	return max(value, 0)
}
