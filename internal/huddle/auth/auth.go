// Package auth decides who may talk to the huddle server, kubeadm style. A server started with
// a state file (<home>/auth.json, 0600) remembers its members, roots, browsers and unused
// invites across restarts and reboots, as sha256 digests only: no secret ever reaches the disk.
// A kick revokes at once, an invite expires on its TTL, and a login code (5 min) is never kept.
//
// The root is the credential of the session that started the server: it acts as anyone, the
// owner included, and mints invites; the digests of the last few roots are kept, so the
// creator's credential still works after a restart. A session that presents an invite once gets
// its own credential, bound to its name, which acts as that name or its subagents
// (<name>.<role>). A one-time login code, which any session holding a credential mints for
// itself, is redeemed for a cookie that acts as the owner in the channels — the dashboard: one
// the root signed in has the root's rights, one a member signed in has no admin rights (no
// invites, members, kicks or login codes) and stops working when that member is kicked.
// Clients send a credential in the header x-huddle-token; a browser sends its cookie. Secrets
// are compared in constant time; only an invite's id (its public half) is ever listed or logged.
package auth

import (
	crand "crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/domain"
	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// Header carries a session's credential; a browser sends its cookie instead.
const Header = "x-huddle-token"

// InviteRe is an invite token's shape: "<id>.<secret>", id and secret in [a-z0-9].
var InviteRe = regexp.MustCompile(`^([a-z0-9]{6})\.([a-z0-9]{16})$`)

const (
	// Day is an invite's default TTL, in seconds.
	Day int64 = 86_400

	rootsKept    = 8               // root digests kept, so a restart's old roots still work
	browsersKept = 64              // browser digests kept, the latest ones
	issuedKept   = 1000            // invite ids kept in the file, for redaction
	codeWindow   = 5 * time.Minute // a login code's life
	flushDelay   = time.Minute     // how long a changed "last seen" waits before it is written
	noInvite     = "\x00no invite" // compared against when the id is unknown, so a miss costs the same
	stateNote    = "Huddle's members, roots, browsers and unused invites: sha256 digests only, never a secret"
)

// Who a credential is: the owner (a root or a browser), a member, or nobody. Browser names the
// member whose login code signed this browser in (empty for sessions and the root's browsers);
// Device names the paired device whose passkey signed this session in; Satellite names the
// machine whose credential the hub listener verified. Kind names which of those this is, for the
// page that asks who it is.
type Who struct {
	Name      string `json:"name"`
	Kind      string `json:"kind,omitempty"`
	Root      bool   `json:"root"`
	Invite    bool   `json:"invite"`
	Browser   string `json:"browser,omitempty"`
	Device    string `json:"device,omitempty"`
	Satellite string `json:"satellite,omitempty"`
}

// The kinds a Who carries: the owner's link session or root token, a paired device's session, a
// member's token or browser, a satellite machine's credential.
const (
	KindOwner     = "owner"
	KindDevice    = "device"
	KindMember    = "member"
	KindSatellite = "satellite"
)

// InviteOptions is a new invite; TTL is seconds, nil the 24 h default and 0 no expiry.
type InviteOptions struct {
	TTL         *int64
	Single      bool
	Invite      bool
	Channel     string
	Description string
	By          string
}

// InviteView is an invite as listed or handed out; Token is set only at creation, once.
type InviteView struct {
	Token       string  `json:"token,omitempty"`
	ID          string  `json:"id"`
	Created     string  `json:"created"`
	Expires     *string `json:"expires"`
	SingleUse   bool    `json:"single_use"`
	Uses        int     `json:"uses"`
	CanInvite   bool    `json:"can_invite"`
	Channel     *string `json:"channel"`
	Description *string `json:"description"`
	By          string  `json:"by"`
}

// MemberView is a member as listed; Invite is the id of the invite it joined with.
type MemberView struct {
	Name      string `json:"name"`
	Since     string `json:"since"`
	Seen      string `json:"seen"`
	CanInvite bool   `json:"can_invite"`
	Invite    string `json:"invite"`
}

// JoinResult is a join's outcome: a fresh credential for the name, or Err ("invalid token").
type JoinResult struct {
	Credential string
	Name       string
	Channel    *string
	Err        string
}

// PeekResult is what a join that asks for a name of its own needs to know.
type PeekResult struct {
	Channel *string
	Members []string
}

// digest is a credential's stored form: its sha256. Only digests are persisted or compared.
type digest = [32]byte

type invite struct {
	id          string
	secret      digest
	created     int64
	expires     *int64 // nil: no expiry
	single      bool
	uses        int
	invite      bool // a member joining with it may mint invites
	channel     string
	description string
	by          string
}

type member struct {
	name   string
	cred   digest
	since  int64
	seen   int64
	invite bool
	via    string // the invite id it joined with
}

// browser is a cookie credential; by names the member who signed it in (nil: the root).
type browser struct {
	cred digest
	by   *string
}

// loginCode is a pending code; by names the member who asked for it (nil: the root).
type loginCode struct {
	code    digest
	expires int64
	by      *string
}

// Auth holds the members, roots, browsers and invites of one run. now is the clock (nil means
// time.Now), log receives one line per invite and member event (nil means silence), and file is
// where the digests persist (empty means memory only: a restart forgets it all).
type Auth struct {
	roots    []digest
	invites  []*invite
	issued   orderedSet // every invite id of this run, for redaction
	members  []*member
	browsers []browser
	codes    []loginCode
	live     orderedSet // every secret handed out: never stored in a channel
	dirty    bool
	flushT   *time.Timer
	now      func() time.Time
	log      func(string)
	file     string
}

// New starts the authority for a run: root is the credential of the session that started the
// server, which acts as anyone and mints invites.
func New(root string, now func() time.Time, log func(string), file string) *Auth {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = func(string) {}
	}
	a := &Auth{now: now, log: log, file: file}
	a.load()
	d := digestOf(root)
	kept := make([]digest, 0, len(a.roots)+1)
	for _, r := range a.roots {
		if !same(r, d) {
			kept = append(kept, r)
		}
	}
	a.roots = append(kept, d)
	if len(a.roots) > rootsKept {
		a.roots = a.roots[len(a.roots)-rootsKept:]
	}
	a.live.add(root)
	a.save()
	return a
}

// state is the state file's shape; the field order is the JSON's key order.
type state struct {
	V        int            `json:"v"`
	Note     string         `json:"note"`
	Roots    []string       `json:"roots"`
	Invites  []stateInvite  `json:"invites"`
	Issued   []string       `json:"issued"`
	Members  []stateMember  `json:"members"`
	Browsers []stateBrowser `json:"browsers"`
}

type stateInvite struct {
	ID          string `json:"id"`
	Secret      string `json:"secret"`
	Created     int64  `json:"created"`
	Expires     *int64 `json:"expires"`
	Single      bool   `json:"single"`
	Uses        int    `json:"uses"`
	Invite      bool   `json:"invite"`
	Channel     string `json:"channel,omitempty"`
	Description string `json:"description,omitempty"`
	By          string `json:"by"`
}

type stateMember struct {
	Name   string `json:"name"`
	Cred   string `json:"cred"`
	Since  int64  `json:"since"`
	Seen   int64  `json:"seen"`
	Invite bool   `json:"invite"`
	Via    string `json:"via"`
}

type stateBrowser struct {
	Cred string  `json:"cred"`
	By   *string `json:"by"`
}

// array is a decoded JSON array (nil when the value is not one).
func array(v any) []any {
	a, _ := v.([]any)
	return a
}

// number coerces a decoded JSON value as JavaScript's Number(): numbers and numeric strings,
// booleans as 1 and 0, an empty or blank string as 0; ok is false for NaN.
func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, !math.IsNaN(x)
	case string:
		if jsfmt.Trim(x) == "" {
			return 0, true
		}
		f, err := strconv.ParseFloat(jsfmt.Trim(x), 64)
		if err != nil {
			return math.NaN(), false
		}
		return f, true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	default:
		return math.NaN(), false
	}
}

// toMs coerces to whole milliseconds; NaN reads as 0.
func toMs(v any) int64 {
	f, ok := number(v)
	if !ok {
		return 0
	}
	return int64(f)
}

// toInt coerces as Number(v) || 0.
func toInt(v any) int { return int(toMs(v)) }

// truthy coerces as JavaScript's Boolean(): undefined, null, false, 0, NaN and "" are false;
// arrays and objects, even empty ones, are true.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0 && !math.IsNaN(x)
	case string:
		return x != ""
	default:
		return true
	}
}

// coerceStr coerces as String(v ?? fallback): null and undefined fall back, numbers and
// booleans print as JavaScript prints them.
func coerceStr(v any, fallback string) string {
	switch x := v.(type) {
	case nil:
		return fallback
	case string:
		return x
	case float64:
		return jsfmt.JSNumber(x)
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		return fallback
	}
}

// text keeps a string field; anything else (or empty) is no field at all.
func text(v any) string {
	s, _ := v.(string)
	return s
}

// load reads the state file: digests and public metadata only; anything unreadable is a fresh
// start.
func (a *Auth) load() {
	if a.file == "" {
		return
	}
	data, err := os.ReadFile(a.file)
	if err != nil {
		return
	}
	var parsed any
	if json.Unmarshal(data, &parsed) != nil {
		return
	}
	j, isObject := parsed.(map[string]any)
	if !isObject {
		return
	}
	if v, isNumber := j["v"].(float64); !isNumber || v != 1 {
		return
	}
	for _, r := range array(j["roots"]) {
		if s, isStr := r.(string); isStr {
			if d := unhexDigest(s); d != nil {
				a.roots = append(a.roots, *d)
			}
		}
	}
	for _, e := range array(j["invites"]) {
		i, isMap := e.(map[string]any)
		if !isMap {
			continue
		}
		var secret *digest
		if s, isStr := i["secret"].(string); isStr {
			secret = unhexDigest(s)
		}
		id, isStr := i["id"].(string)
		if secret == nil || !isStr {
			continue
		}
		inv := &invite{
			id:          id,
			secret:      *secret,
			created:     toMs(i["created"]),
			single:      truthy(i["single"]),
			uses:        toInt(i["uses"]),
			invite:      truthy(i["invite"]),
			channel:     text(i["channel"]),
			description: text(i["description"]),
			by:          coerceStr(i["by"], "owner"),
		}
		// a value that coerces to NaN never expires, and is written back as null
		if raw, present := i["expires"]; present && raw != nil {
			if e, ok := number(raw); ok {
				ms := int64(e)
				inv.expires = &ms
			}
		}
		a.invites = append(a.invites, inv)
	}
	for _, e := range array(j["issued"]) {
		if s, isStr := e.(string); isStr {
			a.issued.add(s)
		}
	}
	for _, e := range array(j["members"]) {
		m, isMap := e.(map[string]any)
		if !isMap {
			continue
		}
		var cred *digest
		if s, isStr := m["cred"].(string); isStr {
			cred = unhexDigest(s)
		}
		name, isStr := m["name"].(string)
		if cred == nil || !isStr {
			continue
		}
		a.setMember(&member{
			name:   name,
			cred:   *cred,
			since:  toMs(m["since"]),
			seen:   toMs(m["seen"]),
			invite: truthy(m["invite"]),
			via:    coerceStr(m["via"], ""),
		})
	}
	for _, e := range array(j["browsers"]) {
		b, isMap := e.(map[string]any)
		if !isMap {
			continue
		}
		var cred *digest
		if s, isStr := b["cred"].(string); isStr {
			cred = unhexDigest(s)
		}
		if cred == nil {
			continue
		}
		entry := browser{cred: *cred}
		if s, isStr := b["by"].(string); isStr {
			entry.by = &s
		}
		a.browsers = append(a.browsers, entry)
	}
	a.sweep()
}

// save writes the state file atomically (a temp file, then a rename) at 0600; a failure reaches
// the log and nothing else.
func (a *Auth) save() {
	if a.file == "" {
		return
	}
	if a.flushT != nil {
		a.flushT.Stop()
		a.flushT = nil
	}
	a.dirty = false
	st := state{
		V:        1,
		Note:     stateNote,
		Roots:    []string{},
		Invites:  []stateInvite{},
		Issued:   []string{},
		Members:  []stateMember{},
		Browsers: []stateBrowser{},
	}
	for _, r := range a.roots {
		st.Roots = append(st.Roots, hexDigest(r))
	}
	for _, i := range a.invites {
		st.Invites = append(st.Invites, stateInvite{
			ID:          i.id,
			Secret:      hexDigest(i.secret),
			Created:     i.created,
			Expires:     i.expires,
			Single:      i.single,
			Uses:        i.uses,
			Invite:      i.invite,
			Channel:     i.channel,
			Description: i.description,
			By:          i.by,
		})
	}
	st.Issued = append(st.Issued, a.issued.last(issuedKept)...)
	for _, m := range a.members {
		st.Members = append(st.Members, stateMember{
			Name:   m.name,
			Cred:   hexDigest(m.cred),
			Since:  m.since,
			Seen:   m.seen,
			Invite: m.invite,
			Via:    m.via,
		})
	}
	browsers := a.browsers
	if len(browsers) > browsersKept {
		browsers = browsers[len(browsers)-browsersKept:]
	}
	for _, b := range browsers {
		st.Browsers = append(st.Browsers, stateBrowser{Cred: hexDigest(b.cred), By: b.by})
	}
	data, err := domain.MarshalCompactJSON(&st)
	if err != nil {
		a.log(fmt.Sprintf("could not save %s: %v", a.file, err))
		return
	}
	if err := a.writeState(data); err != nil {
		a.log(fmt.Sprintf("could not save %s: %v", a.file, err))
	}
}

// writeState replaces the state file atomically; the directory and the file stay private to the
// server's user.
func (a *Auth) writeState(data []byte) error {
	if err := os.MkdirAll(filepath.Dir(a.file), 0o700); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", a.file, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, a.file); err != nil {
		return err
	}
	_ = os.Chmod(a.file, 0o600) // a rename carries the temp file's mode; an older file may differ
	return nil
}

// later asks for a write within a minute: "last seen" changes on every request, and is not
// written on each one.
func (a *Auth) later() {
	if a.file == "" || a.flushT != nil {
		return
	}
	a.dirty = true
	a.flushT = time.AfterFunc(flushDelay, a.Flush)
}

// Flush writes what is pending now (the server calls it when it stops).
func (a *Auth) Flush() {
	if a.dirty || a.flushT != nil {
		a.save()
	}
}

// Who a credential is: the owner (a root, or a browser the root signed in), a member, or
// nobody.
func (a *Auth) Who(cred string) (Who, bool) {
	if cred == "" {
		return Who{}, false
	}
	d := digestOf(cred)
	var found Who
	ok := false
	for _, r := range a.roots {
		if same(d, r) {
			found, ok = Who{Name: "owner", Root: true, Invite: true, Kind: KindOwner}, true
		}
	}
	for _, b := range a.browsers {
		if !same(d, b.cred) {
			continue
		}
		if b.by == nil {
			found, ok = Who{Name: "owner", Root: true, Invite: true, Kind: KindOwner}, true
		} else if _, isMember := a.member(*b.by); isMember {
			found, ok = Who{Name: "owner", Browser: *b.by, Kind: KindMember}, true
		}
	}
	for _, m := range a.members {
		if !same(d, m.cred) {
			continue
		}
		found, ok = Who{Name: m.name, Invite: m.invite, Kind: KindMember}, true
		m.seen = a.nowMs()
		a.later()
	}
	return found, ok
}

// nowMs is the clock, in whole milliseconds.
func (a *Auth) nowMs() int64 { return a.now().UnixMilli() }

// member finds a member by name.
func (a *Auth) member(name string) (*member, bool) {
	for _, m := range a.members {
		if m.name == name {
			return m, true
		}
	}
	return nil, false
}

// setMember adds a member, or replaces one by name; a replacement keeps the name's place in
// the listing.
func (a *Auth) setMember(m *member) {
	if old, ok := a.member(m.name); ok {
		*old = *m
		return
	}
	a.members = append(a.members, m)
}

// deleteMember drops a member by name.
func (a *Auth) deleteMember(name string) bool {
	return drop(&a.members, func(m *member) bool { return m.name == name })
}

// invite finds an invite by id.
func (a *Auth) invite(id string) (*invite, bool) {
	for _, i := range a.invites {
		if i.id == id {
			return i, true
		}
	}
	return nil, false
}

// deleteInvite drops an invite by id.
func (a *Auth) deleteInvite(id string) bool {
	return drop(&a.invites, func(i *invite) bool { return i.id == id })
}

// drop removes every element the keep predicate refuses, in place, and reports whether any
// went.
func drop[T any](list *[]T, gone func(T) bool) bool {
	before := len(*list)
	*list = slices.DeleteFunc(*list, gone)
	return len(*list) < before
}

// Create mints an invite; the whole token is returned once, here.
func (a *Auth) Create(o InviteOptions) InviteView {
	var id string
	for {
		id = Rand36(6)
		if !a.issued.contains(id) {
			break
		}
	}
	secret := Rand36(16)
	ttl := Day
	if o.TTL != nil {
		ttl = *o.TTL
	}
	created := a.nowMs()
	i := &invite{
		id:          id,
		secret:      digestOf(secret),
		created:     created,
		single:      o.Single,
		invite:      o.Invite,
		channel:     o.Channel,
		description: o.Description,
		by:          o.By,
	}
	if ttl > 0 {
		expires := created + ttl*1000
		i.expires = &expires
	}
	a.invites = append(a.invites, i)
	a.issued.add(id)
	token := id + "." + secret
	a.live.add(token)
	line := fmt.Sprintf("invite %s created by %s", id, o.By)
	if i.expires != nil && *i.expires != 0 {
		line += fmt.Sprintf(", expires %s", iso(*i.expires))
	}
	if i.single {
		line += ", single use"
	}
	a.log(line)
	a.save()
	view := i.view()
	view.Token = token
	return view
}

// view is an invite as listed: its public half and its rules, never its secret.
func (i *invite) view() InviteView {
	return InviteView{
		ID:          i.id,
		Created:     iso(i.created),
		Expires:     isoIfSet(i.expires),
		SingleUse:   i.single,
		Uses:        i.uses,
		CanInvite:   i.invite,
		Channel:     strPtr(i.channel),
		Description: strPtr(i.description),
		By:          i.by,
	}
}

// List returns the live invites, by id only.
func (a *Auth) List() []InviteView {
	a.sweep()
	views := make([]InviteView, 0, len(a.invites))
	for _, i := range a.invites {
		views = append(views, i.view())
	}
	return views
}

// Revoke removes an invite.
func (a *Auth) Revoke(id string) bool {
	if a.deleteInvite(id) {
		a.log("invite " + id + " revoked")
		a.save()
		return true
	}
	return false
}

// sweep drops the invites past their expiry.
func (a *Auth) sweep() {
	t := a.nowMs()
	kept := a.invites[:0]
	gone := false
	for _, i := range a.invites {
		if i.expires != nil && *i.expires <= t {
			gone = true
			continue
		}
		kept = append(kept, i)
	}
	a.invites = kept
	if gone {
		a.save()
	}
}

// valid returns the live invite a token names, or nil. The secret is compared even when the id
// is unknown, so a miss costs the same.
func (a *Auth) valid(token string) *invite {
	m := InviteRe.FindStringSubmatch(token)
	secret := token
	var found *invite
	if m != nil {
		secret = m[2]
		found, _ = a.invite(m[1])
	}
	target := digestOf(noInvite)
	if found != nil {
		target = found.secret
	}
	ok := same(digestOf(secret), target)
	if found == nil || !ok {
		return nil
	}
	if found.expires != nil && *found.expires <= a.nowMs() {
		a.deleteInvite(found.id)
		a.save()
		return nil
	}
	return found
}

// Peek shows a live invite's channel (nil: none) and the member names taken; a token that
// would not join gets none of that.
func (a *Auth) Peek(token string) (PeekResult, bool) {
	i := a.valid(token)
	if i == nil {
		return PeekResult{}, false
	}
	names := make([]string, 0, len(a.members))
	for _, m := range a.members {
		names = append(names, m.name)
	}
	return PeekResult{Channel: strPtr(i.channel), Members: names}, true
}

// Join presents an invite once for a credential bound to the name; a name joining again gets a
// new one, and the old one stops working.
func (a *Auth) Join(token, name string) JoinResult {
	i := a.valid(token)
	if i == nil {
		return JoinResult{Err: "invalid token"}
	}
	cred := credential()
	now := a.nowMs()
	a.setMember(&member{name: name, cred: digestOf(cred), since: now, seen: now, invite: i.invite, via: i.id})
	a.live.add(cred)
	i.uses++
	if i.single {
		a.deleteInvite(i.id)
	}
	a.log(fmt.Sprintf("%s joined with invite %s", name, i.id))
	a.save()
	return JoinResult{Credential: cred, Name: name, Channel: strPtr(i.channel)}
}

// MemberList returns the members, in the order they joined.
func (a *Auth) MemberList() []MemberView {
	views := make([]MemberView, 0, len(a.members))
	for _, m := range a.members {
		views = append(views, MemberView{
			Name:      m.name,
			Since:     iso(m.since),
			Seen:      iso(m.seen),
			CanInvite: m.invite,
			Invite:    m.via,
		})
	}
	return views
}

// Kick revokes a member's credential, its browsers and its login codes.
func (a *Auth) Kick(name string) bool {
	if !a.deleteMember(name) {
		return false
	}
	drop(&a.browsers, func(b browser) bool { return b.by != nil && *b.by == name })
	drop(&a.codes, func(c loginCode) bool { return c.by != nil && *c.by == name })
	a.log(name + " kicked")
	a.save()
	return true
}

// LoginCode mints a code good once, for five minutes; by is the member asking for it (nil
// means the root), whose browser gets none of its rights.
func (a *Auth) LoginCode(by *string) string {
	code := base64.RawURLEncoding.EncodeToString(randomBytes(18))
	t := a.nowMs()
	kept := a.codes[:0]
	for _, c := range a.codes {
		if c.expires > t {
			kept = append(kept, c)
		}
	}
	owner := by
	if by != nil { // the code keeps its own copy, not the caller's variable
		name := *by
		owner = &name
	}
	a.codes = append(kept, loginCode{code: digestOf(code), expires: t + codeWindow.Milliseconds(), by: owner})
	a.live.add(code)
	return code
}

// Redeem exchanges a login code for a cookie credential that acts as the owner; the code is
// spent either way.
func (a *Auth) Redeem(code string) (string, bool) {
	d := digestOf(code)
	t := a.nowMs()
	index := -1
	for i, c := range a.codes {
		if same(c.code, d) {
			index = i
			break
		}
	}
	if index < 0 {
		return "", false
	}
	c := a.codes[index]
	a.codes = append(a.codes[:index], a.codes[index+1:]...)
	if c.by != nil {
		if _, isMember := a.member(*c.by); !isMember {
			return "", false
		}
	}
	if c.expires <= t {
		return "", false
	}
	cred := credential()
	a.browsers = append(a.browsers, browser{cred: digestOf(cred), by: c.by})
	a.live.add(cred)
	if len(a.browsers) > browsersKept {
		a.browsers = a.browsers[len(a.browsers)-browsersKept:]
	}
	a.save()
	return cred, true
}

var (
	inviteShape = regexp.MustCompile(`\b([a-z0-9]{6})\.([a-z0-9]{16})\b`)
	credShape   = regexp.MustCompile(`hcred_[A-Za-z0-9_-]+`)
)

// Redact walks a decoded request body and replaces any credential, code or invite of
// this run with "<redacted>"; an invite keeps its public id. The keys keep their arrival
// order, so the redacted body re-encodes to JSON.stringify's bytes.
func (a *Auth) Redact(v *jsfmt.Value) *jsfmt.Value {
	if v == nil {
		return nil
	}
	switch v.Kind() {
	case jsfmt.KindString:
		return jsfmt.String(a.scrub(v.Text()))
	case jsfmt.KindObject:
		keys := v.Keys()
		fields := v.Fields()
		outKeys := make([]string, 0, len(keys))
		outFields := make([]*jsfmt.Value, 0, len(fields))
		for i, key := range keys {
			outKeys = append(outKeys, key)
			outFields = append(outFields, a.Redact(fields[i]))
		}
		return jsfmt.Object(outKeys, outFields)
	case jsfmt.KindArray:
		items := v.Items()
		out := make([]*jsfmt.Value, 0, len(items))
		for _, item := range items {
			out = append(out, a.Redact(item))
		}
		return jsfmt.Array(out)
	default:
		return v
	}
}

// scrub cleans one string: this run's invites (by id), then any credential shape, then every
// secret still live, in the order they were handed out.
func (a *Auth) scrub(s string) string {
	s = inviteShape.ReplaceAllStringFunc(s, func(all string) string {
		m := inviteShape.FindStringSubmatch(all)
		if m != nil && a.issued.contains(m[1]) {
			return m[1] + ".<redacted>"
		}
		return all
	})
	s = credShape.ReplaceAllString(s, "<redacted>")
	for _, secret := range a.live.order {
		if jsfmt.UTF16Len(secret) >= 8 && strings.Contains(s, secret) {
			s = strings.ReplaceAll(s, secret, "<redacted>")
		}
	}
	return s
}

// orderedSet is a string set that remembers insertion order, which redaction relies on.
type orderedSet struct {
	order []string
	has   map[string]struct{}
}

func (s *orderedSet) add(v string) {
	if s.has == nil {
		s.has = make(map[string]struct{})
	}
	if _, ok := s.has[v]; ok {
		return
	}
	s.has[v] = struct{}{}
	s.order = append(s.order, v)
}

func (s *orderedSet) contains(v string) bool {
	_, ok := s.has[v]
	return ok
}

// last returns the insertion order trimmed to its final n entries.
func (s *orderedSet) last(n int) []string {
	if len(s.order) <= n {
		return s.order
	}
	return s.order[len(s.order)-n:]
}

// digestOf is a secret's stored form.
func digestOf(s string) digest { return sha256.Sum256([]byte(s)) }

// same compares two digests in constant time; both are sha256, so the lengths always match and
// nothing leaks.
func same(a, b digest) bool { return subtle.ConstantTimeCompare(a[:], b[:]) == 1 }

func hexDigest(d digest) string { return hex.EncodeToString(d[:]) }

// unhexDigest reads a persisted digest; anything but 32 bytes of hex is nothing.
func unhexDigest(s string) *digest {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != len(digest{}) {
		return nil
	}
	var d digest
	copy(d[:], b)
	return &d
}

// randomBytes draws from crypto/rand; Read never returns an error and always fills the buffer
// (it crashes the program itself on the legacy systems where its source can fail).
func randomBytes(n int) []byte {
	buf := make([]byte, n)
	_, _ = crand.Read(buf)
	return buf
}

const alpha = "abcdefghijklmnopqrstuvwxyz0123456789"

// Rand36 returns n characters of [a-z0-9], uniform: bytes at or past the last multiple of 36
// are drawn again.
func Rand36(n int) string {
	out := make([]byte, 0, n)
	for len(out) < n {
		for _, b := range randomBytes(n * 2) {
			if b < 252 && len(out) < n { // 252 is the last multiple of 36
				out = append(out, alpha[b%36])
			}
		}
	}
	return string(out)
}

// credential mints a session's own secret.
func credential() string {
	return "hcred_" + base64.RawURLEncoding.EncodeToString(randomBytes(32))
}

// iso renders a moment as JavaScript's toISOString: milliseconds, UTC, "Z".
func iso(ms int64) string { return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z") }

// isoIfSet renders an optional moment; an unset one is null.
func isoIfSet(ms *int64) *string {
	if ms == nil {
		return nil
	}
	s := iso(*ms)
	return &s
}

// strPtr turns an optional string field into its nullable form.
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// MayAct reports whether w may act as as: the root as anyone, a member as itself and its
// subagents (<name>.<role>), anyone as nobody in particular.
func MayAct(w Who, as string) bool {
	return w.Root || as == "" || as == w.Name || strings.HasPrefix(as, w.Name+".")
}

// Given returns what a request carries: the header, else this server's cookie. The
// separator allows JavaScript's whitespace, Unicode spaces included, which RE2's \s does
// not match.
func Given(req *http.Request, cookie string) string {
	if h := req.Header.Get(Header); h != "" {
		return h
	}
	m := regexp.MustCompile(`(?:^|;` + jsfmt.SpaceClass + `*)` + regexp.QuoteMeta(cookie) + `=([^;]*)`).FindStringSubmatch(req.Header.Get("Cookie"))
	if m == nil {
		return ""
	}
	if v, err := url.PathUnescape(m[1]); err == nil {
		return v
	}
	return m[1]
}
