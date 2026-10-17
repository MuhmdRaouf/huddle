package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/jsfmt"
)

// clockAt is the injected clock of the suite: now() is ms since the epoch, add advances it by
// whole seconds.
func clockAt(ms int64) (now func() time.Time, add func(sec int64)) {
	t := ms
	return func() time.Time { return time.UnixMilli(t) }, func(sec int64) { t += sec * 1000 }
}

func newClock() (func() time.Time, func(int64)) {
	return clockAt(1_700_000_000_000)
}

func sec(s int64) *int64   { return &s }
func str(s string) *string { return &s }

// joined reports whether a join handed out a credential.
func joined(r JoinResult) bool { return r.Err == "" }

// secretHalf is an invite token's private half (the whole token when it has none).
func secretHalf(token string) string {
	if _, half, ok := strings.Cut(token, "."); ok {
		return half
	}
	return token
}

func memberNames(ms []MemberView) []string {
	names := make([]string, 0, len(ms))
	for _, m := range ms {
		names = append(names, m.Name)
	}
	return names
}

func inviteUses(list []InviteView, id string) (int, bool) {
	for _, i := range list {
		if i.ID == id {
			return i.Uses, true
		}
	}
	return 0, false
}

func TestAuth(t *testing.T) {
	t.Run("invites are <id>.<secret> in [a-z0-9]{6}.[a-z0-9]{16}, the whole token returned once and listed by id only", func(t *testing.T) {
		a := New("root-cred-xyz", nil, nil, "")
		inv := a.Create(InviteOptions{By: "owner"})
		if !InviteRe.MatchString(inv.Token) {
			t.Errorf("token %q does not match the invite pattern", inv.Token)
		}
		if !strings.HasPrefix(inv.Token, inv.ID+".") {
			t.Errorf("token %q does not start with its id %q", inv.Token, inv.ID)
		}
		if !regexp.MustCompile(`^[a-z0-9]{40}$`).MatchString(Rand36(40)) {
			t.Errorf("rand36(40) = %q, want 40 characters of [a-z0-9]", Rand36(40))
		}
		listed, err := json.Marshal(a.List())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(listed), inv.ID) {
			t.Errorf("the invite list does not name invite %s", inv.ID)
		}
		if strings.Contains(string(listed), secretHalf(inv.Token)) {
			t.Errorf("the invite list carries the secret half of invite %s", inv.ID)
		}
		list := a.List()
		if len(list) == 0 {
			t.Fatal("the invite list is empty")
		}
		if first := list[0]; first.ID != inv.ID || first.SingleUse || first.Uses != 0 {
			t.Errorf("list entry = %+v, want id %s, single_use false, uses 0", first, inv.ID)
		}
	})

	t.Run("an invite expires after its TTL (24 h by default; 0 = never)", func(t *testing.T) {
		now, add := newClock()
		a := New("root-cred-xyz", now, nil, "")
		day := a.Create(InviteOptions{By: "owner"})
		hour := a.Create(InviteOptions{By: "owner", TTL: sec(3600)})
		never := a.Create(InviteOptions{By: "owner", TTL: sec(0)})
		add(3599)
		if !joined(a.Join(hour.Token, "early")) {
			t.Error("the invite refused a join one second before its TTL")
		}
		add(2)
		if got := a.Join(hour.Token, "late"); got.Err != "invalid token" {
			t.Errorf("a join past the TTL: %+v", got)
		}
		add(86_400)
		if got := a.Join(day.Token, "x"); got.Err != "invalid token" {
			t.Errorf("a join past the default 24 h TTL: %+v", got)
		}
		if got, want := memberIDs(a.List()), []string{never.ID}; !slices.Equal(got, want) {
			t.Errorf("listed invites = %v, want %v (the expired ones are gone)", got, want)
		}
		add(10 * 86_400)
		if !joined(a.Join(never.Token, "y")) {
			t.Error("a no-expiry invite refused a join ten days on")
		}
	})

	t.Run("a single-use invite works once; a multi-use one until revoked", func(t *testing.T) {
		a := New("root-cred-xyz", nil, nil, "")
		once := a.Create(InviteOptions{By: "owner", Single: true})
		many := a.Create(InviteOptions{By: "owner"})
		if !joined(a.Join(once.Token, "a")) {
			t.Error("the single-use invite refused its one join")
		}
		if got := a.Join(once.Token, "b"); got.Err != "invalid token" {
			t.Errorf("the single-use invite joined twice: %+v", got)
		}
		if !joined(a.Join(many.Token, "c")) || !joined(a.Join(many.Token, "d")) {
			t.Error("the multi-use invite refused a join")
		}
		if uses, ok := inviteUses(a.List(), many.ID); !ok || uses != 2 {
			t.Errorf("invite %s uses = %d (found %v), want 2", many.ID, uses, ok)
		}
		if !a.Revoke(many.ID) {
			t.Error("revoking a live invite failed")
		}
		if got := a.Join(many.Token, "e"); got.Err != "invalid token" {
			t.Errorf("a revoked invite joined: %+v", got)
		}
		if a.Revoke(many.ID) {
			t.Error("revoking the same invite twice succeeded")
		}
	})

	t.Run("a wrong secret, an unknown id or garbage is refused the same way", func(t *testing.T) {
		a := New("root-cred-xyz", nil, nil, "")
		inv := a.Create(InviteOptions{By: "owner"})
		for _, bad := range []string{inv.ID + ".0000000000000000", "zzzzzz.0123456789abcdef", inv.ID, "", "not a token", inv.Token + "x"} {
			if got := a.Join(bad, "x"); got.Err != "invalid token" {
				t.Errorf("join(%q) = %+v, want {invalid token}", bad, got)
			}
		}
	})

	t.Run("joining issues a credential bound to the name; a rejoin replaces it; kick revokes it", func(t *testing.T) {
		a := New("root-cred-xyz", nil, nil, "")
		inv := a.Create(InviteOptions{By: "owner", Channel: "shop"})
		j := a.Join(inv.Token, "api")
		if j.Err != "" || j.Name != "api" || j.Channel == nil || *j.Channel != "shop" {
			t.Fatalf("join = %+v, want name api on channel shop", j)
		}
		if strings.Contains(j.Credential, secretHalf(inv.Token)) {
			t.Error("the member credential carries the invite's secret")
		}
		if w, ok := a.Who(j.Credential); !ok || w != (Who{Name: "api"}) {
			t.Errorf("who(member) = %+v (found %v), want {api false false}", w, ok)
		}
		if w, ok := a.Who("root-cred-xyz"); !ok || w != (Who{Name: "owner", Root: true, Invite: true}) {
			t.Errorf("who(root) = %+v (found %v), want {owner true true}", w, ok)
		}
		if _, ok := a.Who("nope"); ok {
			t.Error("who of an unknown credential found someone")
		}
		again := a.Join(inv.Token, "api")
		if _, ok := a.Who(j.Credential); ok {
			t.Error("the credential of a name that rejoined still works")
		}
		if w, ok := a.Who(again.Credential); !ok || w.Name != "api" {
			t.Errorf("who of the rejoining name = %+v (found %v), want api", w, ok)
		}
		if got, want := memberNames(a.MemberList()), []string{"api"}; !slices.Equal(got, want) {
			t.Errorf("members = %v, want %v", got, want)
		}
		if !a.Kick("api") {
			t.Error("kick failed")
		}
		if _, ok := a.Who(again.Credential); ok {
			t.Error("a kicked member's credential still works")
		}
	})

	t.Run("a member acts as itself and its subagents; root as anyone", func(t *testing.T) {
		member := Who{Name: "api"}
		if !MayAct(member, "api") {
			t.Error("a member may not act as itself")
		}
		if !MayAct(member, "api.explore") {
			t.Error("a member may not act as its subagent")
		}
		if MayAct(member, "apix") {
			t.Error("a member acted as a name with its own as a prefix")
		}
		if MayAct(member, "owner") {
			t.Error("a member acted as the owner")
		}
		if !MayAct(Who{Name: "owner", Root: true, Invite: true}, "anyone") {
			t.Error("the root may not act as anyone")
		}
	})

	t.Run("a login code works once, within five minutes", func(t *testing.T) {
		now, add := newClock()
		a := New("root-cred-xyz", now, nil, "")
		code := a.LoginCode(nil)
		cookie, ok := a.Redeem(code)
		if !ok {
			t.Fatal("the login code did not redeem")
		}
		if w, ok := a.Who(cookie); !ok || w.Name != "owner" || !w.Root {
			t.Errorf("who(cookie) = %+v (found %v), want the owner as root", w, ok)
		}
		if _, ok := a.Redeem(code); ok {
			t.Error("a login code redeemed twice")
		}
		late := a.LoginCode(nil)
		add(301)
		if _, ok := a.Redeem(late); ok {
			t.Error("a login code redeemed after five minutes")
		}
	})

	t.Run("a member mints a login code for its own browser: the owner's dashboard, none of the creator's rights; gone with a kick", func(t *testing.T) {
		now, _ := newClock()
		a := New("root-cred-xyz", now, nil, "")
		j := a.Join(a.Create(InviteOptions{By: "owner"}).Token, "api")
		cookie, ok := a.Redeem(a.LoginCode(str("api")))
		if !ok {
			t.Fatal("the member's login code did not redeem")
		}
		w, found := a.Who(cookie)
		if !found || w != (Who{Name: "owner", Browser: "api"}) {
			t.Fatalf("who(cookie) = %+v (found %v), want the owner as api's browser", w, found)
		}
		if !MayAct(w, "owner") {
			t.Error("the dashboard may not act as the owner in the channels")
		}
		if MayAct(w, "api") {
			t.Error("the dashboard acted as a session")
		}
		if m, ok := a.Who(j.Credential); !ok || m.Name != "api" {
			t.Errorf("who of the member = %+v (found %v), want api", m, ok)
		}
		pending := a.LoginCode(str("api"))
		a.Kick("api")
		if _, ok := a.Who(cookie); ok {
			t.Error("a kicked member's browser still works")
		}
		if _, ok := a.Redeem(pending); ok {
			t.Error("a kicked member's unused login code still redeems")
		}
	})

	t.Run("secrets never reach a channel: credentials, codes and this run's invites are redacted", func(t *testing.T) {
		a := New("root-cred-xyz", nil, nil, "")
		inv := a.Create(InviteOptions{By: "owner"})
		j := a.Join(inv.Token, "api")
		code := a.LoginCode(nil)
		body, err := jsfmt.DecodeValue([]byte(fmt.Sprintf(
			`{"msg":"join with %s please","nested":[{"note":"cred %s root root-cred-xyz code %s"}],"keep":"abcdef.0123456789abcdef"}`,
			inv.Token, j.Credential, code)))
		if err != nil {
			t.Fatal(err)
		}
		out := a.Redact(body)
		if out.Kind() != jsfmt.KindObject {
			t.Fatalf("redact returned %v, want an object", out.Kind())
		}
		if got, want := fieldOf(t, out, "msg").Text(), fmt.Sprintf("join with %s.<redacted> please", inv.ID); got != want {
			t.Errorf("msg = %q, want %q", got, want)
		}
		note := fieldOf(t, fieldOf(t, out, "nested").Items()[0], "note").Text()
		if got, want := note, "cred <redacted> root <redacted> code <redacted>"; got != want {
			t.Errorf("note = %q, want %q", got, want)
		}
		if got := fieldOf(t, out, "keep").Text(); got != "abcdef.0123456789abcdef" {
			t.Errorf("keep = %v: not an invite of this run, it must stay untouched", got)
		}
	})

	t.Run("with a state file, a restart remembers roots, members, browsers and unused invites as digests; kicks and expiry hold", func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, "data", "auth.json")
		now, add := newClock()
		a := New("root-one", now, nil, file)
		keep := a.Create(InviteOptions{By: "owner", Channel: "shop"})
		short := a.Create(InviteOptions{By: "owner", TTL: sec(60)})
		used := a.Create(InviteOptions{By: "owner", Single: true})
		api := a.Join(used.Token, "api")
		web := a.Join(keep.Token, "web")
		cookie, ok := a.Redeem(a.LoginCode(str("api")))
		if !ok {
			t.Fatal("the member's login code did not redeem")
		}
		rootCookie, ok := a.Redeem(a.LoginCode(nil))
		if !ok {
			t.Fatal("the root's login code did not redeem")
		}
		pending := a.LoginCode(nil)
		fi, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("state file mode = %#o, want 0600", fi.Mode().Perm())
		}
		disk, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for role, secret := range map[string]string{
			"the root credential":            "root-one",
			"the api member credential":      api.Credential,
			"the web member credential":      web.Credential,
			"the api browser cookie":         cookie,
			"the root browser cookie":        rootCookie,
			"the pending login code":         pending,
			"the keep invite's secret half":  secretHalf(keep.Token),
			"the short invite's secret half": secretHalf(short.Token),
		} {
			if strings.Contains(string(disk), secret) {
				t.Errorf("the state file stores %s in the clear", role)
			}
		}
		// A new run, with a new root handed over: the old root still works, so does everyone else.
		add(61)
		b := New("root-two", now, nil, file)
		if w, ok := b.Who("root-one"); !ok || !w.Root {
			t.Error("the previous root no longer works after a restart")
		}
		if w, ok := b.Who("root-two"); !ok || !w.Root {
			t.Error("the new root does not work")
		}
		if w, ok := b.Who(api.Credential); !ok || w.Name != "api" || w.Root {
			t.Errorf("who of the api member = %+v (found %v)", w, ok)
		}
		if w, ok := b.Who(cookie); !ok || w.Browser != "api" {
			t.Errorf("who of the member's browser = %+v (found %v)", w, ok)
		}
		if w, ok := b.Who(rootCookie); !ok || !w.Root {
			t.Errorf("who of the root's browser = %+v (found %v)", w, ok)
		}
		if _, ok := b.Redeem(pending); ok {
			t.Error("a login code survived a restart")
		}
		if got := b.Join(keep.Token, "late"); got.Err != "" {
			t.Errorf("an unused invite no longer joins after a restart: %+v", got)
		}
		if got := b.Join(short.Token, "x"); got.Err != "invalid token" {
			t.Errorf("an expired invite joined after a restart: %+v", got)
		}
		if got := b.Join(used.Token, "y"); got.Err != "invalid token" {
			t.Errorf("a used single-use invite joined after a restart: %+v", got)
		}
		if got := b.Redact(jsfmt.String("invite " + keep.Token)).Text(); got != "invite "+keep.ID+".<redacted>" {
			t.Errorf("redact = %q, want the invite still known as this run's", got)
		}
		if !b.Kick("api") {
			t.Error("kick failed")
		}
		d := New("root-two", now, nil, file)
		if _, ok := d.Who(api.Credential); ok {
			t.Error("a kick did not hold across a restart")
		}
		if _, ok := d.Who(cookie); ok {
			t.Error("a kicked member's browser still works after a restart")
		}
		if w, ok := d.Who(web.Credential); !ok || w.Name != "web" {
			t.Errorf("who of the web member = %+v (found %v)", w, ok)
		}
		got := memberNames(d.MemberList())
		sort.Strings(got)
		if want := []string{"late", "web"}; !slices.Equal(got, want) {
			t.Errorf("members = %v, want %v", got, want)
		}
	})

	t.Run("an unreadable state file is a fresh start, never a crash", func(t *testing.T) {
		dir := t.TempDir()
		file := filepath.Join(dir, "auth.json")
		if err := os.WriteFile(file, []byte("{torn"), 0o600); err != nil {
			t.Fatal(err)
		}
		a := New("root-x", nil, nil, file)
		if w, ok := a.Who("root-x"); !ok || !w.Root {
			t.Errorf("who of the new root = %+v (found %v)", w, ok)
		}
		if got := a.MemberList(); len(got) != 0 {
			t.Errorf("members = %v, want none", got)
		}
	})
}

func memberIDs(list []InviteView) []string {
	ids := make([]string, 0, len(list))
	for _, i := range list {
		ids = append(ids, i.ID)
	}
	return ids
}

// fixtureSecrets lists the plaintexts behind testdata/state.json (every one a fake), as
// testdata/secrets.json records them.
type fixtureSecrets struct {
	T0         int64  `json:"t0"`
	Root       string `json:"root"`
	Keep       string `json:"keep"`
	KeepID     string `json:"keepID"`
	Short      string `json:"short"`
	Used       string `json:"used"`
	APICred    string `json:"apiCred"`
	WebCred    string `json:"webCred"`
	Cookie     string `json:"cookie"`
	RootCookie string `json:"rootCookie"`
	Pending    string `json:"pending"`
}

// copyState puts the fixture in a scratch home, as a restart would find it.
func copyState(t *testing.T, want []byte, file string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, want, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestStateFileWrittenByAnEarlierRelease verifies a credential file an earlier release's
// server wrote loads and rewrites unchanged. state.json holds sha256 digests only;
// secrets.json lists the fake plaintexts, with the fixed clock the fixture was built on.
func TestStateFileWrittenByAnEarlierRelease(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("testdata", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "secrets.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f fixtureSecrets
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}

	// A restart 30 s on (before anything expires) rewrites the file byte for byte.
	file := filepath.Join(t.TempDir(), "data", "auth.json")
	copyState(t, want, file)
	now, _ := clockAt(f.T0 + 30_000)
	a := New(f.Root, now, nil, file)
	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("a rewrite of the earlier release's state file changed it:\n got: %s\nwant: %s", got, want)
	}
	if w, ok := a.Who(f.APICred); !ok || w.Name != "api" || w.Root {
		t.Errorf("who of the api member = %+v (found %v)", w, ok)
	}
	if w, ok := a.Who(f.Cookie); !ok || w.Browser != "api" {
		t.Errorf("who of the member's browser = %+v (found %v)", w, ok)
	}
	if w, ok := a.Who(f.RootCookie); !ok || !w.Root {
		t.Errorf("who of the root's browser = %+v (found %v)", w, ok)
	}
	if _, ok := a.Redeem(f.Pending); ok {
		t.Error("a login code survived into the state file")
	}
	if got := a.Join(f.Used, "y"); got.Err != "invalid token" {
		t.Error("a used single-use invite joined again")
	}

	// Past the short invite's TTL (60 s), at 61 s, it is refused; the 24 h one still joins, and
	// redaction still recognises this run's invite ids.
	file2 := filepath.Join(t.TempDir(), "data", "auth.json")
	copyState(t, want, file2)
	nowLate, _ := clockAt(f.T0 + 61_000)
	b := New(f.Root, nowLate, nil, file2)
	if got := b.Join(f.Short, "x"); got.Err != "invalid token" {
		t.Error("the expired invite joined")
	}
	if got := b.Join(f.Keep, "late"); got.Err != "" {
		t.Errorf("the 24 h invite no longer joins: %+v", got)
	}
	if got := b.Redact(jsfmt.String("invite " + f.Keep)).Text(); got != "invite "+f.KeepID+".<redacted>" {
		t.Errorf("redact = %q, want %q", got, "invite "+f.KeepID+".<redacted>")
	}
}

// TestPeekNamesTheJoin: peek shows the channel an invite names and the member names taken, and
// nothing for a token that would not join.
func TestPeekNamesTheJoin(t *testing.T) {
	a := New("root-cred-xyz", nil, nil, "")
	inv := a.Create(InviteOptions{By: "owner", Channel: "shop"})
	if _, ok := a.Peek("garbage"); ok {
		t.Error("a garbage token peeked")
	}
	if !joined(a.Join(inv.Token, "api")) {
		t.Fatal("the invite refused its join")
	}
	p, ok := a.Peek(inv.Token)
	if !ok {
		t.Fatal("a live invite did not peek")
	}
	if p.Channel == nil || *p.Channel != "shop" {
		t.Errorf("channel = %v, want shop", p.Channel)
	}
	if want := []string{"api"}; !slices.Equal(p.Members, want) {
		t.Errorf("members = %v, want %v", p.Members, want)
	}
}

// TestGivenReadsHeaderElseCookie: a request's credential comes from the header, else the cookie,
// percent-escapes decoded.
func TestGivenReadsHeaderElseCookie(t *testing.T) {
	none, _ := http.NewRequest(http.MethodPost, "/", nil)
	if got := Given(none, "huddle"); got != "" {
		t.Errorf("given of a bare request = %q, want empty", got)
	}
	withHeader, _ := http.NewRequest(http.MethodPost, "/", nil)
	withHeader.Header.Set(Header, "tok")
	if got := Given(withHeader, "huddle"); got != "tok" {
		t.Errorf("given of the header = %q, want tok", got)
	}
	withCookie, _ := http.NewRequest(http.MethodPost, "/", nil)
	withCookie.Header.Set("Cookie", "a=1; huddle=tok%20en; b=2")
	if got := Given(withCookie, "huddle"); got != "tok en" {
		t.Errorf("given of the cookie = %q, want %q", got, "tok en")
	}
	plainCookie, _ := http.NewRequest(http.MethodPost, "/", nil)
	plainCookie.Header.Set("Cookie", "huddle=abc123")
	if got := Given(plainCookie, "huddle"); got != "abc123" {
		t.Errorf("given of the cookie = %q, want abc123", got)
	}
	// The separator's \s is JavaScript's, which U+00A0 and U+3000 belong to and U+0085 does
	// not: new RegExp(`(?:^|;\\s*)huddle=([^;]*)`).exec(header)
	nbspCookie, _ := http.NewRequest(http.MethodPost, "/", nil)
	nbspCookie.Header.Set("Cookie", "a=1;\u00a0huddle=tok%20en; b=2")
	if got := Given(nbspCookie, "huddle"); got != "tok en" {
		t.Errorf("given of a U+00A0-separated cookie = %q, want %q", got, "tok en")
	}
	ideographicCookie, _ := http.NewRequest(http.MethodPost, "/", nil)
	ideographicCookie.Header.Set("Cookie", "a=1;\u3000huddle=abc123")
	if got := Given(ideographicCookie, "huddle"); got != "abc123" {
		t.Errorf("given of a U+3000-separated cookie = %q, want abc123", got)
	}
	nelCookie, _ := http.NewRequest(http.MethodPost, "/", nil)
	nelCookie.Header.Set("Cookie", "a=1;\u0085huddle=x")
	if got := Given(nelCookie, "huddle"); got != "" {
		t.Errorf("given of a U+0085-separated cookie = %q, want empty", got)
	}
}

// TestFlushWritesWhatRequestsChanged: a member's last-seen lands in the file on flush, not on
// every request.
func TestFlushWritesWhatRequestsChanged(t *testing.T) {
	t0 := int64(1_700_000_000_000)
	now, add := clockAt(t0)
	file := filepath.Join(t.TempDir(), "auth.json")
	a := New("root-cred-xyz", now, nil, file)
	j := a.Join(a.Create(InviteOptions{By: "owner"}).Token, "api")
	if j.Err != "" {
		t.Fatal("the invite refused its join")
	}
	add(10)
	if _, ok := a.Who(j.Credential); !ok {
		t.Fatal("the member credential does not verify")
	}
	disk, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if seen := fmt.Sprintf(`"seen":%d`, t0+10_000); bytes.Contains(disk, []byte(seen)) {
		t.Error("a request wrote the state file at once")
	}
	a.Flush()
	disk, err = os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if seen := fmt.Sprintf(`"seen":%d`, t0+10_000); !bytes.Contains(disk, []byte(seen)) {
		t.Error("flush did not write the new last-seen")
	}
}

// TestStateFileReadingIsLenient: a state file with corrupt entries keeps its readable ones
// (anything unreadable is skipped, never a crash), garbage fields coerce as the readers
// coerce, and the caps (8 roots, 1000 issued ids) hold over a restart.
func TestStateFileReadingIsLenient(t *testing.T) {
	now, _ := newClock()
	file := filepath.Join(t.TempDir(), "auth.json")
	goodRoot := hexDigest(digestOf("hostile-root"))
	goodCred := hexDigest(digestOf("member-cred"))
	ghostCookie := hexDigest(digestOf("ghost-cookie"))
	rootCookie := hexDigest(digestOf("root-cookie"))
	goodSecret := hexDigest(digestOf("abcdefghijklmnop"))
	var sb strings.Builder
	sb.WriteString(`{"v":1,"note":"hostile","roots":[`)
	for i := 1; i <= 9; i++ {
		if i > 1 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, "%q", hexDigest(digestOf(fmt.Sprintf("r%d", i))))
	}
	fmt.Fprintf(&sb, ",%q,5,null,\"tooshort\"],\"invites\":[", goodRoot)
	fmt.Fprintf(&sb, `{"id":"aaaaaa","secret":%q,"created":"1700000000000","expires":"nope","single":1,"uses":true,"invite":[],"channel":7,"description":false,"by":5},`, goodSecret)
	fmt.Fprintf(&sb, `{"secret":%q},"junk",{"id":"bbbbbb"}],"issued":["aaaaaa","aaaaaa",5,"zzzzzz"`, goodSecret)
	for i := 0; i < 1000; i++ {
		fmt.Fprintf(&sb, ",\"i%04d\"", i)
	}
	fmt.Fprintf(&sb, `],"members":[{"name":"m","cred":%q,"since":"garbage","seen":null,"invite":"yes","via":true},{"name":"x"},"junk",{"cred":%q},42],`, goodCred, goodCred)
	fmt.Fprintf(&sb, `"browsers":[{"cred":%q,"by":"ghost"},{"cred":"zz","by":"x"},17,{"cred":%q,"by":7}]}`, ghostCookie, rootCookie)
	if err := os.WriteFile(file, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	a := New("hostile-root", now, nil, file)
	if w, ok := a.Who("hostile-root"); !ok || !w.Root {
		t.Error("the root does not verify")
	}
	if w, ok := a.Who("member-cred"); !ok || w.Name != "m" || !w.Invite {
		t.Errorf("who of the member = %+v (found %v)", w, ok)
	}
	if got := a.MemberList(); len(got) != 1 || got[0].Name != "m" {
		t.Fatalf("members = %+v, want only m", got)
	} else if got[0].Since != "1970-01-01T00:00:00.000Z" || got[0].Invite != "true" || !got[0].CanInvite {
		t.Errorf("the member's coerced fields = %+v", got[0])
	}
	if _, ok := a.Who("ghost-cookie"); ok {
		t.Error("a browser of a member who is not there still verifies")
	}
	if w, ok := a.Who("root-cookie"); !ok || !w.Root {
		t.Error("a browser by a non-string is not the root's")
	}
	list := a.List()
	if len(list) != 1 {
		t.Fatalf("listed invites = %d, want the one readable one", len(list))
	}
	if got := list[0]; got.By != "5" || got.Expires != nil || got.Uses != 1 || !got.CanInvite || got.Channel != nil {
		t.Errorf("the invite's coerced fields = %+v", got)
	}
	j := a.Join("aaaaaa.abcdefghijklmnop", "x")
	if j.Err != "" || j.Channel != nil {
		t.Errorf("the readable invite did not join: %+v", j)
	}
	if w, ok := a.Who(j.Credential); !ok || !w.Invite {
		t.Errorf("who of the new member = %+v (found %v)", w, ok)
	}
	if a.Kick("nobody") {
		t.Error("kicking a name that never joined succeeded")
	}
	var saved state
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Roots) != rootsKept || saved.Roots[0] != hexDigest(digestOf("r3")) || saved.Roots[len(saved.Roots)-1] != goodRoot {
		t.Errorf("kept roots = %v, want the last %d with the new root last", saved.Roots, rootsKept)
	}
	if len(saved.Issued) != issuedKept || saved.Issued[0] != "i0000" {
		t.Errorf("kept issued ids = %d starting %q, want %d starting i0000", len(saved.Issued), saved.Issued[0], issuedKept)
	}

	// A file of another shape (or a scalar) is a fresh start, never a crash.
	for name, content := range map[string]string{
		"another version": `{"v":2,"members":[{"name":"m","cred":"` + goodCred + `"}]}`,
		"a scalar":        `["a"]`,
	} {
		other := filepath.Join(t.TempDir(), "auth.json")
		if err := os.WriteFile(other, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		b := New("root-cred-xyz", nil, nil, other)
		if got := b.MemberList(); len(got) != 0 {
			t.Errorf("%s: members = %+v, want none", name, got)
		}
	}
}

// TestSaveFailureReachesTheLog: a state file that cannot be written is logged, and the run
// keeps working in memory.
func TestSaveFailureReachesTheLog(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var lines []string
	a := New("root-cred-xyz", nil, func(line string) { lines = append(lines, line) }, filepath.Join(blocker, "data", "auth.json"))
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "could not save ") {
		t.Fatalf("log = %v, want one could-not-save line", lines)
	}
	if w, ok := a.Who("root-cred-xyz"); !ok || !w.Root {
		t.Error("a run whose file cannot be written lost its root")
	}
}

// TestBrowserDigestsAreCapped: only the latest browsers' digests are kept, and a code past its
// five minutes is dropped when the next one is minted.
func TestBrowserDigestsAreCapped(t *testing.T) {
	now, add := newClock()
	a := New("root-cred-xyz", now, nil, "")
	stale := a.LoginCode(nil)
	add(301)
	var first, last string
	for i := 0; i < browsersKept+2; i++ {
		cookie, ok := a.Redeem(a.LoginCode(nil))
		if !ok {
			t.Fatal("a login code did not redeem")
		}
		if i == 0 {
			first = cookie
		}
		last = cookie
	}
	if _, ok := a.Redeem(stale); ok {
		t.Error("a login code past its five minutes redeemed")
	}
	if _, ok := a.Who(first); ok {
		t.Error("an evicted browser digest still verifies")
	}
	if w, ok := a.Who(last); !ok || !w.Root {
		t.Error("the latest browser digest does not verify")
	}
}

// TestRedactLeavesOtherValues: redaction walks structures and touches strings only.
func TestRedactLeavesOtherValues(t *testing.T) {
	a := New("root-cred-xyz", nil, nil, "")
	body, err := jsfmt.DecodeValue([]byte(`{"n":42,"b":true,"nil":null,"s":"plain text","arr":[7]}`))
	if err != nil {
		t.Fatal(err)
	}
	out := a.Redact(body)
	for _, key := range []string{"n", "b", "nil", "s"} {
		want := fieldOf(t, body, key).EncodeJSON()
		if got := fieldOf(t, out, key).EncodeJSON(); got != want {
			t.Errorf("redact(%s) = %s, want %s", key, got, want)
		}
	}
	if got := fieldOf(t, out, "arr").EncodeJSON(); got != `[7]` {
		t.Errorf("redact(arr) = %s, want [7]", got)
	}
}

// fieldOf finds an object field's value by key, for the redaction tests.
func fieldOf(t *testing.T, v *jsfmt.Value, key string) *jsfmt.Value {
	t.Helper()
	for i, k := range v.Keys() {
		if k == key {
			return v.Fields()[i]
		}
	}
	t.Fatalf("no field %s in %s", key, v.EncodeJSON())
	return nil
}

// The state file's bytes are JSON.stringify's: an invite's free-text fields keep `<`, `>`,
// `&` and the two line separators raw, never as escapes.
func TestSaveKeepsJSONStringifyBytes(t *testing.T) {
	now, _ := clockAt(1_700_000_000_000)
	file := filepath.Join(t.TempDir(), "auth.json")
	a := New("root-cred-xyz", now, nil, file)
	a.Create(InviteOptions{By: "a<b>&c\u2028d", Channel: "shop", Description: "e>f&g\u2029h"})
	disk, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"\"by\":\"a<b>&c\u2028d\"", "\"description\":\"e>f&g\u2029h\""} {
		if !bytes.Contains(disk, []byte(raw)) {
			t.Errorf("the state file lacks %q: %s", raw, disk)
		}
	}
	for _, escaped := range []string{"\\u003c", "\\u003e", "\\u0026", "\\u2028", "\\u2029"} {
		if bytes.Contains(disk, []byte(escaped)) {
			t.Errorf("the state file holds the escape %s: %s", escaped, disk)
		}
	}
}
