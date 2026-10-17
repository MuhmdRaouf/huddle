package radartab

// The mount's own guards, unit-proven: the token a mount answers, the prefix strip that keeps
// the escapes, the not-found outside the prefix, and the proxy's credential rules toward a
// recorded owner.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/huddle/auth"
	"github.com/MuhmdRaouf/huddle/internal/radar/server"
)

// coverURL is a url.Parse that fails the test.
func coverURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.ParseRequestURI(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return parsed
}

// TestAnswersTokenNeedsTheMountsOwnToken: a mount with no token answers no one, and one with a
// token answers only the word it published in its lock record.
func TestAnswersTokenNeedsTheMountsOwnToken(t *testing.T) {
	if (&Tab{}).AnswersToken("t1") {
		t.Error("a tokenless mount answered a credential")
	}
	if (&Tab{}).AnswersToken("") {
		t.Error("a tokenless mount answered the empty credential")
	}
	tab := &Tab{token: "t1"}
	if tab.AnswersToken("") {
		t.Error("the mount answered the empty credential")
	}
	if tab.AnswersToken("t2") {
		t.Error("the mount answered a stranger's credential")
	}
	if !tab.AnswersToken("t1") {
		t.Error("the mount refused its own token")
	}
}

// TestStripPrefixKeepsEscapesAndRefusesOtherPaths: the bare prefix becomes /, an escaped id
// keeps its escapes byte for byte, and a path outside the prefix is not ours to serve.
func TestStripPrefixKeepsEscapesAndRefusesOtherPaths(t *testing.T) {
	root, ok := stripPrefix(coverURL(t, Prefix))
	if !ok || root.Path != "/" || root.RawPath != "" {
		t.Errorf("stripPrefix(%s) = (%+v, %v), want /", Prefix, root, ok)
	}
	kept, ok := stripPrefix(coverURL(t, Prefix+"/x%2Fy"))
	if !ok || kept.RawPath != "/x%2Fy" || kept.Path != "/x/y" {
		t.Errorf("stripPrefix of an escaped id = (%+v, %v), want the escape kept", kept, ok)
	}
	for _, refused := range []string{Prefix + "x", "/other"} {
		if got, ok := stripPrefix(coverURL(t, refused)); ok || got != nil {
			t.Errorf("stripPrefix(%s) = (%+v, %v), want a refusal", refused, got, ok)
		}
	}
}

// TestServeRadarAnswersNotFoundOutsideItsPrefix: the tab's own router answers nothing for a path
// that never carried the prefix.
func TestServeRadarAnswersNotFoundOutsideItsPrefix(t *testing.T) {
	env := tempEnv(t)
	tab, _, _ := mountMux(t, env, MountOptions{IntervalMs: quietMs, InsightsTickMs: quietMs})
	if !tab.Owns {
		t.Fatal("the mount did not take the lock")
	}
	recorder := httptest.NewRecorder()
	tab.serveRadar(recorder, httptest.NewRequest(http.MethodGet, "/other", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("serveRadar outside the prefix = %d, want 404", recorder.Code)
	}
}

// startHeaderOwner runs a fake owner that answers every path and records each call's path and
// credential; it answers its host:port and a reader for what it took.
func startHeaderOwner(t *testing.T) (host string, took func() ([]string, []string)) {
	t.Helper()
	var mu sync.Mutex
	var paths, tokens []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.EscapedPath())
		tokens = append(tokens, r.Header.Get(auth.Header))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)
	return strings.TrimPrefix(server.URL, "http://"), func() ([]string, []string) {
		mu.Lock()
		defer mu.Unlock()
		return append([]string{}, paths...), append([]string{}, tokens...)
	}
}

// TestTheProxyDropsTheCallersCredentialWithoutAToken: a mount with no recorded token forwards
// the caller's own credential to no one, and the prefix is stripped — /api/radar arrives as a
// root-relative path.
func TestTheProxyDropsTheCallersCredentialWithoutAToken(t *testing.T) {
	host, took := startHeaderOwner(t)
	proxy := proxyTo(host, false, "")
	inbound := httptest.NewRequest(http.MethodGet, "http://huddle.example"+Prefix+"/api/summary", nil)
	inbound.Header.Set(auth.Header, "caller-secret")
	recorder := httptest.NewRecorder()
	proxy.ServeHTTP(recorder, inbound)
	paths, tokens := took()
	if len(paths) != 1 {
		t.Fatalf("the owner took %d calls (%v), want 1", len(paths), paths)
	}
	if paths[0] != "/api/summary" {
		t.Errorf("the owner saw %q, want /api/summary", paths[0])
	}
	if tokens[0] != "" {
		t.Errorf("the owner saw credential %q, want none", tokens[0])
	}
}

// TestTheProxySwapsItForTheRecordedToken: the mount presents the owner's own published token —
// not the browser's — and keeps the prefix only for an owner that serves the prefix itself.
func TestTheProxySwapsItForTheRecordedToken(t *testing.T) {
	host, took := startHeaderOwner(t)
	proxy := proxyTo(host, false, "t1")
	recorder := httptest.NewRecorder()
	proxy.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://huddle.example"+Prefix, nil))
	paths, tokens := took()
	if len(paths) != 1 || paths[0] != "/" || tokens[0] != "t1" {
		t.Fatalf("the bare prefix arrived as %v with %v, want / with t1", paths, tokens)
	}
	kept := proxyTo(host, true, "t1")
	recorder = httptest.NewRecorder()
	kept.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://huddle.example"+Prefix+"/x", nil))
	paths, tokens = took()
	if len(paths) != 2 || paths[1] != Prefix+"/x" || tokens[1] != "t1" {
		t.Fatalf("a prefix-keeping proxy delivered %v with %v, want %s with t1", paths, tokens, Prefix+"/x")
	}
}

// TestAProxyingTabReadsOnlyAProperAnswerFromItsOwner: a read through a proxying mount is false
// when the owner refuses, answers text instead of JSON, or answers nothing at all — and a
// session the owner does not name carries no facts.
func TestAProxyingTabReadsOnlyAProperAnswerFromItsOwner(t *testing.T) {
	refuse, body := true, ""
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if refuse {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(owner.Close)
	ctx := context.Background()
	tab := &Tab{target: strings.TrimPrefix(owner.URL, "http://"), client: owner.Client()}
	if _, ok := tab.RadarAnswer(ctx, "/api/sessions/s1"); ok {
		t.Error("the owner's refusal answered a body")
	}
	refuse, body = false, "not json"
	if _, ok := tab.RadarAnswer(ctx, "/api/sessions/s1"); ok {
		t.Error("the owner's text answered as JSON")
	}
	body = `{}`
	if _, ok := tab.RadarAnswer(ctx, "/api/sessions/s1"); !ok {
		t.Fatal("the owner's empty object did not answer")
	}
	if facts := tab.FactsOf(ctx, "s1"); facts.CostUSD != nil || facts.Provider != nil {
		t.Errorf("the facts of a session the owner does not name = %+v, want both nil", facts)
	}
	gone := &Tab{target: "127.0.0.1:1", client: &http.Client{Timeout: probeTimeout}}
	if _, ok := gone.RadarAnswer(ctx, "/api/sessions/s1"); ok {
		t.Error("a dead owner answered")
	}
}

// TestReleaseLockKeepsARecordThatIsNotOurs: a stop whose record is already gone, or one a
// successor already rewrote, removes nothing.
func TestReleaseLockKeepsARecordThatIsNotOurs(t *testing.T) {
	env := tempEnv(t)
	releaseLock(env, 1) // no record at all
	if recorded := server.ReadServerInfo(context.Background(), env); recorded != nil {
		t.Fatalf("server.json = %+v, want none", recorded)
	}
	if ok := server.WriteServerInfo(context.Background(), env, server.ServerInfo{
		Pid:  1 << 20, // a pid nothing here owns
		Port: 1,
		URL:  "http://127.0.0.1:1",
	}); !ok {
		t.Fatal("write a stranger's record")
	}
	releaseLock(env, 1)
	recorded := server.ReadServerInfo(context.Background(), env)
	if recorded == nil || recorded.Pid != 1<<20 {
		t.Fatalf("server.json = %+v, want the stranger's record kept", recorded)
	}
}

// TestARecordWhoseOwnerAnswersNothingIsTakenOver: a record naming a live pid whose port answers
// no health names no owner — the mount takes the lock itself.
func TestARecordWhoseOwnerAnswersNothingIsTakenOver(t *testing.T) {
	env := tempEnv(t)
	if ok := server.WriteServerInfo(context.Background(), env, server.ServerInfo{
		Pid:  int64(os.Getpid()),
		Port: 1, // nothing listens here; the probe must fail
		URL:  "http://127.0.0.1:1",
	}); !ok {
		t.Fatal("write the record")
	}
	tab, _, _ := mountMux(t, env, MountOptions{IntervalMs: quietMs, InsightsTickMs: quietMs})
	if !tab.Owns {
		t.Fatal("a record whose owner answers nothing must not keep the lock")
	}
}
