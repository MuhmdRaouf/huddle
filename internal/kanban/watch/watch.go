// Package watch keeps one board's live view: a stat walk of the board every
// 300 ms reparses only the ticket files whose size or mtime moved, turns what
// it finds into a snapshot plus deltas, and stamps the moves nothing but the
// cli made. The walk reads through the board's own seam, so a root holding
// kanban.db is watched as live as a folder board.
package watch

import (
	"cmp"
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
	"github.com/MuhmdRaouf/huddle/internal/kanban/ticket"
)

// Snapshot is the whole board as one page loads it.
type Snapshot struct {
	Rev         int64           `json:"rev"`
	Board       string          `json:"board"`
	Project     string          `json:"project"`
	Epoch       int             `json:"epoch"`
	StopLine    *board.StopLine `json:"stopLine"`
	Features    Features        `json:"features"`
	Columns     []Column        `json:"columns"`
	Cards       []board.Card    `json:"cards"`
	GeneratedAt string          `json:"generatedAt"`
}

// Column is one column: its folder name, its display title and how many ticket
// files sit in it.
type Column struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Count int    `json:"count"`
}

// ClosedFeature is one closed feature as the page names it: the feature, when
// it closed and how many of its cards sit hidden.
type ClosedFeature struct {
	Name     string `json:"name"`
	ClosedAt string `json:"closedAt"`
	Cards    int    `json:"cards"`
}

// Features is the board's closed features; Closed is never nil in JSON, an
// empty board encodes as [].
type Features struct {
	Closed []ClosedFeature `json:"closed"`
}

// Meta is a delta's board half: the fields around the cards, sent only when one
// of them changed.
type Meta struct {
	Epoch    int             `json:"epoch"`
	StopLine *board.StopLine `json:"stopLine"`
	Features Features        `json:"features"`
	Columns  []Column        `json:"columns"`
}

// Delta is one change: the cards a page upserts, the ids it drops and, when the
// board around them moved, the new board fields.
type Delta struct {
	Rev    int64        `json:"rev"`
	Upsert []board.Card `json:"upsert"`
	Remove []string     `json:"remove"`
	Meta   *Meta        `json:"meta,omitempty"`
}

// Options tunes a watcher; the zero value polls every 300 ms, reads through
// the board's seam and stamps with time.Now.
type Options struct {
	EveryMs  int
	ReadFile func(string) ([]byte, error)
	Now      func() time.Time
}

// columnTitles are the display titles of the seven columns.
var columnTitles = map[string]string{
	"backlog": "Backlog", "ready": "Ready", "in-progress": "In Progress",
	"review": "Review", "testing": "Testing", "blocked": "Blocked", "done": "Done",
}

// handMoveWindow is how long a flow event keeps owning a ticket's position: a
// file that changes folders with no flow event inside this window behind it
// moved by hand.
const handMoveWindow = 5 * time.Second

// Watcher is one board's live view. Scan does one pass, Run loops, and the
// mutex guards the whole state, so Snapshot answers either the previous or the
// next snapshot, never half of one.
type Watcher struct {
	b     board.Board
	abs   string
	every time.Duration
	read  func(string) ([]byte, error)
	now   func() time.Time

	mu        sync.Mutex
	rev       int64
	files     map[string]*file // path -> the file's last read
	cols      map[string]string
	idSig     map[string]string
	meta      Meta
	snap      Snapshot
	epochStat stat
	epochSeen bool
	stopStat  stat
	stopSeen  bool
	featStat  stat
	featSeen  bool
	featRecs  []board.ClosedFeature
}

// file is one ticket file: its stat at the last read, that read's parse and the
// card derived from it.
type file struct {
	path     string
	id       string
	col      string
	size     int64
	mod      time.Time
	t        ticket.Ticket
	problems []ticket.Problem
	card     board.Card
	hasCard  bool
	hidden   bool // the file's cluster belongs to a closed feature: kept out of the cards
}

// entry is one file the current walk found.
type entry struct {
	col  string
	name string
	size int64
	mod  time.Time
}

// stat is the (present, size, mtime) triple that decides a reread.
type stat struct {
	present bool
	size    int64
	mod     time.Time
}

// New answers a watcher for b. Scan (or Run) before reading Snapshot: an
// unscanned watcher holds the zero snapshot.
func New(b board.Board, opts Options) *Watcher {
	w := &Watcher{
		b:     b,
		abs:   b.Root,
		every: 300 * time.Millisecond,
		read:  b.ReadPath,
		now:   time.Now,
		files: map[string]*file{},
		cols:  map[string]string{},
		idSig: map[string]string{},
	}
	if abs, err := filepath.Abs(b.Root); err == nil {
		w.abs = abs
	}
	if opts.EveryMs > 0 {
		w.every = time.Duration(opts.EveryMs) * time.Millisecond
	}
	if opts.ReadFile != nil {
		w.read = opts.ReadFile
	}
	if opts.Now != nil {
		w.now = opts.Now
	}
	return w
}

// Scan walks the board's seven columns once and answers what changed since the
// previous pass; false means the board still matches the snapshot. The walk is
// stat only: a file is read when it is new or its size or mtime moved, and
// EPOCH, STOP-LINE and FEATURES follow the same rule. A ticket whose cluster a
// closed feature owns stays out of the cards, the counts and the deltas until
// the feature reopens. A card is derived again only when its own file moved, it
// comes back from a closed feature, or another card's move can change what it
// shows: a dependency's column, or a second file holding its id.
func (w *Watcher) Scan(ctx context.Context) (Delta, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()

	found, counts, idCols := w.walk()
	depCol := make(map[string]string, len(idCols))
	sig := make(map[string]string, len(idCols))
	for id, cols := range idCols {
		depCol[id] = cols[0]
		sig[id] = strings.Join(cols, "|")
	}

	// a file sitting in a column its id never sat in, with no flow command
	// behind it, is stamped before its read, so the card this delta carries
	// already shows the mark
	for path, e := range found {
		id := idOf(e.name)
		if prevSig, known := w.idSig[id]; known && !sigHas(prevSig, e.col) && w.handMove(id, now) {
			if err := w.b.RecordHandMove(ctx, id, w.cols[id], e.col); err == nil {
				if info, ok := w.b.StatPath(path); ok {
					e.size, e.mod = info.Size, info.ModTime
					found[path] = e
				}
			}
		}
	}

	// an id whose columns changed can change what other cards show
	affected := make(map[string]bool, len(w.idSig))
	for id, s := range sig {
		if w.idSig[id] != s {
			affected[id] = true
		}
	}
	for id, s := range w.idSig {
		if _, ok := sig[id]; !ok && s != "" {
			affected[id] = true // the id left the board; its dependants show it missing now
		}
	}

	// the memory moves with the walk, not with the delta: a pass that answers
	// no change must not leave the stamp a stale column to read next time.
	// idSig keeps the ids that left, so a hand move that spans two passes (out
	// of the board, then into another column) is still seen
	for id, col := range depCol {
		w.cols[id] = col
		w.idSig[id] = sig[id]
	}

	var gone []string
	for path, f := range w.files {
		if _, ok := found[path]; ok {
			continue
		}
		if f.hasCard && !f.hidden {
			gone = append(gone, f.card.ID) // a hidden card left the page already
		}
		delete(w.files, path)
	}

	recompute := make(map[string]bool, len(found))
	for path, e := range found {
		if old, ok := w.files[path]; ok && old.size == e.size && old.mod.Equal(e.mod) {
			continue
		}
		recompute[path] = true
		w.readFile(path, e)
	}
	if len(affected) > 0 {
		for _, f := range w.files {
			if recompute[f.path] {
				continue
			}
			if affected[f.t.ID] || affected[f.id] {
				recompute[f.path] = true
				continue
			}
			for _, dep := range f.t.DependsOn {
				if affected[dep] {
					recompute[f.path] = true
					break
				}
			}
		}
	}

	epoch, stop := w.readBoardFiles()

	// a closed feature keeps its tickets out of the page: the counts drop, the
	// feature carries how many of its cards sit hidden
	closed := make(map[string]bool, len(w.featRecs))
	hiddenBy := make(map[string]int, len(w.featRecs))
	for _, rec := range w.featRecs {
		closed[rec.Name] = true
	}
	for _, f := range w.files {
		if !closed[f.t.Cluster] {
			continue
		}
		counts[f.col]--
		hiddenBy[f.t.Cluster]++
	}
	feats := Features{Closed: make([]ClosedFeature, 0, len(w.featRecs))}
	for _, rec := range w.featRecs {
		feats.Closed = append(feats.Closed, ClosedFeature{
			Name: rec.Name, ClosedAt: rec.ClosedAt, Cards: hiddenBy[rec.Name],
		})
	}

	columns := make([]Column, 0, len(board.Columns))
	for _, col := range board.Columns {
		columns = append(columns, Column{ID: col, Title: columnTitles[col], Count: counts[col]})
	}
	newMeta := Meta{Epoch: epoch, StopLine: stop, Features: feats, Columns: columns}
	metaChanged := !reflect.DeepEqual(w.meta, newMeta)

	where := func(id string) (string, bool) {
		col, ok := depCol[id]
		return col, ok
	}
	groups := make(map[string][]*file, len(board.Columns))
	for _, f := range w.files {
		groups[f.col] = append(groups[f.col], f)
	}
	upsert := []board.Card{}
	cards := make([]board.Card, 0, len(w.files))
	present := make(map[string]bool, len(w.files))
	for _, col := range board.Columns {
		group := groups[col]
		slices.SortStableFunc(group, lessCard)
		for _, f := range group {
			if closed[f.t.Cluster] {
				if f.hasCard && !f.hidden {
					gone = append(gone, f.card.ID) // the page drops it this delta
				}
				f.hidden = true
				continue
			}
			wasHidden := f.hidden
			f.hidden = false
			if recompute[f.path] || wasHidden {
				selfID := f.id
				prev, had := f.card, f.hasCard
				f.card = board.CardOfLookup(f.t, f.col, f.path, f.problems, where, func(id string) bool {
					n := len(idCols[id])
					if id == selfID {
						n--
					}
					return n > 0
				})
				// the walk's own stat stamps the card: a db board backs no path
				// with a file for CardOfLookup's stat to answer
				if !f.mod.IsZero() {
					f.card.UpdatedAt = f.mod.Format(time.RFC3339)
				}
				f.hasCard = true
				if !had || wasHidden || !reflect.DeepEqual(prev, f.card) {
					upsert = append(upsert, f.card)
				}
			}
			cards = append(cards, f.card)
			present[f.card.ID] = true
		}
	}
	remove := []string{}
	for _, id := range gone {
		if !present[id] {
			remove = append(remove, id) // an id that still shows moved, it did not leave
		}
	}
	slices.Sort(remove)

	if len(upsert) == 0 && len(remove) == 0 && !metaChanged {
		return Delta{}, false
	}
	w.rev++
	w.meta = newMeta
	w.snap = Snapshot{
		Rev: w.rev, Board: w.abs, Project: projectOf(w.abs),
		Epoch: newMeta.Epoch, StopLine: newMeta.StopLine,
		Features: newMeta.Features,
		Columns:  newMeta.Columns, Cards: cards,
		GeneratedAt: now.Format(time.RFC3339),
	}
	d := Delta{Rev: w.rev, Upsert: upsert, Remove: remove}
	if metaChanged {
		d.Meta = &newMeta
	}
	return d, true
}

// Snapshot answers the current snapshot. A pass replaces it wholesale, never
// edits it in place, so the answer stays safe to read while the next pass runs.
func (w *Watcher) Snapshot() Snapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.snap
}

// Run scans at once and then every interval, handing each delta to publish
// until ctx ends; publish runs with no watcher lock held.
func (w *Watcher) Run(ctx context.Context, publish func(Delta)) {
	ticker := time.NewTicker(w.every)
	defer ticker.Stop()
	for {
		if d, changed := w.Scan(ctx); changed {
			publish(d)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// walk stats the board's columns and answers the ticket files it found, the
// per-column counts and each id's columns in board order (so a set's first
// column is the one Find answers for it). It reads through the board's seam:
// the folder board's files, or a db board's rows.
func (w *Watcher) walk() (found map[string]entry, counts map[string]int, idCols map[string][]string) {
	found = map[string]entry{}
	counts = map[string]int{}
	idCols = map[string][]string{}
	for _, col := range board.Columns {
		for _, de := range w.b.ColumnEntries(col) {
			found[de.Path] = entry{col: col, name: de.Name, size: de.Size, mod: de.ModTime}
			counts[col]++
			id := idOf(de.Name)
			idCols[id] = append(idCols[id], col)
		}
	}
	return found, counts, idCols
}

// readFile reads and parses one ticket file. A file that cannot be read is
// dropped from the cache this pass and retried the next; one that cannot be
// parsed still answers a card, named after its file and flagged incomplete.
func (w *Watcher) readFile(path string, e entry) {
	data, err := w.read(path)
	if err != nil {
		delete(w.files, path)
		return
	}
	t, problems := ticket.Parse(e.name, data)
	if t.ID == "" {
		t.ID = idOf(e.name)
	}
	if t.Title == "" {
		t.Title = e.name
	}
	w.files[path] = &file{
		path: path, id: idOf(e.name), col: e.col, size: e.size, mod: e.mod,
		t: t, problems: problems,
	}
}

// readBoardFiles answers the epoch and the stop-line, and refreshes the closed
// features, reading each only when its stat moved. A missing or unreadable
// EPOCH reads as 1, as the board does; a missing or unreadable FEATURES means
// no feature is closed; an unchanged pass keeps the values the last meta
// carries.
func (w *Watcher) readBoardFiles() (int, *board.StopLine) {
	epoch := w.meta.Epoch
	epochPath := filepath.Join(w.b.Root, "EPOCH")
	if s := w.statOf(epochPath); !w.epochSeen || s != w.epochStat {
		w.epochSeen, w.epochStat = true, s
		epoch = 1
		if data, err := w.read(epochPath); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
				epoch = n
			}
		}
	}
	stop := w.meta.StopLine
	stopPath := filepath.Join(w.b.Root, "STOP-LINE")
	if s := w.statOf(stopPath); !w.stopSeen || s != w.stopStat {
		w.stopSeen, w.stopStat = true, s
		stop = nil
		if data, err := w.read(stopPath); err == nil {
			var rec board.StopLine
			if json.Unmarshal(data, &rec) == nil {
				stop = &rec
			}
		}
	}
	w.readFeatures()
	return epoch, stop
}

// readFeatures refreshes the closed-feature list when the FEATURES file's stat
// moved, the way readBoardFiles reads EPOCH and STOP-LINE. On a db board the
// file is the file:FEATURES meta row, and its stat moves by size alone.
func (w *Watcher) readFeatures() {
	featPath := filepath.Join(w.b.Root, "FEATURES")
	if s := w.statOf(featPath); !w.featSeen || s != w.featStat {
		w.featSeen, w.featStat = true, s
		w.featRecs = w.b.ClosedFeatures()
	}
}

// handMove answers whether a flow command owns this ticket's position: one
// inside the window does, anything older or absent leaves the move to a hand.
func (w *Watcher) handMove(id string, now time.Time) bool {
	when, ok := w.b.LastFlowEvent(id)
	if !ok {
		return true
	}
	return now.Sub(when) > handMoveWindow
}

// lessCard orders one column: expedite first, then priority, then id; done goes
// by finish, newest first.
func lessCard(a, b *file) int {
	if a.col == "done" {
		if c := finishTime(b).Compare(finishTime(a)); c != 0 {
			return c
		}
	}
	if a.t.Expedite != b.t.Expedite {
		if a.t.Expedite {
			return -1
		}
		return 1
	}
	if c := cmp.Compare(priorityRank(a.t.Priority), priorityRank(b.t.Priority)); c != 0 {
		return c
	}
	return strings.Compare(a.t.ID, b.t.ID)
}

// priorityRank answers P0..P3 as 0..3; anything else sorts last.
func priorityRank(p string) int {
	if len(p) == 2 && p[0] == 'P' && p[1] >= '0' && p[1] <= '3' {
		return int(p[1] - '0')
	}
	return 9
}

// finishTime answers the ticket's finished_at, zero when it does not parse.
func finishTime(f *file) time.Time {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05-0700"} {
		if when, err := time.Parse(layout, f.t.Fields["finished_at"]); err == nil {
			return when
		}
	}
	return time.Time{}
}

// statOf stats one path on the board's seam: a real file, or a db board's
// meta row.
func (w *Watcher) statOf(path string) stat {
	info, ok := w.b.StatPath(path)
	if !ok {
		return stat{}
	}
	return stat{present: true, size: info.Size, mod: info.ModTime}
}

// sigHas answers whether an id's column signature holds the column.
func sigHas(sig, col string) bool {
	return strings.Contains("|"+sig+"|", "|"+col+"|")
}

// idOf answers the ticket id a column file names.
func idOf(name string) string { return strings.TrimSuffix(name, ".md") }

// projectOf answers the repo folder name: a board sits at
// <repo>/.agents/kanban, so the project is two levels up.
func projectOf(boardPath string) string {
	return filepath.Base(filepath.Dir(filepath.Dir(filepath.Clean(boardPath))))
}
