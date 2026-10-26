// Knowledge that lasts, on top of the channel's own store: a session or the owner vouches for an
// entry (verified entries come first in recall), every entry shows its age and is marked "may be
// stale" when it is old or its referenced files changed, remember finds an existing entry that
// says the same and offers to supersede it, an entry can be for every channel on this server (it
// lives in the shared store and every channel's recall finds it), and the export answers Markdown
// grouped by kind, to paste into a project's CLAUDE.md.
package channel

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

// SharedBase is where the shared store's ids start, so an id says which store holds it.
const SharedBase int64 = 100000

// KBShape is what recall and the dashboard show of an entry.
type KBShape struct {
	ID         int64    `json:"id"`
	Kind       string   `json:"kind"`
	Title      string   `json:"title"`
	By         string   `json:"by"`
	Tags       []string `json:"tags"`
	Refs       []string `json:"refs"`
	Task       *string  `json:"task"`
	CreatedAt  string   `json:"created_at"`
	Hit        string   `json:"hit"`
	Scope      string   `json:"scope"`
	Origin     *string  `json:"origin"`
	VerifiedBy *string  `json:"verified_by"`
	VerifiedAt *string  `json:"verified_at"`
	AgeDays    int64    `json:"age_days"`
	Stale      *string  `json:"stale"`
}

// ExistingEntry is the entry a duplicate answer names.
type ExistingEntry struct {
	ID    int64  `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	By    string `json:"by"`
	Scope string `json:"scope"`
}

// DuplicateResult is a remember that added nothing because an entry already says it.
type DuplicateResult struct {
	Duplicate bool          `json:"duplicate"`
	Added     bool          `json:"added"`
	Existing  ExistingEntry `json:"existing"`
	Hint      string        `json:"hint"`
}

// RememberShared is a server-wide entry as remember answers it.
type RememberShared struct {
	KBShape
	Body       string `json:"body"`
	Hits       int64  `json:"hits"`
	Supersedes *int64 `json:"supersedes"`
}

// ShareResult is a channel entry made one for every channel.
type ShareResult struct {
	KBShape
	Body string `json:"body"`
	From int64  `json:"from"`
}

// KBFull is one channel entry read in full, with where it moved when it was shared.
type KBFull struct {
	KBView
	Hit        string  `json:"hit"`
	Scope      string  `json:"scope"`
	Origin     *string `json:"origin"`
	VerifiedBy *string `json:"verified_by"`
	VerifiedAt *string `json:"verified_at"`
	AgeDays    int64   `json:"age_days"`
	Stale      *string `json:"stale"`
	MovedTo    *int64  `json:"moved_to"`
}

// KBShared is one server-wide entry read in full, with its superseding one.
type KBShared struct {
	KBShape
	Body         string `json:"body"`
	Hits         int64  `json:"hits"`
	Supersedes   *int64 `json:"supersedes"`
	SupersededBy *int64 `json:"superseded_by"`
	MovedTo      *int64 `json:"moved_to"`
}

// RememberArgs is one knowledge entry to add; Scope "server" writes it for every channel.
type RememberArgs struct {
	Kind       string
	Title      string
	Body       string
	Tags       []string
	Refs       []string
	Task       *string
	Supersedes *int64
	Scope      string
	Force      bool
}

// SimilarHit is the existing entry closest to a new one.
type SimilarHit struct {
	ID    int64
	Kind  string
	Title string
	By    string
	Scope string
	Score float64
}

// ExportOpts picks what the Markdown export carries.
type ExportOpts struct {
	Verified bool
}

// StaleDays is how long an entry goes without a verify before it may be stale:
// $HUDDLE_KB_STALE_DAYS when it names a positive number, else 30 days.
func StaleDays() float64 {
	if n, err := strconv.ParseFloat(os.Getenv("HUDDLE_KB_STALE_DAYS"), 64); err == nil && n > 0 {
		return n
	}
	return 30
}

// Shared is the server-wide knowledge store: the knowledge table of one SQLite file, written one
// call at a time. Its ids start at SharedBase.
type Shared struct {
	Store *store.Store
	FTS   bool

	mu   sync.Mutex // one write at a time
	dbMu sync.Mutex // one store call at a time; held across a whole transaction
}

// OpenShared opens the server-wide knowledge file, creating it when missing.
func OpenShared(ctx context.Context, file string) (*Shared, error) {
	st, err := store.Open(ctx, file)
	if err != nil {
		return nil, err
	}
	if err := st.Init(ctx); err != nil {
		_ = st.Close()
		return nil, err
	}
	err = st.Run(ctx, "INSERT INTO sqlite_sequence (name, seq) SELECT 'knowledge', ? WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name='knowledge')", SharedBase-1)
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	return &Shared{Store: st, FTS: st.FTS}, nil
}

// Serial runs fn as the store's only write.
func (s *Shared) Serial(ctx context.Context, fn func(context.Context) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fn(ctx)
}

// tx runs fn in one transaction; no read slips inside it.
func (s *Shared) tx(ctx context.Context, fn func(context.Context, store.Q) error) error {
	s.dbMu.Lock()
	defer s.dbMu.Unlock()
	return s.Store.Tx(ctx, fn)
}

func (s *Shared) all(ctx context.Context, query string, params ...any) ([]Row, error) {
	s.dbMu.Lock()
	defer s.dbMu.Unlock()
	return s.Store.All(ctx, query, params...)
}

func (s *Shared) get(ctx context.Context, query string, params ...any) (Row, error) {
	s.dbMu.Lock()
	defer s.dbMu.Unlock()
	return s.Store.Get(ctx, query, params...)
}

// Close ends the store's session.
func (s *Shared) Close() error { return s.Store.Close() }

// AttachShared hands a channel the server-wide knowledge store.
func AttachShared(c *Channel, s *Shared) {
	c.stateMu.Lock()
	c.shared = s
	c.stateMu.Unlock()
}

// SharedOf answers the channel's server-wide knowledge store, nil when there is none.
func SharedOf(c *Channel) *Shared {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.shared
}

func isShared(id int64) bool { return id >= SharedBase }

func needShared(c *Channel) (*Shared, error) {
	if s := SharedOf(c); s != nil {
		return s, nil
	}
	return nil, httpErr(400, "this server keeps no server-wide knowledge")
}

// sharedRetired lists the shared entries recall never shows: the ones another superseded.
func sharedRetired(ctx context.Context, s *Shared) (map[int64]bool, error) {
	rows, err := s.all(ctx, "SELECT supersedes AS id FROM knowledge WHERE supersedes IS NOT NULL")
	if err != nil {
		return nil, err
	}
	out := map[int64]bool{}
	for _, r := range rows {
		out[rInt(r, "id")] = true
	}
	return out, nil
}

// ── staleness ────────────────────────────────────────────────────────────────

var (
	urlRefRe = regexp.MustCompile(`(?i)^[a-z][\w+.-]*://`)
	seqRefRe = regexp.MustCompile(`^#?\d+$`)
	lineRe   = regexp.MustCompile(`:\d+(-\d+)?$`)
	refSpace = regexp.MustCompile(jsscalar.SpaceClass)
)

// fileRef reads one knowledge ref as a file path: empty for a url, an event seq or prose. A
// trailing line range comes off, and a leading ./; the whitespace test is JavaScript's set.
func fileRef(r string) string {
	trimmed := jsscalar.Trim(r)
	if urlRefRe.MatchString(r) || seqRefRe.MatchString(r) || refSpace.MatchString(trimmed) {
		return ""
	}
	out := strings.TrimPrefix(lineRe.ReplaceAllString(trimmed, ""), "./")
	return out
}

// parseMs reads one of the store's timestamps; ok is false for a value that is not a date.
func parseMs(s string) (int64, bool) {
	t, ok := jsscalar.ParseISO(s)
	if !ok {
		return 0, false
	}
	return t.UnixMilli(), true
}

// staleness answers an entry's age in days and why it may be stale: unverified for too long, a
// referenced file edited in this channel since, or a newer file on disk in the channel's repo.
func staleness(ctx context.Context, c *Channel, r Row) (int64, *string, error) {
	createdAt := rStr(r, "created_at")
	since := createdAt
	if v := rStr(r, "verified_at"); v != "" && v > since {
		since = v
	}
	nowMs := c.nowMs()
	days := int64(0)
	if created, ok := parseMs(createdAt); ok {
		days = int64(math.Floor(float64(nowMs-created) / 86400000))
	}
	if sinceMs, ok := parseMs(since); ok && float64(nowMs-sinceMs) > StaleDays()*86400000 {
		return days, sp(fmt.Sprintf("not verified for over %s days", jsscalar.JSNumber(StaleDays()))), nil
	}
	repo := deref(c.Config().Repo)
	for _, ref := range strsOf(rJSON(r, "refs")) {
		f := fileRef(ref)
		if f == "" {
			continue
		}
		at, err := LastEdit(ctx, c, f, since)
		if err != nil {
			return 0, nil, err
		}
		if at != nil {
			return days, sp(fmt.Sprintf("%s was edited since", f)), nil
		}
		file := f
		if !filepath.IsAbs(f) {
			if repo == "" {
				continue
			}
			file = repo + "/" + f
		}
		if st, err := os.Stat(file); err == nil {
			if sinceMs, ok := parseMs(since); ok && st.ModTime().UnixMilli() > sinceMs+1000 {
				return days, sp(fmt.Sprintf("%s changed since", f)), nil
			}
		}
	}
	return days, nil, nil
}

// kbShape is what recall and the dashboard show of an entry.
func kbShape(ctx context.Context, c *Channel, r Row) (KBShape, error) {
	days, stale, err := staleness(ctx, c, r)
	if err != nil {
		return KBShape{}, err
	}
	hit := ""
	if h, ok := r["hit"]; ok && h != nil {
		if s, isStr := h.(string); isStr {
			hit = s
		}
	} else {
		hit = jsscalar.UTF16Slice(rStr(r, "body"), 0, 200)
	}
	tags := strsOf(rJSON(r, "tags"))
	if tags == nil {
		tags = []string{}
	}
	refs := strsOf(rJSON(r, "refs"))
	if refs == nil {
		refs = []string{}
	}
	scope := "channel"
	if isShared(rInt(r, "id")) {
		scope = "server"
	}
	return KBShape{
		ID: rInt(r, "id"), Kind: rStr(r, "kind"), Title: rStr(r, "title"), By: rStr(r, "by"),
		Tags: tags, Refs: refs, Task: rStrPtr(r, "task_id"), CreatedAt: rStr(r, "created_at"),
		Hit: hit, Scope: scope, Origin: rStrPtr(r, "origin"), VerifiedBy: rStrPtr(r, "verified_by"),
		VerifiedAt: rStrPtr(r, "verified_at"), AgeDays: days, Stale: stale,
	}, nil
}

// ── recall: the channel's entries and the server-wide ones; verified first, then relevance ──

type recallRanked struct {
	row  Row
	rank float64
}

// Recall searches the channel's entries and the server-wide ones; verified entries come first,
// then relevance, and both lists interleave by position (each best first, or newest first without
// words).
func Recall(ctx context.Context, c *Channel, q string, o RecallOpts) ([]KBShape, error) {
	lim := o.Limit
	if lim == 0 {
		lim = 10
	}
	lim = max(1, min(50, lim))
	mine, err := c.Recall(ctx, q, RecallOpts{Kind: o.Kind, Tag: o.Tag, By: o.By, Limit: 50})
	if err != nil {
		return nil, err
	}
	list := []recallRanked{}
	for i, k := range mine {
		r, err := c.dbGet(ctx, "SELECT * FROM knowledge WHERE id=?", k.ID)
		if err != nil {
			return nil, err
		}
		if r == nil {
			continue
		}
		r["hit"] = k.Hit
		list = append(list, recallRanked{row: r, rank: float64(i)})
	}
	trimmed := jsscalar.Trim(q)
	if S := SharedOf(c); S != nil {
		gone, err := sharedRetired(ctx, S)
		if err != nil {
			return nil, err
		}
		var theirs []Row
		if trimmed != "" && S.FTS {
			S.dbMu.Lock()
			theirs = S.Store.SearchKnowledge(ctx, trimmed, 60)
			S.dbMu.Unlock()
		} else if trimmed != "" {
			var likes []string
			var params []any
			for i, w := range jsWords(trimmed) {
				if i >= 8 {
					break
				}
				likes = append(likes, "(title LIKE ? OR body LIKE ?)")
				params = append(params, "%"+w+"%", "%"+w+"%")
			}
			theirs, err = S.all(ctx, fmt.Sprintf(`SELECT *, substr(body,1,200) hit FROM knowledge WHERE %s LIMIT 60`, strings.Join(likes, " OR ")), params...)
			if err != nil {
				return nil, err
			}
		} else {
			theirs, err = S.all(ctx, "SELECT *, substr(body,1,200) hit FROM knowledge ORDER BY id DESC LIMIT 60")
			if err != nil {
				return nil, err
			}
		}
		for i, r := range theirs {
			if gone[rInt(r, "id")] {
				continue
			}
			if o.Kind != "" && rStr(r, "kind") != o.Kind {
				continue
			}
			by := rStr(r, "by")
			if o.By != "" && by != o.By && !strings.HasPrefix(by, o.By+".") {
				continue
			}
			if o.Tag != "" && !slices.Contains(strsOf(rJSON(r, "tags")), o.Tag) {
				continue
			}
			list = append(list, recallRanked{row: r, rank: float64(i) + 0.5})
		}
		if trimmed == "" {
			slices.SortStableFunc(list, func(a, b recallRanked) int {
				return jsscalar.LocaleCompare(rStr(b.row, "created_at"), rStr(a.row, "created_at"))
			})
		} else {
			slices.SortStableFunc(list, func(a, b recallRanked) int {
				return cmpF(a.rank, b.rank)
			})
		}
	}
	// stable: verified first, order kept within
	slices.SortStableFunc(list, func(a, b recallRanked) int {
		av, bv := 0, 0
		if rStr(a.row, "verified_at") != "" {
			av = 1
		}
		if rStr(b.row, "verified_at") != "" {
			bv = 1
		}
		return bv - av
	})
	if len(list) > lim {
		list = list[:lim]
	}
	out := []KBShape{}
	for _, x := range list {
		s, err := kbShape(ctx, c, x.row)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func cmpF(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// jsWords splits on runs of whitespace the way split(/\s+/) does, JavaScript's set.
func jsWords(s string) []string {
	var out []string
	start, inSpace := 0, false
	for i, r := range s {
		switch {
		case jsscalar.IsSpace(r):
			if !inSpace {
				out = append(out, s[start:i])
				inSpace = true
			}
		case inSpace:
			start, inSpace = i, false
		}
	}
	return append(out, s[start:])
}

// ── read one ─────────────────────────────────────────────────────────────────

// KB reads one knowledge entry in full, counting the read. A channel's entry answers with where it
// moved when it was made server-wide; a server-wide entry answers its superseding one.
func KB(ctx context.Context, c *Channel, id int64) (any, error) {
	if !isShared(id) {
		k, err := c.KB(ctx, id)
		if err != nil {
			return nil, err
		}
		r, err := c.dbGet(ctx, "SELECT * FROM knowledge WHERE id=?", id)
		if err != nil {
			return nil, err
		}
		if r == nil {
			return nil, httpErr(404, fmt.Sprintf("no knowledge entry %d", id))
		}
		shape, err := kbShape(ctx, c, r)
		if err != nil {
			return nil, err
		}
		return KBFull{
			KBView: k, Hit: shape.Hit, Scope: shape.Scope, Origin: shape.Origin,
			VerifiedBy: shape.VerifiedBy, VerifiedAt: shape.VerifiedAt, AgeDays: shape.AgeDays,
			Stale: shape.Stale, MovedTo: rIntPtr(r, "moved_to"),
		}, nil
	}
	S, err := needShared(c)
	if err != nil {
		return nil, err
	}
	var row Row
	err = S.Serial(ctx, func(ctx context.Context) error {
		row, err = S.get(ctx, "UPDATE knowledge SET hits = hits + 1 WHERE id=? RETURNING *", id)
		return err
	})
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, httpErr(404, fmt.Sprintf("no knowledge entry %d", id))
	}
	by, err := S.get(ctx, "SELECT id FROM knowledge WHERE supersedes=?", id)
	if err != nil {
		return nil, err
	}
	shape, err := kbShape(ctx, c, row)
	if err != nil {
		return nil, err
	}
	full := KBShared{
		KBShape: shape, Body: rStr(row, "body"), Hits: rInt(row, "hits"),
		Supersedes: rIntPtr(row, "supersedes"),
	}
	if by != nil {
		sup := rInt(by, "id")
		full.SupersededBy = &sup
	}
	return full, nil
}

// ── duplicates ───────────────────────────────────────────────────────────────

var simWordRe = regexp.MustCompile(`[\p{L}\p{N}_]{2,}`)

func simWords(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range simWordRe.FindAllString(strings.ToLower(s), -1) {
		out[w] = true
	}
	return out
}

// jaccard answers how much two word sets share.
func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	n := 0
	for w := range a {
		if b[w] {
			n++
		}
	}
	return float64(n) / float64(len(a)+len(b)-n)
}

// Similar answers the existing entry (this channel's or the server-wide one) that says what a new
// one would, when there is one.
func Similar(ctx context.Context, c *Channel, title, body string) (*SimilarHit, error) {
	t := simWords(title)
	b := simWords(body)
	look := func(read func(context.Context, string, ...any) ([]Row, error), gone map[int64]bool) ([]Row, error) {
		rows, err := read(ctx, "SELECT id, kind, title, body, by FROM knowledge ORDER BY id DESC LIMIT 500")
		if err != nil {
			return nil, err
		}
		out := []Row{}
		for _, r := range rows {
			if !gone[rInt(r, "id")] {
				out = append(out, r)
			}
		}
		return out, nil
	}
	gone, err := c.Retired(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := look(c.dbAll, gone)
	if err != nil {
		return nil, err
	}
	if S := SharedOf(c); S != nil {
		gone, err := sharedRetired(ctx, S)
		if err != nil {
			return nil, err
		}
		more, err := look(S.all, gone)
		if err != nil {
			return nil, err
		}
		rows = append(rows, more...)
	}
	var best *SimilarHit
	for _, r := range rows {
		ts := jaccard(t, simWords(rStr(r, "title")))
		bs := jaccard(b, simWords(rStr(r, "body")))
		dup := ts >= 0.75 || (ts >= 0.5 && bs >= 0.5) || bs >= 0.85
		score := max(ts, (ts+bs)/2, bs)
		if dup && (best == nil || score > best.Score) {
			scope := "channel"
			if isShared(rInt(r, "id")) {
				scope = "server"
			}
			best = &SimilarHit{
				ID: rInt(r, "id"), Kind: rStr(r, "kind"), Title: rStr(r, "title"),
				By: rStr(r, "by"), Scope: scope, Score: score,
			}
		}
	}
	return best, nil
}

// ── remember: duplicate check, then the channel's store or the server-wide one ──

// sharedEntry is one entry to write into the shared store.
type sharedEntry struct {
	by, kind, title, body  string
	tags, refs             []string
	supersedes             *int64
	origin, createdAt      string
	verifiedBy, verifiedAt *string
}

// insertShared writes one entry into the shared store and indexes it.
func insertShared(ctx context.Context, S *Shared, e sharedEntry) (Row, error) {
	var row Row
	err := S.Serial(ctx, func(ctx context.Context) error {
		return S.tx(ctx, func(ctx context.Context, q store.Q) error {
			r, err := q.Get(ctx, `INSERT INTO knowledge (by, kind, title, body, tags, refs, supersedes, created_at, origin, verified_by, verified_at) VALUES (?,?,?,?,?,?,?,?,?,?,?) RETURNING *`,
				e.by, e.kind, e.title, e.body, jstrs(e.tags).EncodeJSON(), jstrs(e.refs).EncodeJSON(),
				derefIntOrNil(e.supersedes), e.createdAt, e.origin, derefOrNil(e.verifiedBy), derefOrNil(e.verifiedAt))
			if err != nil {
				return err
			}
			row = r
			return S.Store.IndexKnowledge(ctx, q, r, e.tags)
		})
	})
	return row, err
}

// Remember adds knowledge: a duplicate check first (an entry that already says it is offered for
// supersede instead of a second copy), then the channel's store, or the shared one when the scope
// is the whole server.
func Remember(ctx context.Context, c *Channel, me string, k RememberArgs) (any, error) {
	if k.Scope != "" && k.Scope != "channel" && k.Scope != "server" {
		return nil, bad("scope: channel (default) or server (every channel on this server)")
	}
	if !k.Force && k.Supersedes == nil && jsscalar.Trim(k.Title) != "" && jsscalar.Trim(k.Body) != "" {
		d, err := Similar(ctx, c, k.Title, k.Body)
		if err != nil {
			return nil, err
		}
		if d != nil {
			return DuplicateResult{
				Duplicate: true, Added: false,
				Existing: ExistingEntry{ID: d.ID, Kind: d.Kind, Title: d.Title, By: d.By, Scope: d.Scope},
				Hint: fmt.Sprintf(`#%d [%s] "%s" (%s) already says this. To replace it, remember again with supersedes=%d; to add yours anyway, force=true; or verify #%d if it is still right.`,
					d.ID, d.Kind, d.Title, d.By, d.ID, d.ID),
			}, nil
		}
	}
	if k.Supersedes != nil && isShared(*k.Supersedes) != (k.Scope == "server") {
		side := "this channel's: supersede it without scope=server"
		if isShared(*k.Supersedes) {
			side = "server-wide: supersede it with scope=server"
		}
		return nil, bad(fmt.Sprintf("#%d is %s", *k.Supersedes, side))
	}
	if k.Scope != "server" {
		return c.Remember(ctx, me, RememberInput{
			Kind: k.Kind, Title: k.Title, Body: k.Body, Tags: k.Tags, Refs: k.Refs,
			Task: k.Task, Supersedes: k.Supersedes,
		})
	}
	S, err := needShared(c)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(KBKinds, k.Kind) {
		return nil, bad("kind must be one of " + strings.Join(KBKinds, ", "))
	}
	if jsscalar.Trim(k.Title) == "" || jsscalar.Trim(k.Body) == "" {
		return nil, bad("need title and body")
	}
	if jsscalar.UTF16Len(k.Body) > 50000 {
		return nil, bad("body ≤ 50000 chars; link the rest with refs")
	}
	if k.Supersedes != nil {
		r, err := S.get(ctx, "SELECT id FROM knowledge WHERE id=?", *k.Supersedes)
		if err != nil {
			return nil, err
		}
		if r == nil {
			return nil, httpErr(404, fmt.Sprintf("no knowledge entry %d", *k.Supersedes))
		}
	}
	if me != Owner {
		s, err := c.Session(ctx, me)
		if err != nil {
			return nil, err
		}
		if s == nil {
			return nil, httpErr(404, fmt.Sprintf("%q has not joined channel %s (call join first)", me, c.Name))
		}
	}
	tags := k.Tags
	if tags == nil {
		tags = []string{}
	}
	refs := k.Refs
	if refs == nil {
		refs = []string{}
	}
	row, err := insertShared(ctx, S, sharedEntry{
		by: me, kind: k.Kind, title: jsscalar.UTF16Slice(jsscalar.Trim(k.Title), 0, 200), body: jsscalar.Trim(k.Body),
		tags: tags, refs: refs, supersedes: k.Supersedes, origin: c.Name, createdAt: c.nowISO(),
	})
	if err != nil {
		return nil, err
	}
	if _, err := c.Append(ctx, me, evIn{
		topic: "kb.added", ref: sp(fmt.Sprintf("kb:%d", rInt(row, "id"))),
		msg: sp(fmt.Sprintf("[%s] %s (every channel)", k.Kind, rStr(row, "title"))),
		data: jsfmt.Object(
			[]string{"kb", "kind", "tags", "scope"},
			[]*jsfmt.Value{jnum(rInt(row, "id")), jstr(k.Kind), jstrs(tags), jstr("server")},
		),
	}); err != nil {
		return nil, err
	}
	shape, err := kbShape(ctx, c, row)
	if err != nil {
		return nil, err
	}
	return RememberShared{
		KBShape: shape, Body: rStr(row, "body"), Hits: 0, Supersedes: rIntPtr(row, "supersedes"),
	}, nil
}

// ── verify: anyone in the channel (or the owner) vouches for an entry, or takes that back ──

// Verify stamps an entry verified (or clears the stamp); verifying also clears "may be stale".
func Verify(ctx context.Context, c *Channel, me string, id int64, undo bool) (KBShape, error) {
	var row Row
	var err error
	if isShared(id) {
		var S *Shared
		S, err = needShared(c)
		if err != nil {
			return KBShape{}, err
		}
		err = S.Serial(ctx, func(ctx context.Context) error {
			row, err = S.get(ctx, `UPDATE knowledge SET verified_by=?, verified_at=? WHERE id=? RETURNING *`,
				derefOrNil(sp(me)), stampOf(c, undo), id)
			return err
		})
	} else {
		err = c.serial(ctx, func(ctx context.Context) error {
			row, err = c.dbGet(ctx, `UPDATE knowledge SET verified_by=?, verified_at=? WHERE id=? RETURNING *`,
				derefOrNil(sp(me)), stampOf(c, undo), id)
			return err
		})
	}
	if err != nil {
		return KBShape{}, err
	}
	if row == nil {
		return KBShape{}, httpErr(404, fmt.Sprintf("no knowledge entry %d", id))
	}
	verb := "verified"
	if undo {
		verb = "unverified"
	}
	if _, err := c.Append(ctx, me, evIn{
		topic: "kb.verified", ref: sp(fmt.Sprintf("kb:%d", id)),
		msg:  sp(fmt.Sprintf("%s [%s] %s", verb, rStr(row, "kind"), rStr(row, "title"))),
		data: jsfmt.Object([]string{"kb", "verified"}, []*jsfmt.Value{jnum(id), jbool(!undo)}),
	}); err != nil {
		return KBShape{}, err
	}
	return kbShape(ctx, c, row)
}

func stampOf(c *Channel, undo bool) any {
	if undo {
		return nil
	}
	return c.nowISO()
}

// ── share: a channel entry becomes one for every channel on this server ──

// Share copies one of this channel's entries into the shared store and points the channel's copy
// at it.
func Share(ctx context.Context, c *Channel, me string, id int64) (any, error) {
	if isShared(id) {
		return nil, bad(fmt.Sprintf("#%d is already for every channel", id))
	}
	S, err := needShared(c)
	if err != nil {
		return nil, err
	}
	r, err := c.dbGet(ctx, "SELECT * FROM knowledge WHERE id=?", id)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, httpErr(404, fmt.Sprintf("no knowledge entry %d", id))
	}
	if m := rIntPtr(r, "moved_to"); m != nil {
		return KB(ctx, c, *m)
	}
	retired, err := c.Retired(ctx)
	if err != nil {
		return nil, err
	}
	if retired[id] {
		return nil, bad(fmt.Sprintf("#%d was superseded: share the entry that replaced it", id))
	}
	tags := strsOf(rJSON(r, "tags"))
	if tags == nil {
		tags = []string{}
	}
	refs := strsOf(rJSON(r, "refs"))
	if refs == nil {
		refs = []string{}
	}
	row, err := insertShared(ctx, S, sharedEntry{
		by: rStr(r, "by"), kind: rStr(r, "kind"), title: rStr(r, "title"), body: rStr(r, "body"),
		tags: tags, refs: refs, origin: c.Name, createdAt: rStr(r, "created_at"),
		verifiedBy: rStrPtr(r, "verified_by"), verifiedAt: rStrPtr(r, "verified_at"),
	})
	if err != nil {
		return nil, err
	}
	err = c.serial(ctx, func(ctx context.Context) error {
		return c.dbRun(ctx, "UPDATE knowledge SET moved_to=? WHERE id=?", rInt(row, "id"), id)
	})
	if err != nil {
		return nil, err
	}
	if _, err := c.Append(ctx, me, evIn{
		topic: "kb.shared", ref: sp(fmt.Sprintf("kb:%d", rInt(row, "id"))),
		msg:  sp(fmt.Sprintf("[%s] %s is now for every channel (#%d → #%d)", rStr(r, "kind"), rStr(r, "title"), id, rInt(row, "id"))),
		data: jsfmt.Object([]string{"kb", "from"}, []*jsfmt.Value{jnum(rInt(row, "id")), jnum(id)}),
	}); err != nil {
		return nil, err
	}
	shape, err := kbShape(ctx, c, row)
	if err != nil {
		return nil, err
	}
	return ShareResult{KBShape: shape, Body: rStr(row, "body"), From: id}, nil
}

// ── export: Markdown for a project's CLAUDE.md, grouped by kind ──

var exportHeads = [][2]string{
	{"lesson", "Lessons"}, {"howto", "How-tos"}, {"fact", "Facts"},
	{"decision", "Decisions"}, {"context", "Context"}, {"result", "Results"},
}

var (
	starRe    = regexp.MustCompile(`\*`)
	blankRuns = regexp.MustCompile(`\n{3,}`)
)

// ExportMarkdown answers the channel's live knowledge as Markdown grouped by kind, verified
// entries only on request, the server-wide ones included.
func ExportMarkdown(ctx context.Context, c *Channel, o ExportOpts) (string, error) {
	live := func(read func(context.Context, string, ...any) ([]Row, error), gone map[int64]bool) ([]Row, error) {
		rows, err := read(ctx, "SELECT * FROM knowledge ORDER BY id")
		if err != nil {
			return nil, err
		}
		out := []Row{}
		for _, r := range rows {
			if gone[rInt(r, "id")] || (o.Verified && rStr(r, "verified_at") == "") {
				continue
			}
			out = append(out, r)
		}
		return out, nil
	}
	gone, err := c.Retired(ctx)
	if err != nil {
		return "", err
	}
	rows, err := live(c.dbAll, gone)
	if err != nil {
		return "", err
	}
	if S := SharedOf(c); S != nil {
		gone, err := sharedRetired(ctx, S)
		if err != nil {
			return "", err
		}
		more, err := live(S.all, gone)
		if err != nil {
			return "", err
		}
		rows = append(rows, more...)
	}
	only := ""
	if o.Verified {
		only = ", verified entries only"
	}
	noun := "ies"
	if len(rows) == 1 {
		noun = "y"
	}
	var L []string
	L = append(L,
		fmt.Sprintf(`## Team knowledge (Huddle channel "%s")`, c.Config().Title), "",
		fmt.Sprintf("<!-- exported from Huddle %s%s: %d entr%s -->", c.nowISO(), only, len(rows), noun), "")
	if len(rows) == 0 {
		nothing := "_Nothing remembered yet._"
		if o.Verified {
			nothing = "_Nothing verified yet._"
		}
		L = append(L, nothing, "")
	}
	for _, head := range exportHeads {
		var of []Row
		for _, r := range rows {
			if rStr(r, "kind") == head[0] {
				of = append(of, r)
			}
		}
		if len(of) == 0 {
			continue
		}
		L = append(L, "### "+head[1], "")
		for _, r := range of {
			var refs []string
			for _, x := range strsOf(rJSON(r, "refs")) {
				if x != "" {
					refs = append(refs, x)
				}
			}
			body := strings.Split(jsscalar.Trim(rStr(r, "body")), "\n")
			var tail []string
			if len(refs) > 0 {
				quoted := make([]string, len(refs))
				for i, x := range refs {
					quoted[i] = "`" + x + "`"
				}
				tail = append(tail, "see "+strings.Join(quoted, ", "))
			}
			if isShared(rInt(r, "id")) {
				tail = append(tail, "every channel")
			}
			line := fmt.Sprintf("- **%s**", starRe.ReplaceAllString(rStr(r, "title"), `\*`))
			if len(body) == 1 {
				line += ": " + body[0]
			}
			if t := strings.Join(tail, "; "); t != "" {
				line += " (" + t + ")"
			}
			L = append(L, line)
			if len(body) > 1 {
				for _, l := range body {
					if jsscalar.Trim(l) != "" {
						L = append(L, "  "+l)
					} else {
						L = append(L, "")
					}
				}
			}
		}
		L = append(L, "")
	}
	return blankRuns.ReplaceAllString(strings.Join(L, "\n"), "\n\n"), nil
}
