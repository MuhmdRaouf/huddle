// Package channel holds one huddle channel: its own SQLite file, its sessions, their events, the
// turn, the owner's (and each other's) pause, messages that need a reply, and a plan of tasks that
// can depend on each other across sessions.
//
// Every write runs one at a time per channel (serial), each multi-statement write is one
// transaction, and every wake-up is in-process: a waiter registers first, then queries; any commit
// that could satisfy it pokes it to query again. Nothing polls, and no wake-up falls between a
// query and the registration.
//
// Visibility: a session sees an event when it did not send it and the event is addressed to it or
// to everyone (to NULL). Each session has one cursor over what it sees; Ack moves it.
package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/outside"
	"github.com/MuhmdRaouf/huddle/internal/huddle/store"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// Row is one result row of the channel's database.
type Row = store.Row

// HuddleError is an expected failure: an HTTP status and the message that goes with it.
type HuddleError struct {
	Status int
	Msg    string
}

func (e *HuddleError) Error() string { return e.Msg }

func bad(msg string) *HuddleError                 { return &HuddleError{Status: 400, Msg: msg} }
func httpErr(status int, msg string) *HuddleError { return &HuddleError{Status: status, Msg: msg} }

// Owner is the channel's owner, the one session that is always there and never paused.
const Owner = "owner"

const nameReSrc = `^[a-z][a-z0-9_-]{0,31}(\.[a-z0-9][a-z0-9_-]{0,31})?$`
const topicReSrc = `^[a-z][a-z0-9_.*-]{0,63}$`

// NameRe is a session's name: a session is "<name>"; a subagent working for it joins as
// "<name>.<role>" (one level).
var NameRe = regexp.MustCompile(nameReSrc)

// TopicRe is a topic's shape (globs may also carry *).
var TopicRe = regexp.MustCompile(topicReSrc)

var taskIDRe = regexp.MustCompile(`^[\w.-]{1,32}$`)
var reservedTopicRe = regexp.MustCompile(`^(control|reply|reserve|session|task)\.`)

var States = []string{"working", "waiting", "paused", "idle", "blocked", "left"}
var KBKinds = []string{"fact", "lesson", "decision", "context", "result", "howto"}
var Statuses = []string{"todo", "doing", "done", "blocked", "skipped"}
var NoteKinds = []string{"note", "change", "optimize", "enhance", "direction", "question"}
var Contexts = []string{"sync", "fresh"}

// JSONFields are the fields of a task body the plan view knows how to show; anything else is
// kept too.
var JSONFields = []string{"alternatives", "how", "snippets", "verify", "files", "refs"}

// TextFields are the text fields of a task body.
var TextFields = []string{"what", "why", "use", "value", "rollback", "notes", "kind", "gate", "risk"}

var editable = func() map[string]bool {
	m := map[string]bool{"title": true}
	for _, f := range JSONFields {
		m[f] = true
	}
	for _, f := range TextFields {
		m[f] = true
	}
	return m
}()

// PresenceStaleS is how many seconds without a seen write make a session stale.
const PresenceStaleS = 900

// stopWords are the words too common to say what a session's role or task is about.
var stopWords = wordsSet("the and for with from that this into what when then than have has are was were will not but you your our its any all each per via use uses using")

func wordsSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

func finished(status string) bool { return status == "done" || status == "skipped" }

// ParentOf answers the session a subagent name works for.
func ParentOf(n string) (string, bool) {
	if i := strings.IndexByte(n, '.'); i >= 0 {
		return n[:i], true
	}
	return "", false
}

// GlobMatch reports whether a topic matches any of the globs; no globs match everything.
func GlobMatch(topic string, globs []string) bool {
	if len(globs) == 0 {
		return true
	}
	for _, g := range globs {
		if globOne(topic, g) {
			return true
		}
	}
	return false
}

// globOne matches one glob: * stands for any run of characters, everything else for itself.
func globOne(s, p string) bool {
	parts := strings.Split(p, "*")
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for i := 1; i < len(parts); i++ {
		if i == len(parts)-1 {
			return strings.HasSuffix(s, parts[i])
		}
		j := strings.Index(s, parts[i])
		if j < 0 {
			return false
		}
		s = s[j+len(parts[i]):]
	}
	return true
}

const countInt = "CAST(COUNT(*) AS INTEGER)"

// ── values and rows ──────────────────────────────────────────────────────────

func rStr(r Row, k string) string {
	v, _ := r[k].(string)
	return v
}

func rStrPtr(r Row, k string) *string {
	if v, ok := r[k].(string); ok {
		return &v
	}
	return nil
}

func rInt(r Row, k string) int64 {
	switch v := r[k].(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	case int:
		return int64(v)
	}
	return 0
}

func rIntPtr(r Row, k string) *int64 {
	if r[k] == nil {
		return nil
	}
	v := rInt(r, k)
	return &v
}

func rBool(r Row, k string) bool { return rInt(r, k) != 0 }

// rJSON reads one JSON column; nil when it is NULL or not JSON.
func rJSON(r Row, k string) *jsfmt.Value {
	s, ok := r[k].(string)
	if !ok {
		return nil
	}
	v, err := jsfmt.DecodeValue([]byte(s))
	if err != nil {
		return nil
	}
	return v
}

func jstr(s string) *jsfmt.Value { return jsfmt.String(s) }
func jarrNull() *jsfmt.Value     { return jsfmt.Array(nil) }
func jnull() *jsfmt.Value        { v, _ := jsfmt.DecodeValue([]byte("null")); return v }
func jbool(b bool) *jsfmt.Value  { return decodeConst("true", "false", b) }
func jnum(i int64) *jsfmt.Value  { return decodeConst(strconv.FormatInt(i, 10), "0", true) }

func decodeConst(a, b string, first bool) *jsfmt.Value {
	s := b
	if first {
		s = a
	}
	v, _ := jsfmt.DecodeValue([]byte(s))
	return v
}

func jstrPtr(p *string) *jsfmt.Value {
	if p == nil {
		return jnull()
	}
	return jstr(*p)
}

func jstrs(items []string) *jsfmt.Value {
	out := make([]*jsfmt.Value, len(items))
	for i, s := range items {
		out[i] = jstr(s)
	}
	return jsfmt.Array(out)
}

// strOf reads a value as a string ("" for anything else).
func strOf(v *jsfmt.Value) string {
	if v == nil || v.Kind() != jsfmt.KindString {
		return ""
	}
	return v.Text()
}

// strPtrOf reads a value as a nullable string.
func strPtrOf(v *jsfmt.Value) *string {
	if v == nil || v.Kind() == jsfmt.KindNull {
		return nil
	}
	s := strOf(v)
	return &s
}

// objField reads one field of an object value; nil when it is not there.
func objField(v *jsfmt.Value, key string) *jsfmt.Value {
	if v == nil || v.Kind() != jsfmt.KindObject {
		return nil
	}
	for i, k := range v.Keys() {
		if k == key {
			return v.Fields()[i]
		}
	}
	return nil
}

// strsOf reads an array value as strings.
func strsOf(v *jsfmt.Value) []string {
	if v == nil || v.Kind() != jsfmt.KindArray {
		return nil
	}
	out := []string{}
	for _, item := range v.Items() {
		out = append(out, strOf(item))
	}
	return out
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// sp answers a pointer to its string, for the optional fields a call fills in place.
func sp(s string) *string { return &s }

// utf16Cut truncates a user-facing string the way the dashboard's text is cut: on UTF-16 units.
func utf16Cut(s string, n int) string { return jsfmt.UTF16Slice(s, 0, n) }

func utf16CutPtr(s *string, n int) *string {
	if s == nil {
		return nil
	}
	cut := utf16Cut(*s, n)
	return &cut
}

func toSession(r Row) *Session {
	return &Session{
		Name: rStr(r, "name"), Label: rStrPtr(r, "label"), Role: rStrPtr(r, "role"),
		JoinedAt: rStr(r, "joined_at"), LastSeen: rStr(r, "last_seen"), State: rStr(r, "state"),
		Task: rStrPtr(r, "task"), Step: rStrPtr(r, "step"), Control: rStr(r, "control"),
		ControlBy: rStrPtr(r, "control_by"), ControlAt: rStrPtr(r, "control_at"), Cursor: rInt(r, "cursor"),
		Parent: rStrPtr(r, "parent"), Context: rStrPtr(r, "context"), Brief: rStrPtr(r, "brief"),
	}
}

// ── the channel's shapes ─────────────────────────────────────────────────────

// Ev is one event on the channel's timeline. Duplicate marks the event a repeated key returned
// instead of inserting.
type Ev struct {
	Seq        int64        `json:"seq"`
	Ts         string       `json:"ts"`
	From       string       `json:"from"`
	To         *string      `json:"to"`
	Topic      string       `json:"topic"`
	Ref        *string      `json:"ref"`
	Msg        *string      `json:"msg"`
	Data       *jsfmt.Value `json:"data"`
	ReplyTo    *int64       `json:"reply_to"`
	NeedsReply bool         `json:"needs_reply"`
	Duplicate  bool         `json:"duplicate,omitempty"`
}

// Live is one live fan-out message to the UI and stdio bridges.
type Live struct {
	Ch   string `json:"ch"`
	Type string `json:"type"`
	Data any    `json:"data"`
	At   string `json:"at"`
}

// LiveFn is a live fan-out listener.
type LiveFn func(Live)

// listenerEntry is one subscribed listener, removable by its pointer.
type listenerEntry struct {
	fn LiveFn
}

// Session is one session's row: its presence, its pause state and its cursor.
type Session struct {
	Name      string  `json:"name"`
	Label     *string `json:"label"`
	Role      *string `json:"role"`
	JoinedAt  string  `json:"joined_at"`
	LastSeen  string  `json:"last_seen"`
	State     string  `json:"state"`
	Task      *string `json:"task"`
	Step      *string `json:"step"`
	Control   string  `json:"control"`
	ControlBy *string `json:"control_by"`
	ControlAt *string `json:"control_at"`
	Cursor    int64   `json:"cursor"`
	Parent    *string `json:"parent"`
	Context   *string `json:"context"`
	Brief     *string `json:"brief"`
}

// PresenceView is a session as the live views see it.
type PresenceView struct {
	Name         string  `json:"name"`
	Parent       *string `json:"parent"`
	Label        *string `json:"label"`
	Role         *string `json:"role"`
	State        string  `json:"state"`
	Task         *string `json:"task"`
	Step         *string `json:"step"`
	LastSeen     string  `json:"last_seen"`
	Stale        bool    `json:"stale"`
	Control      string  `json:"control"`
	ControlBy    *string `json:"control_by"`
	ControlAt    *string `json:"control_at"`
	Cursor       int64   `json:"cursor"`
	Context      *string `json:"context"`
	BriefWaiting bool    `json:"brief_waiting"`
}

// SessionView is one session in the whole-cast list.
type SessionView struct {
	PresenceView
	Unread    int64 `json:"unread"`
	Open      int64 `json:"open"`
	HoldsTurn bool  `json:"holds_turn"`
}

// TurnView is who may work now, and the event that gave them the turn.
type TurnView struct {
	Holder *string `json:"holder"`
	Since  *Ev     `json:"since"`
}

// Config is a channel's stored configuration.
type Config struct {
	Title        string       `json:"title"`
	Description  string       `json:"description"`
	Start        *string      `json:"start"`
	Handover     *jsfmt.Value `json:"handover"`
	Profile      *string      `json:"profile"`
	Repo         *string      `json:"repo"`
	CreatedAt    *string      `json:"created_at"`
	Members      []string     `json:"members"`
	Orchestrator *string      `json:"orchestrator"`
}

// ConfigureArgs changes a channel's configuration; a nil field leaves it as it is.
type ConfigureArgs struct {
	Title        *string
	Description  *string
	Start        *string
	Handover     *jsfmt.Value
	Profile      *string
	Repo         *string
	Members      *jsfmt.Value
	Orchestrator *string
}

// JoinOpts is how a session joins.
type JoinOpts struct {
	Label   *string
	Role    *string
	Task    *string
	Context *string
}

// PresenceInput sets a session's state, task and (when SetStep) its step.
type PresenceInput struct {
	State   *string
	Task    *string
	SetStep bool
	Step    *string
}

// StoredBrief is a brief kept for a session's next fresh join.
type StoredBrief struct {
	Msg string `json:"msg"`
	By  string `json:"by"`
	At  string `json:"at"`
}

// InboxItem is one message waiting for a reply.
type InboxItem struct {
	Seq  int64   `json:"seq"`
	From string  `json:"from"`
	Msg  string  `json:"msg"`
	Task *string `json:"task"`
}

// UnreadItem is one event a session has not acked, trimmed for the snapshot.
type UnreadItem struct {
	Seq   int64   `json:"seq"`
	From  string  `json:"from"`
	To    *string `json:"to"`
	Topic string  `json:"topic"`
	Msg   *string `json:"msg"`
}

// OtherSession is one other session in the join snapshot.
type OtherSession struct {
	Name    string  `json:"name"`
	Role    *string `json:"role"`
	State   string  `json:"state"`
	Task    *string `json:"task"`
	Stale   bool    `json:"stale"`
	Control string  `json:"control"`
}

// KnowledgeStat is the channel's knowledge in one line.
type KnowledgeStat struct {
	Total  int64    `json:"total"`
	Latest []string `json:"latest"`
}

// NextRef is a session's next task in the join snapshot.
type NextRef struct {
	ID    string     `json:"id"`
	Title string     `json:"title"`
	Ready bool       `json:"ready"`
	Unmet []UnmetDep `json:"unmet"`
}

// Brief is what a fresh join gets instead of the backlog: the channel's goal, the orchestrator's
// brief, the session's own open tasks with what they wait on, the knowledge nearest to its role
// and task, and every open ask.
type Brief struct {
	Title            string           `json:"title"`
	Goal             string           `json:"goal"`
	Orchestrator     *string          `json:"orchestrator"`
	FromOrchestrator *StoredBrief     `json:"from_orchestrator"`
	Tasks            []BriefTask      `json:"tasks"`
	Knowledge        []BriefKnowledge `json:"knowledge"`
	Asks             []InboxItem      `json:"asks"`
}

// BriefTask is one of the session's own open tasks in a brief.
type BriefTask struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Status  string   `json:"status"`
	Ready   bool     `json:"ready"`
	WaitsOn []string `json:"waits_on"`
}

// BriefKnowledge is one knowledge entry in a brief.
type BriefKnowledge struct {
	ID    int64  `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	By    string `json:"by"`
	Hits  int64  `json:"hits"`
	Hit   string `json:"hit"`
}

// Snapshot is what a session sees when it joins (or asks for its status).
type Snapshot struct {
	Channel      string         `json:"channel"`
	Me           string         `json:"me"`
	Control      string         `json:"control"`
	Turn         TurnView       `json:"turn"`
	Orchestrator *string        `json:"orchestrator"`
	Context      *string        `json:"context"`
	Skipped      int            `json:"skipped"`
	Brief        *Brief         `json:"brief"`
	Sessions     []OtherSession `json:"sessions"`
	Knowledge    KnowledgeStat  `json:"knowledge"`
	Unread       []UnreadItem   `json:"unread"`
	UnreadTotal  int            `json:"unread_total"`
	Inbox        []InboxItem    `json:"inbox"`
	Next         *NextRef       `json:"next"`
}

// SnapshotJoin fills the join-specific parts of a snapshot.
type SnapshotJoin struct {
	Context *string
	Skipped *int
	Brief   *Brief
}

// SkippedEvent is one event the waiter stepped over to reach its hit.
type SkippedEvent struct {
	Seq   int64   `json:"seq"`
	From  string  `json:"from"`
	To    *string `json:"to"`
	Topic string  `json:"topic"`
	Msg   *string `json:"msg"`
}

// WaitHit is what a wait woke up to: the event or message (spread into the fields), the task or
// reply it waited for, or a timeout or cancellation.
type WaitHit struct {
	Kind         string         `json:"kind"`
	Seq          int64          `json:"seq,omitempty"`
	Ts           string         `json:"ts,omitempty"`
	From         string         `json:"from,omitempty"`
	To           *string        `json:"to,omitempty"`
	Topic        string         `json:"topic,omitempty"`
	Ref          *string        `json:"ref,omitempty"`
	Msg          *string        `json:"msg,omitempty"`
	Data         *jsfmt.Value   `json:"data,omitempty"`
	ReplyTo      *int64         `json:"reply_to,omitempty"`
	NeedsReply   bool           `json:"needs_reply,omitempty"`
	More         int            `json:"more,omitempty"`
	Skipped      []SkippedEvent `json:"skipped,omitempty"`
	SkippedTotal int            `json:"skipped_total,omitempty"`
	Task         string         `json:"task,omitempty"`
	Status       string         `json:"status,omitempty"`
	By           *string        `json:"by,omitempty"`
	Note         *string        `json:"note,omitempty"`
	Control      string         `json:"control,omitempty"`
}

func hitFromEv(e Ev, kind string) WaitHit {
	return WaitHit{
		Kind: kind, Seq: e.Seq, Ts: e.Ts, From: e.From, To: e.To, Topic: e.Topic, Ref: e.Ref,
		Msg: e.Msg, Data: e.Data, ReplyTo: e.ReplyTo, NeedsReply: e.NeedsReply,
	}
}

// GateResult is the pause check's answer.
type GateResult struct {
	Control   string `json:"control"`
	Timeout   bool   `json:"timeout,omitempty"`
	Cancelled bool   `json:"cancelled,omitempty"`
}

// AckResult is the session's cursor after an ack.
type AckResult struct {
	Cursor int64 `json:"cursor"`
}

// PublishArgs is one published event.
type PublishArgs struct {
	Topic      string
	Msg        string
	To         *string
	Ref        *string
	Data       *jsfmt.Value
	NeedsReply bool
	Key        *string
}

// SendOpts is how a message is sent.
type SendOpts struct {
	Ask  bool
	Task *string
	Key  *string
}

// EventsFilter reads the channel history.
type EventsFilter struct {
	After  *int64
	Before *int64
	Limit  *int64
	Topic  string
	From   *string
}

// Note is a review note on a task. Resolved is the stored 0 or 1, the number every view of a
// note carries.
type Note struct {
	ID        int64   `json:"id"`
	TaskID    string  `json:"task_id"`
	Kind      string  `json:"kind"`
	Body      string  `json:"body"`
	By        *string `json:"by"`
	Resolved  int64   `json:"resolved"`
	CreatedAt string  `json:"created_at"`
}

// UnmetDep is one unfinished task a task waits on.
type UnmetDep struct {
	ID     string  `json:"id"`
	Owner  *string `json:"owner"`
	Status string  `json:"status"`
	Title  *string `json:"title"`
}

// TaskEdit is one owner edit of a task field: what it was, when, by whom.
type TaskEdit struct {
	Field    string       `json:"field"`
	Original *jsfmt.Value `json:"original"`
	At       string       `json:"at"`
	By       *string      `json:"by"`
}

// TaskView is a task as the views see it: Fields is the flattened body (body keys in stored
// order, the plan's own columns, the missing JSON fields, the edits applied), exactly the object
// a task renders as.
type TaskView struct {
	Fields    *jsfmt.Value `json:"-"`
	ID        string       `json:"-"`
	PhaseN    int64        `json:"-"`
	Ord       int64        `json:"-"`
	Title     string       `json:"-"`
	Owner     *string      `json:"-"`
	Status    string       `json:"-"`
	Depends   []string     `json:"-"`
	Edited    []TaskEdit   `json:"-"`
	Comments  []Note       `json:"comments"`
	NeededBy  []string     `json:"needed_by"`
	Unmet     []UnmetDep   `json:"unmet"`
	Ready     bool         `json:"ready"`
	OpenNotes []Note       `json:"open_notes"`
}

// MarshalJSON writes the task as the views see it: the flattened body (its own key order) with
// the review's five answers folded in at the end.
func (t *TaskView) MarshalJSON() ([]byte, error) {
	tail := struct {
		Comments  []Note     `json:"comments"`
		NeededBy  []string   `json:"needed_by"`
		Unmet     []UnmetDep `json:"unmet"`
		Ready     bool       `json:"ready"`
		OpenNotes []Note     `json:"open_notes"`
	}{t.Comments, t.NeededBy, t.Unmet, t.Ready, t.OpenNotes}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(tail); err != nil {
		return nil, err
	}
	encoded := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	if t.Fields == nil || t.Fields.Kind() != jsfmt.KindObject {
		return encoded, nil
	}
	body := []byte(t.Fields.EncodeJSON())
	return append(append(body[:len(body)-1], ','), encoded[1:]...), nil
}

// Text reads one text field of the flattened body ("" when it is not there).
func (t *TaskView) Text(field string) string { return strOf(objField(t.Fields, field)) }

// TextPtr reads one nullable text field of the flattened body.
func (t *TaskView) TextPtr(field string) *string { return strPtrOf(objField(t.Fields, field)) }

// JSON reads one structured field of the flattened body.
func (t *TaskView) JSON(field string) *jsfmt.Value { return objField(t.Fields, field) }

// TaskComments counts a task's notes.
type TaskComments struct {
	N     int64    `json:"n"`
	Open  int64    `json:"open"`
	Kinds []string `json:"kinds"`
}

// TaskListItem is one task in the plan list.
type TaskListItem struct {
	ID        string        `json:"id"`
	PhaseN    int64         `json:"phase_n"`
	Title     string        `json:"title"`
	Owner     *string       `json:"owner"`
	Kind      string        `json:"kind,omitempty"`
	Gate      string        `json:"gate,omitempty"`
	Risk      string        `json:"risk,omitempty"`
	Status    string        `json:"status"`
	Value     string        `json:"value,omitempty"`
	Depends   []string      `json:"depends"`
	StatusBy  *string       `json:"status_by"`
	BlockedBy []string      `json:"blocked_by"`
	Comments  *TaskComments `json:"comments"`
	Edited    bool          `json:"edited"`
}

// TasksFilter picks tasks for the list.
type TasksFilter struct {
	Owner  *string
	Status string
	Phase  *int64
}

// CreateTaskInput is a new task.
type CreateTaskInput struct {
	ID     string
	Title  string
	Owner  *string
	After  []string
	Phase  *int64
	Body   *jsfmt.Value
	Status string
}

// CreateTaskResult is the task and the event its creation put on the timeline.
type CreateTaskResult struct {
	Task  *TaskView `json:"task"`
	Event Ev        `json:"event"`
}

// UpdateTaskInput reassigns a task, changes its dependencies or edits a field (a field with a nil
// Value restores the plan's text).
type UpdateTaskInput struct {
	Owner *string
	After []string
	Field string
	Value *jsfmt.Value
}

// SetStatusResult confirms a status move.
type SetStatusResult struct {
	OK     bool   `json:"ok"`
	ID     string `json:"id"`
	Status string `json:"status"`
}

// AssignResult is how a session's next return joins.
type AssignResult struct {
	Session string  `json:"session"`
	Context *string `json:"context"`
	Task    *string `json:"task"`
}

// BriefResult confirms a stored brief.
type BriefResult struct {
	Session string `json:"session"`
	Stored  bool   `json:"stored"`
	Event   int64  `json:"event"`
}

// ControlResult is a pause or resume.
type ControlResult struct {
	Name    string `json:"name"`
	Control string `json:"control"`
	By      string `json:"by"`
}

// SearchHit is one task the search found, with a snippet.
type SearchHit struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Hit   string `json:"hit"`
}

// RememberInput is one knowledge entry.
type RememberInput struct {
	Kind       string
	Title      string
	Body       string
	Tags       []string
	Refs       []string
	Task       *string
	Supersedes *int64
}

// KBEntry is one knowledge entry as it is stored.
type KBEntry struct {
	ID         int64    `json:"id"`
	By         string   `json:"by"`
	Kind       string   `json:"kind"`
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	Tags       []string `json:"tags"`
	Refs       []string `json:"refs"`
	Task       *string  `json:"task"`
	Supersedes *int64   `json:"supersedes"`
	CreatedAt  string   `json:"created_at"`
	Hits       int64    `json:"hits"`
}

// KBView is one knowledge entry read in full.
type KBView struct {
	KBEntry
	SupersededBy *int64 `json:"superseded_by"`
}

// RecallOpts filters a recall.
type RecallOpts struct {
	Kind  string
	Tag   string
	By    string
	Limit int
}

// RecallItem is one knowledge entry a recall found: its title and a snippet.
type RecallItem struct {
	ID        int64    `json:"id"`
	Kind      string   `json:"kind"`
	Title     string   `json:"title"`
	By        string   `json:"by"`
	Tags      []string `json:"tags"`
	Task      *string  `json:"task"`
	CreatedAt string   `json:"created_at"`
	Hit       string   `json:"hit"`
}

// PlanStep is one step of an imported plan; Body is everything the plan carries besides the
// columns the channel owns.
type PlanStep struct {
	ID      string
	Title   string
	Status  string
	Depends []string
	Owner   *string
	Body    *jsfmt.Value
}

// PlanPhase is one phase of an imported plan.
type PlanPhase struct {
	N     *int64
	Title string
	Body  *jsfmt.Value
	Steps []PlanStep
}

// Plan is an imported plan: phases with their tasks.
type Plan struct {
	Phases []PlanPhase
}

// ImportOpts names the owner of plan steps that name none.
type ImportOpts struct {
	Owner *string
}

// MovedTask is a task whose title changed under owner state.
type MovedTask struct {
	ID  string  `json:"id"`
	Was *string `json:"was"`
	Now *string `json:"now"`
}

// ImportResult is what loading a plan did.
type ImportResult struct {
	Phases int         `json:"phases"`
	Tasks  int         `json:"tasks"`
	Moved  []MovedTask `json:"moved"`
	Errors []string    `json:"errors"`
}

// PhaseView is one phase of the plan: its number, its title, and the phase's own fields
// (summary, needs, make, value, …) flattened beside them.
type PhaseView struct {
	N     int64
	Title string
	Body  *jsfmt.Value
}

// MarshalJSON lays a phase out flat, its body's fields after n and title in their stored order.
func (p PhaseView) MarshalJSON() ([]byte, error) {
	keys := []string{"n", "title"}
	fields := []*jsfmt.Value{jsfmt.Number(float64(p.N)), jsfmt.String(p.Title)}
	if p.Body != nil && p.Body.Kind() == jsfmt.KindObject {
		keys = append(keys, p.Body.Keys()...)
		fields = append(fields, p.Body.Fields()...)
	}
	return []byte(jsfmt.Object(keys, fields).EncodeJSON()), nil
}

// EditNoteInput resolves, rewrites or removes a note.
type EditNoteInput struct {
	Resolved *bool
	Body     *string
	Remove   bool
}

// ReviewOpenNote is an unresolved note in the review.
type ReviewOpenNote struct {
	Note
	StepID string  `json:"step_id"`
	Title  *string `json:"title"`
}

// ReviewEditItem is one owner edit in the review.
type ReviewEditItem struct {
	StepID    string  `json:"step_id"`
	Field     string  `json:"field"`
	UpdatedAt string  `json:"updated_at"`
	By        *string `json:"by"`
	Title     *string `json:"title"`
}

// Review is everything the owner's review lists.
type Review struct {
	Open  []ReviewOpenNote `json:"open"`
	Edits []ReviewEditItem `json:"edits"`
	Moved *jsfmt.Value     `json:"moved"`
}

// AskItem is one ask waiting for the owner.
type AskItem struct {
	Seq  int64   `json:"seq"`
	From string  `json:"from"`
	Msg  string  `json:"msg"`
	Ts   string  `json:"ts"`
	Task *string `json:"task"`
	Ref  *string `json:"ref"`
}

// GateItem is one gated task waiting for the owner's go-ahead.
type GateItem struct {
	ID     string  `json:"id"`
	Title  string  `json:"title"`
	Gate   string  `json:"gate"`
	Owner  *string `json:"owner"`
	Status string  `json:"status"`
	PhaseN int64   `json:"phase_n"`
}

// BlockedItem is one blocked task.
type BlockedItem struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Owner   *string  `json:"owner"`
	Note    string   `json:"note"`
	WaitsOn []string `json:"waits_on"`
}

// PausedItem is one paused session.
type PausedItem struct {
	Name string  `json:"name"`
	By   *string `json:"by"`
	At   *string `json:"at"`
}

// StaleItem is one stale session.
type StaleItem struct {
	Name     string `json:"name"`
	LastSeen string `json:"last_seen"`
}

// NextUp is one session's next task in the attention view.
type NextUp struct {
	Session string    `json:"session"`
	Task    *NextTask `json:"task"`
}

// NextTask is the next task of one session in the attention view.
type NextTask struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	Ready bool     `json:"ready"`
	Unmet []string `json:"unmet"`
}

// Attention is everything that waits on the owner, in one read.
type Attention struct {
	Asks        []AskItem     `json:"asks"`
	Gates       []GateItem    `json:"gates"`
	Blocked     []BlockedItem `json:"blocked"`
	OpenNotes   int64         `json:"open_notes"`
	Paused      []PausedItem  `json:"paused"`
	Stale       []StaleItem   `json:"stale"`
	Next        []NextUp      `json:"next"`
	UnblockedBy int           `json:"unblocked_by"`
	Total       int           `json:"total"`
}

// ApproveResult confirms the owner's go-ahead.
type ApproveResult struct {
	OK    bool   `json:"ok"`
	ID    string `json:"id"`
	Event int64  `json:"event"`
}

// PhaseStat is one phase's progress in the map.
type PhaseStat struct {
	N     *int64 `json:"n"`
	Title string `json:"title"`
	Done  int    `json:"done"`
	Total int    `json:"total"`
}

// TaskRef names one task.
type TaskRef struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// TaskRefLite names one task and whether it may start.
type TaskRefLite struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Ready bool   `json:"ready"`
}

// MapSession is one session in the map.
type MapSession struct {
	Name    string       `json:"name"`
	State   string       `json:"state"`
	Current *TaskRef     `json:"current"`
	Next    *TaskRefLite `json:"next"`
}

// CriticalTask is one task on the critical path.
type CriticalTask struct {
	ID     string  `json:"id"`
	Title  string  `json:"title"`
	Owner  *string `json:"owner"`
	Status string  `json:"status"`
}

// MapView is the big picture in one read: each phase's progress, what every session does now and
// next, and the critical path.
type MapView struct {
	Title        string         `json:"title"`
	Orchestrator *string        `json:"orchestrator"`
	Done         int            `json:"done"`
	Total        int            `json:"total"`
	Phases       []PhaseStat    `json:"phases"`
	Sessions     []MapSession   `json:"sessions"`
	CriticalPath []CriticalTask `json:"critical_path"`
}

// Stats counts the channel in one read.
type Stats struct {
	Events    int64 `json:"events"`
	Tasks     int64 `json:"tasks"`
	Done      int64 `json:"done"`
	Sessions  int64 `json:"sessions"`
	Knowledge int64 `json:"knowledge"`
	OpenAsks  int64 `json:"open_asks"`
	Last      int64 `json:"last"`
}

// ── the channel ──────────────────────────────────────────────────────────────

// waiter is one registered wait: a poke re-runs its query, a stop releases it as cancelled.
type waiter struct {
	poke   chan struct{}
	stop   chan struct{}
	pokeIf func(Ev) bool
}

// Channel is one channel: its SQLite file, its waiters and its live listeners. The server process
// is the only writer of the file.
type Channel struct {
	Name  string
	Store *store.Store
	FTS   bool

	// Now is the clock every timestamp comes from; Rand feeds the occasional-cleanup roll.
	Now  func() time.Time
	Rand func() float64

	// Radar answers the estimated cost per session when the radar plugin runs; nil leaves the
	// digest's costs out.
	Radar *outside.Radar

	mu      sync.Mutex // one write at a time per channel
	dbMu    sync.Mutex // one store call at a time; held across a whole transaction
	stateMu sync.Mutex

	waiters   map[*waiter]struct{}
	listeners []*listenerEntry
	seen      map[string]int64   // last_seen writes, throttled
	metaC     map[string]string  // meta, read once: this process is the only writer
	metaP     map[string]*string // meta written by the open transaction
	pending   []Ev               // events of the open transaction, emitted once it commits
	linksMade bool               // the session-link table exists
	shared    *Shared            // the server-wide knowledge store, when this server keeps one
}

// heldKey carries the channels whose write lock the running call chain holds: a write called from
// a write runs inline.
type heldKey struct{}

type heldSet struct {
	chans []*Channel
}

func (h *heldSet) has(c *Channel) bool { return slices.Contains(h.chans, c) }

func withHeld(ctx context.Context, c *Channel) context.Context {
	prev, _ := ctx.Value(heldKey{}).(*heldSet)
	next := &heldSet{}
	if prev != nil {
		next.chans = append(next.chans, prev.chans...)
	}
	next.chans = append(next.chans, c)
	return context.WithValue(ctx, heldKey{}, next)
}

// Open opens one channel over its store, initialising the file and stamping its creation.
func Open(ctx context.Context, name string, st *store.Store) (*Channel, error) {
	if err := st.Init(ctx); err != nil {
		return nil, err
	}
	c := &Channel{
		Name: name, Store: st, FTS: st.FTS, Now: time.Now, Rand: rand.Float64,
		waiters: map[*waiter]struct{}{}, seen: map[string]int64{}, metaC: map[string]string{},
		metaP: map[string]*string{},
	}
	rows, err := c.dbAll(ctx, "SELECT key, value FROM meta")
	if err != nil {
		return nil, err
	}
	c.stateMu.Lock()
	for _, r := range rows {
		c.metaC[rStr(r, "key")] = rStr(r, "value")
	}
	c.stateMu.Unlock()
	if c.meta("created_at") == "" {
		created := c.nowISO()
		if err := c.serial(ctx, func(ctx context.Context) error {
			return c.setMeta(ctx, nil, "created_at", &created)
		}); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// checkpoint folds the WAL back into the database file, so the file a remove keeps is whole.
func (c *Channel) checkpoint(ctx context.Context) error {
	c.dbMu.Lock()
	defer c.dbMu.Unlock()
	_, err := c.Store.All(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	return err
}

// Close cancels every waiter, waits for the running write to end and closes the store.
func (c *Channel) Close(ctx context.Context) error {
	c.stateMu.Lock()
	for w := range c.waiters {
		select {
		case w.stop <- struct{}{}:
		default:
		}
	}
	c.stateMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Store.Close()
}

func (c *Channel) nowISO() string { return c.Now().UTC().Format("2006-01-02T15:04:05Z") }

func (c *Channel) nowMs() int64 { return c.Now().UnixMilli() }

// serial runs fn as the channel's only write; a write called from a write runs inline.
func (c *Channel) serial(ctx context.Context, fn func(context.Context) error) error {
	if held, _ := ctx.Value(heldKey{}).(*heldSet); held != nil && held.has(c) {
		return fn(ctx)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return fn(withHeld(ctx, c))
}

// tx runs fn in one transaction; meta and live events it produced take effect only when it
// commits. The transaction holds the database lock for its whole span, so no read slips inside it.
func (c *Channel) tx(ctx context.Context, fn func(context.Context, store.Q) error) error {
	c.dbMu.Lock()
	defer c.dbMu.Unlock()
	err := c.Store.Tx(ctx, fn)
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if err != nil {
		c.metaP = map[string]*string{}
		c.pending = nil
		return err
	}
	for k, v := range c.metaP {
		if v == nil {
			delete(c.metaC, k)
		} else {
			c.metaC[k] = *v
		}
	}
	c.metaP = map[string]*string{}
	return nil
}

// dbAll, dbGet and dbRun run one statement on the store, one at a time.
func (c *Channel) dbAll(ctx context.Context, query string, params ...any) ([]Row, error) {
	c.dbMu.Lock()
	defer c.dbMu.Unlock()
	return c.Store.All(ctx, query, params...)
}

func (c *Channel) dbGet(ctx context.Context, query string, params ...any) (Row, error) {
	c.dbMu.Lock()
	defer c.dbMu.Unlock()
	return c.Store.Get(ctx, query, params...)
}

func (c *Channel) dbRun(ctx context.Context, stmt string, params ...any) error {
	c.dbMu.Lock()
	defer c.dbMu.Unlock()
	return c.Store.Run(ctx, stmt, params...)
}

// ── meta and config ─────────────────────────────────────────────────────────

// meta reads one meta value; an empty string means it is not set.
func (c *Channel) meta(k string) string {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if v, ok := c.metaP[k]; ok {
		return deref(v)
	}
	return c.metaC[k]
}

// Meta answers one meta value; an empty string means it is not set.
func (c *Channel) Meta(k string) string { return c.meta(k) }

// SetMeta writes one meta value (nil deletes it) under the channel's write lock.
func (c *Channel) SetMeta(ctx context.Context, k string, v *string) error {
	return c.serial(ctx, func(ctx context.Context) error {
		return c.setMeta(ctx, nil, k, v)
	})
}

// Serial runs fn as the channel's only write.
func (c *Channel) Serial(ctx context.Context, fn func(context.Context) error) error {
	return c.serial(ctx, fn)
}

func (c *Channel) metaPtr(k string) *string {
	v := c.meta(k)
	if v == "" {
		return nil
	}
	return &v
}

func (c *Channel) metaJSON(k string) *jsfmt.Value {
	if v := rJSON(Row{"v": c.meta(k)}, "v"); v != nil {
		return v
	}
	return jsfmt.Object(nil, nil)
}

// setMeta writes one meta value (nil deletes it); q is the open transaction's Q, nil for a direct
// write. A transaction's meta lands in the read cache only when it commits.
func (c *Channel) setMeta(ctx context.Context, q store.Q, k string, v *string) error {
	var err error
	if v == nil {
		if q != nil {
			err = q.Run(ctx, "DELETE FROM meta WHERE key=?", k)
		} else {
			err = c.dbRun(ctx, "DELETE FROM meta WHERE key=?", k)
		}
	} else {
		stmt := "INSERT INTO meta (key, value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value"
		if q != nil {
			err = q.Run(ctx, stmt, k, *v)
		} else {
			err = c.dbRun(ctx, stmt, k, *v)
		}
	}
	if err != nil {
		return err
	}
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if q != nil {
		c.metaP[k] = v
	} else if v == nil {
		delete(c.metaC, k)
	} else {
		c.metaC[k] = *v
	}
	return nil
}

// Config is the channel's configuration as it stands.
func (c *Channel) Config() Config {
	// members is always an array: a channel that admits anyone holds [], not nothing
	members := strsOf(c.metaJSON("members"))
	if members == nil {
		members = []string{}
	}
	return Config{
		Title:        orDefault(c.meta("title"), c.Name),
		Description:  c.meta("description"),
		Start:        c.metaPtr("turn_start"),
		Handover:     c.metaJSON("handover"),
		Profile:      c.metaPtr("profile"),
		Repo:         c.metaPtr("repo"),
		CreatedAt:    c.metaPtr("created_at"),
		Members:      members,
		Orchestrator: c.metaPtr("orchestrator"),
	}
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// handoverTo answers the turn target for one sender's topics.
func (c *Channel) handoverTo(from string) (string, []string, bool) {
	h := c.Config().Handover
	if h == nil || h.Kind() != jsfmt.KindObject {
		return "", nil, false
	}
	for i, k := range h.Keys() {
		if k != from {
			continue
		}
		entry := h.Fields()[i]
		to := strOf(objField(entry, "to"))
		topics := strsOf(objField(entry, "topics"))
		return to, topics, true
	}
	return "", nil, false
}

// Configure validates and stores a configuration change.
func (c *Channel) Configure(ctx context.Context, a ConfigureArgs) (Config, error) {
	err := c.serial(ctx, func(ctx context.Context) error {
		if a.Start != nil && *a.Start != "" && !NameRe.MatchString(*a.Start) {
			return bad("bad start")
		}
		if a.Handover != nil {
			if a.Handover.Kind() == jsfmt.KindObject {
				for i, k := range a.Handover.Keys() {
					entry := a.Handover.Fields()[i]
					if !NameRe.MatchString(k) || !NameRe.MatchString(strOf(objField(entry, "to"))) ||
						objField(entry, "topics") == nil || objField(entry, "topics").Kind() != jsfmt.KindArray {
						return bad("handover: {sender: {to, topics[]}}")
					}
				}
			}
		}
		if a.Orchestrator != nil && *a.Orchestrator != "" &&
			(!NameRe.MatchString(*a.Orchestrator) || strings.Contains(*a.Orchestrator, ".")) {
			return bad("orchestrator: a session name (not a subagent)")
		}
		if a.Members != nil {
			for _, m := range strsOf(a.Members) {
				if !NameRe.MatchString(m) || strings.Contains(m, ".") {
					return bad("members: session names")
				}
			}
		}
		setStr := func(k string, v *string) error { return c.setMeta(ctx, nil, k, v) }
		setOrClear := func(k string, v *string) error {
			if v == nil || *v == "" {
				return c.setMeta(ctx, nil, k, nil)
			}
			return c.setMeta(ctx, nil, k, v)
		}
		if a.Title != nil {
			if err := setStr("title", sp(utf16Cut(*a.Title, 120))); err != nil {
				return err
			}
		}
		if a.Description != nil {
			if err := setStr("description", sp(utf16Cut(*a.Description, 2000))); err != nil {
				return err
			}
		}
		if a.Start != nil {
			if err := setOrClear("turn_start", a.Start); err != nil {
				return err
			}
		}
		if a.Handover != nil {
			v := a.Handover
			if v == nil || v.Kind() == jsfmt.KindNull {
				v = jsfmt.Object(nil, nil)
			}
			if err := setStr("handover", sp(v.EncodeJSON())); err != nil {
				return err
			}
		}
		if a.Profile != nil {
			if err := setOrClear("profile", a.Profile); err != nil {
				return err
			}
		}
		if a.Members != nil {
			v := a.Members
			if v == nil || v.Kind() == jsfmt.KindNull {
				v = jarrNull()
			}
			if err := setStr("members", sp(v.EncodeJSON())); err != nil {
				return err
			}
		}
		if a.Repo != nil {
			if err := setOrClear("repo", a.Repo); err != nil {
				return err
			}
		}
		if a.Orchestrator != nil {
			if err := setOrClear("orchestrator", a.Orchestrator); err != nil {
				return err
			}
		}
		c.emit("channel", c.Config())
		return nil
	})
	if err != nil {
		return Config{}, err
	}
	return c.Config(), nil
}

// ── live fan-out and waiters ─────────────────────────────────────────────────

// Subscribe adds a live listener; the answer cancels it.
func (c *Channel) Subscribe(f LiveFn) func() {
	entry := &listenerEntry{fn: f}
	c.stateMu.Lock()
	c.listeners = append(c.listeners, entry)
	c.stateMu.Unlock()
	return func() {
		c.stateMu.Lock()
		defer c.stateMu.Unlock()
		c.listeners = slices.DeleteFunc(c.listeners, func(e *listenerEntry) bool { return e == entry })
	}
}

// Emit fans one message out to every listener; a listener that panics costs nothing.
func (c *Channel) Emit(typ string, data any) {
	c.emit(typ, data)
}

func (c *Channel) emit(typ string, data any) {
	c.stateMu.Lock()
	listeners := slices.Clone(c.listeners)
	c.stateMu.Unlock()
	m := Live{Ch: c.Name, Type: typ, Data: data, At: jsfmt.ISOTime(c.Now())}
	for _, l := range listeners {
		func() {
			defer func() { _ = recover() }()
			l.fn(m)
		}()
	}
}

// wake pokes every waiter into querying again; the poke is a non-blocking send, so it never waits
// on the write that caused it.
// wake pokes every waiter the committed events could move: those without a filter (their answer
// reads more than events), and those whose filter says one of the events may satisfy their query.
// A waiter left unparked lost nothing: the same query would have missed, and the event that does
// satisfy it carries its own wake.
func (c *Channel) wake(ev []Ev) {
	c.stateMu.Lock()
	waiters := make([]*waiter, 0, len(c.waiters))
	for w := range c.waiters {
		waiters = append(waiters, w)
	}
	c.stateMu.Unlock()
	for _, w := range waiters {
		if w.pokeIf != nil {
			hit := false
			for _, e := range ev {
				if w.pokeIf(e) {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
		}
		select {
		case w.poke <- struct{}{}:
		default:
		}
	}
}

// block registers, then queries; a poke while a query runs makes it query once more. onMiss runs
// once, when the first query finds nothing. It answers when a query hits, the timeout passes, the
// context is cancelled or the channel closes.
func (c *Channel) block(check func() (*WaitHit, error), timeoutS int, cancel <-chan struct{}, onMiss func(),
	pokeIf func(Ev) bool) (WaitHit, error) {
	w := &waiter{poke: make(chan struct{}, 1), stop: make(chan struct{}, 1), pokeIf: pokeIf}
	c.stateMu.Lock()
	c.waiters[w] = struct{}{}
	c.stateMu.Unlock()
	defer func() {
		c.stateMu.Lock()
		delete(c.waiters, w)
		c.stateMu.Unlock()
	}()
	var timeout <-chan time.Time
	if timeoutS > 0 {
		timer := time.NewTimer(time.Duration(timeoutS) * time.Second)
		defer timer.Stop()
		timeout = timer.C
	}
	first := true
	for {
		h, err := check()
		if err != nil {
			return WaitHit{}, err
		}
		if h != nil {
			return *h, nil
		}
		if first {
			first = false
			if onMiss != nil {
				onMiss()
			}
		}
		select {
		case <-w.poke:
		case <-timeout:
			return WaitHit{Kind: "timeout"}, nil
		case <-cancel:
			return WaitHit{Kind: "cancelled"}, nil
		case <-w.stop:
			return WaitHit{Kind: "cancelled"}, nil
		}
	}
}

// sleep waits like block, marking the session's presence while it sleeps and restoring it after.
// The presence writes run on their own context: they are what the session is while it waits, not
// work the waiter's cancellation should stop.
func (c *Channel) sleep(ctx context.Context, name string, check func() (*WaitHit, error), timeoutS int,
	asleep func() PresenceInput, awake func(WaitHit) error, pokeIf func(Ev) bool) (WaitHit, error) {
	bctx := context.Background()
	h, err := c.block(check, timeoutS, ctx.Done(), func() {
		if _, err := c.Presence(bctx, name, asleep()); err != nil {
			// marking the wait is best effort: a failed mark never stops the wait
			_ = err
		}
	}, pokeIf)
	if err != nil {
		return h, err
	}
	if err := awake(h); err != nil {
		return h, err
	}
	return h, nil
}

// ── sessions ─────────────────────────────────────────────────────────────────

// Session reads one session's row; nil when it has not joined.
func (c *Channel) Session(ctx context.Context, name string) (*Session, error) {
	r, err := c.dbGet(ctx, "SELECT * FROM sessions WHERE name=?", name)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, nil
	}
	return toSession(r), nil
}

// need answers the session's row, the owner's implicit one included.
func (c *Channel) need(ctx context.Context, name string) (*Session, error) {
	if name == Owner {
		return &Session{Name: name, Control: "run", Cursor: 0}, nil
	}
	s, err := c.Session(ctx, name)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, httpErr(404, fmt.Sprintf("%q has not joined channel %s (call join first)", name, c.Name))
	}
	return s, nil
}

// touch marks the session seen, at most once every five seconds.
func (c *Channel) touch(ctx context.Context, name string) error {
	if name == Owner {
		return nil
	}
	c.stateMu.Lock()
	last := c.seen[name]
	throttle := c.nowMs()-last < 5000
	if !throttle {
		c.seen[name] = c.nowMs()
	}
	c.stateMu.Unlock()
	if throttle {
		return nil
	}
	return c.serial(ctx, func(ctx context.Context) error {
		return c.dbRun(ctx, "UPDATE sessions SET last_seen=? WHERE name=?", c.nowISO(), name)
	})
}

// Join adds (or resumes) a session in the channel. Context "sync" reads on from the session's
// cursor; "fresh" moves the cursor to the present and returns a brief instead (open asks are kept
// either way). The default is fresh for a subagent and for a first join; on a return, whatever the
// orchestrator assigned it, else sync.
func (c *Channel) Join(ctx context.Context, name string, o JoinOpts) (Snapshot, error) {
	if !NameRe.MatchString(name) || name == Owner || name == "all" {
		return Snapshot{}, bad(fmt.Sprintf("session name must match /%s/ and not be owner/all", nameReSrc))
	}
	if o.Context != nil && *o.Context != "" && !slices.Contains(Contexts, *o.Context) {
		return Snapshot{}, bad("context must be one of " + strings.Join(Contexts, ", "))
	}
	skipped := 0
	var brief *Brief
	var mode string
	err := c.serial(ctx, func(ctx context.Context) error {
		parent, isSub := ParentOf(name)
		if isSub {
			p, err := c.Session(ctx, parent)
			if err != nil {
				return err
			}
			if p == nil {
				return bad(fmt.Sprintf("%s must join before its subagent %s", parent, name))
			}
		}
		member := name
		if isSub {
			member = parent
		}
		if members := c.Config().Members; len(members) > 0 && !slices.Contains(members, member) {
			return httpErr(403, fmt.Sprintf("channel %s admits %s (the owner adds members)", c.Name, strings.Join(members, ", ")))
		}
		prev, err := c.Session(ctx, name)
		if err != nil {
			return err
		}
		mode = ""
		if o.Context != nil && *o.Context != "" {
			mode = *o.Context
		} else if isSub || prev == nil {
			mode = "fresh"
		} else if prev.Context != nil {
			mode = *prev.Context
		} else {
			mode = "sync"
		}
		t := c.nowISO()
		task := "joined"
		if o.Task != nil {
			task = *o.Task
		}
		var parentPtr *string
		if isSub {
			parentPtr = sp(parent)
		}
		if err := c.dbRun(ctx, `INSERT INTO sessions (name, label, role, joined_at, last_seen, state, task, parent) VALUES (?,?,?,?,?, 'working', ?, ?)
        ON CONFLICT(name) DO UPDATE SET label=COALESCE(excluded.label, sessions.label), role=COALESCE(excluded.role, sessions.role),
        last_seen=excluded.last_seen, state='working', task=excluded.task`,
			name, o.Label, o.Role, t, t, task, parentPtr); err != nil {
			return err
		}
		// what the session missed: skipped when it starts fresh, to catch up on when it syncs
		var cursor int64
		if prev != nil {
			cursor = prev.Cursor
		}
		r, err := c.dbGet(ctx, fmt.Sprintf("SELECT %s n FROM events WHERE seq > ? AND from_name != ?", countInt), cursor, name)
		if err != nil {
			return err
		}
		skipped = int(rInt(r, "n"))
		// every start is on the timeline, with how the session came in
		returning := prev != nil && prev.State != "left"
		cameAs := "joined"
		if returning {
			cameAs = "returned"
		}
		msg := fmt.Sprintf("%s %s", name, cameAs)
		if o.Role != nil && *o.Role != "" {
			msg += ": " + *o.Role
		}
		data := jsfmt.Object([]string{"context", "skipped", "returning"}, []*jsfmt.Value{jstr(mode), jnum(int64(skipped)), jbool(returning)})
		if _, err := c.append(ctx, name, evIn{topic: "session.joined", msg: sp(msg), data: data}); err != nil {
			return err
		}
		if mode == "fresh" {
			// the backlog is skipped, the cursor moves to the present, the stored brief is used once
			s2, err := c.Session(ctx, name)
			if err != nil {
				return err
			}
			last, err := c.LastSeq(ctx)
			if err != nil {
				return err
			}
			if err := c.dbRun(ctx, "UPDATE sessions SET cursor=?, brief=NULL WHERE name=?", last, name); err != nil {
				return err
			}
			role := o.Role
			if role == nil && s2 != nil {
				role = s2.Role
			}
			var stored *StoredBrief
			if s2 != nil && s2.Brief != nil {
				if v := rJSON(Row{"b": *s2.Brief}, "b"); v != nil && v.Kind() == jsfmt.KindObject {
					stored = &StoredBrief{
						Msg: strOf(objField(v, "msg")), By: strOf(objField(v, "by")), At: strOf(objField(v, "at")),
					}
				}
			}
			brief, err = c.briefFor(ctx, name, role, stored)
			if err != nil {
				return err
			}
		}
		c.emit("presence", c.presenceOf(ctx, name))
		return nil
	})
	if err != nil {
		return Snapshot{}, err
	}
	return c.Snapshot(ctx, name, SnapshotJoin{Context: sp(mode), Skipped: &skipped, Brief: brief})
}

// Leave records a session's departure; it stays in the history, out of the live views.
func (c *Channel) Leave(ctx context.Context, name string, summary string) (PresenceView, error) {
	var out PresenceView
	err := c.serial(ctx, func(ctx context.Context) error {
		if _, err := c.need(ctx, name); err != nil {
			return err
		}
		msg := name + " left"
		if summary != "" {
			msg = name + " left: " + summary
		}
		task := summary
		if task == "" {
			task = "left"
		}
		var err error
		out, err = c.depart(ctx, name, name, "session.left", msg, task)
		return err
	})
	return out, err
}

// depart writes one session's exit: the event, the left presence, the released reservations. It
// runs inside the caller's serial, so a leave and a removal share one body.
func (c *Channel) depart(ctx context.Context, by, name, topic, msg, task string) (PresenceView, error) {
	if _, err := c.append(ctx, by, evIn{topic: topic, msg: sp(msg)}); err != nil {
		return PresenceView{}, err
	}
	p, err := c.Presence(ctx, name, PresenceInput{State: sp("left"), Task: sp(task)})
	if err != nil {
		return PresenceView{}, err
	}
	c.releaseAll(ctx, name) // the leaver's reservations die with its presence
	return p, nil
}

// Remove takes a session, and its subagents with it, out of the live views on the owner's order;
// they stay in the history. A turn a removed session held lands with the owner.
func (c *Channel) Remove(ctx context.Context, by, target, why string) ([]PresenceView, error) {
	var out []PresenceView
	err := c.serial(ctx, func(ctx context.Context) error {
		if by != Owner {
			return httpErr(403, "only the owner removes a session")
		}
		if target == Owner {
			return bad("the owner cannot be removed")
		}
		s, err := c.need(ctx, target)
		if err != nil {
			return err
		}
		if s.State == "left" {
			return bad(fmt.Sprintf("%s already left", target))
		}
		subs, err := c.subagentsOf(ctx, target)
		if err != nil {
			return err
		}
		names := []string{target}
		for _, sub := range subs {
			names = append(names, sub.Name)
		}
		for _, name := range names {
			msg := name + " removed by the owner"
			if why != "" {
				msg = name + " removed by the owner: " + why
			}
			p, err := c.depart(ctx, by, name, "session.removed", msg, "removed by the owner")
			if err != nil {
				return err
			}
			out = append(out, p)
		}
		holder := c.metaPtr("turn_holder")
		if holder == nil {
			holder = c.Config().Start
		}
		if holder != nil && slices.Contains(names, *holder) {
			take := fmt.Sprintf("owner takes the turn: %s was removed", target)
			if _, err := c.append(ctx, Owner, evIn{topic: "turn.take", msg: sp(take)}); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// subagentsOf answers the live sessions whose parent is name, by name.
func (c *Channel) subagentsOf(ctx context.Context, name string) ([]*Session, error) {
	rows, err := c.dbAll(ctx, "SELECT * FROM sessions WHERE parent=? AND state != 'left' ORDER BY name", name)
	if err != nil {
		return nil, err
	}
	out := make([]*Session, 0, len(rows))
	for _, r := range rows {
		out = append(out, toSession(r))
	}
	return out, nil
}

// Presence sets a session's state, task and step.
func (c *Channel) Presence(ctx context.Context, name string, p PresenceInput) (PresenceView, error) {
	var out PresenceView
	err := c.serial(ctx, func(ctx context.Context) error {
		if _, err := c.need(ctx, name); err != nil {
			return err
		}
		if p.State != nil && *p.State != "" && !slices.Contains(States, *p.State) {
			return bad("state must be one of " + strings.Join(States, ", "))
		}
		stmt := "UPDATE sessions SET state=COALESCE(?, state), task=COALESCE(?, task), last_seen=?"
		params := []any{derefOrNil(p.State), derefOrNil(p.Task), c.nowISO()}
		if p.SetStep {
			stmt += ", step=?"
			params = append(params, derefOrNil(p.Step))
		}
		stmt += " WHERE name=?"
		params = append(params, name)
		if err := c.dbRun(ctx, stmt, params...); err != nil {
			return err
		}
		c.stateMu.Lock()
		c.seen[name] = c.nowMs()
		c.stateMu.Unlock()
		out = c.presenceOf(ctx, name)
		c.emit("presence", out)
		return nil
	})
	return out, err
}

func derefOrNil(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func (c *Channel) shape(s *Session) PresenceView {
	age := float64(0)
	if t, err := time.Parse("2006-01-02T15:04:05Z", s.LastSeen); err == nil {
		age = float64(c.nowMs()-t.UnixMilli()) / 1000
	}
	return PresenceView{
		Name: s.Name, Parent: s.Parent, Label: s.Label, Role: s.Role, State: s.State, Task: s.Task,
		Step: s.Step, LastSeen: s.LastSeen, Stale: age > PresenceStaleS, Control: s.Control,
		ControlBy: s.ControlBy, ControlAt: s.ControlAt, Cursor: s.Cursor, Context: s.Context,
		BriefWaiting: s.Brief != nil,
	}
}

// Assign sets how a session's next return joins (sync, fresh or the default), and can hand it a
// task.
func (c *Channel) Assign(ctx context.Context, by, session string, joinContext *string, task string) (AssignResult, error) {
	var out AssignResult
	err := c.serial(ctx, func(ctx context.Context) error {
		if _, err := c.need(ctx, session); err != nil {
			return err
		}
		if joinContext != nil && !slices.Contains(Contexts, *joinContext) {
			return bad("context must be one of " + strings.Join(Contexts, ", ") + " or null")
		}
		if task != "" {
			if _, err := c.UpdateTask(ctx, by, task, UpdateTaskInput{Owner: sp(session)}); err != nil {
				return err
			}
		}
		if err := c.dbRun(ctx, "UPDATE sessions SET context=? WHERE name=?", derefOrNil(joinContext), session); err != nil {
			return err
		}
		c.emit("presence", c.presenceOf(ctx, session))
		var taskPtr *string
		if task != "" {
			taskPtr = sp(task)
		}
		out = AssignResult{Session: session, Context: joinContext, Task: taskPtr}
		return nil
	})
	return out, err
}

// Brief stores a brief for a session's next fresh join (kept until then; a newer one replaces
// it), and sends it as a message now.
func (c *Channel) Brief(ctx context.Context, by, session, msg string) (BriefResult, error) {
	var out BriefResult
	err := c.serial(ctx, func(ctx context.Context) error {
		if _, err := c.need(ctx, session); err != nil {
			return err
		}
		if strings.TrimSpace(msg) == "" || jsfmt.UTF16Len(msg) > 20000 {
			return bad("need msg (≤ 20000 chars)")
		}
		trimmed := strings.TrimSpace(msg)
		stored := jsfmt.Object([]string{"msg", "by", "at"}, []*jsfmt.Value{jstr(trimmed), jstr(by), jstr(c.nowISO())})
		if err := c.dbRun(ctx, "UPDATE sessions SET brief=? WHERE name=?", stored.EncodeJSON(), session); err != nil {
			return err
		}
		ev, err := c.append(ctx, by, evIn{
			topic: "msg", to: sp(session), msg: sp(trimmed),
			data: jsfmt.Object([]string{"brief"}, []*jsfmt.Value{jbool(true)}),
		})
		if err != nil {
			return err
		}
		out = BriefResult{Session: session, Stored: true, Event: ev.Seq}
		return nil
	})
	return out, err
}

// briefFor builds what a fresh join gets instead of the backlog.
func (c *Channel) briefFor(ctx context.Context, name string, role *string, stored *StoredBrief) (*Brief, error) {
	cfg := c.Config()
	all, err := c.Tasks(ctx, TasksFilter{Owner: sp(name)})
	if err != nil {
		return nil, err
	}
	var mine []TaskListItem
	for _, t := range all {
		if !finished(t.Status) {
			mine = append(mine, t)
		}
	}
	n, done, err := c.Next(ctx, name)
	if err != nil {
		return nil, err
	}
	focusID := ""
	if !done {
		focusID = n.ID
	}
	for _, t := range mine {
		if t.Status == "doing" {
			focusID = t.ID
			break
		}
	}
	if done && len(mine) > 0 {
		focusID = mine[0].ID
	}
	focusTitle, focusWhat := "", ""
	if focusID != "" {
		full, err := c.Task(ctx, focusID)
		if err != nil {
			return nil, err
		}
		if full != nil {
			focusTitle = full.Title
			focusWhat = full.Text("what")
		}
	}
	roleStr := deref(role)
	knowledge, err := c.relevant(ctx, strings.Join([]string{roleStr, focusTitle, focusWhat}, " "), 5)
	if err != nil {
		return nil, err
	}
	asks := []InboxItem{}
	ib, err := c.Inbox(ctx, name)
	if err != nil {
		return nil, err
	}
	for _, e := range ib {
		var task *string
		if t := strPtrOf(objField(e.Data, "task")); t != nil {
			task = t
		}
		asks = append(asks, InboxItem{Seq: e.Seq, From: e.From, Msg: deref(e.Msg), Task: task})
	}
	tasks := []BriefTask{}
	for _, t := range mine {
		tasks = append(tasks, BriefTask{ID: t.ID, Title: t.Title, Status: t.Status, Ready: len(t.BlockedBy) == 0, WaitsOn: t.BlockedBy})
	}
	return &Brief{
		Title: cfg.Title, Goal: cfg.Description, Orchestrator: cfg.Orchestrator,
		FromOrchestrator: stored, Tasks: tasks, Knowledge: knowledge, Asks: asks,
	}, nil
}

// Retired lists the knowledge entries recall never shows: superseded ones, and ones moved to the
// server-wide store.
func (c *Channel) Retired(ctx context.Context) (map[int64]bool, error) {
	rows, err := c.dbAll(ctx, "SELECT supersedes AS id FROM knowledge WHERE supersedes IS NOT NULL UNION SELECT id FROM knowledge WHERE moved_to IS NOT NULL")
	if err != nil {
		return nil, err
	}
	out := map[int64]bool{}
	for _, r := range rows {
		out[rInt(r, "id")] = true
	}
	return out, nil
}

var wordRe = regexp.MustCompile(`[\p{L}\p{N}_]{3,}`)

// relevant answers the knowledge entries nearest to some text: full-text matches best first (most
// hits on ties), then the most-read entries; superseded entries never.
func (c *Channel) relevant(ctx context.Context, text string, k int) ([]BriefKnowledge, error) {
	seenWords := map[string]bool{}
	var words []string
	for _, w := range wordRe.FindAllString(strings.ToLower(text), -1) {
		if !seenWords[w] && !stopWords[w] {
			seenWords[w] = true
			words = append(words, w)
		}
	}
	if len(words) > 16 {
		words = words[:16]
	}
	retired, err := c.Retired(ctx)
	if err != nil {
		return nil, err
	}
	type scored struct {
		id    int64
		kind  string
		title string
		by    string
		hits  int64
		hit   string
		score float64
	}
	var hits []scored
	if len(words) > 0 && c.FTS {
		c.dbMu.Lock()
		rows := c.Store.SearchKnowledge(ctx, strings.Join(words, " "), k*4)
		c.dbMu.Unlock()
		for _, r := range rows {
			if !retired[rInt(r, "id")] {
				hits = append(hits, scored{
					id: rInt(r, "id"), kind: rStr(r, "kind"), title: rStr(r, "title"), by: rStr(r, "by"),
					hits: rInt(r, "hits"), hit: rStr(r, "hit"), score: float64(rInt(r, "score")),
				})
			}
		}
		sort.SliceStable(hits, func(i, j int) bool {
			d := math.Round((hits[j].score - hits[i].score) * 1e6)
			if d != 0 {
				return d < 0
			}
			return hits[j].hits < hits[i].hits
		})
	}
	outIDs := map[int64]bool{}
	out := []BriefKnowledge{}
	for i := 0; i < len(hits) && i < k; i++ {
		out = append(out, BriefKnowledge{ID: hits[i].id, Kind: hits[i].kind, Title: hits[i].title, By: hits[i].by, Hits: hits[i].hits, Hit: hits[i].hit})
		outIDs[hits[i].id] = true
	}
	if len(out) < k {
		rows, err := c.dbAll(ctx, "SELECT *, substr(body,1,200) hit FROM knowledge ORDER BY hits DESC, id DESC LIMIT ?", k*4)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if len(out) >= k {
				break
			}
			id := rInt(r, "id")
			if retired[id] || outIDs[id] {
				continue
			}
			out = append(out, BriefKnowledge{ID: id, Kind: rStr(r, "kind"), Title: rStr(r, "title"), By: rStr(r, "by"), Hits: rInt(r, "hits"), Hit: rStr(r, "hit")})
			outIDs[id] = true
		}
	}
	return out, nil
}

func (c *Channel) presenceOf(ctx context.Context, name string) PresenceView {
	s, err := c.Session(ctx, name)
	if err != nil || s == nil {
		return PresenceView{}
	}
	return c.shape(s)
}

// PresenceOf answers one session's presence, nil when it has not joined.
func (c *Channel) PresenceOf(ctx context.Context, name string) (*PresenceView, error) {
	s, err := c.Session(ctx, name)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, nil
	}
	p := c.shape(s)
	return &p, nil
}

// openAsk is the query for messages that wait for a session's reply: asks, and owner directives.
const openAsk = `e.needs_reply = 1 AND e.from_name != ? AND (e.to_name IS NULL OR e.to_name = ?)
    AND NOT EXISTS (SELECT 1 FROM events r WHERE r.reply_to = e.seq AND r.from_name = ?)`

// Sessions lists every session with its unread counts.
func (c *Channel) Sessions(ctx context.Context) ([]SessionView, error) {
	turn, err := c.Turn(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := c.dbAll(ctx, "SELECT * FROM sessions ORDER BY joined_at, name")
	if err != nil {
		return nil, err
	}
	out := []SessionView{}
	for _, r := range rows {
		s := toSession(r)
		shape := c.shape(s)
		u, err := c.dbGet(ctx, fmt.Sprintf("SELECT %s n FROM events WHERE seq > ? AND from_name != ?", countInt), s.Cursor, s.Name)
		if err != nil {
			return nil, err
		}
		o, err := c.dbGet(ctx, fmt.Sprintf("SELECT %s n FROM events e WHERE %s", countInt, openAsk), s.Name, s.Name, s.Name)
		if err != nil {
			return nil, err
		}
		holds := turn.Holder != nil && *turn.Holder == s.Name
		out = append(out, SessionView{PresenceView: shape, Unread: rInt(u, "n"), Open: rInt(o, "n"), HoldsTurn: holds})
	}
	return out, nil
}

// ── control: the owner, or any session in the channel, pauses and resumes a session ──

// Control pauses or resumes a session.
func (c *Channel) Control(ctx context.Context, by, target, action, why string) (ControlResult, error) {
	var out ControlResult
	err := c.serial(ctx, func(ctx context.Context) error {
		if _, err := c.need(ctx, by); err != nil {
			return err
		}
		if _, err := c.need(ctx, target); err != nil {
			return err
		}
		if target == Owner {
			return bad("the owner cannot be paused")
		}
		st := "run"
		if action == "pause" {
			st = "pause"
		}
		err := c.tx(ctx, func(ctx context.Context, q store.Q) error {
			if err := q.Run(ctx, "UPDATE sessions SET control=?, control_by=?, control_at=? WHERE name=?", st, by, c.nowISO(), target); err != nil {
				return err
			}
			msg := why
			if msg == "" {
				msg = fmt.Sprintf("%s %sd %s", by, action, target)
			}
			_, err := c.insert(ctx, q, by, evIn{topic: "control." + action, to: sp(target), msg: sp(msg)})
			return err
		})
		if err != nil {
			return err
		}
		c.emit("presence", c.presenceOf(ctx, target))
		c.flush()
		c.wake(nil)
		out = ControlResult{Name: target, Control: st, By: by}
		return nil
	})
	return out, err
}

// ControlOf answers a session's pause state.
func (c *Channel) ControlOf(ctx context.Context, name string) (string, error) {
	if name == Owner {
		return "run", nil
	}
	s, err := c.need(ctx, name)
	if err != nil {
		return "", err
	}
	return s.Control, nil
}

func (c *Channel) mustRun(s *Session, what string) error {
	if s.Control == "pause" {
		return httpErr(423, fmt.Sprintf("%s is paused by %s since %s; %s refused. Call gate to wait for resume.",
			s.Name, deref(s.ControlBy), deref(s.ControlAt), what))
	}
	return nil
}

// Gate checks the pause; with a timeout it waits for the resume.
func (c *Channel) Gate(ctx context.Context, name string, timeoutS int) (GateResult, error) {
	if _, err := c.need(ctx, name); err != nil {
		return GateResult{}, err
	}
	bctx := context.Background()
	check := func() (*WaitHit, error) {
		st, err := c.ControlOf(bctx, name)
		if err != nil {
			return nil, err
		}
		if st == "run" {
			return &WaitHit{Kind: "event", Control: "run"}, nil
		}
		return nil, nil
	}
	h, err := c.sleep(ctx, name, check, timeoutS,
		func() PresenceInput {
			by := ""
			if s, _ := c.Session(bctx, name); s != nil {
				by = deref(s.ControlBy)
			}
			return PresenceInput{State: sp("paused"), Task: sp("paused by " + by)}
		},
		func(h WaitHit) error {
			if h.Kind != "event" {
				return nil
			}
			if s, _ := c.Session(bctx, name); s != nil && s.State == "paused" {
				if _, err := c.Presence(bctx, name, PresenceInput{State: sp("working"), Task: sp("resumed")}); err != nil {
					return err
				}
			}
			return nil
		}, nil)
	if err != nil {
		return GateResult{}, err
	}
	if h.Kind == "event" {
		return GateResult{Control: "run"}, nil
	}
	st, err := c.ControlOf(ctx, name)
	if err != nil {
		return GateResult{}, err
	}
	return GateResult{Control: st, Timeout: h.Kind == "timeout", Cancelled: h.Kind == "cancelled"}, nil
}

// ── events ───────────────────────────────────────────────────────────────────

func toEv(r Row) Ev {
	return Ev{
		Seq: rInt(r, "seq"), Ts: rStr(r, "ts"), From: rStr(r, "from_name"), To: rStrPtr(r, "to_name"),
		Topic: rStr(r, "topic"), Ref: rStrPtr(r, "ref"), Msg: rStrPtr(r, "msg"), Data: rJSON(r, "data"),
		ReplyTo: rIntPtr(r, "reply_to"), NeedsReply: rBool(r, "needs_reply"),
	}
}

// evIn is one event to insert.
type evIn struct {
	topic      string
	to         *string
	ref        *string
	msg        *string
	data       *jsfmt.Value
	replyTo    *int64
	needsReply bool
	key        *string
	ts         string
}

// insert writes one event inside a caller's transaction, and answers it (or the earlier one for
// a repeated key).
func (c *Channel) insert(ctx context.Context, q store.Q, from string, e evIn) (Ev, error) {
	if e.key != nil {
		dup, err := q.Get(ctx, "SELECT * FROM events WHERE from_name=? AND key=?", from, *e.key)
		if err != nil {
			return Ev{}, err
		}
		if dup != nil {
			ev := toEv(dup)
			ev.Duplicate = true
			return ev, nil
		}
	}
	ts := e.ts
	if ts == "" {
		ts = c.nowISO()
	}
	var data any
	if e.data != nil {
		data = e.data.EncodeJSON()
	}
	needsReply := int64(0)
	if e.needsReply {
		needsReply = 1
	}
	r, err := q.Get(ctx, `INSERT INTO events (ts, from_name, to_name, topic, ref, msg, data, reply_to, needs_reply, key)
      VALUES (?,?,?,?,?,?,?,?,?,?) RETURNING *`,
		ts, from, derefOrNil(e.to), e.topic, derefOrNil(e.ref), derefOrNil(e.msg), data, derefIntOrNil(e.replyTo), needsReply, derefOrNil(e.key))
	if err != nil {
		return Ev{}, err
	}
	ev := toEv(r)
	if err := c.advanceTurn(ctx, q, ev); err != nil {
		return Ev{}, err
	}
	c.stateMu.Lock()
	c.pending = append(c.pending, ev)
	c.stateMu.Unlock()
	return ev, nil
}

func derefIntOrNil(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// flush emits the events of the committed transaction and wakes every waiter.
func (c *Channel) flush() {
	c.stateMu.Lock()
	p := c.pending
	c.pending = nil
	c.stateMu.Unlock()
	for _, ev := range p {
		c.emit("event", ev)
	}
	if len(p) > 0 {
		c.wake(p)
	}
}

// Append writes one event on the timeline.
func (c *Channel) Append(ctx context.Context, from string, e evIn) (Ev, error) {
	return c.append(ctx, from, e)
}

// EventIn is one event to put on the timeline from outside the package, without the checks a
// session operation carries.
type EventIn struct {
	Topic      string
	To         *string
	Ref        *string
	Msg        *string
	Data       *jsfmt.Value
	ReplyTo    *int64
	NeedsReply bool
	Key        *string
}

// AppendEvent writes one event on the timeline.
func (c *Channel) AppendEvent(ctx context.Context, from string, e EventIn) (Ev, error) {
	return c.append(ctx, from, evIn{
		topic: e.Topic, to: e.To, ref: e.Ref, msg: e.Msg, data: e.Data,
		replyTo: e.ReplyTo, needsReply: e.NeedsReply, key: e.Key,
	})
}

func (c *Channel) append(ctx context.Context, from string, e evIn) (Ev, error) {
	var ev Ev
	err := c.serial(ctx, func(ctx context.Context) error {
		err := c.tx(ctx, func(ctx context.Context, q store.Q) error {
			var insertErr error
			ev, insertErr = c.insert(ctx, q, from, e)
			return insertErr
		})
		if err != nil {
			return err
		}
		c.flush()
		return nil
	})
	return ev, err
}

// Publish puts an event on a topic of the channel (to everyone, or to one session).
func (c *Channel) Publish(ctx context.Context, from string, a PublishArgs) (Ev, error) {
	var ev Ev
	err := c.serial(ctx, func(ctx context.Context) error {
		me, err := c.need(ctx, from)
		if err != nil {
			return err
		}
		if !TopicRe.MatchString(a.Topic) || strings.Contains(a.Topic, "*") {
			return bad(fmt.Sprintf("topic must match %s without *", topicReSrc))
		}
		if reservedTopicRe.MatchString(a.Topic) || a.Topic == "reply" {
			return bad(fmt.Sprintf("topic %s is reserved; use the matching tool", a.Topic))
		}
		if a.Msg == "" {
			return bad("need msg")
		}
		if a.To != nil && *a.To != Owner {
			if _, err := c.need(ctx, *a.To); err != nil {
				return err
			}
		}
		if err := c.mustRun(me, "publish"); err != nil {
			return err
		}
		ev, err = c.append(ctx, from, evIn{
			topic: a.Topic, to: a.To, ref: a.Ref, msg: sp(utf16Cut(a.Msg, 20000)), data: a.Data,
			needsReply: a.NeedsReply, key: a.Key,
		})
		return err
	})
	return ev, err
}

// Send messages one session (or everyone); an ask lands in their inbox until they reply.
func (c *Channel) Send(ctx context.Context, from string, to *string, msg string, o SendOpts) (Ev, error) {
	var ev Ev
	err := c.serial(ctx, func(ctx context.Context) error {
		if _, err := c.need(ctx, from); err != nil {
			return err
		}
		if to != nil && *to != Owner {
			if _, err := c.need(ctx, *to); err != nil {
				return err
			}
		}
		if msg == "" {
			return bad("need msg")
		}
		topic := "msg"
		if o.Ask {
			topic = "ask"
		}
		var data *jsfmt.Value
		if o.Task != nil {
			data = jsfmt.Object([]string{"task"}, []*jsfmt.Value{jstr(*o.Task)})
		}
		var key *string
		if o.Key != nil {
			key = o.Key
		}
		evIn := evIn{topic: topic, to: to, msg: sp(utf16Cut(msg, 20000)), data: data, needsReply: o.Ask, key: key}
		var err error
		ev, err = c.append(ctx, from, evIn)
		return err
	})
	return ev, err
}

// Reply answers a message by its seq.
func (c *Channel) Reply(ctx context.Context, from string, seq int64, msg string) (Ev, error) {
	var ev Ev
	err := c.serial(ctx, func(ctx context.Context) error {
		if _, err := c.need(ctx, from); err != nil {
			return err
		}
		q, err := c.dbGet(ctx, "SELECT * FROM events WHERE seq=?", seq)
		if err != nil {
			return err
		}
		if q == nil {
			return httpErr(404, fmt.Sprintf("no event %d", seq))
		}
		if rStr(q, "from_name") == from {
			return bad("you cannot answer your own message")
		}
		if to := rStrPtr(q, "to_name"); to != nil && *to != from {
			return httpErr(403, fmt.Sprintf("event %d is addressed to %s, not %s", seq, *to, from))
		}
		if rBool(q, "needs_reply") {
			answered, err := c.dbGet(ctx, "SELECT 1 FROM events WHERE reply_to=? AND from_name=?", seq, from)
			if err != nil {
				return err
			}
			if answered != nil {
				return httpErr(409, fmt.Sprintf("event %d is already answered by %s", seq, from))
			}
		}
		out := msg
		if out == "" {
			out = "ack"
		}
		ev, err = c.append(ctx, from, evIn{
			topic: "reply", to: sp(rStr(q, "from_name")), msg: sp(out), replyTo: sp2(seq), ref: rStrPtr(q, "ref"),
		})
		return err
	})
	return ev, err
}

func sp2(i int64) *int64 { return &i }

// Event reads one event; nil when there is none.
func (c *Channel) Event(ctx context.Context, seq int64) (*Ev, error) {
	r, err := c.dbGet(ctx, "SELECT * FROM events WHERE seq=?", seq)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, nil
	}
	ev := toEv(r)
	return &ev, nil
}

// Events reads the channel history, newest last.
func (c *Channel) Events(ctx context.Context, o EventsFilter) ([]Ev, error) {
	lim := int64(300)
	if o.Limit != nil {
		lim = *o.Limit
	}
	lim = max(1, min(2000, lim))
	where := []string{"seq > ?"}
	params := []any{int64(0)}
	if o.After != nil {
		params[0] = *o.After
	}
	if o.Before != nil {
		where = append(where, "seq < ?")
		params = append(params, *o.Before)
	}
	if o.From != nil {
		where = append(where, "from_name = ?")
		params = append(params, *o.From)
	}
	params = append(params, lim)
	rows, err := c.dbAll(ctx, fmt.Sprintf("SELECT * FROM events WHERE %s ORDER BY seq DESC LIMIT ?", strings.Join(where, " AND ")), params...)
	if err != nil {
		return nil, err
	}
	out := []Ev{}
	for i := len(rows) - 1; i >= 0; i-- {
		ev := toEv(rows[i])
		if o.Topic == "" || GlobMatch(ev.Topic, []string{o.Topic}) {
			out = append(out, ev)
		}
	}
	return out, nil
}

// LastSeq is the newest event's seq.
func (c *Channel) LastSeq(ctx context.Context) (int64, error) {
	r, err := c.dbGet(ctx, "SELECT COALESCE(MAX(seq),0) s FROM events")
	if err != nil {
		return 0, err
	}
	return rInt(r, "s"), nil
}

// Unread is everything the session has not acked yet, in order: every member sees every message,
// also those addressed to another session (it overhears them); only its own are left out.
func (c *Channel) Unread(ctx context.Context, name string) ([]Ev, error) {
	s, err := c.need(ctx, name)
	if err != nil {
		return nil, err
	}
	rows, err := c.dbAll(ctx, "SELECT * FROM events WHERE seq > ? AND from_name != ? ORDER BY seq", s.Cursor, name)
	if err != nil {
		return nil, err
	}
	out := make([]Ev, len(rows))
	for i, r := range rows {
		out[i] = toEv(r)
	}
	return out, nil
}

// Inbox lists the messages waiting for this session's reply.
func (c *Channel) Inbox(ctx context.Context, name string) ([]Ev, error) {
	rows, err := c.dbAll(ctx, fmt.Sprintf("SELECT e.* FROM events e WHERE %s ORDER BY e.seq", openAsk), name, name, name)
	if err != nil {
		return nil, err
	}
	out := make([]Ev, len(rows))
	for i, r := range rows {
		out[i] = toEv(r)
	}
	return out, nil
}

// Ack marks everything up to seq as handled; the cursor never moves back.
func (c *Channel) Ack(ctx context.Context, name string, seq int64) (AckResult, error) {
	var out AckResult
	err := c.serial(ctx, func(ctx context.Context) error {
		s, err := c.need(ctx, name)
		if err != nil {
			return err
		}
		if seq < 0 {
			return bad("seq must be an integer")
		}
		last, err := c.LastSeq(ctx)
		if err != nil {
			return err
		}
		top := min(seq, last)
		if top > s.Cursor {
			if err := c.dbRun(ctx, "UPDATE sessions SET cursor=? WHERE name=?", top, name); err != nil {
				return err
			}
		}
		cursor := max(top, s.Cursor)
		c.emit("ack", jsfmt.Object([]string{"name", "cursor"}, []*jsfmt.Value{jstr(name), jnum(cursor)}))
		out = AckResult{Cursor: cursor}
		return nil
	})
	return out, err
}

// Wait blocks until (1) a message waits for this session's reply; (2) the first unread event for
// this session (to it or to everyone) matches a topic glob, with the unread events before it,
// overheard ones included; (3) the timeout passes, or the context is cancelled. A message to
// another session never wakes this one.
func (c *Channel) Wait(ctx context.Context, name string, topics []string, timeoutS int) (WaitHit, error) {
	before, err := c.need(ctx, name)
	if err != nil {
		return WaitHit{}, err
	}
	for _, t := range topics {
		if !TopicRe.MatchString(t) {
			return WaitHit{}, bad(fmt.Sprintf("bad topic glob %s", t))
		}
	}
	bctx := context.Background()
	check := func() (*WaitHit, error) {
		ib, err := c.Inbox(bctx, name)
		if err != nil {
			return nil, err
		}
		if len(ib) > 0 {
			h := hitFromEv(ib[0], "message")
			h.More = len(ib) - 1
			return &h, nil
		}
		all, err := c.Unread(bctx, name)
		if err != nil {
			return nil, err
		}
		for i, e := range all {
			if (e.To == nil || *e.To == name) && GlobMatch(e.Topic, topics) {
				sk := all[:i]
				lo := max(0, len(sk)-25)
				skipped := []SkippedEvent{}
				for _, s := range sk[lo:] {
					skipped = append(skipped, SkippedEvent{Seq: s.Seq, From: s.From, To: s.To, Topic: s.Topic, Msg: utf16CutPtr(s.Msg, 200)})
				}
				h := hitFromEv(e, "event")
				h.Skipped = skipped
				h.SkippedTotal = len(sk)
				return &h, nil
			}
		}
		return nil, nil
	}
	return c.sleep(ctx, name, check, timeoutS,
		func() PresenceInput {
			waitingFor := "any event"
			if len(topics) > 0 {
				waitingFor = strings.Join(topics, " ")
			}
			return PresenceInput{State: sp("waiting"), Task: sp("waiting for " + waitingFor)}
		},
		func(h WaitHit) error {
			s, _ := c.Session(bctx, name)
			if s == nil || s.State != "waiting" {
				return nil
			}
			state := before.State
			if state == "waiting" {
				state = "working"
			}
			task := "handling a wake-up"
			if h.Kind == "timeout" {
				task = "wait timed out"
			}
			_, err := c.Presence(bctx, name, PresenceInput{State: sp(state), Task: sp(task)})
			return err
		}, func(e Ev) bool { return waitMatch(name, topics, e) })
}

// waitMatch answers whether one new event can move this topic wait at all: an ask it may have to
// answer, or an event it could read (not its own, addressed to it or to everyone, on a topic it
// waits for). It is deliberately a superset of the check: a false yes only costs one query, a
// false no would cost the wake-up. The check's own rules are narrower (its own cursor, the ask
// still open), so an event this sends back can still miss there — that only leaves the waiter
// parked, as the event were never committed.
func waitMatch(name string, topics []string, e Ev) bool {
	if e.From == name {
		return false
	}
	if e.NeedsReply && (e.To == nil || *e.To == name) {
		return true
	}
	if e.To != nil && *e.To != name {
		return false
	}
	return GlobMatch(e.Topic, topics)
}

// ── the turn: who may work now ───────────────────────────────────────────────

// advanceTurn keeps the materialized turn: turn.pass gives it to the named session, turn.take to
// the sender, and a sender's handover topic to that sender's handover target.
func (c *Channel) advanceTurn(ctx context.Context, q store.Q, e Ev) error {
	var holder *string
	if e.Topic == "turn.take" {
		holder = sp(e.From)
	} else if e.Topic == "turn.pass" && e.To != nil {
		holder = sp(*e.To)
	} else if to, _, ok := c.handoverTo(e.From); ok {
		for _, t := range c.handoverTopics(e.From) {
			if t == e.Topic {
				holder = sp(to)
				break
			}
		}
	}
	if holder != nil {
		if err := c.setMeta(ctx, q, "turn_holder", holder); err != nil {
			return err
		}
		return c.setMeta(ctx, q, "turn_seq", sp(strconv.FormatInt(e.Seq, 10)))
	}
	return nil
}

func (c *Channel) handoverTopics(from string) []string {
	_, topics, _ := c.handoverTo(from)
	return topics
}

// Turn is who may work now, and the event that gave it.
func (c *Channel) Turn(ctx context.Context) (TurnView, error) {
	seq, _ := strconv.ParseInt(c.meta("turn_seq"), 10, 64)
	holder := c.metaPtr("turn_holder")
	if holder == nil {
		holder = c.Config().Start
	}
	var since *Ev
	if seq != 0 {
		ev, err := c.Event(ctx, seq)
		if err != nil {
			return TurnView{}, err
		}
		since = ev
	}
	return TurnView{Holder: holder, Since: since}, nil
}

// PassTurn hands the turn to another session.
func (c *Channel) PassTurn(ctx context.Context, from, to, msg string) (Ev, error) {
	var ev Ev
	err := c.serial(ctx, func(ctx context.Context) error {
		me, err := c.need(ctx, from)
		if err != nil {
			return err
		}
		if _, err := c.need(ctx, to); err != nil {
			return err
		}
		if err := c.mustRun(me, "turn.pass"); err != nil {
			return err
		}
		if msg == "" {
			msg = fmt.Sprintf("%s hands the turn to %s", from, to)
		}
		ev, err = c.append(ctx, from, evIn{topic: "turn.pass", to: sp(to), msg: sp(msg)})
		return err
	})
	return ev, err
}

// TakeTurn takes the turn.
func (c *Channel) TakeTurn(ctx context.Context, from, msg string) (Ev, error) {
	var ev Ev
	err := c.serial(ctx, func(ctx context.Context) error {
		me, err := c.need(ctx, from)
		if err != nil {
			return err
		}
		if err := c.mustRun(me, "turn.take"); err != nil {
			return err
		}
		if msg == "" {
			msg = fmt.Sprintf("%s takes the turn", from)
		}
		ev, err = c.append(ctx, from, evIn{topic: "turn.take", msg: sp(msg)})
		return err
	})
	return ev, err
}

// ReplayTurn recomputes the materialized turn from the whole history.
func (c *Channel) ReplayTurn(ctx context.Context) error {
	return c.serial(ctx, func(ctx context.Context) error {
		return c.tx(ctx, func(ctx context.Context, q store.Q) error {
			if err := c.setMeta(ctx, q, "turn_holder", nil); err != nil {
				return err
			}
			if err := c.setMeta(ctx, q, "turn_seq", nil); err != nil {
				return err
			}
			rows, err := q.All(ctx, "SELECT * FROM events ORDER BY seq")
			if err != nil {
				return err
			}
			for _, r := range rows {
				if err := c.advanceTurn(ctx, q, toEv(r)); err != nil {
					return err
				}
			}
			return nil
		})
	})
}

// ── tasks: the plan, shared by every session ────────────────────────────────

func (c *Channel) rawTask(ctx context.Context, id string) (Row, error) {
	return c.dbGet(ctx, "SELECT * FROM tasks WHERE id=?", id)
}

func (c *Channel) rawTaskQ(ctx context.Context, q store.Q, id string) (Row, error) {
	return q.Get(ctx, "SELECT * FROM tasks WHERE id=?", id)
}

// editsBy groups every task's owner edits, in application order.
func (c *Channel) editsBy(ctx context.Context) (map[string][]Row, error) {
	rows, err := c.dbAll(ctx, "SELECT * FROM edits")
	if err != nil {
		return nil, err
	}
	m := map[string][]Row{}
	for _, r := range rows {
		id := rStr(r, "task_id")
		m[id] = append(m[id], r)
	}
	return m, nil
}

// builder assembles an object value key by key, replacing a key where it stands and appending a
// new one at the end: the object shape a flattened task keeps.
type builder struct {
	keys   []string
	fields []*jsfmt.Value
}

func (b *builder) set(k string, v *jsfmt.Value) {
	for i, existing := range b.keys {
		if existing == k {
			b.fields[i] = v
			return
		}
	}
	b.keys = append(b.keys, k)
	b.fields = append(b.fields, v)
}

func (b *builder) has(k string) bool { return slices.Contains(b.keys, k) }

func (b *builder) obj() *jsfmt.Value { return jsfmt.Object(b.keys, b.fields) }

func (b *builder) field(k string) *jsfmt.Value {
	for i, existing := range b.keys {
		if existing == k {
			return b.fields[i]
		}
	}
	return nil
}

// effective flattens a task row into the object the views see: body fields flattened, owner edits
// applied, depends = after.
func (c *Channel) effective(r Row, edits []Row) (*TaskView, error) {
	var b builder
	if body := rJSON(r, "body"); body != nil && body.Kind() == jsfmt.KindObject {
		b.keys = slices.Clone(body.Keys())
		b.fields = slices.Clone(body.Fields())
	}
	b.set("id", jstr(rStr(r, "id")))
	b.set("phase_n", jnum(rInt(r, "phase")))
	b.set("ord", jnum(rInt(r, "ord")))
	b.set("title", jstr(rStr(r, "title")))
	b.set("owner", jstrPtr(rStrPtr(r, "owner")))
	b.set("status", jstr(rStr(r, "status")))
	b.set("depends", jstrs(strsOf(rJSON(r, "after"))))
	b.set("origin", jstr(rStr(r, "origin")))
	b.set("created_by", jstrPtr(rStrPtr(r, "created_by")))
	b.set("status_at", jstrPtr(rStrPtr(r, "status_at")))
	b.set("status_by", jstrPtr(rStrPtr(r, "status_by")))
	b.set("status_note", jstrPtr(rStrPtr(r, "status_note")))
	for _, f := range JSONFields {
		if !b.has(f) {
			b.set(f, jarrNull())
		}
	}
	t := &TaskView{
		ID: rStr(r, "id"), PhaseN: rInt(r, "phase"), Ord: rInt(r, "ord"), Title: rStr(r, "title"),
		Owner: rStrPtr(r, "owner"), Status: rStr(r, "status"), Depends: strsOf(rJSON(r, "after")),
	}
	var editedKeys []string
	var editedFields []*jsfmt.Value
	b.set("edited", jsfmt.Object(editedKeys, editedFields))
	for _, e := range edits {
		field := rStr(e, "field")
		value := rJSON(e, "value")
		if value == nil {
			value = jstr(rStr(e, "value"))
		}
		t.Edited = append(t.Edited, TaskEdit{Field: field, Original: b.field(field), At: rStr(e, "at"), By: rStrPtr(e, "by")})
		editedKeys = append(editedKeys, field)
		editedFields = append(editedFields, jsfmt.Object(
			[]string{"original", "at", "by"},
			[]*jsfmt.Value{b.field(field), jstr(rStr(e, "at")), jstrPtr(rStrPtr(e, "by"))},
		))
		b.set(field, value)
		b.set("edited", jsfmt.Object(editedKeys, editedFields))
	}
	t.Fields = b.obj()
	return t, nil
}

// Task reads one task as the owner sees it; nil when there is none.
func (c *Channel) Task(ctx context.Context, id string) (*TaskView, error) {
	r, err := c.rawTask(ctx, id)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, nil
	}
	editRows, err := c.dbAll(ctx, "SELECT * FROM edits WHERE task_id=?", id)
	if err != nil {
		return nil, err
	}
	noteRows, err := c.dbAll(ctx, "SELECT * FROM notes WHERE task_id=? ORDER BY created_at, id", id)
	if err != nil {
		return nil, err
	}
	all, err := c.dbAll(ctx, "SELECT id, after, status, owner, title FROM tasks")
	if err != nil {
		return nil, err
	}
	e, err := c.effective(r, editRows)
	if err != nil {
		return nil, err
	}
	comments := []Note{}
	for _, nr := range noteRows {
		comments = append(comments, Note{
			ID: rInt(nr, "id"), TaskID: rStr(nr, "task_id"), Kind: rStr(nr, "kind"), Body: rStr(nr, "body"),
			By: rStrPtr(nr, "by"), Resolved: rInt(nr, "resolved"), CreatedAt: rStr(nr, "created_at"),
		})
	}
	e.Comments = comments
	status := map[string]Row{}
	for _, x := range all {
		status[rStr(x, "id")] = x
	}
	e.NeededBy = []string{}
	for _, x := range all {
		if slices.Contains(strsOf(rJSON(x, "after")), id) {
			e.NeededBy = append(e.NeededBy, rStr(x, "id"))
		}
	}
	e.Unmet = []UnmetDep{}
	for _, d := range e.Depends {
		dep, ok := status[d]
		depStatus := "todo"
		if ok {
			depStatus = rStr(dep, "status")
		}
		if !finished(depStatus) {
			unmet := UnmetDep{ID: d}
			if ok {
				unmet.Owner = rStrPtr(dep, "owner")
				unmet.Status = depStatus
				unmet.Title = rStrPtr(dep, "title")
			} else {
				unmet.Status = "missing"
			}
			e.Unmet = append(e.Unmet, unmet)
		}
	}
	e.Ready = len(e.Unmet) == 0
	e.OpenNotes = []Note{}
	for _, n := range comments {
		if n.Resolved == 0 {
			e.OpenNotes = append(e.OpenNotes, n)
		}
	}
	return e, nil
}

// Tasks lists the plan in plan order, for lists and the kanban.
func (c *Channel) Tasks(ctx context.Context, o TasksFilter) ([]TaskListItem, error) {
	edits, err := c.editsBy(ctx)
	if err != nil {
		return nil, err
	}
	countRows, err := c.dbAll(ctx, fmt.Sprintf("SELECT task_id, %s n, CAST(SUM(CASE WHEN resolved=0 THEN 1 ELSE 0 END) AS INTEGER) open, GROUP_CONCAT(DISTINCT kind) kinds FROM notes GROUP BY task_id", countInt))
	if err != nil {
		return nil, err
	}
	counts := map[string]Row{}
	for _, cr := range countRows {
		counts[rStr(cr, "task_id")] = cr
	}
	rows, err := c.dbAll(ctx, "SELECT * FROM tasks ORDER BY phase, ord, id")
	if err != nil {
		return nil, err
	}
	status := map[string]string{}
	for _, r := range rows {
		status[rStr(r, "id")] = rStr(r, "status")
	}
	out := []TaskListItem{}
	for _, r := range rows {
		owner := rStrPtr(r, "owner")
		if o.Owner != nil && (owner == nil || *owner != *o.Owner) {
			continue
		}
		if o.Status != "" && rStr(r, "status") != o.Status {
			continue
		}
		if o.Phase != nil && rInt(r, "phase") != *o.Phase {
			continue
		}
		e, err := c.effective(r, edits[rStr(r, "id")])
		if err != nil {
			return nil, err
		}
		blockedBy := []string{}
		for _, d := range e.Depends {
			st, ok := status[d]
			if !ok {
				st = "todo"
			}
			if !finished(st) {
				blockedBy = append(blockedBy, d)
			}
		}
		var comments *TaskComments
		if cr, ok := counts[e.ID]; ok {
			kinds := []string{}
			for _, k := range strings.Split(rStr(cr, "kinds"), ",") {
				if k != "" {
					kinds = append(kinds, k)
				}
			}
			comments = &TaskComments{N: rInt(cr, "n"), Open: rInt(cr, "open"), Kinds: kinds}
		}
		out = append(out, TaskListItem{
			ID: e.ID, PhaseN: e.PhaseN, Title: e.Title, Owner: e.Owner, Kind: e.Text("kind"),
			Gate: e.Text("gate"), Risk: e.Text("risk"), Status: e.Status, Value: e.Text("value"),
			Depends: e.Depends, StatusBy: statusBy(e), BlockedBy: blockedBy, Comments: comments,
			Edited: len(e.Edited) > 0,
		})
	}
	return out, nil
}

func statusBy(e *TaskView) *string { return e.TextPtr("status_by") }

// Next is the first unfinished task the session should work: its own or an unowned one, in plan
// order; done says there is none.
func (c *Channel) Next(ctx context.Context, name string) (*TaskView, bool, error) {
	if _, err := c.need(ctx, name); err != nil {
		return nil, false, err
	}
	r, err := c.dbGet(ctx, `SELECT id FROM tasks WHERE status NOT IN ('done','skipped') AND (owner = ? OR owner IS NULL)
      ORDER BY phase, ord, id LIMIT 1`, name)
	if err != nil {
		return nil, false, err
	}
	if r == nil {
		return nil, true, nil
	}
	t, err := c.Task(ctx, rStr(r, "id"))
	if err != nil || t == nil {
		return nil, true, err
	}
	return t, false, nil
}

// checkAfter rejects unknown dependencies and cycles: the graph is walked from the new edges back
// to the task itself.
func (c *Channel) checkAfter(ctx context.Context, q store.Q, id string, after []string) error {
	rows, err := q.All(ctx, "SELECT id, after FROM tasks")
	if err != nil {
		return err
	}
	graph := map[string][]string{}
	for _, r := range rows {
		graph[rStr(r, "id")] = strsOf(rJSON(r, "after"))
	}
	for _, d := range after {
		if _, ok := graph[d]; !ok {
			return bad(fmt.Sprintf("depends on unknown task %s", d))
		}
	}
	graph[id] = after
	seen := map[string]bool{}
	stack := slices.Clone(after)
	for len(stack) > 0 {
		x := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if x == id {
			return bad(fmt.Sprintf("dependency cycle through %s", id))
		}
		if seen[x] {
			continue
		}
		seen[x] = true
		stack = append(stack, graph[x]...)
	}
	return nil
}

// CreateTask adds a task to the plan.
func (c *Channel) CreateTask(ctx context.Context, by string, t CreateTaskInput) (CreateTaskResult, error) {
	var out CreateTaskResult
	err := c.serial(ctx, func(ctx context.Context) error {
		me, err := c.need(ctx, by)
		if err != nil {
			return err
		}
		if err := c.mustRun(me, "task create"); err != nil {
			return err
		}
		id := t.ID
		if id == "" {
			id = fmt.Sprintf("t%s%s", strconv.FormatInt(c.nowMs(), 36), randTail(c.Rand))
		}
		if !taskIDRe.MatchString(id) {
			return bad("task id: 1-32 of [A-Za-z0-9_.-]")
		}
		existing, err := c.rawTask(ctx, id)
		if err != nil {
			return err
		}
		if existing != nil {
			return httpErr(409, fmt.Sprintf("task %s exists", id))
		}
		if t.Title == "" {
			return bad("need title")
		}
		if t.Owner != nil && *t.Owner != Owner {
			if _, err := c.need(ctx, *t.Owner); err != nil {
				return err
			}
		}
		after := []string{}
		if t.After != nil {
			after = t.After
		}
		if err := c.checkAfter(ctx, c.Store, id, after); err != nil {
			return err
		}
		phase := int64(0)
		if t.Phase != nil {
			phase = *t.Phase
		}
		status := t.Status
		if status == "" {
			status = "todo"
		}
		origin := "session"
		if by == Owner {
			origin = "owner"
		}
		body := t.Body
		if body == nil {
			body = jsfmt.Object(nil, nil)
		}
		var ev Ev
		err = c.tx(ctx, func(ctx context.Context, q store.Q) error {
			ordRow, err := q.Get(ctx, "SELECT COALESCE(MAX(ord),-1)+1 o FROM tasks WHERE phase=?", phase)
			if err != nil {
				return err
			}
			if err := q.Run(ctx, `INSERT INTO tasks (id, phase, ord, title, owner, status, after, body, origin, created_by, created_at)
              VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
				id, phase, rInt(ordRow, "o"), t.Title, derefOrNil(t.Owner), status, jstrs(after).EncodeJSON(), body.EncodeJSON(), origin, by, c.nowISO()); err != nil {
				return err
			}
			msg := fmt.Sprintf("%s %s", id, t.Title)
			if t.Owner != nil && *t.Owner != "" {
				msg += fmt.Sprintf(" → %s", *t.Owner)
			}
			if len(after) > 0 {
				msg += fmt.Sprintf(" after %s", strings.Join(after, ", "))
			}
			var to *string
			if t.Owner != nil && *t.Owner != by {
				to = t.Owner
			}
			ev, err = c.insert(ctx, q, by, evIn{
				topic: "task.created", to: to, ref: sp(id), msg: sp(msg),
				data: jsfmt.Object([]string{"task"}, []*jsfmt.Value{jstr(id)}),
			})
			return err
		})
		if err != nil {
			return err
		}
		if err := c.Reindex(ctx, []string{id}); err != nil {
			return err
		}
		c.flush()
		c.emit("task", c.taskBrief(ctx, id))
		task, err := c.Task(ctx, id)
		if err != nil {
			return err
		}
		out = CreateTaskResult{Task: task, Event: ev}
		return nil
	})
	return out, err
}

func randTail(r func() float64) string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	out := make([]byte, 3)
	for i := range out {
		out[i] = alphabet[int(r()*float64(len(alphabet)))%len(alphabet)]
	}
	return string(out)
}

// UpdateTask reassigns a task, changes its dependencies (cycles are refused) or edits a field.
func (c *Channel) UpdateTask(ctx context.Context, by, id string, f UpdateTaskInput) (*TaskView, error) {
	var out *TaskView
	err := c.serial(ctx, func(ctx context.Context) error {
		if _, err := c.need(ctx, by); err != nil {
			return err
		}
		r, err := c.rawTask(ctx, id)
		if err != nil {
			return err
		}
		if r == nil {
			return httpErr(404, fmt.Sprintf("no task %s", id))
		}
		if f.Owner != nil && *f.Owner != Owner {
			if _, err := c.need(ctx, *f.Owner); err != nil {
				return err
			}
		}
		if f.After != nil {
			if err := c.checkAfter(ctx, c.Store, id, f.After); err != nil {
				return err
			}
		}
		if f.Field != "" && !editable[f.Field] {
			return bad(fmt.Sprintf("field %s is not editable", f.Field))
		}
		err = c.tx(ctx, func(ctx context.Context, q store.Q) error {
			if f.Owner != nil {
				if err := q.Run(ctx, "UPDATE tasks SET owner=? WHERE id=?", derefOrNil(f.Owner), id); err != nil {
					return err
				}
				var to *string
				if *f.Owner != by {
					to = f.Owner
				}
				if _, err := c.insert(ctx, q, by, evIn{
					topic: "task.assigned", to: to, ref: sp(id),
					msg:  sp(fmt.Sprintf("%s → %s", id, orDefault(deref(f.Owner), "nobody"))),
					data: jsfmt.Object([]string{"task", "owner"}, []*jsfmt.Value{jstr(id), jstrPtr(f.Owner)}),
				}); err != nil {
					return err
				}
			}
			if f.After != nil {
				if err := q.Run(ctx, "UPDATE tasks SET after=? WHERE id=?", jstrs(f.After).EncodeJSON(), id); err != nil {
					return err
				}
			}
			if f.Field != "" {
				if f.Value == nil || f.Value.Kind() == jsfmt.KindNull {
					if err := q.Run(ctx, "DELETE FROM edits WHERE task_id=? AND field=?", id, f.Field); err != nil {
						return err
					}
				} else {
					if err := q.Run(ctx, `INSERT INTO edits (task_id, field, value, by, at) VALUES (?,?,?,?,?)
            ON CONFLICT(task_id, field) DO UPDATE SET value=excluded.value, by=excluded.by, at=excluded.at`,
						id, f.Field, f.Value.EncodeJSON(), by, c.nowISO()); err != nil {
						return err
					}
				}
			}
			if f.After != nil {
				return c.releaseReadyTx(ctx, q, by, []string{id})
			}
			return nil
		})
		if err != nil {
			return err
		}
		if err := c.Reindex(ctx, []string{id}); err != nil {
			return err
		}
		c.flush()
		c.emit("task", c.taskBrief(ctx, id))
		out, err = c.Task(ctx, id)
		if err != nil {
			return err
		}
		if out == nil {
			return httpErr(404, fmt.Sprintf("no task %s", id))
		}
		return nil
	})
	return out, err
}

// SetStatus moves a task through its statuses; done and skipped release every task that waited
// only on it: each gets a task.ready event addressed to its owner, so a session blocked on
// another's work wakes up.
func (c *Channel) SetStatus(ctx context.Context, by, id, status, note string) (SetStatusResult, error) {
	var out SetStatusResult
	err := c.serial(ctx, func(ctx context.Context) error {
		me, err := c.need(ctx, by)
		if err != nil {
			return err
		}
		if !slices.Contains(Statuses, status) {
			return bad("status must be one of " + strings.Join(Statuses, ", "))
		}
		r, err := c.rawTask(ctx, id)
		if err != nil {
			return err
		}
		if r == nil {
			return httpErr(404, fmt.Sprintf("no task %s", id))
		}
		if err := c.mustRun(me, "task status"); err != nil {
			return err
		}
		if status == "doing" {
			st, err := c.statusMap(ctx)
			if err != nil {
				return err
			}
			var unmet []string
			for _, d := range strsOf(rJSON(r, "after")) {
				depStatus := "todo"
				if s, ok := st[d]; ok {
					depStatus = s
				}
				if !finished(depStatus) {
					unmet = append(unmet, d)
				}
			}
			if len(unmet) > 0 {
				return httpErr(409, fmt.Sprintf("%s waits on %s; wait for task.ready (or the owner edits its dependencies)", id, strings.Join(unmet, ", ")))
			}
		}
		err = c.tx(ctx, func(ctx context.Context, q store.Q) error {
			if err := q.Run(ctx, "UPDATE tasks SET status=?, status_by=?, status_at=?, status_note=? WHERE id=?", status, by, c.nowISO(), note, id); err != nil {
				return err
			}
			msg := fmt.Sprintf("%s → %s", id, status)
			if note != "" {
				msg += " · " + note
			}
			if _, err := c.insert(ctx, q, by, evIn{
				topic: "task.status", ref: sp(id), msg: sp(msg),
				data: jsfmt.Object([]string{"task", "status", "note"}, []*jsfmt.Value{jstr(id), jstr(status), jstr(note)}),
			}); err != nil {
				return err
			}
			if finished(status) {
				waiting, err := q.All(ctx, "SELECT id, after FROM tasks WHERE status NOT IN ('done','skipped')")
				if err != nil {
					return err
				}
				var ids []string
				for _, d := range waiting {
					if slices.Contains(strsOf(rJSON(d, "after")), id) {
						ids = append(ids, rStr(d, "id"))
					}
				}
				return c.releaseReadyTx(ctx, q, by, ids)
			}
			return nil
		})
		if err != nil {
			return err
		}
		c.flush()
		c.emit("task", c.taskBrief(ctx, id))
		out = SetStatusResult{OK: true, ID: id, Status: status}
		return nil
	})
	return out, err
}

func (c *Channel) statusMap(ctx context.Context) (map[string]string, error) {
	return c.statusMapQ(ctx, c.Store)
}

func (c *Channel) statusMapQ(ctx context.Context, q store.Q) (map[string]string, error) {
	rows, err := q.All(ctx, "SELECT id, status FROM tasks")
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, r := range rows {
		m[rStr(r, "id")] = rStr(r, "status")
	}
	return m, nil
}

// releaseReadyTx gives each of these tasks that is open and has nothing left to wait on a
// task.ready.
func (c *Channel) releaseReadyTx(ctx context.Context, q store.Q, by string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	st, err := c.statusMapQ(ctx, q)
	if err != nil {
		return err
	}
	for _, id := range ids {
		d, err := c.rawTaskQ(ctx, q, id)
		if err != nil {
			return err
		}
		if d == nil {
			continue
		}
		deps := strsOf(rJSON(d, "after"))
		allDone := true
		for _, x := range deps {
			depStatus := "todo"
			if s, ok := st[x]; ok {
				depStatus = s
			}
			if !finished(depStatus) {
				allDone = false
			}
		}
		if len(deps) > 0 && !finished(rStr(d, "status")) && allDone {
			var to *string
			if owner := rStrPtr(d, "owner"); owner != nil {
				to = owner
			}
			if _, err := c.insert(ctx, q, by, evIn{
				topic: "task.ready", to: to, ref: sp(rStr(d, "id")),
				msg:  sp(fmt.Sprintf("%s %s: everything it waits on is done", rStr(d, "id"), rStr(d, "title"))),
				data: jsfmt.Object([]string{"task", "after"}, []*jsfmt.Value{jstr(rStr(d, "id")), jstrs(deps)}),
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// WaitTask blocks until a task is done or skipped (a session that needs another's work before its
// own).
func (c *Channel) WaitTask(ctx context.Context, name, id string, timeoutS int) (WaitHit, error) {
	if _, err := c.need(ctx, name); err != nil {
		return WaitHit{}, err
	}
	r, err := c.rawTask(context.Background(), id)
	if err != nil {
		return WaitHit{}, err
	}
	if r == nil {
		return WaitHit{}, httpErr(404, fmt.Sprintf("no task %s", id))
	}
	bctx := context.Background()
	check := func() (*WaitHit, error) {
		t, err := c.rawTask(bctx, id)
		if err != nil {
			return nil, err
		}
		if t != nil && finished(rStr(t, "status")) {
			return &WaitHit{Kind: "event", Task: id, Status: rStr(t, "status"), By: rStrPtr(t, "status_by"), Note: rStrPtr(t, "status_note")}, nil
		}
		return nil, nil
	}
	return c.sleep(ctx, name, check, timeoutS,
		func() PresenceInput { return PresenceInput{State: sp("waiting"), Task: sp("waiting for task " + id)} },
		func(h WaitHit) error {
			if s, _ := c.Session(bctx, name); s != nil && s.State == "waiting" {
				task := fmt.Sprintf("task %s finished", id)
				if h.Kind == "timeout" {
					task = "wait timed out"
				}
				if _, err := c.Presence(bctx, name, PresenceInput{State: sp("working"), Task: sp(task)}); err != nil {
					return err
				}
			}
			return nil
		}, nil)
}

// WaitReply blocks until `from` (or anyone the message was sent to) answers the message at seq.
func (c *Channel) WaitReply(ctx context.Context, name string, seq int64, timeoutS int) (WaitHit, error) {
	if _, err := c.need(ctx, name); err != nil {
		return WaitHit{}, err
	}
	bctx := context.Background()
	check := func() (*WaitHit, error) {
		r, err := c.dbGet(bctx, "SELECT * FROM events WHERE reply_to=? ORDER BY seq LIMIT 1", seq)
		if err != nil {
			return nil, err
		}
		if r == nil {
			return nil, nil
		}
		h := hitFromEv(toEv(r), "event")
		return &h, nil
	}
	return c.sleep(ctx, name, check, timeoutS,
		func() PresenceInput {
			return PresenceInput{State: sp("waiting"), Task: sp(fmt.Sprintf("waiting for a reply to #%d", seq))}
		},
		func(h WaitHit) error {
			if s, _ := c.Session(bctx, name); s != nil && s.State == "waiting" {
				task := fmt.Sprintf("reply to #%d received", seq)
				if h.Kind == "timeout" {
					task = "no reply yet"
				}
				if _, err := c.Presence(bctx, name, PresenceInput{State: sp("working"), Task: sp(task)}); err != nil {
					return err
				}
			}
			return nil
		}, nil)
}

// Current is the task a session is working now (its newest doing one).
func (c *Channel) Current(ctx context.Context, name string) (*string, error) {
	r, err := c.dbGet(ctx, "SELECT id FROM tasks WHERE status='doing' AND (owner=? OR status_by=?) ORDER BY status_at DESC LIMIT 1", name, name)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, nil
	}
	id := rStr(r, "id")
	return &id, nil
}

// Note adds a review note to a task.
func (c *Channel) Note(ctx context.Context, by, id, kind, body string) (*TaskView, error) {
	var out *TaskView
	err := c.serial(ctx, func(ctx context.Context) error {
		r, err := c.rawTask(ctx, id)
		if err != nil {
			return err
		}
		if r == nil {
			return httpErr(404, fmt.Sprintf("no task %s", id))
		}
		if !slices.Contains(NoteKinds, kind) {
			return bad("kind must be one of " + strings.Join(NoteKinds, ", "))
		}
		if strings.TrimSpace(body) == "" || jsfmt.UTF16Len(body) > 20000 {
			return bad("need body (≤ 20000 chars)")
		}
		if err := c.dbRun(ctx, "INSERT INTO notes (task_id, kind, body, by, created_at) VALUES (?,?,?,?,?)", id, kind, strings.TrimSpace(body), by, c.nowISO()); err != nil {
			return err
		}
		c.emit("task", c.taskBrief(ctx, id))
		out, err = c.Task(ctx, id)
		return err
	})
	return out, err
}

// EditNote resolves, rewrites or removes a note.
func (c *Channel) EditNote(ctx context.Context, id int64, f EditNoteInput) (*TaskView, error) {
	var out *TaskView
	err := c.serial(ctx, func(ctx context.Context) error {
		cRow, err := c.dbGet(ctx, "SELECT task_id FROM notes WHERE id=?", id)
		if err != nil {
			return err
		}
		if cRow == nil {
			return httpErr(404, "no such note")
		}
		if f.Remove {
			if err := c.dbRun(ctx, "DELETE FROM notes WHERE id=?", id); err != nil {
				return err
			}
		} else {
			if f.Resolved != nil {
				v := int64(0)
				if *f.Resolved {
					v = 1
				}
				if err := c.dbRun(ctx, "UPDATE notes SET resolved=? WHERE id=?", v, id); err != nil {
					return err
				}
			}
			if f.Body != nil && strings.TrimSpace(*f.Body) != "" {
				if err := c.dbRun(ctx, "UPDATE notes SET body=? WHERE id=?", strings.TrimSpace(*f.Body), id); err != nil {
					return err
				}
			}
		}
		c.emit("task", c.taskBrief(ctx, rStr(cRow, "task_id")))
		out, err = c.Task(ctx, rStr(cRow, "task_id"))
		return err
	})
	return out, err
}

func (c *Channel) taskBrief(ctx context.Context, id string) any {
	r, err := c.rawTask(ctx, id)
	if err != nil || r == nil {
		return jsfmt.Object([]string{"id", "removed"}, []*jsfmt.Value{jstr(id), jbool(true)})
	}
	return jsfmt.Object([]string{"id", "status", "owner", "title"}, []*jsfmt.Value{
		jstr(id), jstr(rStr(r, "status")), jstrPtr(rStrPtr(r, "owner")), jstr(rStr(r, "title")),
	})
}

// ── phases and the plan import ───────────────────────────────────────────────

// Phases lists the plan's phases.
func (c *Channel) Phases(ctx context.Context) ([]PhaseView, error) {
	rows, err := c.dbAll(ctx, "SELECT * FROM phases ORDER BY n")
	if err != nil {
		return nil, err
	}
	out := []PhaseView{}
	for _, r := range rows {
		body := rJSON(r, "body")
		if body == nil || body.Kind() != jsfmt.KindObject {
			body = jsfmt.Object(nil, nil)
		}
		out = append(out, PhaseView{N: rInt(r, "n"), Title: rStr(r, "title"), Body: body})
	}
	return out, nil
}

// ImportPlan loads a plan: phases with their tasks. Content (title, body, order, dependencies)
// comes from the plan; state (status, owner edits, notes) is kept by id. A task whose title
// changed under owner state is reported in Moved. Tasks the plan no longer has are removed unless
// a session or the owner created them.
func (c *Channel) ImportPlan(ctx context.Context, by string, plan Plan, o ImportOpts) (ImportResult, error) {
	var out ImportResult
	err := c.serial(ctx, func(ctx context.Context) error {
		oldRows, err := c.dbAll(ctx, "SELECT id, title FROM tasks")
		if err != nil {
			return err
		}
		old := map[string]string{}
		for _, r := range oldRows {
			old[rStr(r, "id")] = rStr(r, "title")
		}
		keep := map[string]bool{}
		var errors []string
		err = c.tx(ctx, func(ctx context.Context, q store.Q) error {
			if err := q.Run(ctx, "DELETE FROM phases"); err != nil {
				return err
			}
			for _, p := range plan.Phases {
				n := int64(0)
				if p.N != nil {
					n = *p.N
				}
				title := p.Title
				if title == "" {
					title = fmt.Sprintf("Phase %d", n)
				}
				if err := q.Run(ctx, "INSERT INTO phases (n, title, body) VALUES (?,?,?)", n, title, bodyOrEmpty(p.Body).EncodeJSON()); err != nil {
					return err
				}
				for i, st := range p.Steps {
					sid := st.ID
					keep[sid] = true
					status := st.Status
					if !slices.Contains(Statuses, status) {
						status = "todo"
					}
					owner := st.Owner
					if owner == nil {
						owner = o.Owner
					}
					stepTitle := st.Title
					if stepTitle == "" {
						stepTitle = sid
					}
					if err := q.Run(ctx, `INSERT INTO tasks (id, phase, ord, title, owner, status, after, body, origin, created_by, created_at)
              VALUES (?,?,?,?,?,?,?,?, 'plan', ?, ?)
              ON CONFLICT(id) DO UPDATE SET phase=excluded.phase, ord=excluded.ord, title=excluded.title, after=excluded.after, body=excluded.body, origin='plan'`,
						sid, n, i, stepTitle, derefOrNil(owner), status, jstrs(st.Depends).EncodeJSON(), bodyOrEmpty(st.Body).EncodeJSON(), by, c.nowISO()); err != nil {
						return err
					}
				}
			}
			leftRows, err := q.All(ctx, "SELECT id, status FROM tasks WHERE origin='plan'")
			if err != nil {
				return err
			}
			for _, r := range leftRows {
				if keep[rStr(r, "id")] {
					continue
				}
				owned, err := q.Get(ctx, "SELECT 1 FROM notes WHERE task_id=? UNION SELECT 1 FROM edits WHERE task_id=?", rStr(r, "id"), rStr(r, "id"))
				if err != nil {
					return err
				}
				if owned != nil || rStr(r, "status") != "todo" {
					errors = append(errors, fmt.Sprintf("%s left the plan but has owner state; kept", rStr(r, "id")))
				} else if err := q.Run(ctx, "DELETE FROM tasks WHERE id=?", rStr(r, "id")); err != nil {
					return err
				}
			}
			rows, err := q.All(ctx, "SELECT id, after FROM tasks")
			if err != nil {
				return err
			}
			ids := map[string]bool{}
			for _, r := range rows {
				ids[rStr(r, "id")] = true
			}
			for _, r := range rows {
				for _, d := range strsOf(rJSON(r, "after")) {
					if !ids[d] {
						errors = append(errors, fmt.Sprintf("%s depends on unknown %s", rStr(r, "id"), d))
					}
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		ownedRows, err := c.dbAll(ctx, "SELECT task_id id FROM notes UNION SELECT task_id FROM edits UNION SELECT id FROM tasks WHERE status != 'todo'")
		if err != nil {
			return err
		}
		owned := map[string]bool{}
		var ownedOrder []string
		for _, r := range ownedRows {
			id := rStr(r, "id")
			if !owned[id] {
				owned[id] = true
				ownedOrder = append(ownedOrder, id)
			}
		}
		nowRows, err := c.dbAll(ctx, "SELECT id, title FROM tasks")
		if err != nil {
			return err
		}
		nowT := map[string]string{}
		for _, r := range nowRows {
			nowT[rStr(r, "id")] = rStr(r, "title")
		}
		var moved []MovedTask
		for _, m := range movedOf(c.metaJSON("plan_moved")) {
			if owned[m.ID] {
				if was, ok := nowT[m.ID]; !ok || was != deref(m.Was) {
					if _, ok := nowT[m.ID]; ok {
						moved = append(moved, m)
					}
				}
			}
		}
		movedHas := map[string]bool{}
		for _, m := range moved {
			movedHas[m.ID] = true
		}
		for _, id := range ownedOrder {
			if was, had := old[id]; had && !movedHas[id] {
				now, still := nowT[id]
				if still && now != was {
					var nowPtr *string
					if v, ok := nowT[id]; ok {
						nowPtr = sp(v)
					}
					moved = append(moved, MovedTask{ID: id, Was: sp(was), Now: nowPtr})
					movedHas[id] = true
				}
			}
		}
		movedValue := jarrNull()
		if len(moved) > 0 {
			items := make([]*jsfmt.Value, len(moved))
			for i, m := range moved {
				items[i] = jsfmt.Object([]string{"id", "was", "now"}, []*jsfmt.Value{jstr(m.ID), jstrPtr(m.Was), jstrPtr(m.Now)})
			}
			movedValue = jsfmt.Array(items)
		}
		if err := c.setMeta(ctx, nil, "plan_moved", sp(movedValue.EncodeJSON())); err != nil {
			return err
		}
		if err := c.setMeta(ctx, nil, "plan_at", sp(c.nowISO())); err != nil {
			return err
		}
		if err := c.Reindex(ctx, nil); err != nil {
			return err
		}
		c.emit("plan", jsfmt.Object([]string{"tasks"}, []*jsfmt.Value{jnum(int64(len(keep)))}))
		out = ImportResult{Phases: len(plan.Phases), Tasks: len(keep), Moved: moved, Errors: errors}
		return nil
	})
	return out, err
}

func movedOf(v *jsfmt.Value) []MovedTask {
	if v == nil || v.Kind() != jsfmt.KindArray {
		return nil
	}
	var out []MovedTask
	for _, item := range v.Items() {
		out = append(out, MovedTask{ID: strOf(objField(item, "id")), Was: strPtrOf(objField(item, "was")), Now: strPtrOf(objField(item, "now"))})
	}
	return out
}

// Reindex rebuilds the search index of the given tasks (all when nil), from their effective text.
func (c *Channel) Reindex(ctx context.Context, ids []string) error {
	return c.serial(ctx, func(ctx context.Context) error {
		if !c.FTS {
			return nil
		}
		var rows []Row
		if ids != nil {
			for _, id := range ids {
				r, err := c.rawTask(ctx, id)
				if err != nil {
					return err
				}
				if r != nil {
					rows = append(rows, r)
				}
			}
		} else {
			var err error
			rows, err = c.dbAll(ctx, "SELECT * FROM tasks")
			if err != nil {
				return err
			}
		}
		edits, err := c.editsBy(ctx)
		if err != nil {
			return err
		}
		docs := []store.TaskDoc{}
		for _, r := range rows {
			e, err := c.effective(r, edits[rStr(r, "id")])
			if err != nil {
				return err
			}
			var b builder
			b.keys = slices.Clone(e.Fields.Keys())
			b.fields = slices.Clone(e.Fields.Fields())
			drop := map[string]bool{"id": true, "title": true, "edited": true}
			var keys []string
			var fields []*jsfmt.Value
			for i, k := range b.keys {
				if !drop[k] {
					keys = append(keys, k)
					fields = append(fields, b.fields[i])
				}
			}
			docs = append(docs, store.TaskDoc{ID: e.ID, Title: e.Title, Body: jsfmt.Object(keys, fields).EncodeJSON()})
		}
		return c.tx(ctx, func(ctx context.Context, q store.Q) error {
			return c.Store.IndexTasks(ctx, q, docs, ids)
		})
	})
}

// Search finds tasks by their words, with « » snippets.
func (c *Channel) Search(ctx context.Context, q string) ([]SearchHit, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return []SearchHit{}, nil
	}
	c.dbMu.Lock()
	rows := c.Store.SearchTasks(ctx, q)
	c.dbMu.Unlock()
	if rows != nil {
		out := make([]SearchHit, len(rows))
		for i, r := range rows {
			out[i] = SearchHit{ID: rStr(r, "id"), Title: rStr(r, "title"), Hit: rStr(r, "hit")}
		}
		return out, nil
	}
	like := "%" + q + "%"
	fallback, err := c.dbAll(ctx, "SELECT id, title, substr(body,1,140) AS hit FROM tasks WHERE title LIKE ? OR body LIKE ? LIMIT 80", like, like)
	if err != nil {
		return nil, err
	}
	out := make([]SearchHit, len(fallback))
	for i, r := range fallback {
		out[i] = SearchHit{ID: rStr(r, "id"), Title: rStr(r, "title"), Hit: rStr(r, "hit")}
	}
	return out, nil
}

// Review lists everything the owner's review shows.
func (c *Channel) Review(ctx context.Context) (Review, error) {
	openRows, err := c.dbAll(ctx, `SELECT n.*, n.task_id AS step_id, t.title FROM notes n LEFT JOIN tasks t ON t.id=n.task_id WHERE n.resolved=0 ORDER BY n.task_id, n.id`)
	if err != nil {
		return Review{}, err
	}
	editRows, err := c.dbAll(ctx, `SELECT e.task_id AS step_id, e.field, e.at AS updated_at, e.by, t.title FROM edits e LEFT JOIN tasks t ON t.id=e.task_id ORDER BY e.task_id`)
	if err != nil {
		return Review{}, err
	}
	open := []ReviewOpenNote{}
	for _, r := range openRows {
		open = append(open, ReviewOpenNote{Note: noteOf(r), StepID: rStr(r, "task_id"), Title: rStrPtr(r, "title")})
	}
	edits := []ReviewEditItem{}
	for _, r := range editRows {
		edits = append(edits, ReviewEditItem{
			StepID: rStr(r, "step_id"), Field: rStr(r, "field"), UpdatedAt: rStr(r, "updated_at"),
			By: rStrPtr(r, "by"), Title: rStrPtr(r, "title"),
		})
	}
	return Review{Open: open, Edits: edits, Moved: c.metaJSON("plan_moved")}, nil
}

func noteOf(r Row) Note {
	return Note{
		ID: rInt(r, "id"), TaskID: rStr(r, "task_id"), Kind: rStr(r, "kind"), Body: rStr(r, "body"),
		By: rStrPtr(r, "by"), Resolved: rInt(r, "resolved"), CreatedAt: rStr(r, "created_at"),
	}
}

// ── knowledge: what one session learned, so the others do not pay for it again ──

// Remember adds one knowledge entry; kinds: fact (true about the code/system), lesson (a mistake
// not to repeat), decision (made, with why), context (a summary of files/state read), result
// (output of a finished piece of work), howto (a command sequence that works).
func (c *Channel) Remember(ctx context.Context, by string, k RememberInput) (KBEntry, error) {
	var out KBEntry
	err := c.serial(ctx, func(ctx context.Context) error {
		if _, err := c.need(ctx, by); err != nil {
			return err
		}
		if !slices.Contains(KBKinds, k.Kind) {
			return bad("kind must be one of " + strings.Join(KBKinds, ", "))
		}
		if strings.TrimSpace(k.Title) == "" || strings.TrimSpace(k.Body) == "" {
			return bad("need title and body")
		}
		if jsfmt.UTF16Len(k.Body) > 50000 {
			return bad("body ≤ 50000 chars; link the rest with refs")
		}
		if k.Task != nil && *k.Task != "" {
			r, err := c.rawTask(ctx, *k.Task)
			if err != nil {
				return err
			}
			if r == nil {
				return httpErr(404, fmt.Sprintf("no task %s", *k.Task))
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
		var row Row
		err := c.tx(ctx, func(ctx context.Context, q store.Q) error {
			r, err := q.Get(ctx, `INSERT INTO knowledge (by, kind, title, body, tags, refs, task_id, supersedes, created_at) VALUES (?,?,?,?,?,?,?,?,?) RETURNING *`,
				by, k.Kind, utf16Cut(strings.TrimSpace(k.Title), 200), strings.TrimSpace(k.Body), jstrs(tags).EncodeJSON(), jstrs(refs).EncodeJSON(),
				derefOrNil(k.Task), derefIntOrNil(k.Supersedes), c.nowISO())
			if err != nil {
				return err
			}
			row = r
			if err := c.Store.IndexKnowledge(ctx, q, r, tags); err != nil {
				return err
			}
			_, err = c.insert(ctx, q, by, evIn{
				topic: "kb.added", ref: sp(fmt.Sprintf("kb:%d", rInt(r, "id"))), msg: sp(fmt.Sprintf("[%s] %s", k.Kind, rStr(r, "title"))),
				data: jsfmt.Object([]string{"kb", "kind", "tags"}, []*jsfmt.Value{jnum(rInt(r, "id")), jstr(k.Kind), jstrs(tags)}),
			})
			return err
		})
		if err != nil {
			return err
		}
		c.flush()
		out = kbRow(row)
		return nil
	})
	return out, err
}

func kbRow(r Row) KBEntry {
	return KBEntry{
		ID: rInt(r, "id"), By: rStr(r, "by"), Kind: rStr(r, "kind"), Title: rStr(r, "title"),
		Body: rStr(r, "body"), Tags: strsOf(rJSON(r, "tags")), Refs: strsOf(rJSON(r, "refs")),
		Task: rStrPtr(r, "task_id"), Supersedes: rIntPtr(r, "supersedes"), CreatedAt: rStr(r, "created_at"),
		Hits: rInt(r, "hits"),
	}
}

// Recall searches the shared knowledge: search first, read second. Recall answers titles and a
// snippet; KB answers the body.
func (c *Channel) Recall(ctx context.Context, q string, o RecallOpts) ([]RecallItem, error) {
	lim := o.Limit
	if lim == 0 {
		lim = 10
	}
	lim = max(1, min(50, lim))
	retired, err := c.Retired(ctx)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(q)
	var rows []Row
	if trimmed != "" && c.FTS {
		c.dbMu.Lock()
		rows = c.Store.SearchKnowledge(ctx, trimmed, lim*3)
		c.dbMu.Unlock()
	} else if trimmed != "" {
		// a SQLite without FTS5: entries holding any of the words, newest first
		var words []string
		for i, w := range strings.Fields(trimmed) {
			if i >= 8 {
				break
			}
			escaped := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(w, `\`, `\\`), "%", `\%`), "_", `\_`)
			words = append(words, "%"+escaped+"%")
		}
		likes := []string{}
		params := []any{}
		for _, w := range words {
			like := "(title LIKE ? ESCAPE '\\' OR body LIKE ? ESCAPE '\\' OR tags LIKE ? ESCAPE '\\')"
			likes = append(likes, like)
			params = append(params, w, w, w)
		}
		params = append(params, lim*3)
		rows, err = c.dbAll(ctx, fmt.Sprintf(`SELECT *, substr(body,1,200) hit FROM knowledge WHERE %s ORDER BY id DESC LIMIT ?`, strings.Join(likes, " OR ")), params...)
		if err != nil {
			return nil, err
		}
	} else {
		rows, err = c.dbAll(ctx, "SELECT *, substr(body,1,200) hit FROM knowledge ORDER BY id DESC LIMIT ?", lim*3)
		if err != nil {
			return nil, err
		}
	}
	out := []RecallItem{}
	for _, r := range rows {
		by := rStr(r, "by")
		if retired[rInt(r, "id")] {
			continue
		}
		if o.Kind != "" && rStr(r, "kind") != o.Kind {
			continue
		}
		if o.By != "" && by != o.By && !strings.HasPrefix(by, o.By+".") {
			continue
		}
		if o.Tag != "" && !slices.Contains(strsOf(rJSON(r, "tags")), o.Tag) {
			continue
		}
		if len(out) >= lim {
			break
		}
		out = append(out, RecallItem{
			ID: rInt(r, "id"), Kind: rStr(r, "kind"), Title: rStr(r, "title"), By: by,
			Tags: strsOf(rJSON(r, "tags")), Task: rStrPtr(r, "task_id"), CreatedAt: rStr(r, "created_at"),
			Hit: rStr(r, "hit"),
		})
	}
	return out, nil
}

// KB reads one knowledge entry in full, counting the read.
func (c *Channel) KB(ctx context.Context, id int64) (KBView, error) {
	var out KBView
	err := c.serial(ctx, func(ctx context.Context) error {
		r, err := c.dbGet(ctx, "UPDATE knowledge SET hits = hits + 1 WHERE id=? RETURNING *", id)
		if err != nil {
			return err
		}
		if r == nil {
			return httpErr(404, fmt.Sprintf("no knowledge entry %d", id))
		}
		by, err := c.dbGet(ctx, "SELECT id FROM knowledge WHERE supersedes=?", id)
		if err != nil {
			return err
		}
		view := KBView{KBEntry: kbRow(r)}
		if by != nil {
			sup := rInt(by, "id")
			view.SupersededBy = &sup
		}
		out = view
		return nil
	})
	return out, err
}

// ── what a session sees when it joins ────────────────────────────────────────

// Snapshot is what one session sees: unread, inbox, the turn, the cast and its next task.
func (c *Channel) Snapshot(ctx context.Context, name string, j SnapshotJoin) (Snapshot, error) {
	u, err := c.Unread(ctx, name)
	if err != nil {
		return Snapshot{}, err
	}
	ib, err := c.Inbox(ctx, name)
	if err != nil {
		return Snapshot{}, err
	}
	control, err := c.ControlOf(ctx, name)
	if err != nil {
		return Snapshot{}, err
	}
	turn, err := c.Turn(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	sessions, err := c.Sessions(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	total, err := c.dbGet(ctx, fmt.Sprintf("SELECT %s n FROM knowledge", countInt))
	if err != nil {
		return Snapshot{}, err
	}
	latest, err := c.Recall(ctx, "", RecallOpts{Limit: 8})
	if err != nil {
		return Snapshot{}, err
	}
	var next *NextRef
	if name != Owner {
		n, done, err := c.Next(ctx, name)
		if err != nil {
			return Snapshot{}, err
		}
		if !done && n != nil {
			next = &NextRef{ID: n.ID, Title: n.Title, Ready: n.Ready, Unmet: n.Unmet}
		}
	}
	unread := []UnreadItem{}
	lo := max(0, len(u)-50)
	for _, e := range u[lo:] {
		unread = append(unread, UnreadItem{Seq: e.Seq, From: e.From, To: e.To, Topic: e.Topic, Msg: utf16CutPtr(e.Msg, 300)})
	}
	inbox := []InboxItem{}
	for _, e := range ib {
		inbox = append(inbox, InboxItem{Seq: e.Seq, From: e.From, Msg: deref(e.Msg), Task: strPtrOf(objField(e.Data, "task"))})
	}
	others := []OtherSession{}
	for _, s := range sessions {
		if s.Name != name && s.State != "left" {
			others = append(others, OtherSession{Name: s.Name, Role: s.Role, State: s.State, Task: s.Task, Stale: s.Stale, Control: s.Control})
		}
	}
	latestLines := []string{}
	for _, k := range latest {
		latestLines = append(latestLines, fmt.Sprintf("#%d [%s] %s (%s)", k.ID, k.Kind, k.Title, k.By))
	}
	skipped := 0
	if j.Skipped != nil {
		skipped = *j.Skipped
	}
	return Snapshot{
		Channel: c.Name, Me: name, Control: control, Turn: turn, Orchestrator: c.Config().Orchestrator,
		Context: j.Context, Skipped: skipped, Brief: j.Brief, Sessions: others,
		Knowledge: KnowledgeStat{Total: rInt(total, "n"), Latest: latestLines},
		Unread:    unread, UnreadTotal: len(u), Inbox: inbox, Next: next,
	}, nil
}

// Attention is everything that waits on the owner, in one read: asks addressed to the owner (or
// to all), paused and stale sessions, blocked tasks, and the next tasks gated owner/ask-first
// that are ready and not yet approved.
func (c *Channel) Attention(ctx context.Context) (Attention, error) {
	inbox, err := c.Inbox(ctx, Owner)
	if err != nil {
		return Attention{}, err
	}
	allSess, err := c.Sessions(ctx)
	if err != nil {
		return Attention{}, err
	}
	all, err := c.Tasks(ctx, TasksFilter{})
	if err != nil {
		return Attention{}, err
	}
	approvedRows, err := c.dbAll(ctx, "SELECT DISTINCT task_id FROM notes WHERE kind='direction' AND body LIKE 'Approved by %'")
	if err != nil {
		return Attention{}, err
	}
	raw, err := c.dbAll(ctx, "SELECT id, status_note FROM tasks WHERE status='blocked'")
	if err != nil {
		return Attention{}, err
	}
	openNotes, err := c.dbGet(ctx, fmt.Sprintf("SELECT %s n FROM notes WHERE resolved=0", countInt))
	if err != nil {
		return Attention{}, err
	}
	asks := []AskItem{}
	for _, e := range inbox {
		asks = append(asks, AskItem{Seq: e.Seq, From: e.From, Msg: deref(e.Msg), Ts: e.Ts, Task: strPtrOf(objField(e.Data, "task")), Ref: e.Ref})
	}
	approved := map[string]bool{}
	for _, r := range approvedRows {
		approved[rStr(r, "task_id")] = true
	}
	notes := map[string]string{}
	for _, r := range raw {
		notes[rStr(r, "id")] = rStr(r, "status_note")
	}
	gates := []GateItem{}
	for _, t := range all {
		if len(gates) >= 12 {
			break
		}
		gate := t.Gate
		if gate != "owner" && gate != "ask-first" {
			continue
		}
		if finished(t.Status) || len(t.BlockedBy) > 0 || approved[t.ID] {
			continue
		}
		gates = append(gates, GateItem{ID: t.ID, Title: t.Title, Gate: gate, Owner: t.Owner, Status: t.Status, PhaseN: t.PhaseN})
	}
	blocked := []BlockedItem{}
	for _, t := range all {
		if t.Status != "blocked" {
			continue
		}
		note := notes[t.ID]
		blocked = append(blocked, BlockedItem{ID: t.ID, Title: t.Title, Owner: t.Owner, Note: note, WaitsOn: t.BlockedBy})
	}
	next := []NextUp{}
	for _, s := range allSess {
		// a session that left holds no next task: only who is here does
		if s.Parent != nil || s.State == "left" {
			continue
		}
		n, done, err := c.Next(ctx, s.Name)
		if err != nil {
			return Attention{}, err
		}
		var task *NextTask
		if !done && n != nil {
			unmet := []string{}
			for _, u := range n.Unmet {
				unmet = append(unmet, u.ID)
			}
			task = &NextTask{ID: n.ID, Title: n.Title, Ready: n.Ready, Unmet: unmet}
		}
		next = append(next, NextUp{Session: s.Name, Task: task})
	}
	paused := []PausedItem{}
	stale := []StaleItem{}
	for _, s := range allSess {
		if s.Control == "pause" {
			paused = append(paused, PausedItem{Name: s.Name, By: s.ControlBy, At: s.ControlAt})
		}
		if s.Stale {
			stale = append(stale, StaleItem{Name: s.Name, LastSeen: s.LastSeen})
		}
	}
	status := map[string]string{}
	for _, t := range all {
		status[t.ID] = t.Status
	}
	unblockers := map[string]bool{}
	for _, t := range all {
		for _, d := range t.BlockedBy {
			unblockers[d] = true
		}
	}
	unblockedBy := 0
	for id := range unblockers {
		if st, ok := status[id]; ok && !finished(st) {
			unblockedBy++
		}
	}
	return Attention{
		Asks: asks, Gates: gates, Blocked: blocked, OpenNotes: rInt(openNotes, "n"),
		Paused: paused, Stale: stale, Next: next, UnblockedBy: unblockedBy,
		Total: len(asks) + len(gates) + len(blocked),
	}, nil
}

// Approve is the owner's go-ahead on a gated task: a note on the task plus a message to its
// owner.
func (c *Channel) Approve(ctx context.Context, id, msg, by string) (ApproveResult, error) {
	var out ApproveResult
	err := c.serial(ctx, func(ctx context.Context) error {
		t, err := c.rawTask(ctx, id)
		if err != nil {
			return err
		}
		if t == nil {
			return httpErr(404, fmt.Sprintf("no task %s", id))
		}
		who := by
		if by == Owner {
			who = "the owner"
		}
		noteMsg := fmt.Sprintf("Approved by %s", who)
		if msg != "" {
			noteMsg += ": " + msg
		}
		if _, err := c.Note(ctx, by, id, "direction", noteMsg); err != nil {
			return err
		}
		var to *string
		if owner := rStrPtr(t, "owner"); owner != nil && *owner != by && *owner != Owner {
			if s, err := c.Session(ctx, *owner); err == nil && s != nil {
				to = owner
			}
		}
		approveMsg := fmt.Sprintf("Approved %s %s", id, rStr(t, "title"))
		if msg != "" {
			approveMsg += ": " + msg
		}
		ev, err := c.append(ctx, by, evIn{
			topic: "msg", to: to, ref: sp(id), msg: sp(approveMsg),
			data: jsfmt.Object([]string{"task", "approved"}, []*jsfmt.Value{jstr(id), jbool(true)}),
		})
		if err != nil {
			return err
		}
		out = ApproveResult{OK: true, ID: id, Event: ev.Seq}
		return nil
	})
	return out, err
}

// Map is the big picture in one read: each phase's progress, what every session does now and
// next, and the critical path: the longest chain of unfinished tasks linked by dependencies.
func (c *Channel) Map(ctx context.Context) (MapView, error) {
	all, err := c.Tasks(ctx, TasksFilter{})
	if err != nil {
		return MapView{}, err
	}
	phases, err := c.Phases(ctx)
	if err != nil {
		return MapView{}, err
	}
	sess, err := c.Sessions(ctx)
	if err != nil {
		return MapView{}, err
	}
	open := map[string]*TaskListItem{}
	for i := range all {
		if !finished(all[i].Status) {
			open[all[i].ID] = &all[i]
		}
	}
	order := map[string]int{}
	for i, t := range all {
		order[t.ID] = i
	}
	depsOf := func(id string) []string {
		if t, ok := open[id]; ok {
			return t.Depends
		}
		return nil
	}
	// len(t) = tasks on the longest open chain ending at t; an imported plan can hold a cycle, so
	// a chain is never followed twice
	lenMemo := map[string]int64{}
	visiting := map[string]bool{}
	var L func(id string) int64
	L = func(id string) int64 {
		if v, ok := lenMemo[id]; ok {
			return v
		}
		if visiting[id] {
			return 0
		}
		visiting[id] = true
		var best int64
		for _, d := range depsOf(id) {
			if _, isOpen := open[d]; isOpen {
				if v := L(d); v > best {
					best = v
				}
			}
		}
		visiting[id] = false
		lenMemo[id] = 1 + best
		return 1 + best
	}
	end := ""
	for _, t := range all {
		if _, isOpen := open[t.ID]; !isOpen {
			continue
		}
		if end == "" || L(t.ID) > L(end) {
			end = t.ID
		}
	}
	var path []string
	for cur := end; cur != ""; {
		path = append([]string{cur}, path...)
		want := L(cur) - 1
		if want <= 0 {
			break
		}
		var cands []string
		for _, d := range depsOf(cur) {
			if _, isOpen := open[d]; isOpen && L(d) == want {
				cands = append(cands, d)
			}
		}
		sort.SliceStable(cands, func(i, j int) bool { return order[cands[i]] < order[cands[j]] })
		cur = ""
		if len(cands) > 0 {
			cur = cands[0]
		}
	}
	byPhase := func(n int64) []TaskListItem {
		var out []TaskListItem
		for _, t := range all {
			if t.PhaseN == n {
				out = append(out, t)
			}
		}
		return out
	}
	known := map[int64]bool{}
	phaseStats := []PhaseStat{}
	for _, p := range phases {
		known[p.N] = true
		done := 0
		tasks := byPhase(p.N)
		for _, t := range tasks {
			if finished(t.Status) {
				done++
			}
		}
		n := p.N
		phaseStats = append(phaseStats, PhaseStat{N: &n, Title: p.Title, Done: done, Total: len(tasks)})
	}
	var loose []TaskListItem
	for _, t := range all {
		if !known[t.PhaseN] {
			loose = append(loose, t)
		}
	}
	if len(loose) > 0 {
		done := 0
		for _, t := range loose {
			if finished(t.Status) {
				done++
			}
		}
		phaseStats = append(phaseStats, PhaseStat{N: nil, Title: "(no phase)", Done: done, Total: len(loose)})
	}
	sessions := []MapSession{}
	for _, s := range sess {
		if s.Parent != nil || s.State == "left" {
			continue
		}
		cur, err := c.Current(ctx, s.Name)
		if err != nil {
			return MapView{}, err
		}
		n, doneN, err := c.Next(ctx, s.Name)
		if err != nil {
			return MapView{}, err
		}
		var current *TaskRef
		if cur != nil {
			for _, t := range all {
				if t.ID == *cur {
					current = &TaskRef{ID: t.ID, Title: t.Title}
				}
			}
		}
		var next *TaskRefLite
		if !doneN && n != nil && (cur == nil || n.ID != *cur) {
			next = &TaskRefLite{ID: n.ID, Title: n.Title, Ready: n.Ready}
		}
		sessions = append(sessions, MapSession{Name: s.Name, State: s.State, Current: current, Next: next})
	}
	critical := []CriticalTask{}
	for _, id := range path {
		t := open[id]
		critical = append(critical, CriticalTask{ID: t.ID, Title: t.Title, Owner: t.Owner, Status: t.Status})
	}
	doneCount := 0
	for _, t := range all {
		if finished(t.Status) {
			doneCount++
		}
	}
	return MapView{
		Title: c.Config().Title, Orchestrator: c.Config().Orchestrator,
		Done: doneCount, Total: len(all), Phases: phaseStats, Sessions: sessions, CriticalPath: critical,
	}, nil
}

// Stats counts the channel in one read.
func (c *Channel) Stats(ctx context.Context) (Stats, error) {
	count := func(q string) (int64, error) {
		r, err := c.dbGet(ctx, q)
		if err != nil {
			return 0, err
		}
		return rInt(r, "n"), nil
	}
	events, err := count(fmt.Sprintf("SELECT %s n FROM events", countInt))
	if err != nil {
		return Stats{}, err
	}
	tasks, err := count(fmt.Sprintf("SELECT %s n FROM tasks", countInt))
	if err != nil {
		return Stats{}, err
	}
	done, err := count(fmt.Sprintf("SELECT %s n FROM tasks WHERE status IN ('done','skipped')", countInt))
	if err != nil {
		return Stats{}, err
	}
	sessions, err := count(fmt.Sprintf("SELECT %s n FROM sessions WHERE state != 'left'", countInt))
	if err != nil {
		return Stats{}, err
	}
	knowledge, err := count(fmt.Sprintf("SELECT %s n FROM knowledge", countInt))
	if err != nil {
		return Stats{}, err
	}
	openAsks, err := count(fmt.Sprintf("SELECT %s n FROM events e WHERE needs_reply=1 AND NOT EXISTS (SELECT 1 FROM events r WHERE r.reply_to=e.seq)", countInt))
	if err != nil {
		return Stats{}, err
	}
	last, err := c.LastSeq(ctx)
	if err != nil {
		return Stats{}, err
	}
	return Stats{Events: events, Tasks: tasks, Done: done, Sessions: sessions, Knowledge: knowledge, OpenAsks: openAsks, Last: last}, nil
}

// PlanFromValue reads a plan out of its JSON shape: {phases: [{n, title, …, steps: [{id, title,
// owner, status, depends, …}]}]}.
func PlanFromValue(v *jsfmt.Value) (Plan, error) {
	plan := Plan{}
	if v == nil || v.Kind() != jsfmt.KindObject {
		return plan, nil
	}
	for _, phaseValue := range itemsOf(objField(v, "phases")) {
		var p PlanPhase
		var b builder
		if phaseValue != nil && phaseValue.Kind() == jsfmt.KindObject {
			for i, k := range phaseValue.Keys() {
				if k == "steps" || k == "n" || k == "title" {
					continue
				}
				b.set(k, phaseValue.Fields()[i])
			}
		}
		if n := objField(phaseValue, "n"); n != nil && n.Kind() == jsfmt.KindNumber {
			if parsed, err := strconv.ParseInt(n.EncodeJSON(), 10, 64); err == nil {
				p.N = &parsed
			}
		}
		p.Title = strOf(objField(phaseValue, "title"))
		p.Body = b.obj()
		for _, stepValue := range itemsOf(objField(phaseValue, "steps")) {
			var st PlanStep
			var sb builder
			if stepValue != nil && stepValue.Kind() == jsfmt.KindObject {
				for i, k := range stepValue.Keys() {
					if k == "id" || k == "title" || k == "status" || k == "depends" || k == "owner" {
						continue
					}
					sb.set(k, stepValue.Fields()[i])
				}
			}
			st.ID = strOf(objField(stepValue, "id"))
			st.Title = strOf(objField(stepValue, "title"))
			st.Status = strOf(objField(stepValue, "status"))
			st.Depends = strsOf(objField(stepValue, "depends"))
			st.Owner = strPtrOf(objField(stepValue, "owner"))
			st.Body = sb.obj()
			p.Steps = append(p.Steps, st)
		}
		plan.Phases = append(plan.Phases, p)
	}
	return plan, nil
}

// bodyOrEmpty answers an object value for a plan part's free-form body (nil is an empty one).
func bodyOrEmpty(v *jsfmt.Value) *jsfmt.Value {
	if v == nil || v.Kind() != jsfmt.KindObject {
		return jsfmt.Object(nil, nil)
	}
	return v
}

func itemsOf(v *jsfmt.Value) []*jsfmt.Value {
	if v == nil || v.Kind() != jsfmt.KindArray {
		return nil
	}
	return v.Items()
}
