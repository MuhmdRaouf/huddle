// The spool: dated jsonl files under `<state>/spool` holding hook lines. Listed by date
// window (datestamped filenames compare lexicographically), parsed tolerantly — a malformed
// line is skipped, never fatal.
package ingest

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// dateFile is a spool file's name: the day it holds, as .jsonl.
var dateFile = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}\.jsonl$`)

// msPerDay is one day in epoch milliseconds.
const msPerDay int64 = 86_400_000

// maxDateMs is the widest epoch a JavaScript Date can hold: beyond it there is no ISO string
// to normalize an epoch-ms ts to.
const maxDateMs = 8.64e15

// ListSpoolFiles lists the spool files whose date is within days of now (the day before the
// cutoff included, so a window never loses its boundary day), oldest first, as full paths; a
// missing directory yields nothing.
func ListSpoolFiles(ctx context.Context, env domain.EnvLookup, now int64, days int) []string {
	dir := paths.SpoolDir(ctx, env)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	cutoff := now - int64(days)*msPerDay
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if !dateFile.MatchString(name) {
			continue
		}
		day, ok := utcMidnightMs(name)
		if !ok || day < cutoff-msPerDay {
			continue
		}
		names = append(names, name)
	}
	slices.Sort(names)
	files := make([]string, 0, len(names))
	for _, name := range names {
		files = append(files, filepath.Join(dir, name))
	}
	return files
}

// utcMidnightMs is a spool file's date at UTC midnight in epoch ms; ok is false when the name
// does not hold a real calendar date, the case Date.parse answers NaN for (and NaN never
// makes the window).
func utcMidnightMs(name string) (int64, bool) {
	year, _ := strconv.Atoi(name[0:4])
	month, _ := strconv.Atoi(name[5:7])
	day, _ := strconv.Atoi(name[8:10])
	date := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if date.Year() != year || int(date.Month()) != month || date.Day() != day {
		return 0, false
	}
	return date.UnixMilli(), true
}

// ParseSpoolLine reads one spool line; ok is false when the line is not an event (broken
// JSON, an array or a scalar, no string ts, no string event name). An epoch-millisecond ts
// reads as the ISO string everything downstream expects, and the line rides along whole —
// every key it carried, in the order it carried them — as the payload the store serves.
func ParseSpoolLine(line string) (shared.SpoolLine, bool) {
	value, err := jsfmt.DecodeValue([]byte(line))
	if err != nil || value.Kind() != jsfmt.KindObject {
		// malformed line: skip it, the spool is append-only so it will not come back different
		return shared.SpoolLine{}, false
	}
	value, ok := withISOts(value)
	if !ok {
		return shared.SpoolLine{}, false
	}
	if ts, ok := lastFieldOf(value, "ts"); !ok || ts.Kind() != jsfmt.KindString {
		return shared.SpoolLine{}, false
	}
	if event, ok := lastFieldOf(value, "event"); !ok || event.Kind() != jsfmt.KindString {
		return shared.SpoolLine{}, false
	}
	var parsed shared.SpoolLine
	if err := json.Unmarshal([]byte(value.EncodeJSON()), &parsed); err != nil {
		return shared.SpoolLine{}, false
	}
	return parsed, true
}

// withISOts swaps a numeric ts for the ISO string everything downstream reads (a router may
// stamp epoch milliseconds); ok is false when no Date can render the value, the case the ISO
// conversion throws on.
func withISOts(object *jsfmt.Value) (*jsfmt.Value, bool) {
	keys := object.Keys()
	fields := slices.Clone(object.Fields())
	for i, key := range keys {
		if key != "ts" || fields[i].Kind() != jsfmt.KindNumber {
			continue
		}
		ms, err := strconv.ParseFloat(fields[i].EncodeJSON(), 64)
		if err != nil || math.Abs(ms) > maxDateMs {
			return nil, false
		}
		fields[i] = jsfmt.String(jsfmt.ISOTime(time.UnixMilli(int64(ms))))
	}
	return jsfmt.Object(slices.Clone(keys), fields), true
}

// lastFieldOf is a key's last value, the one a JSON parse keeps.
func lastFieldOf(object *jsfmt.Value, key string) (*jsfmt.Value, bool) {
	var last *jsfmt.Value
	keys, fields := object.Keys(), object.Fields()
	for i, existing := range keys {
		if existing == key {
			last = fields[i]
		}
	}
	return last, last != nil
}

// ReadSpoolLines reads every parseable line from the given spool files, in file order;
// unreadable files are skipped.
func ReadSpoolLines(files []string) []shared.SpoolLine {
	var lines []shared.SpoolLine
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if parsed, ok := ParseSpoolLine(line); ok {
				lines = append(lines, parsed)
			}
		}
	}
	return lines
}
