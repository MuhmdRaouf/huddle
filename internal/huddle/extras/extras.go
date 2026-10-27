// Package extras holds what the server adds around a channel for the owner, in one place:
// the approval rules (and the requests a session's hook posts against them), the desktop
// notifications for what needs the owner, the session links, and Radar's alerts and costs.
//
// The routes live under /api/c/<ch>/x/…: rules, approval, approvals, approvals/dismiss, radar,
// digest and link; plus the server-wide settings ({notify}, {capture}) the dashboard switches.
package extras

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/channel"
	"github.com/MuhmdRaouf/huddle/internal/huddle/outside"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
	"github.com/MuhmdRaouf/huddle/internal/jsscalar"
)

func bad(msg string) error {
	return &channel.HuddleError{Status: 400, Msg: msg}
}

// ── reading values and rows ───────────────────────────────────────────────────

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

// strOf reads a value as a string, "" for anything else.
func strOf(v *jsfmt.Value) string {
	if v == nil || v.Kind() != jsfmt.KindString {
		return ""
	}
	return v.Text()
}

// strsOf reads an array value as strings, [] for anything else.
func strsOf(v *jsfmt.Value) []string {
	if v == nil || v.Kind() != jsfmt.KindArray {
		return []string{}
	}
	out := make([]string, len(v.Items()))
	for i, item := range v.Items() {
		out[i] = strOf(item)
	}
	return out
}

// strAnyOf renders one value the way a template literal does: a string as it is, a number as
// JavaScript prints it, null as "null", anything else as an object.
func strAnyOf(v *jsfmt.Value) string {
	if v == nil || v.Kind() == jsfmt.KindNull {
		return "null"
	}
	switch v.Kind() {
	case jsfmt.KindString:
		return v.Text()
	case jsfmt.KindNumber:
		f, err := strconv.ParseFloat(v.EncodeJSON(), 64)
		if err != nil {
			return v.EncodeJSON()
		}
		return jsscalar.JSNumber(f)
	case jsfmt.KindBool:
		return v.EncodeJSON()
	default:
		return "[object Object]"
	}
}

// refOf renders a nullable reference the way a template literal does: a missing ref as "null".
func refOf(p *string) string {
	if p == nil {
		return "null"
	}
	return *p
}

// truthy answers whether a value counts in an if: false, 0, "", null and nothing do not.
func truthy(v *jsfmt.Value) bool {
	if v == nil || v.Kind() == jsfmt.KindNull {
		return false
	}
	switch v.Kind() {
	case jsfmt.KindBool:
		return v.EncodeJSON() == "true"
	case jsfmt.KindNumber:
		f, err := strconv.ParseFloat(v.EncodeJSON(), 64)
		return err == nil && f != 0
	case jsfmt.KindString:
		return v.Text() != ""
	default:
		return true
	}
}

// jsInt reads a value as an integer, false for anything that is not one.
func jsInt(v *jsfmt.Value) (int64, bool) {
	if v == nil || v.Kind() != jsfmt.KindNumber {
		return 0, false
	}
	n, err := strconv.ParseInt(v.EncodeJSON(), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// jsSeqOf reads a dismissal's seq: an integer, or a string holding one.
func jsSeqOf(v *jsfmt.Value) (int64, bool) {
	if v != nil && v.Kind() == jsfmt.KindString {
		f, err := strconv.ParseFloat(v.Text(), 64)
		if err != nil || f != float64(int64(f)) {
			return 0, false
		}
		v, _ = jsfmt.DecodeValue([]byte(strconv.FormatFloat(f, 'f', -1, 64)))
	}
	n, ok := jsInt(v)
	if !ok || n <= 0 {
		return 0, false
	}
	return n, true
}

func jstrs(items []string) *jsfmt.Value {
	out := make([]*jsfmt.Value, len(items))
	for i, s := range items {
		out[i] = jsfmt.String(s)
	}
	return jsfmt.Array(out)
}

func jnum(n int64) *jsfmt.Value {
	v, _ := jsfmt.DecodeValue([]byte(strconv.FormatInt(n, 10)))
	return v
}

func rowStr(r channel.Row, k string) string {
	s, _ := r[k].(string)
	return s
}

func rowInt(r channel.Row, k string) int64 {
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

// rowJSON reads one JSON column; nil when it is NULL or not JSON.
func rowJSON(r channel.Row, k string) *jsfmt.Value {
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

// metaJSON reads one meta value as JSON; nil when it is not set or not JSON.
func metaJSON(ch *channel.Channel, k string) *jsfmt.Value {
	if s := ch.Meta(k); s != "" {
		if v, err := jsfmt.DecodeValue([]byte(s)); err == nil {
			return v
		}
	}
	return nil
}

// metaInt reads one meta value as a number, 0 when it is not one.
func metaInt(ch *channel.Channel, k string) int64 {
	if n, err := strconv.ParseInt(ch.Meta(k), 10, 64); err == nil {
		return n
	}
	return 0
}

// isoOfMs renders epoch ms the way the channel's own timestamps read: UTC, seconds.
func isoOfMs(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05Z")
}

// ── approval rules ───────────────────────────────────────────────────────────

// RuleView is one approval rule as the dashboard reads it: its switch, and the default it ships
// with.
type RuleView struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Help    string `json:"help"`
	On      bool   `json:"on"`
	Default bool   `json:"default"`
}

// RulesAnswer is the rules routes' answer.
type RulesAnswer struct {
	Rules []RuleView `json:"rules"`
}

// RulesOf is the channel's switches: its stored ones over the defaults.
func RulesOf(ch *channel.Channel) []channel.Switch {
	return channel.Effective(metaJSON(ch, "approval_rules"))
}

// RulesList lists the approval rules in their fixed order, each with its switch.
func RulesList(ch *channel.Channel) []RuleView {
	on := map[string]bool{}
	for _, s := range RulesOf(ch) {
		on[s.ID] = s.On
	}
	out := make([]RuleView, len(channel.Rules))
	for i, r := range channel.Rules {
		out[i] = RuleView{ID: r.ID, Label: r.Label, Help: r.Help, On: on[r.ID], Default: r.On}
	}
	return out
}

// SetRules validates and stores a switch patch ({<rule id>: true|false}), answering the rules as
// they stand. Only whole switches land: the stored object carries every rule, in their order.
func SetRules(ctx context.Context, ch *channel.Channel, patch *jsfmt.Value) ([]RuleView, error) {
	if patch == nil || (patch.Kind() != jsfmt.KindObject && patch.Kind() != jsfmt.KindArray) {
		return nil, bad("rules: {<rule id>: true|false}")
	}
	known := map[string]bool{}
	var ids []string
	for _, s := range channel.Defaults() {
		known[s.ID] = true
		ids = append(ids, s.ID)
	}
	cur := map[string]bool{}
	for _, s := range RulesOf(ch) {
		cur[s.ID] = s.On
	}
	if patch.Kind() == jsfmt.KindObject {
		for i, k := range patch.Keys() {
			if !known[k] {
				return nil, bad(fmt.Sprintf("no approval rule %s (rules: %s)", k, strings.Join(ids, ", ")))
			}
			f := patch.Fields()[i]
			if f == nil || f.Kind() != jsfmt.KindBool {
				return nil, bad(fmt.Sprintf("rule %s: true or false", k))
			}
			cur[k] = f.EncodeJSON() == "true"
		}
	}
	keys := make([]string, len(channel.Rules))
	vals := make([]*jsfmt.Value, len(channel.Rules))
	for i, r := range channel.Rules {
		keys[i] = r.ID
		vals[i] = jbool(cur[r.ID])
	}
	stored := jsfmt.Object(keys, vals).EncodeJSON()
	if err := ch.Serial(ctx, func(ctx context.Context) error {
		return ch.SetMeta(ctx, "approval_rules", &stored)
	}); err != nil {
		return nil, err
	}
	return RulesList(ch), nil
}

func jbool(b bool) *jsfmt.Value {
	v, _ := jsfmt.DecodeValue([]byte("false"))
	if b {
		v, _ = jsfmt.DecodeValue([]byte("true"))
	}
	return v
}

// ── approval requests ────────────────────────────────────────────────────────

// ApprovalAnswer is what a session's permission request means for the asking hook: ask, with the
// rules that matched and a short reason for the prompt, or no ask at all.
type ApprovalAnswer struct {
	Ask    bool     `json:"ask"`
	Rules  []string `json:"rules,omitempty"`
	Labels []string `json:"labels,omitempty"`
	Event  int64    `json:"event,omitempty"`
	Reason string   `json:"reason,omitempty"`
}

// Approval records a session's permission request when a rule that is on matches its command. The
// command itself is never stored, logged or shown: it may hold a secret.
func Approval(ctx context.Context, ch *channel.Channel, as string, b *jsfmt.Value) (ApprovalAnswer, error) {
	if as == "" || as == channel.Owner || !channel.NameRe.MatchString(as) {
		return ApprovalAnswer{}, bad("say which session asks: ?as=<session>")
	}
	s, err := ch.Session(ctx, as)
	if err != nil {
		return ApprovalAnswer{}, err
	}
	if s == nil {
		return ApprovalAnswer{}, &channel.HuddleError{
			Status: 404, Msg: fmt.Sprintf("%q has not joined channel %s", as, ch.Name),
		}
	}
	channel.Link(ctx, ch, as, strOf(objField(b, "claude_session")))
	on := map[string]bool{}
	for _, sw := range RulesOf(ch) {
		on[sw.ID] = sw.On
	}
	var hit []channel.Rule
	for _, r := range channel.Matching(strAnyOf(objField(b, "command")), strOf(objField(b, "cwd"))) {
		if on[r.ID] {
			hit = append(hit, r)
		}
	}
	if len(hit) == 0 {
		return ApprovalAnswer{Ask: false}, nil
	}
	var ids, labels []string
	for _, r := range hit {
		ids = append(ids, r.ID)
		labels = append(labels, r.Label)
	}
	msg := fmt.Sprintf("%s asks permission: %s", as, strings.Join(labels, ", "))
	data := jsfmt.Object([]string{"rules", "labels"}, []*jsfmt.Value{jstrs(ids), jstrs(labels)})
	to := channel.Owner
	ev, err := ch.AppendEvent(ctx, as, channel.EventIn{
		Topic: "approval.request", To: &to, Msg: &msg, Data: data,
	})
	if err != nil {
		return ApprovalAnswer{}, err
	}
	return ApprovalAnswer{
		Ask: true, Rules: ids, Labels: labels, Event: ev.Seq,
		Reason: fmt.Sprintf("Huddle (channel %s): the owner asked to approve %s — allow only if this is intended.",
			ch.Name, strings.ToLower(strings.Join(labels, " and "))),
	}, nil
}

// ApprovalItem is one approval request waiting for the owner.
type ApprovalItem struct {
	Seq    int64    `json:"seq"`
	From   string   `json:"from"`
	Ts     string   `json:"ts"`
	Rules  []string `json:"rules"`
	Labels []string `json:"labels"`
}

// ApprovalsAnswer is the approvals route's answer.
type ApprovalsAnswer struct {
	Approvals []ApprovalItem `json:"approvals"`
}

// Approvals lists the approval requests of the last day the owner has not dismissed, newest
// first.
func Approvals(ctx context.Context, ch *channel.Channel) ([]ApprovalItem, error) {
	cleared := metaInt(ch, "approvals_cleared")
	dismissed := map[int64]bool{}
	if v := metaJSON(ch, "approvals_dismissed"); v != nil && v.Kind() == jsfmt.KindArray {
		for _, item := range v.Items() {
			if n, ok := jsInt(item); ok {
				dismissed[n] = true
			}
		}
	}
	since := isoOfMs(ch.Now().UnixMilli() - 86_400_000)
	rows, err := ch.Store.All(ctx,
		"SELECT * FROM events WHERE topic='approval.request' AND seq > ? AND ts >= ? ORDER BY seq DESC LIMIT 50",
		cleared, since)
	if err != nil {
		return nil, err
	}
	out := []ApprovalItem{}
	for _, r := range rows {
		seq := rowInt(r, "seq")
		if dismissed[seq] {
			continue
		}
		d := rowJSON(r, "data")
		out = append(out, ApprovalItem{
			Seq: seq, From: rowStr(r, "from_name"), Ts: rowStr(r, "ts"),
			Rules: strsOf(objField(d, "rules")), Labels: strsOf(objField(d, "labels")),
		})
	}
	return out, nil
}

// Dismissal is the owner's dismissal answer: what is still open.
type Dismissal struct {
	Approvals []ApprovalItem `json:"approvals"`
}

// Dismiss hides one approval request (seq), or all of them (all); the last 300 single
// dismissals are remembered.
func Dismiss(ctx context.Context, ch *channel.Channel, b *jsfmt.Value) (Dismissal, error) {
	err := ch.Serial(ctx, func(ctx context.Context) error {
		if truthy(objField(b, "all")) {
			last, err := ch.LastSeq(ctx)
			if err != nil {
				return err
			}
			v := strconv.FormatInt(last, 10)
			return ch.SetMeta(ctx, "approvals_cleared", &v)
		}
		seq, ok := jsSeqOf(objField(b, "seq"))
		if !ok {
			return bad("dismiss: {seq} or {all: true}")
		}
		var list []int64
		if v := metaJSON(ch, "approvals_dismissed"); v != nil && v.Kind() == jsfmt.KindArray {
			for _, item := range v.Items() {
				if n, ok := jsInt(item); ok {
					list = append(list, n)
				}
			}
		}
		if !slices.Contains(list, seq) {
			list = append(list, seq)
		}
		if len(list) > 300 {
			list = list[len(list)-300:]
		}
		items := make([]*jsfmt.Value, len(list))
		for i, n := range list {
			items[i] = jnum(n)
		}
		v := jsfmt.Array(items).EncodeJSON()
		return ch.SetMeta(ctx, "approvals_dismissed", &v)
	})
	if err != nil {
		return Dismissal{}, err
	}
	open, err := Approvals(ctx, ch)
	if err != nil {
		return Dismissal{}, err
	}
	return Dismissal{Approvals: open}, nil
}

// ── radar, for this channel ──────────────────────────────────────────────────

// RadarAlertView is one of Radar's alerts on a session of this channel.
type RadarAlertView struct {
	ID      string   `json:"id"`
	Kind    string   `json:"kind"`
	Session string   `json:"session"`
	Agent   *string  `json:"agent"`
	Since   float64  `json:"since"`
	Detail  string   `json:"detail"`
	Cost    *float64 `json:"cost"`
}

// RadarView is Radar's part of the channel: its alerts here, and the cost per session over a day.
type RadarView struct {
	Available bool             `json:"available"`
	Range     string           `json:"range"`
	Alerts    []RadarAlertView `json:"alerts"`
	// Cost is nil (null in JSON) when Radar answered no costs, else the cost by Huddle name with
	// the names in the order Radar's attribution rows name them.
	Cost  *jsfmt.Value `json:"cost"`
	Total *float64     `json:"total"`
}

// RadarMissing is the answer when Radar is not there: the flag alone, no error.
type RadarMissing struct {
	Available bool `json:"available"`
}

// RadarOf answers Radar's alerts for this channel's sessions and the estimated cost per session,
// folded onto Huddle names through the session links.
func RadarOf(ctx context.Context, ch *channel.Channel) (any, error) {
	var alerts []outside.Alert
	var costs *outside.SessionCosts
	alertsOK, costsOK := false, false
	if r := ch.Radar; r != nil {
		alerts, alertsOK = r.Alerts(ctx)
		costs, costsOK = r.CostBySession(ctx, outside.RangeDay)
	}
	if !alertsOK && !costsOK {
		return RadarMissing{Available: false}, nil
	}
	L, err := channel.Links(ctx, ch)
	if err != nil {
		return nil, err
	}
	view := RadarView{Available: true, Range: "day", Alerts: []RadarAlertView{}}
	for _, a := range alerts {
		name, linked := L[a.SessionID]
		if !linked {
			continue
		}
		view.Alerts = append(view.Alerts, RadarAlertView{
			ID: a.ID, Kind: a.Kind, Session: name, Agent: a.AgentID, Since: a.Since,
			Detail: jsscalar.UTF16Slice(a.Detail, 0, 300), Cost: a.CostUSD,
		})
	}
	if costsOK {
		var names []string
		byName := map[string]float64{}
		total := 0.0
		for _, cid := range costs.Keys() {
			name, linked := L[cid]
			if !linked {
				continue
			}
			if _, seen := byName[name]; !seen {
				names = append(names, name)
				byName[name] = 0
			}
			byName[name] += costs.USD(cid)
			total += costs.USD(cid)
		}
		usd := make([]*jsfmt.Value, len(names))
		for i, n := range names {
			usd[i] = jfloat(byName[n])
		}
		view.Cost = jsfmt.Object(names, usd)
		view.Total = &total
	}
	return view, nil
}

// jfloat answers one number value, printed the way JavaScript prints the double.
func jfloat(f float64) *jsfmt.Value {
	v, _ := jsfmt.DecodeValue([]byte(jsscalar.JSNumber(f)))
	return v
}

// ── needs-you notifications ──────────────────────────────────────────────────

// Notification is what an event means for the owner's desktop: names and ids only, never a
// message body.
type Notification struct {
	Kind    string `json:"kind"`
	Subject string `json:"subject"`
	Title   string `json:"title"`
	Body    string `json:"body"`
}

// NeedsYou answers what an event means for the owner's desktop, nil when it means nothing: a
// question to the owner, a pause by someone else, an approval request, a blocked task. The
// owner's own moves never notify.
func NeedsYou(e channel.Ev, channelName string) *Notification {
	if e.From == channel.Owner {
		return nil
	}
	title := "Huddle · " + channelName
	if e.NeedsReply && (e.To == nil || *e.To == channel.Owner) {
		about := ""
		if task := objField(e.Data, "task"); truthy(task) {
			about = " about task " + strAnyOf(task)
		}
		return &Notification{
			Kind: "ask", Subject: channelName + "/" + e.From, Title: title,
			Body: e.From + " asks you a question" + about,
		}
	}
	switch {
	case e.Topic == "control.pause" && e.To != nil:
		return &Notification{
			Kind: "pause", Subject: channelName + "/" + *e.To, Title: title,
			Body: fmt.Sprintf("%s was paused by %s", *e.To, e.From),
		}
	case e.Topic == "approval.request":
		rules := strsOf(objField(e.Data, "rules"))
		labels := strings.Join(strsOf(objField(e.Data, "labels")), ", ")
		if labels == "" {
			labels = "a command"
		}
		return &Notification{
			Kind: "approval", Subject: channelName + "/" + e.From + "/" + strings.Join(rules, ","),
			Title: title,
			Body:  fmt.Sprintf("%s asks permission: %s. Answer in its Claude Code window.", e.From, labels),
		}
	case e.Topic == "task.status" && strOf(objField(e.Data, "status")) == "blocked":
		return &Notification{
			Kind: "blocked", Subject: channelName + "/" + refOf(e.Ref), Title: title,
			Body: fmt.Sprintf("Task %s is blocked (%s)", refOf(e.Ref), e.From),
		}
	}
	return nil
}

// Watcher notifies the owner's desktop of what needs them as it happens. One watcher serves a
// server: every channel it watches is subscribed once.
type Watcher struct {
	n *outside.Notifier

	mu   sync.Mutex
	seen map[*channel.Channel]bool
}

// NewWatcher answers a watcher showing its notifications through n.
func NewWatcher(n *outside.Notifier) *Watcher {
	return &Watcher{n: n, seen: map[*channel.Channel]bool{}}
}

// Watch watches one channel: the first call subscribes, later ones do nothing.
func (w *Watcher) Watch(ch *channel.Channel) {
	w.mu.Lock()
	if w.seen[ch] {
		w.mu.Unlock()
		return
	}
	w.seen[ch] = true
	w.mu.Unlock()
	ch.Subscribe(func(m channel.Live) {
		if m.Type != "event" {
			return
		}
		ev, ok := m.Data.(channel.Ev)
		if !ok {
			return
		}
		title := ch.Config().Title
		if title == "" {
			title = ch.Name
		}
		if x := NeedsYou(ev, title); x != nil {
			w.n.Send(x.Kind, x.Subject, x.Title, x.Body)
		}
	})
}

// WatchHub watches every channel the hub answers.
func (w *Watcher) WatchHub(h *channel.Hub) {
	h.OnGet(func(ch *channel.Channel) { w.Watch(ch) })
}

// ── routes ───────────────────────────────────────────────────────────────────

// OKAnswer confirms a route's action.
type OKAnswer struct {
	OK bool `json:"ok"`
}

// Route answers /api/c/<ch>/x/<rest>: ok is false when the path is not extras'. Since is the
// digest window the query names ("" for none); body is the request's JSON, nil for none.
func Route(ctx context.Context, ch *channel.Channel, rest, method, since, as string, body *jsfmt.Value) (any, bool, error) {
	owner := func() error {
		if as != channel.Owner {
			return &channel.HuddleError{
				Status: 403, Msg: "only the owner changes this (the dashboard, or ?as=owner)",
			}
		}
		return nil
	}
	post := method == http.MethodPost
	switch {
	case rest == "rules" && !post:
		return RulesAnswer{Rules: RulesList(ch)}, true, nil
	case rest == "rules" && post:
		if err := owner(); err != nil {
			return nil, true, err
		}
		patch := objField(body, "rules")
		if patch == nil || patch.Kind() == jsfmt.KindNull {
			patch = jsfmt.Object(nil, nil)
		}
		rules, err := SetRules(ctx, ch, patch)
		if err != nil {
			return nil, true, err
		}
		return RulesAnswer{Rules: rules}, true, nil
	case rest == "approval" && post:
		r, err := Approval(ctx, ch, as, body)
		return r, true, err
	case rest == "approvals" && !post:
		open, err := Approvals(ctx, ch)
		return ApprovalsAnswer{Approvals: open}, true, err
	case rest == "approvals/dismiss" && post:
		if err := owner(); err != nil {
			return nil, true, err
		}
		d, err := Dismiss(ctx, ch, body)
		return d, true, err
	case rest == "radar" && !post:
		v, err := RadarOf(ctx, ch)
		return v, true, err
	case rest == "digest" && !post:
		d, err := channel.Digest(ctx, ch, since, channel.DigestOpts{})
		return d, true, err
	case rest == "link" && post:
		channel.Link(ctx, ch, as, strOf(objField(body, "claude_session")))
		return OKAnswer{OK: true}, true, nil
	}
	return nil, false, nil
}

// SettingsAnswer is the hub's settings: the notify switch, the display time zone and the
// router's body capture switch.
type SettingsAnswer struct {
	outside.State
	outside.HubZone
	Capture bool `json:"capture"`
}

// Settings answers the server-wide settings, the owner's to change: a POST turns the
// notifications or the body capture on or off, a PUT picks the display time zone. A POST has
// one switch only, and names it in full: {notify: true|false} or {capture: true|false}.
func Settings(method string, n *outside.Notifier, z *outside.Zones, c *outside.Capture, me string, body *jsfmt.Value) (SettingsAnswer, error) {
	if method != http.MethodPost && method != http.MethodPut {
		return SettingsAnswer{State: n.State(), HubZone: z.Get(), Capture: c.On()}, nil
	}
	if me != channel.Owner {
		return SettingsAnswer{}, &channel.HuddleError{
			Status: 403, Msg: "only the owner (the dashboard, or the session that started Huddle) changes settings",
		}
	}
	if method == http.MethodPut {
		zone := objField(body, "zone")
		if zone == nil || zone.Kind() != jsfmt.KindString {
			return SettingsAnswer{}, bad("settings: {zone: \"<IANA zone name>\"}")
		}
		picked, err := z.Set(strOf(zone))
		if err != nil {
			return SettingsAnswer{}, bad(err.Error())
		}
		return SettingsAnswer{State: n.State(), HubZone: picked, Capture: c.On()}, nil
	}
	notify, capture := objField(body, "notify"), objField(body, "capture")
	if (notify != nil) == (capture != nil) ||
		(notify != nil && notify.Kind() != jsfmt.KindBool) ||
		(capture != nil && capture.Kind() != jsfmt.KindBool) {
		return SettingsAnswer{}, bad("settings: {notify: true|false} or {capture: true|false}")
	}
	if capture == nil {
		return SettingsAnswer{State: n.Set(notify.EncodeJSON() == "true"), HubZone: z.Get(), Capture: c.On()}, nil
	}
	return SettingsAnswer{State: n.State(), HubZone: z.Get(), Capture: c.Set(capture.EncodeJSON() == "true")}, nil
}
