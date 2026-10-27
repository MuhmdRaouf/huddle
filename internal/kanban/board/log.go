package board

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// stampLayout is the offset-carrying timestamp every ticket field and LOG line
// uses.
const stampLayout = "2006-01-02T15:04:05-0700"

func (b Board) stamp() string { return b.now().Format(stampLayout) }

func (b Board) logPath() string      { return filepath.Join(b.Root, "LOG") }
func (b Board) epochPath() string    { return filepath.Join(b.Root, "EPOCH") }
func (b Board) stopPath() string     { return filepath.Join(b.Root, "STOP-LINE") }
func (b Board) featuresPath() string { return filepath.Join(b.Root, "FEATURES") }

// jsonFields renders one JSON object with its pairs in call order: a Go map
// has none, and a LOG line keeps the order its command wrote. Only `"`, `\`
// and the control characters are escaped; UTF-8 stays raw and `<`, `>` and `&`
// alone.
type jsonFields struct {
	buf   strings.Builder
	first bool
}

func newJSONFields() *jsonFields { return &jsonFields{first: true} }
func (j *jsonFields) sep() {
	if !j.first {
		j.buf.WriteByte(',')
	}
	j.first = false
}

func (j *jsonFields) str(key, value string) {
	j.sep()
	j.buf.WriteString(jsonKey(key))
	j.buf.WriteString(":")
	j.buf.WriteString(jsonQuote(value))
}

func (j *jsonFields) num(key string, value int) {
	j.sep()
	j.buf.WriteString(jsonKey(key))
	j.buf.WriteString(":")
	j.buf.WriteString(strconv.Itoa(value))
}

func (j *jsonFields) boolean(key string, value bool) {
	j.sep()
	j.buf.WriteString(jsonKey(key))
	j.buf.WriteString(":")
	j.buf.WriteString(strconv.FormatBool(value))
}

func (j *jsonFields) fields(key string, inner ...logPair) {
	j.sep()
	j.buf.WriteString(jsonKey(key))
	j.buf.WriteString(":")
	nested := newJSONFields()
	for _, p := range inner {
		p.set(nested)
	}
	j.buf.WriteString(nested.String())
}

func (j *jsonFields) String() string {
	return "{" + j.buf.String() + "}"
}

func jsonKey(key string) string { return jsonQuote(key) }

func jsonQuote(v string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range v {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// logPair is one LOG line field: a string, a number, a bool or a nested
// object.
type logPair struct {
	key string
	set func(j *jsonFields)
}

func logStr(key, value string) logPair {
	return logPair{key, func(j *jsonFields) { j.str(key, value) }}
}

func logNum(key string, value int) logPair {
	return logPair{key, func(j *jsonFields) { j.num(key, value) }}
}

func logBool(key string, value bool) logPair {
	return logPair{key, func(j *jsonFields) { j.boolean(key, value) }}
}

func logObj(key string, inner ...logPair) logPair {
	return logPair{key, func(j *jsonFields) { j.fields(key, inner...) }}
}

// appendLog writes one LOG line: the audit trail every mutation leaves. A LOG
// the process may not append to refuses the command after its move, so the
// move is never lost and the line says what to do.
func (b Board) appendLog(event string, pairs ...logPair) error {
	line := newJSONFields()
	line.str("ts", b.stamp())
	line.str("event", event)
	for _, p := range pairs {
		p.set(line)
	}
	if err := b.fs().appendLine(b.logPath(), line.String()); err != nil {
		return refused("cannot append to %s: %s; the move happened, fix the LOG permissions and re-run the command to record it",
			b.logPath(), permissionReason(err))
	}
	return nil
}

func permissionReason(err error) string {
	var pathErr *os.PathError
	if ok := asPathError(err, &pathErr); ok && pathErr.Err != nil {
		return pathErr.Err.Error()
	}
	return err.Error()
}

func asPathError(err error, target **os.PathError) bool {
	if pe, ok := err.(*os.PathError); ok {
		*target = pe
		return true
	}
	return false
}

// readEpoch answers the coordination epoch; a missing or unreadable file is 1.
func (b Board) readEpoch() int {
	data, err := b.fs().readFile(b.epochPath())
	if err != nil {
		return 1
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 1
	}
	return n
}

func (b Board) writeEpoch(ctx context.Context, n int) error {
	return b.fs().writeFile(ctx, b.epochPath(), []byte(strconv.Itoa(n)))
}

// stopped answers the STOP-LINE record, nil while the line runs.
func (b Board) stopped() *StopLine {
	data, err := b.fs().readFile(b.stopPath())
	if err != nil {
		return nil
	}
	var rec StopLine
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil
	}
	return &rec
}

func (b Board) writeStop(ctx context.Context, rec *StopLine) error {
	line := newJSONFields()
	line.num("epoch", rec.Epoch)
	line.str("actor", rec.Actor)
	line.str("note", rec.Note)
	line.str("ts", rec.TS)
	return b.fs().writeFile(ctx, b.stopPath(), []byte(line.String()))
}

// flowEvents are the LOG events that count as a flow command on a ticket: the
// watcher treats everything else (a hand move, a stop) as not the cli's doing.
var flowEvents = map[string]bool{
	"new": true, "promote": true, "pull": true, "review": true, "reject": true,
	"test": true, "done": true, "block": true, "unblock": true, "set": true,
	"move": true, "log": true,
}

// logEntry is one LOG line as the readers below need it; At is zero when the stamp does
// not parse.
type logEntry struct {
	At     time.Time
	Event  string
	Ticket string
}

// logEntries answers the LOG's lines in file order, skipping blank and unreadable ones.
func (b Board) logEntries() []logEntry {
	data, err := b.fs().readFile(b.logPath())
	if err != nil {
		return nil
	}
	var out []logEntry
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var raw struct {
			Ts     string `json:"ts"`
			Event  string `json:"event"`
			Ticket string `json:"ticket"`
		}
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		entry := logEntry{Event: raw.Event, Ticket: raw.Ticket}
		for _, layout := range []string{stampLayout, time.RFC3339} {
			if when, err := time.Parse(layout, raw.Ts); err == nil {
				entry.At = when
				break
			}
		}
		out = append(out, entry)
	}
	return out
}

// LastFlowEvent answers the newest LOG flow event naming the ticket.
func (b Board) LastFlowEvent(id string) (time.Time, bool) {
	entries := b.logEntries()
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if !flowEvents[entry.Event] || entry.Ticket != id || entry.At.IsZero() {
			continue
		}
		return entry.At, true
	}
	return time.Time{}, false
}

// DoneBetween counts the LOG's done lines stamped from <= ts < to.
func (b Board) DoneBetween(from, to time.Time) int {
	count := 0
	for _, entry := range b.logEntries() {
		if entry.Event == "done" && !entry.At.Before(from) && entry.At.Before(to) {
			count++
		}
	}
	return count
}
