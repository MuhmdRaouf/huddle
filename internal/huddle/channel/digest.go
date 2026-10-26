// What happened in a channel since a moment, per session: tasks finished, notes written,
// knowledge added, approval requests; plus what is blocked now and the questions still open.
// Built from the stored events and tables only (no model calls); the estimated cost per session
// comes from Radar when it runs, else it is left out.
package channel

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/MuhmdRaouf/huddle/internal/huddle/outside"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

// digestMaxMs is the oldest a window may start: 90 days.
const digestMaxMs = int64(90 * 86400000)

// DigestDone is one task a session finished or skipped in the window.
type DigestDone struct {
	ID     string  `json:"id"`
	Title  *string `json:"title"`
	Status string  `json:"status"`
	Note   string  `json:"note"`
	At     string  `json:"at"`
}

// DigestKnowledge is one knowledge entry added in the window.
type DigestKnowledge struct {
	ID    int64   `json:"id"`
	Kind  *string `json:"kind"`
	Title *string `json:"title"`
	At    string  `json:"at"`
}

// DigestApproval is one approval request in the window.
type DigestApproval struct {
	Seq    int64    `json:"seq"`
	Labels []string `json:"labels"`
	At     string   `json:"at"`
}

// DigestNote is one review note written in the window.
type DigestNote struct {
	Task  string  `json:"task"`
	Title *string `json:"title"`
	Kind  string  `json:"kind"`
	Body  string  `json:"body"`
	At    string  `json:"at"`
}

// DigestQuestion is one question still open.
type DigestQuestion struct {
	Seq  int64   `json:"seq"`
	From string  `json:"from"`
	To   *string `json:"to"`
	Msg  string  `json:"msg"`
	At   string  `json:"at"`
	Task any     `json:"task"`
}

// DigestBlocked is one task blocked now.
type DigestBlocked struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Owner   *string  `json:"owner"`
	WaitsOn []string `json:"waits_on"`
	Note    string   `json:"note"`
}

// DigestSess is one session's part of the digest; a subagent's work counts for its parent.
type DigestSess struct {
	Name      string            `json:"name"`
	State     *string           `json:"state"`
	Role      *string           `json:"role"`
	Done      []DigestDone      `json:"done"`
	Notes     []DigestNote      `json:"notes"`
	Knowledge []DigestKnowledge `json:"knowledge"`
	Approvals []DigestApproval  `json:"approvals"`
	Asked     int64             `json:"asked"`
	Events    int64             `json:"events"`
	Cost      *float64          `json:"cost"`
}

// DigestTotals counts the window.
type DigestTotals struct {
	Done      int64 `json:"done"`
	Notes     int64 `json:"notes"`
	Knowledge int64 `json:"knowledge"`
	Events    int64 `json:"events"`
	Blocked   int64 `json:"blocked"`
	Questions int64 `json:"questions"`
	Approvals int64 `json:"approvals"`
}

// DigestCost is the estimated cost per session when Radar runs.
type DigestCost struct {
	Available bool     `json:"available"`
	Total     *float64 `json:"total"`
	Range     string   `json:"range"`
}

// DigestResult is the digest of a channel over a window.
type DigestResult struct {
	Channel   string           `json:"channel"`
	Title     string           `json:"title"`
	Since     string           `json:"since"`
	Until     string           `json:"until"`
	Totals    DigestTotals     `json:"totals"`
	Sessions  []DigestSess     `json:"sessions"`
	Blocked   []DigestBlocked  `json:"blocked"`
	Questions []DigestQuestion `json:"questions"`
	Cost      DigestCost       `json:"cost"`
}

// DigestOpts picks what the digest carries; SkipCost leaves the estimated costs out.
type DigestOpts struct {
	SkipCost bool
}

// sinceRe reads a relative window ("24h", "90m", "7d", "2w", bare seconds); the whitespace class
// is JavaScript's.
var sinceRe = regexp.MustCompile(`(?i)^(\d+(?:\.\d+)?)` + jsscalar.SpaceClass + `*([smhdw]?)$`)

// spaceRuns matches runs of whitespace the way /\s+/ does, JavaScript's set.
var spaceRuns = regexp.MustCompile(jsscalar.SpaceClass + `+`)

// ParseSince reads "24h", "90m", "7d", "2w", seconds, or an ISO date as epoch ms (default: 24
// hours ago), never older than 90 days.
func ParseSince(v string, nowMs int64) (int64, error) {
	s := jsscalar.Trim(v)
	if s == "" {
		return nowMs - 86400000, nil
	}
	if m := sinceRe.FindStringSubmatch(s); m != nil {
		if n, err := strconv.ParseFloat(m[1], 64); err == nil {
			unit := 1000.0
			switch strings.ToLower(m[2]) {
			case "m":
				unit = 60000
			case "h":
				unit = 3600000
			case "d":
				unit = 86400000
			case "w":
				unit = 604800000
			}
			ms := min(n*unit, float64(digestMaxMs))
			return nowMs - int64(ms), nil
		}
	}
	if t, ok := jsscalar.ParseISO(s); ok {
		return max(t.UnixMilli(), nowMs-digestMaxMs), nil
	}
	return 0, httpErr(400, fmt.Sprintf("since: 24h, 90m, 7d or an ISO date, not %s", s))
}

// jsAnyToString renders one untyped argument the way String(v ?? "") does.
func jsAnyToString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case *jsfmt.Value:
		if x == nil || x.Kind() == jsfmt.KindNull {
			return ""
		}
		if x.Kind() == jsfmt.KindString {
			return x.Text()
		}
		return x.EncodeJSON()
	default:
		return fmt.Sprintf("%v", v)
	}
}

// rangeFor names the window radar sums attribution over.
func rangeFor(ms int64) string {
	const day = int64(86400000)
	if ms <= int64(float64(day)*1.01) {
		return "day"
	}
	if ms <= int64(float64(7*day)*1.01) {
		return "week"
	}
	return "month"
}

// Digest builds what happened in a channel since a moment: per session (a subagent's work counts
// for its parent) the tasks finished, the notes written, the knowledge added and the approval
// requests; what is blocked now; and the questions still open. The estimated cost per session
// comes from radar when it runs, folded onto Huddle names through the session links.
func Digest(ctx context.Context, c *Channel, since any, o DigestOpts) (*DigestResult, error) {
	nowMs := c.nowMs()
	from, err := ParseSince(jsAnyToString(since), nowMs)
	if err != nil {
		return nil, err
	}
	at := isoOfMs(from)
	events, err := c.dbAll(ctx, "SELECT * FROM events WHERE ts >= ? ORDER BY seq LIMIT 50000", at)
	if err != nil {
		return nil, err
	}
	tasks, err := c.Tasks(ctx, TasksFilter{})
	if err != nil {
		return nil, err
	}
	notes, err := c.dbAll(ctx, "SELECT task_id, kind, body, by, created_at FROM notes WHERE created_at >= ? ORDER BY id", at)
	if err != nil {
		return nil, err
	}
	sessions, err := c.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	open, err := c.dbAll(ctx, `SELECT e.* FROM events e WHERE e.needs_reply = 1 AND NOT EXISTS (SELECT 1 FROM events r WHERE r.reply_to = e.seq) ORDER BY e.seq`)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*TaskListItem, len(tasks))
	for i := range tasks {
		byID[tasks[i].ID] = &tasks[i]
	}
	live := make(map[string]*SessionView, len(sessions))
	for i := range sessions {
		live[sessions[i].Name] = &sessions[i]
	}
	per := map[string]*DigestSess{}
	sess := func(n string) *DigestSess {
		top := n
		if parent, ok := ParentOf(n); ok {
			top = parent
		}
		if s, ok := per[top]; ok {
			return s
		}
		s := &DigestSess{
			Name: top, Done: []DigestDone{}, Notes: []DigestNote{},
			Knowledge: []DigestKnowledge{}, Approvals: []DigestApproval{},
		}
		if x := live[top]; x != nil {
			state := x.State
			s.State = &state
			s.Role = x.Role
		} else if top != Owner {
			left := "left"
			s.State = &left
		}
		per[top] = s
		return s
	}
	for _, s := range sessions {
		if s.Parent == nil && s.State != "left" {
			sess(s.Name)
		}
	}

	var kbIDs []int64
	type finishedTask struct {
		who    string
		id     string
		title  *string
		status string
		note   string
		at     string
		now    *string
	}
	finAt := map[string]int{}
	var finished []finishedTask
	for _, r := range events {
		e := toEv(r)
		s := sess(e.From)
		d := e.Data
		s.Events++
		status := fieldStr(d, "status")
		switch {
		case e.Topic == "task.status" && (status == "done" || status == "skipped"):
			key := deref(e.Ref)
			if key == "" {
				key = fieldStr(d, "task")
			}
			var title, now *string
			if t := byID[key]; t != nil {
				tt, nn := t.Title, t.Status
				title, now = &tt, &nn
			}
			entry := finishedTask{
				who: s.Name, id: key, title: title, status: status,
				note: jsscalar.UTF16Slice(fieldStr(d, "note"), 0, 300), at: e.Ts, now: now,
			}
			if i, ok := finAt[key]; ok {
				finished[i] = entry // the position of its first finish stays
			} else {
				finAt[key] = len(finished)
				finished = append(finished, entry)
			}
		case e.Topic == "kb.added" && dataPresent(d, "kb"):
			id := fieldNum(d, "kb")
			s.Knowledge = append(s.Knowledge, DigestKnowledge{ID: id, Kind: dataStrPtr(d, "kind"), At: e.Ts})
			kbIDs = append(kbIDs, id)
		case e.Topic == "approval.request":
			labels := []string{}
			if l := objField(d, "labels"); l != nil && l.Kind() == jsfmt.KindArray {
				labels = strsOf(l)
			}
			s.Approvals = append(s.Approvals, DigestApproval{Seq: e.Seq, Labels: labels, At: e.Ts})
		}
	}
	for _, f := range finished {
		if f.now == nil || *f.now == f.status {
			sess(f.who).Done = append(sess(f.who).Done, DigestDone{
				ID: f.id, Title: f.title, Status: f.status, Note: f.note, At: f.at,
			})
		}
	}
	if len(kbIDs) > 0 {
		params := make([]any, len(kbIDs))
		for i, id := range kbIDs {
			params[i] = id
		}
		rows, err := c.dbAll(ctx, fmt.Sprintf(`SELECT id, kind, title FROM knowledge WHERE id IN (%s)`,
			strings.TrimSuffix(strings.Repeat("?,", len(kbIDs)), ",")), params...)
		if err != nil {
			return nil, err
		}
		t := map[int64]Row{}
		for _, r := range rows {
			t[rInt(r, "id")] = r
		}
		for _, s := range per {
			for i := range s.Knowledge {
				if r, ok := t[s.Knowledge[i].ID]; ok {
					title, kind := rStr(r, "title"), rStr(r, "kind")
					s.Knowledge[i].Title = &title
					s.Knowledge[i].Kind = &kind
				}
			}
		}
	}
	for _, n := range notes {
		by := deref(rStrPtr(n, "by"))
		if by == "" {
			by = Owner
		}
		var title *string
		if t := byID[rStr(n, "task_id")]; t != nil {
			tt := t.Title
			title = &tt
		}
		sess(by).Notes = append(sess(by).Notes, DigestNote{
			Task: rStr(n, "task_id"), Title: title, Kind: rStr(n, "kind"),
			Body: jsscalar.UTF16Slice(rStr(n, "body"), 0, 300), At: rStr(n, "created_at"),
		})
	}

	questions := []DigestQuestion{}
	for _, r := range open {
		e := toEv(r)
		var task any
		if d := e.Data; d != nil && d.Kind() == jsfmt.KindObject {
			if t := objField(d, "task"); t != nil && t.Kind() != jsfmt.KindNull {
				if t.Kind() == jsfmt.KindString {
					task = t.Text()
				} else {
					task = t
				}
			}
		}
		questions = append(questions, DigestQuestion{
			Seq: e.Seq, From: e.From, To: e.To,
			Msg: jsscalar.UTF16Slice(deref(e.Msg), 0, 300), At: e.Ts, Task: task,
		})
	}
	for _, q := range questions {
		if top, ok := ParentOf(q.From); ok {
			if s := per[top]; s != nil {
				s.Asked++
			}
		} else if s := per[q.From]; s != nil {
			s.Asked++
		}
	}
	blocked := []DigestBlocked{}
	noteRows, err := c.dbAll(ctx, "SELECT id, status_note FROM tasks WHERE status='blocked'")
	if err != nil {
		return nil, err
	}
	notesOf := map[string]string{}
	for _, r := range noteRows {
		notesOf[rStr(r, "id")] = rStr(r, "status_note")
	}
	for i := range tasks {
		t := &tasks[i]
		if t.Status != "blocked" {
			continue
		}
		waits := t.BlockedBy
		if waits == nil {
			waits = []string{}
		}
		blocked = append(blocked, DigestBlocked{
			ID: t.ID, Title: t.Title, Owner: t.Owner, WaitsOn: waits, Note: notesOf[t.ID],
		})
	}

	// estimated cost per session: radar's per-Claude-session spend, folded onto Huddle names in
	// the order radar's rows name them, so the sums add up the same way
	cost := DigestCost{Available: false, Range: rangeFor(nowMs - from)}
	if !o.SkipCost && c.Radar != nil {
		if m, ok := c.Radar.CostBySession(ctx, outside.Range(cost.Range)); ok {
			L, err := Links(ctx, c)
			if err != nil {
				return nil, err
			}
			total := 0.0
			for _, cid := range m.Keys() {
				n, linked := L[cid]
				if !linked {
					continue
				}
				s := sess(n)
				if s.Cost == nil {
					cost0 := 0.0
					s.Cost = &cost0
				}
				*s.Cost += m.USD(cid)
				total += m.USD(cid)
			}
			cost.Available = true
			cost.Total = &total
		}
	}

	list := []*DigestSess{}
	for _, s := range per {
		costSet := s.Cost != nil && *s.Cost != 0
		if s.Events > 0 || len(s.Notes) > 0 || (s.State != nil && *s.State != "left") || costSet {
			list = append(list, s)
		}
	}
	ownerRank := func(s *DigestSess) int {
		if s.Name == Owner {
			return 1
		}
		return 0
	}
	slices.SortStableFunc(list, func(a, b *DigestSess) int {
		if d := ownerRank(a) - ownerRank(b); d != 0 {
			return d
		}
		if d := (len(b.Done) + len(b.Knowledge)) - (len(a.Done) + len(a.Knowledge)); d != 0 {
			return d
		}
		if d := int(b.Events - a.Events); d != 0 {
			return d
		}
		return jsscalar.LocaleCompare(a.Name, b.Name)
	})

	sessList := []DigestSess{}
	for _, s := range list {
		sessList = append(sessList, *s)
	}
	var done, approvals int64
	for _, s := range list {
		done += int64(len(s.Done))
		approvals += int64(len(s.Approvals))
	}
	return &DigestResult{
		Channel: c.Name, Title: c.Config().Title, Since: at, Until: isoOfMs(nowMs),
		Totals: DigestTotals{
			Done: done, Notes: int64(len(notes)), Knowledge: int64(len(kbIDs)),
			Events: int64(len(events)), Blocked: int64(len(blocked)),
			Questions: int64(len(questions)), Approvals: approvals,
		},
		Sessions: sessList, Blocked: blocked, Questions: questions, Cost: cost,
	}, nil
}

// dataPresent reports whether an event's data object carries a non-null field.
func dataPresent(v *jsfmt.Value, key string) bool {
	f := objField(v, key)
	return f != nil && f.Kind() != jsfmt.KindNull
}

// fieldStr reads one string field of an event's data object, empty when it is not one.
func fieldStr(v *jsfmt.Value, key string) string {
	return strOf(objField(v, key))
}

// fieldNum reads one number field of an event's data object as an integer, 0 when it is not one.
func fieldNum(v *jsfmt.Value, key string) int64 {
	f := objField(v, key)
	if f == nil || f.Kind() != jsfmt.KindNumber {
		return 0
	}
	n, err := strconv.ParseInt(f.EncodeJSON(), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// dataStrPtr reads one nullable string field of an event's data object.
func dataStrPtr(v *jsfmt.Value, key string) *string {
	return strPtrOf(objField(v, key))
}

// usd renders a cost the way the digest's text does.
func usd(n float64) string {
	switch {
	case n >= 100:
		return "$" + jsscalar.ToFixed(n, 0)
	case n >= 0.01:
		return "$" + jsscalar.ToFixed(n, 2)
	case n > 0:
		return "$" + jsscalar.ToFixed(n, 3)
	default:
		return "$0"
	}
}

// DigestText renders the digest for an agent's context: the channel's totals, each session's
// part, what is blocked now and the questions still open.
func DigestText(r *DigestResult) string {
	plural := func(n int64, word string) string {
		if n == 1 {
			return word
		}
		return word + "s"
	}
	t := r.Totals
	costPart := ""
	if r.Cost.Available {
		total := 0.0
		if r.Cost.Total != nil {
			total = *r.Cost.Total
		}
		costPart = fmt.Sprintf(" · est. cost %s (radar, this %s)", usd(total), r.Cost.Range)
	}
	L := []string{
		fmt.Sprintf("digest: %s · since %s", r.Title, r.Since),
		fmt.Sprintf("%d %s finished · %d knowledge · %d %s · %d blocked · %d open %s%s",
			t.Done, plural(t.Done, "task"), t.Knowledge, t.Notes, plural(t.Notes, "note"),
			t.Blocked, t.Questions, plural(t.Questions, "question"), costPart),
	}
	for _, s := range r.Sessions {
		name := s.Name
		if name == Owner {
			name = "owner (you)"
		}
		state := ""
		if s.State != nil {
			state = fmt.Sprintf(" (%s)", *s.State)
		}
		costPart := ""
		if s.Cost != nil {
			costPart = fmt.Sprintf(" · est. %s", usd(*s.Cost))
		}
		L = append(L, "", fmt.Sprintf("%s%s%s · %d %s", name, state, costPart, s.Events, plural(s.Events, "event")))
		for _, d := range s.Done {
			verb := "done"
			if d.Status == "skipped" {
				verb = "skipped"
			}
			title := ""
			if d.Title != nil && *d.Title != "" {
				title = " " + *d.Title
			}
			note := ""
			if d.Note != "" {
				note = " — " + jsscalar.UTF16Slice(d.Note, 0, 120)
			}
			L = append(L, fmt.Sprintf("  %s %s%s%s", verb, d.ID, title, note))
		}
		for _, k := range s.Knowledge {
			kind := "?"
			if k.Kind != nil {
				kind = *k.Kind
			}
			title := ""
			if k.Title != nil {
				title = *k.Title
			}
			L = append(L, fmt.Sprintf("  kb #%d [%s] %s", k.ID, kind, title))
		}
		for i, n := range s.Notes {
			if i >= 8 {
				break
			}
			L = append(L, fmt.Sprintf("  note on %s [%s] %s", n.Task, n.Kind,
				jsscalar.UTF16Slice(spaceRuns.ReplaceAllString(n.Body, " "), 0, 100)))
		}
		if len(s.Notes) > 8 {
			L = append(L, fmt.Sprintf("  … %d more notes", len(s.Notes)-8))
		}
		if len(s.Approvals) > 0 {
			seen := map[string]bool{}
			var labels []string
			for _, a := range s.Approvals {
				for _, l := range a.Labels {
					if !seen[l] {
						seen[l] = true
						labels = append(labels, l)
					}
				}
			}
			L = append(L, fmt.Sprintf("  asked permission %d×: %s", len(s.Approvals), strings.Join(labels, ", ")))
		}
		if len(s.Done) == 0 && len(s.Knowledge) == 0 && len(s.Notes) == 0 && len(s.Approvals) == 0 {
			L = append(L, "  nothing finished or shared")
		}
	}
	if len(r.Blocked) > 0 {
		L = append(L, "", "blocked now:")
		for _, b := range r.Blocked {
			owner := ""
			if b.Owner != nil && *b.Owner != "" {
				owner = " @" + *b.Owner
			}
			note := ""
			if b.Note != "" {
				note = " — " + jsscalar.UTF16Slice(b.Note, 0, 120)
			}
			waits := ""
			if len(b.WaitsOn) > 0 {
				waits = fmt.Sprintf(" (waits on %s)", strings.Join(b.WaitsOn, ", "))
			}
			L = append(L, fmt.Sprintf("  %s %s%s%s%s", b.ID, b.Title, owner, note, waits))
		}
	}
	if len(r.Questions) > 0 {
		L = append(L, "", "open questions:")
		for i, q := range r.Questions {
			if i >= 20 {
				break
			}
			to := "everyone"
			if q.To != nil {
				to = *q.To
			}
			L = append(L, fmt.Sprintf("  #%d %s → %s: %s", q.Seq, q.From, to,
				jsscalar.UTF16Slice(spaceRuns.ReplaceAllString(q.Msg, " "), 0, 140)))
		}
	}
	return strings.Join(L, "\n")
}
