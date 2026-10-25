package cost

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unique"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/radar/clock"
	"github.com/MuhmdRaouf/huddle/internal/radar/paths"
	"github.com/MuhmdRaouf/huddle/internal/radar/shared"
)

// LedgerRow is one request in the usage ledger.
type LedgerRow struct {
	ID        string
	Ts        int64
	Model     string
	SessionID string
	AgentID   string
	Tokens    shared.Tokens
	// CacheWrite1h, Speed, Geo and ServiceTier are the rest of what pricing reads; nil when
	// the row predates the field or the request said nothing.
	CacheWrite1h *int64
	Speed        *string
	Geo          *string
	ServiceTier  *string
}

// SessionNames is what a session is called and where it ran, remembered so a 30-day-old row
// still has a name.
type SessionNames struct {
	Project *string
	Title   *string
	// Repo is the repo root the session (or, for a job, its workspace) ran in; nil when
	// never known.
	Repo *string
	// ParentSessionID is the session that submitted this one as a job; nil for a plain
	// session.
	ParentSessionID *string
	// Name is the session's own display name (custom title, slug, job name); nil when it
	// only has its id.
	Name *string
}

// AgentNames is what an agent is called: its display name, its agent type and its task
// description when known.
type AgentNames struct {
	Name        string
	Type        *string
	Description *string
}

// LedgerNames is what a session and an agent are called, remembered so a 30-day-old row
// still has a name.
type LedgerNames struct {
	Sessions map[string]SessionNames
	Agents   map[string]AgentNames
}

// LedgerDays is how long the ledger keeps a row.
const LedgerDays = 35

// dayMs is one day in the epoch milliseconds every timestamp counts.
const dayMs = 86_400_000

// dayFileName matches a day file's name, and nothing else a state dir may hold.
var dayFileName = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}\.json$`)

// packed is a row as it lands on disk: the eight base slots, then what pricing read off the
// request (nil = the request said nothing).
type packed struct {
	Ts           int64
	Model        string
	SessionID    string
	AgentID      string
	Input        int64
	Output       int64
	CacheRead    int64
	CacheWrite   int64
	CacheWrite1h *int64
	Speed        *string
	Geo          *string
	ServiceTier  *string
	// slots is how many slots the row carries — 8 as an older ledger wrote it, 12 now — so
	// a row read back equals, and rewrites as, exactly what it carried.
	slots int
}

// dayFile is one UTC day's ledger: its rows keyed by request id, the session and agent
// names on record, and the one *string per distinct optional value its rows share.
type dayFile struct {
	rows     orderedMap[packed]
	sessions orderedMap[SessionNames]
	agents   orderedMap[AgentNames]
	// texts holds one pointer per distinct Speed, Geo or ServiceTier text this day's rows
	// carry — a handful of values, so rows share a pointer instead of holding an allocation
	// each.
	texts map[string]*string
}

// emptyDay is a day nothing has landed in yet.
func emptyDay() *dayFile { return &dayFile{} }

// canonical is one shared copy of a string: models, session ids and agent ids repeat across
// hundreds of thousands of rows, and unique hands every equal string the same copy, which the
// runtime frees when no row references it anymore.
func canonical(s string) string { return unique.Make(s).Value() }

// canonicalPtr is an optional string's pointer shared by every row of the day carrying the
// same text, nil staying nil. Rows must never be written through it: every holder assumes the
// text is fixed.
func (d *dayFile) canonicalPtr(text *string) *string {
	if text == nil {
		return nil
	}
	if known, ok := d.texts[*text]; ok {
		return known
	}
	value := canonical(*text)
	ptr := &value
	if d.texts == nil {
		d.texts = make(map[string]*string)
	}
	d.texts[*text] = ptr
	return ptr
}

// Ledger is the usage ledger: every model request radar has seen, kept for LedgerDays in one
// small JSON file per UTC day. A request seen again (a stream update, a restart re-reading
// the transcript) replaces itself, never counts twice. Only numbers and ids are written:
// never a prompt, a tool input or a body.
type Ledger struct {
	dir     string
	days    orderedMap[*dayFile]
	dayOfID map[string]string
	dirty   orderedMap[struct{}]
	ver     int64
	cache   *ledgerCache
	now     func() time.Time
}

// ledgerCache holds the rows derived from the current state, keyed to its version.
type ledgerCache struct {
	version int64
	rows    []LedgerRow
}

// LedgerDir is where the ledger's day files live.
func LedgerDir(ctx context.Context, env domain.EnvLookup) string {
	return filepath.Join(paths.StateDir(ctx, env), "ledger")
}

// DayKey is the UTC date of a timestamp, as the file name stem.
func DayKey(ts int64) string {
	return clock.DayKey(time.UnixMilli(ts))
}

// NewLedger loads the days inside the window and returns a ledger on them; now is the clock
// it reads (nil is time.Now).
func NewLedger(ctx context.Context, env domain.EnvLookup, now func() time.Time) *Ledger {
	if now == nil {
		now = time.Now
	}
	ledger := &Ledger{
		dir:     LedgerDir(ctx, env),
		dayOfID: make(map[string]string),
		now:     now,
	}
	ledger.load()
	return ledger
}

// cutoffKey is the oldest UTC day still inside the window.
func (l *Ledger) cutoffKey() string {
	return DayKey(l.now().UnixMilli() - LedgerDays*dayMs)
}

// listDays is every day file's stem in the ledger dir, oldest first; a dir that cannot be
// read holds none.
func (l *Ledger) listDays() []string {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return nil
	}
	var days []string
	for _, entry := range entries {
		if !dayFileName.MatchString(entry.Name()) {
			continue
		}
		days = append(days, entry.Name()[:10])
	}
	slices.Sort(days)
	return days
}

// readDay is one day file, or nil when it cannot be read.
func (l *Ledger) readDay(key string) *dayFile {
	text, err := os.ReadFile(filepath.Join(l.dir, key+".json"))
	if err != nil {
		return nil
	}
	return parseDay(string(text))
}

// load brings every day inside the window into memory and indexes its rows by id.
func (l *Ledger) load() {
	cutoff := l.cutoffKey()
	for _, key := range l.listDays() {
		if key < cutoff {
			continue
		}
		entry := l.readDay(key)
		if entry == nil {
			continue
		}
		l.days.set(key, entry)
		for _, id := range entry.rows.keys() {
			l.dayOfID[id] = key
		}
	}
}

// dayOf is the day's entry, an empty day created for it when it has none yet.
func (l *Ledger) dayOf(key string) *dayFile {
	if entry, ok := l.days.get(key); ok {
		return entry
	}
	entry := emptyDay()
	l.days.set(key, entry)
	return entry
}

// changed marks a day for writing and bumps the version everything derived caches against.
func (l *Ledger) changed(key string) {
	l.dirty.set(key, struct{}{})
	l.ver++
}

// prune drops every day outside the window, in memory and on disk; a file past the cutoff is
// never read either way, so one that cannot be removed stays put without a word.
func (l *Ledger) prune() {
	cutoff := l.cutoffKey()
	old := newStringSet(l.days.keys(), l.listDays())
	for key := range old {
		if key >= cutoff {
			continue
		}
		if entry, ok := l.days.get(key); ok {
			for _, id := range entry.rows.keys() {
				delete(l.dayOfID, id)
			}
		}
		l.days.delete(key)
		l.dirty.delete(key)
		_ = os.Remove(filepath.Join(l.dir, key+".json"))
	}
}

// Record adds or replaces one request.
func (l *Ledger) Record(request shared.RequestRecord) {
	if request.Model == "" {
		return
	}
	key := DayKey(request.Ts)
	if key < l.cutoffKey() {
		return
	}
	if previous, ok := l.dayOfID[request.ID]; ok && previous != key {
		if entry, ok := l.days.get(previous); ok {
			entry.rows.delete(request.ID)
		}
		l.changed(previous)
	}
	day := l.dayOf(key)
	row := day.packRow(request)
	if current, ok := day.rows.get(request.ID); ok && equalPacked(current, row) {
		return
	}
	day.rows.set(request.ID, row)
	l.dayOfID[request.ID] = key
	l.changed(key)
}

// NameSession remembers what a session is called and where it ran.
func (l *Ledger) NameSession(id string, names SessionNames) {
	key := DayKey(l.now().UnixMilli())
	day := l.dayOf(key)
	if known, ok := day.sessions.get(id); ok && namesEqual(known, names) {
		return
	}
	day.sessions.set(id, copySessionNames(names))
	l.changed(key)
}

// NameAgent remembers what an agent of one session is called.
func (l *Ledger) NameAgent(sessionID string, agentID string, agent AgentNames) {
	key := DayKey(l.now().UnixMilli())
	id := sessionID + ":" + agentID
	day := l.dayOf(key)
	if known, ok := day.agents.get(id); ok && agentsEqual(known, agent) {
		return
	}
	day.agents.set(id, copyAgentNames(agent))
	l.changed(key)
}

// Rows is every row at or after since, oldest first.
func (l *Ledger) Rows(since int64) []LedgerRow {
	if l.cache == nil || l.cache.version != l.ver {
		var all []LedgerRow
		for _, key := range l.days.keys() {
			entry, _ := l.days.get(key)
			for _, id := range entry.rows.keys() {
				row, _ := entry.rows.get(id)
				all = append(all, unpackRow(id, row))
			}
		}
		slices.SortStableFunc(all, func(a, b LedgerRow) int {
			switch {
			case a.Ts < b.Ts:
				return -1
			case a.Ts > b.Ts:
				return 1
			}
			return 0
		})
		l.cache = &ledgerCache{version: l.ver, rows: all}
	}
	rows := make([]LedgerRow, 0, len(l.cache.rows))
	for _, row := range l.cache.rows {
		if row.Ts >= since {
			rows = append(rows, row)
		}
	}
	return rows
}

// Names is what the sessions and agents on record are called, the newest name a session was
// given winning.
func (l *Ledger) Names() LedgerNames {
	out := LedgerNames{
		Sessions: make(map[string]SessionNames, l.days.len()),
		Agents:   make(map[string]AgentNames, l.days.len()),
	}
	// oldest day first, so the newest name a session was given wins
	days := l.days.keys()
	slices.Sort(days)
	for _, key := range days {
		entry, _ := l.days.get(key)
		for _, id := range entry.sessions.keys() {
			names, _ := entry.sessions.get(id)
			out.Sessions[id] = names
		}
		for _, id := range entry.agents.keys() {
			agent, _ := entry.agents.get(id)
			out.Agents[id] = agent
		}
	}
	return out
}

// Flush prunes the window and writes the days that changed; never fails (the ledger is a
// convenience, the dashboard runs without it — a full disk or a removed state dir leaves the
// dirty days for the next flush).
func (l *Ledger) Flush(ctx context.Context) {
	l.prune()
	if l.dirty.len() == 0 {
		return
	}
	if err := os.MkdirAll(l.dir, 0o700); err != nil {
		return
	}
	for _, key := range l.dirty.keys() {
		entry, ok := l.days.get(key)
		if !ok {
			l.dirty.delete(key)
			continue
		}
		if err := writeDay(l.dir, key, entry); err != nil {
			return
		}
		l.dirty.delete(key)
	}
}

// Version bumps on every change, so callers can cache what they derive.
func (l *Ledger) Version() int64 { return l.ver }

// writeDay lands one day file: a temp file named for this process, then a rename over the
// day's own name.
func writeDay(dir, key string, entry *dayFile) error {
	path := filepath.Join(dir, key+".json")
	tmp := path + "." + strconv.Itoa(os.Getpid()) + ".tmp"
	if err := os.WriteFile(tmp, encodeDay(entry), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// packRow is a request as it lands on disk, its repeated strings interned on the day.
func (d *dayFile) packRow(request shared.RequestRecord) packed {
	row := packed{
		Ts:         request.Ts,
		Model:      canonical(request.Model),
		SessionID:  canonical(request.SessionID),
		AgentID:    canonical(request.AgentID),
		Input:      request.Tokens.Input,
		Output:     request.Tokens.Output,
		CacheRead:  request.Tokens.CacheRead,
		CacheWrite: request.Tokens.CacheWrite,
		slots:      12,
	}
	if request.CacheWrite1h != nil {
		row.CacheWrite1h = int64Ptr(*request.CacheWrite1h)
	}
	row.Speed = d.canonicalPtr(request.Speed)
	row.Geo = d.canonicalPtr(request.Geo)
	row.ServiceTier = d.canonicalPtr(request.ServiceTier)
	return row
}

// unpackRow is a stored row as the views read it, the pricing fields absent when the request
// said nothing.
func unpackRow(id string, row packed) LedgerRow {
	out := LedgerRow{
		ID:        id,
		Ts:        row.Ts,
		Model:     row.Model,
		SessionID: row.SessionID,
		AgentID:   row.AgentID,
		Tokens: shared.Tokens{
			Input:      row.Input,
			Output:     row.Output,
			CacheRead:  row.CacheRead,
			CacheWrite: row.CacheWrite,
		},
	}
	if row.slots > 8 {
		out.CacheWrite1h = int64Copy(row.CacheWrite1h)
		out.Speed = textCopy(row.Speed)
		out.Geo = textCopy(row.Geo)
		out.ServiceTier = textCopy(row.ServiceTier)
	}
	return out
}

// parseDay reads a day file keeping only well-formed entries; anything unreadable is an
// empty day.
func parseDay(text string) *dayFile {
	day := emptyDay()
	value, err := jsfmt.DecodeValue([]byte(text))
	if err != nil || value.Kind() != jsfmt.KindObject {
		return day
	}
	// JSON keeps the last copy of a repeated key, at the first position
	rows := lastFieldOf(value, "rows")
	sessions := lastFieldOf(value, "sessions")
	agents := lastFieldOf(value, "agents")
	if rows != nil && rows.Kind() == jsfmt.KindObject {
		for i, id := range rows.Keys() {
			if row, ok := day.parsePacked(rows.Fields()[i]); ok {
				day.rows.set(id, row)
			}
		}
	}
	if sessions != nil && sessions.Kind() == jsfmt.KindObject {
		for i, id := range sessions.Keys() {
			day.sessions.set(id, parseSessionEntry(sessions.Fields()[i]))
		}
	}
	if agents != nil && agents.Kind() == jsfmt.KindObject {
		for i, id := range agents.Keys() {
			if agent, ok := parseAgentEntry(agents.Fields()[i]); ok {
				day.agents.set(id, agent)
			}
		}
	}
	return day
}

// lastFieldOf is an object's last value for a key, or nil when the key is absent.
func lastFieldOf(object *jsfmt.Value, key string) *jsfmt.Value {
	var last *jsfmt.Value
	keys, fields := object.Keys(), object.Fields()
	for i := range keys {
		if keys[i] == key {
			last = fields[i]
		}
	}
	return last
}

// parsePacked reads a row's slots, ok false when the row is not well-formed. Token counts
// are counts, so a figure that is not an integer reads as malformed. The row's repeated
// strings intern on the day, so rows never carry the decoder's own copy.
func (d *dayFile) parsePacked(value *jsfmt.Value) (packed, bool) {
	if value.Kind() != jsfmt.KindArray {
		return packed{}, false
	}
	items := value.Items()
	if len(items) != 8 && len(items) != 12 {
		return packed{}, false
	}
	ts, ok := numberValue(items[0])
	if !ok {
		return packed{}, false
	}
	model, ok := stringValue(items[1])
	if !ok {
		return packed{}, false
	}
	sessionID, ok := stringValue(items[2])
	if !ok {
		return packed{}, false
	}
	agentID, ok := stringValue(items[3])
	if !ok {
		return packed{}, false
	}
	row := packed{
		Ts: ts, Model: canonical(model), SessionID: canonical(sessionID), AgentID: canonical(agentID),
		slots: len(items),
	}
	counts := []*int64{&row.Input, &row.Output, &row.CacheRead, &row.CacheWrite}
	for i, item := range items[4:8] {
		count, ok := numberValue(item)
		if !ok {
			return packed{}, false
		}
		*counts[i] = count
	}
	if len(items) == 8 {
		return row, true
	}
	if items[8].Kind() == jsfmt.KindNull {
		row.CacheWrite1h = nil
	} else if count, ok := numberValue(items[8]); ok {
		row.CacheWrite1h = int64Ptr(count)
	} else {
		return packed{}, false
	}
	texts := []**string{&row.Speed, &row.Geo, &row.ServiceTier}
	for i, item := range items[9:12] {
		if item.Kind() == jsfmt.KindNull {
			*texts[i] = nil
			continue
		}
		if text, ok := stringValue(item); ok {
			*texts[i] = d.canonicalPtr(&text)
			continue
		}
		return packed{}, false
	}
	return row, true
}

// parseSessionEntry reads a session entry; anything unreadable counts as unknown.
func parseSessionEntry(value *jsfmt.Value) SessionNames {
	if value.Kind() != jsfmt.KindObject {
		return SessionNames{}
	}
	return SessionNames{
		Project:         textOrNil(lastFieldOf(value, "project")),
		Title:           textOrNil(lastFieldOf(value, "title")),
		Repo:            textOrNil(lastFieldOf(value, "repo")),
		ParentSessionID: textOrNil(lastFieldOf(value, "parentSessionId")),
		Name:            textOrNil(lastFieldOf(value, "name")),
	}
}

// parseAgentEntry reads an agent entry; an older ledger wrote the name alone, and nothing
// readable is nil.
func parseAgentEntry(value *jsfmt.Value) (AgentNames, bool) {
	if value.Kind() == jsfmt.KindString {
		return AgentNames{Name: value.Text()}, true
	}
	if value.Kind() != jsfmt.KindObject {
		return AgentNames{}, false
	}
	name := lastFieldOf(value, "name")
	if name == nil || name.Kind() != jsfmt.KindString {
		return AgentNames{}, false
	}
	return AgentNames{
		Name:        name.Text(),
		Type:        textOrNil(lastFieldOf(value, "type")),
		Description: textOrNil(lastFieldOf(value, "description")),
	}, true
}

// textOrNil reads a string field, or nil for any other shape: every name on disk is
// optional, absent meaning unknown.
func textOrNil(value *jsfmt.Value) *string {
	if value == nil || value.Kind() != jsfmt.KindString {
		return nil
	}
	return textPtr(value.Text())
}

// numberValue reads a JSON number as an exact integer count; anything else (a string, a
// fraction, an out-of-range figure) reads as absent.
func numberValue(value *jsfmt.Value) (int64, bool) {
	if value.Kind() != jsfmt.KindNumber {
		return 0, false
	}
	text := value.EncodeJSON()
	if n, err := strconv.ParseInt(text, 10, 64); err == nil {
		return n, true
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) || f != math.Trunc(f) ||
		f > math.MaxInt64 || f < math.MinInt64 {
		return 0, false
	}
	return int64(f), true
}

// stringValue reads a JSON string.
func stringValue(value *jsfmt.Value) (string, bool) {
	if value.Kind() != jsfmt.KindString {
		return "", false
	}
	return value.Text(), true
}

// encodeDay writes a day file: version 1, the rows, the session names, the agent names, keys
// in insertion order.
func encodeDay(entry *dayFile) []byte {
	var out strings.Builder
	out.WriteString(`{"version":1,"rows":`)
	encodeObject(&out, &entry.rows, encodePacked)
	out.WriteString(`,"sessions":`)
	encodeObject(&out, &entry.sessions, encodeSession)
	out.WriteString(`,"agents":`)
	encodeObject(&out, &entry.agents, encodeAgent)
	out.WriteByte('}')
	return []byte(out.String())
}

// encodeObject writes the map as a JSON object, keys in insertion order and each value
// through encode.
func encodeObject[V any](out *strings.Builder, m *orderedMap[V], encode func(V) string) {
	out.WriteByte('{')
	for i, key := range m.order {
		if i > 0 {
			out.WriteByte(',')
		}
		out.WriteString(jsfmt.String(key).EncodeJSON())
		out.WriteByte(':')
		out.WriteString(encode(m.values[key]))
	}
	out.WriteByte('}')
}

// encodePacked writes a row as the JSON array it lands on disk as: the eight base slots,
// then the pricing four a 12-slot row carries.
func encodePacked(p packed) string {
	parts := make([]string, 0, 12)
	parts = append(parts,
		strconv.FormatInt(p.Ts, 10),
		jsfmt.String(p.Model).EncodeJSON(),
		jsfmt.String(p.SessionID).EncodeJSON(),
		jsfmt.String(p.AgentID).EncodeJSON(),
		strconv.FormatInt(p.Input, 10),
		strconv.FormatInt(p.Output, 10),
		strconv.FormatInt(p.CacheRead, 10),
		strconv.FormatInt(p.CacheWrite, 10),
	)
	if p.slots == 12 {
		parts = append(parts, intOrNull(p.CacheWrite1h), textOrNull(p.Speed), textOrNull(p.Geo), textOrNull(p.ServiceTier))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// intOrNull is an optional count's JSON, null when absent.
func intOrNull(v *int64) string {
	if v == nil {
		return "null"
	}
	return strconv.FormatInt(*v, 10)
}

// textOrNull is an optional string's JSON, null when absent.
func textOrNull(v *string) string {
	if v == nil {
		return "null"
	}
	return jsfmt.String(*v).EncodeJSON()
}

// encodeSession writes a session entry as the JSON object it lands on disk as.
func encodeSession(names SessionNames) string {
	var out strings.Builder
	out.WriteString(`{"project":`)
	writeNullableText(&out, names.Project)
	out.WriteString(`,"title":`)
	writeNullableText(&out, names.Title)
	out.WriteString(`,"repo":`)
	writeNullableText(&out, names.Repo)
	out.WriteString(`,"parentSessionId":`)
	writeNullableText(&out, names.ParentSessionID)
	out.WriteString(`,"name":`)
	writeNullableText(&out, names.Name)
	out.WriteByte('}')
	return out.String()
}

// encodeAgent writes an agent entry as the JSON object it lands on disk as.
func encodeAgent(agent AgentNames) string {
	var out strings.Builder
	out.WriteString(`{"name":`)
	out.WriteString(jsfmt.String(agent.Name).EncodeJSON())
	out.WriteString(`,"type":`)
	writeNullableText(&out, agent.Type)
	out.WriteString(`,"description":`)
	writeNullableText(&out, agent.Description)
	out.WriteByte('}')
	return out.String()
}

// writeNullableText writes an optional string, null when absent.
func writeNullableText(out *strings.Builder, text *string) {
	if text == nil {
		out.WriteString("null")
		return
	}
	out.WriteString(jsfmt.String(*text).EncodeJSON())
}

// orderedMap is a JSON object read and written in key-insertion order: JavaScript objects
// keep it, Go maps do not. A key set again keeps its first position with the new value, the
// way a JavaScript object assignment behaves.
type orderedMap[V any] struct {
	order  []string
	values map[string]V
}

func (m *orderedMap[V]) set(key string, value V) {
	if _, ok := m.values[key]; !ok {
		m.order = append(m.order, key)
	}
	if m.values == nil {
		m.values = make(map[string]V)
	}
	m.values[key] = value
}

func (m *orderedMap[V]) get(key string) (value V, ok bool) {
	value, ok = m.values[key]
	return value, ok
}

func (m *orderedMap[V]) delete(key string) {
	if _, ok := m.values[key]; !ok {
		return
	}
	delete(m.values, key)
	if i := slices.Index(m.order, key); i >= 0 {
		m.order = slices.Delete(m.order, i, i+1)
	}
}

func (m *orderedMap[V]) keys() []string {
	return slices.Clone(m.order)
}

func (m *orderedMap[V]) len() int { return len(m.order) }

// newStringSet is the union of the key lists it is given.
func newStringSet(lists ...[]string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, list := range lists {
		for _, key := range list {
			set[key] = struct{}{}
		}
	}
	return set
}

// int64Ptr is the pointer a decoded count carries.
func int64Ptr(v int64) *int64 { return &v }

// int64Copy copies an optional count, nil staying nil.
func int64Copy(v *int64) *int64 {
	if v == nil {
		return nil
	}
	return int64Ptr(*v)
}

// textPtr is the pointer a decoded string carries.
func textPtr(text string) *string { return &text }

// textCopy copies an optional string, nil staying nil.
func textCopy(text *string) *string {
	if text == nil {
		return nil
	}
	return textPtr(*text)
}

// namesEqual compares two name sets field by field, nil against nil.
func namesEqual(a, b SessionNames) bool {
	return eqText(a.Project, b.Project) && eqText(a.Title, b.Title) && eqText(a.Repo, b.Repo) &&
		eqText(a.ParentSessionID, b.ParentSessionID) && eqText(a.Name, b.Name)
}

// agentsEqual compares two agent names field by field, nil against nil.
func agentsEqual(a, b AgentNames) bool {
	return a.Name == b.Name && eqText(a.Type, b.Type) && eqText(a.Description, b.Description)
}

// copySessionNames copies a name set so later edits of the caller's copy never reach the
// day file.
func copySessionNames(names SessionNames) SessionNames {
	return SessionNames{
		Project:         textCopy(names.Project),
		Title:           textCopy(names.Title),
		Repo:            textCopy(names.Repo),
		ParentSessionID: textCopy(names.ParentSessionID),
		Name:            textCopy(names.Name),
	}
}

// copyAgentNames copies agent names the same way.
func copyAgentNames(agent AgentNames) AgentNames {
	return AgentNames{Name: agent.Name, Type: textCopy(agent.Type), Description: textCopy(agent.Description)}
}

// eqInt64 compares two optional counts by value.
func eqInt64(a, b *int64) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

// eqText compares two optional strings by value.
func eqText(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

// equalPacked says whether two rows carry the same slots, the comparison a re-record skips:
// an older eight-slot row never equals a twelve-slot one, so re-recording it upgrades the
// day file.
func equalPacked(a, b packed) bool {
	if a.slots != b.slots || a.Ts != b.Ts || a.Model != b.Model || a.SessionID != b.SessionID ||
		a.AgentID != b.AgentID || a.Input != b.Input || a.Output != b.Output ||
		a.CacheRead != b.CacheRead || a.CacheWrite != b.CacheWrite {
		return false
	}
	return eqInt64(a.CacheWrite1h, b.CacheWrite1h) && eqText(a.Speed, b.Speed) &&
		eqText(a.Geo, b.Geo) && eqText(a.ServiceTier, b.ServiceTier)
}
